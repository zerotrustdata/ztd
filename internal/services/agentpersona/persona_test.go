// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package agentpersona

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func mustRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

// TestRegistry_TiersFollowOwnerDirection pins every persona to its model tier:
// Lite runs Triage, the Interrogator, and the Tribunal members; Assistant runs
// Dash and Marshal; Primary runs Sage and the Auditor. No tier runs an anonymous
// prompt.
func TestRegistry_TiersFollowOwnerDirection(t *testing.T) {
	want := map[string]models.InferenceModelRole{
		IDSage: models.InferenceModelRolePrimary, IDAuditor: models.InferenceModelRolePrimary,
		IDDash: models.InferenceModelRoleAssistant, IDMarshal: models.InferenceModelRoleAssistant,
		IDTriage: models.InferenceModelRoleLite, IDInterrogator: models.InferenceModelRoleLite, IDAxiom: models.InferenceModelRoleLite, IDConcord: models.InferenceModelRoleLite,
		IDVariance: models.InferenceModelRoleLite, IDPragma: models.InferenceModelRoleLite, IDNemesis: models.InferenceModelRoleLite,
	}
	all := mustRegistry(t).All()
	if len(all) != len(want) {
		t.Fatalf("registry has %d personas, want %d", len(all), len(want))
	}
	seen := map[string]bool{}
	for _, p := range all {
		if seen[p.ID] {
			t.Fatalf("duplicate persona ID %q", p.ID)
		}
		seen[p.ID] = true
		if tier, ok := want[p.ID]; !ok || p.Tier != tier {
			t.Errorf("persona %q tier = %v, want %v (registered %v)", p.ID, p.Tier, tier, ok)
		}
	}
}

func TestPersona_SystemPromptRendersEverySectionAfterSecurityPrefix(t *testing.T) {
	for _, p := range mustRegistry(t).All() {
		prompt := p.SystemPrompt()
		if !strings.HasPrefix(prompt, constants.AgentSecurityPrompt) {
			t.Errorf("%s: system prompt does not start with the security prefix", p.ID)
		}
		if !strings.Contains(prompt, "Pass tokens verbatim in tool arguments; the backend resolves them at execution.") {
			t.Errorf("%s: system prompt is missing the protected-token passthrough rule", p.ID)
		}
		last := len(constants.AgentSecurityPrompt)
		tags := []string{"role", "identity", "purpose", "autonomy"}
		if p.OutputContract != "" {
			tags = []string{"role", "output_contract", "identity", "purpose", "autonomy"}
		}
		for _, tag := range tags {
			open := strings.Index(prompt[last:], "<"+tag+">\n")
			if open < 0 {
				t.Errorf("%s: section <%s> missing or out of order", p.ID, tag)
				break
			}
			last += open
		}
		if !strings.Contains(p.Identity, "You are "+p.DisplayName) && !strings.Contains(p.Identity, "You are the "+p.DisplayName) {
			t.Errorf("%s: identity does not name the persona %q", p.ID, p.DisplayName)
		}
		if !strings.HasSuffix(prompt, "</autonomy>\n\n") {
			t.Errorf("%s: system prompt must end with the autonomy section and a blank line", p.ID)
		}
	}
}

func TestTribunalVotePersonasHaveIndependentDigestBoundRenderings(t *testing.T) {
	registry := mustRegistry(t)
	for _, id := range []string{IDAxiom, IDConcord, IDVariance, IDPragma, IDNemesis} {
		p, err := registry.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		prompt := p.VoteSystemPrompt()
		if !strings.HasPrefix(prompt, constants.AgentSecurityPrompt) || !strings.Contains(prompt, "<identity>") || !strings.Contains(prompt, "<purpose>") || !strings.Contains(prompt, `"decision":"approve"|"deny"`) {
			t.Errorf("%s vote prompt is incomplete: %s", id, prompt)
		}
		if p.VoteDigest() == "" || p.VoteDigest() == p.Digest() {
			t.Errorf("%s vote digest must be nonempty and distinct from authoring digest", id)
		}
		changed := p
		changed.VotePurpose += " A changed vote purpose."
		if changed.VoteDigest() == p.VoteDigest() {
			t.Errorf("%s vote digest did not change with the vote rendering", id)
		}
		if strings.Contains(prompt, "Author one run_shell_command") || strings.Contains(prompt, "propose an argument set") {
			t.Errorf("%s vote prompt contains authoring instructions", id)
		}
	}
	nemesis, err := registry.Get(IDNemesis)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nemesis.VoteSystemPrompt(), nemesis.Identity) || !strings.Contains(nemesis.Identity, "always cunning, not always aggressive") {
		t.Error("Nemesis vote rendering must preserve its identity as-is")
	}
	if strings.Contains(nemesis.OutputContract, "honest argument set") || !strings.Contains(nemesis.VotePurpose, "calibrated attacker") {
		t.Error("Nemesis must author and vote as the adversarial seat")
	}
	for _, id := range []string{IDAxiom, IDConcord, IDVariance, IDPragma} {
		p, err := registry.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.OutputContract, "honest argument set") || strings.Contains(p.VotePurpose, "attacker") {
			t.Errorf("%s must author and vote as an honest member", id)
		}
	}
}

// TestPersona_TriageNeverInterrogates keeps Triage a classifier: it runs once
// at ingest, and the Interrogator owns the I1-I4 questions in the loop.
func TestPersona_TriageNeverInterrogates(t *testing.T) {
	r := mustRegistry(t)
	triagePersona, err := r.Get(IDTriage)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(triagePersona.SystemPrompt(), "<interrogation>") {
		t.Error("triage must not carry the interrogation protocol")
	}
	for _, id := range []string{IDSage, IDDash} {
		reasoner, err := r.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(reasoner.SystemPrompt(), "<interrogation>") || !strings.Contains(reasoner.SystemPrompt(), clarificationSignal) {
			t.Errorf("%s must signal for clarification, never ask the I1-I4 questions itself", id)
		}
	}
	interrogatorPersona, err := r.Get(IDInterrogator)
	if err != nil {
		t.Fatal(err)
	}
	prompt := interrogatorPersona.SystemPrompt()
	for _, rule := range []string{"I1 |", "I2 |", "I3 |", "I4 |", "<interrogation>\n1. Question one?"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("interrogator is missing %q", rule)
		}
	}
}

func TestTriageDistinguishesReadOnlyKnowledgeRetrievalFromAction(t *testing.T) {
	triage, err := mustRegistry(t).Get(IDTriage)
	if err != nil {
		t.Fatal(err)
	}
	prompt := triage.SystemPrompt()
	for _, phrase := range []string{
		"Read-only lookup and knowledge retrieval stay information, even when tools are needed.",
		"asks to change state or to run a command/action",
	} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("Triage prompt is missing %q", phrase)
		}
	}
}

func TestPersona_OnlySageAndDashMayBeDeclaredTools(t *testing.T) {
	for _, p := range mustRegistry(t).All() {
		want := ToolAccessNone
		if p.ID == IDSage || p.ID == IDDash {
			want = ToolAccessNative
		}
		if p.Tools != want {
			t.Errorf("%s: tool access = %v, want %v", p.ID, p.Tools, want)
		}
	}
}

func TestPersona_DigestIsStableAndBindsText(t *testing.T) {
	r := mustRegistry(t)
	sagePersona, err := r.Get(IDSage)
	if err != nil {
		t.Fatal(err)
	}
	digest := sagePersona.Digest()
	if len(digest) != 64 || digest != mustRegistry(t).All()[0].Digest() {
		t.Fatalf("digest %q is not a stable SHA-256 hex", digest)
	}
	changed := sagePersona
	changed.Purpose += " "
	changed.Autonomy += "."
	if changed.Digest() == digest {
		t.Fatal("digest did not change when persona text changed")
	}
	digests := map[string]string{}
	for _, p := range r.All() {
		if other, dup := digests[p.Digest()]; dup {
			t.Errorf("%s and %s render the same digest", p.ID, other)
		}
		digests[p.Digest()] = p.ID
	}
}

func TestRegistry_GetUnknownPersonaIsNotFound(t *testing.T) {
	if _, err := mustRegistry(t).Get("lite"); !errors.Is(err, constants.ErrNotFound) {
		t.Fatalf("Get(lite) error = %v, want ErrNotFound: no anonymous Lite persona exists", err)
	}
}

func TestPersona_ValidateRejectsMissingSectionOrTier(t *testing.T) {
	base := triage()
	noIdentity := base
	noIdentity.Identity = ""
	noTier := base
	noTier.Tier = models.InferenceModelRoleUnspecified
	for name, p := range map[string]Persona{"identity": noIdentity, "tier": noTier} {
		if err := p.validate(); !errors.Is(err, constants.ErrPersonaInvalid) {
			t.Errorf("missing %s: validate() = %v, want ErrPersonaInvalid", name, err)
		}
	}
}

// TestPersona_DashBoundedDelegateWorker asserts Dash's role as Sage's bounded
// worker: single-step tool execution, clarification signaling, and handing off
// complex multi-step reasoning to Sage.
func TestPersona_DashBoundedDelegateWorker(t *testing.T) {
	dashPersona, err := mustRegistry(t).Get(IDDash)
	require.NoError(t, err)

	assert.Equal(t, IDDash, dashPersona.ID)
	assert.Equal(t, "Dash", dashPersona.DisplayName)
	assert.Equal(t, models.InferenceModelRoleAssistant, dashPersona.Tier)
	assert.Equal(t, ToolAccessNative, dashPersona.Tools)
	assert.Equal(t, "responder", dashPersona.Role)

	prompt := dashPersona.SystemPrompt()
	assert.Contains(t, prompt, clarificationSignal, "Dash must include the clarification signal")
	assert.NotContains(t, prompt, "<interrogation>", "Dash must never ask interrogation questions directly")

	expectedRules := []string{
		"D1 | Answer directly in 1-3 sentences",
		"D2 | Prefer one well-aimed tool call over a chain. If the request requires multi-step planning or deep reasoning beyond your lane, explain that it needs Sage",
		"D3 | If the request lacks the detail for a precise answer or tool call, signal for clarification instead of guessing.",
		"D4 | Base every response on evidence from context or tool output",
	}
	for _, rule := range expectedRules {
		assert.Contains(t, prompt, rule)
	}

	assert.Contains(t, dashPersona.Purpose, "Resolve straightforward requests with minimal latency: answer directly, or make one targeted tool call. Say when a multi-step or deeply ambiguous request needs Sage.")
	assert.Contains(t, dashPersona.Autonomy, "Own requests in your lane without deferral or hedging.")
}

// TestPersona_InterrogatorClarificationSpecialist asserts that Interrogator is
// the Lite-tier clarification questioner: strictly 3 binary questions, zero tools,
// and full adherence to the I1-I4 interrogation protocol.
func TestPersona_InterrogatorClarificationSpecialist(t *testing.T) {
	interrogatorPersona, err := mustRegistry(t).Get(IDInterrogator)
	require.NoError(t, err)

	assert.Equal(t, IDInterrogator, interrogatorPersona.ID)
	assert.Equal(t, "Interrogator", interrogatorPersona.DisplayName)
	assert.Equal(t, models.InferenceModelRoleLite, interrogatorPersona.Tier)
	assert.Equal(t, ToolAccessNone, interrogatorPersona.Tools)
	assert.Equal(t, "interrogator", interrogatorPersona.Role)

	prompt := interrogatorPersona.SystemPrompt()
	assert.NotContains(t, prompt, clarificationSignal, "Interrogator asks questions directly and must not emit a clarification signal")

	assert.Contains(t, interrogatorPersona.OutputContract, "I4 | The interrogation block MUST be your entire response, with no other text or tool calls.")
	assert.Contains(t, interrogatorPersona.OutputContract, "<interrogation>\n1. Question one?\n2. Question two?\n3. Question three?\n</interrogation>")

	assert.Contains(t, interrogatorPersona.Identity, "I1 | Issue exactly three targeted questions, each strictly binary (YES/NO, never multiple-choice or open-ended), chosen to maximize information gain.")
	assert.Contains(t, interrogatorPersona.Identity, "I2 | If the request contradicts the stated goal, make the contradiction clear in the questions.")
	assert.Contains(t, interrogatorPersona.Identity, "I3 | Do not act until you can fulfill the request with high confidence.")

	assert.Contains(t, interrogatorPersona.Autonomy, "You ask; you never act or call tools.")
	assert.Contains(t, interrogatorPersona.Purpose, "When missing context prevents a safe, precise answer or action, ask the questions that resolve it.")
}

type protocolAgentMetadata struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Icon        string `json:"icon"`
}

type protocolAgentModel struct {
	Comment     string                `json:"_comment"`
	Name        string                `json:"name"`
	Metadata    protocolAgentMetadata `json:"metadata"`
	Description string                `json:"description"`
	Request     map[string]any        `json:"request"`
	Result      map[string]any        `json:"result"`
}

// TestProtocolAgentModels_JSONValidityAndCommonSchema asserts that every canonical
// agent model JSON file in protocol/models/agents/ is valid JSON and adheres to the
// common protocol model schema.
func TestProtocolAgentModels_JSONValidityAndCommonSchema(t *testing.T) {
	cases := []struct {
		file          string
		expectedName  string
		expectedID    string
		expectedNameD string
		expectedIcon  string
		toolsExpected bool
	}{
		{
			file:          "assistant.json",
			expectedName:  "AssistantAI",
			expectedID:    "dash",
			expectedNameD: "Dash",
			expectedIcon:  "bolt",
			toolsExpected: true,
		},
		{
			file:          "auditor.json",
			expectedName:  "Auditor",
			expectedID:    "auditor",
			expectedNameD: "Auditor",
			expectedIcon:  "fact_check",
			toolsExpected: false,
		},
		{
			file:          "interrogator.json",
			expectedName:  "Interrogator",
			expectedID:    "interrogator",
			expectedNameD: "Interrogator",
			expectedIcon:  "contact_support",
			toolsExpected: false,
		},
		{
			file:          "lite.json",
			expectedName:  "LiteAI",
			expectedID:    "lite",
			expectedNameD: "Lite",
			expectedIcon:  "speed",
			toolsExpected: false,
		},
		{
			file:          "primary.json",
			expectedName:  "PrimaryAI",
			expectedID:    "sage",
			expectedNameD: "Sage",
			expectedIcon:  "psychology",
			toolsExpected: true,
		},
		{
			file:          "title_generator.json",
			expectedName:  "CaseTitleGenerator",
			expectedID:    "scribe",
			expectedNameD: "Scribe",
			expectedIcon:  "title",
			toolsExpected: false,
		},
		{
			file:          "triage.json",
			expectedName:  "Triage",
			expectedID:    "triage",
			expectedNameD: "Triage",
			expectedIcon:  "manage_search",
			toolsExpected: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "protocol", "models", "agents", tc.file)
			data, err := os.ReadFile(path)
			require.NoError(t, err, "failed to read protocol agent model %s", tc.file)
			require.True(t, json.Valid(data), "%s is not valid JSON", tc.file)

			var model protocolAgentModel
			err = json.Unmarshal(data, &model)
			require.NoError(t, err, "failed to unmarshal %s", tc.file)

			assert.Equal(t, tc.expectedName, model.Name, "model name mismatch in %s", tc.file)
			assert.Equal(t, tc.expectedID, model.Metadata.ID, "metadata.id mismatch in %s", tc.file)
			assert.Equal(t, tc.expectedNameD, model.Metadata.DisplayName, "metadata.display_name mismatch in %s", tc.file)
			assert.Equal(t, tc.expectedIcon, model.Metadata.Icon, "metadata.icon mismatch in %s", tc.file)
			assert.NotEmpty(t, model.Description, "description missing in %s", tc.file)
			assert.NotEmpty(t, model.Request, "request schema missing in %s", tc.file)
			assert.NotEmpty(t, model.Result, "result schema missing in %s", tc.file)

			if tc.toolsExpected {
				assert.Contains(t, model.Result, "tool_calls", "%s expected to declare tool_calls", tc.file)
			} else {
				assert.NotContains(t, model.Result, "tool_calls", "%s must not declare tool_calls", tc.file)
			}
		})
	}
}

// TestProtocolAgentModels_InterrogatorContract verifies the specific schema
// requirements for protocol/models/agents/interrogator.json: missing_context input,
// strictly 3 binary questions output schema, and zero tool declarations.
func TestProtocolAgentModels_InterrogatorContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "protocol", "models", "agents", "interrogator.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var model protocolAgentModel
	require.NoError(t, json.Unmarshal(data, &model))

	assert.Equal(t, "Interrogator", model.Name)
	assert.Equal(t, "interrogator", model.Metadata.ID)
	assert.Equal(t, "Interrogator", model.Metadata.DisplayName)
	assert.Equal(t, "contact_support", model.Metadata.Icon)

	assert.Contains(t, model.Request, "missing_context")
	assert.Contains(t, model.Request, "conversation_history")

	require.Contains(t, model.Result, "questions")
	questions, ok := model.Result["questions"].(map[string]any)
	require.True(t, ok, "questions field must be an object schema")

	assert.Equal(t, "array", questions["type"])
	minItems, ok := questions["minItems"].(float64)
	require.True(t, ok, "minItems must be numeric")
	assert.Equal(t, 3, int(minItems))

	maxItems, ok := questions["maxItems"].(float64)
	require.True(t, ok, "maxItems must be numeric")
	assert.Equal(t, 3, int(maxItems))
	assert.NotContains(t, model.Result, "tool_calls", "interrogator must not declare tool_calls")
}

// TestProtocolAgentModels_LiteContract verifies that lite.json represents the
// shared lightweight model tier without stale metadata or tool declarations.
func TestProtocolAgentModels_LiteContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "protocol", "models", "agents", "lite.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var model protocolAgentModel
	require.NoError(t, json.Unmarshal(data, &model))

	assert.Equal(t, "LiteAI", model.Name)
	assert.NotEqual(t, "AssistantAI", model.Name, "stale AssistantAI name in lite.json")
	assert.Equal(t, "lite", model.Metadata.ID)
	assert.NotEqual(t, "dash", model.Metadata.ID, "stale dash id in lite.json")
	assert.Equal(t, "Lite", model.Metadata.DisplayName)
	assert.Equal(t, "speed", model.Metadata.Icon)

	assert.Contains(t, model.Request, "message")
	assert.Contains(t, model.Request, "role")
	assert.Contains(t, model.Result, "content")
	assert.NotContains(t, model.Result, "tool_calls", "lite tier must not declare tool_calls")
}

// TestProtocolAgentModels_ParityWithGoPersonaRegistry verifies bidirectional parity
// between the canonical JSON models in protocol/models/agents/ and the in-code
// Go agent persona registry.
func TestProtocolAgentModels_ParityWithGoPersonaRegistry(t *testing.T) {
	registry := mustRegistry(t)

	cases := []struct {
		personaID string
		modelFile string
	}{
		{personaID: IDSage, modelFile: "primary.json"},
		{personaID: IDDash, modelFile: "assistant.json"},
		{personaID: IDInterrogator, modelFile: "interrogator.json"},
		{personaID: IDTriage, modelFile: "triage.json"},
		{personaID: IDAuditor, modelFile: "auditor.json"},
	}

	for _, tc := range cases {
		t.Run(tc.personaID, func(t *testing.T) {
			p, err := registry.Get(tc.personaID)
			require.NoError(t, err, "persona %s not found in registry", tc.personaID)

			path := filepath.Join("..", "..", "..", "protocol", "models", "agents", tc.modelFile)
			data, err := os.ReadFile(path)
			require.NoError(t, err, "failed to read %s", tc.modelFile)

			var model protocolAgentModel
			require.NoError(t, json.Unmarshal(data, &model))

			assert.Equal(t, p.ID, model.Metadata.ID, "persona ID and metadata.id mismatch for %s", tc.personaID)
			assert.Equal(t, p.DisplayName, model.Metadata.DisplayName, "persona DisplayName and metadata.display_name mismatch for %s", tc.personaID)

			if p.Tools == ToolAccessNative {
				assert.Contains(t, model.Result, "tool_calls", "persona %s has native tools; model %s must declare tool_calls", tc.personaID, tc.modelFile)
			} else {
				assert.NotContains(t, model.Result, "tool_calls", "persona %s has ToolAccessNone; model %s must not declare tool_calls", tc.personaID, tc.modelFile)
			}
		})
	}
}

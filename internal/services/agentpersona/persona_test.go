// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package agentpersona

import (
	"errors"
	"strings"
	"testing"

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
// Lite runs Triage and the Tribunal members, Assistant runs Dash and Marshal,
// Primary runs Sage and the Auditor. No tier runs an anonymous prompt.
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

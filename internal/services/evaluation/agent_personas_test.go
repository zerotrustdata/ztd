// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/agentpersona"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func testCatalogPersonas(t *testing.T) []*evalv1.AgentPersonaBinding {
	t.Helper()
	bindings, err := catalogPersonaBindings()
	require.NoError(t, err)
	return bindings
}

func TestCampaignRolePersona_EveryScoredRoleRunsANamedPersona(t *testing.T) {
	bindings := testCatalogPersonas(t)
	for _, id := range []string{agentpersona.IDAxiom, agentpersona.IDConcord, agentpersona.IDVariance, agentpersona.IDPragma, agentpersona.IDNemesis} {
		binding := personaBinding(bindings, id)
		require.NotNil(t, binding)
		assert.NotEmpty(t, binding.GetVoteDigest(), "vote prompt digest for %s", id)
	}
	for role, want := range map[evalv1.ModelCampaignRole]string{
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:   agentpersona.IDSage,
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT: agentpersona.IDDash,
		evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:      agentpersona.IDTriage,
	} {
		persona, err := frozenRolePersona(bindings, role)
		require.NoError(t, err)
		assert.Equal(t, want, persona.ID, role.String())
	}
	_, err := frozenRolePersona(bindings, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED)
	assert.ErrorIs(t, err, constants.ErrInferenceRoleInvalid)
}

func TestFrozenRolePersona_FailsClosedOnMissingOrStaleBinding(t *testing.T) {
	_, err := frozenRolePersona(nil, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE)
	require.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "a catalog frozen before personas cannot execute")

	stale := testCatalogPersonas(t)
	for _, binding := range stale {
		if binding.GetPersonaId() == agentpersona.IDSage {
			binding.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
		}
	}
	_, err = frozenRolePersona(stale, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY)
	require.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "an edited persona needs a fresh campaign")

	staleVote := testCatalogPersonas(t)
	for _, binding := range staleVote {
		if binding.GetPersonaId() == agentpersona.IDAxiom {
			binding.VoteDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		}
	}
	_, err = frozenPersona(staleVote, agentpersona.IDAxiom)
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "an edited vote rendering needs a fresh catalog binding")
}

func TestBuildScenarioCatalog_FreezesEveryPersonaIntoTheDigest(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	assert.True(t, proto.Equal(&evalv1.EvaluationScenarioCatalog{Personas: testCatalogPersonas(t)}, &evalv1.EvaluationScenarioCatalog{Personas: catalog.GetPersonas()}))

	tampered := proto.Clone(catalog).(*evalv1.EvaluationScenarioCatalog)
	tampered.Personas[0].Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	assert.ErrorIs(t, ValidateScenarioCatalogDigest(tampered), constants.ErrChecksumMismatch)
}

func personaTrace(t *testing.T, role evalv1.ModelCampaignRole, label string) *AgentTrace {
	t.Helper()
	persona, err := frozenRolePersona(testCatalogPersonas(t), role)
	require.NoError(t, err)
	system := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
	return &AgentTrace{
		SchemaVersion:  agentTraceSchemaVersion,
		DesignatedRole: label,
		Persona:        &AgentTracePersona{ID: persona.ID, Version: persona.Version, Digest: persona.Digest()},
		Input:          &AgentTraceInput{Messages: []AgentTraceMessage{{Role: system, Text: persona.SystemPrompt() + "Operator ID: data-1\n"}}},
	}
}

func TestVerifyTracePersona_BindsFrozenPersonaToTheDispatchedSystemPrompt(t *testing.T) {
	bindings := testCatalogPersonas(t)
	trace := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, "lite")
	require.NoError(t, verifyTracePersona(trace, bindings))

	t.Run("relabelled persona", func(t *testing.T) {
		relabelled := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, "primary")
		assert.ErrorIs(t, verifyTracePersona(relabelled, bindings), constants.ErrPersonaDigestMismatch)
	})
	t.Run("edited system prompt", func(t *testing.T) {
		edited := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, "lite")
		edited.Input.Messages[0].Text = "You are Quill." + edited.Input.Messages[0].Text
		assert.ErrorIs(t, verifyTracePersona(edited, bindings), constants.ErrPersonaDigestMismatch)
	})
	t.Run("missing persona", func(t *testing.T) {
		missing := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, "lite")
		missing.Persona = nil
		assert.ErrorIs(t, tracePersonaError(AssignmentExecutionRequest{Binding: CampaignExecutionBinding{Personas: bindings}}, missing), constants.ErrMissingRequiredField)
	})
	t.Run("frozen binding differs", func(t *testing.T) {
		assert.ErrorIs(t, verifyTracePersona(trace, nil), constants.ErrPersonaDigestMismatch)
	})
}

func TestValidateAgentTraceRules_HistoricalTraceCannotCarryPersona(t *testing.T) {
	trace := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, "primary")
	trace.SchemaVersion = agentTraceSchemaVersionV10
	trace.EvidenceDigest = "digest"
	assert.ErrorIs(t, validateAgentTraceRules(trace), constants.ErrEvidenceArtifactMalformed)
}

func TestMaterializeSuite_RejectsStaleOrMissingPersonas(t *testing.T) {
	def := customSuite(t, "persona-suite", "1.0.0", "instruction-classify-severity")
	_, _, err := MaterializeSuite(def)
	require.NoError(t, err)
	for _, persona := range def.Personas {
		if slices.Contains([]string{agentpersona.IDAxiom, agentpersona.IDConcord, agentpersona.IDVariance, agentpersona.IDPragma, agentpersona.IDNemesis}, persona.ID) {
			assert.NotEmpty(t, persona.VoteDigest, "suite persona %s must freeze its vote rendering", persona.ID)
		}
	}

	missing := def
	missing.Personas = nil
	_, _, err = MaterializeSuite(missing)
	require.ErrorIs(t, err, constants.ErrPersonaDigestMismatch)

	stale := def
	stale.Personas = append([]SuitePersona(nil), def.Personas...)
	stale.Personas[0].Version = "0.9.0"
	_, _, err = MaterializeSuite(stale)
	require.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "a persona edit requires re-exporting the suite under a new version")

	staleVote := def
	staleVote.Personas = append([]SuitePersona(nil), def.Personas...)
	for i := range staleVote.Personas {
		if staleVote.Personas[i].ID == agentpersona.IDNemesis {
			staleVote.Personas[i].VoteDigest = "stale"
		}
	}
	_, _, err = MaterializeSuite(staleVote)
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "a vote-rendering edit requires re-exporting the suite")
}

// builtinSuitePersonaPins records the persona set each built-in suite version
// was released with. A persona edit fails this test until every built-in suite
// is bumped and its new version is pinned here; never re-pin an old version.
var builtinSuitePersonaPins = map[string]string{
	DefaultSuiteID + "@1.27.0":        "52b0de054a1d2ccad403d4afe40ba7f8c96807c29a60d6aea5bde43cd933b599",
	SmokeSuiteID + "@1.26.0":          "52b0de054a1d2ccad403d4afe40ba7f8c96807c29a60d6aea5bde43cd933b599",
	InvestigationsSuiteID + "@1.25.0": "52b0de054a1d2ccad403d4afe40ba7f8c96807c29a60d6aea5bde43cd933b599",
	DefaultSuiteID + "@1.28.0":        "56a37a1c486c07336c317ca5ca5cb8f5331d25b2f96421ac732118c64c592f46",
	SmokeSuiteID + "@1.27.0":          "56a37a1c486c07336c317ca5ca5cb8f5331d25b2f96421ac732118c64c592f46",
	InvestigationsSuiteID + "@1.26.0": "56a37a1c486c07336c317ca5ca5cb8f5331d25b2f96421ac732118c64c592f46",
}

func TestBuiltinSuites_PersonaChangeRequiresSuiteVersionBump(t *testing.T) {
	personas, err := currentSuitePersonas()
	require.NoError(t, err)
	body, err := json.Marshal(personas)
	require.NoError(t, err)
	digest := models.SHA256Hex(body)
	for _, id := range BuiltinSuiteIDs() {
		def, _, err := BuiltinScenarioSuite(id)
		require.NoError(t, err)
		ref := def.ID + "@" + def.Version
		assert.Equal(t, builtinSuitePersonaPins[ref], digest, "%s: personas changed; bump the suite version and pin the new version", ref)
	}
}

func TestParseClarificationSignal_WholeResponseOnly(t *testing.T) {
	missing, ok := ParseClarificationSignal(" <clarification_needed> the target host </clarification_needed>\n")
	assert.True(t, ok)
	assert.Equal(t, "the target host", missing)
	for _, text := range []string{
		"I need more detail. <clarification_needed>the host</clarification_needed>",
		"<interrogation>\n1. Is it up?\n2. Is it down?\n3. Is it slow?\n</interrogation>",
		"",
	} {
		_, ok := ParseClarificationSignal(text)
		assert.False(t, ok, "%q is not a clarification signal: the reasoner never asks the questions itself", text)
	}
}

func TestTracePersonaError_BindsInterrogatorCallToFrozenPersona(t *testing.T) {
	bindings := testCatalogPersonas(t)
	interrogator, err := frozenPersona(bindings, agentpersona.IDInterrogator)
	require.NoError(t, err)
	system := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
	trace := personaTrace(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, "primary")
	trace.ModelCalls = []AgentModelCall{{ProviderAttemptID: "ask-1", Purpose: agentModelCallPurposeInterrogation, Persona: agentpersona.IDInterrogator, Input: []AgentTraceMessage{{Role: system, Text: interrogator.SystemPrompt()}}}}
	req := AssignmentExecutionRequest{Binding: CampaignExecutionBinding{Personas: bindings}}
	require.NoError(t, tracePersonaError(req, trace))

	trace.ModelCalls[0].Input[0].Text += "Ask four questions."
	assert.ErrorIs(t, tracePersonaError(req, trace), constants.ErrPersonaDigestMismatch)
}

// TestGradeHomogeneousScenario_ReasonerMustSignalRatherThanAsk fails a v11
// reasoner that writes the interrogation itself and passes the same block
// when the Interrogator asked it.
func TestGradeHomogeneousScenario_ReasonerMustSignalRatherThanAsk(t *testing.T) {
	block := "<interrogation>\n1. Is the host reachable?\n2. Is the service running?\n3. Did it fail after the deploy?\n</interrogation>"
	grade := func(trace *AgentTrace) *evalv1.DeterministicGrade {
		req := verifiedGradingRequest(trace, nil)
		req.ScenarioGold.ContentCheck = &ScenarioContentCheck{Interrogation: true}
		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)
		for _, g := range result.DeterministicGrades {
			if g.GetCriterionId() == publicCriterionScenarioContent {
				return g
			}
		}
		t.Fatal("no scenario-content grade")
		return nil
	}

	reasonerAsked := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	reasonerAsked.FinalOutput = block
	got := grade(reasonerAsked)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, got.GetStatus())
	assert.Contains(t, got.GetDetail(), constants.ErrEvaluationInterrogationNotSignalled.Error())

	interrogatorAsked := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	interrogatorAsked.FinalOutput = block
	interrogatorAsked.ModelCalls = append(interrogatorAsked.ModelCalls, AgentModelCall{ProviderAttemptID: "ask-1", TransactionID: "tx-ask-1", Purpose: agentModelCallPurposeInterrogation, Text: block})
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade(interrogatorAsked).GetStatus())
}

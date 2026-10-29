// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
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
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "a catalog frozen before personas cannot execute")

	stale := testCatalogPersonas(t)
	for _, binding := range stale {
		if binding.GetPersonaId() == agentpersona.IDSage {
			binding.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
		}
	}
	_, err = frozenRolePersona(stale, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY)
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "an edited persona needs a fresh campaign")
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
	def := customSuite(t, "persona-suite", "1.0.0", "instruction-exact-format")
	_, _, err := MaterializeSuite(def)
	require.NoError(t, err)

	missing := def
	missing.Personas = nil
	_, _, err = MaterializeSuite(missing)
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch)

	stale := def
	stale.Personas = append([]SuitePersona(nil), def.Personas...)
	stale.Personas[0].Version = "0.9.0"
	_, _, err = MaterializeSuite(stale)
	assert.ErrorIs(t, err, constants.ErrPersonaDigestMismatch, "a persona edit requires re-exporting the suite under a new version")
}

// builtinSuitePersonaPins records the persona set each built-in suite version
// was released with. A persona edit fails this test until every built-in suite
// is bumped and its new version is pinned here; never re-pin an old version.
var builtinSuitePersonaPins = map[string]string{
	DefaultSuiteID + "@" + DefaultSuiteVersion:               "354b3c14aada601d506ec7842509b9d443f338b9900e019d3c792b44a8d3f235",
	SmokeSuiteID + "@" + SmokeSuiteVersion:                   "354b3c14aada601d506ec7842509b9d443f338b9900e019d3c792b44a8d3f235",
	InvestigationsSuiteID + "@" + InvestigationsSuiteVersion: "354b3c14aada601d506ec7842509b9d443f338b9900e019d3c792b44a8d3f235",
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

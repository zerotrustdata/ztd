// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestBuildHomogeneousAssignmentMatrix_MaterializesFullCrossProduct(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	variants := []*evalv1.ModelVariant{
		{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: "qwen3:4b", ModelDigest: repeatHex('a')},
		{VariantId: "gemma3-4b", ProviderClass: "ollama", ServedModelTag: "gemma3:4b", ModelDigest: repeatHex('b')},
	}
	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID:      "smoke-campaign",
		RunID:           "run-1",
		Catalog:         catalog,
		ActivePosture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:        variants,
		RepetitionCount: 1,
		QueuedAt:        time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	assert.Len(t, assignments, 164)
}

func TestBuildHomogeneousAssignmentMatrix_GatesL2DependentScenarios(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	byID := make(map[string]*evalv1.EvaluationScenarioDefinition, len(catalog.GetScenarios()))
	for _, scenario := range catalog.GetScenarios() {
		byID[scenario.GetScenarioId()] = scenario
	}
	assert.True(t, byID["recovery-live-command-error"].GetExcludeWhenL2Enforced())
	assert.True(t, byID["tool-arg-shell-command"].GetExcludeWhenL2Enforced())

	variant := testModelVariant()
	consensus, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID: "posture-gate", RunID: "consensus-run", Catalog: catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
		Variants:      []*evalv1.ModelVariant{variant},
	})
	require.NoError(t, err)
	assert.Len(t, consensus, 78)
	for _, assignment := range consensus {
		assert.NotContains(t, []string{"recovery-live-command-error", "tool-arg-shell-command"}, assignment.GetScenarioId())
	}

	doctrine, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID: "posture-gate", RunID: "doctrine-run", Catalog: catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:      []*evalv1.ModelVariant{variant},
	})
	require.NoError(t, err)
	assert.Len(t, doctrine, 82)
}

func TestBuildHomogeneousAssignmentMatrix_RunsModelByModel(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	variants := []*evalv1.ModelVariant{
		{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: "qwen3:4b", ModelDigest: repeatHex('a')},
		{VariantId: "gemma3-4b", ProviderClass: "ollama", ServedModelTag: "gemma3:4b", ModelDigest: repeatHex('b')},
		{VariantId: "llama3-2-3b", ProviderClass: "ollama", ServedModelTag: "llama3.2:3b", ModelDigest: repeatHex('c')},
	}
	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID:      "model-by-model",
		RunID:           "run-1",
		Catalog:         catalog,
		ActivePosture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:        variants,
		RepetitionCount: 2,
		QueuedAt:        time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)

	// Each model's assignments must form one contiguous block, so the provider
	// loads every model exactly once.
	blocks := make([]string, 0, len(variants))
	for _, assignment := range assignments {
		tag := assignment.GetHomogeneous().GetCandidateVariant().GetServedModelTag()
		if len(blocks) == 0 || blocks[len(blocks)-1] != tag {
			blocks = append(blocks, tag)
		}
	}
	assert.Equal(t, []string{"gemma3:4b", "llama3.2:3b", "qwen3:4b"}, blocks)

	for _, tag := range blocks {
		var scenarioIDs []string
		for _, assignment := range assignments {
			if assignment.GetHomogeneous().GetCandidateVariant().GetServedModelTag() != tag {
				continue
			}
			id := assignment.GetScenarioId()
			if len(scenarioIDs) == 0 || scenarioIDs[len(scenarioIDs)-1] != id {
				scenarioIDs = append(scenarioIDs, id)
			}
		}
		var expected []string
		for _, blueprint := range scenarioBlueprints() {
			expected = append(expected, blueprint.ScenarioID)
		}
		assert.Equal(t, expected, scenarioIDs, tag)
	}

	// The store lists assignments from files named by hash; re-sorting a
	// reversed copy must reproduce the scheduled order exactly.
	reversed := make([]*evalv1.EvaluationAssignment, len(assignments))
	for i, assignment := range assignments {
		reversed[len(assignments)-1-i] = assignment
	}
	sortAssignmentsDeterministic(reversed, catalog)
	assert.Equal(t, assignments, reversed)
}

func TestBuildHomogeneousAssignmentMatrix_SchedulesEachScenarioOnlyForItsEligibleRoles(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: "qwen3:4b", ModelDigest: repeatHex('a')}
	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID:      "role-eligibility",
		RunID:           "run-1",
		Catalog:         catalog,
		ActivePosture:   evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:        []*evalv1.ModelVariant{variant},
		RepetitionCount: 1,
		QueuedAt:        time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)

	rolesByScenario := make(map[string][]evalv1.ModelCampaignRole)
	for _, assignment := range assignments {
		rolesByScenario[assignment.GetScenarioId()] = append(rolesByScenario[assignment.GetScenarioId()], assignment.GetHomogeneous().GetDesignatedRole())
	}
	const (
		primary   = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
		assistant = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
		lite      = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	)
	assert.ElementsMatch(t, []evalv1.ModelCampaignRole{primary, assistant}, rolesByScenario["tool-arg-file-path"])
	assert.ElementsMatch(t, []evalv1.ModelCampaignRole{primary, assistant}, rolesByScenario["instruction-classify-severity"])
	assert.ElementsMatch(t, []evalv1.ModelCampaignRole{lite}, rolesByScenario["route-lite-triage"])
	assert.ElementsMatch(t, []evalv1.ModelCampaignRole{primary}, rolesByScenario["final-response-diagnosis"])
	assert.ElementsMatch(t, []evalv1.ModelCampaignRole{assistant}, rolesByScenario["route-handoff-assistant"])
	assert.Len(t, rolesByScenario, len(catalog.GetScenarios()))
}

func TestBuildHomogeneousAssignmentMatrix_RejectsScenarioWithoutEligibleRoles(t *testing.T) {
	catalog := testScenarioCatalog()
	catalog.Scenarios[0].EligibleRoles = nil

	_, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID:    "campaign",
		RunID:         "run",
		Catalog:       catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:      []*evalv1.ModelVariant{testModelVariant()},
	})

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioRolesUnassigned)
}

func TestBuildHomogeneousAssignmentMatrix_PreservesFilteredCatalogOrderAfterReload(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	// A custom suite's declared order is authoritative even when it differs
	// from the built-in workflow and the filter is supplied in reverse order.
	catalog.Scenarios = []*evalv1.EvaluationScenarioDefinition{catalog.Scenarios[5], catalog.Scenarios[0], catalog.Scenarios[38]}
	catalog.CatalogDigest, err = ComputeScenarioCatalogDigest(catalog)
	require.NoError(t, err)
	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID: "workflow-campaign", RunID: "workflow-run", Catalog: catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		Variants:      []*evalv1.ModelVariant{{VariantId: "model", ServedModelTag: "model:1", ModelDigest: repeatHex('a')}},
		ScenarioIDs:   []string{catalog.Scenarios[2].GetScenarioId(), catalog.Scenarios[0].GetScenarioId()},
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	var ids []string
	for _, assignment := range assignments {
		if len(ids) == 0 || ids[len(ids)-1] != assignment.GetScenarioId() {
			ids = append(ids, assignment.GetScenarioId())
		}
	}
	assert.Equal(t, []string{catalog.Scenarios[0].GetScenarioId(), "final-response-diagnosis"}, ids)
	store := NewStore(newCampaignMemoryFileService())
	require.NoError(t, store.SaveScenarioCatalog(t.Context(), "workflow-campaign", catalog))
	for i := len(assignments) - 1; i >= 0; i-- {
		require.NoError(t, store.SaveAssignment(t.Context(), assignments[i]))
	}
	loaded, err := store.ListAssignments(t.Context(), "workflow-run")
	require.NoError(t, err)
	require.Len(t, loaded, len(assignments))
	for i := range assignments {
		assert.Equal(t, assignments[i].GetAssignmentId(), loaded[i].GetAssignmentId())
	}
}

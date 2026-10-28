// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestBuildHeterogeneousAssignmentMatrix_MaterializesStacksByScenarios(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	stackSet, err := testFormationStackSet("heterogeneous-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)
	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID:    "heterogeneous-campaign",
		RunID:         "run-heterogeneous-1",
		Catalog:       catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		StackSet:      stackSet,
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	assert.Len(t, assignments, int(FormationMatrixSize(catalog, uint64(len(stackSet.Stacks)), evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)))
	require.NoError(t, ValidateHeterogeneousAssignmentMatrix(catalog, stackSet, assignments, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
	for _, assignment := range assignments {
		assert.Equal(t, evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM, assignment.GetLane())
	}
}

func TestBuildHeterogeneousAssignmentMatrix_PreservesCatalogOrderAfterReload(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	stackSet, err := testFormationStackSet("workflow-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)
	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID: "workflow-campaign", RunID: "workflow-run", Catalog: catalog, StackSet: stackSet,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	cellsPerStack := int(CatalogModelRoleCells(catalog, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
	require.Len(t, assignments, cellsPerStack*len(stackSet.Stacks))
	wantCells := expectedHeterogeneousCells(catalog)
	for offset := 0; offset < len(assignments); offset += cellsPerStack {
		stackID := assignments[offset].GetHeterogeneous().GetStack().GetStackId()
		for i, want := range wantCells {
			got := assignments[offset+i]
			assert.Equal(t, stackID, got.GetHeterogeneous().GetStack().GetStackId())
			assert.Equal(t, want, got.GetScenarioId()+"|"+got.GetHeterogeneous().GetDesignatedRole().String())
		}
	}
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

// expectedHeterogeneousCells lists each stack's (scenario, role) cells in
// execution order: catalog scenario order, then ascending role within a scenario.
func expectedHeterogeneousCells(catalog *evalv1.EvaluationScenarioCatalog) []string {
	var cells []string
	for _, scenario := range catalog.GetScenarios() {
		roles := append([]evalv1.ModelCampaignRole(nil), scenario.GetEligibleRoles()...)
		sort.Slice(roles, func(i, j int) bool { return roles[i] < roles[j] })
		for _, role := range roles {
			cells = append(cells, scenario.GetScenarioId()+"|"+role.String())
		}
	}
	return cells
}

// liteOnlyCatalog returns a copy of catalog whose first scenario is eligible for
// the Lite role alone, with the catalog digest recomputed.
func liteOnlyCatalog(t *testing.T, catalog *evalv1.EvaluationScenarioCatalog) *evalv1.EvaluationScenarioCatalog {
	t.Helper()
	clone, ok := proto.Clone(catalog).(*evalv1.EvaluationScenarioCatalog)
	require.True(t, ok)
	clone.Scenarios[0].EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE}
	digest, err := ComputeScenarioCatalogDigest(clone)
	require.NoError(t, err)
	clone.CatalogDigest = digest
	return clone
}

func TestBuildHeterogeneousAssignmentMatrix_LiteOnlyScenarioYieldsOnlyLiteCells(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	liteCatalog := liteOnlyCatalog(t, catalog)
	liteScenarioID := liteCatalog.GetScenarios()[0].GetScenarioId()
	stackSet, err := testFormationStackSet("heterogeneous-lite-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)

	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID:    "heterogeneous-lite-campaign",
		RunID:         "run-heterogeneous-lite",
		Catalog:       liteCatalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		StackSet:      stackSet,
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	require.Len(t, assignments, int(FormationMatrixSize(liteCatalog, uint64(len(stackSet.Stacks)), evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)))
	require.NoError(t, ValidateHeterogeneousAssignmentMatrix(liteCatalog, stackSet, assignments, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))

	liteCells := 0
	for _, assignment := range assignments {
		if assignment.GetScenarioId() != liteScenarioID {
			continue
		}
		liteCells++
		assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, assignment.GetHeterogeneous().GetDesignatedRole())
	}
	assert.Equal(t, len(stackSet.Stacks), liteCells)
}

func TestValidateHeterogeneousAssignmentMatrix_RejectsIneligibleDesignatedRole(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	liteCatalog := liteOnlyCatalog(t, catalog)
	liteScenarioID := liteCatalog.GetScenarios()[0].GetScenarioId()
	stackSet, err := testFormationStackSet("heterogeneous-ineligible-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)
	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID:    "heterogeneous-ineligible-campaign",
		RunID:         "run-heterogeneous-ineligible",
		Catalog:       liteCatalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		StackSet:      stackSet,
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)

	for _, assignment := range assignments {
		if assignment.GetScenarioId() != liteScenarioID {
			continue
		}
		assignment.GetHeterogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY
		identity, err := ComputeAssignmentDeterministicIdentity(assignment)
		require.NoError(t, err)
		assignment.DeterministicIdentity = identity
		assignment.AssignmentId = identity
		break
	}
	err = ValidateHeterogeneousAssignmentMatrix(liteCatalog, stackSet, assignments, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	require.ErrorIs(t, err, constants.ErrEvaluationScenarioRolesUnassigned)
}

func TestValidateHeterogeneousAssignmentMatrix_RejectsDuplicateStackScenarioRole(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	stackSet, err := testFormationStackSet("heterogeneous-duplicate-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)
	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID:    "heterogeneous-duplicate-campaign",
		RunID:         "run-heterogeneous-duplicate",
		Catalog:       catalog,
		ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		StackSet:      stackSet,
		QueuedAt:      time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	require.NoError(t, ValidateHeterogeneousAssignmentMatrix(catalog, stackSet, assignments, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))

	// Replace one cell with a copy of its neighbour: same stack, scenario, and
	// role, so the matrix is the right size but repeats a (stack, scenario, role).
	assignments[1] = assignments[0]
	err = ValidateHeterogeneousAssignmentMatrix(catalog, stackSet, assignments, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE)
	require.Error(t, err)
}

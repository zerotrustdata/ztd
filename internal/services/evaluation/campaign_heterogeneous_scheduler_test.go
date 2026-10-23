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

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestBuildHeterogeneousAssignmentMatrix_MaterializesStacksByScenarios(t *testing.T) {
	catalog, _, err := BuildScenarioCatalog()
	require.NoError(t, err)
	stackSet, err := testFormationStackSet("heterogeneous-campaign", 11, testFormationCatalogVariants())
	require.NoError(t, err)
	assignments, err := BuildHeterogeneousAssignmentMatrix(HeterogeneousScheduleRequest{
		CampaignID: "heterogeneous-campaign",
		RunID:      "run-heterogeneous-1",
		Catalog:    catalog,
		StackSet:   stackSet,
		QueuedAt:   time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	assert.Len(t, assignments, int(FormationMatrixSize(catalog, uint64(len(stackSet.Stacks)))))
	require.NoError(t, ValidateHeterogeneousAssignmentMatrix(catalog, stackSet, assignments))
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
		QueuedAt: time.Unix(1_700_000_000, 0).UTC(),
	})
	require.NoError(t, err)
	for offset := 0; offset < len(assignments); offset += len(catalog.Scenarios) {
		for i, scenario := range catalog.Scenarios {
			assert.Equal(t, assignments[offset].GetHeterogeneous().GetStack().GetStackId(), assignments[offset+i].GetHeterogeneous().GetStack().GetStackId())
			assert.Equal(t, scenario.GetScenarioId(), assignments[offset+i].GetScenarioId())
		}
	}
	store := NewStore(newCampaignMemoryFileService())
	require.NoError(t, store.SaveScenarioCatalog(t.Context(), "workflow-campaign", catalog))
	for i := len(assignments) - 1; i >= 0; i-- {
		require.NoError(t, store.SaveAssignment(t.Context(), assignments[i]))
	}
	loaded, err := store.ListAssignments(t.Context(), "workflow-run")
	require.NoError(t, err)
	require.Equal(t, len(assignments), len(loaded))
	for i := range assignments {
		assert.Equal(t, assignments[i].GetAssignmentId(), loaded[i].GetAssignmentId())
	}
}

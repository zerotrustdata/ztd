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

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignController_GenerateFormationStackSet_PersistsStacks(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })

	req := testCampaignInitRequest(t)
	catalog := req.Catalog
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Personas:      catalog.GetPersonas(),
		Scenarios:     catalog.GetScenarios()[:3],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated
	req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	req.Inventory, err = MaterializeModelRegistry(req.CampaignID, testHeterogeneousVariants())
	require.NoError(t, err)

	stackSet, err := testFormationStackSet(req.CampaignID, 17, req.Inventory.Variants)
	require.NoError(t, err)
	require.NotNil(t, stackSet)
	assert.NotEmpty(t, stackSet.Stacks)
	req.FormationStackSet = stackSet
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)

	loaded, err := store.LoadHeterogeneousStackSet(context.Background(), run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	assert.Equal(t, stackSet.SetDigest, loaded.SetDigest)
}

func TestCampaignController_ScheduleRun_SystemLaneUsesFrozenCatalogFormations(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })

	req := testCampaignInitRequest(t)
	req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	var err error
	req.Inventory, err = MaterializeModelRegistry(req.CampaignID, testHeterogeneousVariants())
	require.NoError(t, err)

	stackSet, err := testFormationStackSet(req.CampaignID, 17, req.Inventory.Variants)
	require.NoError(t, err)
	req.FormationStackSet = stackSet
	_, err = controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)

	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	assert.Equal(t, int(FormationMatrixSize(req.Catalog, uint64(len(stackSet.Stacks)), testGovernance.Posture)), count)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	assert.Len(t, assignments, count)
	for _, assignment := range assignments {
		assert.Equal(t, evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM, assignment.GetLane())
	}
}

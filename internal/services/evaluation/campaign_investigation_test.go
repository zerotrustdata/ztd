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
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignInvestigationValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		investigation *evalv1.EvaluationInvestigation
		valid         bool
	}{
		{name: "omitted", investigation: nil, valid: true},
		{name: "fresh", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH}, valid: true},
		{name: "fresh rejects identity fields", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH, InvestigationId: "case-1"}},
		{name: "unspecified mode", investigation: &evalv1.EvaluationInvestigation{}},
		{name: "unknown mode", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode(99)}},
		{name: "persistent requires investigation ID", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, DataOperatorId: "operator-1"}},
		{name: "persistent requires Data Operator ID", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, InvestigationId: "case-1"}},
		{name: "persistent rejects harness namespace", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, InvestigationId: "eval-harness:case-1", DataOperatorId: "operator-1"}},
		{name: "persistent rejects whitespace", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, InvestigationId: " case-1", DataOperatorId: "operator-1"}},
		{name: "persistent", investigation: &evalv1.EvaluationInvestigation{Mode: evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT, InvestigationId: "case-1", DataOperatorId: "operator-1"}, valid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCampaignInvestigation(test.investigation)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, constants.ErrEvaluationInvestigationInvalid)
			}
		})
	}
}

func TestCampaignInvestigationIsDigestBoundAndLegacyAbsenceRemainsVerifiable(t *testing.T) {
	t.Parallel()
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("investigation-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)

	fresh, err := MaterializeCampaignSpec("investigation-campaign", catalog, inventory, 1, false, testPlatform)
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH, fresh.GetInvestigation().GetMode())

	persistent, err := MaterializeCampaignSpecWithInvestigation("investigation-campaign", catalog, inventory, 1, false, testPlatform, &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: "checkout-incident",
		DataOperatorId:  "operator-primary",
	})
	require.NoError(t, err)
	assert.NotEqual(t, fresh.GetCampaignDigest(), persistent.GetCampaignDigest())
	require.NoError(t, ValidateCampaignSpecDigest(persistent))

	tampered := proto.Clone(persistent).(*evalv1.EvaluationCampaignSpec)
	tampered.Investigation.InvestigationId = "other-investigation"
	require.ErrorIs(t, ValidateCampaignSpecDigest(tampered), constants.ErrChecksumMismatch)

	legacy := proto.Clone(fresh).(*evalv1.EvaluationCampaignSpec)
	legacy.Investigation = nil
	legacy.CampaignDigest, err = ComputeCampaignSpecDigest(legacy)
	require.NoError(t, err)
	require.NoError(t, ValidateCampaignSpecDigest(legacy))
}

func TestCampaignControllerRejectsInvestigationIdentityConflict(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	first, err := controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_FRESH, first.GetInvestigation().GetMode())

	_, err = controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
		Investigation: &evalv1.EvaluationInvestigation{
			Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
			InvestigationId: "checkout-incident",
			DataOperatorId:  "operator-primary",
		},
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
}

func TestCampaignControllerRejectsSpecChangedAfterRunBindingBeforeStartingAssignment(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &stubCampaignExecutor{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)

	changed, err := store.LoadCampaignSpec(context.Background(), req.CampaignID)
	require.NoError(t, err)
	changed.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: "checkout-incident",
		DataOperatorId:  "operator-primary",
	}
	changed.CampaignDigest, err = ComputeCampaignSpecDigest(changed)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(context.Background(), changed))

	_, executed, err := controller.ExecuteNextAssignment(context.Background(), req.RunID, CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}, req.ScenarioArtifacts)
	require.ErrorIs(t, err, constants.ErrChecksumMismatch)
	assert.False(t, executed)
	assert.Zero(t, executor.calls)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED, assignments[0].GetLifecycleStatus())
}

func TestCampaignControllerKeepsLegacyMissingInvestigationUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &stubCampaignExecutor{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	spec, err := controller.CreateCampaign(ctx, CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	spec.Investigation = nil
	spec.CampaignDigest, err = ComputeCampaignSpecDigest(spec)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(ctx, spec))

	_, err = controller.StartRun(ctx, RunStartRequest{ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, ModelProvenancePolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
		CampaignID:                 req.CampaignID,
		RunID:                      req.RunID,
		InferenceOperatorSessionID: "inf-session",
		DataOperatorSessionID:      "data-session",
		PlatformRelease:            req.Platform.Release,
	})
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	_, executed, err := controller.ExecuteNextAssignment(ctx, req.RunID, CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}, req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.True(t, executed)
	assert.Nil(t, executor.investigation, "legacy absence remains absent at execution")

	reloaded, err := store.LoadCampaignSpec(ctx, req.CampaignID)
	require.NoError(t, err)
	assert.Nil(t, reloaded.GetInvestigation(), "execution must not rewrite legacy frozen campaign artifacts")
}

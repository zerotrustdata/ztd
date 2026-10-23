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
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type stubCampaignExecutor struct {
	calls         int
	investigation *evalv1.EvaluationInvestigation
	binding       CampaignExecutionBinding
}

func (s *stubCampaignExecutor) ExecuteAssignment(_ context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	s.calls++
	s.investigation = req.Investigation
	s.binding = req.Binding
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.Now(),
		ModelInferences: StubHomogeneousAssignmentModelInferences(req.Assignment),
	}
	digest, err := ComputeAssignmentResultDigest(result)
	if err != nil {
		return nil, err
	}
	result.ResultDigest = digest
	return result, nil
}

func testCampaignInitRequest(t *testing.T) CampaignInitRequest {
	t.Helper()
	catalog, artifacts, err := LoadScenarioCatalog()
	require.NoError(t, err)
	inventory, err := MaterializeModelRegistry("smoke-campaign", []*evalv1.ModelVariant{testModelVariant()})
	require.NoError(t, err)
	return CampaignInitRequest{
		Platform:                   testPlatform,
		Governance:                 testGovernance,
		CampaignID:                 "smoke-campaign",
		RunID:                      "run-smoke-1",
		Catalog:                    catalog,
		Inventory:                  inventory,
		ScenarioArtifacts:          artifacts,
		RepetitionCount:            1,
		InferenceOperatorSessionID: "inf-session",
		DataOperatorSessionID:      "data-session",
	}
}

func TestCampaignControllerInitializeScheduleAndResume(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &stubCampaignExecutor{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: "checkout-incident",
		DataOperatorId:  "operator-primary",
	}
	catalog := req.Catalog
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Scenarios:     catalog.GetScenarios()[:3],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated

	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "run-smoke-1", run.GetRunId())

	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	assert.Equal(t, 3, count)

	first, ok, err := controller.ResumeNextAssignment(context.Background(), req.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, first)

	result, executed, err := controller.ExecuteNextAssignment(context.Background(), req.RunID, CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executed)
	require.NotNil(t, result)
	assert.Equal(t, 1, executor.calls)
	require.NotNil(t, executor.investigation)
	assert.True(t, proto.Equal(req.Investigation, executor.investigation))

	exists, err := store.AssignmentResultExists(context.Background(), req.RunID, first.GetAssignmentId())
	require.NoError(t, err)
	assert.True(t, exists)

	restarted := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }, func(prefix string) string { return prefix + "-2" })
	resumed, ok, err := restarted.ResumeNextAssignment(context.Background(), req.RunID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, first.GetAssignmentId(), resumed.GetAssignmentId())
}

func TestCampaignControllerExecuteBindsOnlyTheFrozenModelRegistry(t *testing.T) { //nolint:paralleltest // subtests share one campaign controller
	ctx := context.Background()
	store := NewStore(newCampaignMemoryFileService())
	executor := &stubCampaignExecutor{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	frozenRegistry := InferenceVariantsFromEvalRegistry(req.Inventory.Variants)

	t.Run("registry digest that differs from the frozen spec fails before execution", func(t *testing.T) { //nolint:paralleltest // subtests share one campaign controller
		_, executed, err := controller.ExecuteNextAssignment(ctx, req.RunID, CampaignExecutionBinding{
			Topology:            testOperatorTopology("inf-session", "data-session"),
			ModelRegistryDigest: "sha256:not-the-frozen-registry",
			ModelRegistry:       frozenRegistry,
		}, req.ScenarioArtifacts)
		require.ErrorIs(t, err, constants.ErrChecksumMismatch)
		assert.False(t, executed)
		assert.Zero(t, executor.calls, "a mismatched registry digest must fail before the executor runs")
	})

	t.Run("caller registry entries are replaced by the frozen registry", func(t *testing.T) { //nolint:paralleltest // subtests share one campaign controller
		rogue := append(append([]*operatorv1.InferenceModelVariant(nil), frozenRegistry...),
			&operatorv1.InferenceModelVariant{Model: "rogue-model", Digest: "sha256:rogue"})
		_, executed, err := controller.ExecuteNextAssignment(ctx, req.RunID, CampaignExecutionBinding{
			Topology:            testOperatorTopology("inf-session", "data-session"),
			ModelRegistryDigest: req.Inventory.RegistryDigest,
			ModelRegistry:       rogue,
		}, req.ScenarioArtifacts)
		require.NoError(t, err)
		require.True(t, executed)
		assert.Equal(t, 1, executor.calls)
		assert.Equal(t, req.Inventory.RegistryDigest, executor.binding.ModelRegistryDigest)
		require.Len(t, executor.binding.ModelRegistry, len(frozenRegistry))
		for i := range frozenRegistry {
			assert.True(t, proto.Equal(frozenRegistry[i], executor.binding.ModelRegistry[i]), "registry entry %d", i)
		}
	})
}

func TestCampaignControllerResumeNextAssignmentFailsOnUnresolvedRunningAssignment(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, &stubCampaignExecutor{}, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	assignments[0].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
	require.NoError(t, store.SaveAssignment(context.Background(), assignments[0]))

	resumed, ok, err := controller.ResumeNextAssignment(context.Background(), req.RunID)
	require.ErrorIs(t, err, constants.ErrEvaluationAssignmentUnresolved)
	assert.False(t, ok)
	assert.Nil(t, resumed)
}

func TestCampaignControllerRunSummaryCountsAssignments(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	require.Positive(t, count)

	summary, err := controller.RunSummary(context.Background(), req.RunID)
	require.NoError(t, err)
	require.NotNil(t, summary)
	assert.Equal(t, req.RunID, summary.Run.GetRunId())
	assert.Positive(t, summary.ExpectedAssignment)
	assert.Equal(t, uint32(count), summary.QueuedCount)
}

func TestCampaignControllerCreateCampaign_IdempotencyAndParameterConflict(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)

	// Initial creation
	spec, err := controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	require.NotNil(t, spec)

	// Re-creating identical campaign must succeed (idempotent across revisions)
	recreated, err := controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount,
		Platform:          req.Platform,
	})
	require.NoError(t, err)
	assert.Equal(t, spec.GetCampaignDigest(), recreated.GetCampaignDigest())

	// Re-creating with different repetition count must fail with ErrEvaluationCampaignConflict
	_, err = controller.CreateCampaign(context.Background(), CampaignCreateRequest{
		CampaignID:        req.CampaignID,
		Catalog:           req.Catalog,
		Inventory:         req.Inventory,
		ScenarioArtifacts: req.ScenarioArtifacts,
		RepetitionCount:   req.RepetitionCount + 1,
		Platform:          req.Platform,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)
}

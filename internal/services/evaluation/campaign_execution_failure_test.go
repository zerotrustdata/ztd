// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type failingCampaignExecutor struct {
	message string
}

func (f *failingCampaignExecutor) ExecuteAssignment(_ context.Context, _ AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	return nil, assignmentExecutionError(f.message, fmt.Errorf("injected executor failure"))
}

func TestBuildExecutorFailureAssignmentResult_ClassifiesProviderFailures(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result, err := BuildExecutorFailureAssignmentResult(req, fmt.Errorf("evaluation: execute assignment: dispatch inference: %w", constants.ErrInferenceBackendTimeout), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, result.GetLifecycleStatus())
	assert.NotEmpty(t, result.GetResultDigest())
	assert.Equal(t, "Provider request timed out (backend_timeout); no final response was produced.", result.GetPublicFailureReason())
}

func TestBuildExecutorFailureAssignmentResult_ClassifiesValidationFailures(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result, err := BuildExecutorFailureAssignmentResult(req, fmt.Errorf("evaluation: execute assignment: build formation initial state: %w", constants.ErrMissingRequiredField), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())
}

func TestIsRecoverableAssignmentExecutionError_UsesTypedSentinel(t *testing.T) {
	t.Parallel()
	assert.True(t, isRecoverableAssignmentExecutionError(assignmentExecutionError("evaluation: execute heterogeneous assignment", fmt.Errorf("status 503"))))
	assert.False(t, isRecoverableAssignmentExecutionError(fmt.Errorf("evaluation: execute heterogeneous assignment: status 503")))
	assert.ErrorIs(t, assignmentExecutionError("evaluation: execute assignment: submit chat", fmt.Errorf("connection refused")), constants.ErrEvaluationAssignmentExecutionFailed)
}

func TestExecuteNextAssignment_PersistsResultOnRecoverableHeterogeneousExecutorFailure(t *testing.T) {
	variants := testHeterogeneousVariants()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &failingCampaignExecutor{message: "evaluation: execute heterogeneous assignment"}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	var err error
	req.Inventory, err = MaterializeModelRegistry(req.CampaignID, variants)
	require.NoError(t, err)
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.GenerateHeterogeneousStackSet(context.Background(), run.GetCampaignBinding().GetCampaignId(), 11)
	require.NoError(t, err)

	_, err = controller.ScheduleRun(context.Background(), run.GetRunId())
	require.NoError(t, err)

	result, executed, err := controller.ExecuteNextAssignment(context.Background(), run.GetRunId(), CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(variants),
	}, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executed)
	require.NotNil(t, result)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())

	exists, err := store.AssignmentResultExists(context.Background(), run.GetRunId(), result.GetAssignmentId())
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestRepairAssignmentsWithoutResults_BackfillsTerminalLifecycle(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
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
	_, err = controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = scheduleModelRoleAssignments(context.Background(), controller, req.RunID)
	require.NoError(t, err)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	assignment := assignments[0]
	assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED
	assignment.CompletedAt = timestamppb.New(time.Unix(1_700_000_100, 0).UTC())
	require.NoError(t, store.SaveAssignment(context.Background(), assignment))

	repaired, err := controller.RepairAssignmentsWithoutResults(context.Background(), req.RunID)
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)

	exists, err := store.AssignmentResultExists(context.Background(), req.RunID, assignment.GetAssignmentId())
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestClassifyExecutionFailure_MapsSentinelsToClosedClasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want ExecutionFailureClass
	}{
		{name: "deadline", err: fmt.Errorf("wait: %w", context.DeadlineExceeded), want: ExecutionFailureTimeout},
		{name: "canceled", err: fmt.Errorf("wait: %w", context.Canceled), want: ExecutionFailureCanceled},
		{name: "missing field", err: fmt.Errorf("build: %w", constants.ErrMissingRequiredField), want: ExecutionFailureInvalidInput},
		{name: "dispatch", err: fmt.Errorf("send: %w", constants.ErrEvaluationDispatchFailed), want: ExecutionFailureDispatch},
		{name: "target", err: fmt.Errorf("resolve: %w", constants.ErrEvaluationTargetUnavailable), want: ExecutionFailureTarget},
		{name: "posture", err: fmt.Errorf("posture: %w", constants.ErrEvaluationPostureUnsupported), want: ExecutionFailurePosture},
		{name: "evidence", err: fmt.Errorf("observe: %w", constants.ErrEvaluationObservationUnavailable), want: ExecutionFailureEvidence},
		{name: "verification", err: fmt.Errorf("verify: %w", constants.ErrEvalRunVerificationFailed), want: ExecutionFailureVerification},
		{name: "inference backend", err: fmt.Errorf("infer: %w", constants.ErrInferenceBackendUnavailable), want: ExecutionFailureProvider},
		{name: "unrecognized", err: fmt.Errorf("opaque failure"), want: ExecutionFailureInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyExecutionFailure(tt.err))
		})
	}
}

func TestBuildExecutorFailureAssignmentResult_PersistsClassNotErrorText(t *testing.T) {
	t.Parallel()
	const marker = "ZTD-ERROR-TEXT-MARKER-91c2"
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result, err := BuildExecutorFailureAssignmentResult(req, fmt.Errorf("model echoed %s: %w", marker, constants.ErrEvaluationDispatchFailed), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	require.Len(t, result.GetDeterministicGrades(), 1)
	assert.Equal(t, "assignment execution failed: dispatch_failure", result.GetDeterministicGrades()[0].GetDetail())
	assert.NotContains(t, result.String(), marker)
}

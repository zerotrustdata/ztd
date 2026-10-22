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
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
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

func TestExecuteNextAssignment_PersistsResultOnRecoverableExecutorFailure(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &failingCampaignExecutor{message: "evaluation: execute assignment"}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)

	_, err = controller.ScheduleRun(context.Background(), run.GetRunId())
	require.NoError(t, err)

	result, executed, err := controller.ExecuteNextAssignment(context.Background(), run.GetRunId(), CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executed)
	require.NotNil(t, result)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())

	exists, err := store.AssignmentResultExists(context.Background(), run.GetRunId(), result.GetAssignmentId())
	require.NoError(t, err)
	assert.True(t, exists)
}

type rejectingCampaignFeedExporter struct {
	reject bool
}

func (*rejectingCampaignFeedExporter) HighWaterSequence(context.Context) (int64, error) {
	return 0, nil
}

func (r *rejectingCampaignFeedExporter) ExportBatch(context.Context, []CampaignPublicFeedRecord) error {
	if r.reject {
		return constants.ErrEvidenceArtifactTooLarge
	}
	return nil
}

// A publication failure after the terminal result is persisted returns that
// result as executed, so the run continues past it.
func TestExecuteNextAssignment_PublicationFailureKeepsPersistedResult(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &failingCampaignExecutor{message: "evaluation: execute assignment"}
	exporter := &rejectingCampaignFeedExporter{}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" }).
		WithPublication(NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter))
	req := testCampaignInitRequest(t)
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), run.GetRunId())
	require.NoError(t, err)
	exporter.reject = true

	result, executed, err := controller.ExecuteNextAssignment(context.Background(), run.GetRunId(), CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}, req.ScenarioArtifacts)
	require.ErrorIs(t, err, constants.ErrEvaluationPublicationFailed)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactTooLarge)
	require.True(t, executed)
	require.NotNil(t, result)
	assignment, err := store.LoadAssignment(context.Background(), run.GetRunId(), result.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, result.GetLifecycleStatus(), assignment.GetLifecycleStatus())
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
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
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

// A running assignment whose frozen scenario names a tool the agent catalog
// lacks is resolved as a scenario_contract_invalid failure that verifies; a
// running assignment with a valid contract stays unresolved.
func TestRepairAssignmentsWithoutResults_ResolvesRunningRejectedScenarioContract(t *testing.T) {
	ctx := context.Background()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	scenarios := make([]*evalv1.EvaluationScenarioDefinition, 0, 2)
	for _, scenario := range req.Catalog.GetScenarios()[:2] {
		scenarios = append(scenarios, proto.Clone(scenario).(*evalv1.EvaluationScenarioDefinition))
	}
	rejected := scenarios[0]
	rejected.AllowedTools = append(rejected.AllowedTools, "removed_native_tool")
	catalog := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: req.Catalog.GetSchemaVersion(),
		CatalogRef:    req.Catalog.GetCatalogRef(),
		Scenarios:     scenarios,
	}
	digest, err := ComputeScenarioCatalogDigest(catalog)
	require.NoError(t, err)
	catalog.CatalogDigest = digest
	req.Catalog = catalog
	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)

	assignments, err := store.ListAssignments(ctx, req.RunID)
	require.NoError(t, err)
	var stranded, valid *evalv1.EvaluationAssignment
	for _, assignment := range assignments {
		if assignment.GetScenarioId() == rejected.GetScenarioId() && stranded == nil {
			stranded = assignment
		} else if assignment.GetScenarioId() != rejected.GetScenarioId() && valid == nil {
			valid = assignment
		}
	}
	require.NotNil(t, stranded)
	require.NotNil(t, valid)
	for _, assignment := range []*evalv1.EvaluationAssignment{stranded, valid} {
		assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
		require.NoError(t, store.SaveAssignment(ctx, assignment))
	}

	repaired, err := controller.RepairAssignmentsWithoutResults(ctx, req.RunID)
	require.NoError(t, err)
	assert.Equal(t, 1, repaired)

	results, err := store.LoadAssignmentResults(ctx, req.RunID, []*evalv1.EvaluationAssignment{stranded})
	require.NoError(t, err)
	result := results[stranded.GetAssignmentId()]
	require.NotNil(t, result)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())
	resolved, err := store.LoadAssignment(ctx, req.RunID, stranded.GetAssignmentId())
	require.NoError(t, err)
	assert.Empty(t, verifyExecutorFailureResult(resolved, result))
	assert.Equal(t, executionFailureDetailPrefix+string(ExecutionFailureScenarioContract), result.GetDeterministicGrades()[0].GetDetail())

	exists, err := store.AssignmentResultExists(ctx, req.RunID, valid.GetAssignmentId())
	require.NoError(t, err)
	assert.False(t, exists, "a running assignment with a valid contract is not repaired")
	_, _, err = controller.ResumeNextAssignment(ctx, req.RunID)
	require.ErrorIs(t, err, constants.ErrEvaluationAssignmentUnresolved)
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
		{name: "held context", err: assignmentExecutionError("held context", constants.ErrEvaluationHeldContextUnavailable), want: ExecutionFailureHeldContext},
		{name: "scenario contract", err: assignmentExecutionError("scope tools", constants.ErrEvaluationScenarioContractInvalid), want: ExecutionFailureScenarioContract},
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

// A traceless executor-failure result verifies structurally; any departure
// from the shape the builder produces does not.
func TestCampaignAssignmentVerifier_VerifiesTracelessExecutorFailure(t *testing.T) {
	t.Parallel()
	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	verify := func(req AssignmentExecutionRequest, result *evalv1.EvaluationAssignmentResult) *evalv1.EvaluationVerificationReport {
		t.Helper()
		digest, err := ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		result.ResultDigest = digest
		assignment := proto.Clone(req.Assignment).(*evalv1.EvaluationAssignment)
		assignment.LifecycleStatus = result.GetLifecycleStatus()
		report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{Assignment: assignment, Result: result})
		require.NoError(t, err)
		return report
	}
	build := func(t *testing.T, execErr error) (AssignmentExecutionRequest, *evalv1.EvaluationAssignmentResult) {
		t.Helper()
		req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
		result, err := BuildExecutorFailureAssignmentResult(req, execErr, time.Unix(1_700_000_000, 0).UTC())
		require.NoError(t, err)
		return req, result
	}
	providerErr := fmt.Errorf("dispatch inference: %w", constants.ErrInferenceBackendTimeout)
	invalidErr := fmt.Errorf("build formation initial state: %w", constants.ErrMissingRequiredField)

	for _, execErr := range []error{providerErr, invalidErr} {
		req, result := build(t, execErr)
		report := verify(req, result)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
	}

	tampers := map[string]func(*evalv1.EvaluationAssignmentResult){
		"unknown class": func(r *evalv1.EvaluationAssignmentResult) {
			r.DeterministicGrades[0].Detail = executionFailureDetailPrefix + "made_up"
		},
		"passing grade": func(r *evalv1.EvaluationAssignmentResult) {
			r.DeterministicGrades[0].Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		},
		"free-text reason": func(r *evalv1.EvaluationAssignmentResult) {
			r.FailureReason, r.PublicFailureReason = "provider said: secret", "provider said: secret"
		},
		"wrong lifecycle": func(r *evalv1.EvaluationAssignmentResult) {
			r.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
		},
		"tool call recorded": func(r *evalv1.EvaluationAssignmentResult) { r.ToolCalls = []*evalv1.ToolCallRecord{{CallId: "c1"}} },
		"dangling trace ref": func(r *evalv1.EvaluationAssignmentResult) {
			r.EvidenceRefs = []*compliancev1.ComplianceEvidenceReference{{ArtifactType: string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace)}}
		},
	}
	for name, tamper := range tampers {
		req, result := build(t, providerErr)
		tamper(result)
		report := verify(req, result)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus(), name)
	}
}

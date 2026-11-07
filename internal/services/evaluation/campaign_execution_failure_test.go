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
	"github.com/zerotrustdata/ztd/internal/services/inference"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type failingCampaignExecutor struct {
	message string
	err     error
}

func (f *failingCampaignExecutor) ExecuteAssignment(_ context.Context, _ AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	err := f.err
	if err == nil {
		err = fmt.Errorf("injected executor failure")
	}
	return nil, assignmentExecutionError(f.message, err)
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

func TestBuildExecutorFailureAssignmentResult_ClassifiesGraderFailures(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result, err := BuildExecutorFailureAssignmentResult(req, fmt.Errorf("evaluation: judge config: %w", constants.ErrEvaluationCampaignJudgeInvalid), time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, result.GetLifecycleStatus())
	assert.NotEmpty(t, result.GetResultDigest())
	require.Len(t, result.GetDeterministicGrades(), 1)
	assert.Equal(t, "assignment execution failed: grader_failed", result.GetDeterministicGrades()[0].GetDetail())
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

func TestExecuteNextAssignment_PersistsResultOnJudgeFailure(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	executor := &failingCampaignExecutor{
		message: "evaluation: record content judgments",
		err:     constants.ErrEvaluationCampaignJudgeInvalid,
	}
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
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, result.GetLifecycleStatus())
	require.NotEmpty(t, result.GetDeterministicGrades())
	assert.Equal(t, "assignment execution failed: grader_failed", result.GetDeterministicGrades()[0].GetDetail())

	exists, err := store.AssignmentResultExists(context.Background(), run.GetRunId(), result.GetAssignmentId())
	require.NoError(t, err)
	assert.True(t, exists)

	assignment, err := store.LoadAssignment(context.Background(), run.GetRunId(), result.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, assignment.GetLifecycleStatus())
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

// scheduledRepairRun schedules a small run and returns its controller, store,
// run ID, and assignments, none of which has a result yet.
func scheduledRepairRun(t *testing.T, now time.Time) (*CampaignController, *Store, string, []*evalv1.EvaluationAssignment) {
	t.Helper()
	store := NewStore(newCampaignMemoryFileService())
	controller := NewCampaignController(store, nil, func() time.Time { return now }, func(prefix string) string { return prefix + "-1" })
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
	_, err = controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	return controller, store, req.RunID, assignments
}

// Repair records a result the verifier accepts for every terminal lifecycle
// that reached it without one. COMPLETED claims an execution that has no
// evidence, so its recovered result is FAILED.
func TestRepairAssignmentsWithoutResults_RecoveredResultVerifies(t *testing.T) {
	ctx := context.Background()
	completedAt := time.Unix(1_700_000_100, 0).UTC()
	tests := []struct {
		name      string
		lifecycle evalv1.EvaluationAssignmentLifecycleStatus
		want      evalv1.EvaluationAssignmentLifecycleStatus
	}{
		{"stopped", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED},
		{"failed", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED},
		{"provider failed", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED},
		{"grader failed", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED},
		{"invalid evidence", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE},
		{"completed without evidence", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller, store, runID, assignments := scheduledRepairRun(t, completedAt.Add(time.Hour))
			assignment := assignments[0]
			assignment.LifecycleStatus = tt.lifecycle
			assignment.CompletedAt = timestamppb.New(completedAt)
			require.NoError(t, store.SaveAssignment(ctx, assignment))

			repaired, err := controller.RepairAssignmentsWithoutResults(ctx, runID)
			require.NoError(t, err)
			assert.Equal(t, 1, repaired)

			result, err := store.LoadAssignmentResult(ctx, runID, assignment.GetAssignmentId())
			require.NoError(t, err)
			stored, err := store.LoadAssignment(ctx, runID, assignment.GetAssignmentId())
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.GetLifecycleStatus())
			assert.Equal(t, executionFailureDetailPrefix+string(ExecutionFailureRecovered), result.GetDeterministicGrades()[0].GetDetail())
			assert.Equal(t, completedAt, result.GetCompletedAt().AsTime(), "repair keeps the recorded completion time")
			require.NoError(t, ValidateAssignmentResultDigest(result))
			assert.Empty(t, verifyExecutorFailureResult(stored, result))
		})
	}
}

// A cancelled run's stopped assignments have no result until repair records
// one. The whole run then verifies with the lifecycle cancel gave them, including
// a tool-bearing assignment whose run stopped before its model was probed.
func TestRepairAssignmentsWithoutResults_CancelledRunVerifies(t *testing.T) {
	for _, probeRequired := range []bool{false, true} {
		t.Run(fmt.Sprintf("tool calling probe required %t", probeRequired), func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1_700_000_100, 0).UTC()
			store := NewStore(newCampaignMemoryFileService())
			controller := NewCampaignController(store, nil, func() time.Time { return now }, func(prefix string) string { return prefix + "-1" })
			req := testCampaignInitRequest(t)
			req.Catalog.ToolCallingProbeRequired = probeRequired
			digest, err := ComputeScenarioCatalogDigest(req.Catalog)
			require.NoError(t, err)
			req.Catalog.CatalogDigest = digest
			_, err = controller.InitializeCampaign(ctx, req)
			require.NoError(t, err)
			scheduled, err := controller.ScheduleRun(ctx, req.RunID)
			require.NoError(t, err)

			stopped, err := controller.CancelRun(ctx, req.RunID)
			require.NoError(t, err)
			require.Equal(t, scheduled, stopped)
			repaired, err := controller.RepairAssignmentsWithoutResults(ctx, req.RunID)
			require.NoError(t, err)
			require.Equal(t, stopped, repaired)

			report, err := NewCampaignRunVerifier(func() time.Time { return now }).VerifyRun(ctx, store, req.RunID, req.Catalog, req.ScenarioArtifacts)
			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
			assert.Equal(t, uint32(scheduled), report.GetVerifiedAssignmentCount())
		})
	}
}

// cancelledToolProbeRun schedules and cancels a run whose catalog requires the
// tool-calling probe, so no assignment executed and no probe was frozen.
func cancelledToolProbeRun(t *testing.T, store *Store, now time.Time) (*CampaignController, CampaignInitRequest, []*evalv1.EvaluationAssignment) {
	t.Helper()
	ctx := context.Background()
	controller := NewCampaignController(store, nil, func() time.Time { return now }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Catalog.ToolCallingProbeRequired = true
	digest, err := ComputeScenarioCatalogDigest(req.Catalog)
	require.NoError(t, err)
	req.Catalog.CatalogDigest = digest
	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	_, err = controller.CancelRun(ctx, req.RunID)
	require.NoError(t, err)
	assignments, err := store.ListAssignments(ctx, req.RunID)
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	return controller, req, assignments
}

// saveMisclassifiedStoppedResult persists the record repair once wrote for a
// stopped assignment: internal_error carrying the STOPPED lifecycle.
func saveMisclassifiedStoppedResult(t *testing.T, store *Store, assignment *evalv1.EvaluationAssignment, mutate func(*evalv1.EvaluationAssignmentResult)) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	result := executorFailureAssignmentResult(assignment, ExecutionFailureInternal, "", assignment.GetCompletedAt())
	result.LifecycleStatus = assignment.GetLifecycleStatus()
	if mutate != nil {
		mutate(result)
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	require.NoError(t, store.SaveAssignmentResult(context.Background(), result))
	return result
}

// Repair replaces a stopped result it misclassified as internal_error, once,
// and the whole run then verifies, including cells whose model was never probed.
func TestRepairAssignmentsWithoutResults_ReplacesMisclassifiedStoppedResults(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_100, 0).UTC()
	store := NewStore(newCampaignRuntimeFileService(t))
	controller, req, assignments := cancelledToolProbeRun(t, store, now)
	for _, assignment := range assignments {
		saveMisclassifiedStoppedResult(t, store, assignment, nil)
	}
	verify := func() *evalv1.EvaluationVerificationReport {
		report, err := NewCampaignRunVerifier(func() time.Time { return now }).VerifyRun(ctx, store, req.RunID, req.Catalog, req.ScenarioArtifacts)
		require.NoError(t, err)
		return report
	}
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, verify().GetStatus())

	repaired, err := controller.RepairAssignmentsWithoutResults(ctx, req.RunID)
	require.NoError(t, err)
	assert.Equal(t, len(assignments), repaired)
	report := verify()
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())

	repaired, err = controller.RepairAssignmentsWithoutResults(ctx, req.RunID)
	require.NoError(t, err)
	assert.Zero(t, repaired, "a replaced result is not replaced again")
}

// Repair replaces only the exact misclassified record. A genuine executor
// failure and a stopped record carrying anything else are left as stored.
func TestRepairAssignmentsWithoutResults_LeavesOtherStoredResults(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_100, 0).UTC()
	failedLifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED
	tests := []struct {
		name      string
		lifecycle evalv1.EvaluationAssignmentLifecycleStatus
		mutate    func(*evalv1.EvaluationAssignmentResult)
		trace     bool
	}{
		{name: "internal error on a failed assignment", lifecycle: failedLifecycle},
		{name: "stopped record with a tool call", mutate: func(r *evalv1.EvaluationAssignmentResult) {
			r.ToolCalls = []*evalv1.ToolCallRecord{{CallId: "c1"}}
		}},
		{name: "stopped record with a sealed trace", trace: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(newCampaignRuntimeFileService(t))
			controller, req, assignments := cancelledToolProbeRun(t, store, now)
			for _, assignment := range assignments {
				require.NoError(t, store.SaveAssignmentResult(ctx, mustRecoveredResult(t, controller, assignment)))
			}
			target := assignments[0]
			if tt.lifecycle != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNSPECIFIED {
				target.LifecycleStatus = tt.lifecycle
				require.NoError(t, store.SaveAssignment(ctx, target))
			}
			stored := saveMisclassifiedStoppedResult(t, store, target, tt.mutate)
			if tt.trace {
				workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, req.RunID, "attempt-1")
				require.NoError(t, err)
				traceBody, err := SealAgentTrace(&AgentTrace{RunID: req.RunID, AssignmentID: target.GetAssignmentId(), EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Workspace: AgentTraceWorkspace(workspace), Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."})
				require.NoError(t, err)
				require.NoError(t, store.SaveAssignmentAgentTrace(ctx, req.RunID, target.GetAssignmentId(), traceBody))
			}

			repaired, err := controller.RepairAssignmentsWithoutResults(ctx, req.RunID)
			require.NoError(t, err)
			assert.Zero(t, repaired)
			kept, err := store.LoadAssignmentResult(ctx, req.RunID, target.GetAssignmentId())
			require.NoError(t, err)
			assert.True(t, proto.Equal(stored, kept))
		})
	}
}

func mustRecoveredResult(t *testing.T, controller *CampaignController, assignment *evalv1.EvaluationAssignment) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	result, err := controller.recoverTerminalAssignmentResult(assignment)
	require.NoError(t, err)
	return result
}

// A traceless executor failure records no execution, so a tool-bearing cell
// verifies without a probe: a probe with no signed verdict freezes no record
// (INV-EVAL-CAMP-31). Any other result still requires one.
func TestVerifyRun_ProbeRequiredOnlyForResultsThatRecordExecution(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_100, 0).UTC()
	failureResult := func(execErr error) func(*testing.T, *evalv1.EvaluationAssignment) *evalv1.EvaluationAssignmentResult {
		return func(t *testing.T, assignment *evalv1.EvaluationAssignment) *evalv1.EvaluationAssignmentResult {
			t.Helper()
			result, err := BuildExecutorFailureAssignmentResult(AssignmentExecutionRequest{Assignment: assignment}, execErr, now)
			require.NoError(t, err)
			return result
		}
	}
	tests := []struct {
		name   string
		build  func(*testing.T, *evalv1.EvaluationAssignment) *evalv1.EvaluationAssignmentResult
		passes bool
	}{
		{name: "provider stream error", build: failureResult(fmt.Errorf("probe: %w", constants.ErrInferenceProviderStreamError)), passes: true},
		{name: "provider timeout", build: failureResult(fmt.Errorf("probe: %w", constants.ErrInferenceBackendTimeout)), passes: true},
		{name: "internal error", build: failureResult(fmt.Errorf("probe: %w", constants.ErrEvaluationToolCallingProbeFailed)), passes: true},
		{name: "completed without a probe", build: func(t *testing.T, assignment *evalv1.EvaluationAssignment) *evalv1.EvaluationAssignmentResult {
			t.Helper()
			result := executorFailureAssignmentResult(assignment, ExecutionFailureInternal, "", timestamppb.New(now))
			result.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
			result.DeterministicGrades[0].CriterionId = "scenario-content"
			digest, err := ComputeAssignmentResultDigest(result)
			require.NoError(t, err)
			result.ResultDigest = digest
			return result
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(newCampaignMemoryFileService())
			controller, req, assignments := cancelledToolProbeRun(t, store, now)
			var target *evalv1.EvaluationAssignment
			for _, assignment := range assignments {
				tools, err := scenarioToolsForAssignment(req.Catalog, assignment)
				require.NoError(t, err)
				if target == nil && assignmentDeclaresTools(AssignmentExecutionRequest{Assignment: assignment, ScenarioTools: tools}) {
					target = assignment
					continue
				}
				require.NoError(t, store.SaveAssignmentResult(ctx, mustRecoveredResult(t, controller, assignment)))
			}
			require.NotNil(t, target)
			result := tt.build(t, target)
			target.LifecycleStatus = result.GetLifecycleStatus()
			require.NoError(t, store.SaveAssignment(ctx, target))
			require.NoError(t, store.SaveAssignmentResult(ctx, result))

			report, err := NewCampaignRunVerifier(func() time.Time { return now }).VerifyRun(ctx, store, req.RunID, req.Catalog, req.ScenarioArtifacts)
			require.NoError(t, err)
			if tt.passes {
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
				return
			}
			assert.Contains(t, report.GetFailureReasons(), fmt.Sprintf("assignment %s is missing a verified run tool-calling probe", target.GetAssignmentId()))
		})
	}
}

// Every attempt class has a categorical reason the verifier accepts.
func TestIsProviderFailureReason_AcceptsEveryAttemptClass(t *testing.T) {
	for _, class := range inference.AttemptFailureClasses() {
		assert.True(t, isProviderFailureReason(providerFailureReason(class)), string(class))
	}
	assert.False(t, isProviderFailureReason("provider said: secret"))
}

// Only the recovered class follows the assignment's lifecycle. Every other
// class keeps its fixed lifecycle, so a stopped result recording internal_error
// and a recovered result disagreeing with its assignment do not verify.
func TestVerifyExecutorFailureResult_RejectsRecoveredLifecycleMismatch(t *testing.T) {
	stoppedLifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED
	failedLifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED
	completedLifecycle := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	completedAt := timestamppb.New(time.Unix(1_700_000_100, 0).UTC())
	tests := []struct {
		name       string
		class      ExecutionFailureClass
		assignment evalv1.EvaluationAssignmentLifecycleStatus
		result     evalv1.EvaluationAssignmentLifecycleStatus
	}{
		{"internal error on a stopped assignment", ExecutionFailureInternal, stoppedLifecycle, stoppedLifecycle},
		{"recovered failed on a stopped assignment", ExecutionFailureRecovered, stoppedLifecycle, failedLifecycle},
		{"recovered stopped on a failed assignment", ExecutionFailureRecovered, failedLifecycle, stoppedLifecycle},
		{"recovered completed on a completed assignment", ExecutionFailureRecovered, completedLifecycle, completedLifecycle},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assignment := &evalv1.EvaluationAssignment{AssignmentId: "a1", RunId: "r1", CampaignId: "c1", LifecycleStatus: tt.assignment}
			result := executorFailureAssignmentResult(assignment, tt.class, "", completedAt)
			result.LifecycleStatus = tt.result
			assert.NotEmpty(t, verifyExecutorFailureResult(assignment, result))
		})
	}
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
		frozen := proto.Clone(scenario).(*evalv1.EvaluationScenarioDefinition)
		frozen.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
		scenarios = append(scenarios, frozen)
	}
	rejected := scenarios[0]
	rejected.AllowedTools = append(rejected.AllowedTools, "removed_native_tool")
	catalog := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: req.Catalog.GetSchemaVersion(),
		CatalogRef:    req.Catalog.GetCatalogRef(),
		Personas:      req.Catalog.GetPersonas(),
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

// Regrade re-imports a stored result whose sealed agent trace imports to a
// different lifecycle, keeps the stored completion time and the trace bytes,
// and leaves an agreeing result alone.
func TestRegradeAssignmentsFromAgentTraces_ReimportsDisagreeingResult(t *testing.T) {
	ctx := context.Background()
	store := NewStore(newCampaignRuntimeFileService(t))
	run := completedTestCampaign(t, store)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_800_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-regrade" })
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	traced := assignments[0]
	stored, err := store.LoadAssignmentResult(ctx, run.GetRunId(), traced.GetAssignmentId())
	require.NoError(t, err)
	invalid := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE
	require.NotEqual(t, invalid, stored.GetLifecycleStatus())
	// A trace that carries no ledger evidence imports as INVALID_EVIDENCE.
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, run.GetRunId(), "attempt-1")
	require.NoError(t, err)
	traceBody, err := SealAgentTrace(&AgentTrace{RunID: run.GetRunId(), AssignmentID: traced.GetAssignmentId(), EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Workspace: AgentTraceWorkspace(workspace), Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."})
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, run.GetRunId(), traced.GetAssignmentId(), traceBody))

	regraded, err := controller.RegradeAssignmentsFromAgentTraces(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.Equal(t, 1, regraded)
	result, err := store.LoadAssignmentResult(ctx, run.GetRunId(), traced.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, invalid, result.GetLifecycleStatus())
	assert.True(t, proto.Equal(stored.GetCompletedAt(), result.GetCompletedAt()))
	assignment, err := store.LoadAssignment(ctx, run.GetRunId(), traced.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, invalid, assignment.GetLifecycleStatus())
	_, body, err := store.LoadAssignmentAgentTraceArtifact(ctx, run.GetRunId(), traced.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, traceBody, body)

	regraded, err = controller.RegradeAssignmentsFromAgentTraces(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.Zero(t, regraded)
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
		{name: "recovered without result", err: constants.ErrEvaluationRecoveredResultMissing, want: ExecutionFailureRecovered},
		{name: "verification", err: fmt.Errorf("verify: %w", constants.ErrEvalRunVerificationFailed), want: ExecutionFailureVerification},
		{name: "inference backend", err: fmt.Errorf("infer: %w", constants.ErrInferenceBackendUnavailable), want: ExecutionFailureProvider},
		{name: "provider stream error", err: fmt.Errorf("infer: %w", constants.ErrInferenceProviderStreamError), want: ExecutionFailureProvider},
		{name: "rehydration", err: fmt.Errorf("infer: %w", constants.ErrL5ActuatorRehydrate), want: ExecutionFailureRehydration},
		{name: "request too large", err: fmt.Errorf("infer: %w", constants.ErrInferenceRequestTooLarge), want: ExecutionFailureRequestTooLarge},
		{name: "campaign judge invalid", err: fmt.Errorf("judge: %w", constants.ErrEvaluationCampaignJudgeInvalid), want: ExecutionFailureGrader},
		{name: "judge response invalid", err: fmt.Errorf("judge: %w", constants.ErrJudgeResponseInvalid), want: ExecutionFailureGrader},
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

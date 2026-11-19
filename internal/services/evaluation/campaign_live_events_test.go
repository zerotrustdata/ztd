// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// recordingLiveReporter records every live event push and answers each with
// err. ctxErr and hasDeadline describe the context the last push ran under.
type recordingLiveReporter struct {
	mu          sync.Mutex
	err         error
	starts      []models.ObserveProducerAssignmentRequest
	completes   []models.ObserveProducerAssignmentRequest
	fanOuts     []models.ObserveProducerToolCallFannedOutRequest
	ctxErr      error
	hasDeadline bool
}

func (r *recordingLiveReporter) seen(ctx context.Context) {
	r.ctxErr = ctx.Err()
	_, r.hasDeadline = ctx.Deadline()
}

func (r *recordingLiveReporter) StartAssignment(ctx context.Context, req models.ObserveProducerAssignmentRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen(ctx)
	r.starts = append(r.starts, req)
	return r.err
}

func (r *recordingLiveReporter) CompleteAssignment(ctx context.Context, req models.ObserveProducerAssignmentRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen(ctx)
	r.completes = append(r.completes, req)
	return r.err
}

func (r *recordingLiveReporter) RecordToolCallFannedOut(ctx context.Context, req models.ObserveProducerToolCallFannedOutRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen(ctx)
	r.fanOuts = append(r.fanOuts, req)
	return r.err
}

func fanOutShellCall() *operatorv1.InferenceToolCall {
	return &operatorv1.InferenceToolCall{
		CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: `{"command":"echo","target_operators":["3","1"]}`,
	}
}

// TestFanOut_ReportsTheGroupAsALiveEvent pushes one fan-out event per group,
// named as the signed trace names it.
func TestFanOut_ReportsTheGroupAsALiveEvent(t *testing.T) {
	reporter := &recordingLiveReporter{}
	run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
	run.executor.tools = &fleetTools{}
	run.executor.WithLiveEvents(reporter)
	run.trace.Roster = fleetRoster()

	group, err := run.runTool(t.Context(), 1, "model-call-1", fanOutShellCall())
	require.NoError(t, err)
	require.Len(t, group, 2)
	require.Len(t, reporter.fanOuts, 1, "one live event per fanned-out group")
	got := reporter.fanOuts[0]
	assert.Equal(t, constants.ObserveEventPayloadSchemaVersion, got.SchemaVersion)
	assert.Equal(t, "run-1", got.CycleID)
	assert.Equal(t, "campaign-1", got.CampaignID)
	assert.Equal(t, "assignment-1", got.AssignmentID)
	assert.Equal(t, group[0].CallID, got.CallID)
	assert.Equal(t, group[0].GroupID, got.GroupID)
	assert.Equal(t, constants.MCPToolRunShellCommand, got.ToolName)
	assert.Equal(t, 2, got.Targets)
	assert.Equal(t, 2, got.Succeeded)
	assert.Equal(t, 2, got.Concurrency)
	assert.GreaterOrEqual(t, got.DurationMS, int64(0))
}

// TestFanOut_AFailedLiveEventLeavesTheGroupUnchanged keeps live events best
// effort: a Gateway that refuses the push changes nothing the trace records.
func TestFanOut_AFailedLiveEventLeavesTheGroupUnchanged(t *testing.T) {
	runGroup := func(reporter LiveEventReporter) []AgentToolCall {
		run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{}, &tribunalAuthoringTools{})
		run.executor.tools = &fleetTools{}
		ids := 0
		run.executor.newID = func(prefix string) string { ids++; return prefix + "-" + string(rune('0'+ids)) }
		run.executor.WithLiveEvents(reporter)
		run.trace.Roster = fleetRoster()
		group, err := run.runTool(t.Context(), 1, "model-call-1", fanOutShellCall())
		require.NoError(t, err)
		return group
	}
	refusing := &recordingLiveReporter{err: errors.New("gateway unreachable")}
	assert.Equal(t, runGroup(nil), runGroup(refusing))
	assert.Len(t, refusing.fanOuts, 1)
}

// TestLiveEvents_PushIsDetachedFromTheCallersCancellation runs a push under
// its own deadline even when the caller's context has already ended, so an
// assignment that ran out of time still reports its end.
func TestLiveEvents_PushIsDetachedFromTheCallersCancellation(t *testing.T) {
	reporter := &recordingLiveReporter{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assignment := &evalv1.EvaluationAssignment{RunId: "run-1", CampaignId: "campaign-1", AssignmentId: "assignment-1", ScenarioId: "scenario-1", Repetition: 2}
	live := liveEvents{reporter: reporter}
	live.assignmentCompleted(ctx, assignment, &evalv1.EvaluationAssignmentResult{LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED})
	require.Len(t, reporter.completes, 1)
	require.NoError(t, reporter.ctxErr)
	assert.True(t, reporter.hasDeadline)
	got := reporter.completes[0]
	assert.Equal(t, "run-1", got.CycleID)
	assert.Equal(t, "scenario-1", got.TaskID)
	assert.Equal(t, 2, got.Repetition)
	assert.Equal(t, models.TerminalOutcomeStatusFailed, got.TerminalStatus)
	assert.Empty(t, got.VariantID, "an assignment without a designated target names no variant")

	liveEvents{}.assignmentStarted(ctx, assignment)
}

func TestLiveTerminalOutcome(t *testing.T) {
	for status, want := range map[evalv1.EvaluationAssignmentLifecycleStatus]models.TerminalOutcomeStatus{
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED:        models.TerminalOutcomeStatusCompleted,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL:          models.TerminalOutcomeStatusCompleted,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_ESCALATED:        models.TerminalOutcomeStatusCompleted,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNAVAILABLE:      models.TerminalOutcomeStatusUnavailable,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED:           models.TerminalOutcomeStatusFailed,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED:          models.TerminalOutcomeStatusFailed,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE: models.TerminalOutcomeStatusFailed,
	} {
		assert.Equal(t, want, liveTerminalOutcome(status), status.String())
	}
}

// TestCampaignControllerReportsAssignmentStartAndCompletion pushes the start
// once the assignment is running and the completion once its result is
// persisted, naming the persisted terminal outcome.
func TestCampaignControllerReportsAssignmentStartAndCompletion(t *testing.T) {
	ctx := t.Context()
	store := NewStore(newCampaignMemoryFileService())
	reporter := &recordingLiveReporter{err: errors.New("gateway unreachable")}
	controller := NewCampaignController(store, &stubCampaignExecutor{}, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" }).WithLiveEvents(reporter)
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)

	result, executed, err := controller.ExecuteNextAssignment(ctx, req.RunID, CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}, req.ScenarioArtifacts)
	require.NoError(t, err, "a refused live event never fails the assignment")
	require.True(t, executed)
	require.Len(t, reporter.starts, 1)
	require.Len(t, reporter.completes, 1)
	start, done := reporter.starts[0], reporter.completes[0]
	assert.Equal(t, result.GetAssignmentId(), start.AssignmentID)
	assert.Equal(t, req.RunID, start.CycleID)
	assert.NotEmpty(t, start.VariantID)
	assert.NotEmpty(t, start.Role)
	assert.Empty(t, start.TerminalStatus)
	assert.Equal(t, start.AssignmentID, done.AssignmentID)
	assert.Equal(t, liveTerminalOutcome(result.GetLifecycleStatus()), done.TerminalStatus)
}

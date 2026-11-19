// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// LiveEventReporter pushes live eval progress to the Gateway's observe
// producer, which emits it as SSE events to the owner's session. The CLI's
// ztd-eval harness client, bound to the run's CLI session, is the production
// reporter.
type LiveEventReporter interface {
	StartAssignment(ctx context.Context, req models.ObserveProducerAssignmentRequest) error
	CompleteAssignment(ctx context.Context, req models.ObserveProducerAssignmentRequest) error
	RecordToolCallFannedOut(ctx context.Context, req models.ObserveProducerToolCallFannedOutRequest) error
}

// liveEventPushTimeout bounds one live event push. The push runs on the eval
// runner's own time, so a slow Gateway costs the scenario at most this much.
const liveEventPushTimeout = 5 * time.Second

// liveEvents pushes live progress best effort. Live events are progress only:
// the local store and the signed trace are the record. A rejected or
// unreachable push is logged and never fails the assignment, credits the
// scenario budget, or changes the trace. Each push runs detached from the
// caller's cancellation (and so from a scenario budget's call context) under
// its own short deadline. A nil reporter pushes nothing.
type liveEvents struct {
	reporter LiveEventReporter
}

func (l liveEvents) push(ctx context.Context, event string, send func(context.Context) error) {
	if l.reporter == nil {
		return
	}
	pushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveEventPushTimeout)
	defer cancel()
	if err := send(pushCtx); err != nil {
		slog.Default().Warn("evaluation: live event push failed; the run continues", "event", event, logging.ErrorKindKey, logging.ErrorKindValue(err), "error", err)
	}
}

// assignmentStarted reports an assignment that is now running.
func (l liveEvents) assignmentStarted(ctx context.Context, assignment *evalv1.EvaluationAssignment) {
	l.push(ctx, string(constants.EventAiEvalAssignmentStarted), func(ctx context.Context) error {
		return l.reporter.StartAssignment(ctx, liveAssignmentRequest(assignment, ""))
	})
}

// assignmentCompleted reports an assignment's persisted terminal result.
func (l liveEvents) assignmentCompleted(ctx context.Context, assignment *evalv1.EvaluationAssignment, result *evalv1.EvaluationAssignmentResult) {
	l.push(ctx, string(constants.EventAiEvalAssignmentCompleted), func(ctx context.Context) error {
		return l.reporter.CompleteAssignment(ctx, liveAssignmentRequest(assignment, liveTerminalOutcome(result.GetLifecycleStatus())))
	})
}

// toolCallFannedOut reports one fanned-out tool call group.
func (l liveEvents) toolCallFannedOut(ctx context.Context, assignment *evalv1.EvaluationAssignment, lead AgentToolCall, targets, succeeded, concurrency int, duration time.Duration) {
	l.push(ctx, string(constants.EventAiEvalToolCallFannedOut), func(ctx context.Context) error {
		return l.reporter.RecordToolCallFannedOut(ctx, models.ObserveProducerToolCallFannedOutRequest{
			SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
			CycleID:       assignment.GetRunId(),
			CampaignID:    assignment.GetCampaignId(),
			AssignmentID:  assignment.GetAssignmentId(),
			CallID:        lead.CallID,
			GroupID:       lead.GroupID,
			ToolName:      lead.ToolName,
			Targets:       targets,
			Succeeded:     succeeded,
			Concurrency:   concurrency,
			DurationMS:    duration.Milliseconds(),
		})
	})
}

// liveAssignmentRequest describes an assignment as the observe producer reports
// it: the run is the cycle and the scenario is the task. The variant and role
// are the designated ones, and are left empty when the assignment has none.
func liveAssignmentRequest(assignment *evalv1.EvaluationAssignment, terminal models.TerminalOutcomeStatus) models.ObserveProducerAssignmentRequest {
	req := models.ObserveProducerAssignmentRequest{
		SchemaVersion:  constants.ObserveEventPayloadSchemaVersion,
		CycleID:        assignment.GetRunId(),
		CampaignID:     assignment.GetCampaignId(),
		AssignmentID:   assignment.GetAssignmentId(),
		TaskID:         assignment.GetScenarioId(),
		Repetition:     int(assignment.GetRepetition()),
		TerminalStatus: terminal,
	}
	if variantID, err := designatedVariantIDForAssignment(assignment); err == nil {
		req.VariantID = variantID
	}
	if role, err := designatedRoleLabelForAssignment(assignment); err == nil {
		req.Role = models.ModelRole(role)
	}
	return req
}

// liveTerminalOutcome is the observe terminal outcome of an assignment
// lifecycle status. A run that reached its end, partial or escalated included,
// completed; an unavailable cell is unavailable; every other end failed.
func liveTerminalOutcome(status evalv1.EvaluationAssignmentLifecycleStatus) models.TerminalOutcomeStatus {
	switch status {
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_ESCALATED:
		return models.TerminalOutcomeStatusCompleted
	case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNAVAILABLE:
		return models.TerminalOutcomeStatusUnavailable
	default:
		return models.TerminalOutcomeStatusFailed
	}
}

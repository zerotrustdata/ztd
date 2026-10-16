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
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// heldAssignmentContext returns the investigation a held-context repetition
// continues: the sealed trace of the previous repetition of the same cell.
// Repetitions of one cell run back to back (assignmentExecutionLess), so the
// previous one is terminal before this one starts. It returns nil when the
// campaign does not hold context or this is a cell's first repetition. A
// previous repetition that left no trace (a harness failure) cannot be
// continued; that is reported as a recoverable assignment execution error so
// this repetition records a harness failure rather than silently starting
// fresh.
func (c *CampaignController) heldAssignmentContext(ctx context.Context, campaignID string, assignment *evalv1.EvaluationAssignment) (*AgentTrace, error) {
	if assignment.GetHomogeneous() == nil || assignment.GetRepetition() <= 1 {
		return nil, nil
	}
	spec, err := c.store.LoadCampaignSpec(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("evaluation: held context: %w", err)
	}
	if !spec.GetHoldContext() {
		return nil, nil
	}
	previous, ok := proto.Clone(assignment).(*evalv1.EvaluationAssignment)
	if !ok {
		return nil, fmt.Errorf("evaluation: held context: assignment clone: %w", constants.ErrEvidenceArtifactMalformed)
	}
	previous.Repetition = assignment.GetRepetition() - 1
	previousID, err := ComputeAssignmentDeterministicIdentity(previous)
	if err != nil {
		return nil, fmt.Errorf("evaluation: held context: %w", err)
	}
	trace, err := c.store.LoadAssignmentAgentTrace(ctx, assignment.GetRunId(), previousID)
	if errors.Is(err, constants.ErrNotFound) {
		return nil, assignmentExecutionError(fmt.Sprintf("evaluation: held context: repetition %d left no trace", previous.GetRepetition()), constants.ErrEvaluationHeldContextUnavailable)
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: held context: %w", err)
	}
	return trace, nil
}

// newAgentTraceHeldContext rebuilds the previous repetition's conversation
// for the next one and checks that it still fits the inference bounds with
// room for the new prompt and at least one tool turn.
func newAgentTraceHeldContext(previous *AgentTrace, repetition uint32) (*AgentTraceHeldContext, []*operatorv1.InferenceMessage, error) {
	conversation, err := previous.Conversation()
	if err != nil {
		return nil, nil, assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: %w", constants.ErrEvaluationHeldContextUnavailable, err))
	}
	if len(conversation)+1 > agentMaxMessages {
		return nil, nil, assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: %d held messages leave no room under the %d-message bound", constants.ErrEvaluationHeldContextUnavailable, len(conversation), agentMaxMessages))
	}
	messages := make([]*operatorv1.InferenceMessage, 0, len(conversation))
	for _, message := range conversation {
		converted, err := message.inferenceMessage()
		if err != nil {
			return nil, nil, assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: %w", constants.ErrEvaluationHeldContextUnavailable, err))
		}
		messages = append(messages, converted)
	}
	return &AgentTraceHeldContext{
		AssignmentID:   previous.AssignmentID,
		Repetition:     repetition - 1,
		EvidenceDigest: previous.EvidenceDigest,
		Workspace:      previous.Workspace,
		Messages:       conversation,
	}, messages, nil
}

// heldScenarioWorkspace returns the workspace a held-context repetition
// continues in: the previous repetition's own, on the same Data Operator and
// working directory this repetition is bound to. A rebound Data Operator
// cannot see the held workspace, so that is a harness failure.
func heldScenarioWorkspace(previous *AgentTrace, topology CampaignOperatorTopology) (ScenarioWorkspace, error) {
	workingDirectory, err := cleanOperatorWorkingDirectory(topology.DataWorkingDirectory)
	if err != nil {
		return ScenarioWorkspace{}, err
	}
	workspace := previous.ScenarioWorkspace()
	if previous.Data.OperatorID != topology.Data.OperatorID || workspace.OperatorWorkingDirectory != workingDirectory {
		return ScenarioWorkspace{}, assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: repetition %s ran on Data Operator %s in %q, this repetition is bound to %s in %q",
			constants.ErrEvaluationHeldContextUnavailable, previous.AssignmentID, previous.Data.OperatorID, workspace.OperatorWorkingDirectory, topology.Data.OperatorID, workingDirectory))
	}
	if err := workspace.ValidateShape(); err != nil {
		return ScenarioWorkspace{}, assignmentExecutionError("evaluation: held context", fmt.Errorf("%w: %w", constants.ErrEvaluationHeldContextUnavailable, err))
	}
	return workspace, nil
}

// validateWorkspace checks the workspace a trace ran in: its own
// attempt-scoped workspace, or for a held-context repetition exactly the
// workspace of the repetition it continued.
func (t *AgentTrace) validateWorkspace() error {
	if t.HeldContext == nil {
		return t.ScenarioWorkspace().Validate(t.RunID, t.EvaluationAttemptID)
	}
	if t.Workspace != t.HeldContext.Workspace {
		return fmt.Errorf("%w: workspace root %q is not the held repetition's root %q", constants.ErrEvaluationWorkspaceUnavailable, t.Workspace.Root, t.HeldContext.Workspace.Root)
	}
	return t.ScenarioWorkspace().ValidateShape()
}

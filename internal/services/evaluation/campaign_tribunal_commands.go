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
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Model-visible answers for a deliberation that wrote no approved command.
const (
	tribunalNoConsensusError   = "ZTD-1607: Tool execution failed for run_commands_with_operator: the Tribunal reached no consensus on a command for this request; restate the request more specifically"
	tribunalAuditFailedError   = "ZTD-1607: Tool execution failed for run_commands_with_operator: the Auditor could not review the Tribunal's command (%s)"
	tribunalMarshalBlocked     = "MARSHAL BLOCKED: %s (feedback: %s)"
	tribunalMarshalConflict    = "AGENT CONFLICT: Marshal blocked command twice. Human intervention required."
	tribunalInferenceFailedMsg = "ZTD-1607: Tool execution failed for run_commands_with_operator: Tribunal inference failed (%s)"
)

// deliberateCommand runs the Tribunal deliberation for one intent-only
// run_commands_with_operator call and returns the approved command. Every
// deliberation inference is a governed call on the trace, in dispatch order,
// under the tool call that owns it, and every persona step joins the trace.
// A deliberation that approves no command is a model-visible failure; a
// Tribunal inference failure is also recorded as the trace's provider failure,
// which ends the loop.
func (r *agentRun) deliberateCommand(ctx context.Context, record AgentToolCall, args agentToolArguments) (string, *agentToolFailure, error) {
	tribunalRole := r.designated
	if IsHeterogeneousAssignment(r.req.Assignment) {
		tribunalRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT
	}
	tribunalModel := r.variants[tribunalRole]
	if tribunalModel == nil {
		return "", nil, fmt.Errorf("evaluation: tribunal model binding: %w", constants.ErrFormationRegistryBinding)
	}
	cfg := tribunal.Config{
		Inferer:     tribunalInferer{run: r, toolCallID: record.CallID, turn: record.LoopTurn},
		Tier:        tribunalRole,
		ModelTag:    tribunalModel.GetServedModelTag(),
		ModelDigest: tribunalModel.GetModelDigest(),
	}
	req := tribunal.Request{
		Request:          strings.TrimSpace(args.String(agentToolArgRequest)),
		Guidelines:       strings.TrimSpace(args.String(agentToolArgGuidelines)),
		WorkingDirectory: r.workspace.OperatorWorkingDirectory,
	}
	result, err := tribunal.Run(ctx, cfg, req, uint32(len(r.trace.Steps)))
	if errors.Is(err, constants.ErrTribunalInferenceFailed) {
		return "", &agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf(tribunalInferenceFailedMsg, r.trace.ProviderFailure)}, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("evaluation: tribunal deliberation: %w", err)
	}
	r.trace.Steps = append(r.trace.Steps, result.Steps...)
	switch result.Outcome {
	case tribunal.OutcomeApproved:
		r.marshalStrikeCount = 0
		return result.Command, nil, nil
	case tribunal.OutcomeBlocked:
		r.marshalStrikeCount++
		if r.marshalStrikeCount >= 2 {
			r.marshalStrikeCount = 0
			return "", &agentToolFailure{ErrorType: agentToolErrorPipeline, Error: tribunalMarshalConflict}, nil
		}
		return "", &agentToolFailure{ErrorType: agentToolErrorPipeline, Error: fmt.Sprintf(tribunalMarshalBlocked, result.Risk.Rationale, result.Risk.Feedback)}, nil
	case tribunal.OutcomeAuditFailed:
		return "", &agentToolFailure{ErrorType: agentToolErrorExecution, Error: fmt.Sprintf(tribunalAuditFailedError, result.Audit.Failure)}, nil
	default:
		return "", &agentToolFailure{ErrorType: agentToolErrorExecution, Error: tribunalNoConsensusError}, nil
	}
}

// tribunalInferer dispatches the Tribunal's inferences through the run's
// governed inference path on the designated model, with no tools declared.
type tribunalInferer struct {
	run        *agentRun
	toolCallID string
	turn       int
}

// Infer dispatches one Tribunal call and returns its answer text. A provider
// failure is recorded on the trace by infer and reported as
// ErrTribunalInferenceFailed so the deliberation stops.
func (t tribunalInferer) Infer(ctx context.Context, call tribunal.Call) (string, error) {
	messages := []*operatorv1.InferenceMessage{
		agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, call.System),
		agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, call.User),
	}
	result, recorded := t.run.infer(ctx, AgentModelCall{
		Purpose:    agentModelCallPurposeTribunalPrefix + string(call.Stage),
		LoopTurn:   t.turn,
		ToolCallID: t.toolCallID,
		Persona:    call.Persona,
	}, messages, nil)
	if recorded == nil {
		return "", fmt.Errorf("%w: %s %s round %d", constants.ErrTribunalInferenceFailed, call.Stage, call.Persona, call.Round)
	}
	text, thinking, _ := agentResponseParts(result)
	recorded.Text = text
	recorded.Thinking = thinking
	return text, nil
}

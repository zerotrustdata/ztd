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

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// TopologyID and TopologyVersion name the deployment topology an evaluation run
// records in its deployment identity: one remote Data Operator reached through
// the governed command lane.
const (
	TopologyID      = "unified-compose-remote-operator"
	TopologyVersion = "1.0.0"
)

// Target is the bound Data Operator a campaign attempt dispatches to.
type Target struct {
	OperatorID string
	SessionID  string
}

type agentDispatchPhase string

const (
	agentDispatchFixtureSetup agentDispatchPhase = "fixture-setup"
	agentDispatchHarnessProbe agentDispatchPhase = "harness-probe"
)

// AgentDispatchScope identifies one governed dispatch made for a campaign attempt.
type AgentDispatchScope struct {
	RunID           string
	ScenarioID      string
	InvestigationID string
	AttemptID       string
	Target          Target
	// HarnessPhase separates fixture and probe receipts from scored actions.
	// Empty denotes a model tool call.
	HarnessPhase agentDispatchPhase
}

type laneClient interface {
	DispatchCommand(context.Context, client.Persona, models.OperatorCommandRequest) (*client.GovernedOutcome, error)
}

// CommandLane is the campaign's governed path to a bound Data Operator. Each
// call is one typed dispatch whose answer carries the signed receipt, so the
// lane never polls for evidence.
type CommandLane struct {
	client  laneClient
	persona client.Persona
}

// NewCommandLane returns a lane that dispatches as persona through laneClient.
func NewCommandLane(laneClient laneClient, persona client.Persona) *CommandLane {
	return &CommandLane{client: laneClient, persona: persona}
}

// WriteWorkspaceFile dispatches a governed file write that materializes one
// frozen scenario fixture file at absPath (under the attempt-scoped workspace)
// on the bound Data Operator. It returns once the dispatch answer is a receipt
// for a completed write, so a scenario's expected read target exists before the
// scenario's chat request is sent.
func (l *CommandLane) WriteWorkspaceFile(ctx context.Context, target Target, runID, scenarioID, attemptID, absPath, content string) error {
	if l == nil || l.client == nil {
		return fmt.Errorf("%w: evaluation lane client is required", constants.ErrMissingRequiredField)
	}
	if target.OperatorID == "" || target.SessionID == "" || runID == "" || scenarioID == "" || attemptID == "" || absPath == "" {
		return fmt.Errorf("%w: workspace file write requires operator target, scenario identity, and path", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.FileEditRequested{FilePath: absPath, Operation: string(constants.FileOperationWrite), ExecutionId: attemptID, Content: content, CreateIfMissing: true})
	if err != nil {
		return fmt.Errorf("%w: marshal file edit request: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	scope := AgentDispatchScope{RunID: runID, ScenarioID: scenarioID, AttemptID: attemptID, Target: target, HarnessPhase: agentDispatchFixtureSetup}
	outcome, err := l.dispatchAction(ctx, scope, constants.ActionTypeFileEdit, absPath, payload)
	if err != nil {
		return err
	}
	if err := requireExecuted(outcome); err != nil {
		return fmt.Errorf("workspace file write for %s: %w", absPath, err)
	}
	return nil
}

// ReadWorkspaceFile dispatches a governed filesystem read of absPath on the
// bound Data Operator, the same action `file_read_on_operator` uses, and
// returns the file content. It proves a materialized fixture is reachable by
// the path the model will be given.
func (l *CommandLane) ReadWorkspaceFile(ctx context.Context, target Target, runID, scenarioID, attemptID, absPath string) (string, error) {
	if l == nil || l.client == nil {
		return "", fmt.Errorf("%w: evaluation lane client is required", constants.ErrMissingRequiredField)
	}
	if target.OperatorID == "" || target.SessionID == "" || runID == "" || scenarioID == "" || attemptID == "" || absPath == "" {
		return "", fmt.Errorf("%w: workspace file read requires operator target, scenario identity, and path", constants.ErrMissingRequiredField)
	}
	payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: absPath, ExecutionId: attemptID})
	if err != nil {
		return "", fmt.Errorf("%w: marshal filesystem read request: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	scope := AgentDispatchScope{RunID: runID, ScenarioID: scenarioID, AttemptID: attemptID, Target: target, HarnessPhase: agentDispatchHarnessProbe}
	outcome, err := l.dispatchAction(ctx, scope, constants.ActionTypeFsRead, absPath, payload)
	if err != nil {
		return "", err
	}
	if err := requireExecuted(outcome); err != nil {
		return "", fmt.Errorf("workspace file read of %s: %w", absPath, err)
	}
	resultPayload := outcome.Dispatch.GetResultPayload()
	if len(resultPayload) == 0 {
		return "", fmt.Errorf("%w: governed read response carries no result for %s", constants.ErrEvaluationDispatchFailed, absPath)
	}
	result := &operatorv1.FsReadResult{}
	if err := proto.Unmarshal(resultPayload, result); err != nil {
		return "", fmt.Errorf("%w: decode filesystem read result: %v", constants.ErrEvaluationDispatchFailed, err)
	}
	if result.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return "", fmt.Errorf("%w: workspace file read of %s ended %s: %s", constants.ErrEvaluationDispatchFailed, absPath, result.GetStatus(), result.GetErrorMessage())
	}
	return result.GetContent(), nil
}

// GovernedToolDispatch is the Gateway's typed answer to one governed agent tool
// dispatch. Outcome is that answer: a receipt, a governance rejection, or an
// ingress refusal. TransactionID and ResultPayload are read from the answer's
// receipt and dispatch result.
type GovernedToolDispatch struct {
	Outcome       *client.GovernedOutcome // the typed answer; Receipt, Rejection or Refusal
	TransactionID string
	ResultPayload []byte
}

// DispatchAgentTool submits one typed operator action on behalf of a model
// tool call and returns the Gateway's typed answer. A receipt, a governance
// rejection or an ingress refusal is an answer the model sees. A transport
// failure, an untyped answer or an approval suspension is a harness failure and
// is returned as an error so it is never graded against the model.
func (l *CommandLane) DispatchAgentTool(ctx context.Context, target Target, runID, investigationID, attemptID string, action constants.ActionType, targetResource string, payload []byte) (GovernedToolDispatch, error) {
	if l == nil || l.client == nil {
		return GovernedToolDispatch{}, fmt.Errorf("%w: evaluation lane client is required", constants.ErrMissingRequiredField)
	}
	if target.OperatorID == "" || target.SessionID == "" || runID == "" || investigationID == "" || attemptID == "" || len(payload) == 0 {
		return GovernedToolDispatch{}, fmt.Errorf("%w: agent tool dispatch requires operator target, scenario identity, and payload", constants.ErrMissingRequiredField)
	}
	scope := AgentDispatchScope{RunID: runID, InvestigationID: investigationID, AttemptID: attemptID, Target: target}
	outcome, err := l.dispatchAction(ctx, scope, action, targetResource, payload)
	if err != nil {
		return GovernedToolDispatch{}, err
	}
	return classifyToolDispatch(action, outcome)
}

// classifyToolDispatch reads one governed answer as a model tool call's
// dispatch. A receipt, a governance rejection or an ingress refusal is an
// answer the model sees; an untyped answer or an approval suspension is a
// harness failure.
func classifyToolDispatch(action constants.ActionType, outcome *client.GovernedOutcome) (GovernedToolDispatch, error) {
	switch answerKind(outcome) {
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT:
		return GovernedToolDispatch{Outcome: outcome, TransactionID: outcome.Dispatch.GetTransactionId(), ResultPayload: outcome.Dispatch.GetResultPayload()}, nil
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION:
		return GovernedToolDispatch{Outcome: outcome, TransactionID: outcome.Observation.GetRejection().GetReceipt().GetTransactionId()}, nil
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL:
		return GovernedToolDispatch{Outcome: outcome}, nil
	default:
		if err := outcome.Typed(); err != nil {
			return GovernedToolDispatch{}, fmt.Errorf("%w: governed %s: %w", constants.ErrEvaluationDispatchFailed, action, err)
		}
		return GovernedToolDispatch{}, fmt.Errorf("%w: governed %s suspended for approval", constants.ErrEvaluationDispatchFailed, action)
	}
}

// dispatchAction submits one typed governed action and returns the Gateway's
// answer unclassified. A transport failure or a missing answer is
// ErrEvaluationDispatchFailed; the caller classifies the answer it received.
func (l *CommandLane) dispatchAction(ctx context.Context, scope AgentDispatchScope, action constants.ActionType, targetResource string, payload []byte) (*client.GovernedOutcome, error) {
	investigationID, taskID := scope.InvestigationID, scope.AttemptID
	if scope.HarnessPhase != "" {
		investigationID = "eval-harness:" + scope.AttemptID
		taskID = string(scope.HarnessPhase) + ":" + scope.AttemptID
	}
	eventType, err := constants.RequestEventForAction(action)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve governed %s event: %w", constants.ErrEvaluationDispatchFailed, action, err)
	}
	outcome, err := l.client.DispatchCommand(ctx, l.persona, models.OperatorCommandRequest{
		TargetOperatorSessionID: scope.Target.SessionID, EventType: string(eventType), Payload: payload, TargetResource: targetResource,
		InvestigationID: investigationID, TaskID: taskID, CLISessionID: l.persona.CLISessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: submit governed %s: %w", constants.ErrEvaluationDispatchFailed, action, err)
	}
	if outcome == nil {
		return nil, fmt.Errorf("%w: governed %s returned no answer", constants.ErrEvaluationDispatchFailed, action)
	}
	return outcome, nil
}

// requireExecuted accepts a governed dispatch whose signed receipt ran to
// completion. Any other answer is ErrEvaluationDispatchFailed: an untyped answer
// wraps its typed error, and a rejection, refusal, or receipt that did not
// complete names what the Gateway or Operator reported.
func requireExecuted(outcome *client.GovernedOutcome) error {
	if err := outcome.Typed(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrEvaluationDispatchFailed, err)
	}
	if answerKind(outcome) != evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT {
		reason := answerMessage(outcome)
		if reason == "" {
			reason = answerKind(outcome).String()
		}
		return fmt.Errorf("%w: governed action was not admitted: %s", constants.ErrEvaluationDispatchFailed, reason)
	}
	if receipt := outcome.Observation.GetReceipt(); receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("%w: governed action ended %s", constants.ErrEvaluationDispatchFailed, receipt.GetStatus())
	}
	return nil
}

// answerKind is the typed kind of a governed answer. A missing answer is
// unspecified.
func answerKind(outcome *client.GovernedOutcome) evalv1.ConformanceObservationKind {
	if outcome == nil {
		return evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNSPECIFIED
	}
	return outcome.Observation.GetKind()
}

// answerMessage is the typed message a rejection or refusal carries. A refusal
// names its code before its error. Other answers carry no message.
func answerMessage(outcome *client.GovernedOutcome) string {
	switch answerKind(outcome) {
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION:
		return outcome.Observation.GetRejection().GetError()
	case evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL:
		return outcome.Observation.GetRefusal().GetCode().String() + ": " + outcome.Observation.GetRefusal().GetError()
	default:
		return ""
	}
}

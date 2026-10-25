// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// capturingTribunalDispatcher admits every request as a distinct envelope and
// runs its OnAdmitted hook, as DispatchService does before publish.
type capturingTribunalDispatcher struct {
	requests []DispatchRequest
	votes    []*operatorv1.ConsensusVoteInference
}

func (d *capturingTribunalDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (*DispatchResult, error) {
	d.requests = append(d.requests, req)
	id := fmt.Sprintf("transaction-%d", len(d.requests))
	if req.OnAdmitted != nil {
		env := &commonv1.GovernanceEnvelope{Id: id, Payload: req.Payload}
		if err := req.OnAdmitted(ctx, env, d.votes); err != nil {
			return nil, governance.WithConsensusInferences(d.votes, fmt.Errorf("dispatch: admitted: %w", err))
		}
	}
	return &DispatchResult{TransactionID: id, ConsensusInferences: d.votes}, nil
}

// fakeAdmissionReviewer records each admitted envelope and answers with review.
type fakeAdmissionReviewer struct {
	envelopes []*commonv1.GovernanceEnvelope
	review    func(*tribunalv1.TribunalIntentResult, *commonv1.GovernanceEnvelope) error
}

func (r *fakeAdmissionReviewer) ReviewAdmitted(_ context.Context, _ *tribunalv1.TribunalIntentRequest, result *tribunalv1.TribunalIntentResult, env *commonv1.GovernanceEnvelope, _ []*operatorv1.ConsensusVoteInference) error {
	r.envelopes = append(r.envelopes, env)
	if r.review == nil {
		return nil
	}
	return r.review(result, env)
}

func dispatchedArguments(t *testing.T, req DispatchRequest) string {
	t.Helper()
	payload := &operatorv1.McpCallRequested{}
	require.NoError(t, proto.Unmarshal(req.Payload, payload))
	return payload.GetArgumentsJson()
}

func TestTribunalIntentDispatchUsesApprovedArgumentBytesOnce(t *testing.T) {
	dispatcher := &capturingTribunalDispatcher{}
	controller := &TribunalIntentController{commands: dispatcher}
	// Noncanonical whitespace proves the controller uses the approved bytes directly.
	approved := `{"command":"printf", "args":["%s", "a  b"],"timeout":17,"working_dir":"/workspace"}`
	req := &tribunalv1.TribunalIntentRequest{TargetOperatorSessionId: "operator-session", ExecutionId: "execution", WorkingDirectory: "/workspace", CaseId: "case", InvestigationId: "investigation", TaskId: "task", CliSessionId: "cli"}
	req.MemberInference = &operatorv1.InferenceDispatchRequest{RunId: "run", AssignmentId: "assignment"}
	result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: approved}
	reviewer := &fakeAdmissionReviewer{}
	controller.dispatchCommand(context.Background(), req, result, "app", "user", reviewer)
	require.Len(t, dispatcher.requests, 1)
	require.Len(t, reviewer.envelopes, 1, "the admitted envelope is reviewed once before publish")
	assert.Equal(t, dispatcher.requests[0].Payload, reviewer.envelopes[0].GetPayload())
	captured := dispatcher.requests[0]
	event, err := constants.RequestEventForAction(constants.ActionTypeMcpCall)
	require.NoError(t, err)
	assert.Equal(t, string(event), captured.EventType)
	assert.Equal(t, constants.MCPToolRunShellCommand, captured.TargetResource)
	assert.Equal(t, "operator-session", captured.TargetOperatorSessionID)
	assert.Equal(t, "app", captured.ActingAppID)
	assert.Equal(t, "user", captured.RequestorUserID)
	assert.Equal(t, "case", captured.CaseID)
	assert.Equal(t, "investigation", captured.InvestigationID)
	assert.Equal(t, "task", captured.TaskID)
	assert.Equal(t, "cli", captured.CliSessionID)
	assert.Equal(t, "run", captured.EvaluationRunID)
	assert.Equal(t, "assignment", captured.EvaluationAssignmentID)
	payload := &operatorv1.McpCallRequested{}
	require.NoError(t, proto.Unmarshal(captured.Payload, payload))
	assert.Equal(t, constants.MCPToolRunShellCommand, payload.GetToolName())
	assert.Equal(t, approved, payload.GetArgumentsJson())
	assert.Equal(t, "execution", payload.GetExecutionId())
	require.NotNil(t, result.GetDispatched())
}

func TestTribunalIntentUnapprovedDoesNotDispatch(t *testing.T) {
	for _, outcome := range []tribunalv1.TribunalOutcome{
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_NO_CONSENSUS,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED,
		tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED,
	} {
		t.Run(outcome.String(), func(t *testing.T) {
			dispatcher := &capturingTribunalDispatcher{}
			controller := &TribunalIntentController{commands: dispatcher}
			result := &tribunalv1.TribunalIntentResult{Outcome: outcome}
			reviewer := &fakeAdmissionReviewer{}
			controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user", reviewer)
			assert.Empty(t, dispatcher.requests)
			assert.Empty(t, reviewer.envelopes)
			assert.Nil(t, result.CommandOutcome)
		})
	}
}

func TestTribunalIntentRevisionDispatchesFreshEnvelope(t *testing.T) {
	const approved, revised = `{"command":"ss","args":["-tlpn"]}`, `{"command":"ss","args":["-tlnp"]}`
	dispatcher := &capturingTribunalDispatcher{}
	controller := &TribunalIntentController{commands: dispatcher}
	reviewer := &fakeAdmissionReviewer{review: func(result *tribunalv1.TribunalIntentResult, _ *commonv1.GovernanceEnvelope) error {
		if result.GetArgumentsJson() == revised {
			return nil
		}
		result.ArgumentsJson = revised
		return fmt.Errorf("tribunal: contextual review: %w", constants.ErrTribunalCommandRevised)
	}}
	result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: approved}
	controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user", reviewer)

	require.Len(t, dispatcher.requests, 2, "the revision is dispatched as its own governed request")
	assert.JSONEq(t, approved, dispatchedArguments(t, dispatcher.requests[0]))
	assert.JSONEq(t, revised, dispatchedArguments(t, dispatcher.requests[1]))
	require.Len(t, reviewer.envelopes, 2)
	assert.NotEqual(t, reviewer.envelopes[0].GetId(), reviewer.envelopes[1].GetId(), "the earlier admission never authorizes the revised command")
	assert.Equal(t, tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, result.GetOutcome())
	assert.JSONEq(t, revised, result.GetArgumentsJson())
	require.NotNil(t, result.GetDispatched())
	assert.Equal(t, "transaction-2", result.GetDispatched().GetTransactionId())
}

func TestTribunalIntentReviewFailuresDoNotDispatch(t *testing.T) {
	cases := map[string]struct {
		review  func(*tribunalv1.TribunalIntentResult, *commonv1.GovernanceEnvelope) error
		outcome tribunalv1.TribunalOutcome
	}{
		"Auditor keeps revising": {
			review: func(result *tribunalv1.TribunalIntentResult, env *commonv1.GovernanceEnvelope) error {
				result.ArgumentsJson = fmt.Sprintf(`{"command":"echo","args":[%q]}`, env.GetId())
				return constants.ErrTribunalCommandRevised
			},
			outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED,
		},
		"revision blocked": {
			review: func(result *tribunalv1.TribunalIntentResult, _ *commonv1.GovernanceEnvelope) error {
				result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED
				return constants.ErrTribunalAnswerInvalid
			},
			outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_BLOCKED,
		},
		"review inference failed": {
			review: func(result *tribunalv1.TribunalIntentResult, _ *commonv1.GovernanceEnvelope) error {
				result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED
				return constants.ErrTribunalInferenceFailed
			},
			outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dispatcher := &capturingTribunalDispatcher{}
			controller := &TribunalIntentController{commands: dispatcher}
			result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: `{"command":"ss"}`}
			reviewer := &fakeAdmissionReviewer{review: tc.review}
			controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user", reviewer)
			assert.LessOrEqual(t, len(dispatcher.requests), tribunal.MaxAdmissionReviews)
			assert.Equal(t, tc.outcome, result.GetOutcome())
			assert.Empty(t, result.GetArgumentsJson(), "no command is reported as authorized")
			assert.Nil(t, result.CommandOutcome, "nothing was published")
		})
	}
}

func TestTribunalIntentReviewCancellationIsRefused(t *testing.T) {
	dispatcher := &capturingTribunalDispatcher{}
	controller := &TribunalIntentController{commands: dispatcher, logger: testutil.NewTestLogger()}
	result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: `{"command":"ss"}`}
	reviewer := &fakeAdmissionReviewer{review: func(*tribunalv1.TribunalIntentResult, *commonv1.GovernanceEnvelope) error {
		return context.Canceled
	}}
	controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user", reviewer)
	require.Len(t, dispatcher.requests, 1)
	assert.Nil(t, result.GetDispatched())
	assert.NotNil(t, result.GetRefusal(), "an interrupted review is a refused dispatch, never a success")
}

func TestTribunalIntentKeepsVotesWhenContextualReviewFails(t *testing.T) {
	votes := []*operatorv1.ConsensusVoteInference{{SubjectTransactionHash: "subject", MemberAppId: "tribunal-axiom"}}
	dispatcher := &capturingTribunalDispatcher{votes: votes}
	controller := &TribunalIntentController{commands: dispatcher}
	result := &tribunalv1.TribunalIntentResult{Outcome: tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED, ArgumentsJson: `{"command":"pwd"}`}
	reviewer := &fakeAdmissionReviewer{review: func(result *tribunalv1.TribunalIntentResult, _ *commonv1.GovernanceEnvelope) error {
		result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_INFERENCE_FAILED
		return constants.ErrTribunalInferenceFailed
	}}
	controller.dispatchCommand(context.Background(), &tribunalv1.TribunalIntentRequest{}, result, "app", "user", reviewer)
	assert.Nil(t, result.GetCommandOutcome())
	require.Len(t, result.GetConsensusInferences(), 1, "executed L2 votes survive a failed contextual review")
	assert.True(t, proto.Equal(votes[0], result.GetConsensusInferences()[0]))
}

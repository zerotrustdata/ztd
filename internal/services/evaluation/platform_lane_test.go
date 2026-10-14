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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type laneTestClient struct {
	dispatchOutcome  *client.GovernedOutcome
	dispatchErr      error
	dispatchRequests []client.DispatchCommandRequest
}

func (c *laneTestClient) DispatchCommand(_ context.Context, _ client.Persona, request client.DispatchCommandRequest) (*client.GovernedOutcome, error) {
	c.dispatchRequests = append(c.dispatchRequests, request)
	return c.dispatchOutcome, c.dispatchErr
}

// TestCommandLane_WriteWorkspaceFile_DispatchesGovernedWriteAndAdmitsCompletedReceipt
// guards the fixture-materialization path: WriteWorkspaceFile dispatches a real
// governed file write for the fixture content at the given absolute path and
// returns once the dispatch answer is a completed receipt. The lane has no
// receipt lookup, so the fake client cannot be asked for one.
func TestCommandLane_WriteWorkspaceFile_DispatchesGovernedWriteAndAdmitsCompletedReceipt(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	fakeClient := &laneTestClient{
		dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: laneCompletedReceipt()}),
	}
	lane := NewCommandLane(fakeClient, client.Persona{CLISessionID: "cli-session-1"})
	absPath := "/home/op/workspaces/ws-0123456789abcdef/net/network-summary.txt"
	content := "upstream_host=payments.internal.example"

	err := lane.WriteWorkspaceFile(context.Background(), target, "run-1", "tool-arg-file-path", "attempt-1", absPath, content)

	require.NoError(t, err)
	require.Len(t, fakeClient.dispatchRequests, 1)
	dispatch := fakeClient.dispatchRequests[0]
	assert.Equal(t, target.SessionID, dispatch.TargetOperatorSessionID)
	assert.Equal(t, absPath, dispatch.TargetResource)
	assert.Equal(t, "run-1", dispatch.CaseID)
	assert.Equal(t, "tool-arg-file-path", dispatch.InvestigationID)
	assert.Equal(t, "attempt-1", dispatch.TaskID)
	fileEdit := &operatorv1.FileEditRequested{}
	require.NoError(t, proto.Unmarshal(dispatch.Payload, fileEdit))
	assert.Equal(t, absPath, fileEdit.FilePath)
	assert.Equal(t, content, fileEdit.Content)
	assert.True(t, fileEdit.CreateIfMissing)
	assert.Equal(t, string(constants.FileOperationWrite), fileEdit.Operation)
}

func TestCommandLane_WriteWorkspaceFile_FailsClosedOnEveryAnswerThatIsNotACompletedReceipt(t *testing.T) {
	tests := []struct {
		name    string
		outcome *client.GovernedOutcome
		err     error
		typed   error
	}{
		{name: "transport error", err: errors.New("transport unavailable")},
		{name: "no answer"},
		{name: "rejection", outcome: laneRejectionOutcome(t)},
		{name: "refusal", outcome: laneRefusalOutcome(t, http.StatusBadRequest, &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, Error: "malformed"})},
		{name: "untyped answer", outcome: laneUntypedOutcome(), typed: constants.ErrIngressResponseUntyped},
		{name: "suspended for approval", outcome: laneSuspendedOutcome("tx-suspended")},
		{name: "receipt that failed", outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED}})},
		{name: "receipt without a status", outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-1"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{dispatchOutcome: tt.outcome, dispatchErr: tt.err}, client.Persona{})

			err := lane.WriteWorkspaceFile(context.Background(), Target{OperatorID: "operator-1", SessionID: "session-1"}, "run-1", "tool-arg-file-path", "attempt-1", "/home/op/workspaces/ws-0123456789abcdef/net/network-summary.txt", "upstream_host=payments.internal.example")

			require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
			if tt.typed != nil {
				require.ErrorIs(t, err, tt.typed)
			}
		})
	}
}

func TestCommandLane_WriteWorkspaceFile_RequiresOperatorTargetAndPath(t *testing.T) {
	lane := NewCommandLane(&laneTestClient{}, client.Persona{})

	err := lane.WriteWorkspaceFile(context.Background(), Target{}, "run-1", "scenario-1", "attempt-1", "", "x")

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestCommandLane_ReadWorkspaceFile_DispatchesGovernedFsReadAndReturnsContent(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	absPath := "/home/op/workspaces/ws-0123456789abcdef/canary/probe.txt"
	payload, err := proto.Marshal(&operatorv1.FsReadResult{
		ExecutionId: "attempt-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Path: absPath, Content: "canary-1",
	})
	require.NoError(t, err)
	fakeClient := &laneTestClient{
		dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ResultPayload: payload, Receipt: laneCompletedReceipt()}),
	}
	lane := NewCommandLane(fakeClient, client.Persona{CLISessionID: "cli-session-1"})

	content, err := lane.ReadWorkspaceFile(context.Background(), target, "run-1", "environment-canary", "attempt-1", absPath)

	require.NoError(t, err)
	assert.Equal(t, "canary-1", content)
	require.Len(t, fakeClient.dispatchRequests, 1)
	dispatch := fakeClient.dispatchRequests[0]
	assert.Equal(t, string(constants.ActionTypeFsRead), dispatch.ActionType)
	assert.Equal(t, target.SessionID, dispatch.TargetOperatorSessionID)
	assert.Equal(t, absPath, dispatch.TargetResource)
	fsRead := &operatorv1.FsReadRequested{}
	require.NoError(t, proto.Unmarshal(dispatch.Payload, fsRead))
	assert.Equal(t, absPath, fsRead.Path)
	assert.Equal(t, "attempt-1", fsRead.ExecutionId)
}

func TestCommandLane_ReadWorkspaceFile_FailsClosed(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	failed, err := proto.Marshal(&operatorv1.FsReadResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, ErrorMessage: "no such file"})
	require.NoError(t, err)
	completed := laneCompletedReceipt()
	tests := []struct {
		name    string
		outcome *client.GovernedOutcome
		err     error
	}{
		{name: "transport error", err: errors.New("transport unavailable")},
		{name: "dispatch rejected", outcome: laneRejectionOutcome(t)},
		{name: "untyped answer", outcome: laneUntypedOutcome()},
		{name: "receipt that failed", outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED}})},
		{name: "no result payload", outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: completed})},
		{name: "operator read failed", outcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ResultPayload: failed, Receipt: completed})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{dispatchOutcome: tt.outcome, dispatchErr: tt.err}, client.Persona{})

			_, err := lane.ReadWorkspaceFile(context.Background(), target, "run-1", "environment-canary", "attempt-1", "/home/op/workspaces/ws-0123456789abcdef/missing.txt")

			require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
		})
	}
}

func TestCommandLane_ReadWorkspaceFile_RequiresOperatorTargetAndPath(t *testing.T) {
	lane := NewCommandLane(&laneTestClient{}, client.Persona{})

	_, err := lane.ReadWorkspaceFile(context.Background(), Target{}, "run-1", "scenario-1", "attempt-1", "")

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// TestCommandLane_DispatchAgentTool_MapsEachAnswerKindToItsOutcome checks that
// a model-visible answer is handed back as the typed outcome it arrived as, with
// the receipt's transaction and dispatch result read from that outcome.
func TestCommandLane_DispatchAgentTool_MapsEachAnswerKindToItsOutcome(t *testing.T) {
	target := Target{OperatorID: "operator-1", SessionID: "session-1"}
	tests := []struct {
		name          string
		outcome       *client.GovernedOutcome
		kind          evalv1.ConformanceObservationKind
		transactionID string
		resultPayload []byte
	}{
		{
			name:          "receipt",
			outcome:       laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ResultPayload: []byte("result"), Receipt: laneCompletedReceipt()}),
			kind:          evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
			transactionID: "tx-1",
			resultPayload: []byte("result"),
		},
		{
			name:          "governance rejection",
			outcome:       laneRejectionOutcome(t),
			kind:          evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			transactionID: "tx-rejected",
		},
		{
			name:    "ingress refusal",
			outcome: laneRefusalOutcome(t, http.StatusBadRequest, &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, Error: "malformed"}),
			kind:    evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{dispatchOutcome: tt.outcome}, client.Persona{CLISessionID: "cli-session-1"})

			dispatched, err := lane.DispatchAgentTool(context.Background(), target, "run-1", "scenario-1", "attempt-1", constants.ActionTypeFileEdit, "/home/op/workspaces/ws-0123456789abcdef/out.txt", []byte("payload"))

			require.NoError(t, err)
			assert.Same(t, tt.outcome, dispatched.Outcome)
			assert.Equal(t, tt.kind, dispatched.Outcome.Observation.GetKind())
			assert.Equal(t, tt.transactionID, dispatched.TransactionID)
			assert.Equal(t, tt.resultPayload, dispatched.ResultPayload)
		})
	}
}

func TestCommandLane_DispatchAgentTool_FailsClosedOnTransportUntypedAndSuspendedAnswers(t *testing.T) {
	tests := []struct {
		name    string
		outcome *client.GovernedOutcome
		err     error
		typed   error
	}{
		{name: "transport error", err: errors.New("transport unavailable")},
		{name: "no answer"},
		{name: "untyped answer", outcome: laneUntypedOutcome(), typed: constants.ErrIngressResponseUntyped},
		{name: "suspended for approval", outcome: laneSuspendedOutcome("tx-suspended")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lane := NewCommandLane(&laneTestClient{dispatchOutcome: tt.outcome, dispatchErr: tt.err}, client.Persona{})

			_, err := lane.DispatchAgentTool(context.Background(), Target{OperatorID: "operator-1", SessionID: "session-1"}, "run-1", "scenario-1", "attempt-1", constants.ActionTypeFileEdit, "/home/op/out.txt", []byte("payload"))

			require.ErrorIs(t, err, constants.ErrEvaluationDispatchFailed)
			if tt.typed != nil {
				require.ErrorIs(t, err, tt.typed)
			}
		})
	}
}

func TestCommandLane_DispatchAgentTool_RequiresOperatorTargetScopeAndPayload(t *testing.T) {
	lane := NewCommandLane(&laneTestClient{}, client.Persona{})

	_, err := lane.DispatchAgentTool(context.Background(), Target{}, "run-1", "scenario-1", "attempt-1", constants.ActionTypeFileEdit, "/home/op/out.txt", []byte("payload"))

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// TestCommandLane_AnswerMessageRendersTypedMessageNotHTTPStatus pins what the
// model is shown for a denied answer: the typed rejection error, or the refusal
// code and error. The HTTP status never appears in the text.
func TestCommandLane_AnswerMessageRendersTypedMessageNotHTTPStatus(t *testing.T) {
	assert.Equal(t, "rejected", answerMessage(laneRejectionOutcome(t)))
	assert.Equal(t, "INGRESS_REFUSAL_CODE_MALFORMED_REQUEST: malformed", answerMessage(laneRefusalOutcome(t, http.StatusBadRequest, &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST, Error: "malformed"})))
	assert.Empty(t, answerMessage(laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", Receipt: laneCompletedReceipt()})))
	assert.Empty(t, answerMessage(nil))
}

// laneCanonicalBody renders msg with the canonical protojson marshaller, the
// bytes the Gateway writes for the same answer.
func laneCanonicalBody(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(msg)
	require.NoError(t, err)
	return body
}

// laneCompletedReceipt is the receipt of a completed execution of transaction tx-1.
func laneCompletedReceipt() *operatorv1.ActionReceipt {
	return &operatorv1.ActionReceipt{TransactionId: "tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
}

// laneReceiptOutcome is the typed outcome of a 200 dispatch the Operator answered with a receipt.
func laneReceiptOutcome(t *testing.T, dispatched *operatorv1.GovernedDispatchResult) *client.GovernedOutcome {
	t.Helper()
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, HttpStatus: http.StatusOK, Receipt: dispatched.GetReceipt()},
		Dispatch:    dispatched,
		Body:        laneCanonicalBody(t, dispatched),
	}
}

// laneRejectionOutcome is the typed outcome of a signed governance rejection, answered 403.
func laneRejectionOutcome(t *testing.T) *client.GovernedOutcome {
	t.Helper()
	rejection := laneRejection()
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, HttpStatus: http.StatusForbidden, Rejection: rejection},
		Body:        laneCanonicalBody(t, rejection),
	}
}

// laneRefusalOutcome is the typed outcome of a pre-envelope ingress refusal.
func laneRefusalOutcome(t *testing.T, status int, refusal *operatorv1.IngressRefusal) *client.GovernedOutcome {
	t.Helper()
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, HttpStatus: int32(status), Refusal: refusal},
		Body:        laneCanonicalBody(t, refusal),
	}
}

// laneUntypedOutcome is an answer that carries no typed outcome.
func laneUntypedOutcome() *client.GovernedOutcome {
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED, HttpStatus: http.StatusInternalServerError},
		Body:        []byte("internal failure"),
	}
}

// laneSuspendedOutcome is an answer that waits on human approval.
func laneSuspendedOutcome(txHash string) *client.GovernedOutcome {
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED, HttpStatus: http.StatusAccepted, SuspendedTxHash: txHash},
	}
}

// laneRejection is the governance rejection the Operator signs for a refused dispatch.
func laneRejection() *operatorv1.GovernanceRejection {
	return &operatorv1.GovernanceRejection{Error: "rejected", Receipt: &operatorv1.ActionReceipt{TransactionId: "tx-rejected"}}
}

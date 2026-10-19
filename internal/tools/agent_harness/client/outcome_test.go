// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const outcomeTestSkill = "list_directory"

func outcomeReceipt(txID string) *operatorv1.ActionReceipt {
	return &operatorv1.ActionReceipt{
		TransactionId:   txID,
		TransactionHash: "hash-" + txID,
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}
}

func outcomeEnvelope() *commonv1.GovernanceEnvelope {
	return &commonv1.GovernanceEnvelope{
		Id:              "test-id",
		TransactionHash: "test-hash",
		ProtocolVersion: govpkg.GovernanceProtocolVersionV2,
		ActionType:      "TEST_ACTION",
	}
}

func outcomeDispatchRequest() DispatchCommandRequest {
	return DispatchCommandRequest{
		TargetOperatorSessionID: "session-1",
		ActionType:              string(constants.ActionTypeFileEdit),
		Payload:                 []byte("payload"),
		CLISessionID:            "cli-1",
	}
}

func outcomeRejection(txID string) *operatorv1.GovernanceRejection {
	return &operatorv1.GovernanceRejection{Error: "L4 rejected the envelope", Receipt: outcomeReceipt(txID)}
}

func outcomeRefusal(code operatorv1.IngressRefusalCode) *operatorv1.IngressRefusal {
	return &operatorv1.IngressRefusal{Code: code, Error: "refused before envelope"}
}

// canonicalBody renders msg with the T06 marshaller, the bytes the Gateway writes.
func canonicalBody(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(msg)
	require.NoError(t, err)
	return body
}

// outcomeClient serves one fixed answer and returns a client pointed at it.
func outcomeClient(t *testing.T, status int, body []byte) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)
	return client
}

// rpcAnswer renders a JSON-RPC envelope with either a result or an error.
func rpcAnswer(t *testing.T, result json.RawMessage, errData json.RawMessage) []byte {
	t.Helper()
	envelope := mcp.JSONRPCResponse{JSONRPC: "2.0", ID: 1}
	if errData != nil {
		envelope.Error = &mcp.JSONRPCError{Code: -32000, Message: "governed call failed", Data: errData}
	} else {
		envelope.Result = result
	}
	body, err := json.Marshal(envelope)
	require.NoError(t, err)
	return body
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func TestGovernedOutcomeTyped(t *testing.T) {
	tests := []struct {
		name        string
		outcome     *GovernedOutcome
		wantUntyped bool
	}{
		{name: "receipt is typed", outcome: receiptOutcome(outcomeReceipt("tx-1"))},
		{name: "rejection is typed", outcome: rejectionOutcome(outcomeRejection("tx-1"))},
		{name: "refusal is typed", outcome: refusalOutcome(outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST))},
		{name: "suspension is typed", outcome: suspendedOutcome(&mcp.GovernedSuspension{TxHash: "tx-2"})},
		{name: "untyped is untyped", outcome: untypedOutcome(), wantUntyped: true},
		{name: "unspecified kind is untyped", outcome: &GovernedOutcome{Observation: &evalv1.ConformanceObservation{}}, wantUntyped: true},
		{name: "missing observation is untyped", outcome: &GovernedOutcome{}, wantUntyped: true},
		{name: "nil outcome is untyped", outcome: nil, wantUntyped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.outcome.Typed()
			if tt.wantUntyped {
				require.Error(t, err)
				assert.True(t, errors.Is(err, constants.ErrIngressResponseUntyped))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestSubmitEnvelope_ClassifiesDirectAnswers(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "200 receipt",
			status:   http.StatusOK,
			body:     canonicalBody(t, outcomeReceipt("tx-1")),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:     "403 rejection",
			status:   http.StatusForbidden,
			body:     canonicalBody(t, outcomeRejection("tx-1")),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:     "400 refusal",
			status:   http.StatusBadRequest,
			body:     canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST)),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:     "500 text body",
			status:   http.StatusInternalServerError,
			body:     []byte("internal error"),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "200 body with unknown field",
			status:   http.StatusOK,
			body:     []byte(`{"transaction_id":"tx-1","unexpected_field":true}`),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "200 receipt with trailing text",
			status:   http.StatusOK,
			body:     append(canonicalBody(t, outcomeReceipt("tx-1")), []byte(" trailing")...),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := outcomeClient(t, tt.status, tt.body)
			outcome, err := client.SubmitEnvelope(context.Background(), Persona{ID: "test"}, outcomeEnvelope())
			require.NoError(t, err)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
			assert.Equal(t, tt.status, int(outcome.Observation.GetHttpStatus()))
			assert.Equal(t, tt.body, outcome.Body)
		})
	}
}

func TestSubmitEnvelope_TransportFailureIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	outcome, err := client.SubmitEnvelope(context.Background(), Persona{ID: "test"}, outcomeEnvelope())
	require.Error(t, err)
	assert.Nil(t, outcome)
}

func TestDispatchCommand_ClassifiesDispatchAnswers(t *testing.T) {
	dispatched := &operatorv1.GovernedDispatchResult{TransactionId: "tx-1", ActionType: string(constants.ActionTypeFileEdit), Receipt: outcomeReceipt("tx-1")}
	noReceipt := &operatorv1.GovernedDispatchResult{TransactionId: "tx-1"}
	tests := []struct {
		name     string
		status   int
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "200 dispatch result with receipt",
			status:   http.StatusOK,
			body:     canonicalBody(t, dispatched),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:     "200 dispatch result without receipt",
			status:   http.StatusOK,
			body:     canonicalBody(t, noReceipt),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "404 unknown operator session refusal",
			status:   http.StatusNotFound,
			body:     canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND)),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:     "403 rejection",
			status:   http.StatusForbidden,
			body:     canonicalBody(t, outcomeRejection("tx-1")),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := outcomeClient(t, tt.status, tt.body)
			outcome, err := client.DispatchCommand(context.Background(), Persona{ID: "test", CLISessionID: "cli-1"}, outcomeDispatchRequest())
			require.NoError(t, err)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
			if tt.wantKind == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT {
				require.NotNil(t, outcome.Dispatch)
				assert.Equal(t, "tx-1", outcome.Dispatch.GetTransactionId())
			} else {
				assert.Nil(t, outcome.Dispatch)
			}
		})
	}
}

func TestDispatchCommand_RefusalCodeIsTyped(t *testing.T) {
	client := outcomeClient(t, http.StatusNotFound, canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND)))

	outcome, err := client.DispatchCommand(context.Background(), Persona{ID: "test", CLISessionID: "cli-1"}, outcomeDispatchRequest())

	require.NoError(t, err)
	require.NotNil(t, outcome.Observation.GetRefusal())
	assert.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND, outcome.Observation.GetRefusal().GetCode())
}

func TestDispatchCommandBody_SendsBodyUnchanged(t *testing.T) {
	malformed := []byte(`{"target_operator_session_id":`)
	client := outcomeClient(t, http.StatusBadRequest, canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST)))

	outcome, err := client.DispatchCommandBody(context.Background(), Persona{ID: "test", CLISessionID: "cli-1"}, malformed)

	require.NoError(t, err)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL, outcome.Observation.GetKind())
}

func TestDispatchCommand_TransportFailureIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	outcome, err := client.DispatchCommand(context.Background(), Persona{ID: "test", CLISessionID: "cli-1"}, outcomeDispatchRequest())
	require.Error(t, err)
	assert.Nil(t, outcome)
}

func TestMCPToolsCall_ClassifiesResults(t *testing.T) {
	receiptJSON := canonicalBody(t, outcomeReceipt("tx-1"))
	suspension := &mcp.GovernedSuspension{ID: "tx-2", Status: "suspended", TxHash: "tx-2", ApprovalURL: "https://example.test/approve/tx-2", Message: "approval pending"}
	tests := []struct {
		name     string
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "result carries receipt",
			body:     rpcAnswer(t, mustJSON(t, mcp.CallToolResult{Content: []mcp.TextContent{{Type: "text", Text: "ok"}}, Receipt: receiptJSON}), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:     "result carries suspension",
			body:     rpcAnswer(t, mustJSON(t, mcp.CallToolResult{Suspension: suspension}), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
		},
		{
			name:     "error data is rejection",
			body:     rpcAnswer(t, nil, canonicalBody(t, outcomeRejection("tx-3"))),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:     "error data is refusal",
			body:     rpcAnswer(t, nil, canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNKNOWN_EVENT_TYPE))),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:     "error without data is untyped",
			body:     []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed"}}`),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "result with unknown field is untyped",
			body:     rpcAnswer(t, json.RawMessage(`{"content":[],"unexpected":true}`), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "result without receipt or suspension is untyped",
			body:     rpcAnswer(t, json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:     "non-JSON body is untyped",
			body:     []byte("gateway unavailable"),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := outcomeClient(t, http.StatusOK, tt.body)
			outcome, err := client.MCPToolsCall(context.Background(), Persona{ID: "test"}, mcp.CallToolRequest{
				Name:      (&mcp.FileReadTool{}).Name(),
				Arguments: mustJSON(t, mcp.FileReadRequest{Path: "."}),
			})
			require.NoError(t, err)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
			if tt.wantKind == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED {
				require.NotNil(t, outcome.Suspension)
				assert.Equal(t, "tx-2", outcome.Observation.GetSuspendedTxHash())
			}
		})
	}
}

func TestA2ACall_ClassifiesResults(t *testing.T) {
	receiptJSON := canonicalBody(t, outcomeReceipt("tx-1"))
	suspension := &mcp.GovernedSuspension{ID: "tx-2", Status: "suspended", TxHash: "tx-2", ApprovalURL: "https://example.test/approve/tx-2", Message: "approval pending"}
	tests := []struct {
		name     string
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "success wraps receipt",
			body:     rpcAnswer(t, mustJSON(t, mcp.A2ASuccessResponse{ID: "tx-1", Result: receiptJSON}), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:     "result is suspension",
			body:     rpcAnswer(t, mustJSON(t, suspension), nil),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
		},
		{
			name:     "error data is rejection",
			body:     rpcAnswer(t, nil, canonicalBody(t, outcomeRejection("tx-3"))),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:     "error data is refusal",
			body:     rpcAnswer(t, nil, canonicalBody(t, outcomeRefusal(operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST))),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:     "error without data is untyped",
			body:     []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed"}}`),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := outcomeClient(t, http.StatusOK, tt.body)
			outcome, err := client.A2ACall(context.Background(), Persona{ID: "test"}, mcp.A2ACallRequest{
				SkillName:   outcomeTestSkill,
				Payload:     json.RawMessage(`{"path":"."}`),
				ExecutionID: "exec-1",
			})
			require.NoError(t, err)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.wantKind, outcome.Observation.GetKind())
		})
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

func TestGovernedOutcomeTyped_Unit(t *testing.T) {
	t.Parallel()
	receipt := &operatorv1.EventReceipt{TransactionId: "tx-1", TransactionHash: "hash-tx-1"}
	cases := []struct {
		name    string
		outcome *GovernedOutcome
		untyped bool
	}{
		{name: "receipt", outcome: receiptOutcome(receipt)},
		{name: "rejection", outcome: rejectionOutcome(&operatorv1.GovernanceRejection{Error: "rejected", Receipt: receipt})},
		{name: "refusal", outcome: refusalOutcome(&operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST})},
		{name: "suspension", outcome: suspendedOutcome(&mcp.GovernedSuspension{TxHash: "tx-2"})},
		{name: "untyped", outcome: untypedOutcome(), untyped: true},
		{name: "nil", outcome: nil, untyped: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.outcome.Typed()
			if tt.untyped {
				require.ErrorIs(t, err, constants.ErrIngressResponseUntyped)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestMCPStdioOutcome_ClassifiesTheBridgeLine(t *testing.T) {
	t.Parallel()
	receipt := canonicalProto(t, &operatorv1.EventReceipt{TransactionId: "tx-1", TransactionHash: "hash-tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED})
	suspension, err := json.Marshal(mcp.CallToolResult{Suspension: &mcp.GovernedSuspension{ID: "tx-2", Status: "suspended", TxHash: "tx-2"}})
	require.NoError(t, err)

	cases := []struct {
		name string
		body []byte
		kind evalv1.ConformanceObservationKind
	}{
		{name: "receipt", body: rpcLine(t, json.RawMessage(mustRaw(t, mcp.CallToolResult{Receipt: receipt})), nil), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{name: "suspension", body: rpcLine(t, suspension, nil), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED},
		{name: "rejection", body: rpcLine(t, nil, canonicalProto(t, &operatorv1.GovernanceRejection{Error: "rejected", Receipt: &operatorv1.EventReceipt{TransactionId: "tx-3", TransactionHash: "hash-tx-3"}})), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION},
		{name: "error without data", body: []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"failed"}}`), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
		{name: "not json", body: []byte("gateway unavailable"), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome := MCPStdioOutcome(tt.body)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.kind, outcome.Observation.GetKind())
			assert.Equal(t, int32(0), outcome.Observation.GetHttpStatus())
			assert.Equal(t, tt.body, outcome.Body)
			if tt.kind == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED {
				require.NotNil(t, outcome.Suspension)
				assert.Equal(t, "tx-2", outcome.Observation.GetSuspendedTxHash())
			}
		})
	}
}

func TestIntentCommandOutcome_ClassifiesTheApprovedCommand(t *testing.T) {
	t.Parallel()
	receipt := &operatorv1.EventReceipt{TransactionId: "tx-1", TransactionHash: "hash-tx-1"}
	cases := []struct {
		name   string
		result *tribunalv1.TribunalIntentResult
		kind   evalv1.ConformanceObservationKind
	}{
		{
			name: "dispatched receipt",
			result: &tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: &operatorv1.GovernedDispatchResult{
				TransactionId: "tx-1",
				Receipt:       receipt,
			}}},
			kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:   "dispatched without receipt",
			result: &tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: &operatorv1.GovernedDispatchResult{TransactionId: "tx-1"}}},
			kind:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
		{
			name:   "rejection",
			result: &tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Rejection{Rejection: &operatorv1.GovernanceRejection{Error: "rejected", Receipt: receipt}}},
			kind:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:   "refusal",
			result: &tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Refusal{Refusal: &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST}}},
			kind:   evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{name: "empty", result: &tribunalv1.TribunalIntentResult{}, kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome := IntentCommandOutcome(tt.result)
			require.NotNil(t, outcome)
			assert.Equal(t, tt.kind, outcome.Observation.GetKind())
			if tt.kind == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT {
				require.NotNil(t, outcome.Dispatch)
				assert.Equal(t, "tx-1", outcome.Dispatch.GetTransactionId())
			}
		})
	}
}

func TestDecodeHTTPOutcome_ClassifiesDirectAnswersWithoutAListener(t *testing.T) {
	t.Parallel()
	receipt := canonicalProto(t, &operatorv1.EventReceipt{TransactionId: "tx-1", TransactionHash: "hash-tx-1", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED})
	cases := []struct {
		name   string
		status int
		body   []byte
		kind   evalv1.ConformanceObservationKind
	}{
		{name: "receipt", status: http.StatusOK, body: receipt, kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT},
		{name: "rejection", status: http.StatusForbidden, body: canonicalProto(t, &operatorv1.GovernanceRejection{Error: "rejected", Receipt: &operatorv1.EventReceipt{TransactionId: "tx-1", TransactionHash: "hash-tx-1"}}), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION},
		{name: "text", status: http.StatusInternalServerError, body: []byte("internal error"), kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			outcome := decodeHTTPOutcome(tt.status, tt.body, &operatorv1.EventReceipt{})
			require.Equal(t, tt.kind, outcome.Observation.GetKind())
			assert.Equal(t, tt.body, outcome.Body)
			assert.Equal(t, int32(tt.status), outcome.Observation.GetHttpStatus())
			if tt.kind == evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED {
				assert.Equal(t, tt.body, outcome.Observation.GetUntypedBody())
			}
		})
	}
}

type scriptedRoundTrip func(*http.Request) (*http.Response, error)

func (f scriptedRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func scriptedClient(t *testing.T, status int, body string, wantPath string) *Client {
	t.Helper()
	client, err := New(config.Config{MTLSBaseURL: "https://gateway.test", PublicBaseURL: "https://public.test"})
	require.NoError(t, err)
	client.http.Transport = scriptedRoundTrip(func(req *http.Request) (*http.Response, error) {
		if wantPath != "" {
			assert.Equal(t, wantPath, req.URL.String())
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	return client
}

func TestHealth_DecodesPostureAndRejectsABadAnswer(t *testing.T) {
	t.Parallel()
	client := scriptedClient(t, http.StatusOK, `{"status":"running","mode":"gateway","posture":"doctrine"}`, "https://gateway.test"+constants.APIPaths.Health)
	health, raw, err := client.Health(context.Background())
	require.NoError(t, err)
	assert.Equal(t, constants.PostureDoctrine, health.Posture)
	assert.Contains(t, string(raw), `"posture":"doctrine"`)

	missing := scriptedClient(t, http.StatusOK, `{"status":"running"}`, "")
	_, _, err = missing.Health(context.Background())
	require.ErrorIs(t, err, constants.ErrInvalidJSONResponse)

	failed := scriptedClient(t, http.StatusServiceUnavailable, `{}`, "")
	_, _, err = failed.Health(context.Background())
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
}

func TestStateRoot_ReadsTheMerkleRoot(t *testing.T) {
	t.Parallel()
	client := scriptedClient(t, http.StatusOK, `{"state_merkle_root":"abc123"}`, "https://public.test"+constants.APIPaths.State)
	root, err := client.StateRoot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "abc123", root)

	mtls := scriptedClient(t, http.StatusOK, `{"state_merkle_root":"from-mtls"}`, "https://gateway.test"+constants.APIPaths.State)
	root, err = mtls.StateRootFromMTLS(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "from-mtls", root)

	empty := scriptedClient(t, http.StatusOK, `{}`, "")
	_, err = empty.StateRoot(context.Background())
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestEnrollSignerApplication_ValidatesInput(t *testing.T) {
	t.Parallel()
	client, err := New(config.Config{MTLSBaseURL: "https://gateway.test"})
	require.NoError(t, err)

	// Fails on invalid app name
	_, err = client.EnrollSignerApplication(context.Background(), "INVALID_APP_NAME", nil, nil, nil)
	require.Error(t, err)

	// Fails on missing file service
	_, err = client.EnrollSignerApplication(context.Background(), "valid-app", nil, nil, nil)
	require.Error(t, err)
}

func canonicalProto(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(msg)
	require.NoError(t, err)
	return body
}

func mustRaw(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func rpcLine(t *testing.T, result json.RawMessage, errData json.RawMessage) []byte {
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

func TestGovernedOutcomePreservesConsensusInferenceEvidence(t *testing.T) {
	vote := &operatorv1.ConsensusVoteInference{SubjectTransactionId: "subject", TransactionId: "vote", Receipt: &operatorv1.EventReceipt{TransactionId: "vote"}}
	votes := []*operatorv1.ConsensusVoteInference{vote}
	receipt := &operatorv1.EventReceipt{TransactionId: "subject"}
	dispatched := &operatorv1.GovernedDispatchResult{Receipt: receipt, ConsensusInferences: votes}
	rejection := &operatorv1.GovernanceRejection{Receipt: receipt, ConsensusInferences: votes}
	refusal := &operatorv1.IngressRefusal{Code: operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, ConsensusInferences: votes}
	callBody, err := json.Marshal(mcp.CallToolResult{Receipt: canonicalProto(t, receipt), ConsensusInferences: governance.WrapConsensusInferences(votes)})
	require.NoError(t, err)
	a2aBody, err := json.Marshal(mcp.A2ASuccessResponse{Result: canonicalProto(t, receipt), ConsensusInferences: governance.WrapConsensusInferences(votes)})
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		outcome *GovernedOutcome
	}{
		{"dispatch", decodeHTTPOutcome(http.StatusOK, canonicalProto(t, dispatched), &operatorv1.GovernedDispatchResult{})},
		{"rejection", decodeHTTPOutcome(http.StatusForbidden, canonicalProto(t, rejection), &operatorv1.GovernedDispatchResult{})},
		{"refusal", decodeHTTPOutcome(http.StatusServiceUnavailable, canonicalProto(t, refusal), &operatorv1.GovernedDispatchResult{})},
		{"MCP", decodeMCPResult(callBody)},
		{"A2A", decodeA2AResult(a2aBody)},
		{"Tribunal intent", IntentCommandOutcome(&tribunalv1.TribunalIntentResult{CommandOutcome: &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: dispatched}})},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, test.outcome.Typed())
			require.Len(t, test.outcome.ConsensusInferences, 1)
			assert.True(t, proto.Equal(vote, test.outcome.ConsensusInferences[0]))
		})
	}
}

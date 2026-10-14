// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// recordedA2ACall is one a2a/call the fake Gateway received.
type recordedA2ACall struct {
	SkillName   string          `json:"skill_name"`
	Payload     json.RawMessage `json:"payload"`
	ExecutionID string          `json:"execution_id"`
}

// fakeA2AGateway answers every a2a/call with the same canned JSON-RPC body. When
// downstream is set it first calls the stub the way the Gateway calls its A2A
// downstream, so the attempt's stub calls are recorded before the answer.
type fakeA2AGateway struct {
	callBody   []byte
	downstream *A2AStub

	mu    sync.Mutex
	calls []recordedA2ACall
}

func (g *fakeA2AGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if r.URL.Path != constants.APIPaths.A2ACall || json.Unmarshal(raw, &req) != nil || req.Method != "a2a/call" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	var call recordedA2ACall
	if err := json.Unmarshal(req.Params, &call); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	g.calls = append(g.calls, call)
	g.mu.Unlock()

	if g.downstream != nil {
		body, err := json.Marshal(mcp.A2ADownstreamRequest{SkillName: call.SkillName, Payload: call.Payload})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		resp, err := stubClient.Post(g.downstream.URL(), "application/json", bytes.NewReader(body))
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = resp.Body.Close()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(g.callBody)
}

// recorded returns a copy of the a2a/call requests received so far.
func (g *fakeA2AGateway) recorded() []recordedA2ACall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedA2ACall(nil), g.calls...)
}

// newFakeA2A starts the fake Gateway and an A2A driver whose stub is a live stub.
func newFakeA2A(t *testing.T, g *fakeA2AGateway) (*A2A, *A2AStub) {
	t.Helper()
	stub := startStub(t)
	g.downstream = stub
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	return NewA2A(c, testIdentity().Persona, stub), stub
}

// a2aResultBody renders an a2a/call success whose result is the given typed value.
func a2aResultBody(t *testing.T, result any) []byte {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	body, err := json.Marshal(rpcResponse(raw))
	require.NoError(t, err)
	return body
}

// a2aAttempt builds an attempt for one fault with the fixed marker used by the A2A tests.
func a2aAttempt(c cases.Case) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case:      c,
		Marker:    "marker-1",
	}
}

// TestA2APayloadAndSkillPerFault pins the skill, the execution ID and the payload each fault sends.
func TestA2APayloadAndSkillPerFault(t *testing.T) {
	tests := []struct {
		name        string
		c           cases.Case
		wantPayload string
	}{
		{
			name:        "none sends the marker",
			c:           cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "unattended mutation sends the marker",
			c:           cases.Case{ID: "row-23", Fault: cases.FaultUnattendedMutation, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "attended approve sends the marker",
			c:           cases.Case{ID: "row-24", Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation},
			wantPayload: `{"marker":"marker-1"}`,
		},
		{
			name:        "doctrine sends the corpus command as cmd",
			c:           cases.Case{ID: "row-14", Fault: cases.FaultDoctrine, Action: cases.ActionMutation, Command: "rm -rf /"},
			wantPayload: `{"cmd":"rm -rf /"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeA2AGateway{callBody: a2aResultBody(t, mcp.A2ASuccessResponse{ID: "tx-1", Result: receiptBody(t)})}
			d, _ := newFakeA2A(t, g)

			driven, err := d.Drive(context.Background(), a2aAttempt(tt.c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)

			calls := g.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, ConformanceA2ASkill, calls[0].SkillName)
			assert.Equal(t, "attempt-1", calls[0].ExecutionID)
			assert.JSONEq(t, tt.wantPayload, string(calls[0].Payload))
		})
	}
}

func TestA2AUnsupportedFaultSendsNothingAndOpensNoAttempt(t *testing.T) {
	g := &fakeA2AGateway{callBody: a2aResultBody(t, mcp.A2ASuccessResponse{ID: "tx-1", Result: receiptBody(t)})}
	d, stub := newFakeA2A(t, g)

	_, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-4", Fault: cases.FaultIDMismatch}))
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
	assert.Empty(t, g.recorded(), "an unsupported fault must not reach the Gateway")
	assert.Empty(t, stub.current, "an unsupported fault must not open a stub attempt")
}

// TestA2AObservationKinds maps each typed answer to its observation kind.
func TestA2AObservationKinds(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "signed receipt in success result",
			body:     a2aResultBody(t, mcp.A2ASuccessResponse{ID: "tx-1", Result: receiptBody(t)}),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT,
		},
		{
			name:     "governance rejection in error data",
			body:     rpcErrorBody(t, rejectionBody(t)),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
		},
		{
			name:     "ingress refusal in error data",
			body:     rpcErrorBody(t, refusalBody(t)),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REFUSAL,
		},
		{
			name:     "suspension in result",
			body:     a2aResultBody(t, mcp.GovernedSuspension{ID: "s-1", Status: "PENDING", TxHash: "tx-hash-1"}),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_SUSPENDED,
		},
		{
			name: "JSON-RPC error without data is untyped",
			body: func() []byte {
				body, err := json.Marshal(client.JSONRPCResponse{JSONRPC: "2.0", ID: 1, Error: &client.JSONRPCError{Code: -32603, Message: "boom"}})
				require.NoError(t, err)
				return body
			}(),
			wantKind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_UNTYPED,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newFakeA2A(t, &fakeA2AGateway{callBody: tt.body})
			driven, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone}))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			assert.Equal(t, tt.wantKind, driven.Observations[0].GetKind())
		})
	}
}

// TestA2ACallsHoldStubCallsForAttempt checks that the calls the Gateway made to the stub
// during the attempt come back in Driven.A2ACalls, and that the attempt is closed afterwards.
func TestA2ACallsHoldStubCallsForAttempt(t *testing.T) {
	g := &fakeA2AGateway{callBody: a2aResultBody(t, mcp.A2ASuccessResponse{ID: "tx-1", Result: receiptBody(t)})}
	d, stub := newFakeA2A(t, g)

	driven, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation}))
	require.NoError(t, err)
	require.Len(t, driven.A2ACalls, 1, "exactly one stub call carrying the marker")
	assert.Equal(t, ConformanceA2ASkill, driven.A2ACalls[0].GetSkillName())
	assert.JSONEq(t, `{"marker":"marker-1"}`, driven.A2ACalls[0].GetPayloadJson())
	assert.Empty(t, stub.current, "the attempt must be closed after Drive returns")
}

// TestA2ARejectedCallReturnsNoStubCalls checks a doctrine rejection that never reaches the downstream.
func TestA2ARejectedCallReturnsNoStubCalls(t *testing.T) {
	g := &fakeA2AGateway{callBody: rpcErrorBody(t, rejectionBody(t))}
	d, _ := newFakeA2A(t, g)
	// The rejected call never reaches the downstream, so the fake must not forward it.
	g.downstream = nil

	driven, err := d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-14", Fault: cases.FaultDoctrine, Command: "rm -rf /"}))
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, driven.Observations[0].GetKind())
	assert.Empty(t, driven.A2ACalls)
}

// TestA2ATransportErrorIsAnErrorAndClosesAttempt checks that a transport failure is an
// error (no observation) and that the stub attempt is still closed.
func TestA2ATransportErrorIsAnErrorAndClosesAttempt(t *testing.T) {
	stub := startStub(t)
	server := httptest.NewServer(&fakeA2AGateway{})
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	d := NewA2A(c, testIdentity().Persona, stub)
	server.Close()

	_, err = d.Drive(context.Background(), a2aAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone}))
	require.Error(t, err)
	assert.False(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
	assert.Empty(t, stub.current, "a failed call must still close the stub attempt")
}

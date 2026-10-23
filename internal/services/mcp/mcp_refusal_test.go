// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// decodeRefusal decodes a JSON-RPC error.data as the typed IngressRefusal.
func decodeRefusal(t *testing.T, data json.RawMessage) *operatorv1.IngressRefusal {
	t.Helper()
	require.NotEmpty(t, data, "a pre-envelope refusal must carry error.data")
	var refusal operatorv1.IngressRefusal
	require.NoError(t, protojson.Unmarshal(data, &refusal))
	return &refusal
}

// failingBody is a request body whose read always fails.
type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

// refusalCase is one pre-envelope refusal and the typed answer it must give.
type refusalCase struct {
	name      string
	body      string
	readFails bool
	setup     func(g *GatewayService)
	code      int
	refusal   operatorv1.IngressRefusalCode
	sentinel  error
}

// runRefusalCase drives handle with the case body and asserts the JSON-RPC
// code, the sentinel-only message, and the typed IngressRefusal in error.data.
func runRefusalCase(t *testing.T, handle func(*GatewayService) http.HandlerFunc, c refusalCase) {
	t.Helper()
	g := newEndpointTestGatewayService()
	if c.setup != nil {
		c.setup(g)
	}

	var body io.Reader = strings.NewReader(c.body)
	if c.readFails {
		body = failingBody{}
	}
	r := httptest.NewRequest(http.MethodPost, "/", body)
	w := httptest.NewRecorder()
	handle(g)(w, r)

	require.Equal(t, http.StatusOK, w.Code)
	var resp JSONRPCResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, c.code, resp.Error.Code)
	require.Equal(t, c.sentinel.Error(), resp.Error.Message)

	refusal := decodeRefusal(t, resp.Error.Data)
	require.Equal(t, c.refusal, refusal.GetCode())
	require.Equal(t, c.sentinel.Error(), refusal.GetError())
}

// circuitOpenSetup opens the downstream circuit for the cooldown window and
// configures a downstream so the call reaches the circuit check.
func circuitOpenSetup(g *GatewayService) {
	g.downstreamURL = "http://127.0.0.1:1"
	g.circuitOpen = true
	g.lastFailure = time.Now()
	g.cooldownDuration = time.Minute
}

func TestHandleMCP_RefusalTableIsTypedIngressRefusal(t *testing.T) {
	t.Parallel()
	tests := []refusalCase{
		{
			name: "payload too large",
			body: `{"jsonrpc":"2.0","id":1,"method":"ping"}`,
			setup: func(g *GatewayService) {
				g.maxPayloadBytes = 8
			},
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE,
			sentinel: constants.ErrPayloadExceedsLimit,
		},
		{
			name:      "failed to read body",
			readFails: true,
			code:      -32603,
			refusal:   operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel:  constants.ErrRequestBodyRead,
		},
		{
			name:     "batch request",
			body:     `[{"jsonrpc":"2.0","id":1,"method":"ping"}]`,
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrJSONRPCBatchUnsupported,
		},
		{
			name:     "invalid JSON",
			body:     `{invalid json`,
			code:     constants.JSONRPCErrorCodeParseError,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrInvalidJSONBody,
		},
		{
			name:     "jsonrpc version not 2.0",
			body:     `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrJSONRPCVersionInvalid,
		},
		{
			name:     "method empty",
			body:     `{"jsonrpc":"2.0","id":1}`,
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrJSONRPCMethodRequired,
		},
		{
			name:     "method not found",
			body:     `{"jsonrpc":"2.0","id":1,"method":"no/such"}`,
			code:     -32601,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED,
			sentinel: constants.ErrJSONRPCMethodNotFound,
		},
		{
			name: "gateway not ready",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			setup: func(g *GatewayService) {
				g.envProc = nil
			},
			code:     -32603,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE,
			sentinel: constants.ErrGatewayNotReady,
		},
		{
			name:     "downstream circuit open",
			body:     `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
			setup:    circuitOpenSetup,
			code:     -32603,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE,
			sentinel: constants.ErrGatewayDownstreamUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runRefusalCase(t, func(g *GatewayService) http.HandlerFunc { return g.HandleMCP }, tt)
		})
	}
}

func TestHandleA2aCall_RefusalTableIsTypedIngressRefusal(t *testing.T) {
	t.Parallel()
	tests := []refusalCase{
		{
			name: "payload too large",
			body: `{"jsonrpc":"2.0","id":1,"method":"a2a/call"}`,
			setup: func(g *GatewayService) {
				g.maxPayloadBytes = 8
			},
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_PAYLOAD_TOO_LARGE,
			sentinel: constants.ErrPayloadExceedsLimit,
		},
		{
			name:      "failed to read body",
			readFails: true,
			code:      -32603,
			refusal:   operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel:  constants.ErrRequestBodyRead,
		},
		{
			name:     "invalid JSON",
			body:     `{invalid json`,
			code:     constants.JSONRPCErrorCodeParseError,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrInvalidJSONBody,
		},
		{
			name:     "jsonrpc version not 2.0",
			body:     `{"jsonrpc":"1.0","id":1,"method":"a2a/call"}`,
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrJSONRPCVersionInvalid,
		},
		{
			name:     "method empty",
			body:     `{"jsonrpc":"2.0","id":1}`,
			code:     -32600,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
			sentinel: constants.ErrJSONRPCMethodRequired,
		},
		{
			name:     "wrong method on A2A",
			body:     `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			code:     -32601,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED,
			sentinel: constants.ErrJSONRPCMethodNotFound,
		},
		{
			name: "gateway not ready",
			body: `{"jsonrpc":"2.0","id":1,"method":"a2a/call"}`,
			setup: func(g *GatewayService) {
				g.envProc = nil
			},
			code:     -32603,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE,
			sentinel: constants.ErrGatewayNotReady,
		},
		{
			name:     "downstream circuit open",
			body:     `{"jsonrpc":"2.0","id":1,"method":"a2a/call"}`,
			setup:    circuitOpenSetup,
			code:     -32603,
			refusal:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_GATEWAY_UNAVAILABLE,
			sentinel: constants.ErrGatewayDownstreamUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runRefusalCase(t, func(g *GatewayService) http.HandlerFunc { return g.HandleA2aCall }, tt)
		})
	}
}

func TestHandleMCP_MethodNotAllowedWritesIngressRefusal(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	r := httptest.NewRequest(http.MethodPut, "/mcp", nil)
	w := httptest.NewRecorder()
	g.HandleMCP(w, r)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	require.Equal(t, "POST, GET", w.Header().Get("Allow"))
	var refusal operatorv1.IngressRefusal
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, refusal.GetCode())
	require.Equal(t, constants.ErrMethodNotAllowed.Error(), refusal.GetError())
}

func TestHandleA2aCall_MethodNotAllowedWritesIngressRefusal(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	r := httptest.NewRequest(http.MethodGet, "/api/v1/a2a/call", nil)
	w := httptest.NewRecorder()
	g.HandleA2aCall(w, r)

	require.Equal(t, http.StatusMethodNotAllowed, w.Code)
	var refusal operatorv1.IngressRefusal
	require.NoError(t, protojson.Unmarshal(w.Body.Bytes(), &refusal))
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_METHOD_NOT_ALLOWED, refusal.GetCode())
	require.Equal(t, constants.ErrMethodNotAllowed.Error(), refusal.GetError())
}

func TestWrapDispatch_UnmappedError_RendersSentinelOnly(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()

	_, dErr := g.wrapDispatch(nil, fmt.Errorf("db at /secret/path: %w", constants.ErrInternal))

	require.NotNil(t, dErr)
	require.Equal(t, -32603, dErr.code)
	require.Equal(t, constants.ErrInternal.Error(), dErr.message)
	require.NotContains(t, dErr.message, "/secret/path")
	require.NotContains(t, string(dErr.data), "/secret/path")

	refusal := decodeRefusal(t, dErr.data)
	require.Equal(t, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED, refusal.GetCode())
	require.Equal(t, constants.ErrInternal.Error(), refusal.GetError())
}

func TestWrapDispatch_Rejection_KeepsGovernanceRejection(t *testing.T) {
	t.Parallel()
	g := newEndpointTestGatewayService()
	receipt, err := invalidL3ProofRejection(t)

	_, dErr := g.wrapDispatch(nil, err)

	require.NotNil(t, dErr)
	require.Equal(t, constants.ErrCodeL3ProofInvalid, dErr.code)
	var rejection operatorv1.GovernanceRejection
	require.NoError(t, protojson.Unmarshal(dErr.data, &rejection))
	require.Equal(t, dErr.message, rejection.GetError())
	require.Equal(t, receipt.GetSignature(), rejection.GetReceipt().GetSignature())
	require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID,
		rejection.GetReceipt().GetRejectionCode())
	require.True(t, governance.IsGovernanceRejectionReceipt(rejection.GetReceipt()))
}

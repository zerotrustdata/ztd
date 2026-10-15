// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// recordedToolCall is one tools/call the fake MCP Gateway received.
type recordedToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// fakeMCPGateway answers every tools/call with the same canned JSON-RPC body
// and records the call. It answers tools/list with listBody, or with both
// native tools when listBody is nil.
type fakeMCPGateway struct {
	callBody []byte
	listBody []byte

	mu    sync.Mutex
	calls []recordedToolCall
}

// nativeToolsListBody is the tools/list answer of a Gateway that serves the
// driver's two native tools and no downstream.
func nativeToolsListBody() []byte {
	return []byte(fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":%q},{"name":%q}]}}`,
		mcpShellTool, mcpReadTool))
}

func (g *fakeMCPGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var req struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || r.URL.Path != "/mcp" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "tools/list":
		body := g.listBody
		if body == nil {
			body = nativeToolsListBody()
		}
		_, _ = w.Write(body)
	case "tools/call":
		var params recordedToolCall
		if err := json.Unmarshal(req.Params, &params); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.calls = append(g.calls, params)
		g.mu.Unlock()
		_, _ = w.Write(g.callBody)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// recorded returns a copy of the tools/call requests received so far.
func (g *fakeMCPGateway) recorded() []recordedToolCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]recordedToolCall(nil), g.calls...)
}

// rpcResponse wraps a result in a JSON-RPC 2.0 success envelope.
func rpcResponse(result json.RawMessage) client.JSONRPCResponse {
	return client.JSONRPCResponse{JSONRPC: "2.0", ID: 1, Result: result}
}

// rpcErrorBody renders a JSON-RPC error envelope carrying data.
func rpcErrorBody(t *testing.T, data []byte) []byte {
	t.Helper()
	body, err := json.Marshal(client.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      1,
		Error:   &client.JSONRPCError{Code: -32000, Message: "refused", Data: data},
	})
	require.NoError(t, err)
	return body
}

// toolResultBody renders a tools/call success whose result is a CallToolResult.
func toolResultBody(t *testing.T, result mcp.CallToolResult) []byte {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	body, err := json.Marshal(rpcResponse(raw))
	require.NoError(t, err)
	return body
}

// newFakeMCP starts the fake Gateway and an MCP driver pointed at it.
func newFakeMCP(t *testing.T, g *fakeMCPGateway) *MCP {
	t.Helper()
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	m, err := NewMCP(context.Background(), c, testIdentity().Persona)
	require.NoError(t, err)
	return m
}

// mcpAttempt builds an attempt for one fault with fixed paths and marker.
func mcpAttempt(c cases.Case) cases.Attempt {
	return cases.Attempt{
		RunID:     "run-1",
		AttemptID: "attempt-1",
		Case:      c,
		TargetAbs: "/work/target.txt",
		ReadAbs:   "/work/read.txt",
		Marker:    "marker-1",
	}
}

// TestMCPNewDiscoversNativeToolsViaToolsList checks that discovery reads the
// Gateway's tools/list and accepts it when the native tools are listed.
func TestMCPNewDiscoversNativeToolsViaToolsList(t *testing.T) {
	server := httptest.NewServer(&fakeMCPGateway{})
	t.Cleanup(server.Close)
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	_, err = NewMCP(context.Background(), c, testIdentity().Persona)
	require.NoError(t, err)
}

// TestMCPNewToolsListDiscoveryFailures checks that discovery fails closed: a
// missing native tool is unsupported, and a tools/list error or a malformed
// result is an error that is not reported as an unsupported ingress.
func TestMCPNewToolsListDiscoveryFailures(t *testing.T) {
	tests := []struct {
		name        string
		listBody    []byte
		unsupported bool
		wantErr     error
	}{
		{
			name:        "run_shell_command missing",
			listBody:    []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":%q}]}}`, mcpReadTool)),
			unsupported: true,
		},
		{
			name:        "read_file missing",
			listBody:    []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":%q}]}}`, mcpShellTool)),
			unsupported: true,
		},
		{
			name:        "no tools",
			listBody:    []byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`),
			unsupported: true,
		},
		{
			name:     "result is not a tools list",
			listBody: []byte(`{"jsonrpc":"2.0","id":1,"result":"nope"}`),
			wantErr:  constants.ErrInvalidJSONResponse,
		},
		{
			name:     "rpc error",
			listBody: rpcErrorBody(t, nil),
			wantErr:  constants.ErrJSONRPCErrorResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(&fakeMCPGateway{listBody: tt.listBody})
			t.Cleanup(server.Close)
			c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
			require.NoError(t, err)
			_, err = NewMCP(context.Background(), c, testIdentity().Persona)
			require.Error(t, err)
			assert.Equal(t, tt.unsupported, errors.Is(err, constants.ErrConformanceIngressUnsupported))
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}

// TestMCPDriveArgumentsPerFault pins the tool and the exact arguments each fault sends.
func TestMCPDriveArgumentsPerFault(t *testing.T) {
	receipt := toolResultBody(t, mcp.CallToolResult{Receipt: receiptBody(t)})
	tests := []struct {
		name     string
		c        cases.Case
		wantTool string
		wantArgs string
	}{
		{
			name:     "none sends the marker write as argv",
			c:        cases.Case{ID: "row-1", Fault: cases.FaultNone, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"],"timeout":0}`,
		},
		{
			name:     "unattended mutation sends the same marker write",
			c:        cases.Case{ID: "row-23", Fault: cases.FaultUnattendedMutation, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"],"timeout":0}`,
		},
		{
			name:     "attended approve sends the same marker write",
			c:        cases.Case{ID: "row-24", Fault: cases.FaultAttendedApprove, Action: cases.ActionMutation},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"sh","args":["-c","printf '%s' \"$1\" > \"$2\"","sh","marker-1","/work/target.txt"],"timeout":0}`,
		},
		{
			name:     "read baseline sends the read path",
			c:        cases.Case{ID: "row-2", Fault: cases.FaultReadBaseline, Action: cases.ActionRead},
			wantTool: "read_file",
			wantArgs: `{"path":"/work/read.txt"}`,
		},
		{
			name:     "doctrine sends the corpus command as the command",
			c:        cases.Case{ID: "row-14", Fault: cases.FaultDoctrine, Action: cases.ActionMutation, Command: "rm -rf /"},
			wantTool: "run_shell_command",
			wantArgs: `{"command":"rm -rf /","args":null,"timeout":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &fakeMCPGateway{callBody: receipt}
			m := newFakeMCP(t, g)

			driven, err := m.Drive(context.Background(), mcpAttempt(tt.c))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)

			calls := g.recorded()
			require.Len(t, calls, 1)
			assert.Equal(t, tt.wantTool, calls[0].Name)
			assert.JSONEq(t, tt.wantArgs, string(calls[0].Arguments))
		})
	}
}

func TestMCPDriveUnsupportedFaultSendsNothing(t *testing.T) {
	g := &fakeMCPGateway{callBody: toolResultBody(t, mcp.CallToolResult{Receipt: receiptBody(t)})}
	m := newFakeMCP(t, g)

	_, err := m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-4", Fault: cases.FaultIDMismatch}))
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
	assert.Empty(t, g.recorded(), "an unsupported fault must not reach the Gateway")
}

// TestMCPDriveObservationKinds maps each typed answer to its observation kind.
func TestMCPDriveObservationKinds(t *testing.T) {
	tests := []struct {
		name     string
		body     []byte
		wantKind evalv1.ConformanceObservationKind
	}{
		{
			name:     "signed receipt in result",
			body:     toolResultBody(t, mcp.CallToolResult{Receipt: receiptBody(t)}),
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
			name: "suspension in result",
			body: toolResultBody(t, mcp.CallToolResult{Suspension: &mcp.GovernedSuspension{
				ID: "s-1", Status: "PENDING", TxHash: "tx-hash-1",
			}}),
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
			m := newFakeMCP(t, &fakeMCPGateway{callBody: tt.body})
			driven, err := m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone}))
			require.NoError(t, err)
			require.Len(t, driven.Observations, 1)
			assert.Equal(t, tt.wantKind, driven.Observations[0].GetKind())
		})
	}
}

func TestMCPDriveSuspensionCarriesTxHash(t *testing.T) {
	body := toolResultBody(t, mcp.CallToolResult{Suspension: &mcp.GovernedSuspension{ID: "s-1", Status: "PENDING", TxHash: "tx-hash-1"}})
	m := newFakeMCP(t, &fakeMCPGateway{callBody: body})

	driven, err := m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-23", Fault: cases.FaultUnattendedMutation}))
	require.NoError(t, err)
	assert.Equal(t, "tx-hash-1", driven.Observations[0].GetSuspendedTxHash())
}

func TestMCPDriveTransportErrorIsAnError(t *testing.T) {
	server := httptest.NewServer(&fakeMCPGateway{})
	c, err := client.New(config.Config{MTLSBaseURL: server.URL, PublicBaseURL: server.URL})
	require.NoError(t, err)
	m, err := NewMCP(context.Background(), c, testIdentity().Persona)
	require.NoError(t, err)

	server.Close()
	_, err = m.Drive(context.Background(), mcpAttempt(cases.Case{ID: "row-1", Fault: cases.FaultNone}))
	require.Error(t, err, "a transport failure has no HTTP answer and must be an error, not an observation")
	assert.False(t, errors.Is(err, constants.ErrConformanceIngressUnsupported))
}

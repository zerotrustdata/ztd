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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestDispatchCommand_UsesTypedAuthenticatedIngressAndRecordsExchange(t *testing.T) {
	dispatched := &operatorv1.GovernedDispatchResult{
		TransactionId: "tx-1",
		EventType:     string(constants.EventOperatorFileEditRequested),
		ActionType:    "FILE_EDIT",
		ResultPayload: []byte("result"),
		Receipt:       outcomeReceipt("tx-1"),
	}
	responseBody := canonicalBody(t, dispatched)
	var received models.OperatorCommandRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, constants.APIPaths.OperatorsCommands, r.URL.Path)
		assert.Empty(t, r.Header.Get(constants.HeaderCLISessionID), "dispatch without a CLI certificate runs on the Operator identity, which never carries the CLI session")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write(responseBody)
		require.NoError(t, err)
	}))
	defer server.Close()

	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)
	exchanges := []Exchange{}
	client.Record(&exchanges)
	request := models.OperatorCommandRequest{
		TargetOperatorSessionID: "session-1",
		EventType:               string(constants.EventOperatorFileEditRequested),
		Payload:                 []byte("payload"),
		CaseID:                  "run-1",
		InvestigationID:         "scenario-1",
		TaskID:                  "attempt-1",
		CLISessionID:            "cli-1",
	}
	outcome, err := client.DispatchCommand(context.Background(), Persona{ID: "evaluation", CLISessionID: "cli-1"}, request)

	require.NoError(t, err)
	require.NoError(t, outcome.Typed())
	assert.Equal(t, int32(http.StatusOK), outcome.Observation.GetHttpStatus())
	assert.Equal(t, "session-1", received.TargetOperatorSessionID)
	assert.Equal(t, string(constants.EventOperatorFileEditRequested), received.EventType)
	require.NotNil(t, outcome.Dispatch)
	assert.Equal(t, "tx-1", outcome.Dispatch.GetTransactionId())
	assert.Equal(t, []byte("result"), outcome.Dispatch.GetResultPayload())
	assert.Equal(t, responseBody, outcome.Body)
	require.Len(t, exchanges, 1)
	assert.Equal(t, constants.APIPaths.OperatorsCommands, exchanges[0].URL[len(server.URL):])
}

func TestDispatchCommand_RequiresEventType(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)

	request := models.OperatorCommandRequest{
		TargetOperatorSessionID: "session-1",
		Payload:                 []byte("payload"),
		CLISessionID:            "cli-1",
	}
	_, err = client.DispatchCommand(context.Background(), Persona{ID: "evaluation", CLISessionID: "cli-1"}, request)
	require.ErrorIs(t, err, constants.ErrTxUnknownEventType)
	assert.Zero(t, requests, "a request without an event type must not reach the Gateway")
}

func TestHealth_ReturnsTypedPostureAndRecordsExchange(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, constants.APIPaths.Health, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"status":"ok","mode":"gateway","version":"2.1.8","pid":1,"governance_ready":true,"posture":"doctrine"}`))
		require.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	client, err := New(config.Config{MTLSBaseURL: server.URL})
	require.NoError(t, err)
	exchanges := []Exchange{}
	client.Record(&exchanges)

	health, raw, err := client.Health(context.Background())

	require.NoError(t, err)
	assert.Equal(t, constants.PostureDoctrine, health.Posture)
	assert.True(t, health.GovernanceReady)
	assert.JSONEq(t, `{"status":"ok","mode":"gateway","version":"2.1.8","pid":1,"governance_ready":true,"posture":"doctrine"}`, string(raw))
	require.Len(t, exchanges, 1)
	assert.Equal(t, constants.APIPaths.Health, exchanges[0].URL[len(server.URL):])
}

func TestMCPToolsList(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
		wantErr      bool
	}{
		{
			name:         "successful list",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"tool1"},{"name":"tool2"}]}}`,
			wantErr:      false,
		},
		{
			name:         "empty tools list",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
			wantErr:      false,
		},
		{
			name:         "server error",
			responseBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/mcp" {
					t.Errorf("expected path /mcp, got %s", r.URL.Path)
				}

				var req mcp.JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				if req.Method != "tools/list" {
					t.Errorf("expected method tools/list, got %s", req.Method)
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tt.responseBody))
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			cfg := config.Config{
				MTLSBaseURL: server.URL,
				Auth:        config.Auth{},
			}

			client, err := New(cfg)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			ctx := context.Background()
			p := Persona{ID: "test-client"}

			resp, err := client.MCPToolsList(ctx, p)

			if (err != nil) != tt.wantErr {
				t.Errorf("MCPToolsList() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp == nil {
				t.Error("MCPToolsList() returned nil response")
			}
		})
	}
}

func TestMCPToolsCall_TypedArgs_SerializeToSameJSONRPCShape(t *testing.T) {
	var capturedParams map[string]any
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Errorf("expected path /mcp, got %s", r.URL.Path)
		}
		var req mcp.JSONRPCRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if req.Method != "tools/call" {
			t.Errorf("expected method tools/call, got %s", req.Method)
		}
		require.NoError(t, json.Unmarshal(req.Params, &capturedParams))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"ok"}}`))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	cfg := config.Config{MTLSBaseURL: srv.URL, Auth: config.Auth{}}
	c, err := New(cfg)
	require.NoError(t, err)

	args := models.RunShellCommandRequest{Command: "dataop", Args: []string{"ingest", "TRK-001"}, Timeout: 10}
	_, err = c.MCPToolsCall(context.Background(), Persona{ID: "test"}, mcp.CallToolRequest{Name: constants.MCPToolRunShellCommand, Arguments: mustJSON(t, args)})
	require.NoError(t, err)

	require.NotNil(t, capturedParams, "params should be a map")
	assert.Equal(t, "run_shell_command", capturedParams["name"])

	argsMap, ok := capturedParams["arguments"].(map[string]any)
	require.True(t, ok, "arguments should be a JSON object")
	assert.Equal(t, "dataop", argsMap["command"])
	assert.Equal(t, []any{"ingest", "TRK-001"}, argsMap["args"])
	assert.Equal(t, float64(10), argsMap["timeout"])
}

func TestMCPToolsCall(t *testing.T) {
	readTool := (&mcp.FileReadTool{}).Name()
	tests := []struct {
		name      string
		tool      string
		arguments json.RawMessage
		wantErr   bool
	}{
		{
			name:      "successful call",
			tool:      readTool,
			arguments: mustJSON(t, mcp.FileReadRequest{Path: "."}),
			wantErr:   false,
		},
		{
			name:      "call with no args",
			tool:      readTool,
			arguments: mustJSON(t, mcp.FileReadRequest{}),
			wantErr:   false,
		},
		{
			name:      "call with nil args",
			tool:      readTool,
			arguments: nil,
			wantErr:   false,
		},
		{
			name:      "call with multi-field args",
			tool:      constants.MCPToolRunShellCommand,
			arguments: mustJSON(t, models.RunShellCommandRequest{Command: "grep", Args: []string{"-r", "TODO", "."}, Timeout: 5}),
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != "/mcp" {
					t.Errorf("expected path /mcp, got %s", r.URL.Path)
				}

				var req mcp.JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				if req.Method != "tools/call" {
					t.Errorf("expected method tools/call, got %s", req.Method)
				}

				var params map[string]any
				if err := json.Unmarshal(req.Params, &params); err != nil {
					t.Errorf("params should be a JSON object: %v", err)
				}

				if params["name"] != tt.tool {
					t.Errorf("expected tool name %s, got %v", tt.tool, params["name"])
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"ok"}}`))
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			cfg := config.Config{
				MTLSBaseURL: server.URL,
				Auth:        config.Auth{},
			}

			client, err := New(cfg)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			ctx := context.Background()
			p := Persona{ID: "test-client"}

			resp, err := client.MCPToolsCall(ctx, p, mcp.CallToolRequest{Name: tt.tool, Arguments: tt.arguments})

			if (err != nil) != tt.wantErr {
				t.Errorf("MCPToolsCall() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp == nil {
				t.Error("MCPToolsCall() returned nil response")
			}
		})
	}
}

func TestMCPResourcesList(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/mcp" {
			t.Errorf("expected path /mcp, got %s", r.URL.Path)
		}

		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		if req.Method != "resources/list" {
			t.Errorf("expected method resources/list, got %s", req.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"resources":[{"uri":"res://1"}]}}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	resp, err := client.MCPResourcesList(ctx, p)

	if err != nil {
		t.Errorf("MCPResourcesList() error = %v", err)
	}

	if resp == nil {
		t.Error("MCPResourcesList() returned nil response")
	}
}

func TestMCPResourcesRead(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/mcp" {
			t.Errorf("expected path /mcp, got %s", r.URL.Path)
		}

		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		if req.Method != "resources/read" {
			t.Errorf("expected method resources/read, got %s", req.Method)
		}

		var params map[string]any
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Errorf("params should be a JSON object: %v", err)
		}

		if params["uri"] != "res://test" {
			t.Errorf("expected uri res://test, got %v", params["uri"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"contents":"test content"}}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	resp, err := client.MCPResourcesRead(ctx, p, mcp.ReadResourceRequest{URI: "res://test"})

	if err != nil {
		t.Errorf("MCPResourcesRead() error = %v", err)
	}

	if resp == nil {
		t.Error("MCPResourcesRead() returned nil response")
	}
}

func TestMCPPromptsList(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/mcp" {
			t.Errorf("expected path /mcp, got %s", r.URL.Path)
		}

		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		if req.Method != "prompts/list" {
			t.Errorf("expected method prompts/list, got %s", req.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"prompts":[{"name":"prompt1"}]}}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	resp, err := client.MCPPromptsList(ctx, p)

	if err != nil {
		t.Errorf("MCPPromptsList() error = %v", err)
	}

	if resp == nil {
		t.Error("MCPPromptsList() returned nil response")
	}
}

func TestMCPPromptsGet(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/mcp" {
			t.Errorf("expected path /mcp, got %s", r.URL.Path)
		}

		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}

		if req.Method != "prompts/get" {
			t.Errorf("expected method prompts/get, got %s", req.Method)
		}

		var params map[string]any
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Errorf("params should be a JSON object: %v", err)
		}

		if params["name"] != "test-prompt" {
			t.Errorf("expected name test-prompt, got %v", params["name"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"messages":[{"role":"user","content":"test"}]}}`))
	})

	server := httptest.NewServer(handler)
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx := context.Background()
	p := Persona{ID: "test-client"}

	resp, err := client.MCPPromptsGet(ctx, p, mcp.GetPromptRequest{Name: "test-prompt", Arguments: map[string]string{"arg": "value"}})

	if err != nil {
		t.Errorf("MCPPromptsGet() error = %v", err)
	}

	if resp == nil {
		t.Error("MCPPromptsGet() returned nil response")
	}
}

func TestA2ACall(t *testing.T) {
	tests := []struct {
		name    string
		skill   string
		payload json.RawMessage
		execID  string
		wantErr bool
	}{
		{
			name:    "successful call",
			skill:   "test-skill",
			payload: json.RawMessage(`{"input":"data"}`),
			execID:  "exec-123",
			wantErr: false,
		},
		{
			name:    "call with empty payload",
			skill:   "test-skill",
			payload: json.RawMessage(`{}`),
			execID:  "exec-456",
			wantErr: false,
		},
		{
			name:    "call with nil payload",
			skill:   "test-skill",
			payload: nil,
			execID:  "exec-789",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != constants.APIPaths.A2ACall {
					t.Errorf("expected path %s, got %s", constants.APIPaths.A2ACall, r.URL.Path)
				}

				var req mcp.JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				if req.Method != "a2a/call" {
					t.Errorf("expected method a2a/call, got %s", req.Method)
				}

				var params map[string]any
				if err := json.Unmarshal(req.Params, &params); err != nil {
					t.Errorf("params should be a JSON object: %v", err)
				}

				if params["skill_name"] != tt.skill {
					t.Errorf("expected skill_name %s, got %v", tt.skill, params["skill_name"])
				}

				if params["execution_id"] != tt.execID {
					t.Errorf("expected execution_id %s, got %v", tt.execID, params["execution_id"])
				}

				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"status":"completed"}}`))
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			cfg := config.Config{
				MTLSBaseURL: server.URL,
				Auth:        config.Auth{},
			}

			client, err := New(cfg)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			ctx := context.Background()
			p := Persona{ID: "test-client"}

			resp, err := client.A2ACall(ctx, p, mcp.A2ACallRequest{SkillName: tt.skill, Payload: tt.payload, ExecutionID: tt.execID})

			if (err != nil) != tt.wantErr {
				t.Errorf("A2ACall() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp == nil {
				t.Error("A2ACall() returned nil response")
			}
		})
	}
}

func TestRPC_DecodesEnvelopeStrictly(t *testing.T) {
	tests := []struct {
		name         string
		responseBody string
		wantErr      bool
	}{
		{
			name:         "successful RPC",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{"status":"ok"}}`,
		},
		{
			name:         "RPC error",
			responseBody: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
		},
		{
			name:         "invalid JSON response",
			responseBody: `invalid json`,
			wantErr:      true,
		},
		{
			name:         "empty response",
			responseBody: ``,
			wantErr:      true,
		},
		{
			name:         "unknown envelope field",
			responseBody: `{"jsonrpc":"2.0","id":1,"result":{},"extra":true}`,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.responseBody))
			})

			server := httptest.NewServer(handler)
			defer server.Close()

			cfg := config.Config{
				MTLSBaseURL: server.URL,
				Auth:        config.Auth{},
			}

			client, err := New(cfg)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			ctx := context.Background()
			p := Persona{ID: "test-client"}

			resp, err := client.rpcResponse(ctx, p, "/test", "test.method", map[string]any{})

			if (err != nil) != tt.wantErr {
				t.Errorf("rpcResponse() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && resp == nil {
				t.Error("rpcResponse() returned nil response")
			}
		})
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/mcptransport"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// stdioBridge runs the production stdio serve loop over pipes with handler in
// place of the Gateway, and returns a driver that has finished its handshake.
func stdioBridge(t *testing.T, handler mcptransport.HandlerFunc) (*MCPStdio, error) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		err := mcptransport.ServeStdio(context.Background(), inR, outW, nil, handler)
		_ = outW.Close()
		served <- err
	}()
	m, err := ConnectMCPStdio(context.Background(), inW, outR, func() error { return <-served })
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m, nil
}

// toolsList answers tools/list with names.
func toolsList(t *testing.T, names ...string) json.RawMessage {
	t.Helper()
	tools := make([]map[string]string, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]string{"name": name})
	}
	raw, err := json.Marshal(map[string]any{"tools": tools})
	require.NoError(t, err)
	return raw
}

func TestMCPStdioHandshakeThenToolsCall(t *testing.T) {
	receipt := &operatorv1.EventReceipt{TransactionHash: "tx-read", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
	receiptJSON, err := response.MarshalProtoJSONCanonical(receipt)
	require.NoError(t, err)
	callResult, err := json.Marshal(mcp.CallToolResult{Content: []mcp.TextContent{}, Receipt: receiptJSON})
	require.NoError(t, err)

	var methods []string
	var call mcp.CallToolRequest
	m, err := stdioBridge(t, func(_ context.Context, req mcptransport.JSONRPCRequest) (mcptransport.JSONRPCResponse, error) {
		methods = append(methods, req.Method)
		resp := mcptransport.JSONRPCResponse{JSONRPC: constants.JSONRPCVersion, ID: req.ID}
		switch req.Method {
		case "initialize":
			resp.Result = json.RawMessage(`{"protocolVersion":"2025-06-18"}`)
		case "tools/list":
			resp.Result = toolsList(t, mcpShellTool, mcpReadTool)
		case "tools/call":
			require.NoError(t, json.Unmarshal(req.Params, &call))
			resp.Result = json.RawMessage(callResult)
		}
		return resp, nil
	})
	require.NoError(t, err)

	driven, err := m.Drive(context.Background(), cases.Attempt{Case: cases.Case{Fault: cases.FaultReadBaseline}, ReadAbs: "/canary"})
	require.NoError(t, err)
	assert.Equal(t, []string{"initialize", "tools/list", "tools/call"}, methods, "the initialized notification gets no answer")
	assert.Equal(t, mcpReadTool, call.Name)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, obs.GetKind())
	assert.Equal(t, "tx-read", obs.GetReceipt().GetTransactionHash())
	assert.Zero(t, obs.GetHttpStatus(), "a stdio answer has no HTTP status")
}

// TestMCPStdioDenyIsRejection checks that the typed rejection the bridge returns
// for a denied approval is observed as a REJECTION.
func TestMCPStdioDenyIsRejection(t *testing.T) {
	denied := &operatorv1.EventReceipt{
		TransactionHash: "tx-deny",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
	}
	data, err := response.MarshalProtoJSONCanonical(governance.ReceiptRejection(denied))
	require.NoError(t, err)

	m, err := stdioBridge(t, func(_ context.Context, req mcptransport.JSONRPCRequest) (mcptransport.JSONRPCResponse, error) {
		resp := mcptransport.JSONRPCResponse{JSONRPC: constants.JSONRPCVersion, ID: req.ID}
		switch req.Method {
		case "tools/list":
			resp.Result = toolsList(t, mcpShellTool, mcpReadTool)
		case "tools/call":
			resp.Error = &mcptransport.JSONRPCError{Code: constants.ErrCodeL3ProofInvalid, Message: "denied", Data: json.RawMessage(data)}
		default:
			resp.Result = json.RawMessage(`{}`)
		}
		return resp, nil
	})
	require.NoError(t, err)

	driven, err := m.Drive(context.Background(), cases.Attempt{Case: cases.Case{Fault: cases.FaultAttendedDeny}, AttemptID: "attempt-1"})
	require.NoError(t, err)
	require.Len(t, driven.Observations, 1)
	obs := driven.Observations[0]
	assert.Equal(t, evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION, obs.GetKind())
	assert.Equal(t, "tx-deny", obs.GetRejection().GetReceipt().GetTransactionHash())
}

func TestMCPStdioCallToolReturnsTypedResult(t *testing.T) {
	var call mcp.CallToolRequest
	m, err := stdioBridge(t, func(_ context.Context, req mcptransport.JSONRPCRequest) (mcptransport.JSONRPCResponse, error) {
		resp := mcptransport.JSONRPCResponse{JSONRPC: constants.JSONRPCVersion, ID: req.ID, Result: json.RawMessage(`{}`)}
		switch req.Method {
		case "tools/list":
			resp.Result = toolsList(t, mcpShellTool, mcpReadTool)
		case "tools/call":
			require.NoError(t, json.Unmarshal(req.Params, &call))
			if call.Name == "fails" {
				resp.Result = nil
				resp.Error = &mcptransport.JSONRPCError{Code: -32603, Message: "tool failed"}
				break
			}
			resp.Result = json.RawMessage(`{"content":[{"type":"text","text":"{\"count\":2}"}]}`)
		}
		return resp, nil
	})
	require.NoError(t, err)

	result, err := m.CallTool(context.Background(), mcp.CallToolRequest{Name: constants.MCPToolEventReceiptList, Arguments: json.RawMessage(`{"operator_session_id":"s1"}`)})
	require.NoError(t, err)
	assert.Equal(t, constants.MCPToolEventReceiptList, call.Name)
	assert.JSONEq(t, `{"operator_session_id":"s1"}`, string(call.Arguments))
	require.Len(t, result.Content, 1)
	assert.Equal(t, `{"count":2}`, result.Content[0].Text)

	_, err = m.CallTool(context.Background(), mcp.CallToolRequest{Name: "fails"})
	require.ErrorIs(t, err, constants.ErrJSONRPCErrorResponse)
}

func TestMCPStdioHandshakeFailsWithoutNativeTools(t *testing.T) {
	_, err := stdioBridge(t, func(_ context.Context, req mcptransport.JSONRPCRequest) (mcptransport.JSONRPCResponse, error) {
		resp := mcptransport.JSONRPCResponse{JSONRPC: constants.JSONRPCVersion, ID: req.ID, Result: json.RawMessage(`{}`)}
		if req.Method == "tools/list" {
			resp.Result = toolsList(t, mcpReadTool)
		}
		return resp, nil
	})
	require.ErrorIs(t, err, constants.ErrConformanceIngressUnsupported)
}

func TestMCPStdioHandshakeFailsOnInitializeError(t *testing.T) {
	_, err := stdioBridge(t, func(_ context.Context, req mcptransport.JSONRPCRequest) (mcptransport.JSONRPCResponse, error) {
		return mcptransport.JSONRPCResponse{JSONRPC: constants.JSONRPCVersion, ID: req.ID,
			Error: &mcptransport.JSONRPCError{Code: -32603, Message: "gateway proxy error"}}, nil
	})
	require.ErrorIs(t, err, constants.ErrJSONRPCErrorResponse)
}

func TestMCPStdioRejectsMissingBridge(t *testing.T) {
	_, err := StartMCPStdio(context.Background(), nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = ConnectMCPStdio(context.Background(), nil, nil, nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// suspendedCallResponse is the tools/call response the Gateway returns when a call
// awaits L3 approval: a typed GovernedSuspension and no receipt.
func suspendedCallResponse(txHash string) JSONRPCResponse {
	message := "Execution paused. Please authorize."
	return JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      float64(1),
		Result: mcp.CallToolResult{
			Content: []mcp.TextContent{{Type: "text", Text: message}},
			Suspension: &mcp.GovernedSuspension{
				ID:          txHash,
				Status:      string(constants.GatewayResponseStatusSuspended),
				TxHash:      txHash,
				ApprovalURL: "https://ztd.local:8443/api/v1/approve/" + txHash,
				Message:     message,
			},
		},
	}
}

// approvalCompletedFrame is an approval.completed SSE frame for txHash whose
// receipt reports receiptTxHash as the resumed transaction's hash.
func approvalCompletedFrame(t *testing.T, userID, txHash, receiptTxHash string) string {
	t.Helper()
	receipt, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId:   "transaction-" + receiptTxHash,
		TransactionHash: receiptTxHash,
		SignerKeyId:     "warden-key-bridge",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "resumed result",
		Signature:       "signature-bridge",
	})
	require.NoError(t, err)
	eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
		Type:    constants.SSEEventTypeApprovalCompleted,
		UserID:  userID,
		TxHash:  txHash,
		Receipt: receipt,
	})
	require.NoError(t, err)
	envelopeJSON, err := json.Marshal(models.SSEPushPayload{UserID: userID, Event: eventPayload})
	require.NoError(t, err)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, string(envelopeJSON))
}

func TestProxySessionToGatewayWithRetry(t *testing.T) {
	t.Run("SSE approval returns the resumed receipt with one tools/call", func(t *testing.T) {
		attempts := 0
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-success"))
		}))
		defer gatewayServer.Close()

		sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, approvalCompletedFrame(t, "user-sse-success", "tx-sse-success", "tx-sse-success"))
		}))
		defer sseServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-sse-success",
		}

		req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}

		resp, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, req, nil)
		require.NoError(t, err)
		assert.Equal(t, 1, attempts, "the approved call must not be re-sent")
		result, ok := resp.Result.(mcp.CallToolResult)
		require.True(t, ok, "the approved call must return a CallToolResult")
		assert.Nil(t, result.Suspension)
		var receipt operatorv1.ActionReceipt
		require.NoError(t, protojson.Unmarshal(result.Receipt, &receipt))
		assert.Equal(t, "tx-sse-success", receipt.GetTransactionHash())
		assert.Equal(t, "resumed result", result.Content[0].Text)
	})

	t.Run("SSE receipt for another transaction fails closed", func(t *testing.T) {
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-mismatch"))
		}))
		defer gatewayServer.Close()

		sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, approvalCompletedFrame(t, "user-sse-mismatch", "tx-sse-mismatch", "tx-other"))
		}))
		defer sseServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-sse-mismatch",
		}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrActionReceiptMissing)
	})

	t.Run("suspension without a transaction hash fails closed", func(t *testing.T) {
		unhashed := suspendedCallResponse("")
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(unhashed)
		}))
		defer gatewayServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   "http://unused.invalid",
			cliSessionID: "cli-no-hash",
		}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
	})

	t.Run("SSE timeout returns error without polling retries", func(t *testing.T) {
		attempts := 0
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-timeout"))
		}))
		defer gatewayServer.Close()

		sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			<-r.Context().Done()
		}))
		defer sseServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-sse-timeout",
		}

		req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := proxySessionToGatewayWithRetryContext(ctx, conn, req, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
		// Only the initial proxy attempt — no polling retries
		assert.Equal(t, 1, attempts)
	})

	t.Run("SSE credentials missing returns ErrNotAuthenticated", func(t *testing.T) {
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-no-creds"))
		}))
		defer gatewayServer.Close()

		conn := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 5 * time.Second},
			gatewayURL:  gatewayServer.URL,
		}

		req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, req, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrNotAuthenticated)
	})
}

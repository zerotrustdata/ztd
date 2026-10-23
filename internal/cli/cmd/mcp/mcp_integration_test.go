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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
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
	return approvalReceiptFrame(t, userID, txHash, &operatorv1.ActionReceipt{
		TransactionId:   "transaction-" + receiptTxHash,
		TransactionHash: receiptTxHash,
		SignerKeyId:     "warden-key-bridge",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "resumed result",
		Signature:       "signature-bridge",
	})
}

// approvalReceiptFrame is an approval.completed SSE frame for txHash carrying receipt.
func approvalReceiptFrame(t *testing.T, userID, txHash string, signed *operatorv1.ActionReceipt) string {
	t.Helper()
	receipt, err := response.MarshalProtoJSONCanonical(signed)
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

// approvalStreamServer serves the approval stream as the Gateway does: it flushes the
// 200 before any event, writes each frame received on frames and flushes it, then
// sends on written once the frame is on the wire. It holds the stream open until the
// client disconnects and counts every connection.
func approvalStreamServer(t *testing.T, frames <-chan string, written chan<- struct{}, connections *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case frame := <-frames:
				fmt.Fprint(w, frame)
				w.(http.Flusher).Flush()
				written <- struct{}{}
			case <-r.Context().Done():
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// publishDuringCall puts frame on the approval stream from inside a Gateway handler and
// waits until the stream has flushed it, so the approval is on the wire before the
// suspended response returns. It reports false when no stream took the frame, which
// means the bridge did not subscribe before it forwarded the call.
func publishDuringCall(frames chan<- string, written <-chan struct{}, frame string) bool {
	select {
	case frames <- frame:
	case <-time.After(5 * time.Second):
		return false
	}
	select {
	case <-written:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func TestL3ApprovalProxySessionToGatewayWithRetry(t *testing.T) {
	t.Run("approval completed while the call is in flight still returns the resumed receipt", func(t *testing.T) {
		var attempts, connections atomic.Int32
		frames := make(chan string)
		written := make(chan struct{}, 1)
		sseServer := approvalStreamServer(t, frames, written, &connections)
		frame := approvalCompletedFrame(t, "user-sse-success", "tx-sse-success", "tx-sse-success")

		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			if !publishDuringCall(frames, written, frame) {
				http.Error(w, "approval stream not subscribed before the call", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-success"))
		}))
		defer gatewayServer.Close()

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
		assert.Equal(t, int32(1), attempts.Load(), "the approved call must not be re-sent")
		assert.Equal(t, int32(1), connections.Load(), "the call must subscribe once")
		result, ok := resp.Result.(mcp.CallToolResult)
		require.True(t, ok, "the approved call must return a CallToolResult")
		assert.Nil(t, result.Suspension)
		var receipt operatorv1.ActionReceipt
		require.NoError(t, protojson.Unmarshal(result.Receipt, &receipt))
		assert.Equal(t, "tx-sse-success", receipt.GetTransactionHash())
		assert.Equal(t, "resumed result", result.Content[0].Text)
	})

	t.Run("a denied approval is the typed L3_DENIED rejection, not a tool result", func(t *testing.T) {
		var connections atomic.Int32
		frames := make(chan string)
		written := make(chan struct{}, 1)
		sseServer := approvalStreamServer(t, frames, written, &connections)
		frame := approvalReceiptFrame(t, "user-sse-deny", "tx-sse-deny", &operatorv1.ActionReceipt{
			TransactionId:   "transaction-tx-sse-deny",
			TransactionHash: "tx-sse-deny",
			SignerKeyId:     "warden-key-bridge",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
			RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
			Signature:       "signature-bridge",
		})

		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !publishDuringCall(frames, written, frame) {
				http.Error(w, "approval stream not subscribed before the call", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-deny"))
		}))
		defer gatewayServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-sse-deny",
		}

		resp, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}, nil)
		require.NoError(t, err)
		require.Nil(t, resp.Result)
		require.NotNil(t, resp.Error)
		data, ok := resp.Error.Data.(json.RawMessage)
		require.True(t, ok, "error.data must be the encoded GovernanceRejection")
		rejection, refusal, err := governance.DecodeIngressFailure(data)
		require.NoError(t, err)
		require.Nil(t, refusal)
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, rejection.GetReceipt().GetRejectionCode())
		assert.Equal(t, "tx-sse-deny", rejection.GetReceipt().GetTransactionHash())
	})

	t.Run("SSE receipt for another transaction fails closed", func(t *testing.T) {
		var connections atomic.Int32
		frames := make(chan string)
		written := make(chan struct{}, 1)
		sseServer := approvalStreamServer(t, frames, written, &connections)
		frame := approvalCompletedFrame(t, "user-sse-mismatch", "tx-sse-mismatch", "tx-other")

		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !publishDuringCall(frames, written, frame) {
				http.Error(w, "approval stream not subscribed before the call", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-mismatch"))
		}))
		defer gatewayServer.Close()

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
		var connections atomic.Int32
		sseServer := approvalStreamServer(t, make(chan string), make(chan struct{}, 1), &connections)
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
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-no-hash",
		}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
	})

	t.Run("SSE timeout returns error without polling retries", func(t *testing.T) {
		var attempts, connections atomic.Int32
		sseServer := approvalStreamServer(t, make(chan string), make(chan struct{}, 1), &connections)
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-sse-timeout"))
		}))
		defer gatewayServer.Close()

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
		require.ErrorIs(t, err, constants.ErrApprovalSSETimeout)
		assert.Contains(t, err.Error(), "timed out")
		// Only the initial proxy attempt — no polling retries
		assert.Equal(t, int32(1), attempts.Load())
	})

	t.Run("approval stream refusing the subscription forwards nothing", func(t *testing.T) {
		var attempts, connections atomic.Int32
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-refused"))
		}))
		defer gatewayServer.Close()

		refusingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			connections.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer refusingServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   refusingServer.URL,
			cliSessionID: "cli-refused",
		}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}, nil)
		require.Error(t, err)
		require.ErrorIs(t, err, constants.ErrCLISessionRefreshRequired)
		assert.Equal(t, int32(1), connections.Load())
		assert.Zero(t, attempts.Load(), "a call without a working approval stream must not be forwarded")
	})

	t.Run("requests other than tools/call do not open the approval stream", func(t *testing.T) {
		var connections atomic.Int32
		refusingServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			connections.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer refusingServer.Close()

		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(JSONRPCResponse{JSONRPC: "2.0", ID: float64(1), Result: map[string]string{"status": "listed"}})
		}))
		defer gatewayServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   refusingServer.URL,
			cliSessionID: "cli-list",
		}

		resp, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/list"}, nil)
		require.NoError(t, err)
		assert.Equal(t, "2.0", resp.JSONRPC)
		assert.Zero(t, connections.Load(), "a request that cannot suspend must not subscribe")
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

func TestRunMCPAgentRun_NoArgs(t *testing.T) {
	t.Run("returns error when no args", func(t *testing.T) {
		err := runMCPAgentRun(nil, false, "", shared.NewFileSvc, authcmd.PanickingEnrollerFactory())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "specify an agent name")
	})

	t.Run("returns ErrAgentNotFound for unknown agent", func(t *testing.T) {
		err := runMCPAgentRun([]string{"unknown-agent-xyz"}, false, "", shared.NewFileSvc, authcmd.PanickingEnrollerFactory())
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrAgentNotFound)
	})

	t.Run("devin returns error for missing gateway or binary", func(t *testing.T) {
		// Devin is a local CLI agent and goes through launchAgentWithGovernance.
		// We can't test the full launch path here (requires gateway), but we verify
		// it does NOT return the old cloud-based error.
		err := runMCPAgentRun([]string{"devin"}, false, "", cmdtest.FailingFileSvcFactory(constants.ErrNotAuthenticated), authcmd.PanickingEnrollerFactory())
		require.Error(t, err)
		assert.NotContains(t, err.Error(), "cloud-based agent")
	})
}

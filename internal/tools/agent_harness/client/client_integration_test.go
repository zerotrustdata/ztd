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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestClient_StateRoot(t *testing.T) {
	tests := []struct {
		name          string
		responseBody  string
		expectedRoot  string
		wantErr       bool
		setupServer   func(*httptest.Server)
		usePublicBase bool
	}{
		{
			name:          "state_merkle_root field",
			responseBody:  `{"state_merkle_root": "abc123"}`,
			expectedRoot:  "abc123",
			wantErr:       false,
			usePublicBase: true,
		},
		{
			name:          "state_root field",
			responseBody:  `{"state_root": "def456"}`,
			expectedRoot:  "def456",
			wantErr:       false,
			usePublicBase: true,
		},
		{
			name:          "both fields (state_merkle_root takes precedence)",
			responseBody:  `{"state_merkle_root": "abc123", "state_root": "def456"}`,
			expectedRoot:  "abc123",
			wantErr:       false,
			usePublicBase: true,
		},
		{
			name:          "empty response",
			responseBody:  `{}`,
			expectedRoot:  "",
			wantErr:       false,
			usePublicBase: true,
		},
		{
			name:          "invalid JSON",
			responseBody:  `invalid`,
			wantErr:       false, // stateRoot is lenient - returns empty string on invalid JSON
			expectedRoot:  "",
			usePublicBase: true,
		},
		{
			name:         "server error",
			responseBody: `{"error": "internal error"}`,
			wantErr:      false, // stateRoot is lenient - doesn't check status codes
			expectedRoot: "",
			setupServer: func(s *httptest.Server) {
				s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(`{"error": "internal error"}`))
				})
			},
			usePublicBase: true,
		},
		{
			name:          "mTLS surface",
			responseBody:  `{"state_merkle_root": "mTLS-root"}`,
			expectedRoot:  "mTLS-root",
			wantErr:       false,
			usePublicBase: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != constants.APIPaths.State {
					t.Errorf("expected path %s, got %s", constants.APIPaths.State, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tt.responseBody))
			})

			if tt.setupServer != nil {
				server := httptest.NewServer(handler)
				tt.setupServer(server)
			}

			server := httptest.NewServer(handler)
			defer server.Close()

			cfg := config.Config{
				PublicBaseURL: server.URL,
				MTLSBaseURL:   server.URL,
				Auth:          config.Auth{},
			}

			client, err := New(cfg)
			if err != nil {
				t.Fatalf("New() failed: %v", err)
			}

			ctx := context.Background()
			var root string
			if tt.usePublicBase {
				root, err = client.StateRoot(ctx)
			} else {
				root, err = client.StateRootFromMTLS(ctx)
			}

			if (err != nil) != tt.wantErr {
				t.Errorf("StateRoot() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && root != tt.expectedRoot {
				t.Errorf("StateRoot() = %v, want %v", root, tt.expectedRoot)
			}
		})
	}
}

func TestClient_RegisterSigner(t *testing.T) {
	tests := []struct {
		name         string
		keyID        string
		pubHex       string
		role         string
		responseCode int
		wantErr      bool
	}{
		{
			name:         "successful registration",
			keyID:        "test-key",
			pubHex:       "abc123",
			role:         "consensus",
			responseCode: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "principal role",
			keyID:        "principal-key",
			pubHex:       "def456",
			role:         "principal",
			responseCode: http.StatusOK,
			wantErr:      false,
		},
		{
			name:         "server returns 404 (best-effort)",
			keyID:        "test-key",
			pubHex:       "abc123",
			role:         "consensus",
			responseCode: http.StatusNotFound,
			wantErr:      true,
		},
		{
			name:         "server returns 500",
			keyID:        "test-key",
			pubHex:       "abc123",
			role:         "consensus",
			responseCode: http.StatusInternalServerError,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("expected POST, got %s", r.Method)
				}
				if r.URL.Path != constants.APIPaths.GovernanceSigners {
					t.Errorf("expected path %s, got %s", constants.APIPaths.GovernanceSigners, r.URL.Path)
				}

				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("failed to decode request: %v", err)
				}

				if req["id"] != tt.keyID {
					t.Errorf("expected id %s, got %v", tt.keyID, req["id"])
				}
				if req["public_key_hex"] != tt.pubHex {
					t.Errorf("expected public_key_hex %s, got %v", tt.pubHex, req["public_key_hex"])
				}
				if req["enabled"] != true {
					t.Errorf("expected enabled true, got %v", req["enabled"])
				}

				w.WriteHeader(tt.responseCode)
			}))
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
			err = client.RegisterSigner(ctx, tt.keyID, tt.pubHex, tt.role)

			if (err != nil) != tt.wantErr {
				t.Errorf("RegisterSigner() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_do(t *testing.T) {
	tests := []struct {
		name          string
		persona       Persona
		method        string
		body          []byte
		responseCode  int
		responseBody  string
		wantErr       bool
		verifyHeaders map[string]string
	}{
		{
			name:         "GET request",
			persona:      Persona{ID: "test-client"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
		},
		{
			name:         "POST request with body",
			persona:      Persona{ID: "test-client"},
			method:       http.MethodPost,
			body:         []byte(`{"test": "data"}`),
			responseCode: http.StatusOK,
			responseBody: `{"result": "created"}`,
			wantErr:      false,
		},
		{
			name:         "request with User-Agent",
			persona:      Persona{ID: "test-client", UserAgent: "TestAgent/1.0"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
			verifyHeaders: map[string]string{
				"User-Agent":           "TestAgent/1.0",
				"X-ZTD-Client-Persona": "test-client",
			},
		},
		{
			name:         "request with API key",
			persona:      Persona{ID: "test-client"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
		},
		{
			name:         "request with Operator session ID",
			persona:      Persona{ID: "test-client", OperatorSessionID: "session-123"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
			verifyHeaders: map[string]string{
				"Authorization":                   "Bearer session-123",
				constants.HeaderOperatorSessionID: "session-123",
			},
		},
		{
			name:         "request with CLI session ID",
			persona:      Persona{ID: "test-client", CLISessionID: "cli-session-123"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
			verifyHeaders: map[string]string{
				constants.HeaderCLISessionID: "cli-session-123",
			},
		},
		{
			name:         "request with User ID",
			persona:      Persona{ID: "test-client", UserID: "user-123"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
			verifyHeaders: map[string]string{
				constants.HeaderUserID: "user-123",
			},
		},
		{
			name:         "request with Operator ID",
			persona:      Persona{ID: "test-client", OperatorID: "operator-123"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      false,
			verifyHeaders: map[string]string{
				constants.HeaderOperatorID: "operator-123",
			},
		},
		{
			name:         "server error",
			persona:      Persona{ID: "test-client"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusInternalServerError,
			responseBody: `{"error": "internal error"}`,
			wantErr:      false, // do() doesn't error on status codes
		},
		{
			name:         "network error (context canceled)",
			persona:      Persona{ID: "test-client"},
			method:       http.MethodGet,
			body:         nil,
			responseCode: http.StatusOK,
			responseBody: `{"result": "ok"}`,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedHeaders http.Header

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				receivedHeaders = r.Header.Clone()
				if r.Method != tt.method {
					t.Errorf("expected method %s, got %s", tt.method, r.Method)
				}
				w.WriteHeader(tt.responseCode)
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
			if tt.name == "network error (context canceled)" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			status, body, err := client.do(ctx, tt.persona, tt.method, server.URL+"/test", tt.body)

			if (err != nil) != tt.wantErr {
				t.Errorf("do() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if status != tt.responseCode {
					t.Errorf("do() status = %d, want %d", status, tt.responseCode)
				}
				if string(body) != tt.responseBody {
					t.Errorf("do() body = %s, want %s", string(body), tt.responseBody)
				}

				// Verify headers
				for key, expectedValue := range tt.verifyHeaders {
					actualValue := receivedHeaders.Get(key)
					if actualValue != expectedValue {
						t.Errorf("do() header %s = %s, want %s", key, actualValue, expectedValue)
					}
				}
			}
		})
	}
}

func TestClient_do_Recording(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	var exchanges []Exchange
	client.Record(&exchanges)

	ctx := context.Background()
	p := Persona{ID: "test-client"}
	_, _, err = client.do(ctx, p, http.MethodGet, server.URL+"/test", nil)

	if err != nil {
		t.Fatalf("do() failed: %v", err)
	}

	if len(exchanges) != 1 {
		t.Errorf("expected 1 exchange, got %d", len(exchanges))
	}

	ex := exchanges[0]
	if ex.Persona != "test-client" {
		t.Errorf("expected persona test-client, got %s", ex.Persona)
	}
	if ex.Method != http.MethodGet {
		t.Errorf("expected method GET, got %s", ex.Method)
	}
	if ex.Status != http.StatusOK {
		t.Errorf("expected status 200, got %d", ex.Status)
	}
	if ex.LatencyMS < 0 {
		t.Errorf("expected non-negative latency, got %d", ex.LatencyMS)
	}
	if ex.At.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

func TestClient_do_Verbose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"result": "ok"}`))
	}))
	defer server.Close()

	cfg := config.Config{
		MTLSBaseURL: server.URL,
		Auth:        config.Auth{},
		Verbose:     true,
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	// Redirect stderr to capture verbose output
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	ctx := context.Background()
	p := Persona{ID: "test-client"}
	_, _, err = client.do(ctx, p, http.MethodGet, server.URL+"/test", nil)

	w.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("do() failed: %v", err)
	}

	// Check that something was written to stderr
	buf := make([]byte, 1024)
	n, _ := r.Read(buf)
	if n == 0 {
		t.Error("expected verbose output to stderr")
	}
}

// resumedReceiptJSON returns the canonical signed receipt of txHash's resumed
// transaction, as the Gateway embeds it in approval.completed.
func resumedReceiptJSON(t *testing.T, txHash string) json.RawMessage {
	t.Helper()
	receiptJSON, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId:   "transaction-" + txHash,
		TransactionHash: txHash,
		SignerKeyId:     "warden-key-resumed",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "resumed result",
		Signature:       "signature-resumed",
	})
	require.NoError(t, err)
	return receiptJSON
}

// approvalCompletedSSE serves one approval.completed SSE frame carrying receipt.
func approvalCompletedSSE(t *testing.T, w http.ResponseWriter, userID, txHash string, receipt json.RawMessage) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
		Type:    constants.SSEEventTypeApprovalCompleted,
		UserID:  userID,
		TxHash:  txHash,
		Receipt: receipt,
	})
	require.NoError(t, err)
	envelopeJSON, err := json.Marshal(models.SSEPushPayload{UserID: userID, Event: eventPayload})
	require.NoError(t, err)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, string(envelopeJSON))
}

func TestClient_WaitForHumanApproval_Success(t *testing.T) {
	const userID = "test-user-approval"
	const txHash = "tx-success-001"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream):
			approvalCompletedSSE(t, w, userID, txHash, resumedReceiptJSON(t, txHash))
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.ApprovalsCLIStatus):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(models.ApprovalStatusResponse{
				Status: string(constants.SuspendedTxStatusApproved),
				TxHash: txHash,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		PublicBaseURL: srv.URL,
		MTLSBaseURL:   srv.URL,
		Auth:          config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receipt, err := client.WaitForHumanApproval(ctx, Persona{ID: "test"}, txHash, userID)
	if err != nil {
		t.Fatalf("WaitForHumanApproval() error = %v", err)
	}
	if receipt.GetTransactionHash() != txHash {
		t.Errorf("receipt transaction hash = %q, want %q", receipt.GetTransactionHash(), txHash)
	}
}

func TestClient_WaitForHumanApproval_ReturnsResumedReceiptFromCompletionEvent(t *testing.T) {
	const userID = "test-user-resumed-receipt"
	const txHash = "tx-resumed-receipt-001"

	statusRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream):
			approvalCompletedSSE(t, w, userID, txHash, resumedReceiptJSON(t, txHash))
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.ApprovalsCLIStatus):
			statusRequests++
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(models.ApprovalStatusResponse{
				Status: string(constants.SuspendedTxStatusExpiredOrNotFound),
			}))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := New(config.Config{PublicBaseURL: srv.URL, MTLSBaseURL: srv.URL, Auth: config.Auth{}})
	require.NoError(t, err)

	receipt, err := client.WaitForHumanApproval(context.Background(), Persona{ID: "test"}, txHash, userID)
	require.NoError(t, err)
	assert.Zero(t, statusRequests)

	want := &operatorv1.ActionReceipt{}
	require.NoError(t, protojson.Unmarshal(resumedReceiptJSON(t, txHash), want))
	assert.True(t, proto.Equal(want, receipt), "the approval must return the event's signed receipt")
}

func TestClient_WaitForHumanApproval_FailsClosedWhenReceiptTransactionDoesNotMatchEvent(t *testing.T) {
	const userID = "test-user-mismatched-receipt"
	const txHash = "tx-matched-event-001"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		approvalCompletedSSE(t, w, userID, txHash, resumedReceiptJSON(t, "different-transaction-hash"))
	}))
	t.Cleanup(srv.Close)

	client, err := New(config.Config{PublicBaseURL: srv.URL, MTLSBaseURL: srv.URL, Auth: config.Auth{}})
	require.NoError(t, err)

	receipt, err := client.WaitForHumanApproval(context.Background(), Persona{ID: "test"}, txHash, userID)

	require.ErrorIs(t, err, constants.ErrActionReceiptMissing)
	assert.Nil(t, receipt)
}

func TestClient_WaitForHumanApproval_FailsClosedWhenReceiptIsNotProtojson(t *testing.T) {
	const userID = "test-user-malformed-receipt"
	const txHash = "tx-malformed-receipt-001"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		approvalCompletedSSE(t, w, userID, txHash, json.RawMessage(`{"transaction_hash":"`+txHash+`","not_a_receipt_field":1}`))
	}))
	t.Cleanup(srv.Close)

	client, err := New(config.Config{PublicBaseURL: srv.URL, MTLSBaseURL: srv.URL, Auth: config.Auth{}})
	require.NoError(t, err)

	receipt, err := client.WaitForHumanApproval(context.Background(), Persona{ID: "test"}, txHash, userID)

	require.Error(t, err, "a receipt with unknown fields must fail strict protojson decoding")
	assert.Nil(t, receipt)
}

func TestClient_WaitForHumanApproval_TimeoutNoMatchingEvent(t *testing.T) {
	const userID = "test-user-timeout"
	const sentTxHash = "tx-wrong-999"
	const expectedTxHash = "tx-correct-001"

	// sseCLISessionID captures the X-ZTD-CLI-Session-ID header from the SSE
	// subscription request so the test can assert WaitForHumanApproval sends the
	// persona's CLI session ID as a header (not in the URL query string).
	var sseCLISessionID string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream):
			sseCLISessionID = r.Header.Get(constants.HeaderCLISessionID)
			w.Header().Set("Content-Type", "text/event-stream")
			eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
				Type:   constants.SSEEventTypeApprovalCompleted,
				UserID: userID,
				TxHash: sentTxHash,
			})
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			envelope := models.SSEPushPayload{
				UserID: userID,
				Event:  eventPayload,
			}
			envelopeJSON, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, string(envelopeJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		PublicBaseURL: srv.URL,
		MTLSBaseURL:   srv.URL,
		Auth:          config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err = client.WaitForHumanApproval(ctx, Persona{ID: "test", CLISessionID: "cli-test-session"}, expectedTxHash, userID)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if err != constants.ErrApprovalSSETimeout {
		t.Errorf("expected ErrApprovalSSETimeout, got %v", err)
	}
	// The SSE subscription must carry the persona's CLI session ID as a header
	// so the gateway can route the subscription to the correct session.
	if sseCLISessionID != "cli-test-session" {
		t.Errorf("SSE subscription X-ZTD-CLI-Session-ID header: got %q, want %q", sseCLISessionID, "cli-test-session")
	}
}

func TestClient_WaitForHumanApproval_FailsClosedWhenCompletionEventOmitsReceipt(t *testing.T) {
	const userID = "test-user-status-err"
	const txHash = "tx-status-err-456"

	statusRequests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.SSEStream):
			w.Header().Set("Content-Type", "text/event-stream")
			eventPayload, err := json.Marshal(models.ApprovalCompletedEvent{
				Type:   constants.SSEEventTypeApprovalCompleted,
				UserID: userID,
				TxHash: txHash,
			})
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			envelope := models.SSEPushPayload{
				UserID: userID,
				Event:  eventPayload,
			}
			envelopeJSON, err := json.Marshal(envelope)
			if err != nil {
				t.Fatalf("marshal envelope: %v", err)
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", constants.SSEEventTypeApprovalCompleted, string(envelopeJSON))
		case strings.HasPrefix(r.URL.Path, constants.APIPaths.ApprovalsCLIStatus):
			statusRequests++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(models.ApprovalStatusResponse{
				Status: string(constants.SuspendedTxStatusExpiredOrNotFound),
				TxHash: txHash,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := config.Config{
		PublicBaseURL: srv.URL,
		MTLSBaseURL:   srv.URL,
		Auth:          config.Auth{},
	}

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receipt, err := client.WaitForHumanApproval(ctx, Persona{ID: "test"}, txHash, userID)
	require.ErrorIs(t, err, constants.ErrActionReceiptMissing)
	assert.Nil(t, receipt)
	assert.Zero(t, statusRequests)
}

func TestMCPToolsCallWithCLI_RoutesThroughCLI(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		MTLSBaseURL: srv.URL,
		Auth:        config.Auth{},
		CLIAuth: config.Auth{
			ClientCert: "/cli/cert",
			ClientKey:  "/cli/key",
		},
	}

	c, err := New(cfg)
	require.NoError(t, err)
	assert.NotNil(t, c.cliHTTP, "cliHTTP must be non-nil for MCPToolsCallWithCLI to route through it")

	ctx := context.Background()
	_, err = c.MCPToolsCallWithCLI(ctx, Persona{ID: "test"}, "fs_list", FSPathArgs{Path: "."})
	require.NoError(t, err)
	assert.True(t, sawRequest, "server should have received the request")
}

func TestMCPToolsCallWithCLI_FallsBackToDoWhenNoCLIHTTP(t *testing.T) {
	var sawRequest bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawRequest = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()

	cfg := config.Config{
		MTLSBaseURL: srv.URL,
		Auth:        config.Auth{},
	}

	c, err := New(cfg)
	require.NoError(t, err)
	assert.Nil(t, c.cliHTTP, "cliHTTP should be nil — fallback to do() expected")

	ctx := context.Background()
	_, err = c.MCPToolsCallWithCLI(ctx, Persona{ID: "test"}, "fs_list", FSPathArgs{Path: "."})
	require.NoError(t, err)
	assert.True(t, sawRequest, "server should have received the fallback request")
}

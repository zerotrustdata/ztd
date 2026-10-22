// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestL3ApprovalProxyToGatewayWithRetry(t *testing.T) {
	t.Run("SSE credentials missing returns ErrNotAuthenticated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-missing-creds"))
		}))
		defer server.Close()

		conn := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 5 * time.Second},
			gatewayURL:  server.URL,
		}

		req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}

		_, err := proxySessionToGatewayWithRetryContext(context.Background(), conn, req, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrNotAuthenticated)
	})

	t.Run("SSE timeout returns error without polling", func(t *testing.T) {
		gatewayServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(suspendedCallResponse("tx-timeout"))
		}))
		defer gatewayServer.Close()

		sseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			// The Gateway flushes the 200 before any event, so the subscription connects.
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		defer sseServer.Close()

		conn := &gatewayConn{
			openBrowser:  func(string) error { return nil },
			client:       &http.Client{Timeout: 5 * time.Second},
			gatewayURL:   gatewayServer.URL,
			sseClient:    &http.Client{Timeout: 5 * time.Second},
			sseBaseURL:   sseServer.URL,
			cliSessionID: "cli-timeout-test",
		}

		req := JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call"}

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		_, err := proxySessionToGatewayWithRetryContext(ctx, conn, req, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "timed out")
	})
}

func TestProxySessionToGateway(t *testing.T) {
	t.Run("proxySessionToGateway marshals request and sets headers", func(t *testing.T) {
		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		}

		reqBody, err := json.Marshal(req)
		require.NoError(t, err)
		assert.Contains(t, string(reqBody), "tools/list")
		assert.Contains(t, string(reqBody), "2.0")
	})

	t.Run("proxySessionToGateway forwards request to gateway", func(t *testing.T) {
		expectedResp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      float64(1),
			Result: map[string]interface{}{
				"tools": []interface{}{
					map[string]interface{}{
						"name":        "test_tool",
						"description": "A test tool",
					},
				},
			},
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

			var req JSONRPCRequest
			err := json.NewDecoder(r.Body).Decode(&req)
			assert.NoError(t, err)
			assert.Equal(t, "tools/list", req.Method)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			err = json.NewEncoder(w).Encode(expectedResp)
			assert.NoError(t, err)
		}))
		defer server.Close()

		session := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 5 * time.Second},
			gatewayURL:  server.URL,
		}

		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		}

		resp, err := proxySessionToGateway(session, req)
		require.NoError(t, err)
		assert.Equal(t, "2.0", resp.JSONRPC)
		assert.NotNil(t, resp.Result)
	})

	t.Run("proxySessionToGateway returns error on non-200 status", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`internal server error`))
		}))
		defer server.Close()

		session := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 5 * time.Second},
			gatewayURL:  server.URL,
		}

		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		}

		_, err := proxySessionToGateway(session, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HTTP 500")
	})

	t.Run("proxySessionToGateway returns error on invalid JSON response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{invalid json`))
		}))
		defer server.Close()

		session := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 5 * time.Second},
			gatewayURL:  server.URL,
		}

		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		}

		_, err := proxySessionToGateway(session, req)
		require.Error(t, err)
	})
}

// TestProxySessionToGateway_RelaysGatewayBytes pins that the proxy relays result
// and error.data as the Gateway encoded them: key order and integers survive.
func TestProxySessionToGateway_RelaysGatewayBytes(t *testing.T) {
	const result = `{"z":1,"receipt":{"seq":9007199254740993,"a":"x"}}`
	const data = `{"receipt":{"b":2,"a":1},"error":"TX_DOCTRINE_L1_FAILED"}`
	for _, tc := range []struct{ name, body, raw string }{
		{"result", `{"jsonrpc":"2.0","id":7,"result":` + result + `}`, result},
		{"error data", `{"jsonrpc":"2.0","id":7,"error":{"code":-32005,"message":"denied","data":` + data + `}}`, data},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			session := &gatewayConn{client: &http.Client{Timeout: 5 * time.Second}, gatewayURL: server.URL}

			resp, err := proxySessionToGateway(session, JSONRPCRequest{JSONRPC: "2.0", ID: 7, Method: "tools/call"})
			require.NoError(t, err)
			relayed, err := json.Marshal(resp)
			require.NoError(t, err)
			assert.Contains(t, string(relayed), tc.raw, "the Gateway's bytes must be relayed unchanged")
		})
	}
}

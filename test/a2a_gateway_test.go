//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tests

/*
TestA2AGateway_EndToEnd exercises ztdo from the perspective of a standard A2A client
(e.g., Google Agent2Agent protocol). It verifies the "Universal Protocol Translator"
logic which allows "dumb" clients to be governed by the ztd Gateway without needing
native signing or envelope construction logic.

Practical Coverage:
1. Protocol Translation: Maps A2A skill calls to typed GovernanceEnvelopes.
2. 3-Layer Verification: Forces skill calls through L1 (Hard Gates), L2 (Consensus), and L3 (Approval).
3. Suspension & OOB: Verifies that mutations are suspended, recorded, and only resumed
   after Out-of-Band (OOB) human approval via WebAuthn/Passkey.
4. Downstream Dispatch: Ensures verified payloads are correctly unwrapped and dispatched
   to the real downstream A2A server.
*/

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestA2AGateway_SkillCallEndToEnd(t *testing.T) {
	// dispatchedSkills records the skill_name of every request the downstream
	// A2A server receives. Only the Gateway dispatches to it, so an entry here
	// proves the governed call continued past suspension into execution.
	var (
		dispatchMu       sync.Mutex
		dispatchedSkills []string
	)
	snapshotSkills := func() []string {
		dispatchMu.Lock()
		defer dispatchMu.Unlock()
		return append([]string(nil), dispatchedSkills...)
	}
	downstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SkillName string `json:"skill_name"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		dispatchMu.Lock()
		dispatchedSkills = append(dispatchedSkills, req.SkillName)
		dispatchMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"a2a says hello","summary":"verified skill execution"}`))
	}))
	defer downstreamServer.Close()

	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:          t.Name(),
		A2ADownstreamURL:  downstreamServer.URL,
		AllowTestPortZero: true,
	})
	fixture.WaitForReady(t)

	identity := fixtures.BootstrapCLIIdentity(t, fixture, "a2a-fingerprint")
	mtlsClient := fixtures.CreateCLIMTLSClient(t, fixture, identity)
	mtlsURL := network.LocalhostHTTPSURL(fixture.Service.GetHTTPSPort())

	// Test A2A Call (Suspends for L3, then Resume)
	t.Run("a2a call", func(t *testing.T) {
		receiptsBefore := getAuditReceipts(t, mtlsClient, fixture.Service.GetHTTPSPort(), identity.OperatorSessionID)

		callReq := map[string]interface{}{
			"jsonrpc": "2.0",
			"method":  "a2a/call",
			"id":      1,
			"params": map[string]interface{}{
				"skill_name": "test-skill",
				"payload":    map[string]string{"foo": "bar"},
			},
		}

		reqBody, _ := json.Marshal(callReq)
		req, _ := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.A2ACall, bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+identity.OperatorSessionID)
		resp, err := mtlsClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var a2aRes struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Result  struct {
				ID          string `json:"id"`
				Status      string `json:"status"`
				TxHash      string `json:"tx_hash"`
				ApprovalURL string `json:"approval_url"`
				Message     string `json:"message"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&a2aRes)
		require.NoError(t, err)
		// The L3 notary rejects, so the transaction should be suspended
		require.Equal(t, string(constants.GatewayResponseStatusSuspended), a2aRes.Result.Status, "expected suspended status, got: %s", a2aRes.Result.Status)
		require.NotEmpty(t, a2aRes.Result.ApprovalURL)

		// The call must have traversed the governance pipeline and been recorded.
		receiptsAfter := getAuditReceipts(t, mtlsClient, fixture.Service.GetHTTPSPort(), identity.OperatorSessionID)
		require.Greater(t, len(receiptsAfter.Receipts), len(receiptsBefore.Receipts),
			"a2a/call must record an audit receipt for its governed execution")

		// A suspended call must not have reached the downstream server yet.
		require.Empty(t, snapshotSkills(), "suspended a2a/call must not dispatch downstream before approval")

		// Approve the suspended transaction the way the approval page does: sign
		// in with the user's passkey, then sign the transaction-bound challenge.
		session := fixtures.ConsoleSignIn(t, fixture, mtlsClient, identity)
		status, body := fixtures.ApproveWithPasskey(t, fixture, mtlsClient, session, identity, a2aRes.Result.TxHash)
		require.Equal(t, http.StatusOK, status, "passkey approval must resume the transaction: %s", body)

		receipt := &operatorv1.ActionReceipt{}
		require.NoError(t, protojson.Unmarshal(body, receipt))
		require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status, "approved a2a/call must complete")
		require.Equal(t, a2aRes.Result.TxHash, receipt.TransactionHash)

		// Verify returns after execution, so the downstream call has happened.
		require.Equal(t, []string{"test-skill"}, snapshotSkills(),
			"downstream must be called exactly once with skill_name test-skill")
		require.True(t, hasCompletedReceipt(getAuditReceipts(t, mtlsClient, fixture.Service.GetHTTPSPort(), identity.OperatorSessionID).Receipts),
			"approved a2a/call must record a COMPLETED audit receipt")
	})
}

// TestA2AGateway_APIKeyRejected verifies that the A2A endpoint rejects
// API-key-based authentication attempts. The gateway requires mTLS; any
// request presenting an X-API-Key header without a client certificate must
// fail with HTTP 401. This is an auth-layer test, not a protocol-error test,
// so it is not part of the shared cross-protocol error-case table.
func TestA2AGateway_APIKeyRejected(t *testing.T) {
	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:          t.Name(),
		AllowTestPortZero: true,
	})
	fixture.WaitForReady(t)

	mtlsURL := network.LocalhostHTTPSURL(fixture.Service.GetHTTPSPort())
	plainClient := fixtures.CreateNoCertClient(t, fixture)
	reqBody := `{"jsonrpc":"2.0","id":1,"method":"a2a/call","params":{"skill_name":"test"}}`

	req, _ := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.A2ACall, bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "test-api-key")

	resp, err := plainClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

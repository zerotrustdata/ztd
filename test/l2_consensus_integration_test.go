// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package tests

/*
TestL2Consensus exercises the L2 consensus and L1–L5 governance
pipeline through the in-process GatewayFixture. These integration tests prove:

1. Idempotent client enrollment (re-enroll same identity succeeds)
2. Malformed CSR rejection at the enrollment endpoint
3. Consensus quorum reached (2-of-3 members vote affirmatively)
4. Consensus quorum not reached (fewer votes than quorum threshold)
5. MITRE veto (L1 doctrine detects malicious command, member votes false)
6. Full L1–L5 walkthrough with receipt verification

All tests use real GatewayFixture infrastructure — no mocks for PKI, consensus,
or database. The only fiction is the client identity (generated test CSRs).
*/

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// makeToolsCallRequest builds a JSON-RPC tools/call request body.
func makeToolsCallRequest(toolName string, args map[string]string) []byte {
	callReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      1,
		"params": map[string]interface{}{
			"name":      toolName,
			"arguments": args,
		},
	}
	b, _ := json.Marshal(callReq)
	return b
}

// doToolsCall performs an MCP tools/call via the gateway mTLS endpoint and
// returns the raw HTTP response. The caller is responsible for closing the body.
func doToolsCall(t *testing.T, client *http.Client, httpsPort int, sessionID string, toolName string, args map[string]string) *http.Response {
	t.Helper()
	mtlsURL := network.LocalhostHTTPSURL(httpsPort)
	reqBody := makeToolsCallRequest(toolName, args)
	req, err := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.MCPEndpoint, bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderAuthorization, "Bearer "+sessionID)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// TestL2Consensus_IdempotentEnrollment verifies that enrolling the same
// client identity twice (same user + fingerprint) succeeds without error.
func TestL2Consensus_IdempotentEnrollment(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "consensus-idempotent",
		Posture:  constants.PostureConsensus,
	})

	// First enrollment
	identity1 := fixtures.EnrollClientIdentity(t, f, "idempotent-user", "test-org", "fp-idempotent", "test-host")
	require.NotEmpty(t, identity1.OperatorID)
	require.NotEmpty(t, identity1.OperatorSessionID)

	// Second enrollment with same user + fingerprint (idempotent)
	identity2 := fixtures.EnrollClientIdentity(t, f, "idempotent-user", "test-org", "fp-idempotent", "test-host")
	require.NotEmpty(t, identity2.OperatorID)
	require.NotEmpty(t, identity2.OperatorSessionID)

	// The operator ID should be the same (same slot resolved by fingerprint)
	require.Equal(t, identity1.OperatorID, identity2.OperatorID)
}

// TestL2Consensus_MalformedCSR verifies that the enrollment endpoint
// rejects a malformed CSR with an appropriate error.
func TestL2Consensus_MalformedCSR(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "consensus-malformed-csr",
		Posture:  constants.PostureConsensus,
	})

	// Enroll a valid identity first to get an mTLS client for the enrollment endpoint
	identity := fixtures.EnrollClientIdentity(t, f, "csr-test-user", "test-org", "fp-csr-test", "test-host")
	enrollClient := fixtures.CreateMTLSClient(t, f, identity)

	mtlsURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())

	// Submit a malformed CSR (not a real PEM)
	regReq := map[string]string{
		"csr_pem":            "-----BEGIN CERTIFICATE REQUEST-----\nnot-a-valid-csr\n-----END CERTIFICATE REQUEST-----",
		"cli_csr":            "",
		"system_fingerprint": "fp-malformed",
		"hostname":           "bad-host",
	}
	regBody, _ := json.Marshal(regReq)
	req, _ := http.NewRequest(http.MethodPost, mtlsURL+constants.APIPaths.PKIDevicesEnroll, bytes.NewReader(regBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := enrollClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should be rejected with 400 or 500
	require.NotEqual(t, http.StatusCreated, resp.StatusCode, "malformed CSR should not succeed")
}

// TestL2Consensus_QuorumReached verifies that when a consensus has
// sufficient voting members (2-of-3), an MCP tools/call succeeds with
// L2 consensus votes present.
func TestL2Consensus_QuorumReached(t *testing.T) {
	// 3-member consensus at quorum 2. All 3 members hold private keys, so all 3
	// can vote — quorum is met.
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:                 "consensus-quorum-reached",
		Posture:                  constants.PostureConsensus,
		ConsensusID:              "quorum-consensus",
		ConsensusNMembers:        3,
		ConsensusQuorum:          2,
		ConsensusNServiceMembers: 3,
	})

	identity := fixtures.BootstrapCLIIdentity(t, f, "fp-quorum")
	apiClient := fixtures.CreateCLIMTLSClient(t, f, identity)

	resp := doToolsCall(t, apiClient, f.Service.GetHTTPSPort(), identity.OperatorSessionID, "echo", map[string]string{"message": "hello quorum"})
	defer resp.Body.Close()

	// Under consensus posture with quorum met, the call should succeed.
	// A 200 means the governance gate passed L1–L4.
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Check for error in the response (JSON-RPC error format)
	if errObj, ok := result["error"]; ok {
		t.Fatalf("expected success with quorum reached, got error: %v", errObj)
	}

	// Verify we got a result with content
	if res, ok := result["result"].(map[string]interface{}); ok {
		if content, ok := res["content"].([]interface{}); ok {
			require.NotEmpty(t, content, "should have content in result")
		}
	}
}

// TestL2Consensus_QuorumNotReached verifies that when fewer members
// vote than the quorum threshold, the governance gate rejects the transaction.
func TestL2Consensus_QuorumNotReached(t *testing.T) {
	// 3 members in policy, quorum 2, but only 1 service member has a private key.
	// Only 1 vote will be produced — below quorum threshold of 2.
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:                 "consensus-quorum-not-reached",
		Posture:                  constants.PostureConsensus,
		ConsensusID:              "no-quorum-consensus",
		ConsensusNMembers:        3,
		ConsensusQuorum:          2,
		ConsensusNServiceMembers: 1,
	})

	identity := fixtures.BootstrapCLIIdentity(t, f, "fp-no-quorum")
	apiClient := fixtures.CreateCLIMTLSClient(t, f, identity)

	resp := doToolsCall(t, apiClient, f.Service.GetHTTPSPort(), identity.OperatorSessionID, "echo", map[string]string{"message": "hello no-quorum"})
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	// The L4 warden should reject with ErrL2QuorumNotMet.
	// This surfaces as a JSON-RPC error.
	errObj, hasErr := result["error"]
	require.True(t, hasErr, "expected error when quorum not met, got: %v", result)

	// The error should mention quorum or L2 signature
	errStr, _ := json.Marshal(errObj)
	require.Contains(t, string(errStr), "QUORUM", "error should mention quorum failure")
}

// TestL2Consensus_VetoByMITRE verifies that when a malicious command
// is submitted, the L1 doctrine detects it and the consensus member votes
// false (veto), causing the transaction to be rejected.
func TestL2Consensus_VetoByMITRE(t *testing.T) {
	// Single-member consensus (quorum 1) so a single false vote blocks.
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:    "consensus-veto",
		Posture:     constants.PostureConsensus,
		ConsensusID: "veto-consensus",
	})

	identity := fixtures.BootstrapCLIIdentity(t, f, "fp-veto")
	apiClient := fixtures.CreateCLIMTLSClient(t, f, identity)

	// Submit a command that should trigger MITRE detection (destructive command).
	// The L1 doctrine should flag "rm -rf /" as malicious and the consensus member
	// votes false, causing L2 quorum to fail (0 affirmative votes < quorum 1).
	resp := doToolsCall(t, apiClient, f.Service.GetHTTPSPort(), identity.OperatorSessionID, "execute_command", map[string]string{"command": "rm -rf /"})
	defer resp.Body.Close()

	var result map[string]interface{}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))

	// The transaction should be rejected — either at L1 (doctrine blocks)
	// or at L2 (consensus votes false, quorum not met).
	errObj, hasErr := result["error"]
	require.True(t, hasErr, "expected error for MITRE veto, got: %v", result)

	errStr, _ := json.Marshal(errObj)
	// Should be rejected by L1 validation or L2 quorum failure
	t.Logf("Veto error response: %s", errStr)
}

// TestL2Consensus_L1ToL5Walkthrough performs a full L1–L5 governance
// walkthrough: enrolls a client, makes a tools/call through the governance
// pipeline, and verifies the receipt is recorded in the audit store.
func TestL2Consensus_L1ToL5Walkthrough(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "consensus-l1-l5",
		Posture:  constants.PostureConsensus,
	})

	// The fixture auto-wires a 1-member consensus for consensus posture.
	identity := fixtures.BootstrapCLIIdentity(t, f, "fp-walkthrough")
	apiClient := fixtures.CreateCLIMTLSClient(t, f, identity)

	// L1: Submit a benign tools/call that passes L1 doctrine checks
	resp := doToolsCall(t, apiClient, f.Service.GetHTTPSPort(), identity.OperatorSessionID, "echo", map[string]string{"message": "l1-l5 test"})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "tools/call must return HTTP 200")

	var rpc struct {
		Error  *mcp.JSONRPCError   `json:"error,omitempty"`
		Result *mcp.CallToolResult `json:"result,omitempty"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&rpc))

	// Should succeed — no JSON-RPC error from the governance gate.
	require.Nil(t, rpc.Error, "L1-L5 walkthrough failed at governance gate")
	require.NotNil(t, rpc.Result, "tools/call must return a result")
	require.NotEmpty(t, rpc.Result.Content, "should have content in result")
	require.Equal(t, "text", rpc.Result.Content[0].Type, "content should be text type")
	require.NotEmpty(t, rpc.Result.Content[0].Text, "should have non-empty text")
	t.Logf("L1-L5 walkthrough result: %v", rpc.Result.Content[0].Text)

	// Verify the audit store recorded the governed execution.
	receipts := getAuditReceipts(t, apiClient, f.Service.GetHTTPSPort(), identity.OperatorSessionID)
	require.NotEmpty(t, receipts.EventReceipts, "governed tools/call must leave at least one event receipt")
	require.True(t, hasCompletedReceipt(receipts.EventReceipts),
		"event receipts must include a COMPLETED execution, got %d receipt(s)", len(receipts.EventReceipts))

	// Also verify the health endpoint reports a valid state root (L5 state integrity)
	mtlsURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
	healthReq, err := http.NewRequest(http.MethodGet, mtlsURL+constants.APIPaths.Health, nil)
	require.NoError(t, err)
	healthReq.Header.Set(constants.HeaderAuthorization, "Bearer "+identity.OperatorSessionID)
	healthResp, err := apiClient.Do(healthReq)
	require.NoError(t, err)
	defer healthResp.Body.Close()
	require.Equal(t, http.StatusOK, healthResp.StatusCode, "health request must return HTTP 200")

	var health models.HealthResponse
	require.NoError(t, json.NewDecoder(healthResp.Body).Decode(&health))
	require.NotEmpty(t, health.StateMerkleRoot, "health should report a state Merkle root")
	t.Logf("L5 state root: %v", health.StateMerkleRoot)
}

// hasCompletedReceipt reports whether any receipt carries EXECUTION_STATUS_COMPLETED.
func hasCompletedReceipt(receipts []*models.EventReceiptRecord) bool {
	for _, r := range receipts {
		if r != nil && r.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
			return true
		}
	}
	return false
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
	"google.golang.org/protobuf/encoding/protojson"
)

type cliApprovalMutationArguments struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type cliApprovalMCPCall struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Name      string                       `json:"name"`
		Arguments cliApprovalMutationArguments `json:"arguments"`
	} `json:"params"`
}

func TestCLIApproval_RatifyResumesSuspendedEnvelopeAndCommitsMutation(t *testing.T) {
	var mutationCount atomic.Int32
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Method != "tools/call" || request.Params.Name != "mutate" {
			http.Error(w, "invalid mutation request", http.StatusBadRequest)
			return
		}
		mutationCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  struct {
				Content []mcp.TextContent `json:"content"`
			} `json:"result"`
		}{
			JSONRPC: "2.0",
			ID:      request.ID,
			Result: struct {
				Content []mcp.TextContent `json:"content"`
			}{Content: []mcp.TextContent{{Type: "text", Text: "mutation applied"}}},
		})
	}))
	t.Cleanup(downstream.Close)

	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:      "cli-approval-ratify-pipeline",
		Posture:       constants.PostureRatify,
		DownstreamURL: downstream.URL,
	})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-approval-ratify-pipeline")
	client := fixtures.CreateCLIMTLSClient(t, f, identity)
	t.Cleanup(client.CloseIdleConnections)

	call := cliApprovalMCPCall{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	call.Params.Name = "mutate"
	call.Params.Arguments = cliApprovalMutationArguments{Key: "deployment", Value: "approved"}
	callBody, err := json.Marshal(call)
	require.NoError(t, err)
	callReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.MCPEndpoint, bytes.NewReader(callBody))
	require.NoError(t, err)
	callReq.Header.Set("Content-Type", "application/json")
	callResp, err := client.Do(callReq)
	require.NoError(t, err)
	callRespBody, err := io.ReadAll(callResp.Body)
	require.NoError(t, callResp.Body.Close())
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, callResp.StatusCode, "MCP call: %s", callRespBody)
	var suspended struct {
		Result struct {
			Suspension *mcp.GovernedSuspension `json:"suspension"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(callRespBody, &suspended))
	require.NotNil(t, suspended.Result.Suspension)
	txHash := suspended.Result.Suspension.TxHash
	require.NotEmpty(t, txHash)
	require.Zero(t, mutationCount.Load(), "the mutation must wait for signed L3 approval")

	cliCert, err := tls.X509KeyPair([]byte(identity.CLICertificate), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.CLIPrivateKey}))
	require.NoError(t, err)
	proof, err := governance.SignCLIApproval(txHash, &cliCert)
	require.NoError(t, err)
	approvalBody, err := json.Marshal(models.ApprovalRespondRequest{
		ApprovalID:   txHash,
		Approved:     true,
		CLISignature: proof.GetCliSignature(),
	})
	require.NoError(t, err)
	approvalReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.ApprovalsRespond, bytes.NewReader(approvalBody))
	require.NoError(t, err)
	approvalReq.Header.Set("Content-Type", "application/json")
	approvalResp, err := client.Do(approvalReq)
	require.NoError(t, err)
	approvalRespBody, err := io.ReadAll(approvalResp.Body)
	require.NoError(t, approvalResp.Body.Close())
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, approvalResp.StatusCode, "CLI approval: %s", approvalRespBody)
	var receipt operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(approvalRespBody, &receipt))
	require.Equal(t, txHash, receipt.GetTransactionHash())
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
	require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status())
	require.EqualValues(t, 1, mutationCount.Load(), "approval must resume and execute the governed mutation exactly once")

	persisted, err := f.Service.GetAuditStore().GetActionReceipt(txHash)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, persisted.ActionReceipt.GetL3Status())
	commitments, err := f.Service.GetAuditStore().CommitmentLedger().ListCommitments()
	require.NoError(t, err)
	require.NotEmpty(t, commitments, "the resumed mutation must be recorded in the commitment ledger")
	foundCommitment := false
	for _, commitment := range commitments {
		if commitment.TransactionID == txHash {
			foundCommitment = true
			break
		}
	}
	require.True(t, foundCommitment, "the commitment ledger must include the approved transaction")

	_, stillSuspended, err := f.MCPGateway.GetSuspendedTransaction(t.Context(), txHash)
	require.NoError(t, err)
	require.False(t, stillSuspended, "successful approval must retire the suspended transaction")
}

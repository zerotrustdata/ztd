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
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
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

// newCLIApprovalMutationDownstream serves the governed "mutate" tool and counts
// every mutation that reaches it.
func newCLIApprovalMutationDownstream(t *testing.T, mutationCount *atomic.Int32) *httptest.Server {
	t.Helper()
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
	return downstream
}

func TestCLIApproval_RatifyResumesSuspendedEnvelopeAndCommitsMutation(t *testing.T) {
	var mutationCount atomic.Int32
	downstream := newCLIApprovalMutationDownstream(t, &mutationCount)

	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:      "cli-approval-ratify-pipeline",
		Posture:       constants.PostureRatify,
		DownstreamURL: downstream.URL,
	})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-approval-ratify-pipeline")
	client := fixtures.CreateCLIMTLSClient(t, f, identity)
	t.Cleanup(client.CloseIdleConnections)

	txHash := suspendCLIApprovalMutation(t, f, client, "deployment", "approved")
	require.Zero(t, mutationCount.Load(), "the mutation must wait for signed L3 approval")

	approvalRespBody := approveCLISuspension(t, f, client, identity, txHash)
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

// suspendCLIApprovalMutation submits a governed mutation over MCP, asserts that
// it is suspended awaiting signed L3 approval, and returns its transaction hash.
func suspendCLIApprovalMutation(t *testing.T, f *fixtures.GatewayFixture, client *http.Client, key, value string) string {
	t.Helper()
	call := cliApprovalMCPCall{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	call.Params.Name = "mutate"
	call.Params.Arguments = cliApprovalMutationArguments{Key: key, Value: value}
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
	return txHash
}

// approveCLISuspension signs the suspended transaction with the identity's CLI
// certificate, posts the approval, asserts that it is accepted, and returns the
// response body.
func approveCLISuspension(t *testing.T, f *fixtures.GatewayFixture, client *http.Client, identity *fixtures.ClientIdentity, txHash string) []byte {
	t.Helper()
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
	return approvalRespBody
}

// persistBindTargetOperatorSession stores an active operator session owned by
// userID, the kind of target a CLI bind accepts, in the Gateway's store.
func persistBindTargetOperatorSession(t *testing.T, f *fixtures.GatewayFixture, userID, operatorID, operatorSessionID string) {
	t.Helper()
	now := timestamppb.Now()
	opBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                operatorID,
		OperatorSessionId: operatorSessionID,
		Status:            string(constants.OperatorStatusActive),
		UserId:            userID,
		OperatorType:      string(constants.OperatorTypeRemote),
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	require.NoError(t, err)
	require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), string(constants.CollectionOperators), operatorID, opBytes))
}

// TestCLIApproval_SignedApprovalSurvivesInPlaceBind binds the enrolled CLI
// session to a second operator session, then approves a governed mutation with
// the same certificate. The certificate's URI SAN names the CLI session it was
// issued for, and the approval is accepted only when the session ID the
// request carries still matches it, so a bind must keep that ID.
func TestCLIApproval_SignedApprovalSurvivesInPlaceBind(t *testing.T) {
	var mutationCount atomic.Int32
	downstream := newCLIApprovalMutationDownstream(t, &mutationCount)

	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:      "cli-approval-bind-in-place",
		Posture:       constants.PostureRatify,
		DownstreamURL: downstream.URL,
	})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-approval-bind-in-place")
	client := fixtures.CreateCLIMTLSClient(t, f, identity)
	t.Cleanup(client.CloseIdleConnections)

	const boundOperatorSessionID = "cli-approval-bind-in-place-operator-session"
	persistBindTargetOperatorSession(t, f, identity.UserID, "cli-approval-bind-in-place-operator", boundOperatorSessionID)
	bindBody, err := json.Marshal(models.CLIBindRequest{OperatorSessionIDs: []string{identity.OperatorSessionID, boundOperatorSessionID}})
	require.NoError(t, err)
	bindReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.AuthCLIBind, bytes.NewReader(bindBody))
	require.NoError(t, err)
	bindReq.Header.Set("Content-Type", "application/json")
	bindResp, err := client.Do(bindReq)
	require.NoError(t, err)
	bindRespBody, err := io.ReadAll(bindResp.Body)
	require.NoError(t, bindResp.Body.Close())
	require.NoError(t, err)
	require.Equalf(t, http.StatusOK, bindResp.StatusCode, "CLI bind: %s", bindRespBody)
	var bound models.CLIBindResponse
	require.NoError(t, json.Unmarshal(bindRespBody, &bound))
	require.Equal(t, identity.CLISessionID, bound.CLISessionID, "a bind keeps the CLI session the certificate names")
	require.Len(t, bound.Bound, 2)

	txHash := suspendCLIApprovalMutation(t, f, client, "deployment", "approved-after-bind")
	require.Zero(t, mutationCount.Load(), "the mutation must wait for signed L3 approval")

	approvalRespBody := approveCLISuspension(t, f, client, identity, txHash)
	var receipt operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(approvalRespBody, &receipt))
	require.Equal(t, txHash, receipt.GetTransactionHash())
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
	require.EqualValues(t, 1, mutationCount.Load(), "approval from the bound CLI session must execute the mutation once")
}

// expireCLISessionForApprovalTest moves every expiry field of the stored CLI
// session into the past: the state a refresh recovers from.
func expireCLISessionForApprovalTest(t *testing.T, f *fixtures.GatewayFixture, cliSessionID string) {
	t.Helper()
	store := f.Service.GetDocStore()
	doc, err := store.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID)
	require.NoError(t, err)
	require.NotNil(t, doc)
	raw, err := json.Marshal(doc.Data)
	require.NoError(t, err)
	var session models.CLISession
	require.NoError(t, json.Unmarshal(raw, &session))
	past := time.Now().UTC().Add(-time.Hour)
	session.ExpiresAt = past
	session.AbsoluteExpiresAt = past
	session.IdleExpiresAt = past
	data, err := json.Marshal(session)
	require.NoError(t, err)
	require.NoError(t, store.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, data))
}

// TestCLIApproval_SignedApprovalSurvivesRefreshInPlace expires the enrolled CLI
// session, refreshes it with the same certificate, and then approves a governed
// mutation with that certificate. Refresh extends the session the certificate
// names, so the signed approval still matches the session it is checked against.
func TestCLIApproval_SignedApprovalSurvivesRefreshInPlace(t *testing.T) {
	var mutationCount atomic.Int32
	downstream := newCLIApprovalMutationDownstream(t, &mutationCount)

	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:      "cli-approval-refresh-in-place",
		Posture:       constants.PostureRatify,
		DownstreamURL: downstream.URL,
	})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-approval-refresh-in-place")
	client := fixtures.CreateCLIMTLSClient(t, f, identity)
	t.Cleanup(client.CloseIdleConnections)

	expireCLISessionForApprovalTest(t, f, identity.CLISessionID)

	refreshReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.AuthCLIRefresh, bytes.NewReader([]byte("{}")))
	require.NoError(t, err)
	refreshReq.Header.Set("Content-Type", "application/json")
	refreshResp, err := client.Do(refreshReq)
	require.NoError(t, err)
	refreshBody, err := io.ReadAll(refreshResp.Body)
	require.NoError(t, refreshResp.Body.Close())
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, refreshResp.StatusCode, "CLI refresh: %s", refreshBody)
	var refreshed models.CLIRefreshResponse
	require.NoError(t, json.Unmarshal(refreshBody, &refreshed))
	require.Equal(t, identity.CLISessionID, refreshed.CLISessionID, "a refresh keeps the CLI session the certificate names")

	txHash := suspendCLIApprovalMutation(t, f, client, "deployment", "approved-after-refresh")
	require.Zero(t, mutationCount.Load(), "the mutation must wait for signed L3 approval")

	approvalRespBody := approveCLISuspension(t, f, client, identity, txHash)
	var receipt operatorv1.ActionReceipt
	require.NoError(t, protojson.Unmarshal(approvalRespBody, &receipt))
	require.Equal(t, txHash, receipt.GetTransactionHash())
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
	require.EqualValues(t, 1, mutationCount.Load(), "approval from the refreshed CLI session must execute the mutation once")
}

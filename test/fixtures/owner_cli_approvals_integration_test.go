// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package fixtures

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	receiptgov "github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestEvalStackOwnerCLIApprovals(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			stack := NewEvalStack(t, EvalStackOptions{Posture: posture})
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			receipts, err := stack.Owner.AwaitApprovalReceipts(ctx, 2)
			require.NoError(t, err)
			require.Len(t, receipts, 2, "each outbound Operator enrollment must be approved through the owner's CLI identity")

			enrollmentIDs, err := stack.Owner.AwaitEnrollmentRequests(ctx, 2)
			require.NoError(t, err)
			require.Len(t, enrollmentIDs, 2)
			wantOperators := map[string]struct{}{}
			data, err := stack.Data.Workload()
			require.NoError(t, err)
			inference, err := stack.Inference.Workload()
			require.NoError(t, err)
			wantOperators[data.OperatorID] = struct{}{}
			wantOperators[inference.OperatorID] = struct{}{}
			require.Len(t, wantOperators, 2, "the stack must run two distinct outbound Operators")

			for _, requestID := range enrollmentIDs {
				request, err := stack.Owner.StoredRequest(ctx, requestID)
				require.NoError(t, err)
				require.Equal(t, models.PlatformComponentOperator, request.ComponentKind)
				require.Equal(t, models.PlatformEnrollmentStateCompleted, request.State)
				_, ok := wantOperators[request.OperatorID]
				require.True(t, ok, "completed enrollment %s must belong to one of the two outbound Operators", requestID)
				delete(wantOperators, request.OperatorID)
			}
			require.Empty(t, wantOperators, "both outbound Operators must complete enrollment")

			publicKey := stack.Gateway.ActuatorPriv.Public().(ed25519.PublicKey)
			seenHashes := make(map[string]struct{}, len(receipts))
			for _, receipt := range receipts {
				require.NotEmpty(t, receipt.GetTransactionHash())
				_, duplicate := seenHashes[receipt.GetTransactionHash()]
				require.False(t, duplicate, "each enrollment must yield a distinct transaction receipt")
				seenHashes[receipt.GetTransactionHash()] = struct{}{}
				require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
				require.Equal(t, string(constants.ActionTypePlatformEnrollmentDecide), receipt.GetActionType())
				// Platform enrollment is a bootstrap action, so L2 stays exempt in
				// notary even though ordinary mutations require a valid consensus proof.
				require.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, receipt.GetL2Status())
				require.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, receipt.GetL3Status())
				require.Equal(t, stack.Gateway.ActuatorKeyID, receipt.GetSignerKeyId())
				require.NoError(t, receiptgov.VerifyActionReceipt(receipt, publicKey), "receipt must verify with the Gateway actuator key")

				persisted, err := stack.Gateway.Service.GetAuditStore().GetEventReceipt(receipt.GetTransactionId())
				require.NoError(t, err)
				require.NotNil(t, persisted, "approval receipt must be present in the Gateway's authoritative audit store")
				require.NotNil(t, persisted.ActionReceipt)
				require.True(t, proto.Equal(receipt, persisted.ActionReceipt), "the API receipt must equal the Gateway's persisted receipt")
			}

			stack.RequireOutboundMTLS(t, stack.Data)
			stack.RequireOutboundMTLS(t, stack.Inference)
		})
	}
}

// TestRespondSuspendedTransactionsContinuesAfterFailedApproval pins that one
// failed approval is recorded without stopping the rest of the pending batch.
func TestRespondSuspendedTransactionsContinuesAfterFailedApproval(t *testing.T) {
	failedHash := sha256Hex([]byte("failed-approval"))
	approvedHash := sha256Hex([]byte("approved-approval"))
	identity := newOwnerCLIApprovalTestIdentity(t, "user-1", "session-1")

	var mu sync.Mutex
	var responded []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case constants.APIPaths.ApprovalsCLIList:
			body, err := json.Marshal(models.SuspendedTransactionsResponse{Transactions: []models.SuspendedTxResponse{
				{TransactionHash: failedHash, ToolName: "tool"},
				{TransactionHash: approvedHash, ToolName: "tool"},
			}})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(body)
		case constants.APIPaths.ApprovalsRespond:
			var req models.ApprovalRespondRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			responded = append(responded, req.ApprovalID)
			mu.Unlock()
			if req.ApprovalID == failedHash {
				http.Error(w, "approval store unavailable", http.StatusInternalServerError)
				return
			}
			wire, err := protojson.Marshal(&operatorv1.ActionReceipt{
				TransactionHash: req.ApprovalID,
				Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				L3Status:        operatorv1.L3Status_L3_STATUS_REQUIRED_VALID,
			})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", constants.HeaderValueApplicationJSON)
			_, _ = w.Write(wire)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	owner := &OwnerConsole{approvalReceipts: newEventLog[*operatorv1.ActionReceipt]()}
	err := respondSuspendedTransactions(t.Context(), server.Client(), server.URL, identity, owner)
	require.ErrorContains(t, err, failedHash, "the failed approval must be reported")

	mu.Lock()
	gotResponded := append([]string(nil), responded...)
	mu.Unlock()
	require.Equal(t, []string{failedHash, approvedHash}, gotResponded, "a failed approval must not stop the remaining pending transactions")

	receipts := owner.ApprovalReceipts()
	require.Len(t, receipts, 1, "only the successful approval produces a recorded receipt")
	require.Equal(t, approvedHash, receipts[0].GetTransactionHash())
}

// newOwnerCLIApprovalTestIdentity builds a CLI approval identity whose
// certificate satisfies the production CLI L3 approval checks.
func newOwnerCLIApprovalTestIdentity(t *testing.T, userID, cliSessionID string) *ClientIdentity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	uri, err := url.Parse("spiffe://ztd.local/cli/" + userID + "/" + cliSessionID)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "owner cli"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{uri},
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	return &ClientIdentity{
		UserID:         userID,
		CLISessionID:   cliSessionID,
		CLICertificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		CLIPrivateKey:  keyDER,
	}
}

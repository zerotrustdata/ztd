// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// approvalRespondMCP is an in-memory MCPServiceProvider for POST /api/v1/approvals/respond.
type approvalRespondMCP struct {
	suspended    *models.SuspendedTransaction
	found        bool
	receipt      *operatorv1.ActionReceipt
	resumedHash  string
	resumedUser  string
	resumedProof *commonv1.L3Proof
	resumeCalls  int
	denyReceipt  *operatorv1.ActionReceipt
	denyErr      error
	deniedHash   string
	deniedUser   string
	denyCalls    int
}

func (m *approvalRespondMCP) GetSuspendedTransaction(_ context.Context, _ string) (*models.SuspendedTransaction, bool, error) {
	return m.suspended, m.found, nil
}

func (m *approvalRespondMCP) ResumeWithL3Proof(_ context.Context, txHash, userID string, proof *commonv1.L3Proof) (*operatorv1.ActionReceipt, error) {
	m.resumeCalls++
	m.resumedHash, m.resumedUser, m.resumedProof = txHash, userID, proof
	return m.receipt, nil
}

func (m *approvalRespondMCP) DenySuspendedTransaction(_ context.Context, txHash, userID string) (*operatorv1.ActionReceipt, error) {
	m.denyCalls++
	m.deniedHash, m.deniedUser = txHash, userID
	return m.denyReceipt, m.denyErr
}

// approvalRespondStore records the approve/deny mutations the handler performs
// on the suspended-transaction store. Unused methods panic via the nil embed.
type approvalRespondStore struct {
	storage.SuspendedTransactionStore
	approvedHash string
	proof        models.ApprovalProof
	deletedHash  string
}

func (s *approvalRespondStore) ApproveSuspendedTransaction(_ context.Context, txHash string, proof models.ApprovalProof) error {
	s.approvedHash, s.proof = txHash, proof
	return nil
}

func (s *approvalRespondStore) DeleteSuspendedTransaction(_ context.Context, txHash string) error {
	s.deletedHash = txHash
	return nil
}

func newApprovalRespondHandler(mcp *approvalRespondMCP, store *approvalRespondStore) *PasskeyHandler {
	logger := testutil.NewTestLogger()
	return NewPasskeyHandler(PasskeyHandlerDeps{
		Service:    &PasskeyService{logger: logger},
		Responder:  response.NewWriter(logger),
		MaxPayload: 1 << 20,
		Orchestrator: &PasskeyOrchestrator{
			mcpSvc:         mcp,
			suspendedStore: store,
			logger:         logger,
		},
		Posture: constants.PostureDoctrine,
	})
}

func approvalRespondRequest(body, userID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/respond", strings.NewReader(body))
	if userID == "" {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, userID))
}

func TestHandleApprovalRespond(t *testing.T) {
	const userID = "user-approval-respond-1"
	const txHash = "tx-approval-respond-1"

	suspendedFor := func(owner string) *approvalRespondMCP {
		return &approvalRespondMCP{
			suspended: &models.SuspendedTransaction{
				TransactionHash:   txHash,
				OperatorSessionID: "operator-session-1",
				Envelope:          []byte("{}"),
				UserID:            owner,
				ToolName:          "test-tool",
				ToolArguments:     []byte("{}"),
				ExpiresAt:         time.Now().Add(5 * time.Minute),
			},
			found: true,
		}
	}

	t.Run("Failure - method not allowed", func(t *testing.T) {
		h := newApprovalRespondHandler(&approvalRespondMCP{}, &approvalRespondStore{})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/approvals/respond", nil)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Failure - unauthorized without user context", func(t *testing.T) {
		h := newApprovalRespondHandler(&approvalRespondMCP{}, &approvalRespondStore{})
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, ""))

		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("Failure - no suspended transaction returns 404", func(t *testing.T) {
		mcp := &approvalRespondMCP{found: false}
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, userID))

		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Contains(t, rr.Body.String(), "transaction not found or expired")
		assert.Empty(t, store.approvedHash, "no approval may be recorded without a suspended transaction")
		assert.Empty(t, store.deletedHash)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("Failure - malformed JSON returns 400", func(t *testing.T) {
		mcp := suspendedFor(userID)
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest("{invalid}", userID))

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Contains(t, rr.Body.String(), "invalid JSON body")
		assert.Empty(t, store.approvedHash)
		assert.Empty(t, store.deletedHash)
	})

	t.Run("Failure - missing approval_id returns 400", func(t *testing.T) {
		mcp := suspendedFor(userID)
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approved":false}`, userID))

		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Empty(t, store.approvedHash)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("Failure - another user's transaction returns 403", func(t *testing.T) {
		mcp := suspendedFor("user-owner-someone-else")
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, userID))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Empty(t, store.approvedHash)
		assert.Empty(t, store.deletedHash)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("Failure - approve is rejected without a passkey assertion", func(t *testing.T) {
		mcp := suspendedFor(userID)
		mcp.receipt = &operatorv1.ActionReceipt{TransactionId: "transaction-approval-respond-1", TransactionHash: txHash}
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":true}`, userID))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrPasskeyProofRequired.Error())
		assert.Empty(t, store.approvedHash, "approve must not record an approval without a WebAuthn assertion")
		assert.Empty(t, store.deletedHash)
		assert.Zero(t, mcp.resumeCalls, "approve must not resume the suspended transaction")
	})

	t.Run("Failure - deny of a transaction that expired before the claim returns 404", func(t *testing.T) {
		mcp := suspendedFor(userID)
		mcp.denyErr = fmt.Errorf("gateway: %w", constants.ErrTransactionExpired)
		h := newApprovalRespondHandler(mcp, &approvalRespondStore{})
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, userID))

		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Equal(t, 1, mcp.denyCalls)
	})

	t.Run("Failure - deny receipt for another transaction fails closed", func(t *testing.T) {
		mcp := suspendedFor(userID)
		mcp.denyReceipt = &operatorv1.ActionReceipt{TransactionHash: "tx-other"}
		h := newApprovalRespondHandler(mcp, &approvalRespondStore{})
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, userID))

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("Success - deny returns the signed L3_DENIED rejection receipt", func(t *testing.T) {
		mcp := suspendedFor(userID)
		mcp.denyReceipt = &operatorv1.ActionReceipt{
			TransactionId:   "transaction-approval-respond-1",
			TransactionHash: txHash,
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			FailureCode:     operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
			RejectionCode:   operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED,
		}
		store := &approvalRespondStore{}
		h := newApprovalRespondHandler(mcp, store)
		rr := httptest.NewRecorder()

		h.handleApprovalRespond(rr, approvalRespondRequest(`{"approval_id":"`+txHash+`","approved":false}`, userID))

		require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
		got := &operatorv1.ActionReceipt{}
		require.NoError(t, protojson.Unmarshal(rr.Body.Bytes(), got))
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, got.GetRejectionCode())
		assert.Equal(t, txHash, got.GetTransactionHash())

		assert.Equal(t, 1, mcp.denyCalls)
		assert.Equal(t, txHash, mcp.deniedHash)
		assert.Equal(t, userID, mcp.deniedUser)
		assert.Empty(t, store.approvedHash)
		assert.Empty(t, store.deletedHash, "the deny path owns the suspended record, the handler never deletes it")
		assert.Zero(t, mcp.resumeCalls, "a denied transaction is never resumed")
	})
}

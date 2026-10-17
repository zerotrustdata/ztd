// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

// handleApprovalAction dispatches approval sub-actions (challenge, verify) based on
// the path segment after /api/v1/approvals/{txHash}/. It is not a standalone REST
// endpoint — see handleApprovalChallenge and handleApprovalVerify for the annotated
// sub-handlers.
func (h *PasskeyHandler) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		h.responder.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, constants.APIPaths.ApprovalsPrefix)
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		h.responder.Error(w, http.StatusBadRequest, "invalid request path")
		return
	}

	txHash := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "challenge":
		h.handleApprovalChallenge(w, r, txHash, userID)
	case "verify":
		h.handleApprovalVerify(w, r, txHash, userID)
	default:
		h.responder.Error(w, http.StatusBadRequest, "unknown action")
	}
}

// @Summary		Get approval challenge
// @Description	Generates a WebAuthn assertion challenge for approving a suspended transaction.
// @Tags			approvals
// @Produce		json
// @Param		txHash	path		string	true	"Transaction hash"
// @Success		200		{object}	json.RawMessage	"WebAuthn assertion options"
// @Failure		404		{string}	string			"Transaction not found or expired"
// @Failure		403		{string}	string			"Forbidden — belongs to another user"
// @Router			/api/v1/approvals/{txHash}/challenge [get]
func (h *PasskeyHandler) handleApprovalChallenge(w http.ResponseWriter, r *http.Request, txHash, userID string) {
	if r.Method != http.MethodGet {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	suspendedTx, ok, err := h.orchestrator.GetSuspendedTransaction(r.Context(), txHash)
	if err != nil {
		h.logger.Error("Failed to get suspended transaction", "error", err, "txHash", txHash)
		h.responder.Error(w, http.StatusInternalServerError, "failed to get transaction")
		return
	}
	if !ok {
		h.responder.Error(w, http.StatusNotFound, "transaction not found or expired")
		return
	}

	if suspendedTx.UserID != "" && suspendedTx.UserID != userID {
		h.responder.Error(w, http.StatusForbidden, "transaction belongs to another user")
		return
	}

	options, err := h.GenerateApprovalChallenge(r.Context(), userID, txHash)
	if err != nil {
		h.responder.Error(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.responder.JSON(w, http.StatusOK, options)
}

// @Summary		Verify approval
// @Description	Verifies a WebAuthn assertion response to approve a suspended transaction. On success,
// @Description	the transaction resumes execution with the L3 proof attached and an SSE event is emitted.
// @Tags			approvals
// @Accept		json
// @Produce		json
// @Param		txHash	path		string						true	"Transaction hash"
// @Param		body	body		models.WebAuthnAssertionResponse	true	"WebAuthn assertion response"
// @Success		200		{object}	operatorv1.ActionReceipt				"Canonical protojson ActionReceipt"
// @Failure		403		{string}	string						"Forbidden — verification failed"
// @Failure		404		{string}	string						"Transaction not found or expired"
// @Failure		503		{string}	string						"Service Unavailable — runtime vault locked; the proof can be resubmitted"
// @Router			/api/v1/approvals/{txHash}/verify [post]
func (h *PasskeyHandler) handleApprovalVerify(w http.ResponseWriter, r *http.Request, txHash, userID string) {
	if r.Method != http.MethodPost {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	suspendedTx, ok, err := h.orchestrator.GetSuspendedTransaction(r.Context(), txHash)
	if err != nil {
		h.logger.Error("Failed to get suspended transaction", "error", err, "txHash", txHash)
		h.responder.Error(w, http.StatusInternalServerError, "failed to get transaction")
		return
	}
	if !ok {
		h.responder.Error(w, http.StatusNotFound, "transaction not found or expired")
		return
	}

	if suspendedTx.UserID != "" && suspendedTx.UserID != userID {
		h.responder.Error(w, http.StatusForbidden, "transaction belongs to another user")
		return
	}

	body, err := h.readBody(w, r)
	if err != nil {
		h.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var req models.WebAuthnAssertionResponse
	if err := json.Unmarshal(body, &req); err != nil {
		h.responder.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	proof := &commonv1.L3Proof{
		ClientDataJson:    req.ClientDataJSON,
		AuthenticatorData: req.AuthenticatorData,
		Signature:         req.Signature,
		CredentialId:      req.ID,
	}

	receipt, err := h.orchestrator.ResumeWithL3Proof(r.Context(), txHash, userID, proof)
	if err != nil {
		// A locked runtime vault is a Gateway availability fault, not a proof
		// rejection. The suspension stays in place, so the same proof can be
		// resubmitted once the vault is unlocked.
		if errors.Is(err, constants.ErrAuditStoreVaultLocked) {
			h.logger.Error("Approval resume blocked: runtime vault locked", "error", err, "txHash", txHash)
			h.responder.Error(w, http.StatusServiceUnavailable, constants.ErrAuditStoreVaultLocked.Error())
			return
		}
		if receipt != nil {
			h.responder.JSON(w, http.StatusForbidden, receipt)
			return
		}
		h.responder.Error(w, http.StatusForbidden, err.Error())
		return
	}

	// The resumed transaction must produce the receipt for the suspended hash. A
	// mismatch means the Gateway cannot vouch for this approval, so it fails closed.
	if receipt == nil || receipt.GetTransactionHash() != suspendedTx.TransactionHash {
		h.logger.Error("Resumed receipt does not match suspended transaction", "txHash", txHash)
		h.responder.Error(w, http.StatusInternalServerError, constants.ErrInternal.Error())
		return
	}
	receiptJSON, err := response.MarshalProtoJSONCanonical(receipt)
	if err != nil {
		h.logger.Error("Failed to encode approval receipt", "error", err, "txHash", txHash)
		h.responder.Error(w, http.StatusInternalServerError, constants.ErrResponseEncodeJSON.Error())
		return
	}

	sseUserID := userID
	if sseUserID == "" {
		sseUserID = suspendedTx.UserID
	}
	h.orchestrator.EmitApprovalCompletedSSE(sseUserID, suspendedTx.SubmitterCLISessionID, txHash, receiptJSON)

	h.responder.JSON(w, http.StatusOK, receipt)
}

// @Summary		Deny a suspended transaction
// @Description	Denies a suspended transaction owned by the caller. The envelope is re-submitted to its executing
// @Description	Operator with the L3 deny marker, so L4 rejects it with `GOVERNANCE_REJECTION_CODE_L3_DENIED` and
// @Description	L5 signs the rejection receipt. The receipt is returned and published as `approval.completed` to
// @Description	the CLI session that submitted the transaction. Under ratify or notary posture a CLI caller must
// @Description	present an mTLS client certificate and a web caller must hold a registered passkey.
// @Description	`approved: true` is always rejected with 403: approval requires a WebAuthn assertion through
// @Description	`/api/v1/approvals/{txHash}/verify`.
// @Tags			approvals
// @Accept		json
// @Produce		json
// @Param		body	body		models.ApprovalRespondRequest	true	"Approval decision"
// @Success		200		{object}	operatorv1.ActionReceipt	"Canonical protojson ActionReceipt: the signed GOVERNANCE_REJECTED receipt of the deny"
// @Failure		400		{string}	string	"Invalid request body"
// @Failure		401		{string}	string	"Unauthorized"
// @Failure		403		{string}	string	"Forbidden — approval requested, session not grounded for posture, or transaction belongs to another user"
// @Failure		404		{string}	string	"Transaction not found or expired"
// @Failure		500		{string}	string	"The deny produced no signed rejection receipt"
// @Failure		503		{string}	string	"Service Unavailable — runtime vault locked; the deny can be resubmitted"
// @Router			/api/v1/approvals/respond [post]
func (h *PasskeyHandler) handleApprovalRespond(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	cliSessionID, _ := r.Context().Value(constants.ContextKeyCLISessionID).(string)
	if strings.TrimSpace(userID) == "" {
		h.responder.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	body, err := h.readBody(w, r)
	if err != nil {
		h.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req models.ApprovalRespondRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.ApprovalID) == "" {
		h.responder.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// L3 approval is a WebAuthn assertion over the transaction hash, verified
	// by the passkey notary at resume. This route carries no assertion, so it
	// can only deny; approvals go through the approval page's verify action.
	if req.Approved {
		h.responder.Error(w, http.StatusForbidden, constants.ErrPasskeyProofRequired.Error())
		return
	}

	if h.posture.RequiresL3() {
		if cliSessionID != "" {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				h.responder.Error(w, http.StatusForbidden, fmt.Sprintf("denial requires an mTLS grounded session under %s posture", h.posture))
				return
			}
		} else {
			user, err := h.userStore.GetUser(r.Context(), userID)
			if err != nil || !user.HasPasskey() {
				h.responder.Error(w, http.StatusForbidden, fmt.Sprintf("denial requires a WebAuthn grounded session under %s posture", h.posture))
				return
			}
		}
	}

	tx, ok, err := h.orchestrator.GetSuspendedTransaction(r.Context(), req.ApprovalID)
	if err != nil {
		h.logger.Error("Failed to get suspended transaction", "error", err, "txHash", req.ApprovalID)
		h.responder.Error(w, http.StatusInternalServerError, "failed to get transaction")
		return
	}
	if !ok || tx == nil {
		h.responder.Error(w, http.StatusNotFound, "transaction not found or expired")
		return
	}
	if tx.UserID != "" && tx.UserID != userID {
		h.responder.Error(w, http.StatusForbidden, "transaction belongs to another user")
		return
	}

	receipt, err := h.orchestrator.DenySuspendedTransaction(r.Context(), req.ApprovalID, userID)
	if err != nil {
		switch {
		case errors.Is(err, constants.ErrTransactionExpired):
			h.responder.Error(w, http.StatusNotFound, "transaction not found or expired")
		case errors.Is(err, constants.ErrAuditStoreVaultLocked):
			h.logger.Error("Deny blocked: runtime vault locked", "error", err, "txHash", req.ApprovalID)
			h.responder.Error(w, http.StatusServiceUnavailable, constants.ErrAuditStoreVaultLocked.Error())
		default:
			h.logger.Error("Failed to deny suspended transaction", "error", err, "txHash", req.ApprovalID)
			h.responder.Error(w, http.StatusInternalServerError, "failed to deny transaction")
		}
		return
	}

	// The deny must produce the signed rejection for the suspended hash. A
	// mismatch means the Gateway cannot vouch for this deny, so it fails closed.
	if receipt == nil || receipt.GetTransactionHash() != tx.TransactionHash {
		h.logger.Error("Deny receipt does not match suspended transaction", "txHash", req.ApprovalID)
		h.responder.Error(w, http.StatusInternalServerError, constants.ErrInternal.Error())
		return
	}
	receiptJSON, err := response.MarshalProtoJSONCanonical(receipt)
	if err != nil {
		h.logger.Error("Failed to encode deny receipt", "error", err, "txHash", req.ApprovalID)
		h.responder.Error(w, http.StatusInternalServerError, constants.ErrResponseEncodeJSON.Error())
		return
	}
	h.orchestrator.EmitApprovalCompletedSSE(userID, tx.SubmitterCLISessionID, req.ApprovalID, receiptJSON)
	h.responder.JSON(w, http.StatusOK, receipt)
}

// handleCLIApprovalStatus is an mTLS-authenticated endpoint that returns the current
// status of a suspended transaction. Used by CLI clients for post-SSE verification
// of approval state.
func (h *PasskeyHandler) handleCLIApprovalStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	txHash := strings.TrimPrefix(r.URL.Path, constants.APIPaths.ApprovalsCLIStatus)
	if txHash == "" {
		h.responder.Error(w, http.StatusBadRequest, "transaction hash required")
		return
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		h.responder.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	suspendedTx, found, err := h.orchestrator.GetSuspendedTransaction(r.Context(), txHash)
	if err != nil {
		h.logger.Error("Failed to get suspended transaction", "error", err, "txHash", txHash)
		h.responder.Error(w, http.StatusInternalServerError, "failed to get transaction")
		return
	}

	if !found {
		h.responder.JSON(w, http.StatusOK, models.ApprovalStatusResponse{
			Status: string(constants.SuspendedTxStatusExpiredOrNotFound),
		})
		return
	}

	if suspendedTx.UserID != "" && suspendedTx.UserID != userID {
		h.responder.Error(w, http.StatusForbidden, "transaction belongs to another user")
		return
	}

	if suspendedTx.Approved {
		h.responder.JSON(w, http.StatusOK, models.ApprovalStatusResponse{
			Status:   string(constants.SuspendedTxStatusApproved),
			TxHash:   txHash,
			ToolName: suspendedTx.ToolName,
		})
		return
	}

	h.responder.JSON(w, http.StatusOK, models.ApprovalStatusResponse{
		Status:   string(constants.SuspendedTxStatusPending),
		TxHash:   txHash,
		ToolName: suspendedTx.ToolName,
	})
}

func (h *PasskeyHandler) handleApprovalPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	txHash := strings.TrimPrefix(r.URL.Path, constants.APIPaths.ApprovePagePrefix)
	if txHash == "" {
		http.Error(w, "transaction hash required", http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/console/#approve="+url.QueryEscape(txHash), http.StatusFound)
}

func (h *PasskeyHandler) handleCLIListSuspended(w http.ResponseWriter, r *http.Request) {
	h.listSuspendedTransactions(w, r, true)
}

// @Summary		List pending approvals
// @Description	Lists suspended transactions pending L3 passkey approval for the authenticated user.
// @Tags			approvals
// @Produce		json
// @Success		200	{object}	models.SuspendedTransactionsResponse
// @Failure		401	{string}	string	"Unauthorized"
// @Router			/api/v1/approvals [get]
func (h *PasskeyHandler) handleListSuspendedTransactions(w http.ResponseWriter, r *http.Request) {
	h.listSuspendedTransactions(w, r, false)
}

func (h *PasskeyHandler) listSuspendedTransactions(w http.ResponseWriter, r *http.Request, filterApproved bool) {
	if r.Method != http.MethodGet {
		h.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		h.responder.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	transactions, err := h.orchestrator.ListSuspendedTransactions(r.Context(), userID)
	if err != nil {
		h.logger.Error("Failed to list suspended transactions", "error", err, "userID", userID)
		h.responder.Error(w, http.StatusInternalServerError, "failed to list suspended transactions")
		return
	}

	var txResponses []models.SuspendedTxResponse
	for _, tx := range transactions {
		if filterApproved && tx.Approved {
			continue
		}
		txResponses = append(txResponses, models.SuspendedTxResponse{
			TransactionHash: tx.TransactionHash,
			CreatedAt:       tx.CreatedAt,
			ExpiresAt:       tx.ExpiresAt,
			ToolName:        tx.ToolName,
			UserID:          tx.UserID,
			OperatorID:      tx.OperatorID,
		})
	}

	h.responder.JSON(w, http.StatusOK, models.SuspendedTransactionsResponse{
		Transactions: txResponses,
	})
}

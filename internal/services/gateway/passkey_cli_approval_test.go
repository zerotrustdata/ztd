// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/protocol"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	cliApprovalTestUser    = "user-cli-approval-1"
	cliApprovalTestSession = "cli-session-approval-1"
	cliApprovalTestHash    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func newCLIApprovalTestCertificate(t *testing.T, userID, sessionID string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	spiffeID, err := url.Parse(protocol.NewWorkloadIdentity().CLISPIFFEID(userID, sessionID))
	require.NoError(t, err)
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{spiffeID},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func signedCLIApprovalRequest(t *testing.T, signedHash string, cert tls.Certificate, sessionID string) *http.Request {
	t.Helper()
	proof, err := governance.SignCLIApproval(signedHash, &cert)
	require.NoError(t, err)
	body, err := json.Marshal(models.ApprovalRespondRequest{
		ApprovalID:   cliApprovalTestHash,
		Approved:     true,
		CLISignature: proof.GetCliSignature(),
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/respond", bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), constants.ContextKeyUserID, cliApprovalTestUser)
	ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, sessionID)
	req = req.WithContext(ctx)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}}
	return req
}

func newCLIApprovalMCP(owner string) *approvalRespondMCP {
	return &approvalRespondMCP{
		suspended: &models.SuspendedTransaction{
			TransactionHash:   cliApprovalTestHash,
			OperatorSessionID: "operator-session-cli-approval",
			Envelope:          []byte("{}"),
			UserID:            owner,
			ExpiresAt:         time.Now().Add(5 * time.Minute),
		},
		found: true,
		receipt: &operatorv1.EventReceipt{
			TransactionId:   "transaction-cli-approval-1",
			TransactionHash: cliApprovalTestHash,
			L3Status:        operatorv1.L3Status_L3_STATUS_REQUIRED_VALID,
		},
	}
}

func TestHandleSignedCLIApproval(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture)+" success", func(t *testing.T) {
			mcp := newCLIApprovalMCP(cliApprovalTestUser)
			h := newApprovalRespondHandler(mcp, &approvalRespondStore{})
			h.posture = posture
			cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
			rr := httptest.NewRecorder()

			h.handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

			require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
			assert.Equal(t, 1, mcp.resumeCalls)
			assert.Equal(t, cliApprovalTestHash, mcp.resumedHash)
			assert.Equal(t, cliApprovalTestUser, mcp.resumedUser)
			require.NotNil(t, mcp.resumedProof)
			assert.NotEmpty(t, mcp.resumedProof.GetCliSignature())
			assert.Equal(t, cert.Certificate[0], mcp.resumedProof.GetCliCertificateDer())
			assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, mcp.receipt.GetL3Status())
		})
	}

	t.Run("tampered signature is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		req := signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession)
		var payload models.ApprovalRespondRequest
		require.NoError(t, json.NewDecoder(req.Body).Decode(&payload))
		payload.CLISignature = payload.CLISignature[:len(payload.CLISignature)-2] + "00"
		req = approvalRespondRequestBody(t, payload, cliApprovalTestUser, cliApprovalTestSession, cert)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrTxL3ProofInvalid.Error())
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("signature for another hash is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		otherHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, otherHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("certificate for another owner is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, "user-other", cliApprovalTestSession)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("transaction owned by another user is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		mcp := newCLIApprovalMCP("user-other")
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrCLIL3SessionUserMismatch.Error())
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("wrong TLS session is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, "cli-session-other")
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrTxL3ProofInvalid.Error())
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("signature from another certificate is rejected", func(t *testing.T) {
		signerCert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		peerCert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		proof, err := governance.SignCLIApproval(cliApprovalTestHash, &signerCert)
		require.NoError(t, err)
		payload := models.ApprovalRespondRequest{ApprovalID: cliApprovalTestHash, Approved: true, CLISignature: proof.GetCliSignature()}
		req := approvalRespondRequestBody(t, payload, cliApprovalTestUser, cliApprovalTestSession, peerCert)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("missing mTLS peer certificate is rejected", func(t *testing.T) {
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		req := approvalRespondRequest(`{"approval_id":"`+cliApprovalTestHash+`","approved":true,"cli_signature":"abcd"}`, cliApprovalTestUser)
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyCLISessionID, cliApprovalTestSession))
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrCLIL3SignatureRequired.Error())
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("browser context cannot use CLI approval", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		req := signedCLIApprovalRequest(t, cliApprovalTestHash, cert, "")
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyCLISessionID, ""))
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Zero(t, mcp.resumeCalls)
	})

	t.Run("expired transaction is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		mcp.suspended.ExpiresAt = time.Now().Add(-time.Minute)
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusNotFound, rr.Code)
		assert.Zero(t, mcp.resumeCalls)
	})

	resumeFailures := []struct {
		name   string
		cause  error
		status int
		body   error
	}{
		{"withdrawn dispatch reports its sentinel", constants.ErrTribunalCommandRevised, http.StatusForbidden, constants.ErrTribunalCommandRevised},
		{"failed review inference reports its sentinel", constants.ErrTribunalInferenceFailed, http.StatusForbidden, constants.ErrTribunalInferenceFailed},
		{"invalid review answer reports its sentinel", constants.ErrTribunalAnswerInvalid, http.StatusForbidden, constants.ErrTribunalAnswerInvalid},
		{"lost pending dispatch reports expiry", constants.ErrTransactionExpired, http.StatusNotFound, constants.ErrTransactionExpired},
		{"locked vault reports unavailability", constants.ErrLocalFirstEventsStoreVaultLocked, http.StatusServiceUnavailable, constants.ErrLocalFirstEventsStoreVaultLocked},
		{"unclassified fault is an internal error", constants.ErrGatewayNotReady, http.StatusInternalServerError, constants.ErrInternal},
	}
	for _, tc := range resumeFailures {
		t.Run(tc.name, func(t *testing.T) {
			cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
			mcp := newCLIApprovalMCP(cliApprovalTestUser)
			mcp.resumeErr = fmt.Errorf("dispatch: admitted: operator op-7 detail: %w", tc.cause)
			rr := httptest.NewRecorder()

			newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

			assert.Equal(t, tc.status, rr.Code)
			var body struct {
				Error string `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(t, tc.body.Error(), body.Error)
			assert.Equal(t, 1, mcp.resumeCalls)
		})
	}

	t.Run("receipt hash mismatch is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		mcp.receipt.TransactionHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})

	t.Run("receipt without valid L3 status is rejected", func(t *testing.T) {
		cert := newCLIApprovalTestCertificate(t, cliApprovalTestUser, cliApprovalTestSession)
		mcp := newCLIApprovalMCP(cliApprovalTestUser)
		mcp.receipt.L3Status = operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
		rr := httptest.NewRecorder()

		newApprovalRespondHandler(mcp, &approvalRespondStore{}).handleApprovalRespond(rr, signedCLIApprovalRequest(t, cliApprovalTestHash, cert, cliApprovalTestSession))

		assert.Equal(t, http.StatusInternalServerError, rr.Code)
	})
}

func approvalRespondRequestBody(t *testing.T, payload models.ApprovalRespondRequest, userID, sessionID string, cert tls.Certificate) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := approvalRespondRequest(string(body), userID)
	ctx := context.WithValue(req.Context(), constants.ContextKeyCLISessionID, sessionID)
	req = req.WithContext(ctx)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}}
	return req
}

func TestHandleApprovalRespond_ApprovedWithoutCLISignatureRequiresProof(t *testing.T) {
	mcp := newCLIApprovalMCP(cliApprovalTestUser)
	h := newApprovalRespondHandler(mcp, &approvalRespondStore{})
	req := approvalRespondRequest(`{"approval_id":"`+cliApprovalTestHash+`","approved":true}`, cliApprovalTestUser)
	req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyCLISessionID, cliApprovalTestSession))
	rr := httptest.NewRecorder()

	h.handleApprovalRespond(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrCLIL3SignatureRequired.Error())
	assert.Zero(t, mcp.resumeCalls)
}

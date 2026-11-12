// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	approvals "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/protocol"
)

const renewalTestTxHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type renewalFixture struct {
	infra     *TestInfrastructure
	c         *CLIRotationController
	userID    string
	sessionID string
	original  tls.Certificate
}

// newRenewalFixture persists an active CLI session for a new user, grounded in
// a certificate the test PKI signed for that session.
func newRenewalFixture(t *testing.T) *renewalFixture {
	t.Helper()
	infra := setupTestInfrastructure(t, false)
	c := newCLIRotationController(CLIRotationControllerDeps{
		Cfg:           infra.Cfg,
		Logger:        infra.Logger,
		PKI:           infra.PKI,
		CLISessionSvc: infra.CLISessionSvc,
		UserSvc:       infra.UserSvc,
		Responder:     infra.Responder,
	})
	user, err := infra.UserSvc.CreateUser(testutil.Context(t))
	require.NoError(t, err)

	sessionID := "cli-session-renewal-" + user.ID
	original := signRenewalTestCLICert(t, c, user.ID, sessionID)
	expires := time.Now().UTC().Add(time.Hour)
	b, err := json.Marshal(models.CLISession{
		ID:                sessionID,
		UserID:            user.ID,
		OperatorSessionID: "op-session-" + user.ID,
		CertFingerprint:   fingerprintOf(original),
		CertSerial:        original.Leaf.SerialNumber.String(),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         expires,
		AbsoluteExpiresAt: expires,
		IdleExpiresAt:     expires,
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       string(constants.HeartbeatTypeBootstrap),
	})
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(testutil.Context(t), marshaler.CollectionName(constants.CollectionCLISessions), sessionID, b))
	return &renewalFixture{infra: infra, c: c, userID: user.ID, sessionID: sessionID, original: original}
}

func signRenewalTestCLICert(t *testing.T, c *CLIRotationController, userID, sessionID string) tls.Certificate {
	t.Helper()
	csrPEM, key, _ := generateTestCSR(t, "ztd-cli-renewal")
	certPEM, _, err := c.pki.SignCSR(csrPEM, constants.LeafTypeCLI, "", "", userID, sessionID, "")
	require.NoError(t, err)
	return tlsCertFromPEM(t, certPEM, key)
}

func tlsCertFromPEM(t *testing.T, certPEM string, key *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{block.Bytes}, PrivateKey: key, Leaf: leaf}
}

func fingerprintOf(cert tls.Certificate) string {
	return calculateFingerprintFromPEM(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})))
}

// renew posts a renewal for the fixture's session authenticated by presented,
// as the auth middleware would admit it.
func (f *renewalFixture) renew(t *testing.T, presented tls.Certificate) (*httptest.ResponseRecorder, *ecdsa.PrivateKey) {
	t.Helper()
	csrPEM, key, _ := generateTestCSR(t, "ztd-cli-renewal")
	body, err := json.Marshal(models.CLIRotationRequest{CLICSRPEM: csrPEM})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIRenew, bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), constants.ContextKeyUserID, f.userID)
	ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, f.sessionID)
	req = req.WithContext(ctx)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{presented.Leaf}}
	rr := httptest.NewRecorder()
	f.c.handleRenew(rr, req)
	return rr, key
}

func (f *renewalFixture) renewOK(t *testing.T, presented tls.Certificate) tls.Certificate {
	t.Helper()
	rr, key := f.renew(t, presented)
	resp := parseRotationResponse(t, rr)
	return tlsCertFromPEM(t, resp.CLICert, key)
}

func (f *renewalFixture) session(t *testing.T) *models.CLISession {
	t.Helper()
	session, err := f.infra.CLISessionSvc.loadCLISession(testutil.Context(t), f.sessionID)
	require.NoError(t, err)
	return session
}

// verifyApproval signs a CLI approval with signer and returns the Gateway L3
// notary's verdict for the fixture's session.
func (f *renewalFixture) verifyApproval(t *testing.T, signer tls.Certificate) (bool, error) {
	t.Helper()
	verifier := NewCLISessionVerifier(f.infra.DocStore, f.infra.PKI, f.infra.Logger, f.infra.UserSvc, f.infra.CLISessionSvc)
	notary := governance.NewGatewayL3Notary(verifier, nil, f.infra.Logger)
	proof, err := approvals.SignCLIApproval(renewalTestTxHash, &signer)
	require.NoError(t, err)
	return notary.VerifyL3Proof(testutil.Context(t), f.userID, renewalTestTxHash, f.sessionID, proof)
}

// requireApprovalAccepted asserts that signer's approval verifies.
func (f *renewalFixture) requireApprovalAccepted(t *testing.T, signer tls.Certificate, msg string) {
	t.Helper()
	ok, err := f.verifyApproval(t, signer)
	require.NoError(t, err, msg)
	require.True(t, ok, msg)
}

func TestCLIRenewal_RenewsInPlaceKeepingTheSession(t *testing.T) {
	f := newRenewalFixture(t)
	before := f.session(t)

	rr, key := f.renew(t, f.original)
	resp := parseRotationResponse(t, rr)
	assert.Equal(t, f.sessionID, resp.CLISessionID, "renewal keeps the CLI session")
	assert.Equal(t, f.userID, resp.UserID)
	assert.NotEmpty(t, resp.HubTrustBundle)

	renewed := tlsCertFromPEM(t, resp.CLICert, key)
	sessionID, ok := protocol.NewWorkloadIdentity().ExtractCLISessionID(renewed.Leaf.URIs[0].String())
	require.True(t, ok)
	assert.Equal(t, f.sessionID, sessionID, "the renewed certificate names the same session")
	assert.True(t, key.PublicKey.Equal(renewed.Leaf.PublicKey))

	after := f.session(t)
	assert.True(t, after.IsActive)
	assert.Equal(t, fingerprintOf(renewed), after.CertFingerprint)
	assert.Equal(t, renewed.Leaf.SerialNumber.String(), after.CertSerial)
	assert.Equal(t, before.CertFingerprint, after.PreviousCertFingerprint)
	assert.Equal(t, before.CertSerial, after.PreviousCertSerial)
	assert.Equal(t, before.OperatorSessionID, after.OperatorSessionID, "the operator binding is untouched")
	assert.True(t, after.ExpiresAt.After(before.ExpiresAt.Add(24*time.Hour)), "renewal extends the session TTL")

	revoked, err := f.infra.PKI.IsRevoked(testutil.Context(t), before.CertSerial)
	require.NoError(t, err)
	assert.False(t, revoked, "the replaced certificate stays valid for a process still holding it")
}

func TestCLIRenewal_OnlyTheCurrentCertificateRenews(t *testing.T) {
	f := newRenewalFixture(t)
	f.renewOK(t, f.original)

	rr, _ := f.renew(t, f.original)
	assert.Equal(t, http.StatusPreconditionFailed, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrCLIRenewalStaleCertificate.Error())
}

func TestCLIRenewal_RefusesAnInactiveSession(t *testing.T) {
	f := newRenewalFixture(t)
	require.NoError(t, f.infra.CLISessionSvc.DeactivateCLISession(testutil.Context(t), f.sessionID))

	rr, _ := f.renew(t, f.original)
	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionAlreadyDeactivated.Error())
}

func TestCLIRenewal_ApprovalsVerifyWithTheCurrentAndReplacedCertificates(t *testing.T) {
	f := newRenewalFixture(t)
	renewed := f.renewOK(t, f.original)
	stranger := newCLIApprovalTestCertificate(t, f.userID, f.sessionID)

	f.requireApprovalAccepted(t, renewed, "the renewed certificate approves")
	f.requireApprovalAccepted(t, f.original, "the replaced certificate still approves until it expires")
	ok, err := f.verifyApproval(t, stranger)
	require.ErrorIs(t, err, constants.ErrCLIL3FingerprintMismatch, "a certificate the session never held does not approve")
	assert.False(t, ok)

	// A second renewal retires the first certificate from the session.
	f.renewOK(t, renewed)
	ok, err = f.verifyApproval(t, f.original)
	require.ErrorIs(t, err, constants.ErrCLIL3FingerprintMismatch)
	assert.False(t, ok)
}

func TestCLIRenewal_LogoutRevokesTheCurrentAndReplacedCertificates(t *testing.T) {
	f := newRenewalFixture(t)
	previous := f.renewOK(t, f.original)
	renewed := f.renewOK(t, previous)

	_, err := newLogoutService(f.infra, f.infra.PKI).Logout(testutil.Context(t), f.userID, f.sessionID, renewed.Leaf.SerialNumber.String(), constants.LogoutScopeCLI)
	require.NoError(t, err)

	for _, serial := range []string{f.original.Leaf.SerialNumber.String(), previous.Leaf.SerialNumber.String(), renewed.Leaf.SerialNumber.String()} {
		revoked, err := f.infra.PKI.IsRevoked(testutil.Context(t), serial)
		require.NoError(t, err)
		assert.True(t, revoked, "serial %s", serial)
	}
}

func TestCLIRenewal_RotationRevokesTheCurrentAndReplacedCertificates(t *testing.T) {
	f := newRenewalFixture(t)
	previous := f.renewOK(t, f.original)
	renewed := f.renewOK(t, previous)

	csrPEM, _, _ := generateTestCSR(t, "rotation-after-renewal")
	rr := httptest.NewRecorder()
	f.c.handleRotate(rr, rotationRequestWithContext(t, f.userID, f.sessionID, csrPEM))
	parseRotationResponse(t, rr)

	for _, serial := range []string{f.original.Leaf.SerialNumber.String(), previous.Leaf.SerialNumber.String(), renewed.Leaf.SerialNumber.String()} {
		revoked, err := f.infra.PKI.IsRevoked(testutil.Context(t), serial)
		require.NoError(t, err)
		assert.True(t, revoked, "serial %s", serial)
	}
}

func TestCLIRenewal_ConcurrentRequestsHaveOneWinner(t *testing.T) {
	f := newRenewalFixture(t)
	var requests [2]*http.Request
	var responses [2]*httptest.ResponseRecorder
	for i := range requests {
		csr, _, _ := generateTestCSR(t, "concurrent-cli-renewal")
		body, err := json.Marshal(models.CLIRotationRequest{CLICSRPEM: csr})
		require.NoError(t, err)
		ctx := context.WithValue(testutil.Context(t), constants.ContextKeyUserID, f.userID)
		ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, f.sessionID)
		requests[i] = httptest.NewRequestWithContext(ctx, http.MethodPost, constants.APIPaths.AuthCLIRenew, bytes.NewReader(body))
		requests[i].TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{f.original.Leaf}}
		responses[i] = httptest.NewRecorder()
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range requests {
		workers.Go(func() { <-start; f.c.handleRenew(responses[i], requests[i]) })
	}
	close(start)
	workers.Wait()
	assert.ElementsMatch(t, []int{http.StatusCreated, http.StatusPreconditionFailed}, []int{responses[0].Code, responses[1].Code})
	assert.Len(t, f.session(t).CertificateSerials, 2)
}

func TestCLIRenewal_AtomicConditionsRejectAnInactiveSessionWithMatchingCertificate(t *testing.T) {
	f := newRenewalFixture(t)
	require.NoError(t, f.infra.CLISessionSvc.DeactivateCLISession(testutil.Context(t), f.sessionID))
	fingerprint, err := json.Marshal(fingerprintOf(f.original))
	require.NoError(t, err)
	applied, err := f.infra.DocStore.DocConditionalUpdateAll(testutil.Context(t), string(constants.CollectionCLISessions), f.sessionID,
		json.RawMessage(`{"cert_serial":"must-not-be-written"}`), []DocumentUpdateCondition{
			{Field: "cert_fingerprint", Value: fingerprint},
			{Field: "is_active", Value: json.RawMessage("true")},
		})
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Equal(t, f.original.Leaf.SerialNumber.String(), f.session(t).CertSerial)
}

func TestCLIRenewal_ConcurrentLogoutRevokesEveryRecordedCertificate(t *testing.T) {
	for range 5 {
		f := newRenewalFixture(t)
		csr, _, _ := generateTestCSR(t, "cli-renewal-logout-race")
		body, err := json.Marshal(models.CLIRotationRequest{CLICSRPEM: csr})
		require.NoError(t, err)
		ctx := context.WithValue(testutil.Context(t), constants.ContextKeyUserID, f.userID)
		ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, f.sessionID)
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, constants.APIPaths.AuthCLIRenew, bytes.NewReader(body))
		request.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{f.original.Leaf}}
		response := httptest.NewRecorder()
		start := make(chan struct{})
		var workers sync.WaitGroup
		var logoutErr error
		workers.Go(func() { <-start; f.c.handleRenew(response, request) })
		workers.Go(func() {
			<-start
			_, logoutErr = newLogoutService(f.infra, f.infra.PKI).Logout(testutil.Context(t), f.userID, f.sessionID, f.original.Leaf.SerialNumber.String(), constants.LogoutScopeCLI)
		})
		close(start)
		workers.Wait()
		require.NoError(t, logoutErr)
		session := f.session(t)
		assert.False(t, session.IsActive)
		serials := cliSessionCertificateSerials(session)
		if response.Code == http.StatusCreated {
			// The renewal won the race, so logout must revoke what it issued.
			serials = append(serials, tlsCertFromPEM(t, parseRotationResponse(t, response).CLICert, nil).Leaf.SerialNumber.String())
		} else {
			assert.Equal(t, http.StatusConflict, response.Code, "logout won the race: %s", response.Body.String())
		}
		for _, serial := range serials {
			revoked, err := f.infra.PKI.IsRevoked(testutil.Context(t), serial)
			require.NoError(t, err)
			assert.True(t, revoked, "serial %s", serial)
		}
	}
}

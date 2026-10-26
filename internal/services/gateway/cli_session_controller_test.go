// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/protocol"
)

// selfSignedCertForSPIFFE builds a self-signed certificate carrying the
// given SPIFFE URI SAN so tests can drive the mTLS auth paths through
// ServeHTTP.
func selfSignedCertForSPIFFE(t *testing.T, uri *url.URL) *x509.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-cli"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

// seedActiveCLISession persists an active CLI session for the given user,
// optionally bound to an operator session.
func seedActiveCLISession(t *testing.T, infra *TestInfrastructure, userID, cliSessionID, operatorSessionID string) {
	t.Helper()
	cliDoc := &models.CLISession{
		ID:                cliSessionID,
		UserID:            userID,
		OperatorSessionID: operatorSessionID,
		IsActive:          true,
		SessionType:       string(constants.SessionTypeCLI),
		ExpiresAt:         time.Now().Add(1 * time.Hour),
	}
	cliBytes, err := json.Marshal(cliDoc)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliBytes))
}

// TestHandleCLISessionInfo_ReturnsPersistedBinding drives GET
// /api/v1/auth/cli/session through the full public router: the auth
// middleware validates the CLI cert against the persisted session, stamps
// the persisted operator binding, and the handler reports it verbatim.
func TestHandleCLISessionInfo_ReturnsPersistedBinding(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	// Claim the embedded operator for the user so its session is the
	// persisted binding the CLI session carries.
	operatorID, operatorSessionID, err := infra.Embedded.ClaimEmbeddedOperator(t.Context(), user.ID)
	require.NoError(t, err)

	cliSessionID := "cli-session-info-bound"
	seedActiveCLISession(t, infra, user.ID, cliSessionID, operatorSessionID)

	wid := protocol.NewWorkloadIdentity()
	cliURI, err := wid.CLISPIFFEURL(user.ID, cliSessionID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedCertForSPIFFE(t, cliURI)}}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var resp models.CLISessionInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, cliSessionID, resp.CLISessionID)
	assert.Equal(t, user.ID, resp.UserID)
	assert.Equal(t, operatorSessionID, resp.OperatorSessionID)
	assert.Equal(t, operatorID, resp.OperatorID)
}

// TestHandleCLISessionInfo_UnboundSessionReturnsEmptyOperatorFields verifies
// that a CLI session with no persisted operator binding reports empty
// operator fields — the endpoint reports state verbatim and the caller
// decides whether the missing binding is actionable.
func TestHandleCLISessionInfo_UnboundSessionReturnsEmptyOperatorFields(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	cliSessionID := "cli-session-info-unbound"
	seedActiveCLISession(t, infra, user.ID, cliSessionID, "")

	wid := protocol.NewWorkloadIdentity()
	cliURI, err := wid.CLISPIFFEURL(user.ID, cliSessionID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedCertForSPIFFE(t, cliURI)}}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var resp models.CLISessionInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, cliSessionID, resp.CLISessionID)
	assert.Equal(t, user.ID, resp.UserID)
	assert.Empty(t, resp.OperatorSessionID)
	assert.Empty(t, resp.OperatorID)
	assert.False(t, resp.PasskeyRegistered)
}

// TestHandleCLISessionInfo_ReportsRegisteredPasskey verifies passkey_registered
// reflects the session user's persisted passkey credentials.
func TestHandleCLISessionInfo_ReportsRegisteredPasskey(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)
	require.NoError(t, infra.UserSvc.UpdatePasskeyCredentials(t.Context(), user.ID, []models.PasskeyCredential{
		{ID: []byte("credential-1"), PublicKey: []byte("public-key")},
	}))

	cliSessionID := "cli-session-info-passkey"
	seedActiveCLISession(t, infra, user.ID, cliSessionID, "")

	wid := protocol.NewWorkloadIdentity()
	cliURI, err := wid.CLISPIFFEURL(user.ID, cliSessionID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedCertForSPIFFE(t, cliURI)}}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "body: %s", rr.Body.String())
	var resp models.CLISessionInfoResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.PasskeyRegistered)
}

// TestHandleCLISessionInfo_NonCLIIdentityIsRejected verifies the endpoint
// fails closed for callers that are not CLI sessions. The workload route
// allowlist rejects an Operator certificate before the CLI-only handler, while
// a request with no client certificate is rejected by mTLS authentication.
func TestHandleCLISessionInfo_NonCLIIdentityIsRejected(t *testing.T) {
	h, _, infra := setupTestHTTPHandler(t)

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	operatorID, operatorSessionID, err := infra.Embedded.ClaimEmbeddedOperator(t.Context(), user.ID)
	require.NoError(t, err)

	t.Run("operator certificate without CLI session", func(t *testing.T) {
		wid := protocol.NewWorkloadIdentity()
		opURI, err := wid.OperatorSPIFFEURL(user.ID, operatorID, operatorSessionID)
		require.NoError(t, err)

		req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{selfSignedCertForSPIFFE(t, opURI)}}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusForbidden, rr.Code)
		assert.Contains(t, rr.Body.String(), constants.ErrPrivilegedEndpointAccess.Error())
	})

	t.Run("no client certificate", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}

// TestHandleCLISessionInfo_MissingSessionContextReturns401 verifies the
// handler fails closed when the auth middleware did not stamp the session
// context (a misconfigured route must never report an identity).
func TestHandleCLISessionInfo_MissingSessionContextReturns401(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)

	t.Run("no context", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
		rr := httptest.NewRecorder()
		h.cliSessionController.handleSessionInfo(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("user context without CLI session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLISession, nil)
		req = req.WithContext(context.WithValue(req.Context(), constants.ContextKeyUserID, "user-1"))
		rr := httptest.NewRecorder()
		h.cliSessionController.handleSessionInfo(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})
}

// TestHandleCLISessionInfo_MethodNotAllowed verifies non-GET requests are
// rejected.
func TestHandleCLISessionInfo_MethodNotAllowed(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLISession, nil)
	rr := httptest.NewRecorder()
	h.cliSessionController.handleSessionInfo(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

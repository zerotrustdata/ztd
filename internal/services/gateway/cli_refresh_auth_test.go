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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/protocol"
)

// setupRefreshAuthTestInfra builds an AuthService backed by a real doc store
// with an active user and (optionally) a CLI session, returning the auth
// service, the user ID, and the CLI session ID. The middleware is constructed
// over a success-recording handler so tests can assert admission vs rejection.
func setupRefreshAuthTestInfra(t *testing.T, sessionID string, expired bool, certFingerprint string) (auth *AuthService, middleware http.Handler, userID, cliSessionID string) {
	t.Helper()
	infra := setupTestInfrastructure(t, false)
	auth = infra.Auth

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)
	userID = user.ID

	if sessionID != "" {
		cliSessionID = sessionID
		now := time.Now().UTC()
		operatorDoc := &operatorv1.OperatorDocument{
			Id:                "refresh-auth-operator",
			UserId:            userID,
			Status:            string(constants.OperatorStatusActive),
			OperatorSessionId: "op-refresh-auth",
			OperatorType:      string(constants.OperatorTypeRemote),
			CreatedAt:         timestamppb.New(now),
			UpdatedAt:         timestamppb.New(now),
		}
		operatorBytes, err := models.MarshalOperatorDocument(operatorDoc)
		require.NoError(t, err)
		require.NoError(t, infra.DocStore.DocSet(t.Context(),
			marshaler.CollectionName(constants.CollectionOperators), operatorDoc.Id, operatorBytes,
		))

		expiresAt := now.Add(1 * time.Hour)
		if expired {
			expiresAt = now.Add(-1 * time.Hour)
		}
		doc := models.CLISession{
			ID:                cliSessionID,
			UserID:            userID,
			OperatorSessionID: "op-refresh-auth",
			SystemFingerprint: "sys-fp",
			CertFingerprint:   certFingerprint,
			CertSerial:        "serial",
			CreatedAt:         time.Now().Add(-2 * time.Hour),
			ExpiresAt:         expiresAt,
			AbsoluteExpiresAt: expiresAt,
			IdleExpiresAt:     expiresAt,
			SessionType:       string(constants.SessionTypeCLI),
			IsActive:          true,
			LoginMethod:       "mTLS",
		}
		b, err := json.Marshal(doc)
		require.NoError(t, err)
		require.NoError(t, infra.DocStore.DocSet(t.Context(),
			marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, b,
		))
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the stamped user ID so tests can assert the cert-derived
		// identity reached the handler.
		uid, _ := r.Context().Value(constants.ContextKeyUserID).(string)
		w.Header().Set("X-Stamped-User-ID", uid)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("admitted"))
	})
	middleware = auth.Middleware(handler)
	return auth, middleware, userID, cliSessionID
}

// cliRefreshMTLSRequest builds a request to the refresh endpoint (or a
// non-refresh endpoint when path is overridden) with the given CLI session
// ID header and a cert whose URI SAN is the CLI SPIFFE ID for the given
// user/session. This mirrors what handleMTLSAuth sees from a real mTLS
// handshake.
func cliRefreshMTLSRequest(t *testing.T, path, cliSessionID, userID string) *http.Request {
	t.Helper()
	wid := protocol.NewWorkloadIdentity()
	cliURI, err := wid.CLISPIFFEURL(userID, cliSessionID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{URIs: []*url.URL{cliURI}},
		},
	}
	return req
}

// TestHandleCLIRefreshAuth_CertURISANMatchesSessionID_Admitted verifies the
// primary recovery path through the unified auth middleware: the CLI session
// is expired, the request targets the refresh endpoint, the cert URI SAN
// session ID matches the header-provided session ID, and the user is active.
// The middleware admits the request to the refresh controller.
func TestHandleCLIRefreshAuth_CertURISANMatchesSessionID_Admitted(t *testing.T) {
	_, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-expired", true, "cert-fp")

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, cliSessionID, userID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "refresh endpoint should admit an expired session with a valid cert, body: %s", rr.Body.String())
	assert.Equal(t, "admitted", rr.Body.String())
	assert.Equal(t, userID, rr.Header().Get("X-Stamped-User-ID"), "user ID must be stamped from the cert URI SAN")
}

// TestHandleCLIRefreshAuth_CertURISANDoesNotMatchSessionID_Rejected verifies
// that a cert whose URI SAN session ID does not match the header-provided
// session ID is rejected with 403 ErrMTLSIdentityMismatch. This is the
// fail-closed guard against a cert trying to refresh a different session.
func TestHandleCLIRefreshAuth_CertURISANDoesNotMatchSessionID_Rejected(t *testing.T) {
	_, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-mismatch", true, "cert-fp")

	// Build a cert whose URI SAN session ID is different from the header.
	wid := protocol.NewWorkloadIdentity()
	wrongURI, err := wid.CLISPIFFEURL(userID, "different-session-id")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIRefresh, nil)
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{URIs: []*url.URL{wrongURI}},
		},
	}
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrMTLSIdentityMismatch.Error())
}

// TestHandleCLIRefreshAuth_UserDisabled_Rejected verifies that a disabled
// user is rejected even on the refresh endpoint. An expired session does
// not bypass user-disabled checks — the cert proves identity, but the user
// must still be active to receive a new session.
func TestHandleCLIRefreshAuth_UserDisabled_Rejected(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	auth := infra.Auth

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	cliSessionID := "refresh-auth-disabled"
	expiresAt := time.Now().Add(-1 * time.Hour)
	doc := models.CLISession{
		ID:                cliSessionID,
		UserID:            user.ID,
		OperatorSessionID: "op-disabled",
		CreatedAt:         time.Now().Add(-2 * time.Hour),
		ExpiresAt:         expiresAt,
		AbsoluteExpiresAt: expiresAt,
		IdleExpiresAt:     expiresAt,
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       "mTLS",
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, b,
	))

	// Disable the user after the session was created.
	require.NoError(t, infra.UserSvc.Disable(t.Context(), user.ID, "test", "actor", "op"))

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	middleware := auth.Middleware(handler)

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, cliSessionID, user.ID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.NotEqual(t, http.StatusOK, rr.Code, "disabled user must not be admitted to the refresh endpoint")
}

// TestHandleCLIAuth_ExpiredSessionOnNonRefreshEndpoint_FailClosed verifies
// that an expired CLI session on a non-refresh endpoint is rejected with
// 401 ErrCLISessionExpired. Only the refresh endpoint bypasses the expired-
// session check; all other endpoints fail closed.
func TestHandleCLIAuth_ExpiredSessionOnNonRefreshEndpoint_FailClosed(t *testing.T) {
	_, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-nonrefresh-expired", true, "cert-fp")

	// Target a non-refresh mTLS endpoint (e.g., the event receipts list).
	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuditEventReceipts, cliSessionID, userID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionExpired.Error())
}

// TestHandleCLIAuth_CertNamingOtherSessionRejected verifies that the fingerprint
// fallback is gone: a certificate whose URI SAN names one CLI session cannot
// authenticate another, even when the other session's stored fingerprint
// matches the certificate.
func TestHandleCLIAuth_CertNamingOtherSessionRejected(t *testing.T) {
	certRaw := []byte("refreshed-session-certificate")
	certHash := sha256.Sum256(certRaw)
	certFingerprint := hex.EncodeToString(certHash[:])
	tests := []struct {
		name              string
		storedFingerprint string
	}{
		{name: "matching fingerprint", storedFingerprint: certFingerprint},
		{name: "different fingerprint", storedFingerprint: "different-fingerprint"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refreshed-session-id", false, tt.storedFingerprint)
			wid := protocol.NewWorkloadIdentity()
			cliURI, err := wid.CLISPIFFEURL(userID, "initial-session-id")
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuditEventReceipts, nil)
			req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
				Raw:  certRaw,
				URIs: []*url.URL{cliURI},
			}}}
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			assert.Contains(t, rr.Body.String(), constants.ErrMTLSIdentityMismatch.Error())
		})
	}
}

// TestHandleCLIRefreshAuth_CertNamingOtherSessionRejected verifies the same
// rule on the refresh route: refresh must name the session the certificate
// names, so a certificate cannot refresh a different session because that
// session's fingerprint matches.
func TestHandleCLIRefreshAuth_CertNamingOtherSessionRejected(t *testing.T) {
	certRaw := []byte("refresh-auth-original-certificate")
	certHash := sha256.Sum256(certRaw)
	certFingerprint := hex.EncodeToString(certHash[:])
	tests := []struct {
		name              string
		storedFingerprint string
	}{
		{name: "matching fingerprint", storedFingerprint: certFingerprint},
		{name: "different fingerprint", storedFingerprint: "different-fingerprint"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-other-session", false, tt.storedFingerprint)
			wid := protocol.NewWorkloadIdentity()
			cliURI, err := wid.CLISPIFFEURL(userID, "refresh-auth-original-session")
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIRefresh, nil)
			req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{
				Raw:  certRaw,
				URIs: []*url.URL{cliURI},
			}}}
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusForbidden, rr.Code, rr.Body.String())
			assert.Contains(t, rr.Body.String(), constants.ErrMTLSIdentityMismatch.Error())
		})
	}
}

// TestHandleCLIAuth_MissingSessionOnNonRefreshEndpoint_FailClosed verifies
// that a missing CLI session (no persisted document for the header-provided
// session ID) on a non-refresh endpoint is rejected with 401
// ErrCLISessionInvalid. Only the refresh endpoint bypasses the missing-
// session check; all other endpoints fail closed.
func TestHandleCLIAuth_MissingSessionOnNonRefreshEndpoint_FailClosed(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	auth := infra.Auth

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	middleware := auth.Middleware(handler)

	// No session persisted — use a random session ID that doesn't exist.
	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuditEventReceipts, "nonexistent-session-id", user.ID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionInvalid.Error())
}

// TestHandleCLIRefreshAuth_MissingSessionOnRefreshEndpoint_Admitted verifies
// that the middleware admits a refresh whose cert names a session the store
// lacks: the cert is valid and the user is active. Admission does not depend on
// session state. The refresh handler then refuses the missing session with 401
// (TestCLIRefresh_UnknownSessionSAN_Unauthorized), because there is nothing to
// extend in place.
func TestHandleCLIRefreshAuth_MissingSessionOnRefreshEndpoint_Admitted(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	auth := infra.Auth

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(constants.ContextKeyUserID).(string)
		w.Header().Set("X-Stamped-User-ID", uid)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("admitted"))
	})
	middleware := auth.Middleware(handler)

	// No session persisted — simulate a volume reset.
	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, "missing-session-id", user.ID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "refresh endpoint should admit a missing session with a valid cert, body: %s", rr.Body.String())
	assert.Equal(t, "admitted", rr.Body.String())
	assert.Equal(t, user.ID, rr.Header().Get("X-Stamped-User-ID"))
}

func TestHandleCLIRefreshAuth_StaleOperatorBinding_Admitted(t *testing.T) {
	auth, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-stale-operator", false, "cert-fp")
	operatorDoc, err := auth.db.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), "refresh-auth-operator")
	require.NoError(t, err)
	require.NotNil(t, operatorDoc)
	operator, err := models.OperatorDocumentFromStore(operatorDoc)
	require.NoError(t, err)
	operator.Status = string(constants.OperatorStatusTerminated)
	data, err := models.MarshalOperatorDocument(operator)
	require.NoError(t, err)
	require.NoError(t, auth.db.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), "refresh-auth-operator", data))

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, cliSessionID, userID)
	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "refresh endpoint should admit a stale operator binding with a valid cert, body: %s", rr.Body.String())
	assert.Equal(t, "admitted", rr.Body.String())
}

func TestHandleCLIRefreshAuth_ServerInvalidSessionStates_Admitted(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*models.CLISession)
	}{
		{name: "deactivated session", mutate: func(session *models.CLISession) { session.IsActive = false }},
		{name: "absolute expiry elapsed", mutate: func(session *models.CLISession) { session.AbsoluteExpiresAt = time.Now().Add(-time.Hour) }},
		{name: "idle expiry elapsed", mutate: func(session *models.CLISession) { session.IdleExpiresAt = time.Now().Add(-time.Hour) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "refresh-auth-server-invalid", false, "cert-fp")
			doc, err := auth.db.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID)
			require.NoError(t, err)
			require.NotNil(t, doc)
			var session models.CLISession
			require.NoError(t, doc.DecodeData(&session))
			tt.mutate(&session)
			data, err := json.Marshal(session)
			require.NoError(t, err)
			require.NoError(t, auth.db.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, data))

			req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, cliSessionID, userID)
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code, "refresh endpoint should admit a server-invalid session with a valid cert, body: %s", rr.Body.String())
			assert.Equal(t, "admitted", rr.Body.String())
		})
	}
}

func TestHandleCLIAuth_ServerInvalidSessionStatesOnNonRefreshEndpoint_FailClosed(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*models.CLISession)
		expectedError error
	}{
		{name: "deactivated session", mutate: func(session *models.CLISession) { session.IsActive = false }, expectedError: constants.ErrCLISessionInvalid},
		{name: "absolute expiry elapsed", mutate: func(session *models.CLISession) { session.AbsoluteExpiresAt = time.Now().Add(-time.Hour) }, expectedError: constants.ErrCLISessionExpired},
		{name: "idle expiry elapsed", mutate: func(session *models.CLISession) { session.IdleExpiresAt = time.Now().Add(-time.Hour) }, expectedError: constants.ErrCLISessionExpired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			auth, middleware, userID, cliSessionID := setupRefreshAuthTestInfra(t, "nonrefresh-auth-server-invalid", false, "cert-fp")
			doc, err := auth.db.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID)
			require.NoError(t, err)
			require.NotNil(t, doc)
			var session models.CLISession
			require.NoError(t, doc.DecodeData(&session))
			tt.mutate(&session)
			data, err := json.Marshal(session)
			require.NoError(t, err)
			require.NoError(t, auth.db.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, data))

			req := cliRefreshMTLSRequest(t, constants.APIPaths.AuditEventReceipts, cliSessionID, userID)
			rr := httptest.NewRecorder()
			middleware.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
			assert.Contains(t, rr.Body.String(), tt.expectedError.Error())
		})
	}
}

// cliRefreshRouteHandler mounts the refresh controller behind the same unified
// auth middleware the Gateway uses, so a request exercises the certificate path
// end to end: admission by the middleware, then the refresh handler.
func cliRefreshRouteHandler(t *testing.T, infra *TestInfrastructure) http.Handler {
	t.Helper()
	c := newCLIRefreshController(CLIRefreshControllerDeps{
		Cfg:                infra.Cfg,
		Logger:             infra.Logger,
		CLISessionSvc:      infra.CLISessionSvc,
		OperatorSessionSvc: infra.OperatorSessionSvc,
		Reg:                infra.Reg,
		Auth:               infra.Auth,
		UserSvc:            infra.UserSvc,
		Responder:          infra.Responder,
	})
	return infra.Auth.Middleware(http.HandlerFunc(c.handleRefresh))
}

// TestCLIRefresh_UnknownSessionSAN_Unauthorized verifies that the fallback is
// gone on the refresh route: a certificate whose URI SAN names a session the
// gateway does not have is refused with 401, and no session is minted for it.
func TestCLIRefresh_UnknownSessionSAN_Unauthorized(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, "refresh-route-unknown", user.ID)
	rr := httptest.NewRecorder()
	cliRefreshRouteHandler(t, infra).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionInvalid.Error())
	sessions, err := infra.CLISessionSvc.ListUserCLISessions(t.Context(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions, "refresh must not mint a session under a certificate's unknown session name")
}

// TestCLIRefresh_DeactivatedSession_ConflictThroughMiddleware verifies that a
// logged-out session cannot be refreshed back to life on the real route: the
// request is refused with 409 and the session stays deactivated.
func TestCLIRefresh_DeactivatedSession_ConflictThroughMiddleware(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	sessionID := "refresh-route-deactivated"
	require.NoError(t, infra.CLISessionSvc.PersistCLISession(t.Context(), sessionID, "op-route-deact", user.ID, "sys-fp", "cert-fp", "serial", "mTLS"))
	require.NoError(t, infra.CLISessionSvc.DeactivateCLISession(t.Context(), sessionID))
	before := loadStoredCLISession(t, infra.CLISessionSvc, sessionID)

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, sessionID, user.ID)
	rr := httptest.NewRecorder()
	cliRefreshRouteHandler(t, infra).ServeHTTP(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	stored := loadStoredCLISession(t, infra.CLISessionSvc, sessionID)
	assert.False(t, stored.IsActive, "a logged-out session cannot be refreshed back to life")
	assert.Equal(t, before.ExpiresAt.Unix(), stored.ExpiresAt.Unix(), "a refused refresh must not extend the expiry")
}

// TestCLIRefresh_ExpiredSession_ExtendedThroughMiddleware verifies the recovery
// path on the real route: an expired session the certificate names is extended
// in place, keeping its ID, and the caller gets a usable session back.
func TestCLIRefresh_ExpiredSession_ExtendedThroughMiddleware(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	operatorSessionID := "op-route-expired"
	require.NoError(t, infra.OperatorSessionSvc.PersistOperatorSession(t.Context(), operatorSessionID, user.ID, "org-route", "op-route-id", "mTLS"))
	sessionID := "refresh-route-expired"
	require.NoError(t, infra.CLISessionSvc.PersistCLISession(t.Context(), sessionID, operatorSessionID, user.ID, "sys-fp", "cert-fp", "serial", "mTLS"))
	expireStoredCLISession(t, infra.CLISessionSvc, sessionID)

	req := cliRefreshMTLSRequest(t, constants.APIPaths.AuthCLIRefresh, sessionID, user.ID)
	rr := httptest.NewRecorder()
	cliRefreshRouteHandler(t, infra).ServeHTTP(rr, req)

	require.Equal(t, http.StatusCreated, rr.Code, rr.Body.String())
	var resp models.CLIRefreshResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, sessionID, resp.CLISessionID, "refresh keeps the session ID the certificate names")
	assert.Equal(t, operatorSessionID, resp.OperatorSessionID)

	stored := loadStoredCLISession(t, infra.CLISessionSvc, sessionID)
	assert.True(t, stored.IsActive)
	assert.True(t, stored.ExpiresAt.After(time.Now()), "the refreshed session must have a future expiry")
	sessions, err := infra.CLISessionSvc.ListUserCLISessions(t.Context(), user.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1, "refresh must not mint a replacement session")
}

// operatorCertRequest builds a request authenticated by an Operator certificate
// whose URI SAN names operatorSessionID. The X-ZTD-CLI-Session-ID header carries
// cliSessionID, which a client fully controls.
func operatorCertRequest(t *testing.T, path, orgID, operatorID, operatorSessionID, cliSessionID string, body []byte) *http.Request {
	t.Helper()
	wid := protocol.NewWorkloadIdentity()
	opURI, err := wid.OperatorSPIFFEURL(orgID, operatorID, operatorSessionID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(constants.HeaderCLISessionID, cliSessionID)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{
			{URIs: []*url.URL{opURI}},
		},
	}
	return req
}

// persistOperatorDocForCert stores an active Operator owned by userID whose
// operator session is operatorSessionID: the shape an Operator certificate
// authenticates against.
func persistOperatorDocForCert(t *testing.T, infra *TestInfrastructure, userID, orgID, operatorID, operatorSessionID string) {
	t.Helper()
	now := time.Now().UTC()
	doc, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                operatorID,
		UserId:            userID,
		OrganizationId:    orgID,
		OperatorSessionId: operatorSessionID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NoError(t, infra.DocStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), operatorID, doc))
}

// TestOperatorCert_CLISessionHeaderNotStamped verifies that a request
// authenticated by an Operator certificate never takes a CLI session identity
// from X-ZTD-CLI-Session-ID. The middleware stamps no CLI session, and the
// CLI-only bind route refuses the request, so a foreign or inactive CLI session
// named in the header keeps its binding.
func TestOperatorCert_CLISessionHeaderNotStamped(t *testing.T) {
	infra := setupTestInfrastructure(t, false)
	owner, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)
	foreignOwner, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)

	const (
		orgID             = "org-op-cert"
		operatorID        = "op-cert-operator"
		operatorSessionID = "op-cert-session"
		targetOperatorID  = "op-cert-target-operator"
		targetSessionID   = "op-cert-bind-target"
	)
	persistOperatorDocForCert(t, infra, owner.ID, orgID, operatorID, operatorSessionID)
	persistOperatorDocForCert(t, infra, owner.ID, orgID, targetOperatorID, targetSessionID)

	foreignSessionID := "op-cert-foreign-cli"
	require.NoError(t, infra.CLISessionSvc.PersistCLISession(t.Context(), foreignSessionID, "op-foreign", foreignOwner.ID, "sys-fp", "cert-fp", "serial", "mTLS"))
	foreignBefore := loadStoredCLISession(t, infra.CLISessionSvc, foreignSessionID)

	inactiveSessionID := "op-cert-own-inactive-cli"
	require.NoError(t, infra.CLISessionSvc.PersistCLISession(t.Context(), inactiveSessionID, "op-own", owner.ID, "sys-fp", "cert-fp", "serial", "mTLS"))
	require.NoError(t, infra.CLISessionSvc.DeactivateCLISession(t.Context(), inactiveSessionID))

	// The middleware must not stamp the header into the request context.
	probe := infra.Auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stamped, _ := r.Context().Value(constants.ContextKeyCLISessionID).(string)
		w.Header().Set("X-Stamped-CLI-Session-ID", stamped)
		w.WriteHeader(http.StatusOK)
	}))
	probeRR := httptest.NewRecorder()
	probe.ServeHTTP(probeRR, operatorCertRequest(t, constants.APIPaths.AuthCLISession, orgID, operatorID, operatorSessionID, foreignSessionID, nil))
	require.Equal(t, http.StatusOK, probeRR.Code, probeRR.Body.String())
	assert.Empty(t, probeRR.Header().Get("X-Stamped-CLI-Session-ID"), "an Operator certificate request must not carry a CLI session identity")

	bindController := newCLIRefreshController(CLIRefreshControllerDeps{
		Cfg:                infra.Cfg,
		Logger:             infra.Logger,
		CLISessionSvc:      infra.CLISessionSvc,
		OperatorSessionSvc: infra.OperatorSessionSvc,
		Reg:                infra.Reg,
		Auth:               infra.Auth,
		UserSvc:            infra.UserSvc,
		Responder:          infra.Responder,
	})
	bindHandler := infra.Auth.Middleware(http.HandlerFunc(bindController.handleBind))
	body, err := json.Marshal(models.CLIBindRequest{OperatorSessionID: targetSessionID})
	require.NoError(t, err)

	for _, cliSessionID := range []string{foreignSessionID, inactiveSessionID} {
		rr := httptest.NewRecorder()
		bindHandler.ServeHTTP(rr, operatorCertRequest(t, constants.APIPaths.AuthCLIBind, orgID, operatorID, operatorSessionID, cliSessionID, body))
		assert.Equal(t, http.StatusNotFound, rr.Code, "bind with an Operator certificate must not reach the CLI session the header names: %s", rr.Body.String())
	}

	foreignAfter := loadStoredCLISession(t, infra.CLISessionSvc, foreignSessionID)
	assert.Equal(t, foreignBefore.OperatorSessionID, foreignAfter.OperatorSessionID, "a foreign CLI session must keep its operator binding")
	assert.Empty(t, foreignAfter.BoundOperatorSessionIDs, "a foreign CLI session must not gain bound operators")
}

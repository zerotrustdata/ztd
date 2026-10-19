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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// setupTestCLIRefreshController creates a CLIRefreshController backed by
// the full test infrastructure (real DB, PKI, session services). A real
// active user is created so refresh has a valid identity. Returns the
// controller and the created user.
func setupTestCLIRefreshController(t *testing.T) (*CLIRefreshController, *models.User) {
	t.Helper()
	infra := setupTestInfrastructure(t, false)

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

	user, err := infra.UserSvc.CreateUser(t.Context())
	require.NoError(t, err)
	require.NotNil(t, user)

	return c, user
}

// persistCLISessionForController creates an active CLI session document
// directly in the doc store for the given user, bound to operatorSessionID.
// Used by controller tests to set up the session state that refresh extends.
func persistCLISessionForController(t *testing.T, c *CLIRefreshController, userID, sessionID, operatorSessionID string) {
	t.Helper()
	doc := models.CLISession{
		ID:                sessionID,
		UserID:            userID,
		OperatorSessionID: operatorSessionID,
		SystemFingerprint: "test-sys-fp",
		CertFingerprint:   "test-cert-fp",
		CertSerial:        "test-serial",
		IsActive:          true,
		SessionType:       string(constants.SessionTypeCLI),
		LoginMethod:       string(constants.HeartbeatTypeBootstrap),
	}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), sessionID, b,
	))
}

// refreshRequestWithContext builds a POST /refresh request with the mTLS
// context (user ID + old CLI session ID) stamped, matching what the unified
// auth middleware does for RouteAuthMTLS routes. The body is empty — the
// cert is the proof of identity.
func refreshRequestWithContext(t *testing.T, userID, oldCLISessionID string) *http.Request {
	t.Helper()
	body, err := json.Marshal(models.CLIRefreshRequest{})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIRefresh, bytes.NewReader(body))
	ctx := context.WithValue(req.Context(), constants.ContextKeyUserID, userID)
	ctx = context.WithValue(ctx, constants.ContextKeyCLISessionID, oldCLISessionID)
	return req.WithContext(ctx)
}

// parseRefreshResponse parses a successful refresh response.
func parseRefreshResponse(t *testing.T, rr *httptest.ResponseRecorder) models.CLIRefreshResponse {
	t.Helper()
	require.Equalf(t, http.StatusCreated, rr.Code, "refresh should succeed, body: %s", rr.Body.String())
	var resp models.CLIRefreshResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	return resp
}

// ---------------------------------------------------------------------------
// handleRefresh — success path
// ---------------------------------------------------------------------------

// TestCLIRefreshController_Refresh_Success verifies the primary recovery path
// on an expired session: the cert is still valid and the session it names is
// active but past its expiry. The refresh keeps the session ID and certificate
// grounding, moves the expiry into the TTL window, and writes the binding.
// No session is minted and the old session is never deactivated.
func TestCLIRefreshController_Refresh_Success(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)
	sessionID := "refresh-ctrl-old-1"
	require.NoError(t, c.operatorSessionSvc.PersistOperatorSession(t.Context(),
		"op-ctrl-1", user.ID, "org-1", "op-id-1", "mTLS",
	))
	persistCLISessionForController(t, c, user.ID, sessionID, "op-ctrl-1")
	expireStoredCLISession(t, c.cliSessionSvc, sessionID)
	before := loadStoredCLISession(t, c.cliSessionSvc, sessionID)

	req := refreshRequestWithContext(t, user.ID, sessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	resp := parseRefreshResponse(t, rr)
	assert.Equal(t, sessionID, resp.CLISessionID, "refresh must keep the session ID the certificate names")
	assert.Equal(t, user.ID, resp.UserID)

	stored := loadStoredCLISession(t, c.cliSessionSvc, sessionID)
	assert.True(t, stored.IsActive, "the refreshed session must be active")
	assert.True(t, stored.ExpiresAt.After(time.Now()), "the refreshed session must have a future expiry")
	assert.Equal(t, before.CertFingerprint, stored.CertFingerprint, "refresh keeps the certificate fingerprint")
	assert.Equal(t, before.CertSerial, stored.CertSerial, "refresh keeps the certificate serial")
	assert.Equal(t, user.ID, stored.UserID)

	sessions, err := c.cliSessionSvc.ListUserCLISessions(t.Context(), user.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1, "refresh must not mint a replacement session")
}

// TestCLIRefreshController_Refresh_DeactivatedSession_ConflictAndNotRevived
// verifies that a logged-out session cannot be refreshed back to life: the
// request is refused with 409 and the session stays deactivated.
func TestCLIRefreshController_Refresh_DeactivatedSession_ConflictAndNotRevived(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)
	sessionID := "refresh-ctrl-deactivated"
	require.NoError(t, c.operatorSessionSvc.PersistOperatorSession(t.Context(),
		"op-ctrl-deact", user.ID, "org-1", "op-id-deact", "mTLS",
	))
	persistCLISessionForController(t, c, user.ID, sessionID, "op-ctrl-deact")
	require.NoError(t, c.cliSessionSvc.DeactivateCLISession(t.Context(), sessionID))

	req := refreshRequestWithContext(t, user.ID, sessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code, rr.Body.String())
	stored := loadStoredCLISession(t, c.cliSessionSvc, sessionID)
	assert.False(t, stored.IsActive, "a deactivated session must not be revived by refresh")
	sessions, err := c.cliSessionSvc.ListUserCLISessions(t.Context(), user.ID)
	require.NoError(t, err)
	assert.Len(t, sessions, 1, "a refused refresh must not mint a session")
}

// TestCLIRefreshController_Refresh_UnknownSession_Unauthorized verifies that a
// certificate whose URI SAN names a session the gateway does not have is
// refused with 401. There is no session to extend, and refresh does not mint
// one under the certificate's name.
func TestCLIRefreshController_Refresh_UnknownSession_Unauthorized(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)

	// An active operator session exists, so only the missing CLI session can
	// cause the refusal.
	require.NoError(t, c.operatorSessionSvc.PersistOperatorSession(t.Context(),
		"op-refresh-missing", user.ID, "org-1", "op-id-1", "mTLS",
	))

	req := refreshRequestWithContext(t, user.ID, "refresh-ctrl-missing-old")
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code, rr.Body.String())
	assert.Contains(t, rr.Body.String(), constants.ErrCLISessionInvalid.Error())
	sessions, err := c.cliSessionSvc.ListUserCLISessions(t.Context(), user.ID)
	require.NoError(t, err)
	assert.Empty(t, sessions, "refresh must not mint a session for an unknown certificate session")
}

// TestCLIRefreshController_Refresh_NoOperatorSession_ReturnsClearError verifies
// the fail-closed case: the CLI session exists but neither it nor the user has
// an active operator session. The controller returns a clear, actionable 409
// pointing to re-enrollment rather than a generic 500, and changes nothing.
func TestCLIRefreshController_Refresh_NoOperatorSession_ReturnsClearError(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)
	sessionID := "refresh-ctrl-no-op-sess"
	persistCLISessionForController(t, c, user.ID, sessionID, "")

	req := refreshRequestWithContext(t, user.ID, sessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	assert.Equal(t, http.StatusConflict, rr.Code)
	assert.Contains(t, rr.Body.String(), "re-enroll")
	stored := loadStoredCLISession(t, c.cliSessionSvc, sessionID)
	assert.Empty(t, stored.OperatorSessionID, "a refused refresh must not write a binding")
}

// TestCLIRefreshController_Refresh_StaleOperatorSession_FallsBackToActive verifies
// that when the old CLI session's operator session ID no longer matches the user's
// active operator session (e.g., the old operator was terminated and a new one
// enrolled), the controller falls back to the active operator session instead of
// inheriting the stale binding. This prevents a CLI session binding mismatch on
// subsequent gateway validation.
func TestCLIRefreshController_Refresh_StaleOperatorSession_FallsBackToActive(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)

	// Persist a new active operator session for the user.
	require.NoError(t, c.operatorSessionSvc.PersistOperatorSession(t.Context(),
		"op-refresh-active", user.ID, "org-1", "op-id-active", "mTLS",
	))

	// Old CLI session references a stale operator session that is no longer active.
	oldSessionID := "refresh-ctrl-stale-op"
	persistCLISessionForController(t, c, user.ID, oldSessionID, "op-refresh-stale")

	req := refreshRequestWithContext(t, user.ID, oldSessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	resp := parseRefreshResponse(t, rr)
	assert.Equal(t, oldSessionID, resp.CLISessionID, "refresh keeps the session ID")
	assert.Equal(t, user.ID, resp.UserID)

	// The refreshed session must be bound to the active operator session, not the stale one.
	newSession := loadStoredCLISession(t, c.cliSessionSvc, oldSessionID)
	assert.True(t, newSession.IsActive)
	assert.Equal(t, user.ID, newSession.UserID)
	assert.Equal(t, "op-refresh-active", newSession.OperatorSessionID,
		"refreshed session must bind to the active operator session, not the stale one")
}

// TestCLIRefreshController_Refresh_PrefersRegistryActiveDataOperator verifies
// that refresh binds to the active governed tool Operator recorded in the
// operator registry even when a stale operator_sessions row still matches the
// old CLI session binding.
func TestCLIRefreshController_Refresh_SkipsProvenanceOperatorBinding(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)

	provenanceSessionID := "op-refresh-provenance"
	dataSessionID := "op-refresh-data"
	now := time.Now().UTC()

	provenanceBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                "op-id-provenance",
		UserId:            user.ID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: provenanceSessionID,
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: []string{string(constants.OperatorCapabilityProvenance)}},
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionOperators), "op-id-provenance", provenanceBytes,
	))

	dataBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                "op-id-data",
		UserId:            user.ID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: dataSessionID,
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionOperators), "op-id-data", dataBytes,
	))

	oldSessionID := "refresh-ctrl-provenance-skip"
	persistCLISessionForController(t, c, user.ID, oldSessionID, provenanceSessionID)

	req := refreshRequestWithContext(t, user.ID, oldSessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	resp := parseRefreshResponse(t, rr)
	assert.Equal(t, dataSessionID, resp.OperatorSessionID)
	assert.Equal(t, "op-id-data", resp.OperatorID)
}

func TestCLIRefreshController_Refresh_PrefersRegistryActiveDataOperator(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)

	staleSessionID := "op-refresh-stale-registry"
	activeSessionID := "op-refresh-registry-active"
	require.NoError(t, c.operatorSessionSvc.PersistOperatorSession(t.Context(),
		staleSessionID, user.ID, "org-1", "op-id-stale", "mTLS",
	))

	now := time.Now().UTC()
	opBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                "op-id-registry-active",
		UserId:            user.ID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: activeSessionID,
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSet(t.Context(),
		marshaler.CollectionName(constants.CollectionOperators), "op-id-registry-active", opBytes,
	))

	oldSessionID := "refresh-ctrl-registry-stale"
	persistCLISessionForController(t, c, user.ID, oldSessionID, staleSessionID)

	req := refreshRequestWithContext(t, user.ID, oldSessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	resp := parseRefreshResponse(t, rr)
	newDoc, err := c.cliSessionSvc.db.DocGet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), resp.CLISessionID)
	require.NoError(t, err)
	require.NotNil(t, newDoc)
	var newSession models.CLISession
	dataBytes, err := json.Marshal(newDoc.Data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(dataBytes, &newSession))
	assert.Equal(t, activeSessionID, newSession.OperatorSessionID,
		"new CLI session must bind to registry-active data operator")
	assert.Equal(t, activeSessionID, resp.OperatorSessionID)
	assert.Equal(t, "op-id-registry-active", resp.OperatorID)
}

// ---------------------------------------------------------------------------
// handleRefresh — error paths
// ---------------------------------------------------------------------------

// TestCLIRefreshController_Refresh_MissingUserContext verifies that a
// request with no authenticated user context (the auth middleware did not
// stamp ContextKeyUserID) is rejected with 401. This is the fail-closed
// guard against a misconfigured route that bypasses the auth middleware.
func TestCLIRefreshController_Refresh_MissingUserContext(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)
	persistCLISessionForController(t, c, user.ID, "refresh-ctrl-no-ctx", "op-ctrl-no-ctx")

	body, _ := json.Marshal(models.CLIRefreshRequest{})
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.AuthCLIRefresh, bytes.NewReader(body))
	// No user context — simulate unauthenticated request.
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

// TestCLIRefreshController_Refresh_UserNotActive verifies that a disabled
// user is rejected with 403 even though the cert is still valid. An expired
// session does not bypass user-disabled checks.
func TestCLIRefreshController_Refresh_UserNotActive(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)
	oldSessionID := "refresh-ctrl-disabled-user"
	persistCLISessionForController(t, c, user.ID, oldSessionID, "op-ctrl-disabled")

	// Disable the user after the session was created.
	require.NoError(t, c.userSvc.Disable(t.Context(), user.ID, "test", "actor", "op"))

	req := refreshRequestWithContext(t, user.ID, oldSessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.Contains(t, rr.Body.String(), "not active")
}

// TestCLIRefreshController_Refresh_MethodNotAllowed verifies that a non-POST
// request is rejected with 405.
func TestCLIRefreshController_Refresh_MethodNotAllowed(t *testing.T) {
	c, _ := setupTestCLIRefreshController(t)
	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.AuthCLIRefresh, nil)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

// TestCLIRefreshController_Refresh_UnboundOldSession_BindsEmbedded verifies
// the binding-recovery path: an old CLI session persisted before the
// operator-binding work (empty operator_session_id) is rebound to the
// user's active embedded operator session on refresh, and the new session
// persists the recovered binding.
func TestCLIRefreshController_Refresh_UnboundOldSession_BindsEmbedded(t *testing.T) {
	c, user := setupTestCLIRefreshController(t)

	// The user has an active embedded operator session — the canonical
	// local binding — but the old CLI session predates the binding work
	// and carries no operator_session_id.
	now := time.Now().UTC()
	embeddedBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                string(constants.DocIDEmbeddedOperator),
		UserId:            user.ID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeEmbedded),
		OperatorSessionId: "embedded-sess-refresh",
		RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{},
		CreatedAt:         timestamppb.New(now),
		UpdatedAt:         timestamppb.New(now),
	})
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionOperators), string(constants.DocIDEmbeddedOperator), embeddedBytes))
	staleRemoteBytes, err := models.MarshalOperatorDocument(&operatorv1.OperatorDocument{
		Id:                "stale-remote-operator",
		UserId:            user.ID,
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeRemote),
		OperatorSessionId: "stale-remote-session",
		CreatedAt:         timestamppb.New(now.Add(-48 * time.Hour)),
		UpdatedAt:         timestamppb.New(now.Add(-48 * time.Hour)),
	})
	require.NoError(t, err)
	require.NoError(t, c.cliSessionSvc.db.DocSetWithTimestamps(t.Context(), marshaler.CollectionName(constants.CollectionOperators), "stale-remote-operator", staleRemoteBytes, now.Add(-48*time.Hour), now.Add(-48*time.Hour)))
	oldSessionID := "refresh-ctrl-unbound-old"
	persistCLISessionForController(t, c, user.ID, oldSessionID, "")

	req := refreshRequestWithContext(t, user.ID, oldSessionID)
	rr := httptest.NewRecorder()
	c.handleRefresh(rr, req)

	resp := parseRefreshResponse(t, rr)
	assert.Equal(t, "embedded-sess-refresh", resp.OperatorSessionID)
	assert.Equal(t, string(constants.DocIDEmbeddedOperator), resp.OperatorID)

	// The new session persists the recovered binding.
	newDoc, err := c.cliSessionSvc.db.DocGet(t.Context(),
		marshaler.CollectionName(constants.CollectionCLISessions), resp.CLISessionID)
	require.NoError(t, err)
	require.NotNil(t, newDoc)
	var newSession models.CLISession
	dataBytes, err := json.Marshal(newDoc.Data)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(dataBytes, &newSession))
	assert.Equal(t, "embedded-sess-refresh", newSession.OperatorSessionID,
		"new session persists the recovered embedded binding")
}

func TestDefaultDataOperatorOrderPrefersEmbedded(t *testing.T) {
	active := func(id string, kind constants.OperatorType, capabilities ...constants.OperatorCapability) *operatorv1.OperatorDocument {
		return &operatorv1.OperatorDocument{
			Id:                id,
			OperatorSessionId: "sess-" + id,
			Status:            string(constants.OperatorStatusActive),
			OperatorType:      string(kind),
			RuntimeConfig:     &operatorv1.OperatorRuntimeConfig{Capabilities: models.OperatorCapabilitiesToProto(capabilities)},
		}
	}
	dedicated := active("data", constants.OperatorTypeRemote, constants.OperatorCapabilityData)
	inference := active("inference", constants.OperatorTypeRemote, constants.OperatorCapabilityInference)
	embedded := active(string(constants.DocIDEmbeddedOperator), constants.OperatorTypeEmbedded)

	assert.Equal(t, []*operatorv1.OperatorDocument{embedded, dedicated}, defaultDataOperatorOrder([]*operatorv1.OperatorDocument{dedicated, inference, embedded}))
	assert.Equal(t, []*operatorv1.OperatorDocument{dedicated}, defaultDataOperatorOrder([]*operatorv1.OperatorDocument{inference, dedicated}))
}

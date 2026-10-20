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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// ---------------------------------------------------------------------------
// authorizeSSERoute
// ---------------------------------------------------------------------------

func TestAuthorizeSSERoute_MissingAuthIdentity(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1", CLISessionID: "sess1"}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil)
	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, sseErr.status)
}

func TestAuthorizeSSERoute_MultipleRoutingTargets(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1", CLISessionID: "sess1", WebSessionID: "web1"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)
	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, sseErr.status)
	assert.Contains(t, sseErr.message, "mutually-exclusive")
}

func TestAuthorizeSSERoute_NoRoutingTarget(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)
	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, sseErr.status)
}

func TestAuthorizeSSERoute_CLISession_OperatorMTLSAuth_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-auth-cli-ok"
	userID := "user-auth-cli-ok"
	cliSessionID := "cli-auth-ok"

	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, opSessID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:cli:"+cliSessionID, channel)
}

func TestAuthorizeSSERoute_CLISession_OperatorMTLSAuth_WrongOperator(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-owner"
	userID := "user1"
	cliSessionID := "cli-owned"
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "different-opsess")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "operator session does not own this cli session")
}

// seedMultiBindCLISessionDoc persists a CLI session whose primary operator
// binding is primarySessionID and whose full bound list is boundSessionIDs.
func seedMultiBindCLISessionDoc(t *testing.T, h *HTTPHandler, cliSessionID, userID, primarySessionID string, boundSessionIDs []string) {
	t.Helper()
	cliBytes, err := json.Marshal(models.CLISession{
		ID:                      cliSessionID,
		UserID:                  userID,
		OperatorSessionID:       primarySessionID,
		BoundOperatorSessionIDs: boundSessionIDs,
	})
	require.NoError(t, err)
	require.NoError(t, h.dataController.docStore.DocSet(t.Context(), marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliBytes))
}

// A CLI session bound to several operators may stream through any bound
// operator session, not only the primary: handleCLIAuth stamps the bound
// secondary the request names, and authorizeSSERoute must accept it.
func TestAuthorizeSSERoute_CLISession_SecondaryBoundOperator_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	userID := "user-secondary-bound"
	cliSessionID := "cli-secondary-bound"
	primaryOpSessID := "opsess-primary-bound"
	secondaryOpSessID := "opsess-secondary-bound"
	seedMultiBindCLISessionDoc(t, h, cliSessionID, userID, primaryOpSessID, []string{primaryOpSessID, secondaryOpSessID})

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, secondaryOpSessID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:cli:"+cliSessionID, channel)
}

// An operator session that is not in the CLI session's bound list fails
// closed, even when the CLI session has a multi-bind list.
func TestAuthorizeSSERoute_CLISession_UnboundOperatorOnMultiBind_Forbidden(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	userID := "user-unbound-multi"
	cliSessionID := "cli-unbound-multi"
	primaryOpSessID := "opsess-primary-unbound"
	seedMultiBindCLISessionDoc(t, h, cliSessionID, userID, primaryOpSessID, []string{primaryOpSessID, "opsess-other-bound"})

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "opsess-not-bound")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "operator session does not own this cli session")
}

func TestAuthorizeSSERoute_CLISession_CLIMTLSAuth_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-cli-mtls"
	userID := "user-cli-mtls"
	cliSessionID := "cli-mtls-ok"
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:cli:"+cliSessionID, channel)
}

func TestAuthorizeSSERoute_CLISession_CLIMTLSAuth_WrongUser(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-cli-wrong"
	userID := "user-owner"
	cliSessionID := "cli-wrong-user"
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: "different-user", CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "different-user")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "user does not own this cli session")
}

func TestAuthorizeSSERoute_CLISession_CookieAuth_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-cookie"
	userID := "user-cookie"
	cliSessionID := "cli-cookie-ok"
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: userID, CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyWebSessionID, "web-sess")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:cli:"+cliSessionID, channel)
}

func TestAuthorizeSSERoute_CLISession_CookieAuth_WrongUser(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-cookie-wrong"
	userID := "user-cookie-owner"
	cliSessionID := "cli-cookie-wrong"
	seedCLISessionDoc(t, h, cliSessionID, userID, opSessID)

	route := SSERoute{UserID: "different-user", CLISessionID: cliSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyWebSessionID, "web-sess")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, "different-user")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "user does not own this cli session")
}

func TestAuthorizeSSERoute_CLISession_NotFound(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1", CLISessionID: "nonexistent"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "opsess1")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "cli session not found")
}

func TestAuthorizeSSERoute_WebSession_OperatorMTLSAuth_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-web-mtls"
	userID := "user-web-mtls"
	webSessionID := "web-mtls-ok"
	bindOperatorToWebSession(t, h, opSessID, webSessionID)

	route := SSERoute{UserID: userID, WebSessionID: webSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, opSessID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:web:"+webSessionID, channel)
}

func TestAuthorizeSSERoute_WebSession_OperatorMTLSAuth_WrongBinding(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	opSessID := "opsess-web-bound"
	userID := "user-web-bound"
	webSessionID := "web-bound"
	bindOperatorToWebSession(t, h, opSessID, webSessionID)

	route := SSERoute{UserID: userID, WebSessionID: "different-web-session"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, opSessID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "operator session does not own this web session")
}

func TestAuthorizeSSERoute_WebSession_OperatorMTLSAuth_NoBinding(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1", WebSessionID: "web-unbound"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "opsess-unbound")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
}

func TestAuthorizeSSERoute_WebSession_CookieAuth_Success(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	webSessionID := "web-cookie-ok"
	userID := "user-web-cookie"

	route := SSERoute{UserID: userID, WebSessionID: webSessionID}
	ctx := context.WithValue(context.Background(), constants.ContextKeyWebSessionID, webSessionID)
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, userID)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	channel, err := h.sseController.authorizeSSERoute(route, req)
	require.NoError(t, err)
	assert.Equal(t, "sse:web:"+webSessionID, channel)
}

func TestAuthorizeSSERoute_WebSession_CookieAuth_Mismatch(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1", WebSessionID: "web-expected"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyWebSessionID, "web-actual")
	ctx = context.WithValue(ctx, constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "web session does not match authenticated session")
}

func TestAuthorizeSSERoute_UserIDMismatch(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user-expected", CLISessionID: "sess1"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user-actual")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusForbidden, sseErr.status)
	assert.Contains(t, sseErr.message, "user does not match authenticated user")
}

func TestAuthorizeSSERoute_MissingSession(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	route := SSERoute{UserID: "user1"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "user1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, sseErr.status)
	assert.Contains(t, sseErr.message, "exactly one of web_session_id or cli_session_id")
}

func TestAuthorizeSSERoute_AppCertExcludedFromMTLSAuth(t *testing.T) {
	h, _, _ := setupTestHTTPHandler(t)
	userID := "user-app-test"
	route := SSERoute{UserID: userID, CLISessionID: "sess1"}
	ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, userID)
	ctx = context.WithValue(ctx, constants.ContextKeyAppID, "spiffe://ztd.local/app/op1")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sse/events", nil).WithContext(ctx)

	_, err := h.sseController.authorizeSSERoute(route, req)
	require.Error(t, err)
	sseErr, ok := err.(*sseAuthError)
	require.True(t, ok)
	assert.Equal(t, http.StatusUnauthorized, sseErr.status)
}

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
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func setupTestAdminController(t *testing.T) *AdminController {
	t.Helper()
	infra := setupTestInfrastructure(t, false)
	return newAdminController(AdminControllerDeps{Cfg: infra.Cfg, Logger: infra.Logger, DocStore: infra.DocStore, SignerStore: infra.SignerStore, UserSvc: infra.UserSvc, Responder: infra.Responder})
}

func TestAdminControllerHandleAppPolicySigner(t *testing.T) {
	adminController := setupTestAdminController(t)

	// The first user created is the gateway admin (IsFirstUser).
	adminUser, err := adminController.userSvc.CreateUser(t.Context())
	require.NoError(t, err)
	require.NotNil(t, adminUser)
	t.Cleanup(func() { adminController.docStore.DocDelete(context.Background(), "users", adminUser.ID) })

	// A second user is a non-admin regular user.
	regularUser, err := adminController.userSvc.CreateUser(t.Context())
	require.NoError(t, err)
	require.NotNil(t, regularUser)
	t.Cleanup(func() { adminController.docStore.DocDelete(context.Background(), "users", regularUser.ID) })

	// Create an app policy document for testing
	now := time.Now()
	appPolicy := models.AppPolicy{
		AppID:              "test-app-id",
		AllowedCollections: []string{"test-collection"},
		AllowedEventTypes:  []string{"test-event"},
		AllowedIntents:     []string{"test-intent"},
		RateLimitRPS:       100,
		MaxPayloadBytes:    1024 * 1024,
		RequireL3Approval:  false,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	policyDoc, err := json.Marshal(appPolicy)
	require.NoError(t, err)
	err = adminController.docStore.DocSet(t.Context(), "app_policies", "test-app-id", policyDoc)
	require.NoError(t, err)
	t.Cleanup(func() { adminController.docStore.DocDelete(context.Background(), "app_policies", "test-app-id") })

	// Generate a valid Ed25519 public key for testing
	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	validPubKeyHex := hex.EncodeToString(pubKey)

	t.Run("MethodNotAllowed - GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/app-policies/test-app-id/signers", nil)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("BadRequest - missing app_id", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies//signers", nil)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("Unauthorized - no user context", func(t *testing.T) {
		body := []byte(`{"public_key_hex": "` + validPubKeyHex + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("Unauthorized - empty user_id", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, "")
		body := []byte(`{"public_key_hex": "` + validPubKeyHex + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusUnauthorized, rr.Code)
	})

	t.Run("Forbidden - non-admin (non-first) user", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, regularUser.ID)
		body := []byte(`{"public_key_hex": "` + validPubKeyHex + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})

	t.Run("Forbidden - app policy not found", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{"public_key_hex": "` + validPubKeyHex + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/nonexistent-app/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusForbidden, rr.Code)
	})

	t.Run("BadRequest - invalid JSON", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{invalid json}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("BadRequest - missing public_key_hex", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("BadRequest - empty public_key_hex", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{"public_key_hex": ""}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("BadRequest - invalid hex public key", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{"public_key_hex": "not-hex"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("BadRequest - invalid public key size", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		shortKey := hex.EncodeToString([]byte{0x01, 0x02})
		body := []byte(`{"public_key_hex": "` + shortKey + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("Success - valid signer registration", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), constants.ContextKeyUserID, adminUser.ID)
		body := []byte(`{"public_key_hex": "` + validPubKeyHex + `"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/app-policies/test-app-id/signers", bytes.NewReader(body)).WithContext(ctx)
		rr := httptest.NewRecorder()
		adminController.handleAppPolicySigner(rr, req)
		assert.Equal(t, http.StatusCreated, rr.Code)

		// Verify the signer was added to the database
		signerDoc, err := adminController.docStore.DocGet(t.Context(), "trusted_signers", "test-app-id")
		require.NoError(t, err)
		require.NotNil(t, signerDoc)
		assert.Equal(t, "test-app-id", signerDoc.ID)

		var signer models.TrustedSigner
		require.NoError(t, signerDoc.DecodeData(&signer))
		assert.Equal(t, validPubKeyHex, signer.PublicKey)
		assert.True(t, signer.Enabled)
	})
}

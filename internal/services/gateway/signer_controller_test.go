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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/models"
)

func setupTestSignerController(t *testing.T) (*SignerController, *DocumentStoreService) {
	t.Helper()
	infra := setupTestInfrastructure(t, false)

	signerController := newSignerController(SignerControllerDeps{
		Cfg:         infra.Cfg,
		Logger:      infra.Logger,
		DocStore:    infra.DocStore,
		SignerStore: infra.SignerStore,
		Responder:   infra.Responder,
	})

	return signerController, infra.DocStore
}

func TestNewSignerController_AllDepsProvidedNoNilFields(t *testing.T) {
	infra := setupTestInfrastructure(t, false)

	controller := newSignerController(SignerControllerDeps{
		Cfg:         infra.Cfg,
		Logger:      infra.Logger,
		DocStore:    infra.DocStore,
		SignerStore: infra.SignerStore,
		Responder:   infra.Responder,
	})

	assert.NotNil(t, controller.cfg)
	assert.NotNil(t, controller.logger)
	assert.NotNil(t, controller.docStore)
	assert.NotNil(t, controller.signerStore)
	assert.NotNil(t, controller.responder)

	assert.Equal(t, infra.Cfg, controller.cfg)
	assert.Equal(t, infra.Logger, controller.logger)
	assert.Equal(t, infra.DocStore, controller.docStore)
	assert.Equal(t, infra.SignerStore, controller.signerStore)
	assert.Equal(t, infra.Responder, controller.responder)
}

func TestSignerControllerHandleGovernanceSigners(t *testing.T) {
	signerController, _ := setupTestSignerController(t)

	t.Run("GET - success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSigners(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), `"success":true`)
	})

	t.Run("POST - method not allowed", func(t *testing.T) {
		signer := models.TrustedSigner{
			ID:        "test-signer-1",
			PublicKey: strings.Repeat("a", 64),
			AddedAt:   time.Now().UTC(),
			Enabled:   true,
		}
		body := mustMarshalJSON(t, signer)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/governance/signers", bytes.NewReader(body))
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSigners(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/governance/signers", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSigners(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})
}

func TestSignerControllerHandleGovernanceSignerByID(t *testing.T) {
	signerController, docStore := setupTestSignerController(t)

	t.Run("GET - not found", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers/nonexistent", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("GET - unregistered Ed25519-shaped key ID is not found", func(t *testing.T) {
		// A key id never vouches for itself (INV-GW-GOV-06).
		keyID := strings.Repeat("d", 64)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers/"+keyID, nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("GET - success", func(t *testing.T) {
		signer := models.TrustedSigner{
			ID:        "test-signer-get",
			PublicKey: strings.Repeat("b", 64),
			AddedAt:   time.Now().UTC(),
			Enabled:   true,
		}
		signerBytes := mustMarshalJSON(t, signer)
		err := docStore.DocSet(t.Context(), "trusted_signers", "test-signer-get", signerBytes)
		require.NoError(t, err)
		t.Cleanup(func() { docStore.DocDelete(context.Background(), "trusted_signers", "test-signer-get") })

		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers/test-signer-get", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Contains(t, rr.Body.String(), "test-signer-get")
	})

	t.Run("DELETE - method not allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/governance/signers/test-signer-delete", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})

	t.Run("Invalid signer id - empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers/", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("Invalid signer id - contains slash", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/governance/signers/invalid/id", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("Method Not Allowed", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/governance/signers/test-id", nil)
		rr := httptest.NewRecorder()
		signerController.handleGovernanceSignerByID(rr, req)
		assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	})
}

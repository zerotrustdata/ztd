// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestModelProvenanceControllerHandleModelProvenance_RejectsNonGet(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	controller := newModelProvenanceController(ModelProvenanceControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
	})
	req := httptest.NewRequest(http.MethodPost, constants.APIPaths.InferenceModelProvenanceAttestations+"attempt-1", nil)
	rr := httptest.NewRecorder()
	controller.handleModelProvenance(rr, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

type emptyProvenanceWindows struct{}

func (emptyProvenanceWindows) Load(context.Context, string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	return nil, constants.ErrNotFound
}

func (emptyProvenanceWindows) Save(context.Context, *evalv1.ModelProvenanceAttestationWindow) error {
	return constants.ErrServiceUnavailable
}

func TestModelProvenanceController_OnlyReadsPersistedWindows(t *testing.T) {
	t.Parallel()

	logger := testutil.NewTestLogger()
	controller := newModelProvenanceController(ModelProvenanceControllerDeps{
		Logger: logger, Responder: response.NewWriter(logger), Windows: emptyProvenanceWindows{},
	})
	for _, suffix := range []string{"_attest", "_preflight"} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, constants.APIPaths.InferenceModelProvenanceAttestations+suffix+"?served_model_tag=probe-model:7b&expected_model_digest="+strings.Repeat("a", 64), nil)
			rr := httptest.NewRecorder()
			controller.handleModelProvenance(rr, req)
			require.Equal(t, http.StatusNotFound, rr.Code)
		})
	}
}

func TestModelProvenanceControllerHandleModelProvenance_UnavailableWithoutStore(t *testing.T) {
	t.Parallel()
	logger := testutil.NewTestLogger()
	controller := newModelProvenanceController(ModelProvenanceControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
	})
	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.InferenceModelProvenanceAttestations+"attempt-1", nil)
	rr := httptest.NewRecorder()
	controller.handleModelProvenance(rr, req)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestModelProvenanceControllerHandleModelProvenance_NotFound(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	logger := testutil.NewTestLogger()
	controller := newModelProvenanceController(ModelProvenanceControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
		Windows:   store,
	})
	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.InferenceModelProvenanceAttestations+"missing-attempt", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	controller.handleModelProvenance(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestModelProvenanceControllerHandleModelProvenance_ReturnsWindow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fileSvc := storagetest.NewTestFileSvc(t, testutil.TempDir(t))
	store, err := model_provenance.NewWindowStore(fileSvc)
	require.NoError(t, err)
	digest := strings.Repeat("a", 64)
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              model_provenance.SchemaVersion,
		ProviderAttemptId:          "attempt-1",
		ProvenanceOperatorId:       "test-provenance-operator",
		ServedModelTag:             "probe-model:7b",
		ExpectedModelDigest:        digest,
		ObservedModelDigest:        digest,
		ManifestDigest:             strings.Repeat("b", 64),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED,
		AttestedAtUnixMs:           1_700_000_000_000,
		DigestMatch:                true,
	}
	attestationDigest, err := model_provenance.ComputeAttestationDigest(window)
	require.NoError(t, err)
	window.AttestationDigest = attestationDigest
	require.NoError(t, store.Save(ctx, window))

	logger := testutil.NewTestLogger()
	controller := newModelProvenanceController(ModelProvenanceControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
		Windows:   store,
	})
	req := httptest.NewRequest(http.MethodGet, constants.APIPaths.InferenceModelProvenanceAttestations+"attempt-1", nil)
	rr := httptest.NewRecorder()
	controller.handleModelProvenance(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var resp models.ModelProvenanceResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	loaded := &evalv1.ModelProvenanceAttestationWindow{}
	require.NoError(t, evalv1.UnmarshalCanonical(resp.Window, loaded))
	assert.Equal(t, "attempt-1", loaded.GetProviderAttemptId())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newEvalCampaignPublicationControllerTest(t *testing.T) *EvalCampaignPublicationController {
	t.Helper()
	logger := testutil.NewTestLogger()
	return newEvalCampaignPublicationController(EvalCampaignPublicationControllerDeps{
		Logger:    logger,
		Responder: response.NewWriter(logger),
		Service:   newEvalCampaignPublicationServiceTest(t),
	})
}

func TestEvalCampaignPublicationController_HandlePublicationState_DeleteReturnsDiscardResponse(t *testing.T) {
	controller := newEvalCampaignPublicationControllerTest(t)
	runID := "eval-init-nemotron-3-nano-4b-1792241875"

	req := httptest.NewRequest(http.MethodDelete, constants.EvalCampaignPublicationStatePath(runID), nil)
	rec := httptest.NewRecorder()
	controller.handlePublicationState(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var discarded models.EvalCampaignPublicationDiscardResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &discarded))
	assert.Equal(t, models.EvalCampaignPublicationDiscardResponse{RunID: runID, Discarded: true}, discarded)
}

func TestEvalCampaignPublicationController_HandlePublicationState_GetEmptyState(t *testing.T) {
	controller := newEvalCampaignPublicationControllerTest(t)
	runID := "eval-init-qwen3-4b-1789739892"
	path := constants.EvalCampaignPublicationStatePath(runID)

	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	controller.handlePublicationState(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var state models.EvalCampaignPublicationState
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &state))
	assert.Equal(t, runID, state.RunID)
	assert.Empty(t, state.PublishedIdempotency)
}

func TestEvalCampaignPublicationController_HandlePublicationState_PutAndGet(t *testing.T) {
	controller := newEvalCampaignPublicationControllerTest(t)
	runID := "eval-init-gemma3-4b-1789739892"
	path := constants.EvalCampaignPublicationStatePath(runID)
	body := models.EvalCampaignPublicationState{
		RunID:                 runID,
		PublishedIdempotency:  []string{"run:key-a"},
		LastPublishedSequence: 12,
	}
	payload, err := json.Marshal(body)
	require.NoError(t, err)

	putReq := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	putRec := httptest.NewRecorder()
	controller.handlePublicationState(putRec, putReq)
	require.Equal(t, http.StatusOK, putRec.Code)

	getReq := httptest.NewRequest(http.MethodGet, path, nil)
	getRec := httptest.NewRecorder()
	controller.handlePublicationState(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)

	var loaded models.EvalCampaignPublicationState
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &loaded))
	assert.Equal(t, runID, loaded.RunID)
	assert.Equal(t, body.PublishedIdempotency, loaded.PublishedIdempotency)
	assert.Equal(t, body.LastPublishedSequence, loaded.LastPublishedSequence)
}

func TestEvalCampaignPublicationController_HandlePublicationState_RejectsInvalidRequests(t *testing.T) {
	controller := newEvalCampaignPublicationControllerTest(t)
	runID := "eval-init-qwen3-4b-1789739892"
	path := constants.EvalCampaignPublicationStatePath(runID)

	tests := []struct {
		name       string
		method     string
		path       string
		body       []byte
		wantStatus int
	}{
		{name: "wrong suffix", method: http.MethodGet, path: constants.APIPaths.EvalCampaignPublicationStateByRun + runID, wantStatus: http.StatusNotFound},
		{name: "missing run id", method: http.MethodGet, path: constants.EvalCampaignPublicationStatePath(""), wantStatus: http.StatusBadRequest},
		{name: "delete ok", method: http.MethodDelete, path: path, wantStatus: http.StatusOK},
		{name: "invalid json body", method: http.MethodPut, path: path, body: []byte("{"), wantStatus: http.StatusBadRequest},
		{
			name:       "run id mismatch",
			method:     http.MethodPut,
			path:       path,
			body:       mustJSON(t, models.EvalCampaignPublicationState{RunID: "other-run"}),
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, test.path, bytes.NewReader(test.body))
			rec := httptest.NewRecorder()
			controller.handlePublicationState(rec, req)
			assert.Equal(t, test.wantStatus, rec.Code)
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	return payload
}

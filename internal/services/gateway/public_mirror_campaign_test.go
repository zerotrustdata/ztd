//go:build integration

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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestMirrorWithdrawCampaignDatasetHidesHistoryAndBootstrap(t *testing.T) {
	env := newMirrorTestEnv(t)
	runID := "eval-init-glm-5-3-air-1790258676"
	datasetID := campaignMirrorDatasetID(runID)
	batch := env.buildBatch([]models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
			"kind":           "evaluation_summary",
			"dataset_id":     datasetID,
			"run_id":         runID,
			"schema_version": "1.5.0",
		})),
	}, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(batch)
	require.True(t, response.Accepted)
	sourceID := batch.SourceID
	assert.Equal(t, constants.PublicFeedZeroHashHex, env.mirror.getSnapshotForSSE(sourceID).WithdrawalDigest)
	subscriber, accepted := env.mirror.addSSESubscriber(sourceID)
	require.True(t, accepted)
	t.Cleanup(func() { env.mirror.removeSSESubscriber(subscriber) })

	require.NoError(t, env.mirror.WithdrawCampaignDataset(context.Background(), runID))
	assert.True(t, env.mirror.CampaignDatasetWithdrawn(runID))
	select {
	case <-subscriber.resync:
	default:
		t.Fatal("withdrawal did not ask open streams to resync")
	}

	bootstrapReq := httptest.NewRequest(http.MethodGet, "/bootstrap", nil)
	bootstrapRec := httptest.NewRecorder()
	env.mirror.handleBootstrap(bootstrapRec, bootstrapReq)
	require.Equal(t, http.StatusOK, bootstrapRec.Code)
	var bootstrap models.PublicFeedBootstrap
	require.NoError(t, json.Unmarshal(bootstrapRec.Body.Bytes(), &bootstrap))
	assert.Empty(t, bootstrap.RecentProjections)
	assert.Regexp(t, `^[0-9a-f]{64}$`, bootstrap.Snapshot.WithdrawalDigest)
	assert.NotEqual(t, constants.PublicFeedZeroHashHex, bootstrap.Snapshot.WithdrawalDigest)

	snapshotRec := httptest.NewRecorder()
	env.mirror.handleSnapshot(snapshotRec, httptest.NewRequest(http.MethodGet, "/snapshot", nil))
	require.Equal(t, http.StatusOK, snapshotRec.Code)
	var snapshot models.PublicFeedSnapshot
	require.NoError(t, json.Unmarshal(snapshotRec.Body.Bytes(), &snapshot))
	assert.Equal(t, bootstrap.Snapshot.WithdrawalDigest, snapshot.WithdrawalDigest)

	historyReq := httptest.NewRequest(http.MethodGet, "/history?limit=10", nil)
	historyRec := httptest.NewRecorder()
	env.mirror.handleHistory(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code)
	var history models.PublicFeedCursorPage
	require.NoError(t, json.Unmarshal(historyRec.Body.Bytes(), &history))
	assert.Empty(t, history.Items)
}

func TestMirrorProjectionMatchesCampaignRun(t *testing.T) {
	runID := "eval-init-qwen3-4b-1789000001"
	item := models.NewPublicFeedObject(map[string]string{
		"idempotency_key": runID + ":aggregate:catalog:s75:t0",
	})
	assert.True(t, mirrorProjectionMatchesCampaignRun(item, runID))
	assert.False(t, mirrorProjectionMatchesCampaignRun(item, "other-run"))
}

func TestEvalCampaignPublicationControllerDeleteWithdrawsRunFromMirror(t *testing.T) {
	env := newMirrorTestEnv(t)
	runID := "eval-init-smollm2-135m-1791934107"
	batch := env.buildBatch([]models.PublicFeedRecord{
		env.makeRecord(1, models.NewPublicFeedObject(map[string]string{
			"kind":           "evaluation_summary",
			"dataset_id":     campaignMirrorDatasetID(runID),
			"run_id":         runID,
			"schema_version": "1.5.0",
		})),
	}, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(batch)
	require.True(t, response.Accepted)

	controller := newEvalCampaignPublicationControllerTest(t)
	controller.spectator = func() *PublicSpectatorRuntime { return &PublicSpectatorRuntime{mirror: env.mirror} }
	path := constants.APIPaths.EvalCampaignPublicationStateByRun + runID + "/publication-state"
	putRec := httptest.NewRecorder()
	controller.handlePublicationState(putRec, httptest.NewRequest(http.MethodPut, path, bytes.NewReader(mustJSON(t, models.EvalCampaignPublicationState{
		RunID:                runID,
		PublishedIdempotency: []string{"run:key-a"},
	}))))
	require.Equal(t, http.StatusOK, putRec.Code)

	deleteRec := httptest.NewRecorder()
	controller.handlePublicationState(deleteRec, httptest.NewRequest(http.MethodDelete, path, nil))
	require.Equal(t, http.StatusOK, deleteRec.Code)
	assert.True(t, env.mirror.CampaignDatasetWithdrawn(runID))

	getRec := httptest.NewRecorder()
	controller.handlePublicationState(getRec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, getRec.Code)
	var state models.EvalCampaignPublicationState
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &state))
	assert.Empty(t, state.PublishedIdempotency)
}

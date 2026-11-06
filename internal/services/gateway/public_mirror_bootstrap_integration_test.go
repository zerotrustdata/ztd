//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

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
	"github.com/zerotrustdata/ztd/internal/models"
)

func TestPublicMirrorBootstrap_OmitsAssignmentResultsAndKeepsSnapshotRows(t *testing.T) {
	ctx := context.Background()
	env := newMirrorTestEnv(t)
	runID := "run-1"
	datasetID := campaignMirrorDatasetID(runID)

	lifecycleBody, err := json.Marshal(map[string]any{
		"schema_version":  "1.0.0",
		"message_type":    "PublicAssignmentLifecycleRecord",
		"idempotency_key": runID + ":assignment-queued:lifecycle",
		"record": map[string]string{
			"run_id":           runID,
			"assignment_id":    "assignment-queued",
			"scenario_id":      "task-alpha@1.0.0",
			"lifecycle_status": "EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED",
		},
	})
	require.NoError(t, err)
	lifecycle := taskIndexTestRawRecord(1, lifecycleBody)
	summary := env.makeRecord(2, models.NewPublicFeedObject(map[string]string{
		"kind":           "evaluation_summary",
		"dataset_id":     datasetID,
		"run_id":         runID,
		"schema_version": "1.5.0",
	}))
	olderResult := taskIndexTestResultRecord(t, 3, "assignment-a", "task-alpha@1.0.0", "2026-09-01T00:00:00Z")
	newerResult := taskIndexTestResultRecord(t, 4, "assignment-c", "task-alpha@1.0.1", "2026-09-02T00:00:00Z")
	require.NotEmpty(t, olderResult.RecordHash)
	records := []models.PublicFeedRecord{lifecycle, summary, olderResult, newerResult}

	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	_, response := env.sendIngest(batch)
	require.True(t, response.Accepted)

	var bootstrap models.PublicFeedBootstrap
	require.Equal(t, http.StatusOK, env.getJSON("/bootstrap?source="+env.sourceID, &bootstrap))
	for _, item := range bootstrap.RecentProjections {
		messageType, _ := item.StringField("message_type")
		assert.NotEqual(t, publicSnapshotAssignmentResultKind, messageType, "bootstrap must not carry assignment results")
	}
	kinds := make(map[string]bool)
	for _, item := range bootstrap.RecentProjections {
		if kind, ok := item.StringField("kind"); ok {
			kinds[kind] = true
		}
		if messageType, ok := item.StringField("message_type"); ok {
			kinds[messageType] = true
		}
	}
	assert.True(t, kinds["PublicAssignmentLifecycleRecord"], "queued lifecycle row stays in bootstrap")
	assert.True(t, kinds["evaluation_summary"], "summary row stays in bootstrap")
	require.Len(t, bootstrap.RecentProjections, 2)

	require.Len(t, bootstrap.TaskIndex, 1)
	assert.Equal(t, "task-alpha", bootstrap.TaskIndex[0].TaskID)
	require.Len(t, bootstrap.TaskIndex[0].Results, 2)
	assert.Equal(t, "assignment-c", bootstrap.TaskIndex[0].Results[0].AssignmentID)
	assert.Equal(t, "assignment-a", bootstrap.TaskIndex[0].Results[1].AssignmentID)
	assert.Equal(t, runID, bootstrap.TaskIndex[0].Results[0].RunID)

	storedRecords, err := env.store.snapshotRecords(ctx, env.sourceID)
	require.NoError(t, err)
	storedResults := 0
	for _, record := range storedRecords {
		isResult, err := isPublicAssignmentResultRecord(record)
		require.NoError(t, err)
		if isResult {
			storedResults++
		}
	}
	assert.Equal(t, 2, storedResults, "snapshot rows keep the assignment results")
	assert.Len(t, storedRecords, 4, "snapshot keeps lifecycle, summary and both results")

	historyRec := httptest.NewRecorder()
	env.mirror.handleHistory(historyRec, httptest.NewRequest(http.MethodGet, "/history?limit=10", nil))
	require.Equal(t, http.StatusOK, historyRec.Code)
	var history models.PublicFeedCursorPage
	require.NoError(t, json.Unmarshal(historyRec.Body.Bytes(), &history))
	historyResults := 0
	for _, item := range history.Items {
		if messageType, _ := item.StringField("message_type"); messageType == publicSnapshotAssignmentResultKind {
			historyResults++
		}
	}
	assert.Equal(t, 2, historyResults, "history still returns the assignment results")
}

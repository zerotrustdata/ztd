// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

//go:build integration

package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestMirror_BootstrapKeepsCurrentStateAcrossRetention(t *testing.T) {
	env := newMirrorTestEnv(t)
	require.NoError(t, env.mirror.SetMaxRetainedBatches(1))
	previous := constants.PublicFeedZeroHashHex
	for i, dataset := range []string{"run-a", "run-b", "run-b"} {
		record := env.makeRecord(int64(i+1), models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": dataset, "title": fmt.Sprint(i)}))
		batch := env.buildBatch([]models.PublicFeedRecord{record}, previous)
		require.NoError(t, env.mirror.acceptBatch(context.Background(), batch))
		previous = batch.ContentHash
	}
	mirror := env.reopenMirror()
	response := httptest.NewRecorder()
	mirror.PublicHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/bootstrap", nil))
	var bootstrap models.PublicFeedBootstrap
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &bootstrap))
	require.Equal(t, int64(3), bootstrap.Snapshot.HighWaterSequence)
	require.Len(t, bootstrap.RecentProjections, 2)
	assert.Equal(t, "3", string(bootstrap.RecentProjections[0]["sequence"]))
}

func TestMirror_SnapshotRetainsCurrentEntitiesInsteadOfRevisions(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	previous := constants.PublicFeedZeroHashHex
	for i := range 120 {
		records := []models.PublicFeedRecord{
			env.makeRecord(int64(i*2+1), models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "ds-live-a"})),
			env.makeRecord(int64(i*2+2), models.NewPublicFeedObject(map[string]string{"kind": "stage_updated", "dataset_id": "ds-live-a", "event_id": fmt.Sprintf("event-%d", i)})),
		}
		batch := env.buildBatch(records, previous)
		require.NoError(t, env.mirror.acceptBatch(ctx, batch))
		previous = batch.ContentHash
	}
	bootstrap, err := env.mirror.currentBootstrap(ctx, env.sourceID)
	require.NoError(t, err)
	assert.Len(t, bootstrap.RecentProjections, constants.PublicFeedSSEReplayMaxRecords+1)
	var count int
	require.NoError(t, env.store.db.QueryRowWithRetry(ctx, `SELECT COUNT(*) FROM snapshot_batches`).Scan(&count))
	assert.Equal(t, constants.PublicFeedSSEReplayMaxRecords, count, "superseded batches have no snapshot references")
	require.NoError(t, env.mirror.WithdrawCampaignDataset(ctx, "a"))
	bootstrap, err = env.mirror.currentBootstrap(ctx, env.sourceID)
	require.NoError(t, err)
	assert.Empty(t, bootstrap.RecentProjections)
}

func TestMirror_SnapshotUpgradeAndCorruption(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"identity", `UPDATE snapshot_records SET dataset_id = 'forged'`},
		{"bytes", `UPDATE snapshot_records SET record_bytes = '{}'`},
		{"missing current record", `DELETE FROM snapshot_records`},
		{"cursor", `UPDATE snapshot_heads SET high_water_sequence = 999`},
		{"signature", `UPDATE snapshot_batches SET batch_json = json_set(batch_json, '$.signature', '00')`},
		{"missing initialization marker", `DELETE FROM snapshot_heads`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			ctx := context.Background()
			batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "run-a"}))}, constants.PublicFeedZeroHashHex)
			require.NoError(t, env.mirror.acceptBatch(ctx, batch))
			require.NoError(t, env.store.db.ExecInTxWithRetry(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, tc.mutation); return err }))
			require.NoError(t, env.store.Close())
			store, err := OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())
			if store != nil {
				_ = store.Close()
			}
			require.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
		})
	}
}

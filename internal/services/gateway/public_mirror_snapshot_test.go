// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

//go:build integration

package gateway

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

func TestMirror_SnapshotBackfillsVerifiedLegacyHistory(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	batch := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "run-a"}))}, constants.PublicFeedZeroHashHex)
	require.NoError(t, env.mirror.acceptBatch(ctx, batch))
	// Simulate a pre-snapshot schema without touching the retained signed chain.
	_, err := env.store.db.ExecWithRetry(ctx, `DROP TABLE snapshot_records; DROP TABLE snapshot_batches; DROP TABLE snapshot_heads; PRAGMA user_version = 0`)
	require.NoError(t, err)
	mirror := env.reopenMirror()
	bootstrap, err := mirror.currentBootstrap(ctx, env.sourceID)
	require.NoError(t, err)
	require.Len(t, bootstrap.RecentProjections, 1)
	assert.Equal(t, int64(1), bootstrap.Snapshot.HighWaterSequence)
}

func TestMirror_SnapshotFailureRollsBackFeedCursor(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	first := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "run-a"}))}, constants.PublicFeedZeroHashHex)
	require.NoError(t, env.mirror.acceptBatch(ctx, first))
	_, err := env.store.db.ExecWithRetry(ctx, `CREATE TRIGGER fail_snapshot BEFORE INSERT ON snapshot_records BEGIN SELECT RAISE(ABORT, 'injected snapshot failure'); END`)
	require.NoError(t, err)
	second := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "run-b"}))}, first.ContentHash)
	require.Error(t, env.mirror.acceptBatch(ctx, second))
	bootstrap, err := env.mirror.currentBootstrap(ctx, env.sourceID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), bootstrap.Snapshot.HighWaterSequence)
	assert.Len(t, bootstrap.RecentProjections, 1)
	summary, found, err := env.store.SourceSummary(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), summary.HighWaterSequence)
}

func TestMirror_SnapshotVerificationAcrossPages(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := t.Context()
	require.NoError(t, env.mirror.SetMaxRetainedBatches(1))
	previous := constants.PublicFeedZeroHashHex
	for i := range publicMirrorReadPageRecords + 1 {
		seq := int64(i*3 + 1)
		records := []models.PublicFeedRecord{
			env.makeRecord(seq, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": fmt.Sprintf("ds-%04d", i)})),
			env.makeRecord(seq+1, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "live", "title": fmt.Sprint(i)})),
			env.makeRecord(seq+2, models.NewPublicFeedObject(map[string]string{"kind": "stage_updated", "dataset_id": "live", "event_id": fmt.Sprintf("event-%04d", i)})),
		}
		batch := env.buildBatch(records, previous)
		require.NoError(t, env.mirror.acceptBatch(ctx, batch))
		previous = batch.ContentHash
	}
	mirror := env.reopenMirror()
	bootstrap, err := mirror.currentBootstrap(ctx, env.sourceID)
	require.NoError(t, err)
	assert.Len(t, bootstrap.RecentProjections, publicMirrorReadPageRecords+2+constants.PublicFeedSSEReplayMaxRecords)
	// Even a row with a self-consistent hash must match the signed record.
	raw := `{"kind":"catalog_snapshot","dataset_id":"ds-0256","title":"forged"}`
	digest := sha256.Sum256([]byte(raw))
	_, err = mirror.store.db.ExecWithRetry(ctx, `UPDATE snapshot_records SET record_bytes = ?, record_hash = ? WHERE dataset_id = ?`, raw, hex.EncodeToString(digest[:]), "ds-0256")
	require.NoError(t, err)
	state, err := mirror.store.loadState(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, mirror.store.verifyPublicSnapshots(ctx, state.keys), constants.ErrPublicFeedMirrorStoreCorrupt)
}

func TestPublicMirrorStore_VerifiesFeedAcrossPages(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := t.Context()
	previous := constants.PublicFeedZeroHashHex
	for i := range publicMirrorVerifyPageBatches + 1 {
		seq := int64(i + 1)
		record := env.makeRecord(seq, models.NewPublicFeedObject(map[string]string{"kind": "catalog_snapshot", "dataset_id": "live", "title": fmt.Sprint(i)}))
		batch := env.buildBatch([]models.PublicFeedRecord{record}, previous)
		require.NoError(t, env.mirror.acceptBatch(ctx, batch))
		previous = batch.ContentHash
	}
	require.NoError(t, env.store.verify(ctx))
	for _, seq := range []int64{publicMirrorReadPageRecords, publicMirrorReadPageRecords + 1, publicMirrorVerifyPageBatches + 1} {
		records, err := env.store.recordsInRange(ctx, env.sourceID, seq, seq)
		require.NoError(t, err)
		require.Len(t, records, 1)
		_, err = env.store.db.ExecWithRetry(ctx, `DELETE FROM records WHERE source_id = ? AND sequence = ?`, env.sourceID, seq)
		require.NoError(t, err)
		require.ErrorIs(t, env.store.verify(ctx), constants.ErrPublicFeedMirrorStoreCorrupt)
		r := records[0]
		_, err = env.store.db.ExecWithRetry(ctx, `INSERT INTO records (source_id, sequence, record_type, record_hash, record_bytes) VALUES (?, ?, ?, ?, ?)`, env.sourceID, r.Sequence, r.RecordType, r.RecordHash, r.RecordBytes)
		require.NoError(t, err)
	}
}

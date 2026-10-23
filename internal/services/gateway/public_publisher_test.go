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
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/assignmentaudit"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newPublicPublisherTestEnv builds a real in-memory SQLite-backed
// PublicPublisherService with a temp-rooted RuntimeFileService, a generated
// Ed25519 signing key, and a test export config. The mirror is a test HTTP
// server that the caller can configure.
func newPublicPublisherTestEnv(t *testing.T) (*PublicPublisherService, *DocumentStoreService, ed25519.PrivateKey, string) {
	t.Helper()
	logger := testutil.NewTestLogger()
	cfg := sqliteutil.DefaultDBConfig(":memory:")
	db, err := sqliteutil.OpenDB(cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec(gatewaySchema)
	require.NoError(t, err)

	docStore := NewDocumentStoreService(db, logger)
	fileSvc := newProducerFileSvc(t)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyID := "test-key-1"

	exportCfg := models.PublicExportConfig{
		Enabled:                 true,
		MirrorOrigin:            "http://test-mirror.invalid",
		SourceID:                "test-source-1",
		SigningKeyID:            keyID,
		BatchMaxRecords:         constants.PublicFeedBatchMaxRecords,
		BatchMaxBytes:           constants.PublicFeedBatchMaxBytes,
		RetryMaxAttempts:        constants.PublicFeedRetryMaxAttempts,
		RetryInitialBackoffSecs: 0,
		RetryMaxBackoffSecs:     1,
		AckWindowSecs:           constants.PublicFeedAckWindowSeconds,
	}

	publisher := NewPublicPublisherService(docStore, fileSvc, logger, exportCfg, priv, keyID)
	return publisher, docStore, priv, hex.EncodeToString(pub)
}

// makeProjectionRecord creates a valid public feed record from a projection object.
func makeProjectionRecord(t *testing.T, seq int64, proj models.PublicFeedObject) models.PublicFeedRecord {
	t.Helper()
	return projectionRecordFromObject(t, seq, proj)
}

func validPublicViewRecordBytes() string {
	return `{"schema_version":"1.3.0","kind":"catalog_snapshot","dataset_id":"test-dataset","quality_state":"live_in_progress","observed_at":"2026-09-21T00:00:00Z"}`
}

// TestExportBatch_SignsAndWritesOutbox verifies that ExportBatch builds a
// signed batch from records, writes it to the durable outbox, and sends it
// to the mirror. The batch must carry a valid Ed25519 signature over the
// content hash.
func TestExportBatch_SendsConfiguredIngestToken(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	publisher.SetIngestAuthToken("mirror-token")
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer mirror-token", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{Accepted: true}))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	require.NoError(t, publisher.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-1"})),
	}))
}

func TestBuildBatch_RejectsEventBodyOutsideExplorerContract(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	recordBytes := []byte(`{"kind":"heartbeat"}`)
	digest := sha256.Sum256(recordBytes)

	_, err := publisher.BuildBatch([]models.PublicFeedRecord{{
		Sequence:    1,
		RecordType:  models.PublicFeedRecordTypeEvent,
		RecordHash:  hex.EncodeToString(digest[:]),
		RecordBytes: string(recordBytes),
	}})

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedRecordSchemaInvalid)
}

func TestExportBatch_SignsAndWritesOutbox(t *testing.T) {
	publisher, _, _, pubKeyHex := newPublicPublisherTestEnv(t)

	// Set up a test mirror that accepts the batch.
	var receivedBatch models.PublicFeedBatch
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedBatch = req.Batch
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v1"})),
		makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1", "variant_id": "v2"})),
	}

	err := publisher.ExportBatch(context.Background(), records)
	require.NoError(t, err)

	// The mirror received a valid batch.
	assert.Equal(t, int64(1), receivedBatch.FirstSequence)
	assert.Equal(t, int64(2), receivedBatch.LastSequence)
	assert.Equal(t, constants.PublicFeedZeroHashHex, receivedBatch.PreviousBatchHash)
	assert.Len(t, receivedBatch.RecordHashes, 2)
	assert.NotEmpty(t, receivedBatch.ContentHash)
	assert.NotEmpty(t, receivedBatch.Signature)

	// Verify the signature is valid.
	contentHashBytes, err := hex.DecodeString(receivedBatch.ContentHash)
	require.NoError(t, err)
	sigBytes, err := hex.DecodeString(receivedBatch.Signature)
	require.NoError(t, err)
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(pubKeyBytes, contentHashBytes, sigBytes), "signature must be valid")

	// The outbox has the entry persisted.
	entries, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1, "outbox must have one entry")
}

// TestExportBatch_HashChainLinksBatches verifies that the second batch
// carries the content hash of the first as its previous_batch_hash.
func TestExportBatch_HashChainLinksBatches(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	var firstBatch, secondBatch models.PublicFeedBatch
	callCount := atomic.Int32{}
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if callCount.Add(1) == 1 {
			firstBatch = req.Batch
		} else {
			secondBatch = req.Batch
		}
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records1 := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records1)
	require.NoError(t, err)

	records2 := []models.PublicFeedRecord{makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err = publisher.ExportBatch(context.Background(), records2)
	require.NoError(t, err)

	// The second batch's previous_batch_hash must equal the first batch's content_hash.
	assert.Equal(t, firstBatch.ContentHash, secondBatch.PreviousBatchHash)
	assert.Equal(t, int64(2), secondBatch.FirstSequence)
	assert.Equal(t, int64(2), secondBatch.LastSequence)
}

// TestExportBatch_RejectsEmptyBatch verifies that an empty batch is rejected.
func TestExportBatch_RejectsEmptyBatch(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	err := publisher.ExportBatch(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedBatchEmpty)
}

// TestExportBatch_RetriesOnMirrorFailure verifies that the publisher retries
// when the mirror is unreachable and eventually succeeds when the mirror
// comes back.
func TestExportBatch_RetriesOnMirrorFailure(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	var accepted atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accepted.Add(1) <= 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, accepted.Load(), int32(2), "must have retried at least once")
}

// TestExportBatch_RejectsMirrorTipThatConflictsWithPublisherSnapshot verifies
// that a different accepted hash at the publisher's next sequence fails closed.
func TestExportBatch_RejectsMirrorTipThatConflictsWithPublisherSnapshot(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	remoteTipHash := strings.Repeat("a", sha256.Size*2)
	var firstBatch models.PublicFeedBatch
	var receivedSequences []int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedSequences = append(receivedSequences, req.Batch.FirstSequence)
		if len(receivedSequences) == 1 {
			firstBatch = req.Batch
			require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{Accepted: true, HighWaterSequence: 1, FeedChainHash: req.Batch.ContentHash}))
			return
		}
		require.Equal(t, int64(2), req.Batch.FirstSequence)
		require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{
			Accepted:          false,
			SourceID:          req.Batch.SourceID,
			HighWaterSequence: 2,
			FeedChainHash:     remoteTipHash,
			BatchCount:        2,
			RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
		}))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	require.NoError(t, publisher.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-1"})),
	}))
	err := publisher.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-2"})),
	})
	require.ErrorIs(t, err, constants.ErrPublicFeedMirrorRejected)
	assert.Equal(t, []int64{1, 2}, receivedSequences)
	snapshot, snapshotErr := publisher.GetSnapshot(context.Background())
	require.NoError(t, snapshotErr)
	assert.Equal(t, int64(1), snapshot.HighWaterSequence)
	assert.Equal(t, firstBatch.ContentHash, snapshot.FeedChainHash)
}

func TestSendToMirror_RejectsUnverifiedMirrorState(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	batch, err := publisher.BuildBatch([]models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-1"})),
	})
	require.NoError(t, err)

	tests := []struct {
		name     string
		response models.PublicIngestResponse
	}{
		{
			name: "different source",
			response: models.PublicIngestResponse{
				SourceID:          "other-source",
				HighWaterSequence: batch.LastSequence,
				FeedChainHash:     batch.ContentHash,
				BatchCount:        1,
				RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
			},
		},
		{
			name: "newer mirror tip",
			response: models.PublicIngestResponse{
				SourceID:          batch.SourceID,
				HighWaterSequence: batch.LastSequence + 1,
				FeedChainHash:     strings.Repeat("a", sha256.Size*2),
				BatchCount:        2,
				RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
			},
		},
		{
			name: "equal sequence with different hash",
			response: models.PublicIngestResponse{
				SourceID:          batch.SourceID,
				HighWaterSequence: batch.LastSequence,
				FeedChainHash:     strings.Repeat("b", sha256.Size*2),
				BatchCount:        1,
				RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
			},
		},
		{
			name: "malformed tip hash",
			response: models.PublicIngestResponse{
				SourceID:          batch.SourceID,
				HighWaterSequence: batch.LastSequence + 1,
				FeedChainHash:     "invalid",
				BatchCount:        2,
				RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
			},
		},
		{
			name: "missing batch count",
			response: models.PublicIngestResponse{
				SourceID:          batch.SourceID,
				HighWaterSequence: batch.LastSequence + 1,
				FeedChainHash:     strings.Repeat("c", sha256.Size*2),
				RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
			},
		},
		{
			name: "non-duplicate rejection",
			response: models.PublicIngestResponse{
				SourceID:          batch.SourceID,
				HighWaterSequence: batch.LastSequence,
				FeedChainHash:     batch.ContentHash,
				BatchCount:        1,
				RejectionReason:   models.PublicFeedIngestRejectionEquivocation,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				require.NoError(t, json.NewEncoder(w).Encode(tt.response))
			}))
			t.Cleanup(mirror.Close)
			err := publisher.sendToMirror(context.Background(), mirror.URL, "", batch)
			require.ErrorIs(t, err, constants.ErrPublicFeedMirrorRejected)
		})
	}
}

func TestExportBatch_RecoversWhenMirrorAcceptedBatchBeforeResponseFailure(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	var accepted models.PublicFeedBatch
	attempts := atomic.Int32{}
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		if attempts.Add(1) == 1 {
			accepted = req.Batch
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		require.Equal(t, accepted.ContentHash, req.Batch.ContentHash)
		require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{
			Accepted:          false,
			SourceID:          accepted.SourceID,
			HighWaterSequence: accepted.LastSequence,
			FeedChainHash:     accepted.ContentHash,
			RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
		}))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	err := publisher.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-1"})),
	})

	require.NoError(t, err)
	assert.Equal(t, int32(2), attempts.Load())
	snapshot, err := publisher.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, accepted.LastSequence, snapshot.HighWaterSequence)
	assert.Equal(t, accepted.ContentHash, snapshot.FeedChainHash)
}

func TestRetransmitOutbox_AcknowledgesBatchAlreadyAcceptedByMirror(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "campaign-1"}))}
	batch, err := publisher.BuildBatch(records)
	require.NoError(t, err)
	require.NoError(t, publisher.writeOutboxEntry(context.Background(), batch))
	require.NoError(t, publisher.updateOutboxStatus(context.Background(), batch.LastSequence, models.PublicFeedOutboxStatusSent, true))
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, batch.ContentHash, req.Batch.ContentHash)
		require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{
			Accepted:          false,
			SourceID:          batch.SourceID,
			HighWaterSequence: batch.LastSequence,
			FeedChainHash:     batch.ContentHash,
			RejectionReason:   models.PublicFeedIngestRejectionDuplicateSequence,
		}))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	require.NoError(t, publisher.RetransmitOutbox(context.Background()))
	snapshot, err := publisher.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, batch.LastSequence, snapshot.HighWaterSequence)
	assert.Equal(t, batch.ContentHash, snapshot.FeedChainHash)
}

func TestExportBatch_IdempotentRetransmit(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	var receivedHashes []string
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedHashes = append(receivedHashes, req.Batch.ContentHash)
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch, err := publisher.BuildBatch(records)
	require.NoError(t, err)
	require.NoError(t, publisher.writeOutboxEntry(context.Background(), batch))
	require.NoError(t, publisher.updateOutboxStatus(context.Background(), batch.LastSequence, models.PublicFeedOutboxStatusSent, true))

	err = publisher.RetransmitOutbox(context.Background())
	require.NoError(t, err)

	require.Len(t, receivedHashes, 1)
}

// TestLoadSnapshotFromOutbox_IgnoresUnacknowledgedTail verifies that only
// mirror-acknowledged outbox entries advance the publisher high-water mark.
func TestLoadSnapshotFromOutbox_IgnoresUnacknowledgedTail(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records1 := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	require.NoError(t, publisher.ExportBatch(context.Background(), records1))

	records2 := []models.PublicFeedRecord{makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c2"}))}
	batch2, err := publisher.BuildBatch(records2)
	require.NoError(t, err)
	require.NoError(t, publisher.writeOutboxEntry(context.Background(), batch2))
	require.NoError(t, publisher.updateOutboxStatus(context.Background(), batch2.LastSequence, models.PublicFeedOutboxStatusSent, true))

	publisher.mu.Lock()
	publisher.highWaterSeq = batch2.LastSequence
	publisher.feedChainHash = batch2.ContentHash
	publisher.batchCount = 2
	publisher.mu.Unlock()

	require.NoError(t, publisher.loadSnapshotFromOutbox(context.Background()))

	publisher.mu.Lock()
	highWater := publisher.highWaterSeq
	publisher.mu.Unlock()
	assert.Equal(t, int64(1), highWater)
}

// TestExportBatch_RetriesUnacknowledgedBeforeNewBatch verifies that a sent
// but unacknowledged outbox tail is retransmitted before appending a new batch.
func TestExportBatch_RetriesUnacknowledgedBeforeNewBatch(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	var receivedSequences []int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedSequences = append(receivedSequences, req.Batch.FirstSequence)
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records1 := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	require.NoError(t, publisher.ExportBatch(context.Background(), records1))

	records2 := []models.PublicFeedRecord{makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c2"}))}
	batch2, err := publisher.BuildBatch(records2)
	require.NoError(t, err)
	require.NoError(t, publisher.writeOutboxEntry(context.Background(), batch2))
	require.NoError(t, publisher.updateOutboxStatus(context.Background(), batch2.LastSequence, models.PublicFeedOutboxStatusSent, true))

	records3 := []models.PublicFeedRecord{makeProjectionRecord(t, 3, models.NewPublicFeedObject(map[string]string{"campaign_id": "c3"}))}
	receivedSequences = nil
	require.NoError(t, publisher.ExportBatch(context.Background(), records3))

	assert.Equal(t, []int64{2, 3}, receivedSequences)

	snap, err := publisher.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), snap.HighWaterSequence)
}

// TestRepairOutboxFromSnapshot_CompactsPrefixGap verifies that a non-contiguous
// outbox prefix is compacted back to the mirror-acknowledged snapshot tip.
func TestRepairOutboxFromSnapshot_CompactsPrefixGap(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records1 := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	require.NoError(t, publisher.ExportBatch(context.Background(), records1))

	records2 := []models.PublicFeedRecord{makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c2"}))}
	require.NoError(t, publisher.ExportBatch(context.Background(), records2))

	entries, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)

	orphanRecord := makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "orphan"}))
	orphanBatch, err := publisher.BuildBatch([]models.PublicFeedRecord{orphanRecord})
	require.NoError(t, err)
	orphanBytes, err := json.Marshal(orphanBatch)
	require.NoError(t, err)
	orphanEntry := models.PublicOutboxEntry{
		Sequence:   orphanBatch.LastSequence,
		BatchHash:  orphanBatch.ContentHash,
		BatchBytes: string(orphanBytes),
		Status:     models.PublicFeedOutboxStatusFailed,
		CreatedAt:  entries[0].CreatedAt,
	}

	rawStore := publisher.outbox.(*runtimePublicOutboxStore)
	require.NoError(t, rawStore.Replace(context.Background(), []models.PublicOutboxEntry{orphanEntry, entries[1]}))

	_, err = publisher.outbox.List(context.Background())
	require.ErrorIs(t, err, constants.ErrPublicFeedHashChainMismatch)

	require.NoError(t, publisher.RepairOutboxFromSnapshot(context.Background()))

	repaired, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, repaired)
}

func TestRepairOutboxFromSnapshot_RetransmitsMultiRecordTail(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	var accepted atomic.Int64
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		accepted.Store(req.Batch.LastSequence)
		require.NoError(t, json.NewEncoder(w).Encode(models.PublicIngestResponse{
			Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash,
		}))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)
	require.NoError(t, publisher.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "first"})),
	}))
	batch, err := publisher.BuildBatch([]models.PublicFeedRecord{
		makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "second"})),
		makeProjectionRecord(t, 3, models.NewPublicFeedObject(map[string]string{"campaign_id": "third"})),
	})
	require.NoError(t, err)
	require.NoError(t, publisher.writeOutboxEntry(context.Background(), batch))
	require.NoError(t, publisher.RepairOutboxFromSnapshot(context.Background()))
	entries, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1, "the pending multi-record batch must survive recovery")
	assert.Equal(t, int64(3), entries[0].Sequence)
	require.NoError(t, publisher.RetransmitOutbox(context.Background()))
	assert.Equal(t, int64(3), accepted.Load())
	snapshot, err := publisher.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(3), snapshot.HighWaterSequence)
	assert.Equal(t, batch.ContentHash, snapshot.FeedChainHash)
}

func TestRepairOutboxFromSnapshot_DropsDiscontinuousTail(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	snapshot := models.PublicFeedSnapshot{
		SourceID:          publisher.cfg.SourceID,
		HighWaterSequence: 10,
		FeedChainHash:     "abc123",
		BatchCount:        5,
	}
	snapBytes, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, publisher.fileSvc.WriteFile(context.Background(), constants.PublicFeedSnapshotPath, snapBytes, constants.PermFilePrivate))

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 15, models.NewPublicFeedObject(map[string]string{"campaign_id": "orphan-tail"}))}
	batch, err := publisher.BuildBatch(records)
	require.NoError(t, err)
	batchBytes, err := json.Marshal(batch)
	require.NoError(t, err)
	entry := models.PublicOutboxEntry{
		Sequence:   batch.LastSequence,
		BatchHash:  batch.ContentHash,
		BatchBytes: string(batchBytes),
		Status:     models.PublicFeedOutboxStatusPending,
		CreatedAt:  time.Now().UTC(),
	}
	rawStore := publisher.outbox.(*runtimePublicOutboxStore)
	require.NoError(t, rawStore.Replace(context.Background(), []models.PublicOutboxEntry{entry}))

	require.NoError(t, publisher.RepairOutboxFromSnapshot(context.Background()))

	repaired, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, repaired)
}

// TestExportBatch_RejectsMirrorRejection verifies that a mirror rejection
// (e.g. signature_invalid) causes the publisher to return an error.
func TestExportBatch_RejectsMirrorRejection(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := models.PublicIngestResponse{Accepted: false, RejectionReason: models.PublicFeedIngestRejectionSignatureInvalid}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorRejected)
}

// TestGetSnapshot_ReturnsCurrentHighWater verifies that GetSnapshot returns
// the current high-water sequence and feed-chain hash after batches are
// exported.
func TestGetSnapshot_ReturnsCurrentHighWater(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records)
	require.NoError(t, err)

	snap, err := publisher.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), snap.HighWaterSequence)
	assert.NotEmpty(t, snap.FeedChainHash)
	assert.Equal(t, 1, snap.BatchCount)
	assert.Equal(t, models.CampaignFreshnessActive, snap.Freshness)
}

// TestGetSnapshot_NotFoundWhenEmpty verifies that GetSnapshot returns
// ErrPublicFeedSnapshotNotFound when no batches have been exported.
func TestGetSnapshot_NotFoundWhenEmpty(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	_, err := publisher.GetSnapshot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedSnapshotNotFound)
}

// TestRecoverOutbox_ResumesAfterRestart verifies that the outbox survives a
// publisher restart and pending entries are retransmitted.
func TestRecoverOutbox_ResumesAfterRestart(t *testing.T) {
	publisher1, docStore, priv, _ := newPublicPublisherTestEnv(t)

	var accepted atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Add(1)
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher1.SetMirrorOrigin(mirror.URL)

	// Export a batch with the first publisher.
	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher1.ExportBatch(context.Background(), records)
	require.NoError(t, err)

	// Create a new publisher with the same docStore and key (simulating restart).
	exportCfg := models.PublicExportConfig{
		Enabled:                 true,
		MirrorOrigin:            mirror.URL,
		SourceID:                "test-source-1",
		SigningKeyID:            "test-key-1",
		BatchMaxRecords:         constants.PublicFeedBatchMaxRecords,
		BatchMaxBytes:           constants.PublicFeedBatchMaxBytes,
		RetryMaxAttempts:        constants.PublicFeedRetryMaxAttempts,
		RetryInitialBackoffSecs: 0,
		RetryMaxBackoffSecs:     1,
		AckWindowSecs:           constants.PublicFeedAckWindowSeconds,
	}
	logger := testutil.NewTestLogger()
	publisher2 := NewPublicPublisherService(docStore, publisher1.fileSvc, logger, exportCfg, priv, "test-key-1")
	publisher2.SetMirrorOrigin(mirror.URL)

	// The snapshot should still be available from the first publisher's state.
	snap, err := publisher2.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), snap.HighWaterSequence)
}

func TestRecoverOutbox_UsesRuntimeFileStateAfterDatabaseReplacement(t *testing.T) {
	publisher1, _, priv, _ := newPublicPublisherTestEnv(t)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(mirror.Close)
	publisher1.SetMirrorOrigin(mirror.URL)

	err := publisher1.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
	})
	require.NoError(t, err)

	logger := testutil.NewTestLogger()
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(":memory:"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(gatewaySchema)
	require.NoError(t, err)

	exportCfg := publisher1.cfg
	exportCfg.MirrorOrigin = mirror.URL
	publisher2 := NewPublicPublisherService(NewDocumentStoreService(db, logger), publisher1.fileSvc, logger, exportCfg, priv, "test-key-1")
	snap, err := publisher2.GetSnapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), snap.HighWaterSequence)
	assert.Equal(t, 1, snap.BatchCount)
}

func TestRecoverOutbox_RejectsCorruptRuntimeFile(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	require.NoError(t, publisher.fileSvc.WriteFile(context.Background(), constants.PublicFeedOutboxPath, []byte("{invalid\n"), constants.PermFilePrivate))

	_, err := publisher.GetSnapshot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedOutboxCorrupt)
}

func TestRecoverOutbox_RejectsSnapshotEquivocationAtHighWater(t *testing.T) {
	publisher1, docStore, priv, _ := newPublicPublisherTestEnv(t)
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		response := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(response))
	}))
	t.Cleanup(mirror.Close)
	publisher1.SetMirrorOrigin(mirror.URL)
	require.NoError(t, publisher1.ExportBatch(context.Background(), []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
	}))

	snapshotBytes, err := publisher1.fileSvc.ReadFile(context.Background(), constants.PublicFeedSnapshotPath)
	require.NoError(t, err)
	var snapshot models.PublicFeedSnapshot
	require.NoError(t, json.Unmarshal(snapshotBytes, &snapshot))
	snapshot.FeedChainHash = constants.PublicFeedZeroHashHex
	snapshotBytes, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, publisher1.fileSvc.WriteFile(context.Background(), constants.PublicFeedSnapshotPath, snapshotBytes, constants.PermFilePrivate))

	publisher2 := NewPublicPublisherService(docStore, publisher1.fileSvc, testutil.NewTestLogger(), publisher1.cfg, priv, "test-key-1")
	_, err = publisher2.GetSnapshot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedOutboxEquivocation)
}

// TestExportBatch_DisabledReturnsError verifies that exporting when the
// publisher is disabled returns ErrPublicFeedDisabled.
func TestExportBatch_DisabledReturnsError(t *testing.T) {
	logger := testutil.NewTestLogger()
	cfg := sqliteutil.DefaultDBConfig(":memory:")
	db, err := sqliteutil.OpenDB(cfg, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(gatewaySchema)
	require.NoError(t, err)
	docStore := NewDocumentStoreService(db, logger)
	fileSvc := newProducerFileSvc(t)
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	disabledCfg := models.PublicExportConfig{Enabled: false}
	publisher := NewPublicPublisherService(docStore, fileSvc, logger, disabledCfg, priv, "key-1")

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err = publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedDisabled)
}

// TestExportBatch_RejectsOversizedBatch verifies that a batch exceeding the
// max records limit is rejected.
func TestExportBatch_RejectsOversizedBatch(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	publisher.SetBatchMaxRecords(2)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{
		makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
		makeProjectionRecord(t, 2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
		makeProjectionRecord(t, 3, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"})),
	}
	err := publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedBatchOversized)
}

// TestBuildBatch_PublishesRecordFieldsVerbatim verifies that public records are
// never screened by field name: identity, transaction, and transcript fields
// are published as-is.
func TestBuildBatch_PublishesRecordFieldsVerbatim(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)
	body := []byte(`{"schema_version":"1.0.0","kind":"evaluation_summary","dataset_id":"d1","quality_state":"live_in_progress","observed_at":"2026-10-15T00:00:00Z","native_result":{"transaction_id":"tx-1","operator_id":"op-1","raw_prompt":"p","output":"o","token":"t"}}`)
	digest := sha256.Sum256(body)
	_, err := publisher.BuildBatch([]models.PublicFeedRecord{{Sequence: 1, RecordType: models.PublicFeedRecordTypeProjection, RecordHash: hex.EncodeToString(digest[:]), RecordBytes: string(body)}})
	require.NoError(t, err)
}

func TestExportBatch_RecordHashMismatch(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := models.PublicIngestResponse{Accepted: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{
		{
			Sequence:    1,
			RecordType:  models.PublicFeedRecordTypeProjection,
			RecordHash:  "0000000000000000000000000000000000000000000000000000000000000000",
			RecordBytes: `{"campaign_id":"c1"}`,
		},
	}
	err := publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedRecordHashMismatch)
}

// TestExportBatch_RejectsInvalidRecordType verifies that an invalid record
// type is rejected.
func TestExportBatch_RejectsInvalidRecordType(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := models.PublicIngestResponse{Accepted: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	recordBytes := validPublicViewRecordBytes()
	recordHash := sha256.Sum256([]byte(recordBytes))
	records := []models.PublicFeedRecord{
		{
			Sequence:    1,
			RecordType:  PublicFeedRecordTypeInvalid,
			RecordHash:  hex.EncodeToString(recordHash[:]),
			RecordBytes: recordBytes,
		},
	}
	err := publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedRecordTypeInvalid)
}

// TestVerifyBatch_SignatureValidation verifies that VerifyBatch accepts a
// valid signature and rejects a tampered signature.
func TestVerifyBatch_SignatureValidation(t *testing.T) {
	publisher, _, _, pubKeyHex := newPublicPublisherTestEnv(t)

	recordBytes := validPublicViewRecordBytes()
	recordHash := sha256.Sum256([]byte(recordBytes))
	records := []models.PublicFeedRecord{
		{
			Sequence:    1,
			RecordType:  models.PublicFeedRecordTypeProjection,
			RecordHash:  hex.EncodeToString(recordHash[:]),
			RecordBytes: recordBytes,
		},
	}

	batch, err := publisher.BuildBatch(records)
	require.NoError(t, err)

	// Valid signature.
	err = publisher.VerifyBatch(batch, pubKeyHex)
	require.NoError(t, err)

	// Tampered signature.
	tampered := batch
	tamperedSig, err := hex.DecodeString(batch.Signature)
	require.NoError(t, err)
	tamperedSig[0] ^= 0xFF
	tampered.Signature = hex.EncodeToString(tamperedSig)
	err = publisher.VerifyBatch(tampered, pubKeyHex)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedSignatureInvalid)
}

// TestVerifyBatch_HashChainValidation verifies that VerifyBatch rejects a
// batch whose content hash does not match the computed hash.
func TestVerifyBatch_HashChainValidation(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	recordBytes := validPublicViewRecordBytes()
	recordHash := sha256.Sum256([]byte(recordBytes))
	records := []models.PublicFeedRecord{
		{
			Sequence:    1,
			RecordType:  models.PublicFeedRecordTypeProjection,
			RecordHash:  hex.EncodeToString(recordHash[:]),
			RecordBytes: recordBytes,
		},
	}

	batch, err := publisher.BuildBatch(records)
	require.NoError(t, err)

	// Tampered content hash.
	tampered := batch
	tampered.ContentHash = constants.PublicFeedZeroHashHex
	err = publisher.VerifyBatch(tampered, hex.EncodeToString(publisher.signingPubKey))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedContentHashMismatch)
}

// TestRotateKey_SwitchesSigningKey verifies that RotateKey generates a new
// key pair, emits a key revocation record, and subsequent batches are signed
// with the new key.
func TestRotateKey_SwitchesSigningKey(t *testing.T) {
	publisher, _, _, oldPubKeyHex := newPublicPublisherTestEnv(t)

	var receivedKeyIDs []string
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/keys/register" {
			require.NoError(t, json.NewEncoder(w).Encode(models.PublicKeyRegistrationResponse{Accepted: true}))
			return
		}
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedKeyIDs = append(receivedKeyIDs, req.Batch.SigningKeyID)
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	// Export with the old key.
	records1 := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records1)
	require.NoError(t, err)

	// Rotate the key.
	newKeyID, newPubKeyHex, err := publisher.RotateKey(context.Background())
	require.NoError(t, err)
	assert.NotEqual(t, "test-key-1", newKeyID)
	assert.NotEqual(t, oldPubKeyHex, newPubKeyHex)

	// Export with the new key.
	records2 := []models.PublicFeedRecord{makeProjectionRecord(t, 3, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err = publisher.ExportBatch(context.Background(), records2)
	require.NoError(t, err)

	// The first batch used the old key, the second used the new key.
	require.Len(t, receivedKeyIDs, 3) // batch1 + revocation + batch2
	assert.Equal(t, "test-key-1", receivedKeyIDs[0])
	assert.Equal(t, "test-key-1", receivedKeyIDs[1]) // revocation record signed with old key
	assert.Equal(t, newKeyID, receivedKeyIDs[2])
}

// TestExportBatch_MirrorOutageDoesNotLoseSequence verifies that when the
// mirror is unreachable, the batch remains in the outbox and can be
// retransmitted later without losing sequence.
func TestExportBatch_MirrorOutageDoesNotLoseSequence(t *testing.T) {
	publisher, _, _, _ := newPublicPublisherTestEnv(t)

	// Point to an invalid URL so the mirror is unreachable.
	publisher.SetMirrorOrigin("http://127.0.0.1:1")

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err := publisher.ExportBatch(context.Background(), records)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedMaxRetriesExceeded)

	// The outbox has the pending entry.
	entries, err := publisher.outbox.List(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1, "outbox must retain the pending entry")
	assert.Equal(t, models.PublicFeedOutboxStatusFailed, entries[0].Status)
	assert.Equal(t, constants.PublicFeedRetryMaxAttempts, entries[0].Attempts)

	// Now set up a working mirror and retransmit.
	var accepted atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accepted.Add(1)
		var req models.PublicIngestRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		resp := models.PublicIngestResponse{Accepted: true, HighWaterSequence: req.Batch.LastSequence, FeedChainHash: req.Batch.ContentHash}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	err = publisher.RetransmitOutbox(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, accepted.Load(), int32(1))
}

// TestExportBatch_NeverMutatesAuthoritativeReports verifies that exporting
// a batch does not modify any existing eval projections or download
// artifacts. The export only reads persisted safe projections.
func TestExportBatch_NeverMutatesAuthoritativeReports(t *testing.T) {
	publisher, docStore, _, _ := newPublicPublisherTestEnv(t)

	// Persist an eval projection.
	evalCollection := marshaler.CollectionName(constants.CollectionObserveEvals)
	originalProj := evalProjection{
		UserID: "user-1",
		EvalDetail: models.EvalDetail{
			SchemaVersion: constants.ObserveAPIReadModelSchemaVersion,
			RunID:         "run-1",
			SuiteID:       "suite-1",
			SuiteVersion:  "1.0.0",
			CampaignID:    "campaign-1",
			Status:        models.RunLifecycleStatusCompleted,
		},
	}
	projBytes, err := json.Marshal(originalProj)
	require.NoError(t, err)
	err = docStore.DocSet(t.Context(), evalCollection, "run-1", projBytes)
	require.NoError(t, err)

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := models.PublicIngestResponse{Accepted: true}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(mirror.Close)
	publisher.SetMirrorOrigin(mirror.URL)

	records := []models.PublicFeedRecord{makeProjectionRecord(t, 1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	err = publisher.ExportBatch(context.Background(), records)
	require.NoError(t, err)

	// The eval projection is unchanged.
	doc, err := docStore.DocGet(t.Context(), evalCollection, "run-1")
	require.NoError(t, err)
	require.NotNil(t, doc)
	var afterProj evalProjection
	require.NoError(t, unmarshalDocData(doc, &afterProj))
	assert.Equal(t, originalProj.RunID, afterProj.RunID)
	assert.Equal(t, originalProj.Status, afterProj.Status)
}

// assignmentAuditRequest is a valid request for run-1 and the named assignment.
func assignmentAuditRequest(assignmentID string) models.PublicAssignmentAuditProofPublishRequest {
	return models.PublicAssignmentAuditProofPublishRequest{
		CampaignID:       "campaign-1",
		CampaignRevision: "rev-1",
		RunID:            "run-1",
		AssignmentID:     assignmentID,
		IndexDigest:      "digest-1",
	}
}

func TestAssignmentAuditProofPlan_AddsAgentTraceOnlyWhenCaptured(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	newProofPushTestMirror(t, publisher)
	seedAssignmentEvents(t, publisher, assignmentSeed{runID: "run-1", assignmentID: "assignment-1", events: 1})
	ctx := context.Background()
	request := assignmentAuditRequest("assignment-1")

	plan, err := publisher.planAssignmentAuditProof(ctx, request)
	require.NoError(t, err)
	require.Len(t, plan.artifacts, 1, "the database alone: no vault key is published")
	database := plan.artifacts[0]
	assert.Equal(t, "assignment-assignment-1.db", database.Filename)
	assert.Equal(t, "ztd public verify-assignment --db assignment-assignment-1.db --segments proof-segments.jsonl --public-key <trusted-ed25519-key-hex>", database.VerificationCommand)
	assert.Empty(t, plan.response.AgentTraceSHA256)

	request.AgentTrace = []byte(`{"evidence_digest":"x","final_output":"spiffe://ztd.local/operator/x"}`)
	plan, err = publisher.planAssignmentAuditProof(ctx, request)
	require.NoError(t, err)
	require.Len(t, plan.artifacts, 2)
	trace := plan.artifacts[1]
	assert.Equal(t, "assignment-assignment-1.agent-trace.json", trace.Filename)
	assert.Equal(t, constants.MediaTypeJSON, trace.MediaType)
	assert.Equal(t, request.AgentTrace, trace.Content, "the trace is published byte for byte, never screened")
	assert.Equal(t, "run-1", trace.SourceRunID)
	assert.Equal(t, "ztd public verify-agent-trace --trace assignment-assignment-1.agent-trace.json", trace.VerificationCommand)
	assert.Equal(t, hashBytes(request.AgentTrace), plan.response.AgentTraceSHA256)
}

func TestAssignmentAuditProofPlan_PublishesScenarioFixtureCampaignScoped(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	newProofPushTestMirror(t, publisher)
	seedAssignmentEvents(t, publisher, assignmentSeed{runID: "run-1", assignmentID: "assignment-1", events: 1})
	request := assignmentAuditRequest("assignment-1")
	request.ScenarioID = "scenario-1"
	request.ScenarioInput = []byte(`{"user_prompt":"Summarize the incident."}`)
	request.ScenarioGold = []byte(`{"exact_token":"ZTD-OK"}`)

	plan, err := publisher.planAssignmentAuditProof(context.Background(), request)

	require.NoError(t, err)
	require.Len(t, plan.artifacts, 3)
	input, gold := plan.artifacts[1], plan.artifacts[2]
	assert.Equal(t, "scenario-scenario-1.input.json", input.Filename)
	assert.Equal(t, "scenario-scenario-1.gold.json", gold.Filename)
	for _, fixture := range []ProofArtifactInput{input, gold} {
		assert.Equal(t, constants.MediaTypeJSON, fixture.MediaType)
		assert.Equal(t, "campaign-1", fixture.CampaignID)
		assert.Empty(t, fixture.SourceRunID, "fixtures are campaign-scoped so one run's prune cannot remove shared bytes")
	}
	assert.Equal(t, request.ScenarioInput, input.Content, "the fixture is published byte for byte, never screened")
	assert.Equal(t, request.ScenarioGold, gold.Content)
	assert.Equal(t, hashBytes(request.ScenarioInput), plan.response.ScenarioInputSHA256)
	assert.Equal(t, hashBytes(request.ScenarioGold), plan.response.ScenarioGoldSHA256)
}

func TestAssignmentAuditProofPlan_RejectsIncompleteRequests(t *testing.T) {
	// Validation runs before any event is read, so no event source is needed.
	publisher := newProofBuilderTestEnv(t)
	cases := map[string]func(*models.PublicAssignmentAuditProofPublishRequest){
		"input without gold": func(r *models.PublicAssignmentAuditProofPublishRequest) {
			r.ScenarioID, r.ScenarioInput = "scenario-1", []byte(`{}`)
		},
		"gold without input": func(r *models.PublicAssignmentAuditProofPublishRequest) {
			r.ScenarioID, r.ScenarioGold = "scenario-1", []byte(`{}`)
		},
		"bodies without scenario": func(r *models.PublicAssignmentAuditProofPublishRequest) {
			r.ScenarioInput, r.ScenarioGold = []byte(`{}`), []byte(`{}`)
		},
		"no campaign revision": func(r *models.PublicAssignmentAuditProofPublishRequest) { r.CampaignRevision = "" },
		"no index digest":      func(r *models.PublicAssignmentAuditProofPublishRequest) { r.IndexDigest = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := assignmentAuditRequest("assignment-1")
			mutate(&request)
			_, err := publisher.planAssignmentAuditProof(context.Background(), request)
			assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
		})
	}
}

func TestAssignmentAuditProof_MissingEventsFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("no retained events for the assignment", func(t *testing.T) {
		publisher := newProofBuilderTestEnv(t)
		newProofPushTestMirror(t, publisher)

		_, err := publisher.PublishAssignmentAuditProof(ctx, assignmentAuditRequest("assignment-1"))

		require.ErrorIs(t, err, constants.ErrPublicAssignmentAuditEventsMissing)
		exists, err := publisher.fileSvc.FileExists(ctx, constants.PublicProofSegmentsFilename)
		require.NoError(t, err)
		assert.False(t, exists, "nothing is appended to the proof log for an assignment with no events")
		catalog, err := publisher.GetProofCatalog(ctx)
		require.NoError(t, err)
		assert.Empty(t, catalog.Entries)
	})

	t.Run("no event source is configured", func(t *testing.T) {
		publisher := newProofBuilderTestEnv(t)

		_, err := publisher.PublishAssignmentAuditProof(ctx, assignmentAuditRequest("assignment-1"))

		assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
	})
}

func TestAssignmentAuditProof_RepublishReturnsSameDigestWithoutRebuild(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, _ := newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	seedAssignmentEvents(t, publisher, assignmentSeed{runID: "run-1", assignmentID: "assignment-1", events: 2})
	counter := &countingEventSource{inner: store}
	publisher.SetAssignmentEventSource(counter)
	request := assignmentAuditRequest("assignment-1")

	first, err := publisher.PublishAssignmentAuditProof(ctx, request)
	require.NoError(t, err)
	require.NotEmpty(t, first.DatabaseSHA256)
	require.Equal(t, 1, counter.calls, "the first publication builds the database from the stored events")

	second, err := publisher.PublishAssignmentAuditProof(ctx, request)
	require.NoError(t, err)
	assert.Equal(t, first.DatabaseSHA256, second.DatabaseSHA256)
	assert.Equal(t, 1, counter.calls, "a republish reuses the active database and never reads the events again")

	catalog, err := publisher.GetProofCatalog(ctx)
	require.NoError(t, err)
	assert.Len(t, catalog.Entries, 1, "the republish lists no second copy of the database")
}

func TestAssignmentAuditProof_BuiltDatabasePassesVerifyDatabase(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	seedAssignmentEvents(t, publisher,
		assignmentSeed{runID: "run-1", assignmentID: "assignment-1", events: 2},
		assignmentSeed{runID: "run-1", assignmentID: "assignment-2", events: 1},
	)

	response, err := publisher.PublishAssignmentAuditProof(ctx, assignmentAuditRequest("assignment-1"))
	require.NoError(t, err)
	assert.Equal(t, "assignment-1", response.AssignmentID)

	image, err := publisher.fileSvc.ReadFile(ctx, filepath.Join(constants.PublicProofsDirname, response.DatabaseSHA256))
	require.NoError(t, err)
	assert.Equal(t, response.DatabaseSHA256, models.SHA256Hex(image))
	header, records, err := assignmentaudit.VerifyDatabase(ctx, image)
	require.NoError(t, err)
	assert.Equal(t, assignmentaudit.Header{
		SourceID:         publisher.cfg.SourceID,
		CampaignID:       "campaign-1",
		CampaignRevision: "rev-1",
		RunID:            "run-1",
		AssignmentID:     "assignment-1",
	}, header)
	require.Len(t, records, 2, "only the assignment's own events are in the database")

	catalog, err := publisher.GetProofCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, catalog.Entries, 1, "the assignment publishes its database alone")
	assert.Equal(t, response.DatabaseSHA256, catalog.Entries[0].ArtifactID)
	assert.Equal(t, "assignment-assignment-1.db", catalog.Entries[0].Filename)

	// The signed segment lists the artifact, so its signature covers the digest.
	segments := requireLinearProofChain(t, publisher)
	last := segments[len(segments)-1]
	require.Len(t, last.Entries, 1)
	assert.Equal(t, response.DatabaseSHA256, last.Entries[0].ArtifactID)
}

func TestAssignmentAuditProofBatch_ReturnsEachProofsDigestsInRequestOrder(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	seedAssignmentEvents(t, publisher,
		assignmentSeed{runID: "run-1", assignmentID: "assignment-1", events: 1},
		assignmentSeed{runID: "run-2", assignmentID: "assignment-2", events: 2},
	)
	second := assignmentAuditRequest("assignment-2")
	second.RunID = "run-2"
	first := assignmentAuditRequest("assignment-1")

	response, err := publisher.PublishAssignmentAuditProofBatch(ctx, models.PublicAssignmentAuditProofBatchPublishRequest{
		Proofs: []models.PublicAssignmentAuditProofPublishRequest{second, first},
	})

	require.NoError(t, err)
	assert.True(t, response.Accepted)
	assert.Equal(t, 2, response.IngestedProofs)
	require.Len(t, response.Proofs, 2)
	assert.Equal(t, "assignment-2", response.Proofs[0].AssignmentID)
	assert.Equal(t, "assignment-1", response.Proofs[1].AssignmentID)
	assert.NotEqual(t, response.Proofs[0].DatabaseSHA256, response.Proofs[1].DatabaseSHA256)
	catalog, err := publisher.GetProofCatalog(ctx)
	require.NoError(t, err)
	listed := map[string]bool{}
	for _, entry := range catalog.Entries {
		listed[entry.ArtifactID] = true
	}
	for _, proof := range response.Proofs {
		assert.True(t, proof.Accepted)
		assert.Equal(t, response.ProofRootSHA256, proof.ProofRootSHA256)
		assert.True(t, listed[proof.DatabaseSHA256], "each digest names the database the catalog lists")
	}
}

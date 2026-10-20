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
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func p95Duration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.Sort(sorted)
	idx := int(math.Ceil(float64(len(sorted))*0.95)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func sendProofIngestToHandler(t *testing.T, handler http.Handler, req models.PublicProofIngestRequest) (int, models.PublicProofIngestResponse) {
	t.Helper()
	bodyBytes, err := json.Marshal(req)
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/proof-ingest", bytes.NewReader(bodyBytes))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+mirrorTestIngestToken)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var resp models.PublicProofIngestResponse
	if recorder.Body.Len() > 0 {
		_ = json.Unmarshal(recorder.Body.Bytes(), &resp)
	}
	return recorder.Code, resp
}

func getStoreDiskBytes(ctx context.Context, fileSvc fs.RuntimeFileService) int64 {
	var total int64
	if info, err := fileSvc.Stat(ctx, constants.PublicMirrorDBPath); err == nil {
		total += info.Size()
	}
	if info, err := fileSvc.Stat(ctx, constants.PublicMirrorDBPath+"-wal"); err == nil {
		total += info.Size()
	}
	return total
}

func populateScalingMirror(t *testing.T, env *mirrorTestEnv, batchCount, artifactCount int) (int64, string, int64, string) {
	t.Helper()
	ctx := context.Background()

	// 1. Populate initial batches
	prevBatchHash := constants.PublicFeedZeroHashHex
	for i := range batchCount {
		seq := int64(i + 1)
		rec := env.makeRecord(seq, models.NewPublicFeedObject(map[string]string{
			"kind":       "catalog_snapshot",
			"dataset_id": fmt.Sprintf("dataset-%d", i%10),
			"detail":     "initial-batch-record-payload",
		}))
		batch := env.buildBatch([]models.PublicFeedRecord{rec}, prevBatchHash)
		prevBatchHash = batch.ContentHash
		require.NoError(t, env.mirror.acceptBatch(ctx, batch))
	}

	// 2. Populate initial artifacts in segments of 100
	const segmentBatchSize = 100
	var segIndex int64
	prevProofRoot := constants.PublicFeedZeroHashHex
	remainingArtifacts := artifactCount
	for remainingArtifacts > 0 {
		segIndex++
		count := min(remainingArtifacts, segmentBatchSize)
		entries := make([]models.PublicProofCatalogEntry, count)
		artifacts := make([]models.PublicProofIngestArtifact, count)
		for k := range count {
			artNum := (artifactCount - remainingArtifacts) + k + 1
			content := []byte(fmt.Sprintf(`{"artifact_index":%d,"data":"initial-proof-artifact-data"}`, artNum))
			entry := makeTestCatalogEntry(fmt.Sprintf("proof-%d.json", artNum), content)
			entries[k] = entry
			artifacts[k] = models.PublicProofIngestArtifact{ArtifactID: entry.ArtifactID, Content: content}
		}
		seg := makeTestSegment(env, segIndex, prevProofRoot, entries, nil)
		prevProofRoot = seg.ProofRootSHA256
		req := models.PublicProofIngestRequest{
			SourceID:  env.sourceID,
			Segment:   &seg,
			Artifacts: artifacts,
		}
		require.NoError(t, env.mirror.storeProofPackage(ctx, req))
		remainingArtifacts -= count
	}

	return int64(batchCount), prevBatchHash, segIndex, prevProofRoot
}

func checkpointStore(ctx context.Context, env *mirrorTestEnv) {
	_, _ = env.store.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
}

func TestPublicMirror_ScalingIntegration(t *testing.T) {
	ctx := context.Background()

	// Build two mirrors: small (500 batches & artifacts) and large (5,000 batches & artifacts)
	smallEnv := newMirrorTestEnv(t)
	smallLastSeq, smallPrevBatchHash, smallSegIndex, smallPrevProofRoot := populateScalingMirror(t, smallEnv, 500, 500)

	largeEnv := newMirrorTestEnv(t)
	largeLastSeq, largePrevBatchHash, largeSegIndex, largePrevProofRoot := populateScalingMirror(t, largeEnv, 5000, 5000)

	const timedBatches = 50
	const timedProofPushes = 10

	// Benchmark Batch Ingest on Small Mirror
	checkpointStore(ctx, smallEnv)
	runtime.GC()
	smallStoreBytesBefore := getStoreDiskBytes(ctx, smallEnv.fileSvc)
	smallBatchDurations := make([]time.Duration, timedBatches)
	var sampleBatchLen int
	for i := range timedBatches {
		smallLastSeq++
		rec := smallEnv.makeRecord(smallLastSeq, models.NewPublicFeedObject(map[string]string{
			"kind":       "catalog_snapshot",
			"dataset_id": fmt.Sprintf("bench-dataset-%d", i),
			"content":    "timed-batch-ingest-payload",
		}))
		batch := smallEnv.buildBatch([]models.PublicFeedRecord{rec}, smallPrevBatchHash)
		smallPrevBatchHash = batch.ContentHash
		if i == 0 {
			raw, _ := json.Marshal(batch)
			sampleBatchLen = len(raw)
		}

		start := time.Now()
		code, resp := sendIngestToHandler(t, smallEnv.mirror.Handler(), batch)
		smallBatchDurations[i] = time.Since(start)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp.Accepted)
	}
	checkpointStore(ctx, smallEnv)
	smallStoreBytesAfter := getStoreDiskBytes(ctx, smallEnv.fileSvc)

	// Benchmark Batch Ingest on Large Mirror
	checkpointStore(ctx, largeEnv)
	runtime.GC()
	largeStoreBytesBefore := getStoreDiskBytes(ctx, largeEnv.fileSvc)
	largeBatchDurations := make([]time.Duration, timedBatches)
	for i := range timedBatches {
		largeLastSeq++
		rec := largeEnv.makeRecord(largeLastSeq, models.NewPublicFeedObject(map[string]string{
			"kind":       "catalog_snapshot",
			"dataset_id": fmt.Sprintf("bench-dataset-%d", i),
			"content":    "timed-batch-ingest-payload",
		}))
		batch := largeEnv.buildBatch([]models.PublicFeedRecord{rec}, largePrevBatchHash)
		largePrevBatchHash = batch.ContentHash

		start := time.Now()
		code, resp := sendIngestToHandler(t, largeEnv.mirror.Handler(), batch)
		largeBatchDurations[i] = time.Since(start)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp.Accepted)
	}
	checkpointStore(ctx, largeEnv)
	largeStoreBytesAfter := getStoreDiskBytes(ctx, largeEnv.fileSvc)

	// Benchmark Proof Ingest on Small Mirror
	runtime.GC()
	smallProofDurations := make([]time.Duration, timedProofPushes)
	for i := range timedProofPushes {
		smallSegIndex++
		content := []byte(fmt.Sprintf(`{"bench_proof_small":%d,"data":"proof-content"}`, i))
		entry := makeTestCatalogEntry(fmt.Sprintf("bench-small-%d.json", i), content)
		seg := makeTestSegment(smallEnv, smallSegIndex, smallPrevProofRoot, []models.PublicProofCatalogEntry{entry}, nil)
		smallPrevProofRoot = seg.ProofRootSHA256
		req := models.PublicProofIngestRequest{
			SourceID:  smallEnv.sourceID,
			Segment:   &seg,
			Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
		}

		start := time.Now()
		code, resp := sendProofIngestToHandler(t, smallEnv.mirror.Handler(), req)
		smallProofDurations[i] = time.Since(start)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp.Accepted)
	}

	// Benchmark Proof Ingest on Large Mirror
	runtime.GC()
	largeProofDurations := make([]time.Duration, timedProofPushes)
	for i := range timedProofPushes {
		largeSegIndex++
		content := []byte(fmt.Sprintf(`{"bench_proof_large":%d,"data":"proof-content"}`, i))
		entry := makeTestCatalogEntry(fmt.Sprintf("bench-large-%d.json", i), content)
		seg := makeTestSegment(largeEnv, largeSegIndex, largePrevProofRoot, []models.PublicProofCatalogEntry{entry}, nil)
		largePrevProofRoot = seg.ProofRootSHA256
		req := models.PublicProofIngestRequest{
			SourceID:  largeEnv.sourceID,
			Segment:   &seg,
			Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
		}

		start := time.Now()
		code, resp := sendProofIngestToHandler(t, largeEnv.mirror.Handler(), req)
		largeProofDurations[i] = time.Since(start)
		require.Equal(t, http.StatusOK, code)
		require.True(t, resp.Accepted)
	}

	// 1. Assert p95 ratio < 2
	smallBatchP95 := p95Duration(smallBatchDurations)
	largeBatchP95 := p95Duration(largeBatchDurations)
	batchRatio := float64(largeBatchP95) / float64(smallBatchP95)

	smallProofP95 := p95Duration(smallProofDurations)
	largeProofP95 := p95Duration(largeProofDurations)
	proofRatio := float64(largeProofP95) / float64(smallProofP95)

	t.Logf("Batch Ingest p95: small=%v, large=%v, ratio=%.2f", smallBatchP95, largeBatchP95, batchRatio)
	t.Logf("Proof Push p95: small=%v, large=%v, ratio=%.2f", smallProofP95, largeProofP95, proofRatio)

	assert.Less(t, batchRatio, 2.0, "batch ingest p95 ratio (large/small) must be < 2")
	assert.Less(t, proofRatio, 2.0, "proof push p95 ratio (large/small) must be < 2")

	// 2. Assert bytes written per batch ingest are bounded by a small multiple of the batch size
	smallDelta := smallStoreBytesAfter - smallStoreBytesBefore
	largeDelta := largeStoreBytesAfter - largeStoreBytesBefore
	avgSmallBatchBytes := smallDelta / timedBatches
	avgLargeBatchBytes := largeDelta / timedBatches

	t.Logf("Store disk growth per batch: small avg=%d bytes, large avg=%d bytes (sample batch JSON=%d bytes)",
		avgSmallBatchBytes, avgLargeBatchBytes, sampleBatchLen)

	assert.Greater(t, sampleBatchLen, 0)
	// In SQLite WAL mode, each commit writes WAL frames (~4KB-8KB).
	// A small multiple of batch size (e.g. 50x) proves it is not rewriting full history (which would be tens/hundreds of MBs).
	assert.Less(t, avgSmallBatchBytes, int64(sampleBatchLen)*50, "small mirror store growth per batch must be bounded by small multiple of batch size")
	assert.Less(t, avgLargeBatchBytes, int64(sampleBatchLen)*50, "large mirror store growth per batch must be bounded by small multiple of batch size")
}

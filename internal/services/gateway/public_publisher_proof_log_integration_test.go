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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// newSharedLogPublisher returns a second publisher over the same runtime tree
// and key, standing in for another process appending to the same proof log.
func newSharedLogPublisher(publisher *PublicPublisherService) *PublicPublisherService {
	return NewPublicPublisherService(publisher.docStore, publisher.fileSvc, publisher.logger, publisher.cfg, publisher.signingPrivKey, publisher.signingKeyID)
}

// requireLinearProofChain asserts the on-disk log is one unbroken chain of
// verified segments from the zero root and returns it.
func requireLinearProofChain(t *testing.T, publisher *PublicPublisherService) []models.PublicProofManifestSegment {
	t.Helper()
	segments := readTestProofSegments(t, publisher)
	previousRoot := constants.PublicFeedZeroHashHex
	for i, segment := range segments {
		require.Equal(t, int64(i+1), segment.SegmentIndex)
		require.Equal(t, previousRoot, segment.PreviousRoot)
		require.NoError(t, models.VerifyPublicProofSegment(publisher.cfg.SourceID, segment, publisher.signingPubKey))
		previousRoot = segment.ProofRootSHA256
	}
	return segments
}

func proofLogSize(t *testing.T, publisher *PublicPublisherService) int64 {
	t.Helper()
	info, err := publisher.fileSvc.Stat(context.Background(), constants.PublicProofSegmentsFilename)
	require.NoError(t, err)
	return info.Size()
}

func TestPublisherProofLog_DiscardsIncompleteTail(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	buildPushTestProof(t, publisher, "a", "run-a")
	buildPushTestProof(t, publisher, "b", "run-b")
	committed := proofLogSize(t, publisher)

	// An append that died mid-write leaves a line with no newline.
	f, err := publisher.fileSvc.OpenForAppend(ctx, constants.PublicProofSegmentsFilename, constants.PermFilePrivate)
	require.NoError(t, err)
	_, err = f.WriteString(`{"schema_version":"1.0.0","source_id":"test-sou`)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	restarted := newSharedLogPublisher(publisher)
	catalog, err := restarted.GetProofCatalog(ctx)
	require.NoError(t, err)
	assert.Len(t, catalog.Entries, 2, "the incomplete tail is never applied")
	assert.Equal(t, committed, restarted.proofIndex.offset)

	manifest := buildPushTestProof(t, restarted, "c", "run-c")
	segments := requireLinearProofChain(t, restarted)
	require.Len(t, segments, 3)
	assert.Equal(t, manifest.ProofRootSHA256, segments[2].ProofRootSHA256)
	assert.Equal(t, 3, manifest.ArtifactCount)
}

func TestPublisherProofLog_AppliesAppendsFromAnotherProcess(t *testing.T) {
	first := newProofBuilderTestEnv(t)
	second := newSharedLogPublisher(first)
	ctx := context.Background()

	buildPushTestProof(t, first, "a", "run-a")
	buildPushTestProof(t, second, "b", "run-b")
	offset := first.proofIndex.offset
	catalog, err := first.GetProofCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, catalog.Entries, 2, "the second process's append is visible")
	assert.Equal(t, offset, first.proofIndex.segments[1].offset, "only bytes past the indexed offset are read")

	pruned, err := first.PruneProofCatalogForRun(ctx, "run-b")
	require.NoError(t, err)
	assert.Equal(t, 1, pruned)
	_, err = second.BuildProofPackage(ctx, "c1", "rev-1", "hash", true, []ProofArtifactInput{{
		Filename: "b.json", MediaType: "application/json", Content: []byte(`{"campaign_id":"c1","artifact":"b"}`), CampaignID: "c1", SourceRunID: "run-b",
	}})
	require.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentEquivocation, "a withdrawal by another process is enforced")
	buildPushTestProof(t, second, "c", "run-c")
	require.Len(t, requireLinearProofChain(t, first), 4)
}

func TestPublisherProofLog_AppendLockHeldElsewhereFailsClosed(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	buildPushTestProof(t, publisher, "a", "run-a")
	held, err := publisher.fileSvc.TryLock(ctx, constants.PublicProofSegmentsLockFilename, constants.PermFilePrivate)
	require.NoError(t, err)
	_, err = publisher.BuildProofPackage(ctx, "c1", "rev-1", "hash", true, []ProofArtifactInput{{
		Filename: "b.json", MediaType: "application/json", Content: []byte(`{"b":true}`), CampaignID: "c1",
	}})
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	require.NoError(t, held.Close())
	buildPushTestProof(t, publisher, "b", "run-b")
	require.Len(t, requireLinearProofChain(t, publisher), 2)
}

func TestPublisherProofLog_TamperedOrReplacedLogFailsClosed(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	buildPushTestProof(t, publisher, "a", "run-a")
	buildPushTestProof(t, publisher, "b", "run-b")
	data, err := publisher.fileSvc.ReadFile(ctx, constants.PublicProofSegmentsFilename)
	require.NoError(t, err)

	// Rewriting the log replaces its inode; the index replays it and rejects
	// the altered link.
	tampered := slices.Clone(data)
	at := slices.Index(tampered, '\n') + 1 + len(`{"schema_version":"`)
	tampered[at] ^= 1
	require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofSegmentsFilename, tampered, constants.PermFilePrivate))
	_, err = publisher.GetProofCatalog(ctx)
	require.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentInvalid)
	_, err = publisher.BuildProofPackage(ctx, "c1", "rev-1", "hash", true, []ProofArtifactInput{{
		Filename: "c.json", MediaType: "application/json", Content: []byte(`{"c":true}`), CampaignID: "c1",
	}})
	require.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentInvalid, "nothing is appended to a log that does not verify")

	require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofSegmentsFilename, data, constants.PermFilePrivate))
	catalog, err := publisher.GetProofCatalog(ctx)
	require.NoError(t, err)
	assert.Len(t, catalog.Entries, 2)
}

func TestPublisherProofLog_ConcurrentPublicationsFormLinearChain(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, _ := newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	const publications = 16

	var wg sync.WaitGroup
	errs := make(chan error, publications)
	for i := range publications {
		wg.Go(func() {
			_, err := publisher.PublishAssignmentAuditProof(ctx, models.PublicAssignmentAuditProofPublishRequest{
				CampaignID:   "c1",
				RunID:        fmt.Sprintf("run-%d", i),
				AssignmentID: fmt.Sprintf("assignment-%d", i),
				Database:     []byte(fmt.Sprintf("database-%d", i)),
				VaultKey:     []byte(fmt.Sprintf("vault-key-%d", i)),
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	segments := requireLinearProofChain(t, publisher)
	require.Len(t, segments, publications)
	catalog, err := publisher.GetProofCatalog(ctx)
	require.NoError(t, err)
	assert.Len(t, catalog.Entries, 2*publications)
	stored, found, err := store.ProofManifest(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, segments[publications-1].ProofRootSHA256, stored.ProofRootSHA256)
	assert.Equal(t, 2*publications, stored.ArtifactCount)
}

// seedPublisherProofLog writes a signed log of segments, each listing
// entriesPerSegment artifacts, in one write.
func seedPublisherProofLog(t *testing.T, publisher *PublicPublisherService, segments, entriesPerSegment int) {
	t.Helper()
	index := newPublicProofIndex()
	var log []byte
	for s := range segments {
		entries := make([]models.PublicProofCatalogEntry, entriesPerSegment)
		for e := range entriesPerSegment {
			entries[e] = makeTestCatalogEntry(fmt.Sprintf("seed-%d-%d.json", s, e), []byte(fmt.Sprintf(`{"seed":%d,"entry":%d}`, s, e)))
			entries[e].SourceRunID = fmt.Sprintf("seed-run-%d", s)
		}
		previousRoot := constants.PublicFeedZeroHashHex
		if s > 0 {
			previousRoot = index.head.ProofRootSHA256
		}
		segment, err := publisher.signProofSegment(models.PublicProofManifestSegment{
			SchemaVersion:  constants.PublicProofSegmentSchemaVersion,
			SourceID:       publisher.cfg.SourceID,
			SegmentIndex:   int64(s + 1),
			PreviousRoot:   previousRoot,
			CampaignID:     "c1",
			VerificationOK: true,
			Entries:        entries,
			GeneratedAt:    time.Now().UTC(),
		})
		require.NoError(t, err)
		line, err := json.Marshal(segment)
		require.NoError(t, err)
		line = append(line, '\n')
		index.commit(segment, int64(len(log)), int64(len(line)))
		log = append(log, line...)
	}
	require.NoError(t, publisher.fileSvc.MkdirAll(context.Background(), constants.PublicProofsDirname, constants.PermDirPrivate))
	require.NoError(t, publisher.fileSvc.WriteFile(context.Background(), constants.PublicProofSegmentsFilename, log, constants.PermFilePrivate))
}

// proofHeadMirror is a mirror stub that reports a fixed head and accepts the
// next segments, recording each ingest body size.
type proofHeadMirror struct {
	mu    sync.Mutex
	head  models.PublicProofRootResponse
	sizes []int
}

func (m *proofHeadMirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(m.head)
		return
	}
	body, _ := io.ReadAll(r.Body)
	m.sizes = append(m.sizes, len(body))
	var request models.PublicProofIngestRequest
	if err := json.Unmarshal(body, &request); err != nil || request.Segment == nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.PublicProofIngestResponse{})
		return
	}
	m.head = models.PublicProofRootResponse{SourceID: request.SourceID, SegmentIndex: request.Segment.SegmentIndex, ProofRootSHA256: request.Segment.ProofRootSHA256}
	_ = json.NewEncoder(w).Encode(models.PublicProofIngestResponse{Accepted: true, ProofRoot: request.Segment.ProofRootSHA256})
}

type publisherScalingSample struct {
	allocBytes uint64
	appended   int64
	pushBytes  int
	elapsed    time.Duration
}

func medianOf[T int | int64 | uint64 | time.Duration](samples []publisherScalingSample, field func(publisherScalingSample) T) T {
	values := collect(samples, field)
	slices.Sort(values)
	return values[len(values)/2]
}

// measurePublisherScaling seeds a history of historySegments segments, then
// measures timed publications, each pushed to a mirror already at the head.
func measurePublisherScaling(t *testing.T, historySegments, timed int) []publisherScalingSample {
	t.Helper()
	ctx := context.Background()
	seeded := newProofBuilderTestEnv(t)
	seedPublisherProofLog(t, seeded, historySegments, 10)
	publisher := newSharedLogPublisher(seeded)
	head, err := publisher.loadProofManifest(ctx) // the one replay of history
	require.NoError(t, err)
	mirror := &proofHeadMirror{head: models.PublicProofRootResponse{SourceID: publisher.cfg.SourceID, SegmentIndex: head.SegmentIndex, ProofRootSHA256: head.ProofRootSHA256}}
	server := httptest.NewServer(mirror)
	t.Cleanup(server.Close)
	publisher.SetMirrorOrigin(server.URL)

	samples := make([]publisherScalingSample, timed)
	for i := range timed {
		size := proofLogSize(t, publisher)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		_, err := publisher.PublishAssignmentAuditProof(ctx, models.PublicAssignmentAuditProofPublishRequest{
			CampaignID:   "c1",
			RunID:        fmt.Sprintf("timed-run-%d", i),
			AssignmentID: fmt.Sprintf("timed-%d", i),
			Database:     []byte(fmt.Sprintf("database-%d", i)),
			VaultKey:     []byte(fmt.Sprintf("vault-key-%d", i)),
		})
		require.NoError(t, err)
		samples[i].elapsed = time.Since(start)
		runtime.ReadMemStats(&after)
		samples[i].allocBytes = after.TotalAlloc - before.TotalAlloc
		samples[i].appended = proofLogSize(t, publisher) - size
		mirror.mu.Lock()
		require.Len(t, mirror.sizes, i+1, "each publication pushes exactly its own segment")
		samples[i].pushBytes = mirror.sizes[i]
		mirror.mu.Unlock()
	}
	return samples
}

// TestPublisherProofLog_PublicationCostIsFlatInHistory guards the publisher's
// per-publication cost: allocation, log write, and push body must not grow
// with retained history.
func TestPublisherProofLog_PublicationCostIsFlatInHistory(t *testing.T) {
	const timed = 21
	small := measurePublisherScaling(t, 100, timed)
	large := measurePublisherScaling(t, 1000, timed)

	allocBytes := func(s publisherScalingSample) uint64 { return s.allocBytes }
	appended := func(s publisherScalingSample) int64 { return s.appended }
	pushBytes := func(s publisherScalingSample) int { return s.pushBytes }
	elapsed := func(s publisherScalingSample) time.Duration { return s.elapsed }
	t.Logf("history 100 segments: median alloc=%d B, appended=%d B, push=%d B, p95=%s",
		medianOf(small, allocBytes), medianOf(small, appended), medianOf(small, pushBytes), p95Duration(collect(small, elapsed)))
	t.Logf("history 1000 segments: median alloc=%d B, appended=%d B, push=%d B, p95=%s",
		medianOf(large, allocBytes), medianOf(large, appended), medianOf(large, pushBytes), p95Duration(collect(large, elapsed)))

	assert.Less(t, medianOf(large, allocBytes), 2*medianOf(small, allocBytes), "publication allocation grows with history")
	assert.InEpsilon(t, medianOf(small, appended), medianOf(large, appended), 0.05, "log write grows with history")
	assert.InEpsilon(t, medianOf(small, pushBytes), medianOf(large, pushBytes), 0.05, "push body grows with history")
	for _, sample := range slices.Concat(small, large) {
		assert.Less(t, sample.appended, int64(1<<20), "one publication writes over 1 MiB")
	}
}

func collect[T any](samples []publisherScalingSample, field func(publisherScalingSample) T) []T {
	values := make([]T, len(samples))
	for i, sample := range samples {
		values[i] = field(sample)
	}
	return values
}

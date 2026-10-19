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
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// proofIngestRecorder wraps a mirror handler and records the inline artifact
// count of every proof ingest request it forwards.
type proofIngestRecorder struct {
	t               *testing.T
	next            http.Handler
	mu              sync.Mutex
	inlined         []int
	requests        []models.PublicProofIngestRequest
	bodySizes       []int
	rejectCommitted bool
}

func (r *proofIngestRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/proof-ingest" {
		body, err := io.ReadAll(request.Body)
		require.NoError(r.t, err)
		var ingest models.PublicProofIngestRequest
		require.NoError(r.t, json.Unmarshal(body, &ingest))
		r.mu.Lock()
		r.inlined = append(r.inlined, len(ingest.Artifacts))
		r.requests = append(r.requests, ingest)
		r.bodySizes = append(r.bodySizes, len(body))
		rejectCommitted := r.rejectCommitted
		r.rejectCommitted = false
		r.mu.Unlock()
		request.Body = io.NopCloser(bytes.NewReader(body))
		if rejectCommitted {
			r.next.ServeHTTP(httptest.NewRecorder(), request)
			http.Error(w, "acknowledgement lost", http.StatusServiceUnavailable)
			return
		}
	}
	r.next.ServeHTTP(w, request)
}

func (r *proofIngestRecorder) takeInlined() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	inlined := r.inlined
	r.inlined = nil
	return inlined
}

func newProofPushTestMirror(t *testing.T, publisher *PublicPublisherService) (*PublicMirrorStore, *proofIngestRecorder) {
	t.Helper()
	store := openTestPublicMirrorStore(t, newProducerFileSvc(t))
	mirror, err := NewPublicMirrorServer(publisher.logger, store, PublicMirrorConfig{})
	require.NoError(t, err)
	mirror.SetIngestAuthToken("proof-token")
	require.NoError(t, mirror.RegisterSourceKey(context.Background(), publisher.cfg.SourceID, publisher.signingKeyID, publisher.signingPrivKey.Public().(ed25519.PublicKey)))
	recorder := &proofIngestRecorder{t: t, next: mirror.Handler()}
	server := httptest.NewServer(recorder)
	t.Cleanup(server.Close)
	publisher.SetMirrorOrigin(server.URL)
	publisher.SetIngestAuthToken("proof-token")
	publisher.SetAssignmentEventSource(store)
	return store, recorder
}

func buildPushTestProof(t *testing.T, publisher *PublicPublisherService, name, runID string) models.PublicProofManifest {
	t.Helper()
	manifest, err := publisher.BuildProofPackage(context.Background(), "c1", "rev-1", "hash", true, []ProofArtifactInput{{
		Filename:    name + ".json",
		MediaType:   "application/json",
		Content:     []byte(`{"campaign_id":"c1","artifact":"` + name + `"}`),
		CampaignID:  "c1",
		SourceRunID: runID,
	}})
	require.NoError(t, err)
	return manifest
}

func TestPushProofPackage_SendsOnlyArtifactsTheMirrorLacks(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, recorder := newProofPushTestMirror(t, publisher)
	ctx := context.Background()

	buildPushTestProof(t, publisher, "a", "run-a")
	manifest := buildPushTestProof(t, publisher, "b", "run-b")
	require.NoError(t, publisher.PushProofPackage(ctx))
	assert.Equal(t, []int{1, 1}, recorder.takeInlined(), "first push sends each signed segment with its new artifacts")
	syncState, err := publisher.loadProofMirrorSyncState(ctx)
	require.NoError(t, err)
	require.NotNil(t, syncState)
	assert.Equal(t, manifest.ProofRootSHA256, syncState.ProofRootSHA256)
	assert.Equal(t, 2, syncState.ArtifactCount)
	assert.Equal(t, int64(2), syncState.SegmentIndex)

	manifest = buildPushTestProof(t, publisher, "c", "run-c")
	require.NoError(t, publisher.PushProofPackage(ctx))
	assert.Equal(t, []int{1}, recorder.takeInlined(), "follow-up push sends only the appended artifact")
	stored, found, err := store.ProofManifest(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, manifest.ProofRootSHA256, stored.ProofRootSHA256)

	require.NoError(t, publisher.PushProofPackage(ctx))
	assert.Empty(t, recorder.takeInlined(), "an unchanged manifest is not resent")
}

func TestPushProofPackage_PruneResetsSyncStateAndRepublishes(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, recorder := newProofPushTestMirror(t, publisher)
	ctx := context.Background()

	buildPushTestProof(t, publisher, "a", "run-a")
	buildPushTestProof(t, publisher, "b", "run-b")
	require.NoError(t, publisher.PushProofPackage(ctx))
	recorder.takeInlined()

	removed, err := publisher.PruneProofCatalogForRun(ctx, "run-a")
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	exists, err := publisher.fileSvc.FileExists(ctx, constants.PublicProofMirrorSyncFilename)
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, publisher.PushProofPackage(ctx))
	assert.Equal(t, []int{0}, recorder.takeInlined(), "the mirror already holds the surviving artifact")
	manifest, err := publisher.loadProofManifest(ctx)
	require.NoError(t, err)
	stored, found, err := store.ProofManifest(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, manifest.ProofRootSHA256, stored.ProofRootSHA256)
	assert.Equal(t, 1, stored.ArtifactCount)
}

func TestLoadProofMirrorSyncState_IgnoresPriorSchema(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	legacy := []byte(`{"schema_version":"1.0.0","source_id":"test-source-1","proof_root_sha256":"abc","synced_artifact_ids":["x"]}`)
	require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofMirrorSyncFilename, legacy, constants.PermFilePrivate))
	syncState, err := publisher.loadProofMirrorSyncState(ctx)
	require.NoError(t, err)
	assert.Nil(t, syncState)
}

func TestPushProofPackage_TransmitsSignedSegmentsAndRecoversMirrorLoss(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, recorder := newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	for _, name := range []string{"a", "b"} {
		buildPushTestProof(t, publisher, name, "run-"+name)
		require.NoError(t, publisher.PushProofPackage(ctx))
	}
	segments, err := store.ProofSegments(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.Len(t, segments, 2, "publisher must transport its signed log to the mirror")
	for _, request := range recorder.requests {
		require.NotNil(t, request.Segment)
		assert.Empty(t, request.Manifest.Artifacts, "requests must not carry the historical entry list")
		assert.Len(t, request.Segment.Entries, 1)
		require.NoError(t, models.VerifyPublicProofSegment(publisher.cfg.SourceID, *request.Segment, publisher.signingPubKey))
	}
	assert.Less(t, recorder.bodySizes[len(recorder.bodySizes)-1], recorder.bodySizes[1]+64, "request size stays bounded as history grows")

	// A replacement mirror has no data even though local sync state says
	// the previous mirror accepted the latest root.
	replacement, _ := newProofPushTestMirror(t, publisher)
	require.NoError(t, publisher.PushProofPackage(ctx))
	recovered, err := replacement.ProofSegments(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	assert.Equal(t, segments, recovered)
}

func TestPushProofPackage_ReplaysWithdrawnHistoryToFreshMirror(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	prior := buildPushTestProof(t, publisher, "withdrawn", "old-run")
	buildPushTestProof(t, publisher, "active", "new-run")
	removed, err := publisher.PruneProofCatalogForRun(ctx, "old-run")
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	store, _ := newProofPushTestMirror(t, publisher)
	require.NoError(t, publisher.PushProofPackage(ctx))
	segments, err := store.ProofSegments(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.Len(t, segments, 3)
	stored, found, err := store.ProofManifest(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.Len(t, stored.Artifacts, 1)
	assert.NotEqual(t, prior.Artifacts[0].ArtifactID, stored.Artifacts[0].ArtifactID)
	assert.Equal(t, segments[2].ProofRootSHA256, stored.ProofRootSHA256)
	artifact, found, err := store.proofArtifact(ctx, prior.Artifacts[0].ArtifactID)
	require.NoError(t, err)
	assert.False(t, found, "withdrawn artifacts are unavailable for download")
	assert.Nil(t, artifact.Entry, "withdrawal removes download authorization")
	exists, err := store.fileSvc.FileExists(ctx, publicMirrorProofArtifactPath(prior.Artifacts[0].ArtifactID))
	require.NoError(t, err)
	assert.True(t, exists, "immutable bytes remain stored")
}

func TestPublicPublisher_RejectsReaddingWithdrawnArtifactBeforeAppending(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	buildPushTestProof(t, publisher, "withdrawn", "old-run")
	_, err := publisher.PruneProofCatalogForRun(ctx, "old-run")
	require.NoError(t, err)
	_, err = publisher.BuildProofPackage(ctx, "c1", "rev-1", "hash", true, []ProofArtifactInput{{
		Filename: "withdrawn.json",
		Content:  []byte(`{"campaign_id":"c1","artifact":"withdrawn"}`),
	}})
	require.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentEquivocation)
	require.Len(t, readTestProofSegments(t, publisher), 2, "an invalid append must not poison the durable log")
}

func TestPushProofPackage_ResumesAfterCommittedResponseIsLost(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	store, recorder := newProofPushTestMirror(t, publisher)
	ctx := context.Background()
	buildPushTestProof(t, publisher, "a", "run-a")
	latest := buildPushTestProof(t, publisher, "b", "run-b")
	recorder.mu.Lock()
	recorder.rejectCommitted = true
	recorder.mu.Unlock()
	require.ErrorIs(t, publisher.PushProofPackage(ctx), constants.ErrPublicFeedMirrorUnreachable)
	assert.Equal(t, []int{1}, recorder.takeInlined())
	require.NoError(t, publisher.PushProofPackage(ctx))
	assert.Equal(t, []int{1}, recorder.takeInlined(), "resume skips the segment already committed at the mirror")
	stored, found, err := store.ProofManifest(ctx, publisher.cfg.SourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, latest.ProofRootSHA256, stored.ProofRootSHA256)
}

func TestPublicPublisher_LegacyPackageCutsOverBeforePushOrWithdrawal(t *testing.T) {
	for _, withdraw := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "withdraw"}[withdraw], func(t *testing.T) {
			publisher := newProofBuilderTestEnv(t)
			ctx := context.Background()
			legacy := buildPushTestProof(t, publisher, "legacy", "old-run")
			legacy.SegmentIndex = 0
			legacy.ProofRootSHA256 = computePublicProofRootHash(legacy)
			root, err := hex.DecodeString(legacy.ProofRootSHA256)
			require.NoError(t, err)
			legacy.Signature = hex.EncodeToString(ed25519.Sign(publisher.signingPrivKey, root))
			data, err := json.Marshal(legacy)
			require.NoError(t, err)
			require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofManifestFilename, data, constants.PermFilePrivate))
			data, err = json.Marshal(proofCatalogFromManifest(legacy))
			require.NoError(t, err)
			require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofCatalogFilename, data, constants.PermFilePrivate))
			require.NoError(t, publisher.fileSvc.Remove(ctx, constants.PublicProofSegmentsFilename))
			store, _ := newProofPushTestMirror(t, publisher)
			if withdraw {
				_, err := publisher.PruneProofCatalogForRun(ctx, "old-run")
				require.NoError(t, err)
			}
			require.NoError(t, publisher.PushProofPackage(ctx))
			segments, err := store.ProofSegments(ctx, publisher.cfg.SourceID)
			require.NoError(t, err)
			require.NotEmpty(t, segments)
			assert.Len(t, segments[0].Entries, 1)
			if withdraw {
				require.Len(t, segments, 2)
				assert.Equal(t, []string{legacy.Artifacts[0].ArtifactID}, segments[1].WithdrawnArtifactIDs)
			}
		})
	}
}

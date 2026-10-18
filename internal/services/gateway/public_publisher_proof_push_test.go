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
	t       *testing.T
	next    http.Handler
	mu      sync.Mutex
	inlined []int
}

func (r *proofIngestRecorder) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/proof-ingest" {
		body, err := io.ReadAll(request.Body)
		require.NoError(r.t, err)
		var ingest models.PublicProofIngestRequest
		require.NoError(r.t, json.Unmarshal(body, &ingest))
		r.mu.Lock()
		r.inlined = append(r.inlined, len(ingest.Artifacts))
		r.mu.Unlock()
		request.Body = io.NopCloser(bytes.NewReader(body))
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
	assert.Equal(t, []int{0, 2}, recorder.takeInlined(), "first push learns the missing set from the mirror")
	syncState, err := publisher.loadProofMirrorSyncState(ctx)
	require.NoError(t, err)
	require.NotNil(t, syncState)
	assert.Equal(t, manifest.ProofRootSHA256, syncState.ProofRootSHA256)
	assert.Equal(t, 2, syncState.ArtifactCount)

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

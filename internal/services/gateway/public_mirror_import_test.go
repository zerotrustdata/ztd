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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// legacyMirrorFixture is a signed legacy state.json payload built from a
// mirror test env's source key.
type legacyMirrorFixture struct {
	state      legacyPublicMirrorStoreState
	batch      models.PublicFeedBatch
	proof      models.PublicProofIngestRequest
	artifactID string
}

func newLegacyMirrorFixture(t *testing.T, env *mirrorTestEnv) legacyMirrorFixture {
	t.Helper()
	records := []models.PublicFeedRecord{env.makeRecord(1, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}
	batch := env.buildBatch(records, constants.PublicFeedZeroHashHex)
	proof := env.makeProofIngestRequest("proof.json", []byte(`{"campaign_id":"c1","verification_ok":true}`))
	artifactID := proof.Artifacts[0].ArtifactID
	acceptedAt := time.Now().UTC().Truncate(time.Millisecond)
	return legacyMirrorFixture{
		state: legacyPublicMirrorStoreState{
			Sources: map[string]*legacyPublicMirrorSourceState{
				env.sourceID: {
					HighWaterSequence: 1,
					FeedChainHash:     batch.ContentHash,
					BatchCount:        1,
					Records:           records,
					Batches:           []models.PublicFeedBatch{batch},
					Freshness:         models.CampaignFreshnessActive,
					LastAcceptedAt:    acceptedAt,
				},
			},
			KeyRegistry:    map[string]ed25519.PublicKey{publicMirrorKeyRegistryID(env.sourceID, env.keyID): env.pub},
			RevokedKeys:    map[string]time.Time{},
			ProofArtifacts: map[string][]byte{artifactID: proof.Artifacts[0].Content},
			ProofManifests: map[string]models.PublicProofManifest{env.sourceID: proof.Manifest},
		},
		batch:      batch,
		proof:      proof,
		artifactID: artifactID,
	}
}

func writeLegacyMirrorState(t *testing.T, fileSvc fs.RuntimeFileService, state legacyPublicMirrorStoreState) {
	t.Helper()
	body, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.PublicMirrorDirname, constants.PermDirPrivate))
	require.NoError(t, fileSvc.WriteFile(context.Background(), constants.PublicMirrorStatePath, body, constants.PermFilePrivate))
}

func TestPublicMirrorStore_ImportsLegacyStateOnceAndRemovesIt(t *testing.T) {
	env := newMirrorTestEnv(t)
	fixture := newLegacyMirrorFixture(t, env)
	ctx := context.Background()
	fileSvc := newProducerFileSvc(t)
	writeLegacyMirrorState(t, fileSvc, fixture.state)

	store := openTestPublicMirrorStore(t, fileSvc)

	legacyExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorStatePath)
	require.NoError(t, err)
	assert.False(t, legacyExists)
	importExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorImportDBPath)
	require.NoError(t, err)
	assert.False(t, importExists)

	summary, found, err := store.SourceSummary(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, int64(1), summary.HighWaterSequence)
	assert.Equal(t, fixture.batch.ContentHash, summary.FeedChainHash)
	assert.Equal(t, int64(1), summary.RetainedFromSequence)
	assert.Equal(t, constants.PublicFeedZeroHashHex, summary.RetainedPreviousBatchHash)
	keys, err := store.Keys(ctx)
	require.NoError(t, err)
	assert.Equal(t, env.pub, keys.Registry[publicMirrorKeyRegistryID(env.sourceID, env.keyID)])
	manifest, found, err := store.ProofManifest(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, fixture.proof.Manifest.ProofRootSHA256, manifest.ProofRootSHA256)
	artifactContent, err := fileSvc.ReadFile(ctx, publicMirrorProofArtifactPath(fixture.artifactID))
	require.NoError(t, err)
	assert.Equal(t, fixture.proof.Artifacts[0].Content, artifactContent)

	mirror, err := NewPublicMirrorServer(testutil.NewTestLogger(), store, PublicMirrorConfig{})
	require.NoError(t, err)
	next := env.buildBatch([]models.PublicFeedRecord{env.makeRecord(2, models.NewPublicFeedObject(map[string]string{"campaign_id": "c1"}))}, fixture.batch.ContentHash)
	reason, err := mirror.validateBatch(ctx, next)
	require.NoError(t, err)
	assert.Empty(t, reason)
	require.NoError(t, mirror.acceptBatch(ctx, next))
}

func TestPublicMirrorStore_LegacyStateAlongsideDatabaseFailsClosed(t *testing.T) {
	env := newMirrorTestEnv(t)
	fixture := newLegacyMirrorFixture(t, env)
	writeLegacyMirrorState(t, env.fileSvc, fixture.state)
	require.NoError(t, env.store.Close())

	_, err := OpenPublicMirrorStore(context.Background(), env.fileSvc, testutil.NewTestLogger())

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorLegacyStateConflict)
}

func TestPublicMirrorStore_FailedLegacyImportLeavesStateUntouched(t *testing.T) {
	env := newMirrorTestEnv(t)
	fixture := newLegacyMirrorFixture(t, env)
	fixture.state.Sources[env.sourceID].FeedChainHash = constants.PublicFeedZeroHashHex
	ctx := context.Background()
	fileSvc := newProducerFileSvc(t)
	writeLegacyMirrorState(t, fileSvc, fixture.state)

	_, err := OpenPublicMirrorStore(ctx, fileSvc, testutil.NewTestLogger())

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
	legacyExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorStatePath)
	require.NoError(t, err)
	assert.True(t, legacyExists)
	dbExists, err := fileSvc.FileExists(ctx, constants.PublicMirrorDBPath)
	require.NoError(t, err)
	assert.False(t, dbExists)
}

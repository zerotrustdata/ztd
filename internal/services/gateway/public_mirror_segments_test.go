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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func makeTestSegment(env *mirrorTestEnv, index int64, prevRoot string, entries []models.PublicProofCatalogEntry, withdrawals []string) models.PublicProofManifestSegment {
	seg := models.PublicProofManifestSegment{
		SchemaVersion:        constants.PublicProofSegmentSchemaVersion,
		SourceID:             env.sourceID,
		SegmentIndex:         index,
		PreviousRoot:         prevRoot,
		CampaignID:           "c1",
		CampaignRevision:     "rev1",
		VerificationOK:       true,
		Entries:              entries,
		WithdrawnArtifactIDs: withdrawals,
		GeneratedAt:          time.Now().UTC(),
		SigningKeyID:         env.keyID,
	}
	root := models.ComputePublicProofSegmentRoot(seg)
	seg.ProofRootSHA256 = root
	rootBytes, _ := hex.DecodeString(root)
	sig := ed25519.Sign(env.priv, rootBytes)
	seg.Signature = hex.EncodeToString(sig)
	return seg
}

func makeTestCatalogEntry(filename string, content []byte) models.PublicProofCatalogEntry {
	hash := sha256.Sum256(content)
	artifactID := hex.EncodeToString(hash[:])
	return models.PublicProofCatalogEntry{
		ArtifactID:          artifactID,
		Filename:            filename,
		MediaType:           "application/json",
		ByteSize:            int64(len(content)),
		SHA256:              artifactID,
		Classification:      models.PublicFeedProofClassificationPublicSafe,
		CampaignID:          "c1",
		GeneratedAt:         time.Now().UTC(),
		VerificationCommand: "sha256sum " + filename,
		ImmutableURL:        "/proofs/" + artifactID,
	}
}

func TestMirror_SegmentIngest_MissingStoredFileRejected(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()

	content := []byte(`{"data":"test"}`)
	entry := makeTestCatalogEntry("test.json", content)
	seg := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)

	// Ingest segment WITHOUT inline artifact
	req := models.PublicProofIngestRequest{
		SourceID: env.sourceID,
		Segment:  &seg,
	}
	err := env.mirror.storeProofPackage(ctx, req)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrPublicFeedProofArtifactsMissing)

	// Now ingest with inline artifact -> succeeds
	req.Artifacts = []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}}
	require.NoError(t, env.mirror.storeProofPackage(ctx, req))
}

func TestMirror_SegmentIngest_EquivocationAcrossSegmentsRejected(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()

	content1 := []byte(`{"v":"1"}`)
	entry1 := makeTestCatalogEntry("v1.json", content1)
	seg1 := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry1}, nil)

	req1 := models.PublicProofIngestRequest{
		SourceID:  env.sourceID,
		Segment:   &seg1,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry1.ArtifactID, Content: content1}},
	}
	require.NoError(t, env.mirror.storeProofPackage(ctx, req1))

	// Re-submitting identical segment is idempotent
	require.NoError(t, env.mirror.storeProofPackage(ctx, req1))

	// Submitting conflicting segment at same index fails with equivocation
	content2 := []byte(`{"v":"2"}`)
	entry2 := makeTestCatalogEntry("v2.json", content2)
	seg1Conflict := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry2}, nil)

	reqConflict := models.PublicProofIngestRequest{
		SourceID:  env.sourceID,
		Segment:   &seg1Conflict,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry2.ArtifactID, Content: content2}},
	}
	err := env.mirror.storeProofPackage(ctx, reqConflict)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentEquivocation)
}

func TestMirror_SegmentIngest_WithdrawalSegments(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()

	content := []byte(`{"data":"to-be-withdrawn"}`)
	entry := makeTestCatalogEntry("withdrawn.json", content)
	seg1 := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)

	req1 := models.PublicProofIngestRequest{
		SourceID:  env.sourceID,
		Segment:   &seg1,
		Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
	}
	require.NoError(t, env.mirror.storeProofPackage(ctx, req1))

	manifest, found, err := env.store.ProofManifest(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 1, manifest.ArtifactCount)

	// Segment 2: withdraw entry
	seg2 := makeTestSegment(env, 2, seg1.ProofRootSHA256, nil, []string{entry.ArtifactID})
	req2 := models.PublicProofIngestRequest{
		SourceID: env.sourceID,
		Segment:  &seg2,
	}
	require.NoError(t, env.mirror.storeProofPackage(ctx, req2))

	// Reconstructed ProofManifest excludes withdrawn artifact
	manifestAfter, found, err := env.store.ProofManifest(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 0, manifestAfter.ArtifactCount)
	assert.Empty(t, manifestAfter.Artifacts)

	// Segments list returns both segments in order
	segments, err := env.store.ProofSegments(ctx, env.sourceID)
	require.NoError(t, err)
	require.Len(t, segments, 2)
	assert.Equal(t, int64(1), segments[0].SegmentIndex)
	assert.Equal(t, int64(2), segments[1].SegmentIndex)
	assert.Equal(t, []string{entry.ArtifactID}, segments[1].WithdrawnArtifactIDs)
}

func TestMirror_SegmentIngest_StartupRejectsChainBreakAndSignatureFailure(t *testing.T) {
	// 1. Chain break
	{
		env := newMirrorTestEnv(t)
		ctx := context.Background()

		content := []byte(`{"chain":"test"}`)
		entry := makeTestCatalogEntry("chain.json", content)
		seg1 := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
		require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
			SourceID:  env.sourceID,
			Segment:   &seg1,
			Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
		}))

		seg2 := makeTestSegment(env, 2, seg1.ProofRootSHA256, nil, nil)
		require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
			SourceID: env.sourceID,
			Segment:  &seg2,
		}))

		// Tamper with previous_root of segment 2 in DB
		_, err := env.store.db.Exec(`UPDATE proof_manifest_segments SET segment_json = json_set(segment_json, '$.previous_root', ?), previous_root = ? WHERE source_id = ? AND segment_index = 2`,
			constants.PublicFeedZeroHashHex, constants.PublicFeedZeroHashHex, env.sourceID)
		require.NoError(t, err)
		require.NoError(t, env.store.Close())

		_, err = OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())
		require.Error(t, err)
		require.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
	}

	// 2. Signature failure
	{
		env := newMirrorTestEnv(t)
		ctx := context.Background()

		content := []byte(`{"sig":"test"}`)
		entry := makeTestCatalogEntry("sig.json", content)
		seg1 := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
		require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{
			SourceID:  env.sourceID,
			Segment:   &seg1,
			Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}},
		}))

		// Tamper with signature of segment 1 in DB
		badSig := hex.EncodeToString(make([]byte, ed25519.SignatureSize))
		_, err := env.store.db.Exec(`UPDATE proof_manifest_segments SET segment_json = json_set(segment_json, '$.signature', ?), signature = ? WHERE source_id = ? AND segment_index = 1`,
			badSig, badSig, env.sourceID)
		require.NoError(t, err)
		require.NoError(t, env.store.Close())

		_, err = OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
	}
}

func TestMirror_SegmentIngest_RejectsUnsignedLinksAndInvalidTransitions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*mirrorTestEnv, *models.PublicProofManifestSegment)
		want   error
	}{
		{"source mismatch", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			seg.SourceID = "different-source"
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"legacy manifest signature", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			request := env.makeProofIngestRequest("proof.json", []byte("proof"))
			seg.Entries = request.Manifest.Artifacts
			seg.ProofRootSHA256 = request.Manifest.ProofRootSHA256
			seg.Signature = request.Manifest.Signature
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"entry metadata tamper", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			seg.Entries[0].SourceRunID = "forged-run"
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"instructions tamper", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			seg.VerifierInstructions = "forged instructions"
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"signature failure", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			seg.Signature = hex.EncodeToString(make([]byte, ed25519.SignatureSize))
		}, constants.ErrPublicFeedSignatureInvalid},
		{"gap", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			*seg = makeTestSegment(env, 2, constants.PublicFeedZeroHashHex, seg.Entries, nil)
		}, constants.ErrPublicFeedProofSegmentOutOfOrder},
		{"unknown withdrawal", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			*seg = makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, nil, []string{seg.Entries[0].ArtifactID})
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"duplicate entry", func(env *mirrorTestEnv, seg *models.PublicProofManifestSegment) {
			*seg = makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, append(seg.Entries, seg.Entries[0]), nil)
		}, constants.ErrPublicFeedProofCatalogMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			content := []byte("proof")
			entry := makeTestCatalogEntry("proof.json", content)
			seg := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
			tc.mutate(env, &seg)
			err := env.mirror.storeProofPackage(context.Background(), models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &seg})
			require.ErrorIs(t, err, tc.want)
			index, _, err := env.store.proofSegmentHead(context.Background(), env.sourceID, 0)
			require.NoError(t, err)
			require.Zero(t, index, "rejection must not advance the signed chain")
		})
	}
}

func TestMirror_SegmentIngest_MissingPreviouslyStoredFileRejected(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	request := env.makeProofIngestRequest("proof.json", []byte("proof"))
	require.NoError(t, env.mirror.storeProofPackage(ctx, request))
	entry := request.Manifest.Artifacts[0]
	require.NoError(t, env.fileSvc.Remove(ctx, publicMirrorProofArtifactPath(entry.ArtifactID)))
	seg := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
	require.ErrorIs(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &seg}), constants.ErrPublicFeedProofArtifactsMissing)
}

func TestMirror_SegmentIngest_StartupVerifiesSignedProjections(t *testing.T) {
	mutations := map[string]string{
		"entry metadata":     `UPDATE proof_manifest_entries SET entry_json = json_set(entry_json, '$.filename', 'forged.json')`,
		"missing entry":      `DELETE FROM proof_manifest_entries`,
		"missing withdrawal": `DELETE FROM proof_withdrawn_artifacts`,
		"extra withdrawal":   `INSERT INTO proof_withdrawn_artifacts (source_id, artifact_id, segment_index, withdrawn_at) SELECT source_id, 'forged', 2, '' FROM proof_manifests`,
		"header metadata":    `UPDATE proof_manifests SET manifest_json = json_set(manifest_json, '$.campaign_id', 'forged')`,
		"missing header":     `DELETE FROM proof_manifests`,
		"indexed root":       `UPDATE proof_manifest_segments SET proof_root_sha256 = 'forged' WHERE segment_index = 1`,
		"indexed position":   `UPDATE proof_manifest_segments SET segment_index = 7 WHERE segment_index = 1`,
	}
	for name, query := range mutations {
		t.Run(name, func(t *testing.T) {
			env := newMirrorTestEnv(t)
			ctx := context.Background()
			content := []byte("proof")
			entry := makeTestCatalogEntry("proof.json", content)
			first := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
			require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &first, Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}}}))
			second := makeTestSegment(env, 2, first.ProofRootSHA256, nil, []string{entry.ArtifactID})
			require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &second}))
			_, err := env.store.db.Exec(query)
			require.NoError(t, err)
			require.NoError(t, env.store.Close())
			reopened, err := OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())
			if reopened != nil {
				require.NoError(t, reopened.Close())
			}
			require.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
		})
	}
}

func TestMirror_SegmentIngest_DoesNotReadHistoricalSegmentsUntilStartup(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	first := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, nil, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &first}))
	_, err := env.store.db.Exec(`UPDATE proof_manifest_segments SET segment_json = 'invalid' WHERE segment_index = 1`)
	require.NoError(t, err)
	second := makeTestSegment(env, 2, first.ProofRootSHA256, nil, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &second}), "ingest must read the indexed head, not deserialize historical segments")
	require.NoError(t, env.store.Close())
	_, err = OpenPublicMirrorStore(ctx, env.fileSvc, testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrPublicFeedMirrorStoreCorrupt)
}

func TestMirror_SegmentIngest_WithdrawalPersistsAndDeniesDownload(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := context.Background()
	content := []byte("proof")
	entry := makeTestCatalogEntry("proof.json", content)
	first := makeTestSegment(env, 1, constants.PublicFeedZeroHashHex, []models.PublicProofCatalogEntry{entry}, nil)
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &first, Artifacts: []models.PublicProofIngestArtifact{{ArtifactID: entry.ArtifactID, Content: content}}}))
	second := makeTestSegment(env, 2, first.ProofRootSHA256, nil, []string{entry.ArtifactID})
	require.NoError(t, env.mirror.storeProofPackage(ctx, models.PublicProofIngestRequest{SourceID: env.sourceID, Segment: &second}))
	mirror := env.reopenMirror()
	catalog, found, err := env.store.ProofCatalog(ctx, env.sourceID)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, catalog.Entries)
	_, found, err = env.store.proofArtifact(ctx, entry.ArtifactID)
	require.NoError(t, err)
	require.False(t, found)
	_, found, err = env.store.proofCatalogEntry(ctx, env.sourceID, entry.ArtifactID)
	require.NoError(t, err)
	require.False(t, found)
	// A full manifest cannot replace or downgrade an established chain.
	require.ErrorIs(t, mirror.storeProofPackage(ctx, env.makeProofIngestRequest("other.json", []byte("other"))), constants.ErrPublicFeedProofSegmentInvalid)
}

func TestPublicPublisher_FirstSegmentIncludesExistingLegacyCatalog(t *testing.T) {
	publisher := newProofBuilderTestEnv(t)
	ctx := context.Background()
	prior := buildPushTestProof(t, publisher, "old", "old-run")
	legacyCatalog, err := json.Marshal(proofCatalogFromManifest(prior))
	require.NoError(t, err)
	require.NoError(t, publisher.fileSvc.WriteFile(ctx, constants.PublicProofCatalogFilename, legacyCatalog, constants.PermFilePrivate))
	require.NoError(t, publisher.fileSvc.Remove(ctx, constants.PublicProofSegmentsFilename))
	buildPushTestProof(t, publisher, "new", "new-run")
	segments := readTestProofSegments(t, publisher)
	require.Len(t, segments, 1)
	require.Len(t, segments[0].Entries, 2)
	require.Equal(t, prior.Artifacts[0], segments[0].Entries[0])
	require.NoError(t, models.VerifyPublicProofSegment(publisher.cfg.SourceID, segments[0], publisher.signingPubKey))
}

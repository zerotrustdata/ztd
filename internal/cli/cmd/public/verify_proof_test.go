// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package public

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestVerifyProofCmd_ValidatesChainAndArtifacts(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)
	keyID := hex.EncodeToString(pub)

	tempDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tempDir, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx := context.Background()
	segmentsPath := fileSvc.Resolve("proof-segments.jsonl")
	artifactsDir := fileSvc.Resolve("proofs")
	require.NoError(t, fileSvc.MkdirAll(ctx, "proofs", constants.PermDirPrivate))

	// Artifact 1
	content1 := []byte(`{"hello":"world 1"}`)
	h1 := sha256.Sum256(content1)
	id1 := hex.EncodeToString(h1[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id1), content1, constants.PermFilePrivate))

	// Artifact 2
	content2 := []byte(`{"hello":"world 2"}`)
	h2 := sha256.Sum256(content2)
	id2 := hex.EncodeToString(h2[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id2), content2, constants.PermFilePrivate))

	// Segment 1
	seg1 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     1,
		PreviousRoot:     constants.PublicFeedZeroHashHex,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		Entries: []models.PublicProofCatalogEntry{
			{
				ArtifactID:          id1,
				Filename:            "file1.json",
				MediaType:           "application/json",
				ByteSize:            int64(len(content1)),
				SHA256:              id1,
				Classification:      models.PublicFeedProofClassificationPublicSafe,
				CampaignID:          "c1",
				GeneratedAt:         time.Now().UTC(),
				VerificationCommand: "sha256sum file1.json",
				ImmutableURL:        "/proofs/" + id1,
			},
		},
		GeneratedAt:  time.Now().UTC(),
		SigningKeyID: keyID,
	}
	root1 := models.ComputePublicProofSegmentRoot(seg1)
	seg1.ProofRootSHA256 = root1
	root1Bytes, _ := hex.DecodeString(root1)
	seg1.Signature = hex.EncodeToString(ed25519.Sign(priv, root1Bytes))

	// Segment 2
	seg2 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     2,
		PreviousRoot:     root1,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		Entries: []models.PublicProofCatalogEntry{
			{
				ArtifactID:          id2,
				Filename:            "file2.json",
				MediaType:           "application/json",
				ByteSize:            int64(len(content2)),
				SHA256:              id2,
				Classification:      models.PublicFeedProofClassificationPublicSafe,
				CampaignID:          "c1",
				GeneratedAt:         time.Now().UTC(),
				VerificationCommand: "sha256sum file2.json",
				ImmutableURL:        "/proofs/" + id2,
			},
		},
		GeneratedAt:  time.Now().UTC(),
		SigningKeyID: keyID,
	}
	root2 := models.ComputePublicProofSegmentRoot(seg2)
	seg2.ProofRootSHA256 = root2
	root2Bytes, _ := hex.DecodeString(root2)
	seg2.Signature = hex.EncodeToString(ed25519.Sign(priv, root2Bytes))

	// Write segments
	line1, _ := json.Marshal(seg1)
	line2, _ := json.Marshal(seg2)
	segmentsContent := append(append(line1, '\n'), append(line2, '\n')...)
	require.NoError(t, fileSvc.WriteFile(ctx, "proof-segments.jsonl", segmentsContent, constants.PermFilePrivate))

	// Verify valid chain
	cmd := publicVerifyProofCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--segments", segmentsPath,
		"--artifacts", artifactsDir,
		"--public-key", pubHex,
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "Proof package verified (2 segments, 2 active artifacts")

	// Verify withdrawal segment
	seg3 := models.PublicProofManifestSegment{
		SchemaVersion:        constants.PublicProofSegmentSchemaVersion,
		SourceID:             "src-1",
		SegmentIndex:         3,
		PreviousRoot:         root2,
		CampaignID:           "c1",
		CampaignRevision:     "rev-1",
		VerificationOK:       true,
		WithdrawnArtifactIDs: []string{id1},
		GeneratedAt:          time.Now().UTC(),
		SigningKeyID:         keyID,
	}
	root3 := models.ComputePublicProofSegmentRoot(seg3)
	seg3.ProofRootSHA256 = root3
	root3Bytes, _ := hex.DecodeString(root3)
	seg3.Signature = hex.EncodeToString(ed25519.Sign(priv, root3Bytes))

	// Remove artifact 1 from disk (since withdrawn)
	require.NoError(t, fileSvc.Remove(ctx, filepath.Join("proofs", id1)))

	line3, _ := json.Marshal(seg3)
	segmentsContentWithWithdrawal := append(segmentsContent, append(line3, '\n')...)
	require.NoError(t, fileSvc.WriteFile(ctx, "proof-segments.jsonl", segmentsContentWithWithdrawal, constants.PermFilePrivate))

	out.Reset()
	cmd = publicVerifyProofCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--segments", segmentsPath,
		"--artifacts", artifactsDir,
		"--public-key", pubHex,
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "Proof package verified (3 segments, 1 active artifacts")

	// Fail on corrupt artifact
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id2), []byte("tampered content"), constants.PermFilePrivate))
	cmd = publicVerifyProofCmd()
	cmd.SetArgs([]string{
		"--segments", segmentsPath,
		"--artifacts", artifactsDir,
		"--public-key", pubHex,
	})
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedProofSizeMismatch)
}

func TestVerifyProofCmd_RejectsBrokenLinkAndInvalidSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubHex := hex.EncodeToString(pub)

	tempDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tempDir, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx := context.Background()
	segmentsPath := fileSvc.Resolve("proof-segments.jsonl")
	artifactsDir := fileSvc.Resolve("proofs")
	require.NoError(t, fileSvc.MkdirAll(ctx, "proofs", constants.PermDirPrivate))

	seg1 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     1,
		PreviousRoot:     constants.PublicFeedZeroHashHex,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		GeneratedAt:      time.Now().UTC(),
		SigningKeyID:     pubHex,
	}
	root1 := models.ComputePublicProofSegmentRoot(seg1)
	seg1.ProofRootSHA256 = root1
	root1Bytes, _ := hex.DecodeString(root1)
	seg1.Signature = hex.EncodeToString(ed25519.Sign(priv, root1Bytes))

	// Broken link segment
	seg2 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     2,
		PreviousRoot:     "bad_previous_root",
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		GeneratedAt:      time.Now().UTC(),
		SigningKeyID:     pubHex,
	}
	root2 := models.ComputePublicProofSegmentRoot(seg2)
	seg2.ProofRootSHA256 = root2
	root2Bytes, _ := hex.DecodeString(root2)
	seg2.Signature = hex.EncodeToString(ed25519.Sign(priv, root2Bytes))

	line1, _ := json.Marshal(seg1)
	line2, _ := json.Marshal(seg2)
	require.NoError(t, fileSvc.WriteFile(ctx, "proof-segments.jsonl", append(append(line1, '\n'), append(line2, '\n')...), constants.PermFilePrivate))

	cmd := publicVerifyProofCmd()
	cmd.SetArgs([]string{
		"--segments", segmentsPath,
		"--artifacts", artifactsDir,
		"--public-key", pubHex,
	})
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPublicFeedProofSegmentLinkMismatch)
}

func TestVerifyProofCmd_RejectsTamperingAndMixedSources(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	base := models.PublicProofManifestSegment{
		SchemaVersion: constants.PublicProofSegmentSchemaVersion, SourceID: "source", SegmentIndex: 1,
		PreviousRoot: constants.PublicFeedZeroHashHex, SigningKeyID: "key", GeneratedAt: time.Now().UTC(), VerificationOK: true,
	}
	sign := func(seg *models.PublicProofManifestSegment) {
		seg.ProofRootSHA256 = models.ComputePublicProofSegmentRoot(*seg)
		root, err := hex.DecodeString(seg.ProofRootSHA256)
		require.NoError(t, err)
		seg.Signature = hex.EncodeToString(ed25519.Sign(priv, root))
	}
	sign(&base)
	cases := []struct {
		name    string
		prepare func() []models.PublicProofManifestSegment
		want    error
	}{
		{"invalid signature", func() []models.PublicProofManifestSegment {
			seg := base
			seg.Signature = hex.EncodeToString(make([]byte, ed25519.SignatureSize))
			return []models.PublicProofManifestSegment{seg}
		}, constants.ErrPublicFeedSignatureInvalid},
		{"mixed sources", func() []models.PublicProofManifestSegment {
			seg := base
			seg.SourceID = "different-source"
			seg.SegmentIndex = 2
			seg.PreviousRoot = base.ProofRootSHA256
			sign(&seg)
			return []models.PublicProofManifestSegment{base, seg}
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"unknown withdrawal", func() []models.PublicProofManifestSegment {
			seg := base
			seg.WithdrawnArtifactIDs = []string{constants.PublicFeedZeroHashHex}
			sign(&seg)
			return []models.PublicProofManifestSegment{seg}
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"instructions tamper", func() []models.PublicProofManifestSegment {
			seg := base
			seg.VerifierInstructions = "forged"
			return []models.PublicProofManifestSegment{seg}
		}, constants.ErrPublicFeedProofSegmentInvalid},
		{"artifact traversal", func() []models.PublicProofManifestSegment {
			seg := base
			seg.Entries = []models.PublicProofCatalogEntry{{ArtifactID: "../escape", SHA256: "../escape", Classification: models.PublicFeedProofClassificationPublicSafe}}
			sign(&seg)
			return []models.PublicProofManifestSegment{seg}
		}, constants.ErrPublicFeedProofCatalogMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			var log bytes.Buffer
			for _, seg := range tc.prepare() {
				require.NoError(t, json.NewEncoder(&log).Encode(seg))
			}
			require.NoError(t, fileSvc.WriteFile(context.Background(), "proof-segments.jsonl", log.Bytes(), constants.PermFilePrivate))
			cmd := publicVerifyProofCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--segments", fileSvc.Resolve("proof-segments.jsonl"), "--artifacts", fileSvc.Resolve("proofs"), "--public-key", hex.EncodeToString(pub)})
			require.ErrorIs(t, cmd.Execute(), tc.want)
		})
	}
}

func TestVerifyProofCmd_ValidatesKeyRegistryWithRotation(t *testing.T) {
	pub1, priv1, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyID1 := "key-1"

	pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyID2 := "key-2"

	tempDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tempDir, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, fileSvc.MkdirAll(ctx, "proofs", constants.PermDirPrivate))

	// Artifact 1
	content1 := []byte(`{"data":"artifact 1"}`)
	h1 := sha256.Sum256(content1)
	id1 := hex.EncodeToString(h1[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id1), content1, constants.PermFilePrivate))

	// Artifact 2
	content2 := []byte(`{"data":"artifact 2"}`)
	h2 := sha256.Sum256(content2)
	id2 := hex.EncodeToString(h2[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id2), content2, constants.PermFilePrivate))

	// Segment 1 signed with key 1
	seg1 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     1,
		PreviousRoot:     constants.PublicFeedZeroHashHex,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		Entries: []models.PublicProofCatalogEntry{
			{
				ArtifactID:          id1,
				Filename:            "art1.json",
				MediaType:           "application/json",
				ByteSize:            int64(len(content1)),
				SHA256:              id1,
				Classification:      models.PublicFeedProofClassificationPublicSafe,
				CampaignID:          "c1",
				GeneratedAt:         time.Now().UTC(),
				VerificationCommand: "sha256sum art1.json",
				ImmutableURL:        "/proofs/" + id1,
			},
		},
		GeneratedAt:  time.Now().UTC(),
		SigningKeyID: keyID1,
	}
	root1 := models.ComputePublicProofSegmentRoot(seg1)
	seg1.ProofRootSHA256 = root1
	root1Bytes, _ := hex.DecodeString(root1)
	seg1.Signature = hex.EncodeToString(ed25519.Sign(priv1, root1Bytes))

	// Segment 2 signed with key 2 (rotated)
	seg2 := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     2,
		PreviousRoot:     root1,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		Entries: []models.PublicProofCatalogEntry{
			{
				ArtifactID:          id2,
				Filename:            "art2.json",
				MediaType:           "application/json",
				ByteSize:            int64(len(content2)),
				SHA256:              id2,
				Classification:      models.PublicFeedProofClassificationPublicSafe,
				CampaignID:          "c1",
				GeneratedAt:         time.Now().UTC(),
				VerificationCommand: "sha256sum art2.json",
				ImmutableURL:        "/proofs/" + id2,
			},
		},
		GeneratedAt:  time.Now().UTC(),
		SigningKeyID: keyID2,
	}
	root2 := models.ComputePublicProofSegmentRoot(seg2)
	seg2.ProofRootSHA256 = root2
	root2Bytes, _ := hex.DecodeString(root2)
	seg2.Signature = hex.EncodeToString(ed25519.Sign(priv2, root2Bytes))

	line1, _ := json.Marshal(seg1)
	line2, _ := json.Marshal(seg2)
	segmentsContent := append(append(line1, '\n'), append(line2, '\n')...)
	require.NoError(t, fileSvc.WriteFile(ctx, "proof-segments.jsonl", segmentsContent, constants.PermFilePrivate))

	// Key registry JSON
	registryJSON, _ := json.Marshal(map[string]string{
		keyID1: hex.EncodeToString(pub1),
		keyID2: hex.EncodeToString(pub2),
	})
	require.NoError(t, fileSvc.WriteFile(ctx, "key-registry.json", registryJSON, constants.PermFilePrivate))

	cmd := publicVerifyProofCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"--segments", fileSvc.Resolve("proof-segments.jsonl"),
		"--artifacts", fileSvc.Resolve("proofs"),
		"--key-registry", fileSvc.Resolve("key-registry.json"),
	})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, out.String(), "Proof package verified (2 segments, 2 active artifacts")
}

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

// pointerChainFixture is a two-segment proof log. Segment 1 is signed under
// key 1 and segment 2 under key 2, so the chain spans a key rotation. The
// registry pins both keys by key ID. Artifacts are written under proofs/.
type pointerChainFixture struct {
	fileSvc      fs.RuntimeFileService
	segmentsPath string
	artifactsDir string
	registryPath string
	headRoot     string
	signingKey   ed25519.PrivateKey
	signingKeyID string
}

func newPointerChainFixture(t *testing.T) pointerChainFixture {
	t.Helper()
	pub1, priv1, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pub2, priv2, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	// Pointers embed their key, so signing_key_id must be the key's digest ID.
	keyID1, keyID2 := models.PublicProofSigningKeyID(pub1), models.PublicProofSigningKeyID(pub2)

	tempDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tempDir, testutil.NewTestLogger())
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, fileSvc.MkdirAll(ctx, "proofs", constants.PermDirPrivate))

	content1 := []byte(`{"data":"artifact 1"}`)
	id1Sum := sha256.Sum256(content1)
	id1 := hex.EncodeToString(id1Sum[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id1), content1, constants.PermFilePrivate))

	content2 := []byte(`{"data":"artifact 2"}`)
	id2Sum := sha256.Sum256(content2)
	id2 := hex.EncodeToString(id2Sum[:])
	require.NoError(t, fileSvc.WriteFile(ctx, filepath.Join("proofs", id2), content2, constants.PermFilePrivate))

	seg1 := signedPointerTestSegment(t, 1, constants.PublicFeedZeroHashHex, id1, int64(len(content1)), keyID1, priv1)
	seg2 := signedPointerTestSegment(t, 2, seg1.ProofRootSHA256, id2, int64(len(content2)), keyID2, priv2)

	line1, err := json.Marshal(seg1)
	require.NoError(t, err)
	line2, err := json.Marshal(seg2)
	require.NoError(t, err)
	segments := append(append(line1, '\n'), append(line2, '\n')...)
	require.NoError(t, fileSvc.WriteFile(ctx, "proof-segments.jsonl", segments, constants.PermFilePrivate))

	registry, err := json.Marshal(map[string]string{
		keyID1: hex.EncodeToString(pub1),
		keyID2: hex.EncodeToString(pub2),
	})
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(ctx, "key-registry.json", registry, constants.PermFilePrivate))

	return pointerChainFixture{
		fileSvc:      fileSvc,
		segmentsPath: fileSvc.Resolve("proof-segments.jsonl"),
		artifactsDir: fileSvc.Resolve("proofs"),
		registryPath: fileSvc.Resolve("key-registry.json"),
		headRoot:     seg2.ProofRootSHA256,
		signingKey:   priv2,
		signingKeyID: keyID2,
	}
}

func signedPointerTestSegment(t *testing.T, index int64, previousRoot, artifactID string, byteSize int64, keyID string, priv ed25519.PrivateKey) models.PublicProofManifestSegment {
	t.Helper()
	seg := models.PublicProofManifestSegment{
		SchemaVersion:    constants.PublicProofSegmentSchemaVersion,
		SourceID:         "src-1",
		SegmentIndex:     index,
		PreviousRoot:     previousRoot,
		CampaignID:       "c1",
		CampaignRevision: "rev-1",
		VerificationOK:   true,
		Entries: []models.PublicProofCatalogEntry{{
			ArtifactID:          artifactID,
			Filename:            "art.json",
			MediaType:           "application/json",
			ByteSize:            byteSize,
			SHA256:              artifactID,
			Classification:      models.PublicFeedProofClassificationPublicSafe,
			CampaignID:          "c1",
			GeneratedAt:         time.Now().UTC(),
			VerificationCommand: "sha256sum art.json",
			ImmutableURL:        "/proofs/" + artifactID,
		}},
		GeneratedAt:  time.Now().UTC(),
		SigningKeyID: keyID,
	}
	seg.ProofRootSHA256 = models.ComputePublicProofSegmentRoot(seg)
	rootBytes, err := hex.DecodeString(seg.ProofRootSHA256)
	require.NoError(t, err)
	seg.Signature = hex.EncodeToString(ed25519.Sign(priv, rootBytes))
	return seg
}

// pointerForChain returns an unsigned pointer naming the given head root.
func pointerForChain(headRoot string) models.PublicFeedPointer {
	readModel := sha256.Sum256([]byte("read-model"))
	feedChain := sha256.Sum256([]byte("feed-chain"))
	return models.PublicFeedPointer{
		SchemaVersion:     constants.PublicFeedPointerSchemaVersion,
		SourceID:          "src-1",
		Revision:          4,
		ReadModelSHA256:   hex.EncodeToString(readModel[:]),
		ProofRootSHA256:   headRoot,
		ProofSegmentHead:  2,
		HighWaterSequence: 12,
		FeedChainHash:     hex.EncodeToString(feedChain[:]),
		GeneratedAt:       time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	}
}

// writePointerFile writes body as pointer.json under the fixture runtime root
// and returns its absolute path.
func (f pointerChainFixture) writePointerFile(t *testing.T, body []byte) string {
	t.Helper()
	require.NoError(t, f.fileSvc.WriteFile(context.Background(), "pointer.json", body, constants.PermFilePrivate))
	return f.fileSvc.Resolve("pointer.json")
}

func runVerifyProofWithPointer(t *testing.T, f pointerChainFixture, pointerPath string) (string, error) {
	t.Helper()
	cmd := publicVerifyProofCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"--segments", f.segmentsPath,
		"--artifacts", f.artifactsDir,
		"--key-registry", f.registryPath,
		"--pointer", pointerPath,
	})
	err := cmd.Execute()
	return out.String(), err
}

func TestVerifyProofCmd_PointerBoundToChain(t *testing.T) {
	t.Run("valid signed pointer across key rotation", func(t *testing.T) {
		f := newPointerChainFixture(t)
		signed, err := models.SignPublicFeedPointer(pointerForChain(f.headRoot), f.signingKey, f.signingKeyID)
		require.NoError(t, err)
		body, err := json.Marshal(signed)
		require.NoError(t, err)

		out, err := runVerifyProofWithPointer(t, f, f.writePointerFile(t, body))
		require.NoError(t, err)
		assert.Contains(t, out, "Proof package verified (2 segments, 2 active artifacts")
		assert.Contains(t, out, "Pointer verified (revision 4, source src-1)")
	})

	t.Run("pointer root disagrees with chain", func(t *testing.T) {
		f := newPointerChainFixture(t)
		other := sha256.Sum256([]byte("not the head root"))
		signed, err := models.SignPublicFeedPointer(pointerForChain(hex.EncodeToString(other[:])), f.signingKey, f.signingKeyID)
		require.NoError(t, err)
		body, err := json.Marshal(signed)
		require.NoError(t, err)

		_, err = runVerifyProofWithPointer(t, f, f.writePointerFile(t, body))
		assert.ErrorIs(t, err, constants.ErrPublicFeedPointerInvalid)
	})

	t.Run("unsigned pointer", func(t *testing.T) {
		f := newPointerChainFixture(t)
		body, err := json.Marshal(pointerForChain(f.headRoot))
		require.NoError(t, err)

		_, err = runVerifyProofWithPointer(t, f, f.writePointerFile(t, body))
		assert.ErrorIs(t, err, constants.ErrPublicFeedSignatureInvalid)
	})

	t.Run("pointer with unknown field", func(t *testing.T) {
		f := newPointerChainFixture(t)
		_, err := runVerifyProofWithPointer(t, f, f.writePointerFile(t, []byte(`{"schema_version":"x","bogus":true}`)))
		assert.ErrorIs(t, err, constants.ErrPublicFeedPointerInvalid)
	})
}

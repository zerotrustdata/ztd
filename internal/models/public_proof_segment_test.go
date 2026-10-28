// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestPublicProofSegmentRoot_BindsEveryFieldWithoutAmbiguousConcatenation(t *testing.T) {
	digest := sha256.Sum256([]byte("proof"))
	id := hex.EncodeToString(digest[:])
	base := PublicProofManifestSegment{
		SchemaVersion: constants.PublicProofSegmentSchemaVersion, SourceID: "source", SegmentIndex: 1,
		PreviousRoot: constants.PublicFeedZeroHashHex, CampaignID: "ab", CampaignRevision: "c",
		VerificationOK: true, GeneratedAt: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC), SigningKeyID: "key",
		Entries: []PublicProofCatalogEntry{{ArtifactID: id, SHA256: id, Filename: "proof.json", MediaType: "application/json", ByteSize: 5,
			Classification: PublicFeedProofClassificationPublicSafe, CampaignID: "campaign", SourceRunID: "run", GeneratedAt: time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC), VerificationCommand: "sha256sum proof.json", ImmutableURL: "/proofs/" + id}},
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	base.ProofRootSHA256 = ComputePublicProofSegmentRoot(base)
	root, err := hex.DecodeString(base.ProofRootSHA256)
	require.NoError(t, err)
	base.Signature = hex.EncodeToString(ed25519.Sign(priv, root))
	require.NoError(t, VerifyPublicProofSegment(base.SourceID, base, pub))
	cases := map[string]func(*PublicProofManifestSegment){
		"schema":                      func(s *PublicProofManifestSegment) { s.SchemaVersion = "2" },
		"source":                      func(s *PublicProofManifestSegment) { s.SourceID = "other" },
		"index":                       func(s *PublicProofManifestSegment) { s.SegmentIndex++ },
		"previous root":               func(s *PublicProofManifestSegment) { s.PreviousRoot = id },
		"ambiguous campaign boundary": func(s *PublicProofManifestSegment) { s.CampaignID = "a"; s.CampaignRevision = "bc" },
		"index generation":            func(s *PublicProofManifestSegment) { s.VerifiedIndexGenerationHash = id },
		"verification":                func(s *PublicProofManifestSegment) { s.VerificationOK = false },
		"artifact id":                 func(s *PublicProofManifestSegment) { s.Entries[0].ArtifactID = constants.PublicFeedZeroHashHex },
		"artifact hash":               func(s *PublicProofManifestSegment) { s.Entries[0].SHA256 = constants.PublicFeedZeroHashHex },
		"filename":                    func(s *PublicProofManifestSegment) { s.Entries[0].Filename = "other.json" },
		"media type":                  func(s *PublicProofManifestSegment) { s.Entries[0].MediaType = "text/plain" },
		"size":                        func(s *PublicProofManifestSegment) { s.Entries[0].ByteSize++ },
		"classification":              func(s *PublicProofManifestSegment) { s.Entries[0].Classification = "private" },
		"campaign":                    func(s *PublicProofManifestSegment) { s.Entries[0].CampaignID = "other" },
		"run":                         func(s *PublicProofManifestSegment) { s.Entries[0].SourceRunID = "other" },
		"entry time": func(s *PublicProofManifestSegment) {
			s.Entries[0].GeneratedAt = s.Entries[0].GeneratedAt.Add(time.Second)
		},
		"verification command": func(s *PublicProofManifestSegment) { s.Entries[0].VerificationCommand = "other" },
		"url":                  func(s *PublicProofManifestSegment) { s.Entries[0].ImmutableURL = "other" },
		"withdrawal":           func(s *PublicProofManifestSegment) { s.WithdrawnArtifactIDs = []string{id} },
		"time":                 func(s *PublicProofManifestSegment) { s.GeneratedAt = s.GeneratedAt.Add(time.Second) },
		"key id":               func(s *PublicProofManifestSegment) { s.SigningKeyID = "other" },
		"instructions":         func(s *PublicProofManifestSegment) { s.VerifierInstructions = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Entries = append([]PublicProofCatalogEntry(nil), base.Entries...)
			mutate(&changed)
			require.NotEqual(t, base.ProofRootSHA256, ComputePublicProofSegmentRoot(changed))
			require.Error(t, VerifyPublicProofSegment(base.SourceID, changed, pub))
		})
	}
	require.ErrorIs(t, VerifyPublicProofSegment("different-source", base, pub), constants.ErrPublicFeedProofSegmentInvalid)
	equivalent := base
	equivalent.GeneratedAt = base.GeneratedAt.In(time.FixedZone("other", 3600))
	equivalent.ProofRootSHA256 = "excluded"
	equivalent.Signature = "excluded"
	equivalent.SigningPublicKey = hex.EncodeToString(pub)
	require.Equal(t, base.ProofRootSHA256, ComputePublicProofSegmentRoot(equivalent))
	require.ErrorIs(t, VerifyPublicProofSegment(base.SourceID, equivalent, pub), constants.ErrPublicFeedProofSegmentInvalid)
}

func TestPublicProofSegment_PublishedKeyIsBoundBySigningKeyID(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	published, ok := PublicProofSigningKeyHex(pub, PublicProofSigningKeyID(pub))
	require.True(t, ok)
	_, ok = PublicProofSigningKeyHex(pub, "test-key-1")
	require.False(t, ok)

	seg := PublicProofManifestSegment{
		SchemaVersion: constants.PublicProofSegmentSchemaVersion, SourceID: "source", SegmentIndex: 1,
		PreviousRoot: constants.PublicFeedZeroHashHex, VerificationOK: true,
		GeneratedAt:  time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC),
		SigningKeyID: PublicProofSigningKeyID(pub), SigningPublicKey: published,
	}
	seg.ProofRootSHA256 = ComputePublicProofSegmentRoot(seg)
	root, err := hex.DecodeString(seg.ProofRootSHA256)
	require.NoError(t, err)
	seg.Signature = hex.EncodeToString(ed25519.Sign(priv, root))
	require.NoError(t, VerifyPublicProofSegment(seg.SourceID, seg, pub))
	require.ErrorIs(t, VerifyPublicProofSegment(seg.SourceID, seg, other), constants.ErrPublicFeedSignatureInvalid)

	forged := seg
	forged.SigningPublicKey = hex.EncodeToString(other)
	require.Equal(t, seg.ProofRootSHA256, ComputePublicProofSegmentRoot(forged))
	require.ErrorIs(t, VerifyPublicProofSegment(seg.SourceID, forged, other), constants.ErrPublicFeedProofSegmentInvalid)
}

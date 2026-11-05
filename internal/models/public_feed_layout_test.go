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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func testPublicFeedPointer() PublicFeedPointer {
	readModel := sha256.Sum256([]byte("read-model"))
	chain := sha256.Sum256([]byte("feed-chain"))
	return PublicFeedPointer{
		SchemaVersion:     constants.PublicFeedPointerSchemaVersion,
		SourceID:          "src-1",
		Revision:          7,
		ReadModelSHA256:   hex.EncodeToString(readModel[:]),
		FeedChainHash:     hex.EncodeToString(chain[:]),
		HighWaterSequence: 40,
		GeneratedAt:       time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
	}
}

func TestVerifyPublicFeedPointer_SignedPointerVerifies(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signed, err := SignPublicFeedPointer(testPublicFeedPointer(), priv, PublicProofSigningKeyID(pub))
	require.NoError(t, err)
	require.NoError(t, VerifyPublicFeedPointer(signed, pub))
}

func TestVerifyPublicFeedPointer_Rejections(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	keyID := PublicProofSigningKeyID(pub)
	otherHash := sha256.Sum256([]byte("tampered"))

	cases := map[string]struct {
		mutate  func(*PublicFeedPointer)
		key     ed25519.PublicKey
		wantErr error
	}{
		"tampered read model digest": {
			mutate:  func(p *PublicFeedPointer) { p.ReadModelSHA256 = hex.EncodeToString(otherHash[:]) },
			key:     pub,
			wantErr: constants.ErrPublicFeedSignatureInvalid,
		},
		"wrong verification key": {
			key:     otherPub,
			wantErr: constants.ErrPublicFeedSignatureInvalid,
		},
		"published key id differs from signing_key_id": {
			mutate:  func(p *PublicFeedPointer) { p.SigningKeyID = "not-the-key-id" },
			key:     pub,
			wantErr: constants.ErrPublicFeedPointerInvalid,
		},
		"bad schema version": {
			mutate:  func(p *PublicFeedPointer) { p.SchemaVersion = "0" },
			key:     pub,
			wantErr: constants.ErrPublicFeedPointerInvalid,
		},
		"revision zero": {
			mutate:  func(p *PublicFeedPointer) { p.Revision = 0 },
			key:     pub,
			wantErr: constants.ErrPublicFeedPointerInvalid,
		},
		"malformed read model hash": {
			mutate:  func(p *PublicFeedPointer) { p.ReadModelSHA256 = "not-hex" },
			key:     pub,
			wantErr: constants.ErrPublicFeedPointerInvalid,
		},
		"uppercase feed chain hash": {
			mutate: func(p *PublicFeedPointer) {
				p.FeedChainHash = "A" + p.FeedChainHash[1:]
			},
			key:     pub,
			wantErr: constants.ErrPublicFeedPointerInvalid,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			signed, err := SignPublicFeedPointer(testPublicFeedPointer(), priv, keyID)
			require.NoError(t, err)
			if tc.mutate != nil {
				tc.mutate(&signed)
			}
			err = VerifyPublicFeedPointer(signed, tc.key)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

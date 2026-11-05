// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// PublicBucketConfig names the S3-compatible bucket the public layout is
// published to. Credentials are sealed separately at
// constants.PublicFeedBucketCredentialsPath and never appear here.
type PublicBucketConfig struct {
	// Name is the bucket name.
	Name string `json:"name"`
	// Endpoint is the S3 API origin, for example
	// https://<account>.r2.cloudflarestorage.com. Requests use path-style keys.
	Endpoint string `json:"endpoint"`
	// PublicOrigin is the bucket's public origin, used to name purged URLs.
	PublicOrigin string `json:"public_origin"`
	// ZoneID is the Cloudflare zone whose cache is purged on withdrawal.
	ZoneID string `json:"zone_id"`
}

// PublicBucketCredentials are the sealed bucket write and cache purge
// credentials. They are read from a file or stdin, never from a flag value.
type PublicBucketCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	PurgeToken      string `json:"purge_token"`
}

// PublicBucketStatus is the bucket publisher's state, read from its journal.
type PublicBucketStatus struct {
	Name            string `json:"name"`
	PointerRevision int64  `json:"pointer_revision"`
	// PointerSequence is the mirror high-water sequence the last written pointer
	// names, compared against HighWaterSequence to report how far the bucket lags.
	PointerSequence int64 `json:"pointer_sequence"`
	PendingDeletes  int   `json:"pending_deletes"`
	PendingPurges   int   `json:"pending_purges"`
}

// PublicFeedPointer is pointer.json: the one mutable object that names the
// current digest-addressed read model and proof catalog. It is written last, so
// a partial upload never goes live. Digests cover uncompressed bytes, which is
// what a browser can hash after transport decoding.
type PublicFeedPointer struct {
	SchemaVersion string `json:"schema_version"`
	SourceID      string `json:"source_id"`
	// Revision increases with every pointer a source writes.
	Revision           int64  `json:"revision"`
	ReadModelSHA256    string `json:"read_model_sha256"`
	ProofCatalogSHA256 string `json:"proof_catalog_sha256,omitempty"`
	ProofRootSHA256    string `json:"proof_root_sha256,omitempty"`
	ProofSegmentHead   int64  `json:"proof_segment_head"`
	HighWaterSequence  int64  `json:"high_water_sequence"`
	// HistoryFirstSequence is the first retained sequence of the signed batch chain.
	HistoryFirstSequence int64  `json:"history_first_sequence"`
	FeedChainHash        string `json:"feed_chain_hash"`
	WithdrawalDigest     string `json:"withdrawal_digest,omitempty"`
	// Active reports whether a run is executing, so browsers poll live/status.json.
	Active      bool      `json:"active"`
	GeneratedAt time.Time `json:"generated_at"`
	// SigningKeyID, SigningPublicKey, and Signature are empty on a pointer served
	// by a mirror that does not hold the source's signing key.
	SigningKeyID     string `json:"signing_key_id,omitempty"`
	SigningPublicKey string `json:"signing_public_key,omitempty"`
	Signature        string `json:"signature,omitempty"`
}

// PublicFeedPointerSigningDigest is the domain-separated digest a pointer's
// signature covers, which is every field except the signature.
func PublicFeedPointerSigningDigest(pointer PublicFeedPointer) ([]byte, error) {
	pointer.Signature = ""
	encoded, err := json.Marshal(pointer)
	if err != nil {
		return nil, fmt.Errorf("public-feed: encode pointer: %w", err)
	}
	digest := sha256.New()
	digest.Write([]byte(publicFeedPointerSigningDomain))
	digest.Write(encoded)
	return digest.Sum(nil), nil
}

const publicFeedPointerSigningDomain = "ztd-public-feed-pointer-v1\n"

// SignPublicFeedPointer returns the pointer signed with key under keyID, with
// the public key published beside the signature.
func SignPublicFeedPointer(pointer PublicFeedPointer, key ed25519.PrivateKey, keyID string) (PublicFeedPointer, error) {
	if len(key) != ed25519.PrivateKeySize || keyID == "" {
		return PublicFeedPointer{}, constants.ErrPublicFeedPointerInvalid
	}
	pointer.SigningKeyID = keyID
	pointer.SigningPublicKey = hex.EncodeToString(key.Public().(ed25519.PublicKey))
	digest, err := PublicFeedPointerSigningDigest(pointer)
	if err != nil {
		return PublicFeedPointer{}, err
	}
	pointer.Signature = hex.EncodeToString(ed25519.Sign(key, digest))
	return pointer, nil
}

// VerifyPublicFeedPointer validates a pointer's shape and its signature under a
// trusted key. Callers additionally decide which key to trust, as they do for
// proof segments, so a rotated key keeps verifying through a key registry.
func VerifyPublicFeedPointer(pointer PublicFeedPointer, key ed25519.PublicKey) error {
	if pointer.SchemaVersion != constants.PublicFeedPointerSchemaVersion || pointer.SourceID == "" || pointer.Revision <= 0 ||
		!validProofHash(pointer.ReadModelSHA256) || !validProofHash(pointer.FeedChainHash) ||
		(pointer.ProofCatalogSHA256 != "" && !validProofHash(pointer.ProofCatalogSHA256)) ||
		(pointer.ProofRootSHA256 != "" && !validProofHash(pointer.ProofRootSHA256)) ||
		(pointer.WithdrawalDigest != "" && !validProofHash(pointer.WithdrawalDigest)) ||
		pointer.HighWaterSequence < 0 || pointer.ProofSegmentHead < 0 || pointer.GeneratedAt.IsZero() || pointer.SigningKeyID == "" {
		return constants.ErrPublicFeedPointerInvalid
	}
	if pointer.SigningPublicKey != "" {
		published, err := DecodePublicProofSigningKey(pointer.SigningPublicKey)
		if err != nil || PublicProofSigningKeyID(published) != pointer.SigningKeyID {
			return constants.ErrPublicFeedPointerInvalid
		}
		if !published.Equal(key) {
			return constants.ErrPublicFeedSignatureInvalid
		}
	}
	signature, err := hex.DecodeString(pointer.Signature)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return constants.ErrPublicFeedSignatureInvalid
	}
	digest, err := PublicFeedPointerSigningDigest(pointer)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, digest, signature) {
		return constants.ErrPublicFeedSignatureInvalid
	}
	return nil
}

// PublicLiveRun is one executing run in live/status.json.
type PublicLiveRun struct {
	DatasetID      string `json:"dataset_id"`
	RunID          string `json:"run_id"`
	LifecycleState string `json:"lifecycle_state"`
}

// PublicLiveStatus is live/status.json: what a browser needs between pointer
// polls. Tail holds the newest records, oldest first, in the wire shape of a run
// bundle line, excluding assignment results, which arrive through
// live/runs/<run>.jsonl.gz.
type PublicLiveStatus struct {
	SchemaVersion string             `json:"schema_version"`
	Snapshot      PublicFeedSnapshot `json:"snapshot"`
	ActiveRuns    []PublicLiveRun    `json:"active_runs"`
	Tail          []PublicLiveRecord `json:"tail"`
}

// PublicLiveRecord is one feed record in the live status tail.
type PublicLiveRecord struct {
	Sequence    int64                `json:"sequence"`
	RecordType  PublicFeedRecordType `json:"record_type"`
	RecordBytes string               `json:"record_bytes"`
}

// PublicBucketJournalKind is what one bucket journal line records.
type PublicBucketJournalKind string

const (
	// PublicBucketJournalPointer records a pointer that reached the bucket.
	PublicBucketJournalPointer PublicBucketJournalKind = "pointer"
	// PublicBucketJournalDelete records an object key still to delete.
	PublicBucketJournalDelete PublicBucketJournalKind = "delete"
	// PublicBucketJournalDeleted clears a pending delete.
	PublicBucketJournalDeleted PublicBucketJournalKind = "deleted"
	// PublicBucketJournalPurge records a public URL whose cache is still to purge.
	PublicBucketJournalPurge PublicBucketJournalKind = "purge"
	// PublicBucketJournalPurged clears a pending purge.
	PublicBucketJournalPurged PublicBucketJournalKind = "purged"
)

// PublicBucketJournalEntry is one line of constants.PublicFeedBucketJournalPath.
type PublicBucketJournalEntry struct {
	Kind PublicBucketJournalKind `json:"kind"`
	// Key is the object key of a delete or the URL of a purge.
	Key string `json:"key,omitempty"`
	// Revision, Sequence, and WithdrawalDigest are set on a pointer line.
	Revision         int64  `json:"revision,omitempty"`
	Sequence         int64  `json:"sequence,omitempty"`
	WithdrawalDigest string `json:"withdrawal_digest,omitempty"`
}

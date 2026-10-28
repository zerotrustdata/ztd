// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/fileidentity"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// blobVerifier owns content verification of weight files, keyed by path. A
// file is streamed through SHA-256 once per file identity; later attestations
// re-read only the identity and rest on that hash. Concurrent requests for one
// file share a single in-flight hash, and full hashes across files are bounded
// by ModelProvenanceHashConcurrency.
type blobVerifier struct {
	now       func() time.Time
	hashSlots chan struct{}

	mu       sync.Mutex
	verified map[string]verifiedBlob
	inflight map[string]chan struct{}
}

type verifiedBlob struct {
	identity        fileidentity.Identity
	digestHex       string
	hashCompletedAt time.Time
}

type blobVerification struct {
	digestHex       string
	sizeBytes       int64
	mode            evalv1.ModelWeightVerificationMode
	hashCompletedAt time.Time
}

func newBlobVerifier(now func() time.Time) *blobVerifier {
	return &blobVerifier{
		now:       now,
		hashSlots: make(chan struct{}, constants.ModelProvenanceHashConcurrency),
		verified:  make(map[string]verifiedBlob),
		inflight:  make(map[string]chan struct{}),
	}
}

// verify reports the SHA-256 of the file at path, hashing only when no settled
// hash covers the file's current identity. A non-empty expectedDigest is the
// content address the file must hold; a different digest fails closed with
// ErrModelProvenanceBlobDigestMismatch.
func (v *blobVerifier) verify(ctx context.Context, path, expectedDigest string) (blobVerification, error) {
	verification, err := v.digest(ctx, path)
	if err != nil {
		return blobVerification{}, err
	}
	if expectedDigest != "" && verification.digestHex != expectedDigest {
		return blobVerification{}, fmt.Errorf("model provenance attestor: %s: %w", path, constants.ErrModelProvenanceBlobDigestMismatch)
	}
	return verification, nil
}

func (v *blobVerifier) digest(ctx context.Context, path string) (blobVerification, error) {
	for {
		identity, err := fileidentity.Stat(path)
		if err != nil {
			return blobVerification{}, fmt.Errorf("model provenance attestor: file identity: %w", err)
		}
		v.mu.Lock()
		if cached, ok := v.verified[path]; ok && cached.identity.Equal(identity) {
			v.mu.Unlock()
			return blobVerification{
				digestHex:       cached.digestHex,
				sizeBytes:       identity.Info.Size(),
				mode:            evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED,
				hashCompletedAt: cached.hashCompletedAt,
			}, nil
		}
		if wait, ok := v.inflight[path]; ok {
			v.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return blobVerification{}, ctx.Err()
			}
		}
		delete(v.verified, path)
		done := make(chan struct{})
		v.inflight[path] = done
		v.mu.Unlock()

		verification, hashed, settled, err := v.hash(ctx, path)
		v.mu.Lock()
		delete(v.inflight, path)
		if err == nil && settled {
			v.verified[path] = verifiedBlob{identity: hashed, digestHex: verification.digestHex, hashCompletedAt: verification.hashCompletedAt}
		}
		v.mu.Unlock()
		close(done)
		return verification, err
	}
}

// hash streams one file through SHA-256. settled reports whether the file's
// identity predates the hash by the settle window, so the hash may be reused.
func (v *blobVerifier) hash(ctx context.Context, path string) (blobVerification, fileidentity.Identity, bool, error) {
	select {
	case v.hashSlots <- struct{}{}:
		defer func() { <-v.hashSlots }()
	case <-ctx.Done():
		return blobVerification{}, fileidentity.Identity{}, false, ctx.Err()
	}
	file, err := os.Open(path)
	if err != nil {
		return blobVerification{}, fileidentity.Identity{}, false, fmt.Errorf("model provenance attestor: open %s: %w", path, err)
	}
	defer file.Close()

	startedAt := v.now()
	before, err := fileidentity.Read(file)
	if err != nil {
		return blobVerification{}, fileidentity.Identity{}, false, err
	}
	hasher := sha256.New()
	size, err := io.CopyBuffer(hasher, &provenanceContextReader{ctx: ctx, reader: file}, make([]byte, constants.ModelProvenanceHashBufferBytes))
	if err != nil {
		return blobVerification{}, fileidentity.Identity{}, false, fmt.Errorf("model provenance attestor: hash %s: %w", path, err)
	}
	if ctx.Err() != nil {
		return blobVerification{}, fileidentity.Identity{}, false, ctx.Err()
	}
	after, err := fileidentity.Read(file)
	if err != nil {
		return blobVerification{}, fileidentity.Identity{}, false, err
	}
	if !before.Equal(after) || size != after.Info.Size() {
		return blobVerification{}, fileidentity.Identity{}, false, fmt.Errorf("model provenance attestor: %s: %w", path, constants.ErrModelProvenanceBlobChanged)
	}
	settled := after.LastChangedAt().Before(startedAt.Add(-constants.ModelProvenanceBlobIdentitySettleWindow))
	return blobVerification{
		digestHex:       hex.EncodeToString(hasher.Sum(nil)),
		sizeBytes:       size,
		mode:            evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FULL_HASH,
		hashCompletedAt: v.now(),
	}, after, settled, nil
}

// Check cancellation between filesystem reads rather than after the entire
// multi-gigabyte blob. An in-flight OS read still completes normally.
type provenanceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *provenanceContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

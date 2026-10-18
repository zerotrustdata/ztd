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
	"path/filepath"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// blobVerifier owns content verification of content-addressed weight blobs.
// A blob is streamed through SHA-256 once per file identity; later
// attestations re-read only the identity and rest on that hash. Concurrent
// requests for one blob share a single in-flight hash, and full hashes across
// blobs are bounded by ModelProvenanceHashConcurrency.
type blobVerifier struct {
	blobsDir  string
	now       func() time.Time
	hashSlots chan struct{}

	mu       sync.Mutex
	verified map[string]verifiedBlob
	inflight map[string]chan struct{}
}

type verifiedBlob struct {
	identity        blobIdentity
	hashCompletedAt time.Time
}

type blobVerification struct {
	sizeBytes       int64
	mode            evalv1.ModelWeightVerificationMode
	hashCompletedAt time.Time
}

// blobIdentity is the filesystem identity a cached hash is bound to. Any
// write moves the change time, and a replaced file has a different file ID.
type blobIdentity struct {
	info         os.FileInfo
	sizeBytes    int64
	modTimeNanos int64
	changeNanos  int64
}

func (a blobIdentity) equal(b blobIdentity) bool {
	return os.SameFile(a.info, b.info) && a.sizeBytes == b.sizeBytes && a.modTimeNanos == b.modTimeNanos && a.changeNanos == b.changeNanos
}

func (a blobIdentity) lastChangedAt() time.Time {
	return time.Unix(0, max(a.modTimeNanos, a.changeNanos))
}

func newBlobVerifier(blobsDir string, now func() time.Time) *blobVerifier {
	return &blobVerifier{
		blobsDir:  blobsDir,
		now:       now,
		hashSlots: make(chan struct{}, constants.ModelProvenanceHashConcurrency),
		verified:  make(map[string]verifiedBlob),
		inflight:  make(map[string]chan struct{}),
	}
}

// verify confirms that blobs/sha256-<digestHex> holds content with that
// digest, hashing only when no settled hash covers the blob's current identity.
func (v *blobVerifier) verify(ctx context.Context, digestHex string) (blobVerification, error) {
	for {
		identity, err := v.currentIdentity(digestHex)
		if err != nil {
			return blobVerification{}, err
		}
		v.mu.Lock()
		if cached, ok := v.verified[digestHex]; ok && cached.identity.equal(identity) {
			v.mu.Unlock()
			return blobVerification{
				sizeBytes:       identity.sizeBytes,
				mode:            evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED,
				hashCompletedAt: cached.hashCompletedAt,
			}, nil
		}
		if wait, ok := v.inflight[digestHex]; ok {
			v.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return blobVerification{}, ctx.Err()
			}
		}
		delete(v.verified, digestHex)
		done := make(chan struct{})
		v.inflight[digestHex] = done
		v.mu.Unlock()

		verification, hashed, settled, err := v.hash(ctx, digestHex)
		v.mu.Lock()
		delete(v.inflight, digestHex)
		if err == nil && settled {
			v.verified[digestHex] = verifiedBlob{identity: hashed, hashCompletedAt: verification.hashCompletedAt}
		}
		v.mu.Unlock()
		close(done)
		return verification, err
	}
}

func (v *blobVerifier) blobPath(digestHex string) string {
	return filepath.Join(v.blobsDir, "sha256-"+digestHex)
}

func (v *blobVerifier) currentIdentity(digestHex string) (blobIdentity, error) {
	file, err := os.Open(v.blobPath(digestHex))
	if err != nil {
		return blobIdentity{}, fmt.Errorf("model provenance attestor: open blob %s: %w", digestHex, err)
	}
	defer file.Close()
	return readBlobIdentity(file)
}

// hash streams one blob through SHA-256. settled reports whether the blob's
// identity predates the hash by the settle window, so the hash may be reused.
func (v *blobVerifier) hash(ctx context.Context, digestHex string) (blobVerification, blobIdentity, bool, error) {
	select {
	case v.hashSlots <- struct{}{}:
		defer func() { <-v.hashSlots }()
	case <-ctx.Done():
		return blobVerification{}, blobIdentity{}, false, ctx.Err()
	}
	file, err := os.Open(v.blobPath(digestHex))
	if err != nil {
		return blobVerification{}, blobIdentity{}, false, fmt.Errorf("model provenance attestor: open blob %s: %w", digestHex, err)
	}
	defer file.Close()

	startedAt := v.now()
	before, err := readBlobIdentity(file)
	if err != nil {
		return blobVerification{}, blobIdentity{}, false, err
	}
	hasher := sha256.New()
	size, err := io.CopyBuffer(hasher, &provenanceContextReader{ctx: ctx, reader: file}, make([]byte, constants.ModelProvenanceHashBufferBytes))
	if err != nil {
		return blobVerification{}, blobIdentity{}, false, fmt.Errorf("model provenance attestor: hash blob %s: %w", digestHex, err)
	}
	if ctx.Err() != nil {
		return blobVerification{}, blobIdentity{}, false, ctx.Err()
	}
	if observed := hex.EncodeToString(hasher.Sum(nil)); observed != digestHex {
		return blobVerification{}, blobIdentity{}, false, fmt.Errorf("model provenance attestor: blob digest mismatch for %s", digestHex)
	}
	after, err := readBlobIdentity(file)
	if err != nil {
		return blobVerification{}, blobIdentity{}, false, err
	}
	if !before.equal(after) || size != after.sizeBytes {
		return blobVerification{}, blobIdentity{}, false, fmt.Errorf("model provenance attestor: blob %s: %w", digestHex, constants.ErrModelProvenanceBlobChanged)
	}
	settled := after.lastChangedAt().Before(startedAt.Add(-constants.ModelProvenanceBlobIdentitySettleWindow))
	return blobVerification{
		sizeBytes:       size,
		mode:            evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FULL_HASH,
		hashCompletedAt: v.now(),
	}, after, settled, nil
}

func readBlobIdentity(file *os.File) (blobIdentity, error) {
	info, err := file.Stat()
	if err != nil {
		return blobIdentity{}, fmt.Errorf("model provenance attestor: stat blob: %w", err)
	}
	changeNanos, err := fileChangeTimeNanos(file, info)
	if err != nil {
		return blobIdentity{}, err
	}
	return blobIdentity{info: info, sizeBytes: info.Size(), modTimeNanos: info.ModTime().UnixNano(), changeNanos: changeNanos}, nil
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

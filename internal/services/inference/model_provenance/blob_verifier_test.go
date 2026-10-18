// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	fullHash          = evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FULL_HASH
	identityUnchanged = evalv1.ModelWeightVerificationMode_MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED
)

// writeSettledBlob writes a content-addressed blob whose modification time
// predates the test, so a later write is always observable in its identity.
func writeSettledBlob(t *testing.T, dir string, body []byte) string {
	t.Helper()
	digest := models.SHA256Hex(body)
	path := filepath.Join(dir, "sha256-"+digest)
	require.NoError(t, os.WriteFile(path, body, 0o600))
	past := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(path, past, past))
	return digest
}

// A clock past the settle window lets blobs written by the test be cached.
func settledVerifier(dir string) *blobVerifier {
	return newBlobVerifier(dir, func() time.Time { return time.Now().Add(time.Hour) })
}

func TestBlobVerifier_ReusesHashUntilIdentityChanges(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	body := []byte("synthetic gguf payload")
	digest := writeSettledBlob(t, dir, body)
	verifier := settledVerifier(dir)

	first, err := verifier.verify(t.Context(), digest)
	require.NoError(t, err)
	assert.Equal(t, fullHash, first.mode)
	assert.Equal(t, int64(len(body)), first.sizeBytes)

	second, err := verifier.verify(t.Context(), digest)
	require.NoError(t, err)
	assert.Equal(t, identityUnchanged, second.mode)
	assert.Equal(t, first.hashCompletedAt, second.hashCompletedAt)

	// An in-place write invalidates the cached hash and fails closed.
	path := filepath.Join(dir, "sha256-"+digest)
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteAt([]byte("X"), 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = verifier.verify(t.Context(), digest)
	require.ErrorContains(t, err, "blob digest mismatch")

	// Restoring the content under a new file is hashed again before reuse.
	replacement := filepath.Join(dir, "replacement")
	require.NoError(t, os.WriteFile(replacement, body, 0o600))
	require.NoError(t, os.Rename(replacement, path))
	third, err := verifier.verify(t.Context(), digest)
	require.NoError(t, err)
	assert.Equal(t, fullHash, third.mode)
}

func TestBlobVerifier_RecentlyChangedBlobIsRehashed(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	body := []byte("fresh payload")
	digest := models.SHA256Hex(body)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sha256-"+digest), body, 0o600))
	verifier := newBlobVerifier(dir, time.Now)

	for range 2 {
		verification, err := verifier.verify(t.Context(), digest)
		require.NoError(t, err)
		assert.Equal(t, fullHash, verification.mode)
	}
}

func TestBlobVerifier_ConcurrentRequestsShareOneHash(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	digest := writeSettledBlob(t, dir, []byte("shared payload"))
	verifier := settledVerifier(dir)
	fillHashSlots(verifier)

	const requests = 5
	modes := make(chan evalv1.ModelWeightVerificationMode, requests)
	var wg sync.WaitGroup
	start := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			verification, err := verifier.verify(t.Context(), digest)
			assert.NoError(t, err)
			modes <- verification.mode
		}()
	}
	start()
	requireInflight(t, verifier, digest, true)
	for range requests - 1 {
		start()
	}
	drainHashSlots(verifier)
	wg.Wait()
	close(modes)

	counts := map[evalv1.ModelWeightVerificationMode]int{}
	for mode := range modes {
		counts[mode]++
	}
	assert.Equal(t, map[evalv1.ModelWeightVerificationMode]int{fullHash: 1, identityUnchanged: requests - 1}, counts)
}

func TestBlobVerifier_BoundsHashesAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	digest := writeSettledBlob(t, dir, []byte("bounded payload"))
	verifier := settledVerifier(dir)
	fillHashSlots(verifier)

	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	leader := make(chan error, 1)
	go func() { _, err := verifier.verify(leaderCtx, digest); leader <- err }()
	requireInflight(t, verifier, digest, true)

	waiterCtx, cancelWaiter := context.WithCancel(t.Context())
	cancelWaiter()
	_, err := verifier.verify(waiterCtx, digest)
	require.ErrorIs(t, err, context.Canceled)

	cancelLeader()
	select {
	case err := <-leader:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("queued hash ignored cancellation")
	}
	requireInflight(t, verifier, digest, false)

	drainHashSlots(verifier)
	verification, err := verifier.verify(t.Context(), digest)
	require.NoError(t, err)
	assert.Equal(t, fullHash, verification.mode)
}

func TestBlobVerifier_HashingHonorsCancellationBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := &cancelAfterRead{reader: strings.NewReader("first chunk"), cancel: cancel}
	_, err := io.Copy(io.Discard, &provenanceContextReader{ctx: ctx, reader: reader})
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, reader.reads)
}

type cancelAfterRead struct {
	reader io.Reader
	cancel context.CancelFunc
	reads  int
}

func (r *cancelAfterRead) Read(p []byte) (int, error) {
	r.reads++
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}

func fillHashSlots(v *blobVerifier) {
	for range cap(v.hashSlots) {
		v.hashSlots <- struct{}{}
	}
}

func drainHashSlots(v *blobVerifier) {
	for range cap(v.hashSlots) {
		<-v.hashSlots
	}
}

func requireInflight(t *testing.T, v *blobVerifier, digest string, want bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		v.mu.Lock()
		defer v.mu.Unlock()
		_, ok := v.inflight[digest]
		return ok == want
	}, time.Second, time.Millisecond)
}

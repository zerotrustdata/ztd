// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License.

//go:build integration

package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func TestPublicMirrorStore_ProofVerificationAllocations(t *testing.T) {
	env := newMirrorTestEnv(t)
	ctx := t.Context()
	const count = 100
	for i := range count {
		content := []byte(fmt.Sprintf("artifact-%d", i))
		digest := sha256.Sum256(content)
		id := hex.EncodeToString(digest[:])
		require.NoError(t, env.fileSvc.WriteFile(ctx, publicMirrorProofArtifactPath(id), content, constants.PermFilePrivate))
		_, err := env.store.db.ExecWithRetry(ctx, `INSERT INTO proof_artifacts (artifact_id, byte_size, stored_at) VALUES (?, ?, '')`, id, len(content))
		require.NoError(t, err)
	}
	// Warm database and filesystem paths before measuring. A fresh 32 KiB
	// io.Copy buffer per file alone would exceed this allocation budget.
	_, err := env.store.verifyProofArtifacts(ctx)
	require.NoError(t, err)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	sizes, err := env.store.verifyProofArtifacts(ctx)
	runtime.ReadMemStats(&after)
	require.NoError(t, err)
	require.Len(t, sizes, count)
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("proof verification allocated %d bytes per artifact", allocated/count)
	assert.Less(t, allocated, uint64(count*16*1024))
}

type cancelOnProofOpenFileSvc struct {
	fs.RuntimeFileService
	cancel context.CancelFunc
}

func (s *cancelOnProofOpenFileSvc) OpenForRead(ctx context.Context, path string) (*os.File, error) {
	file, err := s.RuntimeFileService.OpenForRead(ctx, path)
	s.cancel()
	return file, err
}

func TestPublicMirrorStore_ProofHashingObservesCancellation(t *testing.T) {
	env := newMirrorTestEnv(t)
	content := []byte("proof bytes")
	digest := sha256.Sum256(content)
	id := hex.EncodeToString(digest[:])
	require.NoError(t, env.fileSvc.WriteFile(t.Context(), publicMirrorProofArtifactPath(id), content, constants.PermFilePrivate))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := PublicMirrorStore{fileSvc: &cancelOnProofOpenFileSvc{RuntimeFileService: env.fileSvc, cancel: cancel}}
	_, err := store.hashProofArtifact(ctx, id, make([]byte, publicMirrorHashBufferBytes))
	require.ErrorIs(t, err, context.Canceled)
}

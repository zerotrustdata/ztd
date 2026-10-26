// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package fileidentity

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestReadAndStatObserveTheSameFileIdentity(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "weights.gguf")
	require.NoError(t, os.WriteFile(path, []byte("weights"), constants.PermFilePrivate))
	future := time.Now().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, os.Chtimes(path, future, future))
	file, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, file.Close()) })
	opened, err := Read(file)
	require.NoError(t, err)
	current, err := Stat(path)
	require.NoError(t, err)
	assert.True(t, opened.Equal(current))
	assert.EqualValues(t, len("weights"), current.Info.Size())
	assert.Equal(t, future, current.LastChangedAt(), "mtime later than change time governs settling")
}

func TestStatDetectsReplacementWithTheSameSizeAndModificationTime(t *testing.T) {
	root := testutil.TempDir(t)
	path := filepath.Join(root, "weights.gguf")
	require.NoError(t, os.WriteFile(path, []byte("first"), constants.PermFilePrivate))
	before, err := Stat(path)
	require.NoError(t, err)
	replacement := filepath.Join(root, "replacement.gguf")
	require.NoError(t, os.WriteFile(replacement, []byte("other"), constants.PermFilePrivate))
	require.NoError(t, os.Chtimes(replacement, before.Info.ModTime(), before.Info.ModTime()))
	require.NoError(t, os.Rename(replacement, path))
	after, err := Stat(path)
	require.NoError(t, err)
	assert.False(t, before.Equal(after), "replacement must invalidate a cached content hash")
	assert.False(t, os.SameFile(before.Info, after.Info))
}

func TestFileIdentityPreservesFilesystemErrors(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "absent.gguf")
	_, err := Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.WriteFile(path, []byte("weights"), constants.PermFilePrivate))
	file, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = Read(file)
	require.ErrorIs(t, err, os.ErrClosed)
}

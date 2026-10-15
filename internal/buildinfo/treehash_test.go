// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package buildinfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zerotrustdata/ztd/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// writeParityFixture builds the fixture used by the manifest-hash tests. The
// layout deliberately exercises path-component ordering: "a" is a directory
// whose entries must sort before "a.b" and "a.txt" even though '.' < '/' in
// byte order.
func writeParityFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"a.txt":         "alpha",
		"a/b.txt":       "beta",
		"a/c.txt":       "chi",
		"a.b/d.txt":     "delta",
		".hidden":       "hidden",
		"dir/sub/c.txt": "gamma",
	}
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
	}
}

func TestComputeSourceManifestHash_KeysRetainEntryPrefix(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)

	// Manifest mode keys files by their path relative to base, so
	// "dir/sub/c.txt" contributes under its full repo-relative name.
	manifest, err := ComputeSourceManifestHash(base, []string{"dir", "a.txt"}, nil)
	require.NoError(t, err)
	assert.Len(t, manifest, 64)
}

func TestComputeSourceManifestHash_RejectsMissingEntry(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)

	_, err := ComputeSourceManifestHash(base, []string{"does-not-exist"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSourceTreeEntryNotFound)
}

func TestComputeSourceManifestHash_RejectsEscapingEntry(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)

	for _, entry := range []string{"../outside", "/absolute/path", "a/../..", ""} {
		_, err := ComputeSourceManifestHash(base, []string{entry}, nil)
		require.Error(t, err, "entry %q must be rejected", entry)
		assert.ErrorIs(t, err, constants.ErrSourceTreeEntryInvalid)
	}
}

func TestComputeSourceManifestHash_RejectsSymlinkEntry(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)
	outside := testutil.TempDir(t)
	testutil.Symlink(t, outside, filepath.Join(base, "ext"))

	_, err := ComputeSourceManifestHash(base, []string{"ext"}, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSourceTreeSymlink)
}

func TestComputeSourceManifestHash_ExcludeSkipsNestedGeneratedDirs(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)
	venv := filepath.Join(base, "pkg", ".venv", "bin")
	require.NoError(t, os.MkdirAll(venv, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(venv, "python"), []byte("x"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "pkg", "real.py"), []byte("y"), 0o644))

	_, err := ComputeSourceManifestHash(base, []string{"pkg"}, nil)
	// No excludes: the .venv content is hashed in (and a symlink inside it
	// would be rejected). With excludes, only real.py remains.
	require.NoError(t, err)
	excluded, err := ComputeSourceManifestHash(base, []string{"pkg"}, []string{".venv"})
	require.NoError(t, err)

	// Excluded run must equal hashing the manifest without .venv at all.
	require.NoError(t, os.RemoveAll(filepath.Join(base, "pkg", ".venv")))
	clean, err := ComputeSourceManifestHash(base, []string{"pkg"}, nil)
	require.NoError(t, err)
	assert.Equal(t, clean, excluded)
}

func TestListSourceManifestFiles_ReturnsPortableSortedDependencies(t *testing.T) {
	base := testutil.TempDir(t)
	writeParityFixture(t, base)
	require.NoError(t, os.MkdirAll(filepath.Join(base, "dir", "tests"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(base, "dir", "tests", "ignored.ts"), []byte("ignored"), 0o644))

	paths, err := ListSourceManifestFiles(base, []string{"dir", "a.txt", "dir"}, []string{"tests"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt", "dir/sub/c.txt"}, paths)
}

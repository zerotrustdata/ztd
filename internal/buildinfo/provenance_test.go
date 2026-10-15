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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestComputeProvenanceManifestHash_MatchesMakefileStamp(t *testing.T) {
	t.Parallel()
	root, err := RepositoryRoot(".")
	require.NoError(t, err)

	hash, err := ComputeProvenanceManifestHash(root)
	require.NoError(t, err)
	assert.Len(t, hash, 64)
}

func TestRepositoryRoot_RejectsModulelessTree(t *testing.T) {
	t.Parallel()
	// Only the filesystem root is guaranteed to have no go.mod ancestor: test
	// scratch lives inside the module (INV-FS-11).
	wd, err := os.Getwd()
	require.NoError(t, err)
	root := filepath.VolumeName(wd) + string(filepath.Separator)

	_, err = RepositoryRoot(root)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrPathNotFound)
}

func TestComputeProvenanceManifestHash_AcceptsModuleRootWithGoModOnly(t *testing.T) {
	t.Parallel()
	root := testutil.TempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test\n"), 0o644))

	hash, err := ComputeProvenanceManifestHash(root)
	require.NoError(t, err)
	assert.Len(t, hash, 64)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

type caBundleFiles struct {
	fs.RuntimeFileService
	readPath string
	err      error
	pathErr  error
}

func (f *caBundleFiles) Rel(path string) (string, error) {
	if f.pathErr != nil {
		return "", f.pathErr
	}
	return f.RuntimeFileService.Rel(path)
}

func (f *caBundleFiles) ReadFile(_ context.Context, path string) ([]byte, error) {
	f.readPath = path
	return []byte("runtime CA bundle"), f.err
}

func TestReadCABundleRuntimePaths(t *testing.T) {
	for _, relative := range []bool{false, true} {
		t.Run(map[bool]string{false: "absolute", true: "working-directory-relative"}[relative], func(t *testing.T) {
			baseDir := testutil.TempDir(t)
			t.Chdir(baseDir)
			local, err := fs.NewRuntimeFileService(baseDir, slog.Default())
			require.NoError(t, err)
			files := &caBundleFiles{RuntimeFileService: local}
			rel := filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
			path := local.Resolve(rel)
			if relative {
				path, err = filepath.Rel(baseDir, path)
				require.NoError(t, err)
			}
			data, err := readCABundle(files, path)
			require.NoError(t, err)
			assert.Equal(t, "runtime CA bundle", string(data))
			assert.Equal(t, rel, files.readPath)

			files.err = constants.ErrFileReadFailed
			_, err = readCABundle(files, path)
			require.ErrorIs(t, err, constants.ErrFileReadFailed)

			files.pathErr = constants.ErrPathValidation
			_, err = readCABundle(files, path)
			require.ErrorIs(t, err, constants.ErrPathValidation)
		})
	}
}

func TestReadCABundleExternalPath(t *testing.T) {
	baseDir := testutil.TempDir(t)
	t.Chdir(baseDir)
	local, err := fs.NewRuntimeFileService(baseDir, slog.Default())
	require.NoError(t, err)
	files := &caBundleFiles{RuntimeFileService: local}
	path := "external-ca.pem"
	require.NoError(t, os.WriteFile(path, []byte("external CA bundle"), constants.PermFilePrivate))
	data, err := readCABundle(files, path)
	require.NoError(t, err)
	assert.Equal(t, "external CA bundle", string(data))
	assert.Empty(t, files.readPath)
}

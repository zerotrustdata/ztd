// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRuntimeRelativePath(t *testing.T) {
	baseDir := testutil.TempDir(t)
	t.Chdir(baseDir)
	files, err := NewRuntimeFileService(baseDir, testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	rel := filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{"absolute runtime", files.Resolve(rel), rel},
		{"relative runtime", filepath.Join(constants.RuntimeDirname, rel), rel},
		{"external absolute", filepath.Join(baseDir, "external.pem"), ""},
		{"external relative", "external.pem", ""},
		{"runtime prefix lookalike", filepath.Join(baseDir, constants.RuntimeDirname+"-backup", "ca.pem"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, inRuntime, err := RuntimeRelativePath(files, tc.path)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.want != "", inRuntime)
		})
	}
}

func TestRuntimeRelativePathRejectsEscapingSymlink(t *testing.T) {
	baseDir := testutil.TempDir(t)
	files, err := NewRuntimeFileService(baseDir, testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	require.NoError(t, files.MkdirAll(t.Context(), "", constants.PermDirPrivate))
	outside := testutil.TempDir(t)
	if err := os.Symlink(outside, files.Resolve(constants.PkiDirname)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, inRuntime, err := RuntimeRelativePath(files, files.Resolve(filepath.Join(constants.PkiDirname, "ca.pem")))
	assert.True(t, inRuntime)
	require.ErrorIs(t, err, constants.ErrPathValidation)
}

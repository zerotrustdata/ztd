// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestCreateOrRestrictFileCreatesAndTightens(t *testing.T) {
	t.Parallel()
	base := testutil.TempDir(t)
	service := newPrivateFileTestService(t, base)
	require.NoError(t, service.CreateRuntimeTree(context.Background()))
	path := service.Resolve(filepath.Join(constants.DataDirname, "private.db"))
	require.NoError(t, os.WriteFile(path, nil, constants.PermFilePublic))

	require.NoError(t, service.CreateOrRestrictFile(context.Background(), filepath.Join(constants.DataDirname, "private.db"), constants.PermFilePrivate))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), info.Mode().Perm())

	freshPath := filepath.Join(constants.DataDirname, "created.db")
	require.NoError(t, service.CreateOrRestrictFile(context.Background(), freshPath, constants.PermFilePrivate))
	info, err = os.Stat(service.Resolve(freshPath))
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), info.Mode().Perm())
}

func TestRestrictFileIfExistsRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	base := testutil.TempDir(t)
	service := newPrivateFileTestService(t, base)
	require.NoError(t, service.CreateRuntimeTree(context.Background()))
	outside := filepath.Join(base, "outside.db")
	require.NoError(t, os.WriteFile(outside, nil, constants.PermFilePublic))
	linkPath := service.Resolve(filepath.Join(constants.DataDirname, "link.db"))
	testutil.Symlink(t, outside, linkPath)

	_, err := service.RestrictFileIfExists(context.Background(), filepath.Join(constants.DataDirname, "link.db"), constants.PermFilePrivate)
	require.ErrorIs(t, err, constants.ErrPathValidation)
	info, err := os.Stat(outside)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePublic, false), info.Mode().Perm())
}

func TestRestrictFileIfExistsReportsMissing(t *testing.T) {
	t.Parallel()
	base := testutil.TempDir(t)
	service := newPrivateFileTestService(t, base)
	require.NoError(t, service.CreateRuntimeTree(context.Background()))
	exists, err := service.RestrictFileIfExists(context.Background(), filepath.Join(constants.DataDirname, "missing.db-wal"), constants.PermFilePrivate)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestPrivateFileOperationsRejectDirectoriesWithoutChangingPermissions(t *testing.T) {
	for _, operation := range []string{"create", "restrict"} {
		t.Run(operation, func(t *testing.T) {
			service := newPrivateFileTestService(t, testutil.TempDir(t))
			require.NoError(t, service.CreateRuntimeTree(testutil.Context(t)))
			path := service.Resolve(constants.DataDirname)
			t.Cleanup(func() { require.NoError(t, os.Chmod(path, constants.PermDirPrivate)) })

			var err error
			if operation == "create" {
				err = service.CreateOrRestrictFile(testutil.Context(t), constants.DataDirname, constants.PermFilePrivate)
			} else {
				_, err = service.RestrictFileIfExists(testutil.Context(t), constants.DataDirname, constants.PermFilePrivate)
			}
			require.ErrorIs(t, err, constants.ErrPathValidation)
			info, err := os.Stat(path)
			require.NoError(t, err)
			assert.Equal(t, testutil.FileMode(constants.PermDirPrivate, true), info.Mode().Perm())
		})
	}
}

func newPrivateFileTestService(t *testing.T, base string) RuntimeFileService {
	t.Helper()
	service, err := NewRuntimeFileService(base, testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	return service
}

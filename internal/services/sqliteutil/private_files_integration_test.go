// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package sqliteutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestOpenDB_RejectsPathOutsideRuntimeRoot(t *testing.T) {
	base := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
	require.NoError(t, err)
	dbPath := filepath.Join(base, "outside.db")

	_, err = OpenDB(fileSvc, DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrPathValidation)
	assert.NoFileExists(t, dbPath)
}

func TestOpenDB_RejectsSymlinkEscape(t *testing.T) {
	base := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(outside, constants.PermDirPrivate))
	link := fileSvc.Resolve("db-link")
	testutil.Symlink(t, outside, link)
	dbPath := filepath.Join(link, "escape.db")

	_, err = OpenDB(fileSvc, DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrPathValidation)
	assert.NoFileExists(t, filepath.Join(outside, "escape.db"))
}

func TestOpenDB_TightensExistingDatabaseAndSidecars(t *testing.T) {
	base := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	dbPath := fileSvc.Resolve(filepath.Join(constants.DataDirname, "sidecars.db"))
	paths := append([]string{dbPath}, sidecarPaths(dbPath)...)
	for _, path := range paths {
		require.NoError(t, os.WriteFile(path, nil, constants.PermFilePublic))
		require.NoError(t, os.Chmod(path, constants.PermFilePublic))
	}

	db, err := OpenDB(fileSvc, DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	for _, path := range paths {
		info, err := os.Stat(path)
		require.NoError(t, err, path)
		assert.Equal(t, testutil.FileMode(constants.PermFilePrivate, false), info.Mode().Perm(), "wrong permissions for %s", path)
	}
}

func TestOpenDB_RejectsSymlinkedSidecarEscape(t *testing.T) {
	base := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(base, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	externalSidecar := filepath.Join(base, "external-wal")
	require.NoError(t, os.WriteFile(externalSidecar, nil, constants.PermFilePublic))
	link := fileSvc.Resolve("sidecar-escape.db-wal")
	testutil.Symlink(t, externalSidecar, link)

	_, err = OpenDB(fileSvc, DefaultDBConfig(fileSvc.Resolve("sidecar-escape.db")), testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrPathValidation)
	info, err := os.Stat(externalSidecar)
	require.NoError(t, err)
	assert.Equal(t, testutil.FileMode(constants.PermFilePublic, false), info.Mode().Perm())
}

func TestOpenDB_RequiresFileServiceForFileDatabase(t *testing.T) {
	_, err := OpenDB(nil, DefaultDBConfig(":memory:unexpected"), testutil.NewTestLogger())
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestOpenDB_InMemoryDatabaseNeedsNoFileService(t *testing.T) {
	db, err := OpenDB(nil, DefaultDBConfig(memoryDatabasePath), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

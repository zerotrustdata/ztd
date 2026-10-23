// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package identityreset

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, ".ztd", name)
	fileSvc, err := fs.NewRuntimeFileService(directory, nil)
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), name, []byte("retain-or-remove"), constants.PermFilePrivate))
	return path
}

func TestResetPreservesDataAndOtherIdentities(t *testing.T) {
	dir := testutil.TempDir(t)
	for _, name := range identityFiles {
		write(t, dir, name)
	}
	retained := []string{"data/ztd.db", "vault/key", "secrets/.master_key", "config.yml", "logs/operator.log", "pki/issued/apps/other.key"}
	for _, name := range retained {
		write(t, dir, name)
	}
	require.NoError(t, Reset(dir))
	fileSvc, err := fs.NewRuntimeFileService(dir, nil)
	require.NoError(t, err)
	for _, name := range identityFiles {
		_, err := fileSvc.Stat(context.Background(), name)
		require.ErrorIs(t, err, constants.ErrNotFound, name)
	}
	for _, name := range retained {
		data, err := fileSvc.ReadFile(context.Background(), name)
		require.NoError(t, err)
		require.Equal(t, "retain-or-remove", string(data))
	}
	require.NoError(t, Reset(dir))
}

func TestResetRefusesGateway(t *testing.T) {
	dir := testutil.TempDir(t)
	write(t, dir, "cli.crt")
	write(t, dir, constants.GatewayRuntimeMarker)
	require.ErrorContains(t, Reset(dir), "gateway runtime")
	fileSvc, err := fs.NewRuntimeFileService(dir, nil)
	require.NoError(t, err)
	_, err = fileSvc.Stat(context.Background(), "cli.crt")
	require.NoError(t, err)
}

func TestResetRefusesSymlinkBeforeRemovingCredentials(t *testing.T) {
	dir := testutil.TempDir(t)
	write(t, dir, "cli.crt")
	fileSvc, err := fs.NewRuntimeFileService(dir, nil)
	require.NoError(t, err)
	require.NoError(t, fileSvc.MkdirAll(context.Background(), "pki", constants.PermDirPrivate))
	outside := testutil.TempDir(t)
	testutil.Symlink(t, outside, fileSvc.Resolve("pki/trust"))
	require.ErrorContains(t, Reset(dir), "symlink")
	_, err = fileSvc.Stat(context.Background(), "cli.crt")
	require.NoError(t, err)
}

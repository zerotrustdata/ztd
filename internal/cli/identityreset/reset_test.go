// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package identityreset

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"

	"github.com/stretchr/testify/require"
)

func write(t *testing.T, directory, name string) string {
	t.Helper()
	path := filepath.Join(directory, ".ztd", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte("retain-or-remove"), 0600))
	return path
}

func TestResetPreservesDataAndOtherIdentities(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	for _, name := range identityFiles {
		write(t, dir, name)
	}
	retained := []string{"data/ztd.db", "vault/key", "secrets/.master_key", "config.yml", "logs/operator.log", "pki/issued/apps/other.key"}
	for _, name := range retained {
		write(t, dir, name)
	}
	require.NoError(t, Reset(dir))
	for _, name := range identityFiles {
		_, err := os.Stat(filepath.Join(dir, ".ztd", name))
		require.True(t, os.IsNotExist(err), name)
	}
	for _, name := range retained {
		data, err := os.ReadFile(filepath.Join(dir, ".ztd", name))
		require.NoError(t, err)
		require.Equal(t, "retain-or-remove", string(data))
	}
	require.NoError(t, Reset(dir))
}

func TestResetRefusesGateway(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	cert := write(t, dir, "cli.crt")
	write(t, dir, constants.GatewayRuntimeMarker)
	require.ErrorContains(t, Reset(dir), "gateway runtime")
	_, err := os.Stat(cert)
	require.NoError(t, err)
}

func TestResetRefusesSymlinkBeforeRemovingCredentials(t *testing.T) {
	t.Parallel()
	dir := testutil.TempDir(t)
	cert := write(t, dir, "cli.crt")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ztd/pki"), 0700))
	outside := testutil.TempDir(t)
	testutil.Symlink(t, outside, filepath.Join(dir, ".ztd/pki/trust"))
	require.ErrorContains(t, Reset(dir), "symlink")
	_, err := os.Stat(cert)
	require.NoError(t, err)
}

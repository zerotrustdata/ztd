// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zerotrustdata/ztd/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/paths"
)

func TestCreateRuntimeTree_CancelledContextReturnsError(t *testing.T) {
	svc := setupTestFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := svc.CreateRuntimeTree(ctx)
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled))
}

func TestCreateRuntimeTree_NilContextSucceeds(t *testing.T) {
	svc := setupTestFS(t)
	var nilCtx context.Context
	require.NoError(t, svc.CreateRuntimeTree(nilCtx))
}

func TestCreateRuntimeTree_DataBearingDirsArePrivate(t *testing.T) {
	svc := setupTestFS(t)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))

	// --data-dir can place the data directory outside the private runtime
	// root, so every data-bearing directory must be private on its own.
	for name, dir := range map[string]string{
		"data":         paths.Infra.DataDir,
		"ledger":       paths.Infra.LedgerDir,
		"ledger files": paths.Infra.LedgerFilesDir,
		"logs":         paths.Infra.LogDir,
		"pids":         paths.Infra.PidDir,
		"bin":          paths.Infra.BinDir,
	} {
		info, err := os.Stat(dir)
		require.NoError(t, err, name)
		assert.Equal(t, testutil.FileMode(constants.PermDirPrivate, info.IsDir()), info.Mode().Perm(), "wrong permissions for %s: %s", name, dir)
	}
}

func TestCreateRuntimeTree_KeyBearingPkiDirsArePrivate(t *testing.T) {
	svc := setupTestFS(t)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))

	for _, dir := range []string{
		paths.Infra.PkiDir,
		paths.Infra.PkiRootDir,
		paths.Infra.PkiAuthoritiesDir,
		paths.Infra.PkiIssuedDir,
		paths.Infra.PkiIssuedHubDir,
		paths.Infra.PkiIssuedGatewayPeerDir,
		paths.Infra.AppCertDir,
		paths.Infra.ClientPkiDir,
	} {
		info, err := os.Stat(dir)
		require.NoError(t, err, dir)
		assert.Equal(t, testutil.FileMode(constants.PermDirPrivate, info.IsDir()), info.Mode().Perm(), "wrong permissions for: %s", dir)
	}
}

func TestCreateRuntimeTree_PublicPkiDirsAreStandard(t *testing.T) {
	svc := setupTestFS(t)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))

	for _, dir := range []string{
		paths.Infra.PkiTrustDir,
		paths.Infra.PkiRevocationDir,
		paths.Infra.PkiBinariesDir,
	} {
		info, err := os.Stat(dir)
		require.NoError(t, err, dir)
		assert.Equal(t, testutil.FileMode(constants.PermDirStandard, info.IsDir()), info.Mode().Perm(), "wrong permissions for: %s", dir)
	}
}

// Nothing writes into protocol/, docs/, or inference/ at the runtime root: the
// inference evidence stores live under data/inference and are created by their
// owning writers on demand.
func TestCreateRuntimeTree_OmitsUnusedRuntimeRootDirs(t *testing.T) {
	svc := setupTestFS(t)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))

	for _, name := range []string{constants.ProtocolDirname, constants.DocsDirname, constants.InferenceDirname} {
		assert.NoDirExists(t, filepath.Join(paths.Infra.RuntimeDir, name))
	}
}

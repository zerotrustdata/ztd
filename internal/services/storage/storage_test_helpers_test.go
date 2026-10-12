// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newTestFileSvc creates a RuntimeFileService backed by baseDir with the full
// .ztd runtime tree created.
func newTestFileSvc(t *testing.T, baseDir string) fs.RuntimeFileService {
	t.Helper()
	svc, err := fs.NewRuntimeFileService(baseDir, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))
	return svc
}

// CreateTestVault creates a new unlocked Vault using the provided runtime file service and private key.
// The vault header is initialized and the vault is unlocked. Cleanup closes it via t.Cleanup.
func CreateTestVault(t testing.TB, fileSvc fs.RuntimeFileService, privateKey []byte) *vault.Vault {
	t.Helper()

	logger := testutil.NewTestLogger()

	header, _, err := vault.NewVaultHeader(privateKey)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))

	v, err := vault.NewVault(&vault.VaultConfig{
		FileSvc: fileSvc,
		Logger:  logger,
	})
	require.NoError(t, err)
	require.NoError(t, v.Unlock(privateKey))

	t.Cleanup(func() { v.Close() })
	return v
}

// openTestTarget opens a governed FILE_EDIT target for path beneath workDir
// through fileSvc and closes it via t.Cleanup. Ledger mutations take the target
// instead of a raw path, so tests obtain it here.
func openTestTarget(t *testing.T, fileSvc fs.RuntimeFileService, path, workDir string) *fs.FileEditTarget {
	t.Helper()
	target, err := fileSvc.OpenFileEditTarget(context.Background(), path, workDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	return target
}

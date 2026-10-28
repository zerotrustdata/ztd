// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package governance

import (
	"context"
	"crypto/ed25519"
	"testing"

	"log/slog"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newTestSuspendedStore builds a suspended transaction store over a fresh ztd.db and an unlocked vault.
func newTestSuspendedStore(t *testing.T) *storage.SuspendedTransactionService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	vHeader, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, vHeader.Save(fileSvc))
	testVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: slog.Default()})
	require.NoError(t, err)
	require.NoError(t, testVault.Unlock(privKey))
	t.Cleanup(func() { require.NoError(t, testVault.Close()) })
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	sts, err := storage.NewSuspendedTransactionService(db, testVault, slog.Default())
	require.NoError(t, err)
	return sts
}

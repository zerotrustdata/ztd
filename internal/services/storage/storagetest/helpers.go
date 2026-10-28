// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package storagetest provides test helpers (vaults, file services, token store)
// and the chaos run's own event store and summary reader. It is not an events store:
// tests and the chaos tool use the production storage.LocalFirstEventsStore.
package storagetest

import (
	"context"
	"crypto/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// CreateTestVault creates a new unlocked Vault in the given directory using the provided private key.
// The vault header is initialized and the vault is unlocked. Cleanup closes it via t.Cleanup.
func CreateTestVault(tb testing.TB, dataDir string, privateKey []byte) *vault.Vault {
	tb.Helper()

	fileSvc, err := fs.NewRuntimeFileService(filepath.Dir(dataDir), testutil.NewTestLogger())
	require.NoError(tb, err)
	require.NoError(tb, fileSvc.CreateRuntimeTree(context.Background()))

	logger := testutil.NewTestLogger()

	header, _, err := vault.NewVaultHeader(privateKey)
	require.NoError(tb, err)
	require.NoError(tb, header.Save(fileSvc))

	v, err := vault.NewVault(&vault.VaultConfig{
		FileSvc: fileSvc,
		Logger:  logger,
	})
	require.NoError(tb, err)
	require.NoError(tb, v.Unlock(privateKey))

	tb.Cleanup(func() { v.Close() })
	return v
}

// NewTestFileSvc creates a RuntimeFileService backed by baseDir with the full
// .ztd runtime tree created. Returns the file service.
func NewTestFileSvc(tb testing.TB, baseDir string) fs.RuntimeFileService {
	tb.Helper()
	svc, err := fs.NewRuntimeFileService(baseDir, testutil.NewTestLogger())
	require.NoError(tb, err)
	require.NoError(tb, svc.CreateRuntimeTree(context.Background()))
	return svc
}

// NewTestEventsStore builds a real storage.LocalFirstEventsStore (real SQLite with foreign keys
// enforced, real unlocked vault) over a fresh isolated runtime root. Each sessionID is created
// as an Operator audit session so events recorded against it satisfy the session foreign key.
// Cleanup closes the store and its database.
func NewTestEventsStore(t *testing.T, sessionIDs ...string) *storage.LocalFirstEventsStore {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc := NewTestFileSvc(t, testutil.TempDir(t))

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc.Resolve(constants.VaultDirname), key)

	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirStandard))
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	eventsStore, err := storage.NewLocalFirstEventsStore(&storage.LocalFirstEventsStoreConfig{
		RetentionDays:   1,
		EncryptionVault: testVault,
	}, db, logger, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, eventsStore.Close()) })

	for _, id := range sessionIDs {
		require.NoError(t, eventsStore.CreateSession(id, constants.SessionTypeOperator, "Operator Session", "test-operator"))
	}
	return eventsStore
}

// NewTestLedger builds a real git file ledger over a fresh isolated runtime root.
// The ledger's vault is unlocked, so snapshots are encrypted and succeed.
// Cleanup closes the vault.
func NewTestLedger(t *testing.T) *storage.GitLedgerService {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc := NewTestFileSvc(t, testutil.TempDir(t))

	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	header, _, err := vault.NewVaultHeader(key)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	require.NoError(t, v.Unlock(key))
	t.Cleanup(func() { _ = v.Close() })

	ledger, err := storage.NewGitLedgerService(&storage.LedgerConfig{EncryptionVault: v}, logger, fileSvc)
	require.NoError(t, err)
	return ledger
}

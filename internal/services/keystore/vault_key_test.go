// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestVaultKey_InitVault_Success(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	// Initially, vault header does not exist
	exists, err := vault.VaultHeaderExists(fileSvc)
	require.NoError(t, err)
	assert.False(t, exists)

	// InitVault succeeds
	err = ks.InitVault()
	require.NoError(t, err)

	// Vault header now exists
	exists, err = vault.VaultHeaderExists(fileSvc)
	require.NoError(t, err)
	assert.True(t, exists)

	// Secret vault_key exists in fileSvc
	vaultKeyRel := filepath.Join(constants.SecretsDirname, constants.SecretsFileVaultKey)
	keyData, err := fileSvc.ReadFile(context.Background(), vaultKeyRel)
	require.NoError(t, err)
	assert.NotEmpty(t, keyData)

	// LoadVaultKey decrypts the key
	keyBytes, err := ks.LoadVaultKey()
	require.NoError(t, err)
	defer vault.SecureZero(keyBytes)
	assert.Len(t, keyBytes, vault.KeySize)

	// The raw key should never appear in the ciphertext on disk
	assert.False(t, bytes.Contains(keyData, keyBytes), "vault key material leaked unencrypted on disk")

	// Vault can be unlocked with this key
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	require.NoError(t, v.Unlock(keyBytes))
	assert.True(t, v.IsUnlocked())
	t.Cleanup(func() { v.Close() })
}

func TestVaultKey_InitVault_AlreadyInitialized(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	// Calling InitVault again fails with ErrVaultAlreadyInitialized
	err = ks.InitVault()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrVaultAlreadyInitialized)
}

func TestVaultKey_LoadVaultKey_NotFound(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	_, err = ks.LoadVaultKey()
	require.Error(t, err)
}

func TestVaultKey_RekeyVault_Success(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	oldKey, err := ks.LoadVaultKey()
	require.NoError(t, err)
	defer vault.SecureZero(oldKey)

	// Rekey vault
	err = ks.RekeyVault()
	require.NoError(t, err)

	// Staged file should no longer exist (it was renamed to current)
	stagedRel := filepath.Join(constants.SecretsDirname, constants.SecretsFileVaultKeyStaged)
	stagedExists, err := fileSvc.FileExists(context.Background(), stagedRel)
	require.NoError(t, err)
	assert.False(t, stagedExists, "staged key file should be renamed away after successful rekey")

	newKey, err := ks.LoadVaultKey()
	require.NoError(t, err)
	defer vault.SecureZero(newKey)

	assert.NotEqual(t, oldKey, newKey, "new key must differ from old key")

	// Old key can no longer unlock the vault
	vOld, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	defer vOld.Close()
	err = vOld.Unlock(oldKey)
	require.Error(t, err, "old key should fail to unlock rekeyed vault")

	// New key unlocks successfully
	vNew, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: logger})
	require.NoError(t, err)
	defer vNew.Close()
	require.NoError(t, vNew.Unlock(newKey))
	assert.True(t, vNew.IsUnlocked())
}

// writeFailingFileService fails writes to failPath once it is set.
type writeFailingFileService struct {
	fs.RuntimeFileService
	failPath string
}

func (f *writeFailingFileService) WriteFile(ctx context.Context, relPath string, data []byte, mode os.FileMode) error {
	if f.failPath != "" && relPath == f.failPath {
		return errors.New("simulated write failure")
	}
	return f.RuntimeFileService.WriteFile(ctx, relPath, data, mode)
}

func TestVaultKey_RekeyVault_StagedFileFailurePath(t *testing.T) {
	t.Parallel()
	baseFileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	wrappedFileSvc := &writeFailingFileService{RuntimeFileService: baseFileSvc}

	ks, err := NewWithKeyringAndFS(logger, keyring, wrappedFileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	// RekeyVault should fail sealing the new key under the current name,
	// after the header already uses it.
	wrappedFileSvc.failPath = filepath.Join(constants.SecretsDirname, constants.SecretsFileVaultKey)
	err = ks.RekeyVault()
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrVaultKeyWriteFailed)

	// Error message should name the staged file
	assert.Contains(t, err.Error(), constants.SecretsFileVaultKeyStaged)

	// The staged key must remain readable so the vault can be recovered.
	staged, err := ks.LoadKeyMaterial(constants.SecretsFileVaultKeyStaged, vault.KeySize)
	require.NoError(t, err)
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: baseFileSvc, Logger: logger})
	require.NoError(t, err)
	defer v.Close()
	require.NoError(t, v.Unlock(staged), "staged key should open the rekeyed vault")
}

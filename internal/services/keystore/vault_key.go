// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

// The vault key wraps the vault DEK. It exists only as key material encrypted
// under the master key in the secrets directory; it is never written in the
// clear.

// InitVault generates a vault key, stores it under the master key, and writes
// a new vault header wrapped by it. The key is stored before the header so a
// failed store never leaves a header that no stored key can open.
func (k *Keystore) InitVault() error {
	exists, err := vault.VaultHeaderExists(k.fileSvc)
	if err != nil {
		return fmt.Errorf("keystore: init vault: %w", err)
	}
	if exists {
		return fmt.Errorf("keystore: init vault: %w", constants.ErrVaultAlreadyInitialized)
	}
	if err := k.fileSvc.MkdirAll(context.Background(), constants.VaultDirname, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}

	key := make([]byte, vault.KeySize)
	defer vault.SecureZero(key)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrVaultKeyGenerateFailed, err)
	}

	header, dek, err := vault.NewVaultHeader(key)
	vault.SecureZero(dek)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrVaultHeaderCreateFailed, err)
	}

	if err := k.StoreKeyMaterial(constants.SecretsFileVaultKey, key); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrVaultKeyWriteFailed, err)
	}
	if err := header.Save(k.fileSvc); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrVaultHeaderSaveFailed, err)
	}
	return nil
}

// LoadVaultKey decrypts the vault key. The caller must SecureZero it.
func (k *Keystore) LoadVaultKey() ([]byte, error) {
	return k.LoadKeyMaterial(constants.SecretsFileVaultKey, vault.KeySize)
}

// OpenVault unlocks the runtime vault with the stored vault key and returns it.
// It fails closed: an unreadable vault key, a missing header, or a failed unlock
// returns a wrapped error and no vault. The caller owns the returned vault and
// must Lock it when finished.
func (k *Keystore) OpenVault() (*vault.Vault, error) {
	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: k.fileSvc, Logger: k.logger})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
	}
	key, err := k.LoadVaultKey()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultKeyReadFailed, err)
	}
	defer vault.SecureZero(key)
	if err := v.Unlock(key); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrVaultUnlockFailed, err)
	}
	return v, nil
}

// RekeyVault rewraps the vault DEK under a freshly generated vault key. The
// new key is staged under the master key before the header changes, so a
// crash after the header is saved leaves the new key recoverable from the
// staged secret rather than lost. The staged secret is removed only after the
// new key is sealed as the current vault key.
func (k *Keystore) RekeyVault() error {
	oldKey, err := k.LoadVaultKey()
	if err != nil {
		return fmt.Errorf("keystore: rekey vault: load current key: %w", err)
	}
	defer vault.SecureZero(oldKey)

	newKey := make([]byte, vault.KeySize)
	defer vault.SecureZero(newKey)
	if _, err := rand.Read(newKey); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrVaultKeyGenerateFailed, err)
	}

	if err := k.StoreKeyMaterial(constants.SecretsFileVaultKeyStaged, newKey); err != nil {
		return fmt.Errorf("keystore: rekey vault: stage new key: %w", err)
	}

	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: k.fileSvc, Logger: k.logger})
	if err != nil {
		_ = k.DeleteSecret(constants.SecretsFileVaultKeyStaged)
		return fmt.Errorf("%w: %w", constants.ErrVaultCreateFailed, err)
	}
	if err := v.Rekey(oldKey, newKey); err != nil {
		_ = k.DeleteSecret(constants.SecretsFileVaultKeyStaged)
		return fmt.Errorf("%w: %w", constants.ErrVaultRekeyFailed, err)
	}

	// Secrets are bound to their names, so the staged ciphertext cannot be
	// renamed into place; the new key is sealed again under the current name.
	staged := filepath.Join(constants.SecretsDirname, constants.SecretsFileVaultKeyStaged)
	if err := k.StoreKeyMaterial(constants.SecretsFileVaultKey, newKey); err != nil {
		return fmt.Errorf("%w: header now uses the key staged at %s: %w", constants.ErrVaultKeyWriteFailed, k.fileSvc.Resolve(staged), err)
	}
	if err := k.DeleteSecret(constants.SecretsFileVaultKeyStaged); err != nil {
		return fmt.Errorf("keystore: rekey vault: remove staged key: %w", err)
	}
	return nil
}

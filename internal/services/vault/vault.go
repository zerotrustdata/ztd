// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package vault

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// Vault manages the encrypted LFAA data store.
// It handles initialization, unlocking, re-keying, and provides
// the DEK for database encryption operations.
type Vault struct {
	fileSvc fs.RuntimeFileService
	dbPath  string
	logger  *slog.Logger

	header   *VaultHeader
	dek      []byte
	unlocked bool

	mu sync.RWMutex
}

// VaultConfig holds configuration for vault initialization
type VaultConfig struct {
	// FileSvc owns the runtime tree containing vault headers and databases.
	FileSvc fs.RuntimeFileService

	// Logger for vault operations
	Logger *slog.Logger
}

// NewVault creates a new Vault instance.
// The vault is not initialized or unlocked until Unlock() is called.
func NewVault(config *VaultConfig) (*Vault, error) {
	if config == nil {
		return nil, constants.ErrVaultConfigRequired
	}
	if config.FileSvc == nil {
		return nil, fmt.Errorf("vault: %w", constants.ErrVaultDataDirRequired)
	}

	logger := config.Logger
	if logger == nil {
		logger = slog.Default()
	}

	v := &Vault{
		fileSvc: config.FileSvc,
		dbPath:  filepath.Join(constants.DataDirname, constants.DbFilename),
		logger:  logger,
	}

	return v, nil
}

// Unlock opens an existing vault using the provided private key.
// The DEK is unwrapped and held in memory for encryption operations.
func (v *Vault) Unlock(privateKey []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.unlocked {
		return constants.ErrVaultAlreadyOpen
	}

	header, err := LoadVaultHeader(v.fileSvc)
	if err != nil {
		if errors.Is(err, constants.ErrVaultHeaderNotFound) {
			return constants.ErrVaultNotInitialized
		}
		return fmt.Errorf("failed to load vault header: %w", err)
	}

	dek, err := header.UnwrapDEK(privateKey)
	if err != nil {
		if errors.Is(err, constants.ErrVaultKeyFingerprintMatch) {
			return constants.ErrVaultInvalidPrivateKey
		}
		return fmt.Errorf("failed to unwrap DEK: %w", err)
	}

	v.header = header
	v.dek = dek
	v.unlocked = true

	v.logger.Info("Vault unlocked",
		"runtime_dir", v.fileSvc.Resolve(constants.VaultDirname),
		"key_fingerprint", header.KeyFingerprint[:8]+"...")

	return nil
}

// Rekey re-encrypts the DEK with a new private key.
// Both old and new private keys are required.
// The vault data itself is not re-encrypted (only the DEK wrapper changes).
func (v *Vault) Rekey(oldPrivateKey, newPrivateKey []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	header := v.header
	if header == nil {
		var err error
		header, err = LoadVaultHeader(v.fileSvc)
		if err != nil {
			if errors.Is(err, constants.ErrVaultHeaderNotFound) {
				return constants.ErrVaultNotInitialized
			}
			return fmt.Errorf("failed to load vault header: %w", err)
		}
	}

	if err := header.Rekey(oldPrivateKey, newPrivateKey); err != nil {
		return fmt.Errorf("failed to rekey vault: %w", err)
	}

	if err := header.Save(v.fileSvc); err != nil {
		return fmt.Errorf("failed to save rekeyed vault header: %w", err)
	}

	if v.unlocked && v.dek != nil {
		v.header = header
	}

	v.logger.Info("Vault rekeyed",
		"runtime_dir", v.fileSvc.Resolve(constants.VaultDirname),
		"new_key_fingerprint", header.KeyFingerprint[:8]+"...")

	return nil
}

// Lock securely clears the DEK from memory and locks the vault.
func (v *Vault) Lock() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.dek != nil {
		SecureZero(v.dek)
		v.dek = nil
	}
	v.unlocked = false

	v.logger.Info("Vault locked")
}

// Close locks the vault and releases resources.
func (v *Vault) Close() error {
	v.Lock()
	return nil
}

// IsUnlocked returns whether the vault is currently unlocked.
func (v *Vault) IsUnlocked() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.unlocked
}

// IsInitialized returns whether the vault has been initialized (header exists).
func (v *Vault) IsInitialized() bool {
	exists, err := VaultHeaderExists(v.fileSvc)
	return err == nil && exists
}

// GetDEK returns the Data Encryption Key for database encryption.
// Returns an error if the vault is locked.
// SECURITY: The caller must not store the DEK or allow it to escape to disk.
func (v *Vault) GetDEK() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if !v.unlocked || v.dek == nil {
		return nil, constants.ErrVaultLocked
	}

	dekCopy := make([]byte, len(v.dek))
	copy(dekCopy, v.dek)
	return dekCopy, nil
}

// EncryptWithAAD encrypts plaintext with AES-256-GCM and binds aad as
// authenticated associated data: the ciphertext only decrypts with the same aad,
// so a blob moved to another row or column fails authentication. A random nonce
// is prepended. Returns an error if the vault is locked.
func (v *Vault) EncryptWithAAD(plaintext, aad []byte) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if !v.unlocked || v.dek == nil {
		return nil, constants.ErrVaultLocked
	}

	nonce, err := GenerateNonce()
	if err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext, err := EncryptAESGCM(v.dek, nonce, plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("encryption failed: %w", err)
	}

	result := make([]byte, NonceSize+len(ciphertext))
	copy(result[:NonceSize], nonce)
	copy(result[NonceSize:], ciphertext)

	return result, nil
}

// DecryptWithAAD reverses EncryptWithAAD. It fails authentication when the
// ciphertext was altered or when aad differs from the value used to encrypt.
// Returns an error if the vault is locked.
func (v *Vault) DecryptWithAAD(ciphertext, aad []byte) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if !v.unlocked || v.dek == nil {
		return nil, constants.ErrVaultLocked
	}

	if len(ciphertext) < NonceSize {
		return nil, constants.ErrVaultCiphertextTooShort
	}

	plaintext, err := DecryptAESGCM(v.dek, ciphertext[:NonceSize], ciphertext[NonceSize:], aad)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	return plaintext, nil
}

// HMACSHA256 returns HMAC-SHA256(data) under a subkey derived from the DEK for
// label. It is the one keyed digest/pseudonym primitive: a stable, secret-keyed
// value (a content digest, a file path name) that reveals nothing without the
// vault, with labels yielding unrelated outputs. The key never leaves the vault.
// Returns ErrVaultLocked if the vault is locked.
func (v *Vault) HMACSHA256(label string, data []byte) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if !v.unlocked || v.dek == nil {
		return nil, constants.ErrVaultLocked
	}

	subkey, err := DeriveSubkey(v.dek, label)
	if err != nil {
		return nil, err
	}
	defer SecureZero(subkey)

	mac := hmac.New(sha256.New, subkey)
	mac.Write(data)
	return mac.Sum(nil), nil
}

// GetDataDir returns the absolute runtime vault directory.
func (v *Vault) GetDataDir() string {
	return v.fileSvc.Resolve(constants.VaultDirname)
}

// VerifyIntegrity checks the vault's integrity by attempting to unwrap the DEK.
// Returns nil if the vault is healthy, error otherwise.
func (v *Vault) VerifyIntegrity(privateKey []byte) error {
	header, err := LoadVaultHeader(v.fileSvc)
	if err != nil {
		return fmt.Errorf("header load failed: %w", err)
	}

	dek, err := header.UnwrapDEK(privateKey)
	if err != nil {
		return fmt.Errorf("DEK unwrap failed: %w", err)
	}
	SecureZero(dek)

	return nil
}

// Reset destroys the vault completely. All encrypted data becomes unrecoverable.
// This is a destructive operation that requires explicit confirmation.
func (v *Vault) Reset(confirmDestroy bool) error {
	if !confirmDestroy {
		return constants.ErrVaultResetConfirmation
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.dek != nil {
		SecureZero(v.dek)
		v.dek = nil
	}
	v.unlocked = false
	v.header = nil

	if err := DeleteVaultHeader(v.fileSvc); err != nil {
		return fmt.Errorf("failed to delete vault header: %w", err)
	}

	for _, relPath := range []string{
		v.dbPath,
		v.dbPath + constants.SQLiteWALSuffix,
		v.dbPath + constants.SQLiteSHMSuffix,
	} {
		if err := v.fileSvc.Remove(context.Background(), relPath); err != nil {
			return fmt.Errorf("failed to delete vault database artifact %s: %w", relPath, err)
		}
	}

	v.logger.Info("Vault reset complete - all data destroyed")

	return nil
}

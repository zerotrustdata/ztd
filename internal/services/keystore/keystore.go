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
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
)

const (
	keyStoreName  = "ztd-platform"
	masterKeyName = "master-encryption-key"
	keyVersion    = 2
)

// Additional data binds every ciphertext to where it belongs: a secret file to
// its name and a sealed value to its caller-supplied binding. A ciphertext
// moved to another name or context fails authentication, and the two domains
// cannot be substituted for each other.
const (
	secretAADPrefix = "ztd-keystore-v2\x00secret\x00"
	valueAADPrefix  = "ztd-keystore-v2\x00value\x00"
)

// EncryptedSecret represents an encrypted secret value on disk. Only version 2
// is accepted; earlier unbound ciphertexts fail closed.
type EncryptedSecret struct {
	Version    int    `json:"version"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Options selects where the master key lives. A nil Keyring uses the
// platform's OS-protected store: the Linux Secret Service or ssh-agent seal,
// the macOS Keychain, or Windows DPAPI. No unprotected fallback key is
// generated, and there is no master key file.
type Options struct {
	// Keyring is an explicit master key store. Production leaves it nil;
	// tests pass an isolated in-memory keyring (keystoretest.NewMemoryKeyring).
	Keyring Keyring
	// Ceremony is called with a newly generated master key before it is stored.
	// The function must not retain key. A nil ceremony stores the key without
	// a recovery prompt; Gateway and Operator start set one that fails closed
	// until the operator retypes the key or explicitly accepts an unrecoverable
	// runtime. A non-nil ceremony that returns an error leaves the key store
	// unchanged.
	Ceremony func(key []byte) error
}

// Keystore provides OS-protected key storage and encryption for platform secrets.
// It reads the master key from its key store once and holds it in process
// memory until Close or Purge zero it, like the vault DEK (INV-ENC-VAULT-02):
// every OS key store read can require user approval, such as a macOS Keychain
// prompt, so a process must not read it again for each secret.
type Keystore struct {
	logger  *slog.Logger
	keyring Keyring
	fileSvc fs.RuntimeFileService

	mu        sync.Mutex
	masterKey []byte
	ceremony  func(key []byte) error
}

// NewWithKeyringAndFS creates a new Keystore instance with a custom keyring and
// a RuntimeFileService for atomic file operations.
func NewWithKeyringAndFS(logger *slog.Logger, keyring Keyring, fileSvc fs.RuntimeFileService) (*Keystore, error) {
	return &Keystore{
		logger:  logger,
		keyring: keyring,
		fileSvc: fileSvc,
	}, nil
}

const masterKeyProvisioning = "unlock the OS key store, or on Linux load an Ed25519 or RSA key into ssh-agent"

// NewWithFS creates a Keystore backed by the master key store that opts
// selects. It fails closed when no OS-protected store is available.
func NewWithFS(fileSvc fs.RuntimeFileService, logger *slog.Logger, opts Options) (*Keystore, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("keystore: %w: runtime file service", constants.ErrMissingRequiredField)
	}
	keyring := opts.Keyring
	if keyring == nil {
		var err error
		keyring, err = platformKeyring(fileSvc)
		if err != nil {
			return nil, fmt.Errorf("keystore: %w: %w (%s)", constants.ErrKeyStoreOSKeyringRequired, err, masterKeyProvisioning)
		}
	}
	return &Keystore{
		logger:   logger,
		keyring:  keyring,
		fileSvc:  fileSvc,
		ceremony: opts.Ceremony,
	}, nil
}

// Open creates a Keystore, loads or generates its master key, and enforces
// permissions on the secrets directory.
func Open(fileSvc fs.RuntimeFileService, logger *slog.Logger, opts Options) (*Keystore, error) {
	ks, err := NewWithFS(fileSvc, logger, opts)
	if err != nil {
		return nil, err
	}
	if err := ks.Initialize(); err != nil {
		return nil, fmt.Errorf("keystore: master key: %w", err)
	}
	if err := ks.EnforcePermissions(); err != nil {
		return nil, fmt.Errorf("keystore: permissions: %w", err)
	}
	return ks, nil
}

// Initialize creates or retrieves the master encryption key from the key store.
func (k *Keystore) Initialize() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.masterKey != nil {
		return nil
	}
	_, err := k.loadMasterKeyLocked()
	if err == nil {
		k.logger.Info("[Keystore] Master key retrieved", "keyring", k.keyring.Name())
		return nil
	}
	if !errors.Is(err, constants.ErrKeyStoreKeyNotFound) {
		return fmt.Errorf("%w: %w (%s)", constants.ErrKeyStoreRetrieveFailed, err, masterKeyProvisioning)
	}
	if err := k.rejectOrphanedSecrets(); err != nil {
		return err
	}
	k.logger.Info("[Keystore] Master key not found, generating new key", "keyring", k.keyring.Name())
	return k.generateAndStoreMasterKeyLocked()
}

// Close zeroes the master key held in memory. A later operation reads it from
// the key store again.
func (k *Keystore) Close() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.dropMasterKeyLocked()
}

func (k *Keystore) dropMasterKeyLocked() {
	if k.masterKey != nil {
		vault.SecureZero(k.masterKey)
		k.masterKey = nil
	}
}

// loadMasterKeyLocked returns the held master key, reading it from the key
// store on first use. Callers hold k.mu and must not retain or zero the key.
func (k *Keystore) loadMasterKeyLocked() ([]byte, error) {
	if k.masterKey != nil {
		return k.masterKey, nil
	}
	key, err := k.retrieveMasterKey()
	if err != nil {
		return nil, err
	}
	if len(key) != vault.KeySize {
		vault.SecureZero(key)
		return nil, fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreInvalidKeyLength, len(key), vault.KeySize)
	}
	k.masterKey = key
	return key, nil
}

// retrieveMasterKey reads the master key and logs once when the key store
// holds the read for user approval, such as a macOS Keychain access prompt for
// a rebuilt ztd binary, so a blocked start explains what it is waiting for.
func (k *Keystore) retrieveMasterKey() ([]byte, error) {
	notice := time.AfterFunc(constants.KeyStoreApprovalNoticeDelay, func() {
		k.logger.Warn(constants.KeyStoreAwaitingApprovalLog, "keyring", k.keyring.Name())
	})
	defer notice.Stop()
	return k.keyring.RetrieveMasterKey()
}

// generateAndStoreMasterKeyLocked generates a new AES-256 key, stores it, and
// reads it back so a store that silently dropped or altered the key fails
// startup instead of leaving secrets encrypted under a key nobody holds. The
// verified key becomes the held master key. Callers hold k.mu.
func (k *Keystore) generateAndStoreMasterKeyLocked() error {
	key := make([]byte, vault.KeySize)
	defer vault.SecureZero(key)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreGenerateFailed, err)
	}
	if k.ceremony != nil {
		if err := k.ceremony(key); err != nil {
			return err
		}
	}

	if err := k.keyring.StoreMasterKey(key); err != nil {
		return fmt.Errorf("%w: %w (%s)", constants.ErrKeyStoreStoreFailed, err, masterKeyProvisioning)
	}

	stored, err := k.keyring.RetrieveMasterKey()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreVerifyFailed, err)
	}
	if subtle.ConstantTimeCompare(stored, key) != 1 {
		vault.SecureZero(stored)
		return constants.ErrKeyStoreVerifyFailed
	}
	k.masterKey = stored

	k.logger.Info("[Keystore] Master key generated and stored", "keyring", k.keyring.Name())
	return nil
}

// MasterKeyPresent reports whether the key store already holds a master key.
// Platform stores answer without returning the key, so a check does not prompt
// for the secret the way a read does.
func (k *Keystore) MasterKeyPresent() (bool, error) {
	type presence interface {
		MasterKeyPresent() (bool, error)
	}
	if p, ok := k.keyring.(presence); ok {
		return p.MasterKeyPresent()
	}
	key, err := k.keyring.RetrieveMasterKey()
	if len(key) > 0 {
		vault.SecureZero(key)
	}
	if errors.Is(err, constants.ErrKeyStoreKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// InstallRecoveryKey stores key as the master key. stored is true when this
// call wrote it. A store that already holds the same key returns stored false.
// A different key is left in place. When sealed secrets exist, key must
// decrypt every one of them or nothing is stored. The caller owns key.
func (k *Keystore) InstallRecoveryKey(key []byte) (stored bool, err error) {
	if len(key) != vault.KeySize {
		return false, fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreInvalidKeyLength, len(key), vault.KeySize)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	existing, err := k.retrieveMasterKey()
	if err == nil {
		match := subtle.ConstantTimeCompare(existing, key) == 1
		vault.SecureZero(existing)
		if match {
			return false, nil
		}
		return false, constants.ErrKeyStoreRecoveryConflict
	}
	// An ssh-agent seal whose sealing key is no longer loaded still holds a
	// master key nobody can read. That is the case a saved recovery key exists
	// for: treat it as an empty store, and let verifySealedSecrets prove the
	// key before the seal is rewritten under the key the agent holds now.
	if !errors.Is(err, constants.ErrKeyStoreKeyNotFound) && !errors.Is(err, constants.ErrKeyStoreSSHAgentKeyMissing) {
		return false, fmt.Errorf("%w: %w", constants.ErrKeyStoreRetrieveFailed, err)
	}
	if err := k.verifySealedSecrets(key); err != nil {
		return false, err
	}
	if err := k.keyring.StoreMasterKey(key); err != nil {
		return false, fmt.Errorf("%w: %w (%s)", constants.ErrKeyStoreStoreFailed, err, masterKeyProvisioning)
	}
	readBack, err := k.keyring.RetrieveMasterKey()
	if err != nil {
		return false, fmt.Errorf("%w: %w", constants.ErrKeyStoreVerifyFailed, err)
	}
	if subtle.ConstantTimeCompare(readBack, key) != 1 {
		vault.SecureZero(readBack)
		return false, constants.ErrKeyStoreVerifyFailed
	}
	k.dropMasterKeyLocked()
	k.masterKey = readBack
	k.logger.Info("[Keystore] Recovery key installed", "keyring", k.keyring.Name())
	return true, nil
}

// verifySealedSecrets decrypts every sealed secret with key. A runtime with no
// sealed secrets accepts the key. A single authentication failure rejects it
// without naming the secret.
func (k *Keystore) verifySealedSecrets(key []byte) error {
	entries, err := k.fileSvc.ReadDir(context.Background(), constants.SecretsDirname)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreReadDir, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || validateSecretName(entry.Name()) != nil {
			continue
		}
		data, err := k.fileSvc.ReadFile(context.Background(), filepath.Join(constants.SecretsDirname, entry.Name()))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", constants.ErrKeyStoreReadFailed, entry.Name(), err)
		}
		var enc EncryptedSecret
		if json.Unmarshal(data, &enc) != nil || enc.Version == 0 || len(enc.Nonce) != vault.NonceSize || len(enc.Ciphertext) == 0 {
			continue
		}
		plain, err := vault.DecryptAESGCM(key, enc.Nonce, enc.Ciphertext, []byte(secretAADPrefix+entry.Name()))
		if err != nil {
			return constants.ErrKeyStoreRecoveryMismatch
		}
		vault.SecureZero(plain)
	}
	return nil
}

// rejectOrphanedSecrets fails when the secrets directory already holds secrets
// sealed by EncryptSecret. A store with no master key next to sealed secrets
// selected a different store than the one that sealed them, or lost its key;
// generating a fresh key would orphan them and, for sticky stores such as the
// ssh-agent seal, make the wrong store permanent.
func (k *Keystore) rejectOrphanedSecrets() error {
	entries, err := k.fileSvc.ReadDir(context.Background(), constants.SecretsDirname)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreReadDir, err)
	}
	var sealed []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() || validateSecretName(entry.Name()) != nil {
			continue
		}
		data, err := k.fileSvc.ReadFile(context.Background(), filepath.Join(constants.SecretsDirname, entry.Name()))
		if err != nil {
			return fmt.Errorf("%w: %s: %w", constants.ErrKeyStoreReadFailed, entry.Name(), err)
		}
		var enc EncryptedSecret
		if json.Unmarshal(data, &enc) == nil && enc.Version > 0 && len(enc.Nonce) == vault.NonceSize && len(enc.Ciphertext) > 0 {
			sealed = append(sealed, entry.Name())
		}
	}
	if len(sealed) > 0 {
		return fmt.Errorf("%w: %s has no master key but %s holds %d sealed secrets (%s); restore the key store that sealed them, or remove %s to start over",
			constants.ErrKeyStoreOrphanedSecrets, k.keyring.Name(), constants.SecretsDirname, len(sealed), strings.Join(sealed, ", "), k.fileSvc.Resolve(constants.SecretsDirname))
	}
	return nil
}

// validateSecretName keeps every secret a direct, non-hidden child of the
// secrets directory. Hidden names are reserved for sealed master keys and
// in-flight atomic writes, which a secret name must never overwrite or delete.
func validateSecretName(name string) error {
	if name == "" || len(name) > 255 || name[0] == '.' || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("%w: %q", constants.ErrKeyStoreInvalidSecretName, name)
	}
	return nil
}

// secretPath returns the runtime-relative path of a validated secret name.
func secretPath(name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
	return filepath.Join(constants.SecretsDirname, name), nil
}

// encrypt performs AES-256-GCM encryption on plaintext bound to aad and
// returns the EncryptedSecret structure.
func (k *Keystore) encrypt(plaintext, aad []byte) (*EncryptedSecret, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, err := k.loadMasterKeyLocked()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreRetrieveFailed, err)
	}

	nonce, err := vault.GenerateNonce()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreNonceGenerate, err)
	}

	ciphertext, err := vault.EncryptAESGCM(key, nonce, plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreCipherCreate, err)
	}

	return &EncryptedSecret{
		Version:    keyVersion,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}, nil
}

// decrypt performs AES-256-GCM decryption on an EncryptedSecret bound to aad
// and returns plaintext. The caller must SecureZero it.
func (k *Keystore) decrypt(enc *EncryptedSecret, aad []byte) ([]byte, error) {
	if enc.Version != keyVersion {
		return nil, fmt.Errorf("%w: got %d, expected %d", constants.ErrKeyStoreUnsupportedVersion, enc.Version, keyVersion)
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	key, err := k.loadMasterKeyLocked()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreRetrieveFailed, err)
	}

	plaintext, err := vault.DecryptAESGCM(key, enc.Nonce, enc.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrInvalidCiphertext, err)
	}

	return plaintext, nil
}

// writeSecret encrypts plaintext and writes it to the secrets directory as name.
func (k *Keystore) writeSecret(name string, plaintext []byte) error {
	relPath, err := secretPath(name)
	if err != nil {
		return err
	}
	enc, err := k.encrypt(plaintext, []byte(secretAADPrefix+name))
	if err != nil {
		return err
	}

	data, err := json.Marshal(enc)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreMarshalFailed, err)
	}

	if err := k.fileSvc.WriteFile(context.Background(), relPath, data, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreWriteFailed, err)
	}

	k.logger.Debug("[Keystore] Secret encrypted and written", "name", name)
	return nil
}

// readSecret reads and decrypts the secret name. The caller must SecureZero it.
func (k *Keystore) readSecret(name string) ([]byte, error) {
	relPath, err := secretPath(name)
	if err != nil {
		return nil, err
	}
	data, err := k.fileSvc.ReadFile(context.Background(), relPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreReadFailed, err)
	}

	var enc EncryptedSecret
	if err := json.Unmarshal(data, &enc); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrKeyStoreUnmarshalFailed, err)
	}

	plaintext, err := k.decrypt(&enc, []byte(secretAADPrefix+name))
	if err != nil {
		return nil, err
	}

	k.logger.Debug("[Keystore] Secret decrypted", "name", name)
	return plaintext, nil
}

// EncryptSecret encrypts a plaintext secret value and writes it to disk.
func (k *Keystore) EncryptSecret(name, plaintext string) error {
	buf := []byte(plaintext)
	defer vault.SecureZero(buf)
	return k.writeSecret(name, buf)
}

// DecryptSecret reads and decrypts a secret value from disk.
func (k *Keystore) DecryptSecret(name string) (string, error) {
	plaintext, err := k.readSecret(name)
	if err != nil {
		return "", err
	}
	defer vault.SecureZero(plaintext)
	return string(plaintext), nil
}

// Encrypt seals a plaintext value bound to binding and returns a
// base64-encoded ciphertext string. The binding names where the value is
// stored, such as its runtime path; Decrypt must present the same binding. This is for
// values stored outside the secrets directory, not file-based secrets.
func (k *Keystore) Encrypt(binding, plaintext string) (string, error) {
	if binding == "" {
		return "", fmt.Errorf("keystore: encrypt: %w: binding", constants.ErrMissingRequiredField)
	}
	buf := []byte(plaintext)
	defer vault.SecureZero(buf)
	enc, err := k.encrypt(buf, []byte(valueAADPrefix+binding))
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(enc)
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrKeyStoreMarshalFailed, err)
	}

	return base64.StdEncoding.EncodeToString(data), nil
}

// Decrypt opens a base64-encoded ciphertext string sealed by Encrypt under the
// same binding and returns the plaintext.
func (k *Keystore) Decrypt(binding, encodedCiphertext string) (string, error) {
	if binding == "" {
		return "", fmt.Errorf("keystore: decrypt: %w: binding", constants.ErrMissingRequiredField)
	}
	data, err := base64.StdEncoding.DecodeString(encodedCiphertext)
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrKeyStoreDecodeBase64, err)
	}

	var enc EncryptedSecret
	if err := json.Unmarshal(data, &enc); err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrKeyStoreUnmarshalFailed, err)
	}

	plaintext, err := k.decrypt(&enc, []byte(valueAADPrefix+binding))
	if err != nil {
		return "", err
	}
	defer vault.SecureZero(plaintext)
	return string(plaintext), nil
}

// DeleteSecret removes a secret file from disk.
func (k *Keystore) DeleteSecret(name string) error {
	relPath, err := secretPath(name)
	if err != nil {
		return err
	}
	if err := k.fileSvc.Remove(context.Background(), relPath); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreDeleteSecret, err)
	}
	k.logger.Debug("[Keystore] Secret deleted", "name", name)
	return nil
}

// Purge removes all secrets from disk and deletes the master key from its key store.
func (k *Keystore) Purge() error {
	k.mu.Lock()
	k.dropMasterKeyLocked()
	k.mu.Unlock()
	if err := k.keyring.DeleteMasterKey(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreDeleteFailed, err)
	}

	entries, err := k.fileSvc.ReadDir(context.Background(), constants.SecretsDirname)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreReadDir, err)
	}

	var purgeErrors []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		relPath := filepath.Join(constants.SecretsDirname, entry.Name())
		if err := k.fileSvc.Remove(context.Background(), relPath); err != nil {
			purgeErrors = append(purgeErrors, fmt.Errorf("%w: %s: %w", constants.ErrKeyStoreDeleteFile, entry.Name(), err))
		}
	}

	if len(purgeErrors) > 0 {
		return fmt.Errorf("%w: %d errors, first: %w", constants.ErrKeyStorePurgeFailed, len(purgeErrors), purgeErrors[0])
	}

	k.logger.Info("[Keystore] All secrets purged", "keyring", k.keyring.Name())
	return nil
}

// EnforcePermissions enforces strict filesystem permissions on the secrets directory.
func (k *Keystore) EnforcePermissions() error {
	if err := k.fileSvc.EnforceDirPermissions(context.Background(), constants.SecretsDirname, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreChmodDir, err)
	}
	entries, err := k.fileSvc.ReadDir(context.Background(), constants.SecretsDirname)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrKeyStoreReadDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		relPath := filepath.Join(constants.SecretsDirname, entry.Name())
		if err := k.fileSvc.EnforceFilePermissions(context.Background(), relPath, constants.PermFilePrivate); err != nil {
			return fmt.Errorf("%w: %w", constants.ErrKeyStoreChmodFile, err)
		}
	}

	k.logger.Debug("[Keystore] Permissions enforced", "dir", constants.SecretsDirname)
	return nil
}

// KeyringName returns the name of the master key store.
func (k *Keystore) KeyringName() string {
	return k.keyring.Name()
}

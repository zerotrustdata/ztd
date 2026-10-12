// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const testBinding = "test/binding"

func TestNewWithKeyringAndFS(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NotNil(t, ks)
	assert.Equal(t, "memory", ks.KeyringName())
}

func TestKeystore_Initialize_GeneratesNewKey(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.NoError(t, err)

	key, err := keyring.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Len(t, key, vault.KeySize)
}

func TestKeystore_Initialize_RetrievesExistingKey(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	testKey := make([]byte, vault.KeySize)
	for i := range testKey {
		testKey[i] = byte(i)
	}
	err := keyring.StoreMasterKey(testKey)
	require.NoError(t, err)

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.NoError(t, err)

	retrievedKey, err := keyring.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, testKey, retrievedKey)
}

func TestKeystore_Initialize_RejectsInvalidKeyLength(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	shortKey := []byte("too-short")
	err := keyring.StoreMasterKey(shortKey)
	require.NoError(t, err)

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreInvalidKeyLength)
}

func TestKeystore_EncryptSecret(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.EncryptSecret("test-secret", "my-plaintext-value")
	require.NoError(t, err)

	secretPath := filepath.Join(secretsDir, "test-secret")
	info, err := os.Stat(secretPath)
	require.NoError(t, err)
	assert.NotZero(t, info.Size())
}

func TestKeystore_EncryptSecret_Atomically(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.EncryptSecret("test-secret", "my-plaintext-value")
	require.NoError(t, err)

	tmpPath := filepath.Join(secretsDir, "test-secret.tmp")
	_, err = os.Stat(tmpPath)
	assert.True(t, os.IsNotExist(err), "temp file should be cleaned up")
}

func TestKeystore_DecryptSecret(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	plaintext := "my-plaintext-value"
	err = ks.EncryptSecret("test-secret", plaintext)
	require.NoError(t, err)

	decrypted, err := ks.DecryptSecret("test-secret")
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestKeystore_DecryptSecret_MissingFile(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	_, err = ks.DecryptSecret("nonexistent-secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreReadFailed)
}

func TestKeystore_DecryptSecret_CorruptedFile(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	corruptedData := []byte(`{"version":2,"nonce":"AAAAAA==","ciphertext":"corrupted"}`)
	secretPath := filepath.Join(secretsDir, "test-secret")
	err = os.WriteFile(secretPath, corruptedData, 0600)
	require.NoError(t, err)

	_, err = ks.DecryptSecret("test-secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreUnmarshalFailed)
}

func TestKeystore_DeleteSecret(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.EncryptSecret("test-secret", "my-plaintext-value")
	require.NoError(t, err)

	err = ks.DeleteSecret("test-secret")
	require.NoError(t, err)

	secretPath := filepath.Join(secretsDir, "test-secret")
	_, err = os.Stat(secretPath)
	assert.True(t, os.IsNotExist(err))
}

func TestKeystore_DeleteSecret_Nonexistent(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.DeleteSecret("nonexistent-secret")
	require.NoError(t, err)
}

func TestKeystore_Purge(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.EncryptSecret("secret1", "value1")
	require.NoError(t, err)
	err = ks.EncryptSecret("secret2", "value2")
	require.NoError(t, err)

	err = ks.Purge()
	require.NoError(t, err)

	_, err = keyring.RetrieveMasterKey()
	require.Error(t, err)
	assert.Equal(t, constants.ErrKeyStoreKeyNotFound, err)

	entries, err := os.ReadDir(secretsDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestKeystore_EnsurePermissions(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	err = ks.EncryptSecret("test-secret", "value")
	require.NoError(t, err)

	err = ks.EnforcePermissions()
	require.NoError(t, err)

	info, err := os.Stat(secretsDir)
	require.NoError(t, err)
	// Windows doesn't support Unix permissions exactly, so skip the permission check on Windows
	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		assert.NotEqual(t, os.FileMode(0777), perm, "directory should not be world-writable")
	}

	secretPath := filepath.Join(secretsDir, "test-secret")
	info, err = os.Stat(secretPath)
	require.NoError(t, err)
	// Windows doesn't support Unix permissions exactly, so skip the permission check on Windows
	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		assert.NotEqual(t, os.FileMode(0777), perm, "secret file should not be world-writable")
	}
}

func TestKeystore_KeyringName(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	assert.Equal(t, "memory", ks.KeyringName())
}

func TestKeystore_Encrypt(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	plaintext := "my-plaintext-value"
	encoded, err := ks.Encrypt(testBinding, plaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)
	assert.NotEqual(t, plaintext, encoded)
}

func TestKeystore_Decrypt(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	plaintext := "my-plaintext-value"
	encoded, err := ks.Encrypt(testBinding, plaintext)
	require.NoError(t, err)

	decrypted, err := ks.Decrypt(testBinding, encoded)
	require.NoError(t, err)
	assert.Equal(t, plaintext, decrypted)
}

func TestKeystore_EncryptDecrypt_RoundTrip(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	testCases := []string{
		"simple text",
		"text with special chars: !@#$%^&*()",
		"text with unicode: café 日本語 🚀",
		"multi\nline\ntext",
		"very long text: " + string(make([]byte, 1000)),
	}

	for _, plaintext := range testCases {
		t.Run(plaintext[:min(20, len(plaintext))], func(t *testing.T) {
			encoded, err := ks.Encrypt(testBinding, plaintext)
			require.NoError(t, err)

			decrypted, err := ks.Decrypt(testBinding, encoded)
			require.NoError(t, err)
			assert.Equal(t, plaintext, decrypted)
		})
	}
}

func TestKeystore_Decrypt_InvalidBase64(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	_, err = ks.Decrypt(testBinding, "not-valid-base64!!!")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode base64")
}

func TestKeystore_Decrypt_InvalidJSON(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	invalidJSON := base64.StdEncoding.EncodeToString([]byte(`{invalid json`))
	_, err = ks.Decrypt(testBinding, invalidJSON)
	require.Error(t, err)
	assert.Error(t, err)
}

func TestKeystore_Decrypt_UnsupportedVersion(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	enc := keystore.EncryptedSecret{
		Version:    999,
		Nonce:      make([]byte, vault.NonceSize),
		Ciphertext: []byte("fake"),
	}
	data, err := json.Marshal(enc)
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(data)

	_, err = ks.Decrypt(testBinding, encoded)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported secret version")
}

func TestKeystore_Decrypt_InvalidCiphertext(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	enc := keystore.EncryptedSecret{
		Version:    2,
		Nonce:      make([]byte, vault.NonceSize),
		Ciphertext: []byte("invalid ciphertext that will fail GCM"),
	}
	data, err := json.Marshal(enc)
	require.NoError(t, err)
	encoded := base64.StdEncoding.EncodeToString(data)

	_, err = ks.Decrypt(testBinding, encoded)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ciphertext")
}

func TestKeystore_Encrypt_EmptyPlaintext(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	encoded, err := ks.Encrypt(testBinding, "")
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decrypted, err := ks.Decrypt(testBinding, encoded)
	require.NoError(t, err)
	assert.Empty(t, decrypted)
}

func TestKeystore_Encrypt_LargePlaintext(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	largePlaintext := string(make([]byte, 10000))
	for i := range largePlaintext {
		largePlaintext = largePlaintext[:i] + "A" + largePlaintext[i+1:]
	}
	encoded, err := ks.Encrypt(testBinding, largePlaintext)
	require.NoError(t, err)
	assert.NotEmpty(t, encoded)

	decrypted, err := ks.Decrypt(testBinding, encoded)
	require.NoError(t, err)
	assert.Equal(t, largePlaintext, decrypted)
}

func TestKeystore_DecryptSecret_InvalidJSON(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	invalidJSON := []byte(`{invalid json`)
	secretPath := filepath.Join(secretsDir, "test-secret")
	err = os.WriteFile(secretPath, invalidJSON, 0600)
	require.NoError(t, err)

	_, err = ks.DecryptSecret("test-secret")
	require.Error(t, err)
	assert.Error(t, err)
}

func TestKeystore_DecryptSecret_UnsupportedVersion(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := keystoretest.NewMemoryKeyring()

	ks, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	enc := keystore.EncryptedSecret{
		Version:    999,
		Nonce:      make([]byte, vault.NonceSize),
		Ciphertext: []byte("fake"),
	}
	data, err := json.Marshal(enc)
	require.NoError(t, err)
	secretPath := filepath.Join(secretsDir, "test-secret")
	err = os.WriteFile(secretPath, data, 0600)
	require.NoError(t, err)

	_, err = ks.DecryptSecret("test-secret")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported secret version")
}

func TestKeystore_RejectsUnsafeSecretNames(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	for _, name := range []string{"", ".", "..", "../config", "a/../../b", "sub/name", `a\b`, ".master_key.ssh-agent", ".ztd-tmp-1", "nul\x00"} {
		require.ErrorIs(t, ks.EncryptSecret(name, "v"), constants.ErrKeyStoreInvalidSecretName, name)
		_, err := ks.DecryptSecret(name)
		require.ErrorIs(t, err, constants.ErrKeyStoreInvalidSecretName, name)
		require.ErrorIs(t, ks.DeleteSecret(name), constants.ErrKeyStoreInvalidSecretName, name)
		require.ErrorIs(t, ks.StoreKeyMaterial(name, []byte{1}), constants.ErrKeyStoreInvalidSecretName, name)
	}
}

func TestKeystore_Initialize_RejectsOrphanedSecrets(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	logger := testutil.NewTestLogger()
	original, err := keystore.NewWithKeyringAndFS(logger, keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, original.Initialize())
	require.NoError(t, original.EncryptSecret("session_token", "sealed"))

	keyring := keystoretest.NewMemoryKeyring()
	replacement, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	err = replacement.Initialize()
	require.ErrorIs(t, err, constants.ErrKeyStoreOrphanedSecrets)
	assert.Contains(t, err.Error(), "session_token")
	_, err = keyring.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound, "no replacement key may be stored")

	require.NoError(t, os.Remove(filepath.Join(secretsDir, "session_token")))
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, constants.SecretsFileBootstrapDigest), []byte(`{"version":1,"secrets":{}}`), constants.PermFilePrivate))
	require.NoError(t, replacement.Initialize(), "files EncryptSecret did not write are not sealed secrets")
}

func TestKeystore_KeyMaterialRoundTrip(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	material := []byte{0x00, 0x01, 0xfe, 0xff}
	require.NoError(t, ks.StoreKeyMaterial("material", material))
	got, err := ks.LoadKeyMaterial("material", len(material))
	require.NoError(t, err)
	assert.Equal(t, material, got)
	_, err = ks.LoadKeyMaterial("material", len(material)+1)
	require.ErrorIs(t, err, constants.ErrKeyStoreInvalidKeyLength)
}

func TestKeystore_CiphertextIsBoundToName(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EncryptSecret("api_key_a", "secret-a"))

	sealed, err := os.ReadFile(filepath.Join(secretsDir, "api_key_a"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "api_key_b"), sealed, constants.PermFilePrivate))
	_, err = ks.DecryptSecret("api_key_b")
	require.ErrorIs(t, err, constants.ErrInvalidCiphertext, "a secret moved to another name must not open")

	_, err = ks.Decrypt("api_key_a", base64.StdEncoding.EncodeToString(sealed))
	require.ErrorIs(t, err, constants.ErrInvalidCiphertext, "a secret file must not open as a sealed value")
}

func TestKeystore_ValueIsBoundToBinding(t *testing.T) {
	fileSvc, _ := keystoretest.NewTestFileService(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	sealed, err := ks.Encrypt("public/a", "value")
	require.NoError(t, err)
	got, err := ks.Decrypt("public/a", sealed)
	require.NoError(t, err)
	assert.Equal(t, "value", got)
	_, err = ks.Decrypt("public/b", sealed)
	require.ErrorIs(t, err, constants.ErrInvalidCiphertext)

	_, err = ks.Encrypt("", "value")
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	_, err = ks.Decrypt("", sealed)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestKeystore_RejectsVersion1Secrets(t *testing.T) {
	fileSvc, secretsDir := keystoretest.NewTestFileService(t)
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EncryptSecret("session_token", "v"))

	path := filepath.Join(secretsDir, "session_token")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var enc keystore.EncryptedSecret
	require.NoError(t, json.Unmarshal(data, &enc))
	enc.Version = 1
	data, err = json.Marshal(enc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, constants.PermFilePrivate))

	_, err = ks.DecryptSecret("session_token")
	require.ErrorIs(t, err, constants.ErrKeyStoreUnsupportedVersion)

	replacement, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.ErrorIs(t, replacement.Initialize(), constants.ErrKeyStoreOrphanedSecrets, "version 1 secrets still block a silent new key")
}

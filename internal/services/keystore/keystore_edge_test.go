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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const testBinding = "test/binding"

func testMasterKey(seed byte) []byte {
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func setupTestFileService(t *testing.T) (fs.RuntimeFileService, string) {
	t.Helper()
	baseDir := testutil.TempDir(t)
	svc, err := fs.NewRuntimeFileService(baseDir, testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))
	return svc, svc.Resolve(constants.SecretsDirname)
}

func TestKeystore_Initialize_RetrieveError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	retrieveErr := errors.New("retrieve failed")
	keyring := &errorKeyring{retrieveErr: retrieveErr}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreRetrieveFailed)
}

func TestKeystore_Initialize_StoreError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := &errorKeyring{storeErr: errors.New("store failed")}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreStoreFailed)
}

func TestKeystore_Initialize_VerifyFailed_DropsWrites(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	// dropsWritesKeyring accepts StoreMasterKey, but RetrieveMasterKey returns ErrKeyStoreKeyNotFound
	keyring := &dropsWritesKeyring{}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreVerifyFailed)
}

func TestKeystore_Initialize_VerifyFailed_Corrupted(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	// corruptingKeyring stores one thing but returns altered bytes
	keyring := &corruptingKeyring{}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreVerifyFailed)
}

func TestKeystore_Encrypt_RetrieveError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	retrieveErr := errors.New("retrieve failed")
	keyring := &errorKeyring{retrieveErr: retrieveErr}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	_, err = ks.Encrypt(testBinding, "plaintext")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreRetrieveFailed)
}

func TestKeystore_EncryptSecret_RetrieveError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	retrieveErr := errors.New("retrieve failed")
	keyring := &errorKeyring{retrieveErr: retrieveErr}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.EncryptSecret("test", "value")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreRetrieveFailed)
}

func TestKeystore_Decrypt_RetrieveError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	retrieveErr := errors.New("retrieve failed")
	keyring := &errorKeyring{retrieveErr: retrieveErr}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	_, err = ks.Decrypt(testBinding, "dGVzdA==")
	require.Error(t, err)
}

func TestKeystore_Purge_DeleteMasterKeyError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := &errorKeyring{deleteErr: errors.New("delete failed")}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.Purge()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreDeleteFailed)
}

func TestKeystore_EnforcePermissions_DeletedDir(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(secretsDir))

	err = ks.EnforcePermissions()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreChmodDir)
}

func TestKeystore_Purge_WithSecrets(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	require.NoError(t, ks.EncryptSecret("s1", "v1"))
	require.NoError(t, ks.EncryptSecret("s2", "v2"))

	err = ks.Purge()
	require.NoError(t, err)

	entries, err := os.ReadDir(secretsDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, !e.IsDir(), "unexpected file entry: %s", e.Name())
	}
}

func TestKeystore_EnforcePermissions_ReadDirError(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(secretsDir))

	err = ks.EnforcePermissions()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreChmodDir)
}

func TestKeystore_Purge_ReadDirError(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	require.NoError(t, os.RemoveAll(secretsDir))

	err = ks.Purge()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreReadDir)
}

func TestKeystore_DecryptSecret_CorruptCiphertext(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	enc := EncryptedSecret{
		Version:    2,
		Nonce:      make([]byte, vault.NonceSize),
		Ciphertext: []byte("invalid ciphertext that will fail GCM"),
	}
	data, err := json.Marshal(enc)
	require.NoError(t, err)

	secretPath := filepath.Join(secretsDir, "test-secret")
	require.NoError(t, os.WriteFile(secretPath, data, 0600))

	_, err = ks.DecryptSecret("test-secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidCiphertext)
}

func TestKeystore_EncryptSecret_MarshalError(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := &errorKeyring{storeErr: errors.New("store failed")}

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)

	err = ks.EncryptSecret("test", "value")
	require.Error(t, err)
}

func TestKeystore_DeleteSecret_Error(t *testing.T) {
	t.Parallel()
	fileSvc, secretsDir := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	keyring := newMemoryKeyring()

	ks, err := NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	// Create a non-empty directory with the secret name so os.Remove fails with ENOTEMPTY
	secretDir := filepath.Join(secretsDir, "test-secret")
	require.NoError(t, os.Mkdir(secretDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(secretDir, "nested"), []byte("data"), 0o600))

	err = ks.DeleteSecret("test-secret")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreDeleteSecret)
}

// errorKeyring is a test keyring that returns configurable errors.
type errorKeyring struct {
	storeErr    error
	retrieveErr error
	deleteErr   error
}

func (e *errorKeyring) Name() string { return "error" }

func (e *errorKeyring) RetrieveMasterKey() ([]byte, error) {
	if e.retrieveErr != nil {
		return nil, e.retrieveErr
	}
	return nil, constants.ErrKeyStoreKeyNotFound
}

func (e *errorKeyring) StoreMasterKey([]byte) error {
	return e.storeErr
}

func (e *errorKeyring) DeleteMasterKey() error {
	return e.deleteErr
}

// dropsWritesKeyring simulates a keyring that silently drops stores.
type dropsWritesKeyring struct{}

func (d *dropsWritesKeyring) Name() string { return "drops-writes" }

func (d *dropsWritesKeyring) RetrieveMasterKey() ([]byte, error) {
	return nil, constants.ErrKeyStoreKeyNotFound
}

func (d *dropsWritesKeyring) StoreMasterKey([]byte) error {
	return nil
}

func (d *dropsWritesKeyring) DeleteMasterKey() error {
	return nil
}

// corruptingKeyring simulates a keyring that corrupts the stored key.
type corruptingKeyring struct {
	key []byte
}

func (c *corruptingKeyring) Name() string { return "corrupting" }

func (c *corruptingKeyring) RetrieveMasterKey() ([]byte, error) {
	if c.key == nil {
		return nil, constants.ErrKeyStoreKeyNotFound
	}
	corrupted := make([]byte, len(c.key))
	copy(corrupted, c.key)
	corrupted[0] ^= 0xff
	return corrupted, nil
}

func (c *corruptingKeyring) StoreMasterKey(key []byte) error {
	c.key = make([]byte, len(key))
	copy(c.key, key)
	return nil
}

func (c *corruptingKeyring) DeleteMasterKey() error {
	c.key = nil
	return nil
}

// newMemoryKeyring creates a simple in-memory keyring for internal tests.
func newMemoryKeyring() *simpleMemoryKeyring {
	return &simpleMemoryKeyring{}
}

type simpleMemoryKeyring struct {
	key []byte
}

func (m *simpleMemoryKeyring) Name() string { return "memory" }

func (m *simpleMemoryKeyring) RetrieveMasterKey() ([]byte, error) {
	if m.key == nil {
		return nil, constants.ErrKeyStoreKeyNotFound
	}
	cp := make([]byte, len(m.key))
	copy(cp, m.key)
	return cp, nil
}

func (m *simpleMemoryKeyring) StoreMasterKey(key []byte) error {
	cp := make([]byte, len(key))
	copy(cp, key)
	m.key = cp
	return nil
}

func (m *simpleMemoryKeyring) DeleteMasterKey() error {
	m.key = nil
	return nil
}

// approvalKeyring holds RetrieveMasterKey the way the macOS Keychain does while
// its access prompt for a rebuilt binary is open, until the test releases it.
type approvalKeyring struct {
	simpleMemoryKeyring
	release chan struct{}
}

func (a *approvalKeyring) RetrieveMasterKey() ([]byte, error) {
	<-a.release
	return a.simpleMemoryKeyring.RetrieveMasterKey()
}

func TestKeystore_Initialize_LogsWhileAwaitingKeyStoreApproval(t *testing.T) {
	keyring := &approvalKeyring{release: make(chan struct{})}
	require.NoError(t, keyring.StoreMasterKey(testMasterKey(7)))
	var logs syncBuffer
	ks := &Keystore{logger: slog.New(slog.NewTextHandler(&logs, nil)), keyring: keyring}

	done := make(chan error, 1)
	go func() { done <- ks.Initialize() }()
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String(), constants.KeyStoreAwaitingApprovalLog)
	}, constants.KeyStoreApprovalNoticeDelay+5*time.Second, 50*time.Millisecond)
	close(keyring.release)
	require.NoError(t, <-done)
}

func TestKeystore_Initialize_FastRetrievalLogsNoApprovalWait(t *testing.T) {
	keyring := newMemoryKeyring()
	require.NoError(t, keyring.StoreMasterKey(testMasterKey(7)))
	var logs syncBuffer
	ks := &Keystore{logger: slog.New(slog.NewTextHandler(&logs, nil)), keyring: keyring}

	require.NoError(t, ks.Initialize())
	assert.NotContains(t, logs.String(), constants.KeyStoreAwaitingApprovalLog)
}

// syncBuffer is a bytes.Buffer safe for a logger goroutine and a reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// countingKeyring counts master key reads, each of which can be an OS key
// store prompt.
type countingKeyring struct {
	simpleMemoryKeyring
	reads int
}

func (c *countingKeyring) RetrieveMasterKey() ([]byte, error) {
	c.reads++
	return c.simpleMemoryKeyring.RetrieveMasterKey()
}

func TestKeystore_ReadsMasterKeyOncePerProcess(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	keyring := &countingKeyring{}
	require.NoError(t, keyring.StoreMasterKey(testMasterKey(3)))
	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)

	require.NoError(t, ks.Initialize())
	for i := range 5 {
		name := fmt.Sprintf("secret-%d", i)
		require.NoError(t, ks.EncryptSecret(name, "value"))
		got, err := ks.DecryptSecret(name)
		require.NoError(t, err)
		require.Equal(t, "value", got)
	}
	assert.Equal(t, 1, keyring.reads, "the master key must be read from the key store once")

	held := ks.masterKey
	ks.Close()
	assert.Nil(t, ks.masterKey)
	assert.Equal(t, make([]byte, vault.KeySize), held, "Close must zero the held key")
	got, err := ks.DecryptSecret("secret-0")
	require.NoError(t, err)
	assert.Equal(t, "value", got)
	assert.Equal(t, 2, keyring.reads, "an operation after Close reads the key again")
}

func TestKeystore_GeneratedKeyIsHeld(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	keyring := &countingKeyring{}
	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)

	require.NoError(t, ks.Initialize())
	reads := keyring.reads
	require.NoError(t, ks.EncryptSecret("secret", "value"))
	assert.Equal(t, reads, keyring.reads, "the generated and verified key is held")
}

func TestKeystore_PurgeZeroesHeldKey(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	keyring := &countingKeyring{}
	require.NoError(t, keyring.StoreMasterKey(testMasterKey(5)))
	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EncryptSecret("secret", "value"))

	held := ks.masterKey
	require.NoError(t, ks.Purge())
	assert.Nil(t, ks.masterKey)
	assert.Equal(t, make([]byte, vault.KeySize), held)
	_, err = ks.DecryptSecret("secret")
	require.Error(t, err)
}

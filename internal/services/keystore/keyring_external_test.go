// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package keystore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	runtimefs "github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestExternalKeyring_RejectsRelativePath(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	_, err := newExternalFileKeyring(fileSvc, "relative/path/key")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
}

func TestExternalKeyring_RejectsPathInsideRuntimeDir(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	insidePath := filepath.Join(fileSvc.Resolve(""), "master.key")
	_, err := newExternalFileKeyring(fileSvc, insidePath)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
}

func TestExternalKeyring_StoreMasterKey_ReadOnly(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "master.key")

	kr, err := newExternalFileKeyring(fileSvc, extPath)
	require.NoError(t, err)
	assert.Equal(t, "external-file", kr.Name())

	err = kr.StoreMasterKey(make([]byte, vault.KeySize))
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreExternalReadOnly)

	// DeleteMasterKey is a no-op
	assert.NoError(t, kr.DeleteMasterKey())
}

func TestExternalKeyring_RetrieveMasterKey_NotFound(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "nonexistent.key")

	kr, err := newExternalFileKeyring(fileSvc, extPath)
	require.NoError(t, err)

	_, err = kr.RetrieveMasterKey()
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestExternalKeyring_RetrieveMasterKey_EmptyFile(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "empty.key")
	require.NoError(t, os.WriteFile(extPath, []byte(""), 0600))

	kr, err := newExternalFileKeyring(fileSvc, extPath)
	require.NoError(t, err)

	_, err = kr.RetrieveMasterKey()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreInvalidKeyLength)
}

func TestExternalKeyring_RetrieveMasterKey_InvalidBase64(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "bad_b64.key")
	require.NoError(t, os.WriteFile(extPath, []byte("not!valid@base64"), 0600))

	kr, err := newExternalFileKeyring(fileSvc, extPath)
	require.NoError(t, err)

	_, err = kr.RetrieveMasterKey()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreDecodeFailed)
}

func TestExternalKeyring_WrongKeyLength(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "short.key")

	shortKey := make([]byte, 16) // 16 bytes instead of 32
	_, _ = rand.Read(shortKey)
	require.NoError(t, os.WriteFile(extPath, []byte(base64.StdEncoding.EncodeToString(shortKey)), 0600))

	ks, err := NewWithFS(fileSvc, logger, Options{MasterKeyFile: extPath})
	require.NoError(t, err)

	err = ks.Initialize()
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrKeyStoreInvalidKeyLength)
}

func TestExternalKeyring_SuccessRoundTrip(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()

	extDir := testutil.TempDir(t)
	extPath := filepath.Join(extDir, "valid.key")

	validKey := make([]byte, vault.KeySize)
	_, err := rand.Read(validKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(extPath, []byte(base64.StdEncoding.EncodeToString(validKey)+"\n"), 0600))

	ks, err := NewWithFS(fileSvc, logger, Options{MasterKeyFile: extPath})
	require.NoError(t, err)
	assert.Equal(t, "external-file", ks.KeyringName())

	require.NoError(t, ks.Initialize())

	// Test encrypt/decrypt secret with external master key
	secretName := "test-ext-secret"
	secretVal := "super-confidential"
	require.NoError(t, ks.EncryptSecret(secretName, secretVal))

	decrypted, err := ks.DecryptSecret(secretName)
	require.NoError(t, err)
	assert.Equal(t, secretVal, decrypted)
}

func TestExternalKeyring_RejectsDotDotNameInsideRuntime(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	_, err := newExternalFileKeyring(fileSvc, filepath.Join(fileSvc.Resolve(""), "..key"))
	require.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
}

func TestExternalKeyring_RetrievalRejectsShortKey(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	path := filepath.Join(testutil.TempDir(t), "key")
	require.NoError(t, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(make([]byte, 16))), constants.PermFilePrivate))
	kr, err := newExternalFileKeyring(fileSvc, path)
	require.NoError(t, err)
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreInvalidKeyLength)
}

func TestExternalKeyring_SelectionAndContinuity(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	logger := testutil.NewTestLogger()
	dir := testutil.TempDir(t)
	a := filepath.Join(dir, "a")
	keyA := []byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, vault.KeySize)))
	require.NoError(t, os.WriteFile(a, keyA, constants.PermFilePrivate))
	ks, err := Open(fileSvc, logger, Options{MasterKeyFile: " " + a + " "})
	require.NoError(t, err)
	require.NoError(t, ks.EncryptSecret("continuity", "existing value"))
	reopened, err := NewWithFS(fileSvc, logger, Options{MasterKeyFile: a})
	require.NoError(t, err)
	value, err := reopened.DecryptSecret("continuity")
	require.NoError(t, err)
	require.Equal(t, "existing value", value)
	for _, blank := range []string{"", "  \t"} {
		// A blank option selects the host OS key store, never the provisioned
		// file key. A host without one fails closed at construction; otherwise
		// the store holds either another key (ciphertext rejected) or none at
		// all (key not found). The file key must not decrypt in any case.
		wrong, err := NewWithFS(fileSvc, logger, Options{MasterKeyFile: blank})
		if err == nil {
			_, err = wrong.DecryptSecret("continuity")
		}
		require.Error(t, err)
		require.True(t, errors.Is(err, constants.ErrKeyStoreOSKeyringRequired) || errors.Is(err, constants.ErrInvalidCiphertext) || errors.Is(err, constants.ErrKeyStoreKeyNotFound), "blank master key file must not select the provisioned key: %v", err)
	}
	_, err = Open(fileSvc, logger, Options{MasterKeyFile: filepath.Join(dir, "missing")})
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = Open(fileSvc, logger, Options{MasterKeyFile: "relative"})
	require.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
	value, err = reopened.DecryptSecret("continuity")
	require.NoError(t, err)
	require.Equal(t, "existing value", value)
	contents, err := os.ReadFile(a)
	require.NoError(t, err)
	require.Equal(t, keyA, contents)
}

func TestExternalKeyring_ResolvedSeparationAndRevalidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	fileSvc, _ := setupTestFileService(t)
	dir := testutil.TempDir(t)
	data := []byte(base64.StdEncoding.EncodeToString(make([]byte, vault.KeySize)))
	safe := filepath.Join(dir, "safe")
	require.NoError(t, os.WriteFile(safe, data, constants.PermFilePrivate))
	link := filepath.Join(dir, "mounted-secret")
	testutil.Symlink(t, safe, link)
	kr, err := newExternalFileKeyring(fileSvc, link)
	require.NoError(t, err)
	_, err = kr.RetrieveMasterKey()
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), "inside-key", data, constants.PermFilePrivate))
	require.NoError(t, os.Remove(link))
	testutil.Symlink(t, fileSvc.Resolve("inside-key"), link)
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
	for _, path := range []string{fileSvc.Resolve(""), fileSvc.Resolve("../" + filepath.Base(fileSvc.Resolve("")) + "/key")} {
		_, err = newExternalFileKeyring(fileSvc, path)
		require.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
	}
	sibling := fileSvc.Resolve("") + "-key"
	require.NoError(t, os.WriteFile(sibling, data, constants.PermFilePrivate))
	kr, err = newExternalFileKeyring(fileSvc, sibling)
	require.NoError(t, err)
	_, err = kr.RetrieveMasterKey()
	require.NoError(t, err)
}

func TestExternalKeyring_InvalidInputsRemainUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, contents string
		want           error
	}{
		{"empty", "", constants.ErrKeyStoreInvalidKeyLength},
		{"whitespace", " \n", constants.ErrKeyStoreInvalidKeyLength},
		{"malformed", "not!base64", constants.ErrKeyStoreDecodeFailed},
		{"short", base64.StdEncoding.EncodeToString(make([]byte, 16)), constants.ErrKeyStoreInvalidKeyLength},
		{"long", base64.StdEncoding.EncodeToString(make([]byte, 33)), constants.ErrKeyStoreInvalidKeyLength},
		{"oversized", strings.Repeat("A", 4097), constants.ErrKeyStoreExternalFileInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, _ := setupTestFileService(t)
			path := filepath.Join(testutil.TempDir(t), "key")
			require.NoError(t, os.WriteFile(path, []byte(tc.contents), constants.PermFilePrivate))
			_, err := Open(fileSvc, testutil.NewTestLogger(), Options{MasterKeyFile: path})
			require.ErrorIs(t, err, tc.want)
			require.NotErrorIs(t, err, constants.ErrKeyStoreOSKeyringRequired)
			require.NotErrorIs(t, err, constants.ErrKeyStoreExternalReadOnly)
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.contents, string(data))
		})
	}
}

func TestExternalKeyring_RejectsResolvedRuntimeRootAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires Windows privileges")
	}
	realBase := testutil.TempDir(t)
	alias := filepath.Join(testutil.TempDir(t), "runtime-parent")
	testutil.Symlink(t, realBase, alias)
	realSvc, err := runtimefs.NewRuntimeFileService(realBase, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, realSvc.CreateRuntimeTree(context.Background()))
	require.NoError(t, realSvc.WriteFile(context.Background(), "inside-key", []byte(base64.StdEncoding.EncodeToString(make([]byte, vault.KeySize))), constants.PermFilePrivate))
	aliasSvc, err := runtimefs.NewRuntimeFileService(alias, testutil.NewTestLogger())
	require.NoError(t, err)
	kr, err := newExternalFileKeyring(aliasSvc, realSvc.Resolve("inside-key"))
	require.NoError(t, err)
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreExternalKeyPath)
}

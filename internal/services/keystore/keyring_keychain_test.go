// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin && integration

package keystore

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// testKeychainKeyring targets a unique item so tests never touch the real
// master key, and removes it when the test ends.
func testKeychainKeyring(t *testing.T) Keyring {
	t.Helper()
	kr, err := newKeychainKeyringFor(fmt.Sprintf("%s-test-%s", keyStoreName, rand.Text()), masterKeyName)
	require.NoError(t, err)
	t.Cleanup(func() { _ = kr.DeleteMasterKey() })
	return kr
}

func TestKeychainKeyring_StoreRetrieveDelete(t *testing.T) {
	kr := testKeychainKeyring(t)
	assert.Equal(t, "keychain", kr.Name())

	_, err := kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)

	key := testMasterKey(31)
	require.NoError(t, kr.StoreMasterKey(key))
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)

	require.NoError(t, kr.DeleteMasterKey())
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
	require.NoError(t, kr.DeleteMasterKey(), "delete is idempotent")
}

func TestKeychainKeyring_NeverReplacesExistingKey(t *testing.T) {
	kr := testKeychainKeyring(t)
	require.NoError(t, kr.StoreMasterKey(testMasterKey(32)))
	require.ErrorIs(t, kr.StoreMasterKey(testMasterKey(33)), constants.ErrKeyStoreDuplicateKey)
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, testMasterKey(32), got)
}

func TestKeychainKeyring_RejectsWrongKeyLength(t *testing.T) {
	kr := testKeychainKeyring(t)
	require.ErrorIs(t, kr.StoreMasterKey(make([]byte, vault.KeySize-1)), constants.ErrKeyStoreInvalidKeyLength)
}

func TestKeychainKeyring_KeystoreRoundTrip(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), testKeychainKeyring(t), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EncryptSecret("session_token", "sealed"))
	got, err := ks.DecryptSecret("session_token")
	require.NoError(t, err)
	assert.Equal(t, "sealed", got)
}

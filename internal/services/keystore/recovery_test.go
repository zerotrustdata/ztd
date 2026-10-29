// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package keystore

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestRecoveryKeyRoundTripAndConfirmation(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x11}, vault.KeySize)
	grouped, err := encodeRecoveryKey(key)
	require.NoError(t, err)
	defer vault.SecureZero(grouped)

	parsed, err := ParseRecoveryKey(append([]byte("  "), append(bytes.ToLower(grouped), '\n')...))
	require.NoError(t, err)
	defer vault.SecureZero(parsed)
	assert.Equal(t, key, parsed)

	var out bytes.Buffer
	err = ConfirmRecoveryKey(&out, bytes.NewReader(append(append([]byte(nil), grouped...), '\n')), RecoveryRoleGateway, "keychain", key, true, false)
	require.NoError(t, err)
	text := out.String()
	assert.Contains(t, text, "shown once")
	assert.Contains(t, text, "cannot be restored")
	assert.Contains(t, text, "There is no backdoor")
	assert.Contains(t, text, "Every gateway and operator on this login")
	assert.Contains(t, text, string(grouped))
	assert.Contains(t, text, RecoveryFingerprint(key))
	assert.Contains(t, text, "Recovery key confirmed")
	assert.NotContains(t, text, "--accept-unrecoverable: no recovery key")
}

func TestConfirmRecoveryKeyFailClosed(t *testing.T) {
	t.Parallel()
	key := bytes.Repeat([]byte{0x22}, vault.KeySize)
	grouped, err := encodeRecoveryKey(key)
	require.NoError(t, err)
	defer vault.SecureZero(grouped)

	t.Run("mismatch stores nothing", func(t *testing.T) {
		t.Parallel()
		fileSvc, _ := setupTestFileService(t)
		ring := newMemoryKeyring()
		var out bytes.Buffer
		ks, err := NewWithFS(fileSvc, testutil.NewTestLogger(), Options{
			Keyring: ring,
			Ceremony: func(generated []byte) error {
				return ConfirmRecoveryKey(&out, strings.NewReader("nope\n"), RecoveryRoleOperator, "ssh-agent", generated, true, false)
			},
		})
		require.NoError(t, err)
		err = ks.Initialize()
		require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryMismatch)
		_, err = ring.RetrieveMasterKey()
		require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
		assert.NotContains(t, out.String(), "Recovery key confirmed")
		assert.Contains(t, out.String(), "Nothing was stored")
		assert.Contains(t, out.String(), "This key opens only this runtime tree")
	})

	t.Run("declined stores nothing", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		err := ConfirmRecoveryKey(&out, strings.NewReader(""), RecoveryRoleGateway, "memory", key, true, false)
		require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryDeclined)
		assert.Contains(t, out.String(), "No confirmation")
	})

	t.Run("no terminal does not print the key", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		err := ConfirmRecoveryKey(&out, strings.NewReader(string(grouped)+"\n"), RecoveryRoleGateway, "keychain", key, false, false)
		require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryRequired)
		assert.NotContains(t, out.String(), string(grouped))
		assert.Contains(t, out.String(), "--accept-unrecoverable")
		assert.NotContains(t, out.String(), "shown once")
	})

	t.Run("accept unrecoverable does not print the key", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		err := ConfirmRecoveryKey(&out, strings.NewReader(""), RecoveryRoleOperator, "dpapi", key, false, true)
		require.NoError(t, err)
		assert.NotContains(t, out.String(), string(grouped))
		assert.Contains(t, out.String(), "no recovery key was saved")
		assert.Contains(t, out.String(), "cannot be restored")
	})

	t.Run("tampered checksum is a mismatch", func(t *testing.T) {
		t.Parallel()
		bad := append([]byte(nil), grouped...)
		bad[0] = 'A'
		if bad[0] == grouped[0] {
			bad[0] = 'B'
		}
		_, err := ParseRecoveryKey(bad)
		require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryMismatch)
	})
}

func TestInstallRecoveryKey(t *testing.T) {
	t.Parallel()
	fileSvc, _ := setupTestFileService(t)
	ring := newMemoryKeyring()
	logger := testutil.NewTestLogger()
	ks, err := NewWithKeyringAndFS(logger, ring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	saved, err := ring.RetrieveMasterKey()
	require.NoError(t, err)
	defer vault.SecureZero(saved)
	require.NoError(t, ks.EncryptSecret("vault_key", "sealed-value"))

	other := bytes.Repeat([]byte{0x33}, vault.KeySize)
	_, err = ks.InstallRecoveryKey(other)
	require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryConflict)
	still, err := ring.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, saved, still)
	vault.SecureZero(still)

	require.NoError(t, ring.DeleteMasterKey())
	fresh, err := NewWithKeyringAndFS(logger, ring, fileSvc)
	require.NoError(t, err)
	_, err = fresh.InstallRecoveryKey(other)
	require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryMismatch)
	_, err = ring.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)

	stored, err := fresh.InstallRecoveryKey(saved)
	require.NoError(t, err)
	assert.True(t, stored)
	got, err := fresh.DecryptSecret("vault_key")
	require.NoError(t, err)
	assert.Equal(t, "sealed-value", got)

	stored, err = fresh.InstallRecoveryKey(saved)
	require.NoError(t, err)
	assert.False(t, stored)
}

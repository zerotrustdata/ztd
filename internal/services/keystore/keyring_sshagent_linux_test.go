// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package keystore

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh/agent"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func memoryAgentDialer(t *testing.T, keys ...any) sshAgentDialer {
	t.Helper()
	ag := agent.NewKeyring()
	for _, k := range keys {
		require.NoError(t, ag.Add(agent.AddedKey{PrivateKey: k}))
	}
	return func() (agent.ExtendedAgent, func() error, error) {
		return ag.(agent.ExtendedAgent), func() error { return nil }, nil
	}
}

func newEd25519(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return priv
}

func noAgent() (agent.ExtendedAgent, func() error, error) {
	return nil, nil, constants.ErrKeyStoreSSHAgentUnavailable
}

func noSecretService() (secretService, error) {
	return nil, errors.New("no session bus")
}

func TestSSHAgentKeyring_SealRoundTrip(t *testing.T) {
	fileSvc, secretsDir := setupTestFileService(t)
	dial := memoryAgentDialer(t, newEd25519(t))
	kr := newSSHAgentKeyring(fileSvc, dial)

	_, err := kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
	key := testMasterKey(20)
	require.NoError(t, kr.StoreMasterKey(key))
	got, err := kr.RetrieveMasterKey()
	require.NoError(t, err)
	assert.Equal(t, key, got)

	raw, err := os.ReadFile(secretsDir + "/" + constants.MasterKeySSHAgentFilename)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), base64.StdEncoding.EncodeToString(key), "the master key is never stored in the clear")

	require.NoError(t, kr.DeleteMasterKey())
	_, err = kr.RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
}

func TestSSHAgentKeyring_WrongKeyCannotUnseal(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	dial := memoryAgentDialer(t, newEd25519(t))
	require.NoError(t, newSSHAgentKeyring(fileSvc, dial).StoreMasterKey(testMasterKey(21)))

	otherDial := memoryAgentDialer(t, newEd25519(t))
	_, err := newSSHAgentKeyring(fileSvc, otherDial).RetrieveMasterKey()
	require.ErrorIs(t, err, constants.ErrKeyStoreSSHAgentKeyMissing)
}

func TestSSHAgentKeyring_RejectsNondeterministicKeys(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	dial := memoryAgentDialer(t, ecKey)
	err = newSSHAgentKeyring(fileSvc, dial).StoreMasterKey(testMasterKey(22))
	require.ErrorIs(t, err, constants.ErrKeyStoreSSHAgentUnavailable)
}

func TestSelectLinuxKeyring_NoDefaultCollectionUsesSSHAgent(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	fake := newFakeSecretService()
	fake.defaultAlias = noObject
	dial := memoryAgentDialer(t, newEd25519(t))

	kr, err := selectLinuxKeyring(fileSvc, fake.dial, dial)
	require.NoError(t, err)
	assert.Equal(t, "ssh-agent", kr.Name())

	ks, err := NewWithKeyringAndFS(testutil.NewTestLogger(), kr, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	assert.NotContains(t, fake.calls, "CreateItem")
}

func TestSelectLinuxKeyring_SealedKeyIsSticky(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	dial := memoryAgentDialer(t, newEd25519(t))
	require.NoError(t, newSSHAgentKeyring(fileSvc, dial).StoreMasterKey(testMasterKey(23)))

	fake := newFakeSecretService()
	kr, err := selectLinuxKeyring(fileSvc, fake.dial, dial)
	require.NoError(t, err)
	assert.Equal(t, "ssh-agent", kr.Name())
	assert.Empty(t, fake.calls, "a sealed ssh-agent key is used without consulting the Secret Service")
}

func TestSelectLinuxKeyring_PrefersWorkingSecretService(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	dial := memoryAgentDialer(t, newEd25519(t))
	kr, err := selectLinuxKeyring(fileSvc, newFakeSecretService().dial, dial)
	require.NoError(t, err)
	assert.Equal(t, "secret-service", kr.Name())
}

func TestSelectLinuxKeyring_NeitherStoreReportsBoth(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	_, err := selectLinuxKeyring(fileSvc, noSecretService, noAgent)
	require.ErrorIs(t, err, constants.ErrKeyStoreSecretServiceDown)
	require.ErrorIs(t, err, constants.ErrKeyStoreSSHAgentUnavailable)
}

func TestSelectLinuxKeyring_SecretServiceFailuresFailClosed(t *testing.T) {
	fileSvc, _ := setupTestFileService(t)
	fake := newFakeSecretService()
	fake.addItem(base64.StdEncoding.EncodeToString(testMasterKey(24)), false)
	fake.addItem(base64.StdEncoding.EncodeToString(testMasterKey(25)), false)
	dial := memoryAgentDialer(t, newEd25519(t))
	_, err := selectLinuxKeyring(fileSvc, fake.dial, dial)
	require.ErrorIs(t, err, constants.ErrKeyStoreDuplicateKey)
}

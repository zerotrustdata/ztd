// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package shared

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestProvisionRecoveryKey_BeforeRuntimeTreeCreation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		accept bool
	}{
		{name: "explicit acceptance stores the key", accept: true},
		{name: "noninteractive refusal leaves the key absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			kr := keystoretest.NewMemoryKeyring()
			var output bytes.Buffer

			err = provisionRecoveryKey(strings.NewReader(""), &output, fileSvc, kr, keystore.RecoveryRoleGateway, tc.accept)
			if tc.accept {
				require.NoError(t, err)
				key, err := kr.RetrieveMasterKey()
				require.NoError(t, err)
				require.Len(t, key, vault.KeySize)
				assert.Contains(t, output.String(), "--accept-unrecoverable")
				output.Reset()
				require.NoError(t, provisionRecoveryKey(strings.NewReader(""), &output, fileSvc, kr, keystore.RecoveryRoleGateway, false))
				assert.Empty(t, output.String(), "an existing key needs no second ceremony")
				retained, err := kr.RetrieveMasterKey()
				require.NoError(t, err)
				assert.Equal(t, key, retained)
			} else {
				require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryRequired)
				_, err := kr.RetrieveMasterKey()
				require.ErrorIs(t, err, constants.ErrKeyStoreKeyNotFound)
			}
			exists, err := fileSvc.FileExists(t.Context(), constants.SecretsDirname)
			require.NoError(t, err)
			assert.False(t, exists, "the ceremony must not depend on a runtime tree")
		})
	}
}

// sealingKeyLostRing is the ssh-agent seal after its sealing key left the agent.
type sealingKeyLostRing struct {
	key  []byte
	lost bool
}

func (s *sealingKeyLostRing) Name() string { return "ssh-agent" }

func (s *sealingKeyLostRing) RetrieveMasterKey() ([]byte, error) {
	if s.lost {
		return nil, constants.ErrKeyStoreSSHAgentKeyMissing
	}
	if s.key == nil {
		return nil, constants.ErrKeyStoreKeyNotFound
	}
	cp := make([]byte, len(s.key))
	copy(cp, s.key)
	return cp, nil
}

func (s *sealingKeyLostRing) StoreMasterKey(key []byte) error {
	cp := make([]byte, len(key))
	copy(cp, key)
	s.key = cp
	s.lost = false
	return nil
}

func (s *sealingKeyLostRing) DeleteMasterKey() error {
	s.key = nil
	s.lost = false
	return nil
}

// recoveryKeyEcho feeds ConfirmRecoveryKey the key it just printed.
type recoveryKeyEcho struct {
	out  *bytes.Buffer
	line []byte
	rest string
}

func (r *recoveryKeyEcho) Read(p []byte) (int, error) {
	if r.rest == "" {
		const marker = "shown once:\n\n    "
		text := r.out.String()
		i := strings.Index(text, marker)
		if i < 0 {
			return 0, io.EOF
		}
		rest := text[i+len(marker):]
		nl := strings.IndexByte(rest, '\n')
		if nl < 0 {
			return 0, io.EOF
		}
		r.line = append([]byte(nil), rest[:nl]...)
		r.rest = rest[:nl+1]
	}
	if r.rest == "" {
		return 0, io.EOF
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func confirmedRecoveryLine(t *testing.T, fileSvc fs.RuntimeFileService, kr keystore.Keyring, role string) []byte {
	t.Helper()
	var shown bytes.Buffer
	echo := &recoveryKeyEcho{out: &shown}
	ks, err := keystore.NewWithFS(fileSvc, testutil.NewTestLogger(), keystore.Options{
		Keyring: kr,
		Ceremony: func(key []byte) error {
			return keystore.ConfirmRecoveryKey(&shown, echo, role, kr.Name(), key, true, false)
		},
	})
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NotEmpty(t, echo.line)
	return append([]byte(nil), echo.line...)
}

func TestRecoverRuntimeKeyAfterSealingKeyLost(t *testing.T) {
	for _, role := range []string{keystore.RecoveryRoleGateway, keystore.RecoveryRoleOperator} {
		t.Run(role, func(t *testing.T) {
			fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			ring := &sealingKeyLostRing{}
			savedLine := confirmedRecoveryLine(t, fileSvc, ring, role)
			t.Cleanup(func() { vault.SecureZero(savedLine) })
			require.NoError(t, mustOpenKeystore(t, fileSvc, ring).EncryptSecret("vault_key", "sealed-value"))
			ring.lost = true

			wrongFile, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
			require.NoError(t, err)
			wrongLine := confirmedRecoveryLine(t, wrongFile, keystoretest.NewMemoryKeyring(), keystore.RecoveryRoleRuntime)
			t.Cleanup(func() { vault.SecureZero(wrongLine) })

			var wrongOut bytes.Buffer
			err = RecoverRuntimeKey(bytes.NewReader(append(append([]byte(nil), wrongLine...), '\n')), &wrongOut, fileSvc, ring, role)
			require.ErrorIs(t, err, constants.ErrKeyStoreRecoveryMismatch)
			assert.True(t, ring.lost, "a wrong key must not re-seal")
			assert.Contains(t, wrongOut.String(), "does not open this runtime")

			var out bytes.Buffer
			err = RecoverRuntimeKey(bytes.NewReader(append(append([]byte(nil), savedLine...), '\n')), &out, fileSvc, ring, role)
			require.NoError(t, err)
			assert.Contains(t, out.String(), "Recovery key installed")
			assert.False(t, ring.lost)
			got, err := mustOpenKeystore(t, fileSvc, ring).DecryptSecret("vault_key")
			require.NoError(t, err)
			assert.Equal(t, "sealed-value", got)
		})
	}
}

func mustOpenKeystore(t *testing.T, fileSvc fs.RuntimeFileService, kr keystore.Keyring) *keystore.Keystore {
	t.Helper()
	ks, err := keystore.NewWithFS(fileSvc, testutil.NewTestLogger(), keystore.Options{Keyring: kr})
	require.NoError(t, err)
	return ks
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
)

func TestIdentityKey_SealedOnDiskAndUnsealsToPEM(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "sealed-app", time.Now().Add(time.Hour))

	require.NoError(t, SealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile(), []byte(keyPEM)))

	raw, err := fileSvc.ReadFile(context.Background(), "cli.key")
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "PRIVATE KEY", "the on-disk key must not be PEM")

	got, err := UnsealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile())
	require.NoError(t, err)
	assert.Equal(t, keyPEM, string(got))
}

func TestIdentityKey_PlaintextPEMFailsClosed(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "plain-app", time.Now().Add(time.Hour))
	require.NoError(t, fileSvc.WriteFile(context.Background(), "cli.key", []byte(keyPEM), constants.PermFilePrivate))

	got, err := UnsealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile())

	require.ErrorIs(t, err, constants.ErrIdentityFilePlaintext)
	assert.Nil(t, got)
}

func TestIdentityKey_WrongMasterKeyFailsClosed(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "sealed-app", time.Now().Add(time.Hour))
	require.NoError(t, SealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile(), []byte(keyPEM)))

	cfg.Keyring = keystoretest.NewMemoryKeyring()
	require.NoError(t, cfg.Keyring.StoreMasterKey(bytes.Repeat([]byte{0x5a}, 32)))
	got, err := UnsealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile())

	require.ErrorIs(t, err, constants.ErrIdentityFileUnseal)
	require.ErrorIs(t, err, constants.ErrInvalidCiphertext)
	assert.Nil(t, got)
}

func TestIdentityKey_MissingMasterKeyFailsClosed(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "sealed-app", time.Now().Add(time.Hour))
	require.NoError(t, SealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile(), []byte(keyPEM)))

	cfg.Keyring = keystoretest.NewMemoryKeyring()
	got, err := UnsealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile())

	require.ErrorIs(t, err, constants.ErrIdentityFileUnseal)
	assert.Nil(t, got)

	_, err = LoadCLICertificate(fileSvc, cfg)
	require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
}

func TestIdentityKey_SealIsBoundToItsPath(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "sealed-app", time.Now().Add(time.Hour))
	require.NoError(t, SealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile(), []byte(keyPEM)))

	sealed, err := fileSvc.ReadFile(context.Background(), "cli.key")
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), "apps/other.key", sealed, constants.PermFilePrivate))

	got, err := UnsealIdentityKey(context.Background(), fileSvc, cfg, cfg.AppKeyFile("other"))

	require.ErrorIs(t, err, constants.ErrIdentityFileUnseal)
	assert.Nil(t, got)
}

func TestLoadCLICertificate_UnsealsKeyForTLS(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeTestCLICert(t, fileSvc, cfg)

	cert, err := LoadCLICertificate(fileSvc, cfg)

	require.NoError(t, err)
	assert.NotEmpty(t, cert.Certificate)
	assert.NotNil(t, cert.PrivateKey)
}

func TestLoadCLICertificate_PlaintextKeyRefused(t *testing.T) {
	fileSvc, cfg := newAuthTestEnv(t)
	writeTestCLICert(t, fileSvc, cfg)
	plain, err := fileSvc.ReadFile(context.Background(), "cli.crt")
	require.NoError(t, err)
	_, keyPEM := generateTestCertificateWithSPIFFE(t, "plain", time.Now().Add(time.Hour))
	require.NoError(t, fileSvc.WriteFile(context.Background(), "cli.key", []byte(keyPEM), constants.PermFilePrivate))
	require.NotEmpty(t, plain)

	_, err = LoadCLICertificate(fileSvc, cfg)

	require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
	require.ErrorIs(t, err, constants.ErrIdentityFilePlaintext)
}

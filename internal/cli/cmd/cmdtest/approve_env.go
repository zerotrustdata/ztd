// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmdtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// GenerateApproveTestCertDER builds a short-lived client certificate for tests.
func GenerateApproveTestCertDER(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, priv.Public(), priv)
	require.NoError(t, err)
	return certBytes
}

// SetupApproveAPITestEnv creates a runtime tree with a key, cert, and credentials.
func SetupApproveAPITestEnv(t *testing.T) (*config.Config, ed25519.PrivateKey, fs.RuntimeFileService) {
	t.Helper()
	fileSvc, cfg := NewCmdTestEnv(t)

	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	WriteSealedKey(t, fileSvc, cfg, cfg.CLIKeyFile(), keyPEM)

	certDER := GenerateApproveTestCertDER(t, priv)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, fileSvc.WriteFile(context.Background(), MustRel(t, fileSvc, cfg.CLICertFile()), certPEM, constants.PermFilePrivate))

	creds := &auth.Credentials{
		OperatorSessionID: "op-sess-test",
		UserID:            "user-test",
		OperatorID:        "op-test",
		CLISessionID:      "cli-sess-test",
	}
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, creds))

	return cfg, priv, fileSvc
}

// SetupApproveSSETestEnv extends SetupApproveAPITestEnv with a trust bundle so
// auth.BuildMTLSClient can succeed.
func SetupApproveSSETestEnv(t *testing.T) (*config.Config, ed25519.PrivateKey, fs.RuntimeFileService) {
	t.Helper()
	cfg, priv, fileSvc := SetupApproveAPITestEnv(t)

	certPEM, err := fileSvc.ReadFile(context.Background(), MustRel(t, fileSvc, cfg.CLICertFile()))
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), certPEM, constants.PermFilePrivate))

	return cfg, priv, fileSvc
}

// WithEndpointOverride sets the config endpoint override for the rest of t.
func WithEndpointOverride(t *testing.T, url string) {
	t.Helper()
	config.SetEndpointOverride(url)
	t.Cleanup(func() { config.SetEndpointOverride("") })
}

// WriteCLIIdentity writes a complete CLI identity: a self-signed client
// certificate and its key sealed under cfg's master key, as enrollment leaves
// them.
func WriteCLIIdentity(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	WriteSealedKey(t, fileSvc, cfg, cfg.CLIKeyFile(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: GenerateApproveTestCertDER(t, priv)})
	require.NoError(t, fileSvc.WriteFile(context.Background(), MustRel(t, fileSvc, cfg.CLICertFile()), certPEM, constants.PermFilePrivate))
}

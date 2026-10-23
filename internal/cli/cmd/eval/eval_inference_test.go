// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	inferdispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// mustGenerateCA creates a self-signed CA certificate and key, returned as PEM.
func mustGenerateCA(t *testing.T, commonName string) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return key, cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// mustGenerateClientLeaf issues a client-auth leaf certificate signed by the
// given CA, returned as PEM cert and PEM key.
func mustGenerateClientLeaf(t *testing.T, caKey *ecdsa.PrivateKey, caCert *x509.Certificate, appName string, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	uri, err := url.Parse("spiffe://ztd.local/app/" + appName)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: fmtAppCN(appName)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func fmtAppCN(appName string) string {
	return "ztd-app-" + appName
}

func newEvalTestFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	svc, err := fs.NewRuntimeFileService(testutil.TempDir(t), slog.Default())
	require.NoError(t, err)
	require.NoError(t, svc.CreateRuntimeTree(context.Background()))
	return svc
}

func evalInferenceCredTestConfig(t *testing.T, base string, trustBundlePEM []byte) *config.Config {
	t.Helper()
	appCertDir := filepath.Join(base, "pki", "issued", "apps")
	require.NoError(t, os.MkdirAll(appCertDir, constants.PermDirPrivate))
	cfg := &config.Config{
		RuntimeDir: base,
		Paths:      &config.PathsConfig{},
		Keyring:    keystoretest.NewMemoryKeyring(),
	}
	cfg.Paths.Infra.AppCertDir = appCertDir
	trustDir := filepath.Join(base, "pki", "trust")
	require.NoError(t, os.MkdirAll(trustDir, constants.PermDirPrivate))
	require.NoError(t, os.WriteFile(filepath.Join(trustDir, constants.PkiFileGatewayBundle), trustBundlePEM, constants.PermFilePrivate))
	return cfg
}

func TestResolveInferenceProbeAppCredentials_NoCandidateReturnsMissingError(t *testing.T) {
	fileSvc := newEvalTestFileSvc(t)
	base := fileSvc.Resolve("")
	_, _, caBundle := mustGenerateCA(t, "current ztd Root CA")
	cfg := evalInferenceCredTestConfig(t, base, caBundle)

	_, err := resolveInferenceProbeAppCredentials(fileSvc, cfg)

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAppIdentityNotFound)
	assert.Contains(t, err.Error(), "ztd auth enroll app ztd-eval")

	// Verify it never touches or expects ztde paths.
	assert.NoFileExists(t, cfg.AppCertFile("ztde"))
	assert.NoFileExists(t, filepath.Join(cfg.Paths.Infra.AppCertDir, "ztde.crt"))
}

func TestResolveInferenceProbeAppCredentials_CurrentAppCertAccepted(t *testing.T) {
	fileSvc := newEvalTestFileSvc(t)
	base := fileSvc.Resolve("")
	caKey, caCert, caBundle := mustGenerateCA(t, "current ztd Root CA")
	cfg := evalInferenceCredTestConfig(t, base, caBundle)

	certPEM, keyPEM := mustGenerateClientLeaf(t, caKey, caCert, "ztd-eval", time.Now().Add(7*24*time.Hour))
	wantCert := cfg.AppCertFile("ztd-eval")
	wantKey := cfg.AppKeyFile("ztd-eval")

	certRel, err := fileSvc.RelFromAbs(wantCert)
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, certPEM, constants.PermFilePrivate))
	require.NoError(t, auth.SealIdentityKey(context.Background(), fileSvc, cfg, wantKey, keyPEM))

	gotCert, err := resolveInferenceProbeAppCredentials(fileSvc, cfg)

	require.NoError(t, err)
	require.NotNil(t, gotCert)
	assert.NotEmpty(t, gotCert.Certificate)
}

func TestResolveInferenceProbeAppCredentials_StaleAppCertRejected(t *testing.T) {
	fileSvc := newEvalTestFileSvc(t)
	base := fileSvc.Resolve("")
	_, _, currentCABundle := mustGenerateCA(t, "current ztd Root CA")
	cfg := evalInferenceCredTestConfig(t, base, currentCABundle)

	// Signed by a prior gateway CA generation.
	staleCAKey, staleCACert, _ := mustGenerateCA(t, "prior ztd Root CA")
	staleCertPEM, staleKeyPEM := mustGenerateClientLeaf(t, staleCAKey, staleCACert, "ztd-eval", time.Now().Add(7*24*time.Hour))

	certRel, err := fileSvc.RelFromAbs(cfg.AppCertFile("ztd-eval"))
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, staleCertPEM, constants.PermFilePrivate))
	require.NoError(t, auth.SealIdentityKey(context.Background(), fileSvc, cfg, cfg.AppKeyFile("ztd-eval"), staleKeyPEM))

	_, err = resolveInferenceProbeAppCredentials(fileSvc, cfg)

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAppIdentityUntrusted)
	assert.Contains(t, err.Error(), "ztd auth enroll app ztd-eval")
}

func TestResolveInferenceProbeAppCredentials_ExpiredAppCertRejected(t *testing.T) {
	fileSvc := newEvalTestFileSvc(t)
	base := fileSvc.Resolve("")
	caKey, caCert, caBundle := mustGenerateCA(t, "current ztd Root CA")
	cfg := evalInferenceCredTestConfig(t, base, caBundle)

	// Expired 1 hour ago.
	expiredCertPEM, expiredKeyPEM := mustGenerateClientLeaf(t, caKey, caCert, "ztd-eval", time.Now().Add(-time.Hour))

	certRel, err := fileSvc.RelFromAbs(cfg.AppCertFile("ztd-eval"))
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, expiredCertPEM, constants.PermFilePrivate))
	require.NoError(t, auth.SealIdentityKey(context.Background(), fileSvc, cfg, cfg.AppKeyFile("ztd-eval"), expiredKeyPEM))

	_, err = resolveInferenceProbeAppCredentials(fileSvc, cfg)

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrAppIdentityExpired)
	assert.Contains(t, err.Error(), "ztd auth enroll app ztd-eval")
}

func TestGatesInference_RequiresModel(t *testing.T) {
	cmd := gatesInferenceEvalCmd(nativeEvalDeps{})
	cmd.SetArgs(nil)

	err := cmd.ExecuteContext(context.Background())

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationModelRequired)
}

func TestGatesProbe_TakesModelOnlyAsPositionalArgument(t *testing.T) {
	cmd := gatesProbeEvalCmd(nativeEvalDeps{})

	assert.Nil(t, cmd.Flags().Lookup("model"), "the deprecated --model flag must not exist")
	assert.NotNil(t, cmd.Flags().Lookup("operator-session-id"), "the --operator-session-id flag must exist")
	assert.Error(t, cmd.Args(cmd, nil), "probe requires the positional <model>")
	assert.NoError(t, cmd.Args(cmd, []string{"qwen3:4b"}))
}

func TestGovernedInferenceDispatchDeadline_CoversApprovalAndProviderRequest(t *testing.T) {
	assert.Equal(t, mcp.ApprovalRequestTTL+inferdispatch.RequestDeadline, governedInferenceDispatchDeadline)
	assert.Greater(t, governedInferenceDispatchDeadline, mcp.ApprovalRequestTTL+inference.ProviderRequestTimeout,
		"a probe must outlast the human approval wait plus the provider request")
}

func TestParseInferenceAcceptanceCases_RejectsAllBlankSelection(t *testing.T) {
	_, err := parseInferenceAcceptanceCases(" , ,")

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationNoCasesSelected)
}

func TestResolveInferenceProbeAppCredentials_NilParameters(t *testing.T) {
	_, err := resolveInferenceProbeAppCredentials(nil, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInternal)
}

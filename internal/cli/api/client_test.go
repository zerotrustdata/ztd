// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
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
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func generateTestCert(t *testing.T) (certPEM, keyPEM []byte, privKey *ecdsa.PrivateKey) {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test-client"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyBytes, err := x509.MarshalECPrivateKey(privKey)
	require.NoError(t, err)
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM, privKey
}

func generateTestCA(t *testing.T) (caCertPEM []byte) {
	t.Helper()
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ztd Test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privKey.PublicKey, privKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func setupTestConfig(t *testing.T) (*config.Config, fs.RuntimeFileService) {
	t.Helper()

	tempDir := testutil.TempDir(t)

	absTempDir, err := filepath.Abs(tempDir)
	require.NoError(t, err)

	projectRoot := filepath.Join(absTempDir, "project")
	require.NoError(t, os.MkdirAll(projectRoot, constants.PermDirStandard))

	fileSvc, err := fs.NewRuntimeFileService(projectRoot, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	caCertPEM := generateTestCA(t)

	cfg, err := config.Load(projectRoot)
	require.NoError(t, err)
	cfg.Keyring = keystoretest.NewMemoryKeyring()

	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), caCertPEM, constants.PermFilePublic))

	return cfg, fileSvc
}

func setupTestCredentials(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()

	creds := &auth.Credentials{
		OperatorSessionID: "test-operator-session-id",
		UserID:            "test-user-id",
		OperatorID:        "test-operator-id",
		CLISessionID:      "test-cli-session-id",
	}

	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, creds))

	certPEM, keyPEM, _ := generateTestCert(t)
	certRel, err := fileSvc.RelFromAbs(cfg.CLICertFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(context.Background(), certRel, certPEM, constants.PermFilePrivate))
	require.NoError(t, auth.SealIdentityKey(context.Background(), fileSvc, cfg, cfg.CLIKeyFile(), keyPEM))
}

func setupTLSClient(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config, server *httptest.Server) *Client {
	t.Helper()

	client, err := NewClientWithURL(fileSvc, cfg, server.URL)
	require.NoError(t, err)

	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(server.Certificate())

	transport := client.httpClient.Transport.(*http.Transport)
	transport.TLSClientConfig.RootCAs = caCertPool

	return client
}

func TestNewClient_Success(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	client, err := NewClient(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, client)

	assert.NotNil(t, client.httpClient)
	assert.Equal(t, cfg, client.cfg)
	assert.NotNil(t, client.creds)
	assert.Equal(t, 5*time.Second, client.httpClient.Timeout)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS13), transport.TLSClientConfig.MinVersion)
	assert.Len(t, transport.TLSClientConfig.Certificates, 1)
	assert.NotNil(t, transport.TLSClientConfig.RootCAs)
}

func TestNewClient_NoCredentials(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)

	credsDir := cfg.RuntimeDir
	os.RemoveAll(credsDir)

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrNotAuthenticated)
}

func TestNewClient_LoadCredentialsError(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)

	credsDir := cfg.RuntimeDir
	require.NoError(t, os.MkdirAll(credsDir, constants.PermDirPrivate))

	credsFile := cfg.CredentialsFile()
	require.NoError(t, os.WriteFile(credsFile, []byte("invalid json"), constants.PermFilePrivate))

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrFailedToLoadCredentials)
}

func TestNewClient_MissingCertFile(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	require.NoError(t, os.Remove(cfg.CLICertFile()))

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
}

func TestNewClient_MissingKeyFile(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	require.NoError(t, os.Remove(cfg.CLIKeyFile()))

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrFailedToLoadClientCertificate)
}

func TestNewClient_MissingTrustBundle(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	caRel := cfg.DefaultTrustBundleRelPath()
	require.NoError(t, fileSvc.Remove(context.Background(), caRel))

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrFailedToReadTrustBundle)
}

func TestNewClient_InvalidTrustBundle(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	caRel := cfg.DefaultTrustBundleRelPath()
	require.NoError(t, fileSvc.WriteFile(context.Background(), caRel, []byte("not a valid PEM"), constants.PermFilePublic))

	client, err := NewClient(fileSvc, cfg)
	require.Error(t, err)
	assert.Nil(t, client)
	require.ErrorIs(t, err, constants.ErrFailedToParseTrustBundle)
}

func TestDoRequest_MarshalError(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	client, err := NewClient(fileSvc, cfg)
	require.NoError(t, err)

	body := make(chan int)
	_, err = client.DoRequest("POST", "https://localhost:9999/api/test", body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal request body")
}

func TestNewClient_TLSConfig(t *testing.T) {
	cfg, fileSvc := setupTestConfig(t)
	setupTestCredentials(t, fileSvc, cfg)

	client, err := NewClient(fileSvc, cfg)
	require.NoError(t, err)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	require.True(t, ok)

	tlsConfig := transport.TLSClientConfig
	require.NotNil(t, tlsConfig)

	assert.Equal(t, uint16(tls.VersionTLS13), tlsConfig.MinVersion)
	assert.Len(t, tlsConfig.Certificates, 1)
	assert.NotNil(t, tlsConfig.RootCAs)
	assert.False(t, tlsConfig.InsecureSkipVerify)
}

func TestStatusError(t *testing.T) {
	err := fmt.Errorf("list approvals: %w", &StatusError{StatusCode: http.StatusUnauthorized, Body: "expired"})
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.True(t, IsUnauthorized(err))
	assert.Equal(t, "list approvals: HTTP status error: status 401: expired", err.Error())

	forbidden := &StatusError{StatusCode: http.StatusForbidden}
	require.ErrorIs(t, forbidden, constants.ErrHTTPStatusError)
	assert.False(t, IsUnauthorized(forbidden))
	assert.False(t, IsUnauthorized(constants.ErrHTTPStatusError))
}

func TestDecodeGatewayError_PreservesRecoveryErrorIdentity(t *testing.T) {
	for _, sentinel := range []error{
		constants.ErrOperatorBindingMismatch,
		constants.ErrPublicFeedSequenceOutOfOrder,
		constants.ErrPublicFeedSnapshotNotFound,
		constants.ErrPublicFeedOutboxEquivocation,
		constants.ErrPublicFeedOutboxCorrupt,
		constants.ErrPublicFeedHashChainMismatch,
		constants.ErrCLISessionExpired,
		constants.ErrCLISessionInvalid,
		constants.ErrMTLSCertRevoked,
	} {
		t.Run(sentinel.Error(), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"error":%q}`, sentinel.Error()+": details"))
			err := fmt.Errorf("gateway operation: %w", DecodeGatewayError(http.StatusConflict, body))
			require.ErrorIs(t, err, constants.ErrHTTPStatusError)
			require.ErrorIs(t, err, sentinel)
			assert.NotErrorIs(t, DecodeGatewayError(http.StatusConflict, []byte(fmt.Sprintf(`{"error":%q}`, "unrelated: "+sentinel.Error()))), sentinel)
		})
	}
	require.ErrorIs(t, DecodeGatewayError(http.StatusNotFound, nil), constants.ErrNotFound)
	assert.NotErrorIs(t, DecodeGatewayError(http.StatusServiceUnavailable, []byte(`{"error":"status 404"}`)), constants.ErrNotFound)
}

func TestDecodeGatewayError_PlatformEnrollmentRequiresBootstrap(t *testing.T) {
	body := []byte(fmt.Sprintf(`{"error":%q}`, constants.ErrPlatformEnrollmentRequiresBootstrap.Error()))
	err := DecodeGatewayError(http.StatusForbidden, body)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentRequiresBootstrap)

	statusErr := &StatusError{StatusCode: http.StatusForbidden, Body: string(body)}
	assert.ErrorIs(t, statusErr, constants.ErrPlatformEnrollmentRequiresBootstrap)
}

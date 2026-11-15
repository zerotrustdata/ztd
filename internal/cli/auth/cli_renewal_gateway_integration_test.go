// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package auth_test

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cliauth "github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/pkg/certutil"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestCLIRenewal_CLICommitsAndExistingClientReloadsAgainstGateway(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: "cli-renewal-client", Posture: constants.PostureDoctrine})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-renewal-client")
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(testutil.Context(t)))
	cfg := &config.Config{RuntimeDir: fileSvc.Resolve(""), Keyring: keystoretest.NewMemoryKeyring()}
	identity.InstallCLIRuntime(t, f, fileSvc, cfg)

	// Use the real Gateway handler and PKI on a recording HTTPS listener so
	// assertions observe the certificate presented on an actual handshake.
	seen := make(chan string, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		seen <- hex.EncodeToString(fp[:])
		f.Service.GetHTTPHandler().ServeHTTP(w, r)
	}))
	server.TLS = f.Service.GetPKI().TLSConfig()
	servingCert, err := server.TLS.GetCertificate(nil)
	require.NoError(t, err)
	server.TLS.Certificates = []tls.Certificate{*servingCert}
	server.StartTLS()
	t.Cleanup(server.Close)
	config.SetHTTPSEndpointOverride(server.URL)
	t.Cleanup(func() { config.SetHTTPSEndpointOverride("") })

	client, err := cliauth.BuildMTLSClient(fileSvc, cfg, 10*time.Second)
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	block, _ := pem.Decode([]byte(identity.CLICertificate))
	require.NotNil(t, block)
	before, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	now := func() time.Time { return certutil.RenewalTime(before) }
	renewer := cliauth.NewEnrollmentClient(cfg, nil)

	// A held lock suppresses the whole request, even when the leaf is due.
	lockRel, err := fileSvc.RelFromAbs(cfg.CLIIdentityLockFile())
	require.NoError(t, err)
	lock, err := fileSvc.TryLock(testutil.Context(t), lockRel, constants.PermFilePrivate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	renewed, err := cliauth.RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, renewer, cliauth.FileKeyProvider{}, now)
	require.NoError(t, err)
	require.False(t, renewed)
	require.Empty(t, seen)
	require.NoError(t, lock.Close())

	renewed, err = cliauth.RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, renewer, cliauth.FileKeyProvider{}, now)
	require.NoError(t, err)
	require.True(t, renewed)
	oldFP := sha256.Sum256(before.Raw)
	require.Equal(t, hex.EncodeToString(oldFP[:]), <-seen)
	after, err := cliauth.LoadCLICertificate(fileSvc, cfg)
	require.NoError(t, err)
	require.NotEqual(t, before.SerialNumber, after.Leaf.SerialNumber)
	creds, err := cliauth.LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	require.Equal(t, identity.CLISessionID, creds.CLISessionID)
	require.Equal(t, identity.OperatorSessionID, creds.OperatorSessionID)
	require.Equal(t, identity.OperatorID, creds.OperatorID)

	sessionJSON, err := f.Service.GetDocStore().DocGet(testutil.Context(t), string(constants.CollectionCLISessions), identity.CLISessionID)
	require.NoError(t, err)
	var session models.CLISession
	require.NoError(t, sessionJSON.DecodeWire(&session))
	require.Equal(t, hex.EncodeToString(oldFP[:]), session.PreviousCertFingerprint)
	newFP := sha256.Sum256(after.Leaf.Raw)
	require.Equal(t, hex.EncodeToString(newFP[:]), session.CertFingerprint)

	renewed, err = cliauth.RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, renewer, cliauth.FileKeyProvider{}, time.Now)
	require.NoError(t, err)
	require.False(t, renewed)
	require.Empty(t, seen)

	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, server.URL+constants.APIPaths.AuthCLISession, nil)
	require.NoError(t, err)
	req.Header.Set(constants.HeaderCLISessionID, identity.CLISessionID)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, hex.EncodeToString(newFP[:]), <-seen, "client constructed before renewal presents the new certificate")
}

func TestCLIReinstatement_ReissuesAnExpiredCertificateWithoutMTLSAgainstGateway(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{TestName: "cli-reinstate-client", Posture: constants.PostureDoctrine})
	f.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, f, "cli-reinstate-client")
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(testutil.Context(t)))
	cfg := &config.Config{RuntimeDir: fileSvc.Resolve(""), Keyring: keystoretest.NewMemoryKeyring()}
	identity.InstallCLIRuntime(t, f, fileSvc, cfg)

	// Record whether each request presented a client certificate: an
	// expired certificate cannot complete the handshake, so reinstatement
	// must arrive without one.
	clientCerts := make(chan int, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientCerts <- len(r.TLS.PeerCertificates)
		f.Service.GetHTTPHandler().ServeHTTP(w, r)
	}))
	server.TLS = f.Service.GetPKI().TLSConfig()
	server.TLS.ClientAuth = tls.VerifyClientCertIfGiven
	servingCert, err := server.TLS.GetCertificate(nil)
	require.NoError(t, err)
	server.TLS.Certificates = []tls.Certificate{*servingCert}
	server.StartTLS()
	t.Cleanup(server.Close)
	config.SetHTTPSEndpointOverride(server.URL)
	t.Cleanup(func() { config.SetHTTPSEndpointOverride("") })

	block, _ := pem.Decode([]byte(identity.CLICertificate))
	require.NotNil(t, block)
	before, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	// The CLI decides from its clock that the certificate has expired.
	expiredClock := func() time.Time { return before.NotAfter.Add(time.Hour) }

	reinstated, err := cliauth.RenewCLIIdentityIfDue(testutil.Context(t), fileSvc, cfg, cliauth.NewEnrollmentClient(cfg, nil), cliauth.FileKeyProvider{}, expiredClock)
	require.NoError(t, err)
	require.True(t, reinstated)
	require.Equal(t, 0, <-clientCerts, "reinstatement presents no client certificate")

	after, err := cliauth.LoadCLICertificate(fileSvc, cfg)
	require.NoError(t, err)
	require.NotEqual(t, before.SerialNumber, after.Leaf.SerialNumber)
	creds, err := cliauth.LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	require.Equal(t, identity.CLISessionID, creds.CLISessionID, "the CLI session is kept")
	require.Equal(t, identity.OperatorSessionID, creds.OperatorSessionID)
	require.Equal(t, identity.OperatorID, creds.OperatorID)

	sessionJSON, err := f.Service.GetDocStore().DocGet(testutil.Context(t), string(constants.CollectionCLISessions), identity.CLISessionID)
	require.NoError(t, err)
	var session models.CLISession
	require.NoError(t, sessionJSON.DecodeWire(&session))
	newFP := sha256.Sum256(after.Leaf.Raw)
	require.Equal(t, hex.EncodeToString(newFP[:]), session.CertFingerprint)

	// The reinstated certificate authenticates over mTLS.
	client, err := cliauth.BuildMTLSClient(fileSvc, cfg, 10*time.Second)
	require.NoError(t, err)
	t.Cleanup(client.CloseIdleConnections)
	req, err := http.NewRequestWithContext(testutil.Context(t), http.MethodGet, server.URL+constants.APIPaths.AuthCLISession, nil)
	require.NoError(t, err)
	req.Header.Set(constants.HeaderCLISessionID, identity.CLISessionID)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotZero(t, <-clientCerts, "the reinstated certificate is presented over mTLS")
}

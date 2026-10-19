// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package fixtures

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

// BootstrapCLIIdentity enrolls the first human through the real bootstrap
// endpoint. Its CLI session binds to the Gateway's certless embedded Operator,
// which owns Gateway-local MCP/A2A execution and its receipts.
func BootstrapCLIIdentity(t *testing.T, f *GatewayFixture, systemFingerprint string) *ClientIdentity {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "fixture-cli"},
	}, priv)
	require.NoError(t, err)
	body, err := json.Marshal(models.BootstrapRequest{
		CLICSR:            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})),
		SystemFingerprint: systemFingerprint,
	})
	require.NoError(t, err)
	client := CreateNoCertClient(t, f)
	t.Cleanup(client.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.AuthBootstrap, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, resp.StatusCode, "CLI bootstrap: %s", responseBody)
	var bootstrap models.BootstrapResponse
	require.NoError(t, json.Unmarshal(responseBody, &bootstrap))
	require.True(t, bootstrap.Success)
	require.Equal(t, string(constants.DocIDEmbeddedOperator), bootstrap.OperatorID)
	require.NotEmpty(t, bootstrap.OperatorSessionID)
	require.NotEmpty(t, bootstrap.CLISessionID)
	require.NotEqual(t, bootstrap.OperatorSessionID, bootstrap.CLISessionID)

	// Supply the real software passkey used by the fixture's L3 ceremonies.
	require.NotNil(t, bootstrap.User)
	passkey := testutil.NewSoftwarePasskey(t)
	bootstrap.User.WebAuthnUserID, err = uuid.NewString()
	require.NoError(t, err)
	bootstrap.User.PasskeyCredentials = []models.PasskeyCredential{passkey.Credential(t)}
	userBytes, err := json.Marshal(bootstrap.User)
	require.NoError(t, err)
	require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), string(constants.CollectionUsers), bootstrap.UserID, userBytes))
	privBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	return &ClientIdentity{
		UserID:            bootstrap.UserID,
		OrganizationID:    bootstrap.UserID,
		CLIPrivateKey:     privBytes,
		CLICertificate:    bootstrap.CLICert,
		CLISessionID:      bootstrap.CLISessionID,
		OperatorID:        bootstrap.OperatorID,
		OperatorSessionID: bootstrap.OperatorSessionID,
		Passkey:           passkey,
	}
}

// InstallCLIRuntime writes this identity's CLI credentials into a local runtime
// and points CLI endpoint discovery at the fixture Gateway.
func (identity *ClientIdentity) InstallCLIRuntime(t *testing.T, f *GatewayFixture, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()
	require.NotNil(t, identity)
	require.NotNil(t, f)
	require.NotNil(t, fileSvc)
	require.NotNil(t, cfg)

	certPath, err := fileSvc.RelFromAbs(cfg.CLICertFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(t.Context(), certPath, []byte(identity.CLICertificate), constants.PermFilePrivate))

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.CLIPrivateKey})
	require.NoError(t, auth.SealIdentityKey(t.Context(), fileSvc, cfg, cfg.CLIKeyFile(), keyPEM))

	trust := append(append([]byte{}, testutil.ReadRootCA(t, f.PKIDir)...), testutil.ReadOperatorCA(t, f.PKIDir)...)
	require.NoError(t, auth.WriteTrustBundleFS(fileSvc, cfg, trust, constants.PermFilePublic))
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, &auth.Credentials{
		OperatorSessionID: identity.OperatorSessionID,
		OperatorID:        identity.OperatorID,
		CLISessionID:      identity.CLISessionID,
		UserID:            identity.UserID,
	}))

	config.SetHTTPEndpointOverride(network.LocalhostHTTPURL(f.Service.GetHTTPPort()))
	config.SetHTTPSEndpointOverride(network.LocalhostHTTPSURL(f.Service.GetHTTPSPort()))
	t.Cleanup(func() { config.SetEndpointOverride("") })
}

// RotateCLICertificate replaces the identity's CLI certificate through the
// Gateway's rotation endpoint, which deactivates the identity's CLI session and
// issues a replacement certificate naming the new session in its URI SAN. The
// identity's CLISessionID must be the active session the rotation runs over.
// A CLI approval is accepted only when the session its certificate names is the
// session the approval arrives on, so a certificate that predates a bind cannot
// approve over the session the bind issued.
func (identity *ClientIdentity) RotateCLICertificate(t *testing.T, f *GatewayFixture) {
	t.Helper()
	require.NotNil(t, identity)
	require.NotNil(t, f)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "fixture-cli"},
	}, priv)
	require.NoError(t, err)
	body, err := json.Marshal(models.CLIRotationRequest{
		CLICSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})),
	})
	require.NoError(t, err)

	client := CreateCLIMTLSClient(t, f, identity)
	t.Cleanup(client.CloseIdleConnections)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())+constants.APIPaths.AuthCLIRotate, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", constants.HeaderValueApplicationJSON)
	resp, err := client.Do(req)
	require.NoError(t, err)
	responseBody, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	require.NoError(t, readErr)
	require.NoError(t, closeErr)
	require.Equalf(t, http.StatusCreated, resp.StatusCode, "CLI rotation: %s", responseBody)

	var rotated models.CLIRotationResponse
	require.NoError(t, json.Unmarshal(responseBody, &rotated))
	require.True(t, rotated.Success)
	require.NotEmpty(t, rotated.CLISessionID)
	privBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	identity.CLICertificate = rotated.CLICert
	identity.CLIPrivateKey = privBytes
	identity.CLISessionID = rotated.CLISessionID
}

// InstallRotatedCLICertificate writes the certificate and key that
// RotateCLICertificate issued into the local runtime, and points the stored CLI
// credentials at the rotated session. The stored operator bindings are left as
// the bind wrote them.
func (identity *ClientIdentity) InstallRotatedCLICertificate(t *testing.T, fileSvc fs.RuntimeFileService, cfg *config.Config) {
	t.Helper()
	require.NotNil(t, identity)
	require.NotNil(t, fileSvc)
	require.NotNil(t, cfg)

	certPath, err := fileSvc.RelFromAbs(cfg.CLICertFile())
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(t.Context(), certPath, []byte(identity.CLICertificate), constants.PermFilePrivate))

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.CLIPrivateKey})
	require.NoError(t, auth.SealIdentityKey(t.Context(), fileSvc, cfg, cfg.CLIKeyFile(), keyPEM))

	creds, err := auth.LoadCredentials(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, creds)
	creds.CLISessionID = identity.CLISessionID
	require.NoError(t, auth.SaveCredentials(fileSvc, cfg, creds))
}

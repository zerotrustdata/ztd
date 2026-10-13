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

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
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

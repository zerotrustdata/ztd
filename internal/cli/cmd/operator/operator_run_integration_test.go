// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package operatorcmd

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
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestOperatorRun_ExecutesThroughGovernedPipeline drives the real `operator run`
// cobra command against a real doctrine Gateway. The first-user bootstrap
// claims the Gateway's embedded (Data) Operator and mints its operator
// session, so the command targets the Gateway-local substrate through the
// production dispatch path (L1-L5), with no mocked client.
func TestOperatorRun_ExecutesThroughGovernedPipeline(t *testing.T) {
	ctx := t.Context()
	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: t.Name(),
		Posture:  constants.PostureDoctrine,
	})
	fixture.WaitForReady(t)

	// Bootstrap the first user over the loopback HTTP listener. This claims the
	// embedded Operator and returns a CLI certificate bound to its session.
	cliKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cliCSR, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "operator-run-it-cli"}}, cliKey)
	require.NoError(t, err)
	bootstrapBody, err := json.Marshal(models.BootstrapRequest{
		CLICSR:            string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: cliCSR})),
		SystemFingerprint: "operator-run-it-host",
	})
	require.NoError(t, err)
	bootstrapReq, err := http.NewRequestWithContext(ctx, http.MethodPost, network.LocalhostHTTPURL(fixture.Service.GetHTTPPort())+constants.APIPaths.AuthBootstrap, bytes.NewReader(bootstrapBody))
	require.NoError(t, err)
	bootstrapResp, err := http.DefaultClient.Do(bootstrapReq)
	require.NoError(t, err)
	bootstrapWire, err := io.ReadAll(bootstrapResp.Body)
	require.NoError(t, bootstrapResp.Body.Close())
	require.NoError(t, err)
	require.Equalf(t, http.StatusCreated, bootstrapResp.StatusCode, "bootstrap body: %s", bootstrapWire)
	var bootstrap models.BootstrapResponse
	require.NoError(t, json.Unmarshal(bootstrapWire, &bootstrap))
	require.True(t, bootstrap.Success)
	require.NotEmpty(t, bootstrap.CLICert)
	require.NotEmpty(t, bootstrap.OperatorSessionID)
	require.Equal(t, string(constants.DocIDEmbeddedOperator), bootstrap.OperatorID)

	// Materialize the CLI runtime: cert, key, trust bundle (root + operator CA),
	// and saved credentials, exactly as `auth enroll` leaves them.
	fileSvc, cliCfg := cmdtest.NewCmdTestEnv(t)
	cliKeyDER, err := x509.MarshalECPrivateKey(cliKey)
	require.NoError(t, err)
	require.NoError(t, fileSvc.WriteFile(ctx, cmdtest.MustRel(t, fileSvc, cliCfg.CLICertFile()), []byte(bootstrap.CLICert), constants.PermFilePrivate))
	cmdtest.WriteSealedKey(t, fileSvc, cliCfg, cliCfg.CLIKeyFile(), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: cliKeyDER}))
	trust := append(append([]byte{}, testutil.ReadRootCA(t, fixture.PKIDir)...), testutil.ReadOperatorCA(t, fixture.PKIDir)...)
	require.NoError(t, auth.WriteTrustBundleFS(fileSvc, cliCfg, trust, constants.PermFilePublic))
	require.NoError(t, auth.SaveCredentials(fileSvc, cliCfg, &auth.Credentials{
		OperatorSessionID: bootstrap.OperatorSessionID,
		OperatorID:        bootstrap.OperatorID,
		CLISessionID:      bootstrap.CLISessionID,
		UserID:            bootstrap.UserID,
	}))
	cmdtest.WithEndpointOverride(t, network.LocalhostHTTPSURL(fixture.Service.GetHTTPSPort()))

	// Audit client over the same production CLI transport the command uses.
	auditClient, err := defaultOperatorRunClientFactory(fileSvc, cliCfg, api.ClientOptions{})
	require.NoError(t, err)
	before := countExecutedBashReceipts(t, auditClient, bootstrap.OperatorSessionID)

	cmd := operatorRunCmdWithConfig(cmdtest.ConfigLoaderFor(cliCfg), defaultOperatorRunClientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{bootstrap.OperatorSessionID, "--cmd", "echo ztd-operator-run-it"})
	require.NoError(t, cmd.ExecuteContext(ctx), "operator run output: %s", out.String())

	assert.Contains(t, out.String(), "ztd-operator-run-it")
	assert.Contains(t, out.String(), bootstrap.OperatorSessionID)

	after := countExecutedBashReceipts(t, auditClient, bootstrap.OperatorSessionID)
	assert.Greater(t, after, before, "governed EXECUTE_BASH must append a COMPLETED action receipt for the embedded operator session")
}

// countExecutedBashReceipts counts COMPLETED EXECUTE_BASH receipts recorded for
// operatorSessionID through the Gateway audit receipts endpoint.
func countExecutedBashReceipts(t *testing.T, client authcmd.APIClient, operatorSessionID string) int {
	t.Helper()
	raw, err := client.Get(constants.APIPaths.AuditReceipts + "?operator_session_id=" + url.QueryEscape(operatorSessionID) + "&limit=500")
	require.NoError(t, err)
	var resp models.AuditReceiptsResponse
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.True(t, resp.Success, "audit receipts response must report success")
	count := 0
	for _, receipt := range resp.Receipts {
		if receipt.ActionType == constants.ActionTypeExecuteBash && receipt.Status == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
			count++
		}
	}
	return count
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	cliauth "github.com/zerotrustdata/ztd/internal/cli/auth"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/protocol"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// A fully enrolled app carries its approving owner's user SAN for attribution.
// That delegation must never authenticate the app as the owner on admin routes,
// including when the active posture does not require L3 for governed actions.
func TestDelegatedAppCannotMutateOwnerAuthority(t *testing.T) {
	for _, posture := range constants.AllPostures {
		t.Run(string(posture), func(t *testing.T) {
			f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
				TestName: "delegated-app-admin", Posture: posture,
			})
			f.WaitForReady(t)
			owner := fixtures.BootstrapCLIIdentity(t, f, "delegated-app-owner")
			const appName = "delegated-admin-regression"
			client := enrollDelegatedApp(t, f, owner, appName)
			baseURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())
			appURI, err := protocol.NewWorkloadIdentity().AppSPIFFEURL(appName)
			require.NoError(t, err)
			t.Run("replace-signer", func(t *testing.T) {
				before, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), appName)
				require.NoError(t, err)
				require.NotEmpty(t, before)
				foreignKey, _, err := ed25519.GenerateKey(nil)
				require.NoError(t, err)
				body, err := json.Marshal(struct {
					PublicKey string `json:"public_key_hex"`
				}{PublicKey: hex.EncodeToString(foreignKey)})
				require.NoError(t, err)
				path := constants.APIPaths.AdminAppPoliciesPrefix + url.PathEscape(appURI.String()) + "/signers"
				status, response := requestDataAPI(t, client, http.MethodPost, baseURL+path, string(body))
				assert.Equal(t, http.StatusForbidden, status, "%s", response)
				assert.Contains(t, string(response), constants.ErrPrivilegedEndpointAccess.Error())
				after, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), appName)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				// The admin handler uses the policy ID as its signer ID. It must
				// not create a second signer under the full SPIFFE URI either.
				alias, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), appURI.String())
				require.NoError(t, err)
				assert.Empty(t, alias)
			})
			// Exercise both actual enrollment records: the signing key uses the
			// short app ID and the policy uses its SPIFFE URI.
			for _, target := range []struct {
				collection constants.CollectionName
				id         string
			}{
				{constants.CollectionTrustedSigners, appName},
				{constants.CollectionAppPolicies, appURI.String()},
			} {
				t.Run(string(target.collection), func(t *testing.T) {
					store := f.Service.GetDocStore()
					before, err := store.DocGet(t.Context(), string(target.collection), target.id)
					require.NoError(t, err)
					require.NotNil(t, before, "governed enrollment must create the authority record")
					body, err := json.Marshal(struct {
						AppID string `json:"app_id"`
					}{AppID: target.id})
					require.NoError(t, err)
					status, response := requestDataAPI(t, client, http.MethodPost, baseURL+constants.APIPaths.AdminAppsRevoke, string(body))
					assert.Equal(t, http.StatusForbidden, status, "%s", response)
					assert.Contains(t, string(response), constants.ErrPrivilegedEndpointAccess.Error())
					after, err := store.DocGet(t.Context(), string(target.collection), target.id)
					require.NoError(t, err)
					assert.Equal(t, before, after, "delegation must not grant the owner's revocation authority")
				})
			}
		})
	}
}

// Use the production app client, proof-of-possession completion, and governed
// owner decision. L3 postures use the owner's real software passkey proof.
func enrollDelegatedApp(t *testing.T, f *fixtures.GatewayFixture, owner *fixtures.ClientIdentity, appName string) *http.Client {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
	cfg := &cliconfig.Config{RuntimeDir: fileSvc.Resolve(""), Keyring: keystoretest.NewMemoryKeyring()}
	bootstrapClient := fixtures.CreateNoCertClient(t, f)
	t.Cleanup(bootstrapClient.CloseIdleConnections)
	enroller, err := cliauth.NewAppPlatformEnrollmentClient(appName, fileSvc, cfg, logger, cliauth.AppEnrollmentOptions{
		GenerateSigningKey: true,
		GatewayHTTPURL:     network.LocalhostHTTPSURL(f.Service.GetHTTPSPort()),
		HTTPClient:         bootstrapClient,
		Approver: func(ctx context.Context, target models.PlatformEnrollmentDecisionTarget) error {
			_, err := f.Service.GetPlatformEnrollmentService().DecideBatch(ctx, owner.UserID, models.PlatformEnrollmentBatchDecisionRequest{
				Requests: []models.PlatformEnrollmentDecisionTarget{target}, Decision: models.PlatformEnrollmentDecisionApprove,
			})
			if !f.Config.Gateway.Posture.RequiresL3() {
				return err
			}
			var required *models.PlatformEnrollmentL3RequiredError
			require.ErrorAs(t, err, &required)
			options, err := f.Service.GetHTTPHandler().GetPasskeyHandler().GenerateApprovalChallenge(ctx, owner.UserID, required.Suspension.TxHash)
			require.NoError(t, err)
			assertion := owner.Passkey.Assert(t, options.Response.RelyingPartyID, options.Response.Challenge)
			_, err = f.Service.GetMCPGateway().ResumeWithL3Proof(ctx, required.Suspension.TxHash, owner.UserID, &commonv1.L3Proof{
				CredentialId: assertion.ID, ClientDataJson: assertion.ClientDataJSON,
				AuthenticatorData: assertion.AuthenticatorData, Signature: assertion.Signature,
			})
			return err
		},
	})
	require.NoError(t, err)
	_, err = enroller.Enroll(t.Context(), io.Discard)
	require.NoError(t, err)
	identity, err := enroller.LoadIdentity()
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	require.NoError(t, err)
	userURI, err := protocol.NewWorkloadIdentity().UserSPIFFEURL(owner.UserID)
	require.NoError(t, err)
	require.Len(t, leaf.URIs, 2)
	require.Equal(t, userURI.String(), leaf.URIs[1].String())
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(testutil.ReadRootCA(t, f.PKIDir)))
	require.True(t, roots.AppendCertsFromPEM(testutil.ReadOperatorCA(t, f.PKIDir)))
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{*identity}, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 10 * time.Second}
}

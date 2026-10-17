// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package auth_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	cliauth "github.com/zerotrustdata/ztd/internal/cli/auth"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func TestPlatformEnrollment_CLISigningIdentityAndConsensusAcrossPostures(t *testing.T) {
	for _, posture := range constants.AllPostures {
		t.Run(string(posture), func(t *testing.T) {
			logger := testutil.NewTestLogger()
			gatewayFS, err := fs.NewRuntimeFileService(testutil.TempDir(t), logger)
			require.NoError(t, err)
			require.NoError(t, gatewayFS.CreateRuntimeTree(t.Context()))
			gatewayConfig := testutil.NewTestConfig(t)
			gatewayConfig.Gateway.Posture = posture
			gatewayConfig.Gateway.HTTPPort = 0
			gatewayConfig.Gateway.HTTPSPort = 0
			gatewayConfig.Gateway.DataDir = gatewayFS.Resolve(constants.DataDirname)
			gatewayConfig.Gateway.PKIDir = gatewayFS.Resolve(constants.PkiDirname)
			gatewayConfig.Gateway.SecretsDir = gatewayFS.Resolve(constants.SecretsDirname)
			gatewayConfig.Gateway.VaultDir = gatewayFS.Resolve(constants.VaultDirname)
			gatewayConfig.Gateway.Keyring = keystoretest.NewMemoryKeyring()
			ks, err := keystore.NewWithKeyringAndFS(logger, gatewayConfig.Gateway.Keyring, gatewayFS)
			require.NoError(t, err)
			require.NoError(t, ks.Initialize())
			ls, err := gateway.NewGatewayModeService(gatewayConfig, gatewayFS, logger)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			started := make(chan error, 1)
			go func() { started <- ls.Start(ctx) }()
			t.Cleanup(func() {
				cancel()
				require.NoError(t, ls.Stop(context.Background()))
				require.NoError(t, <-started)
			})
			select {
			case <-ls.Ready():
			case <-ctx.Done():
				t.Fatal("Gateway did not become ready")
			}
			// The governance boundary consumes an active local account ID,
			// independently of the account's authentication provider. This tests
			// the JWT/JIT account shape, not external token validation or OIDC login.
			owner, err := ls.GetUserService().CreateUserWithSub(t.Context(), "00u-federated-owner")
			require.NoError(t, err)
			require.Empty(t, owner.PasskeyCredentials)
			passkey := testutil.NewSoftwarePasskey(t)
			if posture.RequiresL3() {
				owner.PasskeyCredentials = []models.PasskeyCredential{passkey.Credential(t)}
				encoded, err := json.Marshal(owner)
				require.NoError(t, err)
				require.NoError(t, ls.GetDocStore().DocSet(t.Context(), string(constants.CollectionUsers), owner.ID, encoded))
			}
			resume := func(suspension models.PlatformEnrollmentL3SuspensionResponse) {
				require.True(t, posture.RequiresL3())
				require.Equal(t, posture, suspension.Posture)
				options, err := ls.GetHTTPHandler().GetPasskeyHandler().GenerateApprovalChallenge(t.Context(), owner.ID, suspension.TxHash)
				require.NoError(t, err)
				assertion := passkey.Assert(t, options.Response.RelyingPartyID, options.Response.Challenge)
				_, err = ls.GetMCPGateway().ResumeWithL3Proof(t.Context(), suspension.TxHash, owner.ID, &commonv1.L3Proof{
					CredentialId: assertion.ID, ClientDataJson: assertion.ClientDataJSON,
					AuthenticatorData: assertion.AuthenticatorData, Signature: assertion.Signature,
				})
				require.NoError(t, err)
			}
			server := httptest.NewTLSServer(ls.GetHTTPHandler())
			t.Cleanup(server.Close)
			fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), logger)
			require.NoError(t, err)
			require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
			cfg := &cliconfig.Config{RuntimeDir: fileSvc.Resolve(""), Paths: &cliconfig.PathsConfig{Host: server.URL}, Keyring: keystoretest.NewMemoryKeyring()}
			client, err := cliauth.NewAppPlatformEnrollmentClient("cli-reviewer", fileSvc, cfg, logger, cliauth.AppEnrollmentOptions{
				GenerateSigningKey: true, GatewayHTTPURL: server.URL, HTTPClient: server.Client(),
				Approver: func(ctx context.Context, target models.PlatformEnrollmentDecisionTarget) error {
					_, err := ls.GetPlatformEnrollmentService().DecideBatch(ctx, owner.ID, models.PlatformEnrollmentBatchDecisionRequest{
						Requests: []models.PlatformEnrollmentDecisionTarget{target}, Decision: models.PlatformEnrollmentDecisionApprove,
					})
					if posture.RequiresL3() {
						var required *models.PlatformEnrollmentL3RequiredError
						require.True(t, errors.As(err, &required))
						resume(required.Suspension)
					} else {
						require.NoError(t, err)
					}
					return err
				},
			})
			require.NoError(t, err)
			_, err = client.Enroll(ctx, io.Discard)
			require.NoError(t, err)
			identity, err := client.LoadIdentity()
			require.NoError(t, err)
			require.IsType(t, &ecdsa.PrivateKey{}, identity.PrivateKey)
			key, err := cliauth.LoadAppSigningKey(t.Context(), fileSvc, cfg, "cli-reviewer")
			require.NoError(t, err)
			registered, err := ls.GetSignerStore().GetTrustedSigner(t.Context(), "cli-reviewer")
			require.NoError(t, err)
			require.Equal(t, key.Public(), registered)
			other, err := ls.GetUserService().CreateUserWithSub(t.Context(), "another-federated-account")
			require.NoError(t, err)
			_, err = ls.GetPlatformEnrollmentService().BindConsensusApplication(t.Context(), other.ID, models.PlatformEnrollmentBindConsensusRequest{AppID: "cli-reviewer", EndpointURL: "https://reviewer.example/deliberate"})
			require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidDecision)

			_, err = ls.GetPlatformEnrollmentService().BindConsensusApplication(t.Context(), owner.ID, models.PlatformEnrollmentBindConsensusRequest{AppID: "cli-reviewer", EndpointURL: "https://reviewer.example/deliberate"})
			if posture.RequiresL3() {
				var required *models.PlatformEnrollmentL3RequiredError
				require.True(t, errors.As(err, &required))
				bound, err := ls.GetConsensusStore().GetBoundConsensusApp(t.Context())
				require.NoError(t, err)
				require.Nil(t, bound)
				resume(required.Suspension)
			} else {
				require.NoError(t, err)
			}
			bound, err := ls.GetConsensusStore().GetBoundConsensusApp(t.Context())
			require.NoError(t, err)
			require.Equal(t, []string{"cli-reviewer"}, bound.MemberAppIDs)
			trust, err := ls.GetConsensusStore().OperatorL2Trust(t.Context())
			require.NoError(t, err)
			require.Len(t, trust.Signers, 1)
			require.Equal(t, hex.EncodeToString(registered), trust.Signers[0].PublicKeyHex)
		})
	}
}

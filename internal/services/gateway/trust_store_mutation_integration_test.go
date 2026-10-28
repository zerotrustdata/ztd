// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestTrustStoreUngovernedMutations proves that no mTLS identity class (app,
// first-user-delegated app, outbound Operator, or owner CLI) can mutate trust
// anchors or sensitive infrastructure collections through ungoverned HTTP routes.
// Workload and Operator clients are denied with 403 Forbidden by identity route
// allowlists, and CLI clients are denied with 405 Method Not Allowed by direct
// route removal. In all cases, underlying SQLite store state remains unmodified.
func TestTrustStoreUngovernedMutations(t *testing.T) {
	f := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "trust-store-mutations",
		Posture:  constants.PostureConsensus,
	})
	f.WaitForReady(t)

	owner := fixtures.EnrollClientIdentity(t, f, "trust-store-owner", "trust-store-org", "trust-store-host", "trust-store.local")
	first, err := f.Service.GetUserService().IsFirstUser(t.Context(), owner.UserID)
	require.NoError(t, err)
	require.True(t, first)

	// Seed a bound consensus member in trusted_signers and consensus binding.
	const boundMemberID = "consensus-member-1"
	memberPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	memberSigner := models.TrustedSigner{
		ID:        boundMemberID,
		PublicKey: hex.EncodeToString(memberPub),
		Enabled:   true,
		AddedAt:   time.Now().UTC(),
	}
	require.NoError(t, f.Service.GetSignerStore().AddTrustedSigner(t.Context(), memberSigner))

	consensusPolicy := models.ConsensusPolicy{
		ID:           string(constants.DocIDConsensusBinding),
		MemberAppIDs: []string{boundMemberID},
		Quorum:       1,
		Provider:     string(constants.ConsensusProviderApplication),
		Enabled:      true,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	policyBytes, err := json.Marshal(consensusPolicy)
	require.NoError(t, err)
	require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionConsensus), string(constants.DocIDConsensusBinding), policyBytes))

	// Seed a revoked certificate in revoked_certificates.
	const revokedCertID = "cert-revoked-001"
	revocationDoc := json.RawMessage(`{"serial":"001","reason":"compromised","revoked_at":"2026-10-26T00:00:00Z"}`)
	require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionRevokedCertificates), revokedCertID, revocationDoc))

	// Seed a user in users.
	const targetUserID = "seed-target-user"
	userDoc := json.RawMessage(`{"id":"seed-target-user","role":"operator","created_at":"2026-10-26T00:00:00Z"}`)
	require.NoError(t, f.Service.GetDocStore().DocSet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), targetUserID, userDoc))

	baseURL := network.LocalhostHTTPSURL(f.Service.GetHTTPSPort())

	clients := []struct {
		name       string
		client     *http.Client
		wantModify int
	}{
		{"app", seedUnprovenAppWorkload(t, f), http.StatusForbidden},
		{"owner-delegated-app", seedUnprovenAppWorkload(t, f, owner.UserID), http.StatusForbidden},
		{"outbound-operator", fixtures.CreateMTLSClient(t, f, owner), http.StatusForbidden},
		{"owner-cli", fixtures.CreateCLIMTLSClient(t, f, owner), http.StatusMethodNotAllowed},
	}

	for _, identity := range clients {
		t.Run(identity.name, func(t *testing.T) {
			defer identity.client.CloseIdleConnections()

			// 1. Attempt to overwrite bound member signer via POST /api/v1/governance/signers
			t.Run("post-governance-signers", func(t *testing.T) {
				attackerPub, _, err := ed25519.GenerateKey(nil)
				require.NoError(t, err)
				body, err := json.Marshal(models.TrustedSigner{
					ID:        boundMemberID,
					PublicKey: hex.EncodeToString(attackerPub),
					Enabled:   true,
				})
				require.NoError(t, err)
				status, resp := requestDataAPI(t, identity.client, http.MethodPost, baseURL+constants.APIPaths.GovernanceSigners, string(body))
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				stored, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), boundMemberID)
				require.NoError(t, err)
				require.NotNil(t, stored)
				assert.Equal(t, memberSigner.PublicKey, hex.EncodeToString(stored), "bound signer key must remain unaltered")
			})

			// 2. Attempt to overwrite bound member signer via PUT /api/v1/data/trusted_signers/{id}
			t.Run("put-data-trusted-signers", func(t *testing.T) {
				attackerPub, _, err := ed25519.GenerateKey(nil)
				require.NoError(t, err)
				body, err := json.Marshal(models.TrustedSigner{
					ID:        boundMemberID,
					PublicKey: hex.EncodeToString(attackerPub),
					Enabled:   true,
				})
				require.NoError(t, err)
				path := baseURL + constants.APIPaths.DataPrefix + string(constants.CollectionTrustedSigners) + "/" + boundMemberID
				status, resp := requestDataAPI(t, identity.client, http.MethodPut, path, string(body))
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				stored, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), boundMemberID)
				require.NoError(t, err)
				require.NotNil(t, stored)
				assert.Equal(t, memberSigner.PublicKey, hex.EncodeToString(stored))
			})

			// 3. Attempt to delete a revocation via DELETE /api/v1/data/revoked_certificates/{id}
			t.Run("delete-data-revocation", func(t *testing.T) {
				before, err := f.Service.GetDocStore().DocGet(t.Context(), marshaler.CollectionName(constants.CollectionRevokedCertificates), revokedCertID)
				require.NoError(t, err)
				require.NotNil(t, before)

				path := baseURL + constants.APIPaths.DataPrefix + string(constants.CollectionRevokedCertificates) + "/" + revokedCertID
				status, resp := requestDataAPI(t, identity.client, http.MethodDelete, path, "")
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				after, err := f.Service.GetDocStore().DocGet(t.Context(), marshaler.CollectionName(constants.CollectionRevokedCertificates), revokedCertID)
				require.NoError(t, err)
				assert.Equal(t, before, after, "revocation document must not be deleted")
			})

			// 4. Attempt to upsert a user via PUT /api/v1/data/users/{id}
			t.Run("put-data-users", func(t *testing.T) {
				before, err := f.Service.GetDocStore().DocGet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), targetUserID)
				require.NoError(t, err)
				require.NotNil(t, before)

				body := `{"id":"seed-target-user","role":"admin"}`
				path := baseURL + constants.APIPaths.DataPrefix + string(constants.CollectionUsers) + "/" + targetUserID
				status, resp := requestDataAPI(t, identity.client, http.MethodPut, path, body)
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				after, err := f.Service.GetDocStore().DocGet(t.Context(), marshaler.CollectionName(constants.CollectionUsers), targetUserID)
				require.NoError(t, err)
				assert.Equal(t, before, after, "user record must remain unchanged")
			})

			// 5. Attempt to mutate settings via PUT /api/v1/data/settings
			t.Run("put-data-settings", func(t *testing.T) {
				store := f.Service.GetDocStore()
				before, err := store.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionSettings), string(constants.DocIDPlatformSettings))
				require.NoError(t, err)

				body := `{"value":"malicious-setting"}`
				status, resp := requestDataAPI(t, identity.client, http.MethodPut, baseURL+constants.APIPaths.DataSettings, body)
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				after, err := store.DocGet(t.Context(), marshaler.CollectionName(constants.CollectionSettings), string(constants.DocIDPlatformSettings))
				require.NoError(t, err)
				assert.Equal(t, before, after, "settings singleton must remain unchanged")
			})

			// 6. Attempt to revoke a signer via DELETE /api/v1/governance/signers/{id}
			t.Run("delete-governance-signer", func(t *testing.T) {
				path := baseURL + constants.APIPaths.GovernanceSignersPrefix + boundMemberID
				status, resp := requestDataAPI(t, identity.client, http.MethodDelete, path, "")
				assert.Equal(t, identity.wantModify, status, "%s", resp)

				stored, err := f.Service.GetSignerStore().GetTrustedSigner(t.Context(), boundMemberID)
				require.NoError(t, err)
				require.NotNil(t, stored, "signer must remain present in store")
				assert.Equal(t, memberSigner.PublicKey, hex.EncodeToString(stored))
			})
		})
	}
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)

func TestGatewayModeService_IsGovernanceReady(t *testing.T) {
	t.Run("Doctrine posture returns true without signers", func(t *testing.T) {
		ls := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureDoctrine})
		assert.True(t, ls.IsGovernanceReady(t.Context()))
	})

	t.Run("Empty posture fails construction", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		fileSvc := newTestFileSvc(t)
		cfg.Gateway.DataDir = testutil.TempDir(t)
		cfg.Gateway.PKIDir = testutil.TempDir(t)
		cfg.Gateway.SecretsDir = fileSvc.Resolve(constants.SecretsDirname)
		cfg.Gateway.VaultDir = fileSvc.Resolve(constants.VaultDirname)
		cfg.Gateway.Posture = ""
		db, err := openTestDB(t, fileSvc, logger)
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })

		_, err = NewGatewayModeServiceWithDB(cfg, fileSvc, logger, db, nil, nil)
		assert.ErrorIs(t, err, constants.ErrInvalidPosture)
	})

	t.Run("Notary posture returns false without signers", func(t *testing.T) {
		ls := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureNotary})
		assert.False(t, ls.IsGovernanceReady(t.Context()))
	})

	t.Run("Notary posture returns false with signers but no bound consensus", func(t *testing.T) {
		ls := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureNotary})
		addGovernanceTestSigner(t, ls, "test-signer-1")

		assert.False(t, ls.IsGovernanceReady(t.Context()))
		assert.Equal(t, models.HealthConsensus{}, ls.ConsensusHealth(t.Context()))
	})

	t.Run("Notary posture returns true with a ready consensus binding", func(t *testing.T) {
		ls := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureNotary})
		addGovernanceTestSigner(t, ls, "test-signer-1")
		require.NoError(t, ls.GetConsensusStore().AddConsensus(t.Context(), models.ConsensusPolicy{
			ID:              string(constants.DocIDConsensusBinding),
			EndpointURL:     "https://consensus.example.test/deliberate",
			MemberAppIDs:    []string{"test-signer-1"},
			Quorum:          1,
			Provider:        string(constants.ConsensusProviderApplication),
			RequireDistinct: true,
			Enabled:         true,
		}))

		assert.True(t, ls.IsGovernanceReady(t.Context()))
		assert.True(t, ls.ConsensusHealth(t.Context()).Ready)
	})
}

// addGovernanceTestSigner registers an enabled L2 member signer backed by a fresh Ed25519 key.
func addGovernanceTestSigner(t *testing.T, ls *GatewayModeService, id string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	require.NoError(t, ls.GetSignerStore().AddTrustedSigner(t.Context(), models.TrustedSigner{
		ID:        id,
		PublicKey: hex.EncodeToString(pub),
		AddedAt:   time.Now().UTC(),
		Enabled:   true,
	}))
}

func TestGatewayModeService_GetGovernanceDeps(t *testing.T) {
	ls := newTestGatewayService(t, testGatewayOpts{})

	deps := ls.GetGovernanceDeps()
	assert.NotNil(t, deps)
	assert.NotNil(t, deps.ReplayStore)
	assert.NotNil(t, deps.StateRootProvider)
	assert.NotNil(t, deps.TransactionAudit)
	assert.NotNil(t, deps.GovernedDocStore, "GovernanceDeps.GovernedDocStore should be populated by GetGovernanceDeps")
	assert.NotNil(t, deps.L3Notary)
	assert.NotNil(t, deps.SignerStore)
	assert.NotNil(t, deps.FieldReader)
	assert.Equal(t, ls.GetDocStore(), deps.FieldReader)
	assert.NotNil(t, deps.Doctrine, "GovernanceDeps.Doctrine should be populated by GetGovernanceDeps")
}

func TestGatewayModeService_GetGovernanceDeps_AlwaysUsesRealNotary(t *testing.T) {
	t.Setenv("ZTD_L3_MOCK", "true")

	ls := newTestGatewayService(t, testGatewayOpts{})

	deps := ls.GetGovernanceDeps()
	require.NotNil(t, deps.L3Notary)

	proof := &commonv1.L3Proof{}
	ok, err := deps.L3Notary.VerifyL3Proof(context.Background(), "test-user", "test-hash", "", proof)
	assert.False(t, ok)
	assert.ErrorIs(t, err, constants.ErrPasskeyProofRequired)
}

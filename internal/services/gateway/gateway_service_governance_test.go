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
	"encoding/json"
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

	t.Run("Notary posture returns true with signers", func(t *testing.T) {
		ls := newTestGatewayService(t, testGatewayOpts{posture: constants.PostureNotary})

		signer := models.TrustedSigner{
			ID:        "test-signer-1",
			PublicKey: "abc123",
			AddedAt:   time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC),
			Enabled:   true,
		}
		signerBytes, err := json.Marshal(signer)
		require.NoError(t, err)
		err = ls.GetDocStore().DocSet(t.Context(), "trusted_signers", "test-signer-1", signerBytes)
		require.NoError(t, err)

		assert.True(t, ls.IsGovernanceReady(t.Context()))
	})
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

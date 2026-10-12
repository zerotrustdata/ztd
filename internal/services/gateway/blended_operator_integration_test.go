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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestGatewayConstructsAndRegistersAllEmbeddedOperatorCapabilities(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)
	cfg.Gateway.DataDir = testutil.TempDir(t)
	cfg.Gateway.PKIDir = testutil.TempDir(t)
	cfg.Gateway.SecretsDir = fileSvc.Resolve(constants.SecretsDirname)
	cfg.Gateway.VaultDir = fileSvc.Resolve(constants.VaultDirname)
	cfg.Gateway.Posture = config.PostureDoctrine
	cfg.OperatorCapabilities = constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver}
	cfg.Inference = config.InferenceConfig{Enabled: true, OllamaEndpoint: "http://127.0.0.1:11434", KeepAlive: "-1"}
	cfg.ProviderBoundaryObserver = config.ProviderBoundaryObserverConfig{Enabled: true, ObserverID: "embedded-observer"}
	cfg.ProvenanceOperator = config.ProvenanceOperatorConfig{Enabled: true, OperatorID: "embedded-provenance", ModelStorageRoot: testutil.TempDir(t)}
	db, err := openTestDB(t, fileSvc, logger)
	require.NoError(t, err)
	svc, err := NewGatewayModeServiceWithDB(cfg, fileSvc, logger, db, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Stop(context.Background()) })
	_, sessionID, err := svc.embeddedOperator.ClaimEmbeddedOperator(t.Context(), "owner")
	require.NoError(t, err)
	ops, err := svc.reg.ListUserOperators(t.Context(), "owner")
	require.NoError(t, err)
	require.Len(t, ops, 1)
	require.Equal(t, sessionID, ops[0].OperatorSessionId)
	require.Equal(t, cfg.OperatorCapabilities, operatorcapability.GetOperatorCapabilities(ops[0]))
	require.True(t, operatorcapability.IsDataOperator(ops[0]))
	require.Len(t, operatorcapability.ActiveProvenanceOperators(ops), 1)
	require.Len(t, operatorcapability.ActiveProviderBoundaryObservers(ops), 1)
}

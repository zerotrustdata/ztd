//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	storagetest "github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestOperatorPubSubService_Start(t *testing.T) {

	t.Run("rejects start when already running", func(t *testing.T) {
		f := newPubsubFixture(t)
		ctx := context.Background()
		err := f.Svc.Start(ctx)
		require.NoError(t, err)

		err = f.Svc.Start(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "already running")

		f.Svc.Stop()
	})

	t.Run("starts in gateway mode without pub/sub subscription", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		cfg.OperatorID = ""
		cfg.OperatorSessionId = ""
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:    mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:       storagetest.NewTestLedger(t),
			RuntimeVault: governancetest.UnlockedVault{},
			Config:       cfg,
			Logger:       testutil.NewTestLogger(),
			PubSubClient: pubsubtest.NewMockOperatorPubSubClient(),
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)

		err = svc.Start(context.Background())
		require.NoError(t, err)
		svc.Stop()
	})
}

func TestOperatorPubSubService_SendAutomaticHeartbeat(t *testing.T) {
	t.Run("sends automatic heartbeat via heartbeat service", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()
		privKey := ed25519.NewKeyFromSeed(make([]byte, 32))
		mockPublisher := &mockResultsPublisher{}
		svc, err := NewOperatorPubSubService(withTestDeps(t, CommandServiceConfig{
			Scrubbing:          mustNewScrubbingSvc(t, testutil.NewTestLogger()),
			Ledger:             storagetest.NewTestLedger(t),
			RuntimeVault:       governancetest.UnlockedVault{},
			Config:             cfg,
			Logger:             logger,
			PubSubClient:       pubsubtest.NewMockOperatorPubSubClient(),
			ResultsService:     mockPublisher,
			ActuatorSigningKey: privKey,
			ActuatorKeyID:      "test-key",
		}), OutboundModeDeps{
			GovernanceCoreDeps: GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}, SessionID: "session-1"},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)

		err = svc.SendAutomaticHeartbeat()
		assert.NoError(t, err)
		assert.True(t, mockPublisher.publishHeartbeatCalled, "automatic heartbeat must be published through the results publisher")
	})
}

func TestOperatorPubSubService_BindMCPGatewayRejectsBindingAfterStart(t *testing.T) {
	svc := newPubsubFixture(t).Svc
	svc.config.OperatorID = ""
	svc.config.OperatorSessionId = ""
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, svc.Stop()) })

	err := svc.BindMCPGateway(&mcp.GatewayService{})

	assert.ErrorIs(t, err, constants.ErrPubSubMCPGatewayBindAfterStart)
	assert.Nil(t, svc.GetMCPGateway())
}

func TestOperatorPubSubService_StartGatewayModeRequiresMCPGatewayBinding(t *testing.T) {
	svc := newGatewayPubsubServiceForBindingTest(t)

	err := svc.Start(context.Background())

	assert.ErrorIs(t, err, constants.ErrPubSubMCPGateway)
	assert.False(t, svc.running)
}

func TestOperatorPubSubService_StartOutboundModeDoesNotRequireMCPGatewayBinding(t *testing.T) {
	svc := newPubsubFixture(t).Svc
	svc.config.OperatorID = ""
	svc.config.OperatorSessionId = ""

	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, svc.Stop()) })
	assert.Nil(t, svc.GetMCPGateway())
	_, hasMCPHandler := svc.actionHandlers[constants.ActionTypeMcpCall]
	_, hasA2AHandler := svc.actionHandlers[constants.ActionTypeA2aCall]
	assert.False(t, hasMCPHandler)
	assert.False(t, hasA2AHandler)
}

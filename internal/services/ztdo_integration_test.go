//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"crypto/tls"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/services/execution"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/governance/governancetest"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestZtdoService_SubServices_Initialization(t *testing.T) {
	t.Run("execution service", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()

		svc := execution.NewExecutionService(cfg, logger)
		assert.NotNil(t, svc)
	})

	t.Run("file edit service", func(t *testing.T) {
		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()

		fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, logger)
		require.NoError(t, err)
		require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
		svc, err := execution.NewFileEditService(cfg, logger, fileSvc)
		require.NoError(t, err)
		assert.NotNil(t, svc)
	})

	t.Run("pub/sub command service", func(t *testing.T) {

		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()

		execSvc := execution.NewExecutionService(cfg, logger)
		fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, logger)
		require.NoError(t, err)
		require.NoError(t, fileSvc.CreateRuntimeTree(t.Context()))
		fileEditSvc, err := execution.NewFileEditService(cfg, logger, fileSvc)
		require.NoError(t, err)

		scrubbingSvc, err := scrubbing.NewScrubbingService(t.Context(), scrubbing.DefaultConfig(), logger, storagetest.NewTestTokenStore())
		require.NoError(t, err)

		ledger := storagetest.NewTestLedger(t)
		eventsStore := storagetest.NewTestEventsStore(t)
		historyHandler, err := storage.NewHistoryHandler(eventsStore, ledger, logger, nil, "")
		require.NoError(t, err)
		pubsubClient := pubsubtest.NewMockOperatorPubSubClient()
		resultsSvc, err := pubsub.NewPubSubResultsService(cfg, logger, pubsubClient, scrubbingSvc)
		require.NoError(t, err)

		cmdSvc, err := pubsub.NewOperatorPubSubService(pubsub.CommandServiceConfig{
			Scrubbing:      scrubbingSvc,
			Ledger:         ledger,
			RuntimeVault:   governancetest.UnlockedVault{},
			Config:         cfg,
			Logger:         logger,
			Execution:      execSvc,
			FileEdit:       fileEditSvc,
			PubSubClient:   pubsubClient,
			ResultsService: resultsSvc,
			EventsStore:    eventsStore,
			HistoryHandler: historyHandler,
		}, pubsub.OutboundModeDeps{
			GovernanceCoreDeps: pubsub.GovernanceCoreDeps{
				ExecutionTarget:   &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1", "op-1"}},
				ReplayStore:       &testutil.MockReplayStore{},
				StateRootProvider: testutil.NewMockStateRootProvider("test-state-root"),
				TransactionAudit:  &testutil.MockTransactionAudit{},
				L3Notary:          &testutil.MockL3Notary{},
				Doctrine:          governance.NewL1Doctrine(),
			},
		})
		require.NoError(t, err)
		require.NotNil(t, cmdSvc)
		t.Cleanup(func() { cmdSvc.Stop() })
	})

	t.Run("pub/sub results service", func(t *testing.T) {

		cfg := testutil.NewTestConfig(t)
		logger := testutil.NewTestLogger()

		subTrustStore := testutil.GetTestTrustStore()
		subClientIdentity := certs.NewClientIdentity(tls.Certificate{})
		subTLSCfg := certs.NewTLSConfig(subTrustStore, subClientIdentity)

		client, err := pubsub.NewOperatorPubSubClient(testutil.GetTestOperatorDirectURL(), "", logger, subTLSCfg)
		require.NoError(t, err)
		t.Cleanup(func() { client.Close() })

		resultsSvc, err := pubsub.NewPubSubResultsService(cfg, logger, client, nil)
		require.NoError(t, err)
		assert.NotNil(t, resultsSvc)
	})
}

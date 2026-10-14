// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"context"
	"crypto/tls"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func newTestTLSConfig(t *testing.T) *certs.TLSConfig {
	t.Helper()
	trustStore := testutil.GetTestTrustStore()
	clientIdentity := certs.NewClientIdentity(tls.Certificate{})
	return certs.NewTLSConfig(trustStore, clientIdentity)
}

// newTestL2Trust is an empty bundle trust: no consensus policy, no signers.
func newTestL2Trust(t *testing.T) *governance.L2TrustStore {
	t.Helper()
	store, err := governance.NewL2TrustStore(nil)
	require.NoError(t, err)
	return store
}

func newTestFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	return fileSvc
}

// newTestCanonicalDB opens the canonical database in its own runtime root, the
// way StartOperator does: an initialized keystore over an in-memory keyring,
// then OpenCanonicalDBService. Close is registered on cleanup; Close is
// idempotent, so a service that takes ownership and closes it first is safe.
func newTestCanonicalDB(t *testing.T) *gateway.CanonicalDBService {
	t.Helper()
	logger := testutil.NewTestLogger()
	fileSvc, err := fs.NewRuntimeFileService(testutil.TempDir(t), logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	ks, err := keystore.NewWithKeyringAndFS(logger, keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EnforcePermissions())
	db, err := gateway.OpenCanonicalDBService(logger, ks, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newTestPubSubClientFactory() PubSubClientFactory {
	return func(string, string, *slog.Logger, *certs.TLSConfig) (pubsub.PubSubClient, error) {
		return pubsubtest.NewMockOperatorPubSubClient(), nil
	}
}

func TestNewZtdoService_InitialState(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	before := time.Now().UTC()
	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	after := time.Now().UTC()

	require.NoError(t, err)
	require.NotNil(t, service)

	assert.Equal(t, cfg, service.config)
	assert.Equal(t, logger, service.logger)
	assert.False(t, service.running)

	assert.False(t, service.startTime.Before(before), "startTime should be >= before")
	assert.False(t, service.startTime.After(after), "startTime should be <= after")
	assert.Equal(t, time.UTC, service.startTime.Location())

	assert.Nil(t, service.execution)
	assert.Nil(t, service.fileEdit)
	assert.Nil(t, service.pubSubCommands)
	assert.Nil(t, service.pubSubResults)
	assert.Nil(t, service.pubSubClient)
	assert.Nil(t, service.tokenStore)
	assert.Nil(t, service.suspendedTxStore)
	assert.Nil(t, service.evidence)
}

func TestNewZtdoService_RequiresCanonicalDB(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)

	service, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), nil, newTestPubSubClientFactory())

	assert.Nil(t, service)
	assert.ErrorIs(t, err, constants.ErrGatewayDatabaseServiceNotConfigured)
}

func TestNewZtdoService_RequiresPubSubClientFactory(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)

	service, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), nil)

	assert.Nil(t, service)
	assert.ErrorIs(t, err, constants.ErrInternal)
}

func TestNewZtdoService_PreservesConfig(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	cfg.MaxConcurrentTasks = 42
	logger := testutil.NewTestLogger()
	fileSvc := newTestFileSvc(t)

	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), fileSvc, newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	assert.Equal(t, 42, service.config.MaxConcurrentTasks)
	assert.Equal(t, cfg.OperatorID, service.config.OperatorID)
	assert.Equal(t, cfg.OperatorSessionId, service.config.OperatorSessionId)
}

func TestNewZtdoService_IndependentInstances(t *testing.T) {
	t.Parallel()
	cfg1 := testutil.NewTestConfig(t)
	cfg2 := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	svc1, err := NewZtdoService(cfg1, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	svc2, err := NewZtdoService(cfg2, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	assert.NotEqual(t, svc1.config.OperatorID, svc2.config.OperatorID)
	assert.NotEqual(t, svc1.config.OperatorSessionId, svc2.config.OperatorSessionId)
}

func TestZtdoService_Start_AlreadyRunning(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	service.mu.Lock()
	service.running = true
	service.mu.Unlock()

	err = service.Start(context.Background())
	require.Error(t, err)
	assert.Error(t, err)
}

func TestZtdoService_Stop(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	// Manually set running to true to test Stop()
	service.mu.Lock()
	service.running = true
	service.mu.Unlock()

	err = service.Stop(context.Background())
	require.NoError(t, err)

	service.mu.RLock()
	running := service.running
	service.mu.RUnlock()
	assert.False(t, running)
}

func TestZtdoService_ConcurrentStateAccess(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	service, err := NewZtdoService(cfg, logger, newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), newTestPubSubClientFactory())
	require.NoError(t, err)

	done := make(chan struct{}, 20)

	for i := 0; i < 10; i++ {
		go func() {
			service.mu.RLock()
			_ = service.running
			_ = service.config
			service.mu.RUnlock()
			done <- struct{}{}
		}()
	}

	for i := 0; i < 10; i++ {
		go func(val bool) {
			service.mu.Lock()
			service.running = val
			service.mu.Unlock()
			done <- struct{}{}
		}(i%2 == 0)
	}

	for i := 0; i < 20; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent state access timed out")
		}
	}
}

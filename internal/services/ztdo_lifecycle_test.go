//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/certs"
	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	pubsubtest "github.com/zerotrustdata/ztd/internal/services/pubsub/pubsubtest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// enrolledTestConfig is a config carrying the in-memory identity StartOperator
// applies from the bootstrap bundle before the service starts.
func enrolledTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := testutil.NewTestConfig(t)
	cfg.OperatorID = "test-op-1"
	cfg.OperatorSessionId = "test-sess-1"
	return cfg
}

func TestZtdoService_Start_SuccessFlow(t *testing.T) {
	cfg := enrolledTestConfig(t)
	cfg.PubSubURL = "wss://127.0.0.1:0" // dummy

	// Initialize paths with test directory
	require.NoError(t, paths.InitWithBase(cfg.WorkDir))

	fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, testutil.NewVerboseTestLogger(t))
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	// StartOperator opens the canonical database before the service exists; do the same here,
	// with an in-memory keyring for the master key.
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewVerboseTestLogger(t), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EnforcePermissions())
	canonicalDB, err := gateway.OpenCanonicalDBService(testutil.NewVerboseTestLogger(t), ks, fileSvc)
	require.NoError(t, err)

	tlsConfig := newTestTLSConfig(t)
	factoryCalls := 0
	factory := func(baseURL, serverName string, logger *slog.Logger, receivedTLSConfig *certs.TLSConfig) (pubsub.PubSubClient, error) {
		factoryCalls++
		assert.Equal(t, cfg.PubSubURL, baseURL)
		assert.Equal(t, cfg.TLSServerName, serverName)
		assert.NotNil(t, logger)
		assert.Same(t, tlsConfig, receivedTLSConfig)
		return pubsubtest.NewMockOperatorPubSubClient(), nil
	}
	service, err := NewZtdoService(cfg, testutil.NewVerboseTestLogger(t), tlsConfig, newTestL2Trust(t), fileSvc, canonicalDB, factory)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- service.Start(ctx)
	}()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("Timed out waiting for ZtdoService to start")
	}

	assert.True(t, service.running)
	assert.Equal(t, 1, factoryCalls)

	service.pubSubCommands.ShutdownChan <- "test shutdown request"
	select {
	case <-service.Done():
	case <-ctx.Done():
		t.Fatal("Timed out waiting for the Operator service to observe shutdown")
	}
	require.ErrorIs(t, service.ctx.Err(), context.Canceled)

	// Clean up to avoid background goroutines logging after test completion
	require.NoError(t, service.Stop(context.Background()))
}

func TestZtdoService_Start_RejectsInvalidInferenceEndpointBeforeConnecting(t *testing.T) {
	cfg := testutil.NewTestConfig(t)
	cfg.Inference.Enabled = true
	cfg.Inference.OllamaEndpoint = "not-a-url"
	calls := 0
	service, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), func(string, string, *slog.Logger, *certs.TLSConfig) (pubsub.PubSubClient, error) {
		calls++
		return pubsubtest.NewMockOperatorPubSubClient(), nil
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if service.cancel != nil {
			service.cancel()
		}
	})

	err = service.Start(testutil.Context(t))
	require.ErrorIs(t, err, constants.ErrInferenceEndpointInvalid)
	assert.Zero(t, calls)
	assert.False(t, service.running)
}

func TestZtdoService_Start_FactoryFailureStopsBeforeDependents(t *testing.T) {
	factoryErr := errors.New("pub/sub client factory unavailable")
	calls := 0
	service, err := NewZtdoService(testutil.NewTestConfig(t), testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), newTestFileSvc(t), newTestCanonicalDB(t), func(string, string, *slog.Logger, *certs.TLSConfig) (pubsub.PubSubClient, error) {
		calls++
		return nil, factoryErr
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		if service.cancel != nil {
			service.cancel()
		}
	})

	err = service.Start(testutil.Context(t))
	require.ErrorIs(t, err, factoryErr)
	require.ErrorIs(t, err, constants.ErrInternal)
	assert.Equal(t, 1, calls)
	assert.Nil(t, service.pubSubClient)
	assert.Nil(t, service.execution)
	assert.False(t, service.running)
}

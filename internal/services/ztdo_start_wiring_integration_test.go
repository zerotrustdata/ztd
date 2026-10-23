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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/gateway"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newStartableZtdoService builds a ZtdoService carrying an enrolled in-memory
// identity, runtime tree, and in-memory keyring so Start exercises the real
// storage, governance, and pub/sub construction. mutate adjusts the config
// before the service is built. Whatever Start managed to open is released on
// cleanup, including when Start fails part-way.
func newStartableZtdoService(t *testing.T, mutate func(cfg *config.Config)) *ZtdoService {
	t.Helper()
	cfg := enrolledTestConfig(t)
	cfg.Endpoint = "127.0.0.1"
	cfg.PubSubURL = "wss://127.0.0.1:0"
	if mutate != nil {
		mutate(cfg)
	}

	require.NoError(t, paths.InitWithBase(cfg.WorkDir))
	fileSvc, err := fs.NewRuntimeFileService(cfg.WorkDir, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.EnforcePermissions())

	canonicalDB, err := gateway.OpenCanonicalDBService(testutil.NewTestLogger(), ks, fileSvc)
	require.NoError(t, err)
	service, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), newTestL2Trust(t), fileSvc, canonicalDB, newTestPubSubClientFactory())
	require.NoError(t, err)

	t.Cleanup(func() {
		// Start can fail after it has been handed the canonical database; force
		// the service into the running state so Stop releases it.
		service.mu.Lock()
		service.running = true
		service.mu.Unlock()
		_ = service.Stop(context.Background())
	})
	return service
}

func startWithTimeout(t *testing.T, service *ZtdoService) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return service.Start(ctx)
}

func fakeOllamaWithModels(t *testing.T, tagsJSON string, status int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tagsJSON))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestZtdoService_Start_InitializesRequiredFileLedger(t *testing.T) {
	service := newStartableZtdoService(t, nil)

	require.NoError(t, startWithTimeout(t, service))

	assert.True(t, service.running)
	assert.NotNil(t, service.evidence.Ledger, "the file ledger is a required Operator dependency")
	assert.NotNil(t, service.replayStore, "replay protection is mandatory in every mode")
	assert.NotNil(t, service.evidence.HistoryHandler)
}

func TestZtdoService_Start_ReceiptStateRootsUseLocalStore(t *testing.T) {
	service := newStartableZtdoService(t, nil)
	require.NoError(t, startWithTimeout(t, service))

	// No Gateway HTTP endpoint exists. Receipt evidence must remain available
	// locally once the Operator has its enrolled identity.
	provider := service.pubSubCommands.Actuator().StateRootProvider
	require.NotNil(t, provider)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	before, err := provider.GetCurrentStateRoot(ctx)
	require.NoError(t, err, "receipt state must not require a Gateway HTTP lookup")
	localBefore, err := service.gatewayDB.GetStateRootSvc().GetCurrentStateRoot(ctx)
	require.NoError(t, err)
	require.Equal(t, localBefore, before)
	require.NotEmpty(t, before)

	require.NoError(t, service.gatewayDB.GetKVStore().KVSet(ctx, "receipt-local-state", "changed", 0))
	after, err := provider.GetCurrentStateRoot(ctx)
	require.NoError(t, err)
	localAfter, err := service.gatewayDB.GetStateRootSvc().GetCurrentStateRoot(ctx)
	require.NoError(t, err)
	require.Equal(t, localAfter, after)
	require.NotEqual(t, before, after, "receipt roots must reflect changes in this runtime")
}

func TestZtdoService_StartReportsSubscribedSessionToDeploymentObserver(t *testing.T) {
	service := newStartableZtdoService(t, nil)
	subscribed := make(chan string, 1)
	require.NoError(t, service.SetCommandSubscriptionObserver(func(_ context.Context, sessionID string, connected bool) error {
		if connected {
			select {
			case subscribed <- sessionID:
			default:
			}
		}
		return nil
	}))
	require.NoError(t, startWithTimeout(t, service))
	select {
	case sessionID := <-subscribed:
		require.Equal(t, "test-sess-1", sessionID)
	case <-time.After(5 * time.Second):
		t.Fatal("deployment observer did not receive the subscribed session")
	}
	require.ErrorIs(t, service.SetCommandSubscriptionObserver(nil), constants.ErrServiceUnavailable)
}

func TestZtdoService_Start_ExternalShutdownRequestCancelsServiceContext(t *testing.T) {
	service := newStartableZtdoService(t, nil)
	require.NoError(t, startWithTimeout(t, service))
	require.NoError(t, service.ctx.Err(), "the service context is live after start")

	service.pubSubCommands.ShutdownChan <- "remote shutdown requested"

	select {
	case <-service.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("an external shutdown request must cancel the service context")
	}
}

func TestZtdoService_Start_SecondStartIsRejectedAndStopIsIdempotent(t *testing.T) {
	service := newStartableZtdoService(t, nil)
	require.NoError(t, startWithTimeout(t, service))

	err := startWithTimeout(t, service)

	require.ErrorIs(t, err, constants.ErrServiceUnavailable)
	require.NoError(t, service.Stop(context.Background()))
	assert.False(t, service.running)
	require.NoError(t, service.Stop(context.Background()), "stopping an already-stopped service is a no-op")
}

func TestZtdoService_Start_InferenceProviderAvailabilityDoesNotGateStartup(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  func(t *testing.T) string
		wantErr   error
		wantReady bool
	}{
		{
			name: "provider reachable",
			endpoint: func(t *testing.T) string {
				t.Helper()
				return fakeOllamaWithModels(t, `{"models":[{"name":"qwen3:4b","digest":"sha256:aa"}]}`, http.StatusOK)
			},
			wantReady: true,
		},
		{
			name: "provider reachable with no models installed",
			endpoint: func(t *testing.T) string {
				t.Helper()
				return fakeOllamaWithModels(t, `{"models":[]}`, http.StatusOK)
			},
			wantReady: true,
		},
		{
			name: "provider unavailable",
			endpoint: func(t *testing.T) string {
				t.Helper()
				return fakeOllamaWithModels(t, "", http.StatusServiceUnavailable)
			},
			wantReady: true,
		},
		{
			name:      "provider connection refused",
			endpoint:  func(*testing.T) string { return "http://127.0.0.1:1" },
			wantReady: true,
		},
		{
			name:      "provider returns malformed status",
			endpoint:  func(t *testing.T) string { t.Helper(); return fakeOllamaWithModels(t, "not JSON", http.StatusOK) },
			wantReady: true,
		},
		{
			name:     "provider endpoint is invalid",
			endpoint: func(*testing.T) string { return "not-a-url" },
			wantErr:  constants.ErrInferenceEndpointInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := tt.endpoint(t)
			service := newStartableZtdoService(t, func(cfg *config.Config) {
				cfg.Inference = config.InferenceConfig{Enabled: true, OllamaEndpoint: endpoint}
			})

			err := startWithTimeout(t, service)

			if tt.wantReady {
				require.NoError(t, err)
				assert.True(t, service.running)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
			assert.Contains(t, err.Error(), "ztdo:", "startup must fail closed with a ztdo-attributed error")
			assert.False(t, service.running)
			assert.Nil(t, service.pubSubCommands, "invalid local configuration must not start a command service")
		})
	}
}

// Startup must not spend a provider request timeout probing a slow/offline daemon.
func TestZtdoService_Start_InferenceDoesNotProbeProvider(t *testing.T) {
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	defer provider.Close()
	service := newStartableZtdoService(t, func(cfg *config.Config) {
		cfg.Inference = config.InferenceConfig{Enabled: true, OllamaEndpoint: provider.URL}
	})
	require.NoError(t, startWithTimeout(t, service))
	assert.True(t, service.running)
	assert.NotNil(t, service.pubSubCommands)
	assert.Zero(t, requests.Load(), "provider traffic starts only when requested")
}

func TestZtdoService_Start_EnablesProviderBoundaryObserverAndProvenanceOperator(t *testing.T) {
	storageRoot := testutil.TempDir(t)
	service := newStartableZtdoService(t, func(cfg *config.Config) {
		cfg.ProviderBoundaryObserver = config.ProviderBoundaryObserverConfig{Enabled: true, ObserverID: "observer-under-test"}
		cfg.ProvenanceOperator = config.ProvenanceOperatorConfig{Enabled: true, OperatorID: "provenance-under-test", ModelStorageRoot: storageRoot}
	})

	require.NoError(t, startWithTimeout(t, service))

	assert.True(t, service.running)
	assert.NotNil(t, service.pubSubCommands)
}

func TestZtdoService_Start_ProvenanceOperatorRequiresModelStorageRoot(t *testing.T) {
	service := newStartableZtdoService(t, func(cfg *config.Config) {
		cfg.ProvenanceOperator = config.ProvenanceOperatorConfig{Enabled: true, OperatorID: "provenance-under-test"}
	})

	err := startWithTimeout(t, service)

	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	assert.Contains(t, err.Error(), "model provenance attestor")
	assert.False(t, service.running)
}

// TestOperatorRuntimeRoot_OneDatabaseAndLedger verifies INV-STOR-ONE-01 on a
// started outbound Operator: its runtime data directory holds only ztd.db
// (plus SQLite sidecars) and the file ledger, with replay nonces, suspended
// transactions, and execution events all living in that one database.
func TestOperatorRuntimeRoot_OneDatabaseAndLedger(t *testing.T) {
	service := newStartableZtdoService(t, nil)
	require.NoError(t, startWithTimeout(t, service))

	testutil.AssertOneDatabaseAndLedger(t, paths.Infra.DataDir)
}

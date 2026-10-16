// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func setupRunEnv(t *testing.T) *runEnv {
	t.Helper()
	root := testutil.TempDir(t)
	writeTestFrozenInventory(t, root, testQwenVariant())

	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: testRunOperators()})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeWitnessPreflightResponse(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	paths := config.DefaultPathsConfig()
	paths.Host = server.URL
	fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	keyring := keystoretest.NewMemoryKeyring()
	cfg := &config.Config{ProjectRoot: root, RuntimeDir: fileSvc.Resolve(""), Paths: &paths, Keyring: keyring}
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	control := newFakeProcessControl()
	fixedNow := time.Unix(1789657337, 0).UTC()
	deps := nativeEvalDeps{
		configLoader: func(string) (*config.Config, error) { return cfg, nil },
		fileSvcFactory: func(string, *slog.Logger) (fs.RuntimeFileService, error) {
			files, err := fs.NewRuntimeFileService(root, slog.Default())
			if err != nil {
				return nil, err
			}
			return evaluation.NewEvidenceFileService(files, func(keys fs.RuntimeFileService) (evaluation.EvidenceVault, error) {
				keyStore, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, keys)
				if err != nil {
					return nil, err
				}
				return keyStore.OpenVault()
			})
		},
		createRuntimeTree: func(context.Context, fs.RuntimeFileService) error { return nil },
		clientFactory:     harnessclient.New,
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: "user-1", CLISessionID: "cli-1", OperatorID: "operator-1"}, nil
		},
		bindClientFactory: func(*config.Config) evalBindClient { return &fakeBindClient{bound: []string{testDataSession}} },
		formationRunnerFactory: func(context.Context, campaignFormationRunnerInputs) (evaluation.CampaignFormationRunner, error) {
			harness, err := evaluation.NewFormationHarness(func() time.Time { return fixedNow }, func(prefix string) string { return prefix + "-attempt" })
			if err != nil {
				return nil, err
			}
			return &harnessFormationRunner{harness: harness}, nil
		},
		runControl: testRunControl(control),
		now:        func() time.Time { return fixedNow },
		newID:      func() (string, error) { return "test-id", nil },
	}

	cmd := cmdtest.SilentCobraCommand()
	cmd.SetContext(testVersionContext())
	cmd.Flags().String("project-root", root, "")
	return &runEnv{root: root, deps: deps, cmd: cmd, control: control}
}

// harnessFormationRunner runs each bound formation on the deterministic
// in-process harness instead of live governed inference. The harness has no
// provider boundary to witness, so its roles carry no observer or provenance
// windows; witness coverage is what the Gateway fake persists.
type harnessFormationRunner struct {
	harness *evaluation.FormationHarness
}

func (r *harnessFormationRunner) RunHeterogeneousFormation(ctx context.Context, binding evaluation.FormationBindingRequest, _ evaluation.FormationRunContext, initialState []byte) (*evaluation.FormationRunResult, error) {
	formation, err := evaluation.ResolveFormationBinding(binding)
	if err != nil {
		return nil, err
	}
	result, err := r.harness.RunBoundFormation(ctx, formation, initialState)
	if err != nil {
		return nil, err
	}
	for i := range result.Roles {
		result.Roles[i].ObserverEvidence = nil
		result.Roles[i].ProvenanceEvidence = nil
	}
	return result, nil
}

// enableWitnessGateway marks the Gateway healthy and enrolls CLI credentials so
// runs can preflight provider observation delivery.
func (e *runEnv) enableWitnessGateway(t *testing.T) {
	t.Helper()
	gwremote.WithGatewayHealthCheck(t, true)
	writeGatewayCredentials(t, e)
	e.startGatewayFake(t, true)
}

// withPublishGateway fakes the Gateway public feed and the public mirror.
func (e *runEnv) withPublishGateway(t *testing.T) {
	t.Helper()
	writeGatewayCredentials(t, e)
	e.startGatewayFake(t, false)
}

// withExecuteGateway is withPublishGateway plus operator listing and witness
// preflight, which execution needs.
func (e *runEnv) withExecuteGateway(t *testing.T) {
	t.Helper()
	writeGatewayCredentials(t, e)
	e.startGatewayFake(t, true)
}

func (e *runEnv) startGatewayFake(t *testing.T, executing bool) {
	t.Helper()
	mirrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"snapshot":{"high_water_sequence":0},"recent_projections":[]}`))
	}))
	originalBootstrap := gwremote.PublicMirrorBootstrapURL
	gwremote.PublicMirrorBootstrapURL = mirrorServer.URL

	operators, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: testRunOperators()})
	require.NoError(t, err)
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if executing && writeWitnessPreflightResponse(w, r) {
			return
		}
		if writePublicationProofResponse(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(operators)
		case executing && r.Method == http.MethodPost && r.URL.Path == constants.APIPaths.OperatorsCommands:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"transaction_id":"tx-simulated-file"}`))
		case executing && r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.AuditReceipts:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"transactionId":"tx-simulated-file","finalPersistenceAttestation":{"transactionId":"tx-simulated-file"}}`))
		case r.URL.Path == constants.APIPaths.PublicFeedSnapshot:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"high_water_sequence":0}`))
		case r.URL.Path == constants.APIPaths.PublicFeedBatches:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"high_water_sequence":1}`))
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "publication-state"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "publication-state"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schema_version":"eval-campaign-publication-state/v1","run_id":"run","published_idempotency_keys":[],"last_published_sequence":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	config.SetEndpointOverride(gateway.URL)
	t.Cleanup(func() {
		gateway.Close()
		mirrorServer.Close()
		gwremote.PublicMirrorBootstrapURL = originalBootstrap
		config.SetEndpointOverride("")
	})
}

// firstQueuedAssignment is the assignment a run executes next.
func (e *runEnv) firstQueuedAssignment(t *testing.T, runID string) *evalv1.EvaluationAssignment {
	t.Helper()
	controller := evaluation.NewCampaignController(e.store(t), nil, e.deps.now, adaptNewID(e.deps.newID))
	assignment, ok, err := controller.ResumeNextAssignment(context.Background(), runID)
	require.NoError(t, err)
	require.True(t, ok)
	return assignment
}

func TestRunsEnv_SharedRuntimeEmbeddedOperatorTopology(t *testing.T) {
	root := testutil.TempDir(t)
	embeddedOp := &operatorv1.OperatorDocument{
		Id:                "embedded-op",
		OperatorSessionId: "embedded-sess-1",
		Status:            string(constants.OperatorStatusActive),
		OperatorType:      string(constants.OperatorTypeEmbedded),
		RuntimeConfig: &operatorv1.OperatorRuntimeConfig{
			Capabilities: []string{
				string(constants.OperatorCapabilityData),
				string(constants.OperatorCapabilityInference),
			},
			InferenceOllamaEndpoint: "http://provider.example:11434",
		},
		LatestHeartbeatSnapshot: &operatorv1.HeartbeatResult{
			Environment: &operatorv1.EnvironmentDetails{Pwd: testDataOperatorWorkingDirectory},
		},
		ActuatorPublicKey: testDataOperatorActuatorKey,
		ActuatorKeyId:     testDataOperatorActuatorKey,
	}

	body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{embeddedOp}})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	paths := config.DefaultPathsConfig()
	paths.Host = server.URL
	fileSvc, err := fs.NewRuntimeFileService(root, slog.Default())
	require.NoError(t, err)
	cfg := &config.Config{ProjectRoot: root, RuntimeDir: fileSvc.Resolve(""), Paths: &paths}
	deps := nativeEvalDeps{
		configLoader:   func(string) (*config.Config, error) { return cfg, nil },
		fileSvcFactory: func(string, *slog.Logger) (fs.RuntimeFileService, error) { return fileSvc, nil },
		clientFactory:  harnessclient.New,
		authLoader: func(fs.RuntimeFileService, *config.Config) (*auth.ClientAuthContext, error) {
			return &auth.ClientAuthContext{UserID: "user-1", CLISessionID: "cli-1"}, nil
		},
	}
	cmd := cmdtest.SilentCobraCommand()
	cmd.SetContext(testVersionContext())
	cmd.Flags().String("project-root", root, "")

	topology, err := resolveOperatorTopology(cmd, deps)
	require.NoError(t, err)
	require.True(t, topology.SharedRuntime(), "embedded operator holding data and inference must resolve as a shared runtime")
	assert.Equal(t, "embedded-op", topology.Inference.OperatorID)
	assert.Equal(t, "embedded-op", topology.Data.OperatorID)
	assert.Equal(t, "embedded-sess-1", topology.Inference.OperatorSessionID)
	assert.Equal(t, "embedded-sess-1", topology.Data.OperatorSessionID)

	anchored, err := topology.WithAnchors([]*operatorv1.OperatorDocument{embeddedOp})
	require.NoError(t, err)
	require.NotNil(t, anchored.Inference.Anchor)
	require.NotNil(t, anchored.Data.Anchor)
	assert.Equal(t, anchored.Inference.Anchor, anchored.Data.Anchor, "shared runtime pins one actuator anchor")
	assert.NoError(t, anchored.ValidateAnchored())
}

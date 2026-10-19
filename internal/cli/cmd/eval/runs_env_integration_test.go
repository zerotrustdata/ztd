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
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func setupRunEnv(t *testing.T) *runEnv {
	return setupRunEnvWithOperatorSource(t, testRunOperators)
}

func setupRunEnvWithOperatorSource(t *testing.T, operatorSource func() []*operatorv1.OperatorDocument) *runEnv {
	return setupRunEnvWithGateway(t, operatorSource, constants.PostureDoctrine)
}

// setupRunEnvWithGateway wires a fake Gateway that lists operatorSource and
// reports posture from its health endpoint.
func setupRunEnvWithGateway(t *testing.T, operatorSource func() []*operatorv1.OperatorDocument, posture constants.Posture) *runEnv {
	t.Helper()
	root := testutil.TempDir(t)
	writeTestFrozenInventory(t, root, testQwenVariant())

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeWitnessPreflightResponse(w, r) || writeGatewayPostureResponse(w, r, posture) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators {
			body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: operatorSource()})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
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
	// An enrolled CLI always has a trust bundle, and the Gateway client refuses
	// to start without the one its config names.
	_, bundleKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	bundlePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cmdtest.GenerateApproveTestCertDER(t, bundleKey)})
	require.NoError(t, fileSvc.WriteFile(context.Background(), cfg.DefaultTrustBundleRelPath(), bundlePEM, constants.PermFilePrivate))
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
		bindClientFactory:    func(*config.Config) evalBindClient { return &fakeBindClient{bound: []string{testDataSession}} },
		agentExecutorFactory: (&scriptedOperatorRuntimes{t: t, ledgers: map[string]*scriptedOperatorLedger{}}).newExecutor,
		runControl:           testRunControl(control),
		now:                  func() time.Time { return fixedNow },
		newID:                func() (string, error) { return "test-id", nil },
	}

	cmd := cmdtest.SilentCobraCommand()
	cmd.SetContext(testVersionContext())
	cmd.Flags().String("project-root", root, "")
	return &runEnv{root: root, deps: deps, cmd: cmd, control: control, stream: newFakeRunStream(), operatorSource: operatorSource}
}

// scriptedAgentAnswer is the final answer the scripted model gives on its first
// turn, without calling a tool.
const scriptedAgentAnswer = "The investigation is complete; no change is required."

// scriptedOperatorRuntimes stands in for the Inference and Data Operator
// runtimes behind the real campaign agent executor and command lane. Each
// runtime keeps a commitment ledger and signs receipts with the actuator key
// its operator document enrolls (testActuatorSigner), so the executor imports
// and the verifier re-checks the trace exactly as it would a live run. The
// scripted model answers every assignment in one turn without a tool call.
type scriptedOperatorRuntimes struct {
	t       *testing.T
	mu      sync.Mutex
	ledgers map[string]*scriptedOperatorLedger
}

// newExecutor is the env's agentExecutorFactory: the production executor and
// command lane over the scripted runtimes of the resolved topology.
func (r *scriptedOperatorRuntimes) newExecutor(in campaignAgentExecutorInputs) (evaluation.CampaignAssignmentExecutor, error) {
	inference, err := r.ledger(in.Topology.Inference)
	if err != nil {
		return nil, err
	}
	data, err := r.ledger(in.Topology.Data)
	if err != nil {
		return nil, err
	}
	persona := harnessclient.Persona{ID: campaignAgentPersonaID, CLISessionID: in.AuthContext.CLISessionID, UserID: in.AuthContext.UserID}
	return evaluation.NewCampaignAgentExecutor(&scriptedInference{ledger: inference}, evaluation.NewCommandLane(&scriptedLaneClient{ledger: data}, persona), &scriptedSentinel{ledger: data}, in.Store, in.Deps.now, adaptNewID(in.Deps.newID))
}

// ledger returns the runtime's ledger, signed by the actuator key its anchor
// pins. One runtime keeps one ledger across executors, as a real Operator does.
func (r *scriptedOperatorRuntimes) ledger(runtime evaluation.CampaignOperatorRuntime) (*scriptedOperatorLedger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ledger, ok := r.ledgers[runtime.OperatorID]; ok {
		return ledger, nil
	}
	var signer oracletest.Signer
	switch runtime.Anchor.GetKeyId() {
	case testDataOperatorActuatorKey:
		signer = testActuatorSigner(7)
	case testInferenceOperatorActuatorKey:
		signer = testActuatorSigner(8)
	default:
		return nil, fmt.Errorf("scripted runtime %s: no actuator key for anchor %q: %w", runtime.OperatorID, runtime.Anchor.GetKeyId(), constants.ErrConformanceAnchorMissing)
	}
	ledger := &scriptedOperatorLedger{t: r.t, signer: signer, operatorID: runtime.OperatorID, sessionID: runtime.OperatorSessionID, head: "h-0"}
	r.ledgers[runtime.OperatorID] = ledger
	return ledger, nil
}

// scriptedOperatorLedger is one Operator runtime's commitment ledger: each
// executed receipt links to the head before it.
type scriptedOperatorLedger struct {
	t          *testing.T
	mu         sync.Mutex
	signer     oracletest.Signer
	operatorID string
	sessionID  string
	head       string
	seq        int
}

// executed appends one commitment and returns its transaction ID and signed
// receipt.
func (l *scriptedOperatorLedger) executed(actionType string) (string, *operatorv1.ActionReceipt) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq++
	txID := fmt.Sprintf("tx-%s-%d", l.operatorID, l.seq)
	prior, hash := l.head, fmt.Sprintf("h-%d", l.seq)
	l.head = hash
	receipt := l.signer.Executed(l.t, oracletest.ReceiptOpts{
		TransactionID: txID, OperatorID: l.operatorID, SessionID: l.sessionID, ActionType: actionType,
		L2: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}, prior, hash)
	return txID, receipt
}

// scriptedInference is the Inference runtime: every turn is one governed
// inference that answers with scriptedAgentAnswer.
type scriptedInference struct {
	ledger *scriptedOperatorLedger
}

func (f *scriptedInference) DispatchInference(_ context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	txID, receipt := f.ledger.executed(string(constants.ActionTypeInference))
	result := oracletest.InferenceResult(f.ledger.t, req, []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: scriptedAgentAnswer}}})
	receipt.ResultSummary = result.GetResultDigest()
	f.ledger.signer.Resign(f.ledger.t, receipt)
	return &operatorv1.InferenceDispatchResponse{TransactionId: txID, Result: result, Receipt: receipt}, nil
}

// scriptedLaneClient is the Gateway under the command lane: each governed
// workspace write executes on the Data runtime and answers with its receipt.
type scriptedLaneClient struct {
	ledger *scriptedOperatorLedger
}

func (c *scriptedLaneClient) DispatchCommand(_ context.Context, _ harnessclient.Persona, request models.OperatorCommandRequest) (*harnessclient.GovernedOutcome, error) {
	if request.EventType != string(constants.EventOperatorFileEditRequested) {
		return nil, fmt.Errorf("scripted lane client: unscripted event %s", request.EventType)
	}
	txID, receipt := c.ledger.executed(string(constants.ActionTypeFileEdit))
	payload, err := proto.Marshal(&operatorv1.FileEditResult{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, FilePath: request.TargetResource, Operation: string(constants.FileOperationWrite)})
	if err != nil {
		return nil, err
	}
	return &harnessclient.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: receipt},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: txID, Receipt: receipt, ResultPayload: payload},
	}, nil
}

// scriptedSentinel reads the Data runtime's ledger head, as the harness ledger
// sentinel does around each attempt.
type scriptedSentinel struct {
	ledger *scriptedOperatorLedger
}

func (s *scriptedSentinel) Read(context.Context, cases.Executor, string) (*operatorv1.ActionReceipt, error) {
	_, receipt := s.ledger.executed(string(constants.ActionTypeFsRead))
	return receipt, nil
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

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if executing {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.SSEStream:
				e.stream.serve(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == constants.APIPaths.ObserveProducerStop:
				e.stream.acceptStop(w, r)
				return
			case r.Method == http.MethodPost && r.URL.Path == constants.APIPaths.ObserveProducerRunState:
				e.stream.acceptRunState(w, r)
				return
			}
		}
		if executing && writeWitnessPreflightResponse(w, r) {
			return
		}
		if writePublicationProofResponse(w, r) || writeGatewayHealthResponse(w, r) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == constants.APIPaths.Operators:
			// Listed per request, so a test that rotates a session mid-run is
			// served the rotated document.
			body, err := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: e.operatorSource()})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
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

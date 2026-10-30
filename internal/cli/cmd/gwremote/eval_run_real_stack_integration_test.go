// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	cliop "github.com/zerotrustdata/ztd/internal/cli/operator"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/drive"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	harnessclient "github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	harnessconfig "github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	evalRunPersonaID  = "ztd-eval-campaign"
	evalRunCampaignID = "smoke-campaign"
	evalRunScenarioID = "tool-arg-shell-command"
	evalRunModelTag   = "qwen3:4b"
	evalRunShellArgs  = `{"command":"cat","args":["status/health.txt"]}`
	evalRunDefaultRun = "run-real-stack"
)

// evalRunShellReplies scripts one answer-free shell turn for the provider: the
// model calls run_shell_command, then answers from the output.
func evalRunShellReplies() []fixtures.OllamaReply {
	return []fixtures.OllamaReply{
		{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(evalRunShellArgs)}}},
		{Content: "HEALTHY"},
	}
}

// evalRunInference forwards governed inference and Tribunal intents from the
// application identity to the production harness client, as the eval runner does.
type evalRunInference struct {
	client       *harnessclient.Client
	cliSessionID string
}

func (d evalRunInference) DispatchInference(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, error) {
	response, _, err := d.client.DispatchInference(ctx, req)
	return response, err
}

func (d evalRunInference) DispatchIntent(ctx context.Context, req *tribunalv1.TribunalIntentRequest) (*tribunalv1.TribunalIntentResult, error) {
	req.CliSessionId = d.cliSessionID
	result, _, err := d.client.DispatchIntent(ctx, req)
	return result, err
}

// evalRunOptions selects the posture, public mirror, provider replies and run ID
// of one real eval stack.
type evalRunOptions struct {
	posture      constants.Posture
	publicMirror bool
	replies      []fixtures.OllamaReply
	runID        string
}

// evalRunStack is a real eval stack (Gateway plus Data and Inference Operators)
// with the production campaign controller, executor and run store wired over it.
// cliFiles and cliConfig are the CLI runtime that gwremote's Gateway remotes read
// through; files is the evidence root the run store seals into.
type evalRunStack struct {
	stack      *fixtures.EvalStack
	cliFiles   fs.RuntimeFileService
	cliConfig  *cliconfig.Config
	files      fs.RuntimeFileService
	store      *evaluation.Store
	controller *evaluation.CampaignController
	req        evaluation.CampaignInitRequest
	binding    evaluation.CampaignExecutionBinding
	clock      func() time.Time
}

// newEvalRunStack boots the real stack and wires the campaign over it. It mirrors
// the production seams of newRealStackAgentRun in the evaluation package; nothing is
// scheduled until initialize is called.
func newEvalRunStack(t *testing.T, opts evalRunOptions) *evalRunStack {
	t.Helper()
	ctx := t.Context()
	stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{
		Posture:               opts.posture,
		Replies:               opts.replies,
		EnablePublicSpectator: opts.publicMirror,
		Provenance:            true,
	})

	logger := testutil.NewTestLogger()
	root := testutil.TempDir(t)
	keyring := keystoretest.NewMemoryKeyring()
	cfg, err := cliconfig.Load(root)
	require.NoError(t, err)
	cfg.Keyring = keyring
	fileSvc, err := fs.NewRuntimeFileService(root, logger)
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(ctx))
	keyStore, err := keystore.NewWithKeyringAndFS(logger, keyring, fileSvc)
	require.NoError(t, err)
	require.NoError(t, keyStore.Initialize())
	require.NoError(t, keyStore.InitVault())
	stack.Identity.InstallCLIRuntime(t, stack.Gateway, fileSvc, cfg)
	stack.EnrollEvalAppIdentity(t, fileSvc, cfg)
	evidenceFiles, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: keyring})(root, logger)
	require.NoError(t, err)

	authContext, err := auth.LoadClientAuthContext(fileSvc, cfg)
	require.NoError(t, err)
	cliCredentials := harnessconfig.Auth{ClientCert: authContext.ClientCert, ClientKey: authContext.ClientKey, Certificate: authContext.Certificate, CABundle: cfg.ResolvedTrustBundlePath()}
	gatewayClient, err := harnessclient.New(harnessconfig.Config{
		MTLSBaseURL: cfg.OperatorHTTPURL(), PublicBaseURL: cfg.OperatorDiscoveryURL(),
		Auth: cliCredentials, CLIAuth: cliCredentials, UseCLIConfig: true,
		UserID: authContext.UserID, CLISessionID: authContext.CLISessionID, OperatorSessionID: authContext.OperatorSessionID,
	})
	require.NoError(t, err)
	appCertificate, err := auth.LoadAppIdentity(fileSvc, cfg, constants.EvaluationAppName)
	require.NoError(t, err)
	appClient, err := harnessclient.New(harnessconfig.Config{
		MTLSBaseURL: cfg.OperatorHTTPURL(),
		Auth:        harnessconfig.Auth{Certificate: appCertificate, CABundle: cfg.ResolvedTrustBundlePath()},
		UserID:      authContext.UserID,
	})
	require.NoError(t, err)

	inferenceWorkload, err := stack.Inference.Workload()
	require.NoError(t, err)
	dataWorkload, err := stack.Data.Workload()
	require.NoError(t, err)
	operators, _, err := gatewayClient.ListOperators(ctx)
	require.NoError(t, err)
	topology, err := evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, inferenceWorkload.OperatorSessionID, dataWorkload.OperatorID)
	require.NoError(t, err)
	topology, err = topology.WithAnchors(operators)
	require.NoError(t, err)

	catalog, artifacts, err := evaluation.LoadScenarioCatalog()
	require.NoError(t, err)
	// Keep the run to the one scenario, scheduled for the primary role only, so
	// one assignment is scheduled and the run completes with one execution.
	var target *evalv1.EvaluationScenarioDefinition
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == evalRunScenarioID {
			target = scenario
			break
		}
	}
	require.NotNil(t, target, "scenario %s must exist in catalog", evalRunScenarioID)
	target.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Personas:      catalog.GetPersonas(),
		Scenarios:     []*evalv1.EvaluationScenarioDefinition{target},
	}
	truncated.CatalogDigest, err = evaluation.ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)

	// The variant declares the digest of the model store the stack's Provenance
	// Operator attests, so the attestation window is a digest match.
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ProviderClass: "ollama", ServedModelTag: evalRunModelTag, ModelDigest: fixtures.ScriptedModelDigest(t, evalRunModelTag)}
	inventory, err := evaluation.MaterializeModelRegistry(evalRunCampaignID, []*evalv1.ModelVariant{variant})
	require.NoError(t, err)
	runID := opts.runID
	if runID == "" {
		runID = evalRunDefaultRun
	}
	posture, err := cases.WirePosture(stack.Posture)
	require.NoError(t, err)
	governance, err := evaluation.NewRunGovernance(posture, nil)
	require.NoError(t, err)
	req := evaluation.CampaignInitRequest{
		Platform:                   evaluation.PlatformIdentity{Release: "v2.3.0", SourceRevision: "0123abc"},
		Governance:                 governance,
		CampaignID:                 evalRunCampaignID,
		RunID:                      runID,
		Catalog:                    truncated,
		Inventory:                  inventory,
		ScenarioArtifacts:          artifacts,
		RepetitionCount:            1,
		InferenceOperatorSessionID: inferenceWorkload.OperatorSessionID,
		DataOperatorSessionID:      dataWorkload.OperatorSessionID,
		Judge:                      &evalv1.ModelVariant{VariantId: "gemma4-e4b", ProviderClass: "ollama", ServedModelTag: evaluation.DefaultJudgeServedModelTag, ModelDigest: fixtures.ScriptedModelDigest(t, evaluation.DefaultJudgeServedModelTag)},
	}

	clock := func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	seq := 0
	newID := func(prefix string) string {
		seq++
		return fmt.Sprintf("%s-%d", prefix, seq)
	}
	store := evaluation.NewStore(evidenceFiles)
	persona := harnessclient.Persona{ID: evalRunPersonaID, CLISessionID: authContext.CLISessionID, UserID: authContext.UserID}
	sentinel, err := drive.NewSentinel(gatewayClient, persona, map[cases.Executor]cliop.Target{
		cases.ExecutorOutbound: {OperatorID: dataWorkload.OperatorID, OperatorSessionID: dataWorkload.OperatorSessionID},
	})
	require.NoError(t, err)
	inference := evalRunInference{client: appClient, cliSessionID: persona.CLISessionID}
	residencyDispatcher, err := evaluation.NewGovernedOllamaModelCommandDispatcher(gatewayClient, persona)
	require.NoError(t, err)
	residencySeq := 0
	residency, err := evaluation.NewProviderModelResidency(residencyDispatcher, inferenceWorkload.OperatorSessionID, func(prefix string) string {
		residencySeq++
		return fmt.Sprintf("%s-residency-%d", prefix, residencySeq)
	})
	require.NoError(t, err)
	executor, err := evaluation.NewCampaignAgentExecutor(
		inference, residency, evaluation.NewCommandLane(gatewayClient, persona), sentinel, store, clock, newID)
	require.NoError(t, err)

	return &evalRunStack{
		stack:      stack,
		cliFiles:   fileSvc,
		cliConfig:  cfg,
		files:      evidenceFiles,
		store:      store,
		controller: evaluation.NewCampaignController(store, executor, clock, newID),
		req:        req,
		binding: evaluation.CampaignExecutionBinding{
			Topology:            topology,
			ModelRegistryDigest: req.Inventory.RegistryDigest,
			ModelRegistry:       req.Inventory.InferenceVariants,
		},
		clock: clock,
	}
}

// initialize creates the run and schedules its one assignment, which stays queued
// until executeOnce runs it.
func (e *evalRunStack) initialize(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	_, err := e.controller.InitializeCampaign(ctx, e.req)
	require.NoError(t, err)
	scheduled, err := e.controller.ScheduleRun(ctx, e.req.RunID)
	require.NoError(t, err)
	require.Equal(t, 1, scheduled, "the primary-role scenario schedules one assignment")
}

// executeOnce runs the next scheduled assignment through the real Data and
// Inference Operators and returns its completed result.
func (e *evalRunStack) executeOnce(t *testing.T) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	res, ok, err := e.controller.ExecuteNextAssignment(t.Context(), e.req.RunID, e.binding, e.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	return res
}

// verifyAndSave verifies the completed run from its sealed evidence and persists
// the passing report, as the run lifecycle does before publication.
func (e *evalRunStack) verifyAndSave(t *testing.T) *evalv1.EvaluationVerificationReport {
	t.Helper()
	ctx := t.Context()
	report, err := evaluation.NewCampaignRunVerifier(e.clock).VerifyRun(ctx, e.store, e.req.RunID, e.req.Catalog, e.req.ScenarioArtifacts)
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus())
	require.NoError(t, e.store.SaveCampaignVerification(ctx, e.req.RunID, report))
	return report
}

// newEvalRunPublication wires run publication over the Gateway exporter and the
// publication state from the CLI runtime, with the run store from the evidence root
// and probe reading the mirror. The optional assignment-audit proof publisher is not
// attached: the Gateway builds those proofs from per-assignment event records that
// this stack never stores, so ingest fails with "no retained event records". Dataset
// publication, which every mirror assertion reads, does not depend on it.
func newEvalRunPublication(t *testing.T, e *evalRunStack, probe evaluation.CampaignMirrorProbe) *evaluation.CampaignPublicationCoordinator {
	t.Helper()
	ctx := t.Context()
	exporter, err := NewCampaignFeedExporter(ctx, e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	state, err := NewCampaignPublicationStateStore(e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	return evaluation.NewCampaignPublicationCoordinator(e.store, e.files, state, exporter).
		WithMirrorProbe(probe)
}

// newEvalWitnessCapture builds the production witness capture over the run store.
func newEvalWitnessCapture(t *testing.T, e *evalRunStack) *evaluation.CampaignWitnessCapture {
	t.Helper()
	capture, err := NewCampaignWitnessCapture(e.cliFiles, e.store, e.cliConfig)
	require.NoError(t, err)
	return capture
}

// saveRunAssignmentNaming persists one completed assignment whose scored inferences
// name the given provider attempts. The attempts are not required to have run: the
// Gateway answers for them with not-found, which is how an attempt that never ran
// looks to the witness capture.
func saveRunAssignmentNaming(t *testing.T, store *evaluation.Store, runID, assignmentID string, attemptIDs ...string) {
	t.Helper()
	ctx := t.Context()
	require.NoError(t, store.SaveAssignment(ctx, &evalv1.EvaluationAssignment{
		SchemaVersion:   evaluation.CampaignSchemaVersion,
		AssignmentId:    assignmentID,
		RunId:           runID,
		CampaignId:      evalRunCampaignID,
		ScenarioId:      evalRunScenarioID,
		Repetition:      1,
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			},
		},
	}))
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion: evaluation.CampaignSchemaVersion,
		AssignmentId:  assignmentID,
		RunId:         runID,
		CampaignId:    evalRunCampaignID,
		Lane:          evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
	}
	for _, id := range attemptIDs {
		result.ModelInferences = append(result.ModelInferences, &evalv1.ModelInferenceRecord{ProviderAttemptId: id})
	}
	digest, err := evaluation.ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	require.NoError(t, store.SaveAssignmentResult(ctx, result))
}

// setPublicMirrorOrigin points the gwremote mirror readers at origin for the
// rest of t.
func setPublicMirrorOrigin(t *testing.T, origin string) {
	t.Helper()
	previous := PublicMirrorOrigin
	PublicMirrorOrigin = origin
	t.Cleanup(func() { PublicMirrorOrigin = previous })
}

// pointMirrorAt points the gwremote mirror readers at one Gateway's public mirror.
func pointMirrorAt(t *testing.T, gateway *fixtures.GatewayFixture) {
	t.Helper()
	setPublicMirrorOrigin(t, gateway.PublicMirrorOrigin)
}

// mirrorHighWaterSequence reads the high-water sequence that one Gateway's public
// mirror serves. Every publication to the mirror advances it.
func mirrorHighWaterSequence(t *testing.T, gateway *fixtures.GatewayFixture) int64 {
	t.Helper()
	pointMirrorAt(t, gateway)
	bootstrap, err := FetchPublicMirrorBootstrap(t.Context())
	require.NoError(t, err)
	return bootstrap.Snapshot.HighWaterSequence
}

// hungHTTPListener accepts TCP connections and never answers them, so an HTTP
// request to it blocks until the caller's deadline ends it. The listener and every
// accepted connection are closed when t ends.
func hungHTTPListener(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	var (
		mu      sync.Mutex
		conns   []net.Conn
		accepts sync.WaitGroup
	)
	accepts.Add(1)
	go func() {
		defer accepts.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		accepts.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return "http://" + listener.Addr().String()
}

// closedLoopbackURL returns a loopback Gateway URL with nothing listening on it.
func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	port, reservation, err := netutil.ReserveTCPPort()
	require.NoError(t, err)
	require.NoError(t, reservation.Close())
	return network.LocalhostHTTPURL(port)
}

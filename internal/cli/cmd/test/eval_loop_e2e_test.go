// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package testcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/uuid"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// This Tier 3 test starts actual OS processes. All Gateway and Operator code,
// identities, enrollment, transport, execution and event receipts are production code.
func TestEvalLoopCrossProcess(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			r := newEvalLoopRun(t, "eval-loop-", posture, 6*time.Minute)
			ctx, cancel := context.WithTimeout(t.Context(), r.opts.Timeout)
			defer cancel()
			replies := evalLoopReplies()
			provider := fixtures.NewScriptedOllama(t, replies)
			servedDigest, ok := provider.ServedDigest(evalLoopScriptedModel)
			require.True(t, ok, "scripted provider must serve %s", evalLoopScriptedModel)
			st := &evalLoopState{}
			phases := evalLoopPhases(t, r, provider.Endpoint(), st, func(ctx context.Context) error {
				return r.executeEvalLoop(ctx, servedDigest, st.inferenceRoot, st.inferenceID, st.inferenceSession)
			})
			require.NoError(t, runEvalLoopPhases(ctx, t, r, phases))
			assert.Equal(t, len(replies), provider.ChatCount(), "every scripted chat turn must be consumed")
			require.NotNil(t, st.approvals)
			assert.NotZero(t, st.approvals.Count(), "separate CLI processes must provide L3 approval")
			assert.Empty(t, st.approvals.Errors())
			if st.consensus != nil {
				assert.NotZero(t, st.consensus.DeliberationCount(), "the production deliberator must call the bound application")
			}
		})
	}
}

// newEvalLoopRun returns the release-smoke run for one posture. Its root is
// prefix+posture+id under .ztd/operators. execute claims the root when the run
// starts, so nothing exists on disk before then.
func newEvalLoopRun(t *testing.T, prefix string, posture constants.Posture, timeout time.Duration) *releaseSmokeRun {
	t.Helper()
	files, repoRoot := evalLoopRepoFiles(t)
	binary := filepath.Join(repoRoot, "ztd")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	_, err := os.Stat(binary)
	require.NoError(t, err, "run make build before test eval-loop")
	id, err := uuid.NewString()
	require.NoError(t, err)
	// Keep the full UUID entropy without separators: the root also contains
	// the isolated key agent socket, whose Unix path length is bounded.
	id = strings.ReplaceAll(id, "-", "")
	root := files.Resolve(evalLoopRunRelPath(prefix + string(posture) + "-" + id))
	t.Logf("eval loop evidence: %s", root)
	return newReleaseSmokeRun(releaseSmokeOptions{RunDir: root, ZtdBin: binary, Timeout: timeout, Posture: posture})
}

// evalLoopRepoFiles returns the repository's runtime file service and the
// repository root, found by walking up to go.mod.
func evalLoopRepoFiles(t *testing.T) (fs.RuntimeFileService, string) {
	t.Helper()
	repoRoot, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(repoRoot)
		require.NotEqual(t, parent, repoRoot)
		repoRoot = parent
	}
	files, err := fs.NewRuntimeFileService(repoRoot, slog.Default())
	require.NoError(t, err)
	return files, repoRoot
}

// evalLoopRunRelPath is the runtime-relative path of the eval loop run named name.
func evalLoopRunRelPath(name string) string {
	return filepath.Join(constants.OperatorsDirname, name)
}

// runEvalLoopPhases executes phases under ctx and returns execute's result.
// execute owns teardown once it returns. The emergency owner covers a fixture
// assertion that exits a phase's goroutine before execute can return. Cleanups
// the caller registers before this call run after the stack has stopped.
func runEvalLoopPhases(ctx context.Context, t *testing.T, r *releaseSmokeRun, phases []smokePhase) error {
	finished := false
	t.Cleanup(func() {
		if !finished {
			cleanup, stop := context.WithTimeout(context.Background(), releaseSmokeCleanupTimeout)
			defer stop()
			assert.NoError(t, r.runCleanups(cleanup))
		}
	})
	err := r.execute(ctx, phases)
	finished = true
	return err
}

// evalLoopState is what one posture's stack hands to the loop's assertions: the
// approval and consensus owners, and the Inference Operator's identity.
type evalLoopState struct {
	approvals        *releaseSmokeCLIApprovalResponder
	consensus        *evalLoopConsensusApp
	inferenceRoot    string
	inferenceID      string
	inferenceSession string
}

// evalLoopPhases is the isolated stack both eval loops bring up for r's posture:
// Gateway, CLI owner, approvals, consensus under notary, a Data Operator, and an
// Inference Operator that reaches endpoint. loop runs as the final phase.
func evalLoopPhases(t *testing.T, r *releaseSmokeRun, endpoint string, st *evalLoopState, loop func(context.Context) error) []smokePhase {
	return []smokePhase{
		{"prepare", r.prepare},
		{"start isolated Gateway", r.startGateway},
		{"enroll CLI owner", r.loginCLI},
		{"subscribe CLI approvals", func(context.Context) error {
			st.approvals = startReleaseSmokeCLIApprovalResponder(t, r)
			r.addCleanup("CLI approval responder", func(context.Context) error { st.approvals.Stop(); return nil })
			return nil
		}},
		{"bind real consensus application", func(context.Context) error {
			if r.opts.Posture == constants.PostureNotary {
				st.consensus = provisionEvalLoopConsensus(t, r)
			}
			return nil
		}},
		{"deploy dedicated Data Operator", r.deployWorker},
		{"deploy dedicated Inference Operator", func(ctx context.Context) error {
			var err error
			st.inferenceRoot = filepath.Join(r.opts.RunDir, "inference")
			st.inferenceID, st.inferenceSession, err = r.deployEvalInference(ctx, st.inferenceRoot, endpoint)
			return err
		}},
		{"execute and verify eval through CLI", loop},
	}
}

func (r *releaseSmokeRun) deployEvalInference(ctx context.Context, root, endpoint string) (string, string, error) {
	cfg, err := cliconfig.Load(r.gwRoot)
	if err != nil {
		return "", "", err
	}
	trust := cfg.ResolvedTrustBundlePath()
	out, err := r.ztd(ctx, "deploy-inference", r.gwRoot, "-e", "127.0.0.1", "operator", "deploy", "--local", "--dest-dir", root, "--background", "--approve", "--capabilities", "inference,observer", "--inference-ollama-endpoint", endpoint, "--trust-bundle", trust, "--gateway-http-port", strconv.Itoa(r.ports.http), "--gateway-https-port", strconv.Itoa(r.ports.https))
	if err != nil {
		return "", "", err
	}
	session, err := parseSmokeDeploySession(out, root)
	if err != nil {
		return "", "", err
	}
	r.addCleanup("Inference Operator", func(ctx context.Context) error {
		_, err := r.ztdGateway(ctx, "inference-stop", "operator", "stop", session, "--grace", "5s", "--reason", "eval loop cleanup")
		return err
	})
	operators, _, err := r.client.ListOperators(ctx)
	if err != nil {
		return "", "", err
	}
	for _, op := range operators {
		if op.GetOperatorSessionId() == session && op.GetStatus() == string(constants.OperatorStatusActive) {
			return op.GetId(), session, nil
		}
	}
	return "", "", fmt.Errorf("inference session %s: %w", session, constants.ErrNotFound)
}

// executeEvalLoop freezes the scripted provider's inventory through the governed
// CLI, requires the frozen digest to be the one the provider serves, then runs
// the campaigns and verifies their evidence.
func (r *releaseSmokeRun) executeEvalLoop(ctx context.Context, servedDigest, inferenceRoot, inferenceID, inferenceSession string) error {
	files, store, err := r.newEvalLoopStore()
	if err != nil {
		return err
	}
	digest, err := r.freezeEvalModel(ctx, files, evalLoopScriptedModel)
	if err != nil {
		return err
	}
	if digest != servedDigest {
		return fmt.Errorf("frozen %s digest %s, want the scripted provider's %s: %w", evalLoopScriptedModel, digest, servedDigest, constants.ErrValidationFailed)
	}
	campaigns, err := evalLoopCampaigns()
	if err != nil {
		return err
	}
	runs, err := r.runEvalCampaigns(ctx, files, store, campaigns, inferenceRoot, inferenceID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := r.verifyEvalRun(ctx, run, inferenceRoot, inferenceID, inferenceSession); err != nil {
			return fmt.Errorf("campaign %s: %w", run.campaign.campaignID, err)
		}
	}
	return nil
}

// freezeEvalModel freezes the provider's inventory through the governed CLI and
// returns the frozen digest of model. The freeze replaces the Gateway's registry,
// which the campaign then binds, so the digest is the provider's own.
func (r *releaseSmokeRun) freezeEvalModel(ctx context.Context, files fs.RuntimeFileService, model string) (string, error) {
	if _, err := r.ztdGateway(ctx, "freeze-eval-models", "eval", "models", "freeze"); err != nil {
		return "", err
	}
	freeze, err := evaluation.LoadModelInventoryFreezeFromRuntime(ctx, files, constants.EvaluationModelInventoryPath)
	if err != nil {
		return "", err
	}
	for _, variant := range freeze.Variants {
		if variant.GetServedModelTag() != model {
			continue
		}
		if variant.GetModelDigest() == "" {
			return "", fmt.Errorf("frozen model %s has no digest: %w", model, constants.ErrMissingRequiredField)
		}
		return variant.GetModelDigest(), nil
	}
	return "", fmt.Errorf("provider inventory has no model %s: %w", model, constants.ErrNotFound)
}

// newEvalLoopStore returns the Gateway runtime files and the evaluation store
// over the Gateway's evidence.
func (r *releaseSmokeRun) newEvalLoopStore() (fs.RuntimeFileService, *evaluation.Store, error) {
	files, err := fs.NewRuntimeFileService(r.gwRoot, slog.Default())
	if err != nil {
		return nil, nil, err
	}
	evidence, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: r.agent.Keyring(files)})(r.gwRoot, slog.Default())
	if err != nil {
		return nil, nil, err
	}
	return files, evaluation.NewStore(evidence), nil
}

// runEvalCampaigns runs each campaign to completion through its own CLI
// processes first. Anchors are bound afterwards, and every trace is verified
// against them by the caller.
func (r *releaseSmokeRun) runEvalCampaigns(ctx context.Context, files fs.RuntimeFileService, store *evaluation.Store, campaigns []evalLoopCampaign, inferenceRoot, inferenceID string) ([]evalLoopRun, error) {
	runs := make([]evalLoopRun, 0, len(campaigns))
	for _, campaign := range campaigns {
		run, err := r.runEvalCampaign(ctx, store, campaign)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := r.bindEvalAnchors(ctx, files, inferenceRoot, inferenceID); err != nil {
		return nil, err
	}
	return runs, nil
}

// Model-call purposes, the intent tool, and the scripted answers of the loop.
const (
	evalLoopScriptedModel    = "qwen3:4b"
	evalLoopDirectAnswer     = "READY"
	evalLoopIntentTool       = "run_shell_command"
	evalLoopPurposeTurn      = "agent_turn"
	evalLoopPurposeGenerate  = "tribunal_generate"
	evalLoopPurposeMarshal   = "tribunal_marshal"
	evalLoopPurposeAudit     = "tribunal_audit"
	evalLoopTribunalProposal = `{"args":["app/service.conf"],"command":"cat"}`
	evalLoopTribunalCommand  = `{"command":"cat","args":["app/service.conf"]}`
	evalLoopTribunalAnswer   = "inventory-api listens on port 8081 in app/service.conf, but the upstream health check expects port 8080. Set listen_port = 8080 in the service configuration."
)

// evalLoopReplies is the scripted provider's answer sequence for the whole loop,
// in chat order: the direct campaign's READY; then the Tribunal campaign's first
// agent turn calling the intent tool, its seven member replies (five unanimous
// generations, one LOW marshal, one APPROVE audit), and the final answer.
func evalLoopReplies() []fixtures.OllamaReply {
	replies := []fixtures.OllamaReply{
		{Content: evalLoopDirectAnswer},
		{ToolCalls: []fixtures.OllamaToolCall{{Name: evalLoopIntentTool, Arguments: json.RawMessage(evalLoopTribunalProposal)}}},
	}
	for range 5 {
		replies = append(replies, fixtures.OllamaReply{Content: evalLoopTribunalCommand})
	}
	return append(replies,
		fixtures.OllamaReply{Content: `{"risk": "LOW", "rationale": "read-only", "feedback": ""}`},
		fixtures.OllamaReply{Content: `{"verdict": "APPROVE", "rationale": "correct"}`},
		fixtures.OllamaReply{Content: evalLoopTribunalAnswer},
	)
}

// evalLoopCampaign is one single-scenario campaign the loop creates, prepares,
// resumes, and verifies through the CLI.
type evalLoopCampaign struct {
	campaignID    string
	record        string // scenario name recorded in the run summary
	model         string // served model tag the campaign selects from the registry
	suite         evaluation.ScenarioSuite
	investigation string
	// tribunal marks the Tribunal intent scenario, whose trace must carry the
	// member deliberation and the approved command's governed tool call.
	tribunal bool
}

// evalLoopRun is one completed campaign run and its agent trace.
type evalLoopRun struct {
	campaign evalLoopCampaign
	trace    *evaluation.AgentTrace
}

// evalLoopCampaigns returns the loop's campaigns in run order: the direct
// instruction scenario, then the Tribunal intent scenario.
func evalLoopCampaigns() ([]evalLoopCampaign, error) {
	direct, err := singleScenarioSuite(evaluation.DefaultScenarioSuite(), "cross-process-suite", "instruction-exact-format")
	if err != nil {
		return nil, err
	}
	tribunal, err := evalLoopTribunalCampaign(evalLoopScriptedModel)
	if err != nil {
		return nil, err
	}
	return []evalLoopCampaign{
		{campaignID: "cross-process", record: "eval-loop", model: evalLoopScriptedModel, suite: direct, investigation: "persistent:cross-process-incident"},
		tribunal,
	}, nil
}

// evalLoopTribunalCampaign returns the Tribunal intent campaign for model: the
// wrong-port configuration scenario on one persistent investigation.
func evalLoopTribunalCampaign(model string) (evalLoopCampaign, error) {
	investigations, err := evaluation.InvestigationsScenarioSuite()
	if err != nil {
		return evalLoopCampaign{}, err
	}
	suite, err := singleScenarioSuite(investigations, "cross-process-tribunal-suite", "wrong-port-config")
	if err != nil {
		return evalLoopCampaign{}, err
	}
	return evalLoopCampaign{campaignID: "cross-process-tribunal", record: "eval-loop-tribunal", model: model, suite: suite, investigation: "persistent:cross-process-tribunal-incident", tribunal: true}, nil
}

// singleScenarioSuite returns source narrowed to the one scenario, under suiteID.
func singleScenarioSuite(source evaluation.ScenarioSuite, suiteID, scenarioID string) (evaluation.ScenarioSuite, error) {
	for _, scenario := range source.Scenarios {
		if scenario.ScenarioID == scenarioID {
			source.ID = suiteID
			source.Scenarios = []evaluation.SuiteScenario{scenario}
			return source, nil
		}
	}
	return evaluation.ScenarioSuite{}, fmt.Errorf("eval loop suite %s has no scenario %s: %w", source.ID, scenarioID, constants.ErrEvaluationSuiteInvalid)
}

// runEvalCampaign creates one campaign through the CLI, prepares and resumes its
// run, and returns the completed assignment's trace once its evidence verifies.
// The run is selected by its campaign, since the store holds every campaign's runs.
func (r *releaseSmokeRun) runEvalCampaign(ctx context.Context, store *evaluation.Store, campaign evalLoopCampaign) (evalLoopRun, error) {
	if err := store.CreateSuite(ctx, campaign.suite); err != nil {
		return evalLoopRun{}, err
	}
	if _, err := r.ztdGateway(ctx, "create-eval-campaign", "eval", "campaigns", "create", campaign.campaignID, campaign.model, "--suite", campaign.suite.ID, "--investigation", campaign.investigation, "--data-operator", r.workerOperatorID); err != nil {
		return evalLoopRun{}, err
	}
	if _, err := r.ztdGateway(ctx, "prepare-eval-run", "eval", "runs", "start", campaign.campaignID, "--prepare-only", "--daemon=false", "--publish=false", "--no-backup"); err != nil {
		return evalLoopRun{}, err
	}
	runIDs, err := store.ListCampaignRunIDs(ctx, campaign.campaignID)
	if err != nil {
		return evalLoopRun{}, err
	}
	if len(runIDs) != 1 {
		return evalLoopRun{}, fmt.Errorf("eval campaign %s has %d runs: %w", campaign.campaignID, len(runIDs), constants.ErrValidationFailed)
	}
	runID := runIDs[0]
	if _, err := r.ztdGateway(ctx, "execute-eval-run", "eval", "runs", "resume", runID, "--no-backup"); err != nil {
		return evalLoopRun{}, err
	}
	assignments, err := store.ListAssignments(ctx, runID)
	if err != nil {
		return evalLoopRun{}, err
	}
	// One resume executes at most --limit queued assignments, and that limit
	// defaults to 1. The other cells the scenario's eligible roles scheduled stay
	// QUEUED, with no result, until a later resume runs them.
	var executed *evalv1.EvaluationAssignment
	for _, assignment := range assignments {
		switch assignment.GetLifecycleStatus() {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED:
			if executed != nil {
				return evalLoopRun{}, fmt.Errorf("eval campaign %s completed more than one assignment: %w", campaign.campaignID, constants.ErrValidationFailed)
			}
			executed = assignment
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED:
		default:
			return evalLoopRun{}, fmt.Errorf("eval campaign %s assignment %s is %s: %w", campaign.campaignID, assignment.GetAssignmentId(), assignment.GetLifecycleStatus(), constants.ErrValidationFailed)
		}
	}
	if executed == nil {
		return evalLoopRun{}, fmt.Errorf("eval campaign %s executed no assignment: %w", campaign.campaignID, constants.ErrValidationFailed)
	}
	trace, err := store.LoadAssignmentAgentTrace(ctx, runID, executed.GetAssignmentId())
	if err != nil {
		return evalLoopRun{}, err
	}
	if err := evaluation.VerifyAgentTraceEvidence(trace); err != nil {
		return evalLoopRun{}, err
	}
	return evalLoopRun{campaign: campaign, trace: trace}, nil
}

// bindEvalAnchors sets the run's conformance anchors: the embedded Gateway
// Operator's, and the Data and Inference Operators', each read from its own runtime.
func (r *releaseSmokeRun) bindEvalAnchors(ctx context.Context, files fs.RuntimeFileService, inferenceRoot, inferenceID string) error {
	operators, _, err := r.client.ListOperators(ctx)
	if err != nil {
		return err
	}
	gatewayAnchor, err := oracle.LoadAnchor(ctx, files, releaseSmokeEmbeddedOpID)
	if err != nil {
		return err
	}
	anchors := []*evalv1.ConformanceAnchor{gatewayAnchor}
	for _, target := range []struct{ root, id string }{{r.workerRoot, r.workerOperatorID}, {inferenceRoot, inferenceID}} {
		for _, op := range operators {
			if op.GetId() != target.id {
				continue
			}
			bound, err := oracle.BoundAnchor(op)
			if err != nil {
				return err
			}
			runtimeFiles, err := fs.NewRuntimeFileService(target.root, slog.Default())
			if err != nil {
				return err
			}
			anchor, err := oracle.CheckedBoundAnchor(ctx, runtimeFiles, target.id, bound)
			if err != nil {
				return err
			}
			anchors = append(anchors, anchor)
		}
	}
	r.anchors, err = oracle.NewAnchorSet(anchors)
	return err
}

// verifyEvalRun checks one campaign's trace against the runtimes that produced
// it, then verifies every event receipt the trace carries against the Operator that
// signed it: the harness sentinels and the governed command on the Data runtime,
// and each model call on the Inference runtime.
func (r *releaseSmokeRun) verifyEvalRun(ctx context.Context, run evalLoopRun, inferenceRoot, inferenceID, inferenceSession string) error {
	trace := run.trace
	if err := r.verifyEvalTraceRuntimes(trace, inferenceID, inferenceSession); err != nil {
		return err
	}
	if run.campaign.tribunal {
		if err := verifyTribunalTrace(trace); err != nil {
			return err
		}
	} else if trace.FinalOutput != evalLoopDirectAnswer || len(trace.ModelCalls) != 1 {
		return fmt.Errorf("eval trace answer: %w", constants.ErrValidationFailed)
	}
	txs, err := r.verifyEvalEventReceipts(ctx, trace, inferenceRoot, inferenceID, inferenceSession)
	if err != nil {
		return err
	}
	r.recordScenario(run.campaign.record, txs)
	return nil
}

// verifyEvalTraceRuntimes binds a trace to the runtimes that produced it: the
// Data Operator this run deployed, and the Inference Operator and session.
func (r *releaseSmokeRun) verifyEvalTraceRuntimes(trace *evaluation.AgentTrace, inferenceID, inferenceSession string) error {
	if trace.Data.OperatorID != r.workerOperatorID || trace.Inference.OperatorID != inferenceID || trace.Inference.SessionID != inferenceSession || trace.Bracket == nil {
		return fmt.Errorf("eval trace runtime binding: %w", constants.ErrIdentityBindingFailed)
	}
	return nil
}

// verifyEvalEventReceipts verifies every event receipt a trace carries against the Operator
// that signed it: the harness sentinels and each tool call on the
// Data runtime, and each model call on the Inference runtime. It returns the
// transaction IDs it verified.
func (r *releaseSmokeRun) verifyEvalEventReceipts(ctx context.Context, trace *evaluation.AgentTrace, inferenceRoot, inferenceID, inferenceSession string) ([]string, error) {
	var txs []string
	for _, eventReceipt := range []*operatorv1.EventReceipt{trace.Bracket.Before.EventReceipt, trace.Bracket.After.EventReceipt} {
		if eventReceipt == nil {
			return nil, constants.ErrEvidenceArtifactMalformed
		}
		if err := r.verifyEventReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, eventReceipt.GetTransactionId()); err != nil {
			return nil, err
		}
		txs = append(txs, eventReceipt.GetTransactionId())
	}
	for _, call := range trace.ModelCalls {
		eventReceipt := call.EventReceipt
		if eventReceipt == nil || eventReceipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
			return nil, fmt.Errorf("eval inference %s requires L3: %w", call.Purpose, constants.ErrValidationFailed)
		}
		if eventReceipt.GetL2Status() != operatorv1.L2Status_L2_STATUS_NOT_REQUIRED {
			return nil, fmt.Errorf("eval inference %s must not require L2: %w", call.Purpose, constants.ErrValidationFailed)
		}
		if err := r.verifyEventReceipt(ctx, inferenceRoot, inferenceID, inferenceSession, eventReceipt.GetTransactionId()); err != nil {
			return nil, err
		}
		txs = append(txs, eventReceipt.GetTransactionId())
	}
	for _, call := range trace.ToolCalls {
		if call.EventReceipt == nil {
			return nil, fmt.Errorf("eval command %s has no event receipt: %w", call.ToolName, constants.ErrValidationFailed)
		}
		wantL2 := operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
		action := constants.ActionType(call.ActionType)
		if r.opts.Posture.RequiresL2() && !action.IsBootstrapAction() && action.ChangesState(call.ToolName) {
			wantL2 = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
		}
		if call.EventReceipt.GetL2Status() != wantL2 {
			return nil, fmt.Errorf("eval command %s L2 status %s, want %s: %w", call.ToolName, call.EventReceipt.GetL2Status(), wantL2, constants.ErrValidationFailed)
		}
		if err := r.verifyEventReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, call.EventReceipt.GetTransactionId()); err != nil {
			return nil, err
		}
		txs = append(txs, call.EventReceipt.GetTransactionId())
	}
	return txs, nil
}

// verifyTribunalTrace checks the Tribunal campaign's trace: two agent turns, and
// one intent tool call whose deliberation made five generations, one marshal
// call and one audit call. The approved arguments ran as one governed MCP_CALL.
func verifyTribunalTrace(trace *evaluation.AgentTrace) error {
	counts := make(map[string]int, len(trace.ModelCalls))
	for _, call := range trace.ModelCalls {
		counts[call.Purpose]++
	}
	for _, want := range []struct {
		purpose string
		calls   int
	}{{evalLoopPurposeTurn, 2}, {evalLoopPurposeGenerate, 5}, {evalLoopPurposeMarshal, 1}, {evalLoopPurposeAudit, 1}} {
		if counts[want.purpose] != want.calls {
			return fmt.Errorf("tribunal trace has %d %s calls, want %d: %w", counts[want.purpose], want.purpose, want.calls, constants.ErrValidationFailed)
		}
	}
	if len(trace.ModelCalls) != 9 {
		return fmt.Errorf("tribunal trace has %d model calls, want 9: %w", len(trace.ModelCalls), constants.ErrValidationFailed)
	}
	if len(trace.ToolCalls) != 1 {
		return fmt.Errorf("tribunal trace has %d tool calls, want 1: %w", len(trace.ToolCalls), constants.ErrValidationFailed)
	}
	if call := trace.ToolCalls[0]; call.ToolName != evalLoopIntentTool || call.ActionType != string(constants.ActionTypeMcpCall) || call.ArgumentsJSON != evalLoopTribunalProposal {
		return fmt.Errorf("tribunal tool call %s %s %q: %w", call.ToolName, call.ActionType, call.ArgumentsJSON, constants.ErrValidationFailed)
	} else {
		var approved struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal([]byte(call.ApprovedArgumentsJSON), &approved); err != nil || approved.Command != "cat" || len(approved.Args) != 1 || approved.Args[0] != "app/service.conf" {
			return fmt.Errorf("approved Tribunal arguments %q: %w", call.ApprovedArgumentsJSON, constants.ErrValidationFailed)
		}
	}
	if trace.FinalOutput != evalLoopTribunalAnswer || len(trace.Steps) == 0 {
		return fmt.Errorf("tribunal trace answer or steps: %w", constants.ErrValidationFailed)
	}
	return nil
}

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
// identities, enrollment, transport, execution and receipts are production code.
func TestEvalLoopCrossProcess(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
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
			binary := filepath.Join(repoRoot, "ztd")
			if runtime.GOOS == "windows" {
				binary += ".exe"
			}
			_, err = os.Stat(binary)
			require.NoError(t, err, "run make build before test eval-loop")
			files, err := fs.NewRuntimeFileService(repoRoot, slog.Default())
			require.NoError(t, err)
			id, err := uuid.NewString()
			require.NoError(t, err)
			root := files.Resolve(filepath.Join(constants.OperatorsDirname, "eval-loop-"+string(posture)+"-"+id))
			t.Logf("cross-process eval evidence: %s", root)
			r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: root, ZtdBin: binary, Timeout: 6 * time.Minute, Posture: posture})
			ctx, cancel := context.WithTimeout(t.Context(), r.opts.Timeout)
			defer cancel()
			// execute normally owns teardown. Keep an emergency owner for a fixture
			// assertion that exits a phase's goroutine before execute can return.
			finished := false
			t.Cleanup(func() {
				if !finished {
					cleanup, stop := context.WithTimeout(context.Background(), releaseSmokeCleanupTimeout)
					defer stop()
					assert.NoError(t, r.runCleanups(cleanup))
				}
			})
			replies := evalLoopReplies()
			provider := fixtures.NewScriptedOllama(t, replies)
			var approvals *releaseSmokeCLIApprovalResponder
			var consensus *evalLoopConsensusApp
			var inferenceRoot, inferenceID, inferenceSession string
			phases := []smokePhase{
				{"prepare", r.prepare},
				{"start isolated Gateway", r.startGateway},
				{"enroll CLI owner", r.loginCLI},
				{"subscribe CLI approvals", func(context.Context) error {
					approvals = startReleaseSmokeCLIApprovalResponder(t, r)
					r.addCleanup("CLI approval responder", func(context.Context) error { approvals.Stop(); return nil })
					return nil
				}},
				{"bind real consensus application", func(context.Context) error {
					if posture == constants.PostureNotary {
						consensus = provisionEvalLoopConsensus(t, r)
					}
					return nil
				}},
				{"deploy dedicated Data Operator", r.deployWorker},
				{"deploy dedicated Inference Operator", func(ctx context.Context) error {
					var err error
					inferenceRoot = filepath.Join(r.opts.RunDir, "inference")
					inferenceID, inferenceSession, err = r.deployEvalInference(ctx, inferenceRoot, provider.Endpoint())
					return err
				}},
				{"execute and verify eval through CLI", func(ctx context.Context) error {
					return r.executeEvalLoop(ctx, inferenceRoot, inferenceID, inferenceSession)
				}},
			}
			err = r.execute(ctx, phases)
			finished = true
			require.NoError(t, err)
			assert.Equal(t, len(replies), provider.ChatCount(), "every scripted chat turn must be consumed")
			require.NotNil(t, approvals)
			assert.NotZero(t, approvals.Count(), "separate CLI processes must provide L3 approval")
			assert.Empty(t, approvals.Errors())
			if consensus != nil {
				assert.NotZero(t, consensus.DeliberationCount(), "the production deliberator must call the bound application")
			}
		})
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

func (r *releaseSmokeRun) executeEvalLoop(ctx context.Context, inferenceRoot, inferenceID, inferenceSession string) error {
	files, err := fs.NewRuntimeFileService(r.gwRoot, slog.Default())
	if err != nil {
		return err
	}
	evidence, err := shared.EvidenceFileSvcFactory(keystore.Options{Keyring: r.agent.Keyring(files)})(r.gwRoot, slog.Default())
	if err != nil {
		return err
	}
	store := evaluation.NewStore(evidence)
	variant := &evalv1.ModelVariant{VariantId: "qwen3-4b", ServedModelTag: "qwen3:4b", ModelDigest: strings.Repeat("0123456789abcdef", 4), ProviderClass: "ollama"}
	wire, err := evalv1.MarshalCanonical(variant)
	if err != nil {
		return err
	}
	inventory, err := json.Marshal(struct {
		Variants []json.RawMessage `json:"variants"`
	}{Variants: []json.RawMessage{wire}})
	if err != nil {
		return err
	}
	if err := files.WriteFile(ctx, constants.EvaluationModelInventoryPath, inventory, constants.PermFileReadOnly); err != nil {
		return err
	}
	campaigns, err := evalLoopCampaigns()
	if err != nil {
		return err
	}
	// Each campaign runs to completion through its own CLI processes first.
	// Anchors are bound afterwards, and every trace is verified against them.
	runs := make([]evalLoopRun, 0, len(campaigns))
	for _, campaign := range campaigns {
		run, err := r.runEvalCampaign(ctx, store, campaign)
		if err != nil {
			return err
		}
		runs = append(runs, run)
	}
	if err := r.bindEvalAnchors(ctx, files, inferenceRoot, inferenceID); err != nil {
		return err
	}
	for _, run := range runs {
		if err := r.verifyEvalRun(ctx, run, inferenceRoot, inferenceID, inferenceSession); err != nil {
			return fmt.Errorf("campaign %s: %w", run.campaign.campaignID, err)
		}
	}
	return nil
}

// Model-call purposes, the intent tool, and the scripted answers of the loop.
const (
	evalLoopDirectAnswer    = "READY"
	evalLoopIntentTool      = "run_commands_with_operator"
	evalLoopPurposeTurn     = "agent_turn"
	evalLoopPurposeGenerate = "tribunal_generate"
	evalLoopPurposeMarshal  = "tribunal_marshal"
	evalLoopPurposeAudit    = "tribunal_audit"
	evalLoopTribunalCommand = "cat app/service.conf"
	evalLoopTribunalAnswer  = "inventory-api listens on port 8081 in app/service.conf, but the upstream health check expects port 8080. Set listen_port = 8080 in the service configuration."
)

// evalLoopReplies is the scripted provider's answer sequence for the whole loop,
// in chat order: the direct campaign's READY; then the Tribunal campaign's first
// agent turn calling the intent tool, its seven member replies (five unanimous
// generations, one LOW marshal, one APPROVE audit), and the final answer.
func evalLoopReplies() []fixtures.OllamaReply {
	replies := []fixtures.OllamaReply{
		{Content: evalLoopDirectAnswer},
		{ToolCalls: []fixtures.OllamaToolCall{{Name: evalLoopIntentTool, Arguments: json.RawMessage(`{"request": "print the configured port in the service config"}`)}}},
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
	investigations, err := evaluation.InvestigationsScenarioSuite()
	if err != nil {
		return nil, err
	}
	tribunalSuite, err := singleScenarioSuite(investigations, "cross-process-tribunal-suite", "wrong-port-config")
	if err != nil {
		return nil, err
	}
	return []evalLoopCampaign{
		{campaignID: "cross-process", record: "eval-loop", suite: direct, investigation: "persistent:cross-process-incident"},
		{campaignID: "cross-process-tribunal", record: "eval-loop-tribunal", suite: tribunalSuite, investigation: "persistent:cross-process-tribunal-incident", tribunal: true},
	}, nil
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
	if _, err := r.ztdGateway(ctx, "create-eval-campaign", "eval", "campaigns", "create", campaign.campaignID, "qwen3:4b", "--suite", campaign.suite.ID, "--posture", string(r.opts.Posture), "--investigation", campaign.investigation, "--data-operator", r.workerOperatorID); err != nil {
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
// it, then verifies every receipt the trace carries against the Operator that
// signed it: the harness sentinels and the governed command on the Data runtime,
// and each model call on the Inference runtime.
func (r *releaseSmokeRun) verifyEvalRun(ctx context.Context, run evalLoopRun, inferenceRoot, inferenceID, inferenceSession string) error {
	trace := run.trace
	if trace.Data.OperatorID != r.workerOperatorID || trace.Inference.OperatorID != inferenceID || trace.Inference.SessionID != inferenceSession || trace.Bracket == nil {
		return fmt.Errorf("eval trace runtime binding: %w", constants.ErrIdentityBindingFailed)
	}
	if run.campaign.tribunal {
		if err := verifyTribunalTrace(trace); err != nil {
			return err
		}
	} else if trace.FinalOutput != evalLoopDirectAnswer || len(trace.ModelCalls) != 1 {
		return fmt.Errorf("eval trace answer: %w", constants.ErrValidationFailed)
	}
	var txs []string
	for _, receipt := range []*operatorv1.ActionReceipt{trace.Bracket.Before.ActionReceipt, trace.Bracket.After.ActionReceipt} {
		if receipt == nil {
			return constants.ErrEvidenceArtifactMalformed
		}
		if err := r.verifyReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, receipt.GetTransactionId()); err != nil {
			return err
		}
		txs = append(txs, receipt.GetTransactionId())
	}
	for _, call := range trace.ModelCalls {
		receipt := call.Receipt
		if receipt == nil || receipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
			return fmt.Errorf("eval inference %s requires L3: %w", call.Purpose, constants.ErrValidationFailed)
		}
		if r.opts.Posture == constants.PostureNotary && receipt.GetL2Status() != operatorv1.L2Status_L2_STATUS_REQUIRED_VALID {
			return fmt.Errorf("eval inference %s requires L2: %w", call.Purpose, constants.ErrValidationFailed)
		}
		if err := r.verifyReceipt(ctx, inferenceRoot, inferenceID, inferenceSession, receipt.GetTransactionId()); err != nil {
			return err
		}
		txs = append(txs, receipt.GetTransactionId())
	}
	for _, call := range trace.ToolCalls {
		if call.ActionType != string(constants.ActionTypeExecuteBash) {
			continue
		}
		if call.Receipt == nil {
			return fmt.Errorf("eval command %s has no receipt: %w", call.ToolName, constants.ErrValidationFailed)
		}
		if err := r.verifyReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, call.Receipt.GetTransactionId()); err != nil {
			return err
		}
		txs = append(txs, call.Receipt.GetTransactionId())
	}
	r.recordScenario(run.campaign.record, txs)
	return nil
}

// verifyTribunalTrace checks the Tribunal campaign's trace: two agent turns, and
// one intent tool call whose deliberation made five generations, one marshal
// call and one audit call. The approved command ran as one governed EXECUTE_BASH.
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
	if call := trace.ToolCalls[0]; call.ToolName != evalLoopIntentTool || call.ActionType != string(constants.ActionTypeExecuteBash) || call.Command != evalLoopTribunalCommand {
		return fmt.Errorf("tribunal tool call %s %s %q: %w", call.ToolName, call.ActionType, call.Command, constants.ErrValidationFailed)
	}
	if trace.FinalOutput != evalLoopTribunalAnswer || len(trace.Steps) == 0 {
		return fmt.Errorf("tribunal trace answer or steps: %w", constants.ErrValidationFailed)
	}
	return nil
}

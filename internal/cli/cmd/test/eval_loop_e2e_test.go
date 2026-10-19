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
			provider := fixtures.NewScriptedOllama(t, []fixtures.OllamaReply{{Content: "READY"}})
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
			assert.Equal(t, 1, provider.ChatCount())
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
	suite := evaluation.DefaultScenarioSuite()
	suite.ID = "cross-process-suite"
	for _, scenario := range suite.Scenarios {
		if scenario.ScenarioID == "instruction-exact-format" {
			suite.Scenarios = []evaluation.SuiteScenario{scenario}
			break
		}
	}
	if len(suite.Scenarios) != 1 {
		return constants.ErrEvaluationSuiteInvalid
	}
	if err := store.CreateSuite(ctx, suite); err != nil {
		return err
	}
	if _, err := r.ztdGateway(ctx, "create-eval-campaign", "eval", "campaigns", "create", "cross-process", "qwen3:4b", "--suite", suite.ID, "--posture", string(r.opts.Posture), "--investigation", "persistent:cross-process-incident", "--data-operator", r.workerOperatorID); err != nil {
		return err
	}
	if _, err := r.ztdGateway(ctx, "prepare-eval-run", "eval", "runs", "start", "cross-process", "--prepare-only", "--daemon=false", "--publish=false", "--no-backup"); err != nil {
		return err
	}
	runs, err := store.ListRunIDs(ctx)
	if err != nil {
		return err
	}
	if len(runs) != 1 {
		return fmt.Errorf("eval loop has %d runs: %w", len(runs), constants.ErrValidationFailed)
	}
	if _, err := r.ztdGateway(ctx, "execute-eval-run", "eval", "runs", "resume", runs[0], "--no-backup"); err != nil {
		return err
	}
	assignments, err := store.ListAssignments(ctx, runs[0])
	if err != nil {
		return err
	}
	if len(assignments) != 1 || assignments[0].GetLifecycleStatus() != evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED {
		return fmt.Errorf("eval assignment not completed: %w", constants.ErrValidationFailed)
	}
	trace, err := store.LoadAssignmentAgentTrace(ctx, runs[0], assignments[0].GetAssignmentId())
	if err != nil {
		return err
	}
	if err := evaluation.VerifyAgentTraceEvidence(trace); err != nil {
		return err
	}
	if trace.Data.OperatorID != r.workerOperatorID || trace.Inference.OperatorID != inferenceID || trace.Inference.SessionID != inferenceSession || trace.FinalOutput != "READY" || len(trace.ModelCalls) != 1 || trace.Bracket == nil {
		return fmt.Errorf("eval trace runtime binding: %w", constants.ErrIdentityBindingFailed)
	}
	operators, _, err := r.client.ListOperators(ctx)
	if err != nil {
		return err
	}
	var anchors []*evalv1.ConformanceAnchor
	gatewayAnchor, err := oracle.LoadAnchor(ctx, files, releaseSmokeEmbeddedOpID)
	if err != nil {
		return err
	}
	anchors = append(anchors, gatewayAnchor)
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
	if err != nil {
		return err
	}
	for _, receipt := range []*operatorv1.ActionReceipt{trace.Bracket.Before.ActionReceipt, trace.Bracket.After.ActionReceipt} {
		if receipt == nil {
			return constants.ErrEvidenceArtifactMalformed
		}
		if err := r.verifyReceipt(ctx, r.workerRoot, r.workerOperatorID, r.workerSession, receipt.GetTransactionId()); err != nil {
			return err
		}
	}
	modelReceipt := trace.ModelCalls[0].Receipt
	if modelReceipt == nil || modelReceipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
		return fmt.Errorf("eval inference requires L3: %w", constants.ErrValidationFailed)
	}
	if r.opts.Posture == constants.PostureNotary && modelReceipt.GetL2Status() != operatorv1.L2Status_L2_STATUS_REQUIRED_VALID {
		return fmt.Errorf("eval inference requires L2: %w", constants.ErrValidationFailed)
	}
	if err := r.verifyReceipt(ctx, inferenceRoot, inferenceID, inferenceSession, modelReceipt.GetTransactionId()); err != nil {
		return err
	}
	r.recordScenario("eval-loop", []string{modelReceipt.GetTransactionId(), trace.Bracket.Before.GetTransactionId(), trace.Bracket.After.GetTransactionId()})
	return nil
}

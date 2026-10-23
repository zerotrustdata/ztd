// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e && live

package testcmd

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// Test-binary flags for the live Tier 4 loop. ztd test eval-loop passes them
// after -args, under the names of its --live-ollama-endpoint and --live-model
// options.
var (
	evalLoopLiveEndpoint = flag.String(evalLoopLiveEndpointFlag, "", "Ollama endpoint for the live eval loop, reached only through the governed Inference Operator")
	evalLoopLiveModel    = flag.String(evalLoopLiveModelFlag, "", "provider model tag for the live eval loop")
)

// TestEvalLoopLive is the Tier 4 gate. It runs the Tribunal eval loop against a
// live provider under ratify and notary, and proves the loop and its evidence.
// It does not judge the model: a FAIL verdict is a legitimate outcome.
func TestEvalLoopLive(t *testing.T) {
	if *evalLoopLiveEndpoint == "" || *evalLoopLiveModel == "" {
		t.Fatalf("live eval loop needs -%s and -%s; run ./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>", evalLoopLiveEndpointFlag, evalLoopLiveModelFlag)
	}
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			r := newEvalLoopRun(t, "eval-live-", posture, evalLoopLiveBudget(t))
			// Registered before the emergency owner that runEvalLoopPhases adds,
			// so this removal runs after the stack has stopped.
			t.Cleanup(func() {
				if t.Failed() {
					removeFailedEvalLoopRun(t, filepath.Base(r.opts.RunDir))
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), r.opts.Timeout)
			defer cancel()
			st := &evalLoopState{}
			phases := evalLoopPhases(t, r, *evalLoopLiveEndpoint, st, func(ctx context.Context) error {
				return r.executeLiveEvalLoop(ctx, t, *evalLoopLiveModel, st.inferenceRoot, st.inferenceID, st.inferenceSession)
			})
			require.NoError(t, runEvalLoopPhases(ctx, t, r, phases))
			require.NotNil(t, st.approvals)
			assert.NotZero(t, st.approvals.Count(), "separate CLI processes must provide L3 approval")
			assert.Empty(t, st.approvals.Errors())
			if st.consensus != nil {
				assert.NotZero(t, st.consensus.DeliberationCount(), "the production deliberator must call the bound application")
			}
		})
	}
}

// executeLiveEvalLoop freezes the provider's real inventory through the governed
// CLI, runs the Tribunal campaign for model, and verifies the evidence it made.
func (r *releaseSmokeRun) executeLiveEvalLoop(ctx context.Context, t *testing.T, model, inferenceRoot, inferenceID, inferenceSession string) error {
	t.Helper()
	files, store, err := r.newEvalLoopStore()
	if err != nil {
		return err
	}
	digest, err := r.freezeEvalModel(ctx, files, model)
	if err != nil {
		return err
	}
	campaign, err := evalLoopTribunalCampaign(model)
	if err != nil {
		return err
	}
	runs, err := r.runEvalCampaigns(ctx, files, store, []evalLoopCampaign{campaign}, inferenceRoot, inferenceID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if err := r.verifyLiveEvalRun(ctx, t, store, run, digest, inferenceRoot, inferenceID, inferenceSession); err != nil {
			return fmt.Errorf("live campaign %s: %w", run.campaign.campaignID, err)
		}
	}
	return nil
}

// verifyLiveEvalRun checks one live run's evidence. It asserts nothing about the
// model's answer, command or verdict; the verdict and the model-call purposes
// are logged. It binds the trace to its runtimes and the frozen digest, requires
// the Tribunal stage to have run, and verifies every receipt the trace carries.
func (r *releaseSmokeRun) verifyLiveEvalRun(ctx context.Context, t *testing.T, store *evaluation.Store, run evalLoopRun, digest, inferenceRoot, inferenceID, inferenceSession string) error {
	t.Helper()
	trace := run.trace
	if err := r.verifyEvalTraceRuntimes(trace, inferenceID, inferenceSession); err != nil {
		return err
	}
	if trace.ServedModelTag != run.campaign.model || trace.ModelDigest != digest {
		return fmt.Errorf("live trace bound %s %s, want frozen %s %s: %w", trace.ServedModelTag, trace.ModelDigest, run.campaign.model, digest, constants.ErrValidationFailed)
	}
	purposes := make(map[string]int, len(trace.ModelCalls))
	for _, call := range trace.ModelCalls {
		if call.ServedModelTag != run.campaign.model || call.ModelDigest != digest {
			return fmt.Errorf("live %s call bound %s %s, want frozen %s %s: %w", call.Purpose, call.ServedModelTag, call.ModelDigest, run.campaign.model, digest, constants.ErrValidationFailed)
		}
		purposes[call.Purpose]++
	}
	result, err := store.LoadAssignmentResult(ctx, trace.RunID, trace.AssignmentID)
	if err != nil {
		return err
	}
	t.Logf("live campaign %s verdict: %s", run.campaign.campaignID, evaluation.DerivePublicSummaryStatus(result))
	t.Logf("live campaign %s model calls by purpose: %v", run.campaign.campaignID, purposes)
	if !liveTraceCalledIntent(trace) {
		return fmt.Errorf("live model never reached the Tribunal stage: %w", constants.ErrValidationFailed)
	}
	if purposes[evalLoopPurposeGenerate] == 0 || purposes[evalLoopPurposeMarshal] == 0 {
		return fmt.Errorf("live Tribunal stage lacked %s or %s calls (%d, %d): %w", evalLoopPurposeGenerate, evalLoopPurposeMarshal, purposes[evalLoopPurposeGenerate], purposes[evalLoopPurposeMarshal], constants.ErrValidationFailed)
	}
	txs, err := r.verifyEvalReceipts(ctx, trace, inferenceRoot, inferenceID, inferenceSession)
	if err != nil {
		return err
	}
	r.recordScenario(run.campaign.record, txs)
	return nil
}

// liveTraceCalledIntent reports whether the model called the Tribunal intent tool.
func liveTraceCalledIntent(trace *evaluation.AgentTrace) bool {
	for _, call := range trace.ToolCalls {
		if call.ToolName == evalLoopIntentTool {
			return true
		}
	}
	return false
}

// evalLoopLiveBudget is one posture's deadline: what remains of the test
// binary's deadline after the stack's cleanup margin. ztd test eval-loop sizes
// that deadline per selected posture, so a slow ratify run spends its own share
// before notary starts and the overall deadline stays honest.
func evalLoopLiveBudget(t *testing.T) time.Duration {
	t.Helper()
	deadline, ok := t.Deadline()
	require.True(t, ok, "run the live eval loop through ./ztd test eval-loop, which sets the deadline")
	budget := time.Until(deadline) - releaseSmokeCleanupTimeout
	require.Positive(t, budget, "the test deadline leaves no time for this posture")
	return budget
}

// removeFailedEvalLoopRun deletes a failed live run's root, named name under
// .ztd/operators, through the runtime file service. The stack has already
// stopped. A Windows handle can outlive its job briefly, so the removal retries
// as smokeTempDir does.
func removeFailedEvalLoopRun(t *testing.T, name string) {
	t.Helper()
	files, _ := evalLoopRepoFiles(t)
	rel := evalLoopRunRelPath(name)
	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	err := files.RemoveAll(ctx, rel)
	for err != nil && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		err = files.RemoveAll(ctx, rel)
	}
	if err != nil {
		t.Errorf("remove failed eval loop run %s: %v", rel, err)
	}
}

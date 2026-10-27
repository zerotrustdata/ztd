// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// scriptedJudgeVariant is the default judge pinned to the digest of the model the
// scripted provider serves under its tag, for stacks that dispatch the judge.
func scriptedJudgeVariant(t *testing.T) *evalv1.ModelVariant {
	t.Helper()
	judge := testJudgeVariant()
	judge.ModelDigest = fixtures.ScriptedModelDigest(t, judge.ServedModelTag)
	return judge
}

// withScriptedJudgeVerdicts makes every scripted turn answer a request for the
// default judge with a strict criterion verdict, so the position of a judge call
// in the call order cannot hand it a scored model's scripted reply.
func withScriptedJudgeVerdicts(replies []fixtures.OllamaReply) []fixtures.OllamaReply {
	out := make([]fixtures.OllamaReply, len(replies))
	for i, reply := range replies {
		reply.Respond = func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
			if request.Model == evaluation.DefaultJudgeServedModelTag {
				return fixtures.OllamaReply{Content: `{"met": true, "rationale": "scripted verdict"}`}
			}
			return fixtures.OllamaReply{}
		}
		out[i] = reply
	}
	return out
}

func TestRunRollout_ProviderCrashStopsBadModelAndContinuesWithHealthyModel(t *testing.T) {
	replies := make([]fixtures.OllamaReply, 96)
	for i := range replies {
		replies[i].Content = "READY"
		if i < rolloutProviderFailureLimit {
			replies[i] = fixtures.OllamaReply{Failure: "llama-server terminated: GGML_ASSERT(hparams.is_swa_any()) failed"}
		}
	}
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{
		Posture: constants.PostureDoctrine, EnablePublicSpectator: true,
		Provenance: true, ModelTags: []string{"qwen3:8b", evaluation.DefaultJudgeServedModelTag}, Replies: withScriptedJudgeVerdicts(replies),
	})
	previousOrigin := gwremote.PublicMirrorOrigin
	gwremote.PublicMirrorOrigin = run.stack.Gateway.PublicMirrorOrigin
	t.Cleanup(func() { gwremote.PublicMirrorOrigin = previousOrigin })
	bad, healthy := testQwenVariant(), testQwenVariant()
	bad.ModelDigest = fixtures.ScriptedModelDigest(t, bad.ServedModelTag)
	healthy.VariantId, healthy.ServedModelTag = "qwen3-8b", "qwen3:8b"
	healthy.ModelDigest = fixtures.ScriptedModelDigest(t, healthy.ServedModelTag)
	writeTestFrozenInventory(t, env.root, bad, healthy, scriptedJudgeVariant(t))
	env.mustRun(t, "rollout", "add", bad.ServedModelTag)
	env.mustRun(t, "rollout", "add", healthy.ServedModelTag)
	var out bytes.Buffer
	env.cmd.SetOut(&out)
	env.cmd.SetErr(&out)
	err := runRollout(env.cmd, env.deps, rolloutRunOptions{
		SkipVerified: true, GateSmoke: true,
		MirrorBootstrapURL: run.stack.Gateway.PublicMirrorBootstrapURL,
	})
	require.Error(t, err, out.String())
	assert.Contains(t, out.String(), "planned=2 succeeded=1 failed=1")
	assert.Contains(t, out.String(), "3 consecutive provider generation failures")
	queue, err := evaluation.LoadRolloutQueue(run.ctx, env.fileSvc(t))
	require.NoError(t, err)
	require.Len(t, queue.Models, 2)
	assert.Equal(t, evaluation.QueueStatusFailed, queue.Models[0].Status)
	assert.Contains(t, queue.Models[0].Notes, "provider_failure (generation_failed)")
	assert.Equal(t, evaluation.QueueStatusPending, queue.Models[1].Status, "a successful smoke gate leaves the healthy entry eligible for full qualification")
	assert.Contains(t, queue.Models[1].Notes, "smoke gate passed")
	store := env.store(t)
	assignments, err := store.ListAssignments(run.ctx, queue.Models[0].VerifiedRunID)
	require.NoError(t, err)
	failed, queued := 0, 0
	for _, assignment := range assignments {
		switch assignment.GetLifecycleStatus() {
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED:
			failed++
			trace, err := store.LoadAssignmentAgentTrace(run.ctx, assignment.GetRunId(), assignment.GetAssignmentId())
			require.NoError(t, err)
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
		case evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED:
			queued++
		default:
			t.Fatalf("unexpected assignment lifecycle %s", assignment.GetLifecycleStatus())
		}
	}
	assert.Equal(t, rolloutProviderFailureLimit, failed)
	assert.Positive(t, queued, "unexecuted assignments keep their real queued lifecycle")
	requests := run.stack.Provider.ChatRequests()
	require.Greater(t, len(requests), rolloutProviderFailureLimit, "the healthy model still receives governed inference")
	for i, raw := range requests {
		var request fixtures.OllamaChatRequest
		require.NoError(t, json.Unmarshal(raw, &request))
		if i < rolloutProviderFailureLimit {
			assert.Equal(t, bad.ServedModelTag, request.Model)
		} else {
			assert.Contains(t, []string{healthy.ServedModelTag, evaluation.DefaultJudgeServedModelTag}, request.Model)
		}
	}
	var plan rolloutRunPlanJSON
	require.NoError(t, env.runJSON(t, &plan, "rollout", "run", "--dry-run", "--skip-verified=false"))
	require.Len(t, plan.Models, 1, "a failed entry is excluded even when verified entries are requested")
	assert.Equal(t, healthy.VariantId, plan.Models[0].VariantID)
	env.mustRun(t, "rollout", "retry", bad.ServedModelTag)
	require.NoError(t, env.runJSON(t, &plan, "rollout", "run", "--dry-run"))
	assert.Len(t, plan.Models, 2, "an explicit retry makes the failed entry eligible again")
}

func TestRunRollout_HealthyResponseResetsGenerationFailureCount(t *testing.T) {
	replies := make([]fixtures.OllamaReply, 96)
	for i := range replies {
		replies[i].Content = "READY"
		if i < 2 || i == 3 || i == 4 {
			replies[i] = fixtures.OllamaReply{Failure: "temporary generation failure"}
		}
	}
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{
		Posture: constants.PostureDoctrine, EnablePublicSpectator: true,
		Provenance: true, ModelTags: []string{evaluation.DefaultJudgeServedModelTag}, Replies: withScriptedJudgeVerdicts(replies),
	})
	previousOrigin := gwremote.PublicMirrorOrigin
	gwremote.PublicMirrorOrigin = run.stack.Gateway.PublicMirrorOrigin
	t.Cleanup(func() { gwremote.PublicMirrorOrigin = previousOrigin })
	variant := testQwenVariant()
	variant.ModelDigest = fixtures.ScriptedModelDigest(t, variant.ServedModelTag)
	writeTestFrozenInventory(t, env.root, variant, scriptedJudgeVariant(t))
	env.mustRun(t, "rollout", "add", variant.ServedModelTag)
	var out bytes.Buffer
	env.cmd.SetOut(&out)
	env.cmd.SetErr(&out)
	require.NoError(t, runRollout(env.cmd, env.deps, rolloutRunOptions{
		SkipVerified: true, GateSmoke: true,
		MirrorBootstrapURL: run.stack.Gateway.PublicMirrorBootstrapURL,
	}), out.String())
	assert.Contains(t, out.String(), "planned=1 succeeded=1 failed=0")
	assert.Greater(t, run.stack.Provider.ChatCount(), 5, "separated failures do not trip the consecutive failure limit")
}

// newRolloutGateEnv is the rollout test environment over the real eval stack. The
// run preflight checks the public mirror bootstrap, which the Gateway serves, so
// the returned URL is the real Gateway's.
func newRolloutGateEnv(t *testing.T) (*runEnv, string) {
	t.Helper()
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine, EnablePublicSpectator: true})
	return env, run.stack.Gateway.PublicMirrorBootstrapURL
}

func TestRunRollout_FailedGateMarksEntryFailedAndContinues(t *testing.T) {
	env, mirrorURL := newRolloutGateEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.cmd.SetOut(&bytes.Buffer{})
	env.cmd.SetErr(&bytes.Buffer{})

	gates := func(fs.RuntimeFileService, []*evalv1.ModelVariant) rolloutGateRunner {
		return func(_ *cobra.Command, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error) {
			return &rolloutRunSuccess{VariantID: entry.VariantID, Tag: entry.ServedModelTag, RunID: "run-failed", Gate: gate}, constants.ErrEvalRunVerificationFailed
		}
	}
	err := runRolloutWith(env.cmd, env.deps, rolloutRunOptions{MirrorBootstrapURL: mirrorURL}, gates)
	require.Error(t, err)

	queue, err := evaluation.LoadRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)
	require.Len(t, queue.Models, 1)
	assert.Equal(t, evaluation.QueueStatusFailed, queue.Models[0].Status)
	assert.Equal(t, "run-failed", queue.Models[0].VerifiedRunID)
}

func TestRunRollout_StoppedRunLeavesEntryPendingAndEndsBatch(t *testing.T) {
	env, mirrorURL := newRolloutGateEnv(t)
	second := testQwenVariant()
	second.VariantId, second.ServedModelTag = "qwen3-8b", "qwen3:8b"
	second.ModelDigest = repeatTestHex('8')
	writeTestFrozenInventory(t, env.root, testQwenVariant(), second)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.mustRun(t, "rollout", "add", "qwen3:8b")
	env.cmd.SetOut(&bytes.Buffer{})
	env.cmd.SetErr(&bytes.Buffer{})

	started := 0
	gates := func(fs.RuntimeFileService, []*evalv1.ModelVariant) rolloutGateRunner {
		return func(_ *cobra.Command, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error) {
			started++
			return &rolloutRunSuccess{VariantID: entry.VariantID, Tag: entry.ServedModelTag, RunID: "run-stopped", Gate: gate}, constants.ErrEvaluationRunStopped
		}
	}
	err := runRolloutWith(env.cmd, env.deps, rolloutRunOptions{MirrorBootstrapURL: mirrorURL}, gates)
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	assert.Equal(t, 1, started, "a stop ends the batch before the next entry starts")

	queue, err := evaluation.LoadRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)
	require.Len(t, queue.Models, 2)
	for _, model := range queue.Models {
		assert.Equal(t, evaluation.QueueStatusPending, model.Status)
		assert.Empty(t, model.VerifiedRunID)
	}
}

func TestRunRollout_HoldsQueueLockForTheBatch(t *testing.T) {
	env, mirrorURL := newRolloutGateEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.cmd.SetOut(&bytes.Buffer{})
	env.cmd.SetErr(&bytes.Buffer{})

	var lockErr error
	gates := func(fs.RuntimeFileService, []*evalv1.ModelVariant) rolloutGateRunner {
		return func(_ *cobra.Command, entry evaluation.CampaignQueueModel, gate string) (*rolloutRunSuccess, error) {
			_, lockErr = evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
			return &rolloutRunSuccess{VariantID: entry.VariantID, Tag: entry.ServedModelTag, RunID: "run-failed", Gate: gate}, constants.ErrEvalRunVerificationFailed
		}
	}
	require.Error(t, runRolloutWith(env.cmd, env.deps, rolloutRunOptions{MirrorBootstrapURL: mirrorURL}, gates))
	require.ErrorIs(t, lockErr, constants.ErrRuntimeFileLocked)

	held, err := evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err, "the batch releases the queue lock when it ends")
	require.NoError(t, held.Close())
}

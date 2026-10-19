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
	"fmt"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// newRolloutGateEnv is the rollout test environment over the real eval stack. The
// run preflight checks the public mirror bootstrap, which the Gateway serves, so
// the returned URL is the real Gateway's.
func newRolloutGateEnv(t *testing.T) (*runEnv, string) {
	t.Helper()
	env, run := newGatewayRunEnvWithOptions(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine, EnablePublicSpectator: true})
	return env, run.stack.Gateway.PublicMirrorBootstrapURL
}

func TestRolloutAdd_QueuesModelsByTag(t *testing.T) {
	env := newStoreRunEnv(t)
	out := env.mustRun(t, "rollout", "add", "qwen3:4b")
	assert.Contains(t, out, "Queued qwen3:4b")
	assert.Contains(t, out, "1 model(s) in the rollout queue")

	var payload rolloutAddJSON
	require.NoError(t, env.runJSON(t, &payload, "rollout", "add", "qwen3:4b"))
	assert.Equal(t, "add", payload.Action)
	assert.Empty(t, payload.Models, "qwen3:4b is already in the queue")
	assert.Empty(t, payload.Skipped)
	assert.Equal(t, 1, payload.AlreadyQueued)
	assert.Equal(t, 1, payload.Remaining)
}

func TestRolloutAdd_RejectsEmptyRegistry(t *testing.T) {
	env := newStoreRunEnv(t)
	// Remove the test variant from the registry
	fileSvc := env.fileSvc(t)
	require.NoError(t, fileSvc.Remove(context.Background(), evaluation.DefaultModelInventoryRelPath))

	_, err := env.run(t, "rollout", "add", "--all")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationSelectionEmpty)
	assert.Contains(t, err.Error(), "registry is empty")
}

func TestRolloutAdd_RejectsUnknownModel(t *testing.T) {
	env := newStoreRunEnv(t)
	_, err := env.run(t, "rollout", "add", "unknown:model")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInferenceModelNotFound)
}

func TestRolloutRemove_DropsModelByTag(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	out := env.mustRun(t, "rollout", "remove", "qwen3:4b")
	assert.Contains(t, out, "Removed qwen3:4b")
	assert.Contains(t, out, "0 model(s) in the rollout queue")

	require.NoError(t, env.runJSON(t, &rolloutListJSON{}, "rollout", "list"))
}

func TestRolloutRemove_LastEntryDeletesQueueFile(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.mustRun(t, "rollout", "remove", "qwen3:4b")

	// Queue file should not exist after removing the last entry
	fileSvc := env.fileSvc(t)
	exists, err := fileSvc.FileExists(context.Background(), evaluation.DefaultInitCampaignQueueRelPath)
	require.NoError(t, err)
	assert.False(t, exists, "queue file deleted after removing last entry")
}

func TestRolloutRetry_ReturnsModelsToPending(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.mustRun(t, "rollout", "skip", "qwen3:4b")

	out := env.mustRun(t, "rollout", "retry", "qwen3:4b")
	assert.Contains(t, out, "Pending qwen3:4b")

	var payload rolloutListJSON
	require.NoError(t, env.runJSON(t, &payload, "rollout", "list", "--status", "pending"))
	require.Len(t, payload.Models, 1)
	assert.Equal(t, "qwen3:4b", payload.Models[0].ServedModelTag)
}

func TestRolloutSkip_ExcludesModelFromRuns(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	out := env.mustRun(t, "rollout", "skip", "qwen3:4b")
	assert.Contains(t, out, "Skipped qwen3:4b")

	var payload rolloutListJSON
	require.NoError(t, env.runJSON(t, &payload, "rollout", "list", "--status", "skipped"))
	require.Len(t, payload.Models, 1)
	assert.Equal(t, evaluation.QueueStatusSkipped, payload.Models[0].Status)
}

func TestRolloutNext_ReturnsFirstPendingModel(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")

	out := env.mustRun(t, "rollout", "next")
	assert.Contains(t, out, "Next pending model")
	assert.Contains(t, out, "qwen3:4b")
}

func TestRolloutNext_FailsWhenNoPendingModels(t *testing.T) {
	env := newStoreRunEnv(t)
	_, err := env.run(t, "rollout", "next")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestRolloutList_FiltersByStatus(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.mustRun(t, "rollout", "skip", "qwen3:4b")

	out := env.mustRun(t, "rollout", "list", "--status", "pending")
	assert.Contains(t, out, "No queue entries found")

	out = env.mustRun(t, "rollout", "list", "--status", "skipped")
	assert.Contains(t, out, "qwen3:4b")
	assert.Contains(t, out, "skipped")
}

func TestRolloutRun_DryRunDoesNotExecute(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")

	out := env.mustRun(t, "rollout", "run", "--dry-run")
	assert.Contains(t, out, "Rollout plan")
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

func TestRolloutAdd_FailsWhileQueueIsLocked(t *testing.T) {
	env := newStoreRunEnv(t)
	held, err := evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)

	_, err = env.run(t, "rollout", "add", "qwen3:4b")
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	_, err = env.run(t, "rollout", "skip", "qwen3:4b")
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	require.NoError(t, held.Close())

	queue, err := evaluation.LoadRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)
	assert.Empty(t, queue.Models, "a locked add changes nothing")
}

func TestRolloutRun_PromoteOnPassRequiresGateSmoke(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")

	_, err := env.run(t, "rollout", "run", "--promote-on-pass")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestRolloutRun_UntilCannotBeNegative(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")

	_, err := env.run(t, "rollout", "run", "--until", "-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestRequireRunServed_FailsWhenEveryAssignmentFailedAtTheProvider(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	ctx := context.Background()
	store := env.store(t)
	assignments, err := store.ListAssignments(ctx, "run-a-1")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(assignments), 2)

	saveLifecycle := func(assignment *evalv1.EvaluationAssignment, lifecycle evalv1.EvaluationAssignmentLifecycleStatus) {
		result := &evalv1.EvaluationAssignmentResult{
			SchemaVersion:   evaluation.CampaignSchemaVersion,
			AssignmentId:    assignment.GetAssignmentId(),
			RunId:           assignment.GetRunId(),
			CampaignId:      assignment.GetCampaignId(),
			Lane:            assignment.GetLane(),
			LifecycleStatus: lifecycle,
		}
		digest, err := evaluation.ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		result.ResultDigest = digest
		require.NoError(t, store.SaveAssignmentResult(ctx, result))
	}
	for _, assignment := range assignments {
		saveLifecycle(assignment, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED)
	}

	// One assignment sealed a trace carrying the attempt class; the others
	// failed before a trace existed.
	traceBody, err := evaluation.SealAgentTrace(&evaluation.AgentTrace{
		RunID: "run-a-1", AssignmentID: assignments[0].GetAssignmentId(), EvaluationAttemptID: "attempt-1",
		Input:                &evaluation.AgentTraceInput{Messages: []evaluation.AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Diagnose the outage."}}},
		ProviderFailure:      string(evaluation.ExecutionFailureProvider),
		ProviderFailureClass: "capability_unsupported",
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, "run-a-1", assignments[0].GetAssignmentId(), traceBody))

	servedErr := requireRunServed(ctx, env.fileSvc(t), rolloutGateFull, "run-a-1")
	require.ErrorIs(t, servedErr, constants.ErrEvaluationModelUnserved)
	assert.Equal(t, evaluation.ExecutionFailureProvider, evaluation.ClassifyExecutionFailure(servedErr))
	assert.Contains(t, servedErr.Error(), "(capability_unsupported)")

	// The queue note names the attempt class, not just provider_failure.
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	queue, err := evaluation.LoadInitCampaignQueueFromRuntime(ctx, env.fileSvc(t), evaluation.DefaultInitCampaignQueueRelPath)
	require.NoError(t, err)
	require.Len(t, queue.Models, 1)
	require.NoError(t, markRolloutFailure(ctx, env.fileSvc(t), queue.Models[0], []rolloutRunSuccess{{RunID: "run-a-1"}}, fmt.Errorf("full gate: %w", servedErr)))
	queue, err = evaluation.LoadInitCampaignQueueFromRuntime(ctx, env.fileSvc(t), evaluation.DefaultInitCampaignQueueRelPath)
	require.NoError(t, err)
	assert.Equal(t, evaluation.QueueStatusFailed, queue.Models[0].Status)
	assert.Equal(t, "run run-a-1: failed: provider_failure (capability_unsupported)", queue.Models[0].Notes)

	saveLifecycle(assignments[0], evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, requireRunServed(ctx, env.fileSvc(t), rolloutGateFull, "run-a-1"))
}

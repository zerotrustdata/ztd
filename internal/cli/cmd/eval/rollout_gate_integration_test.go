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

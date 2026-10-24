// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func TestRolloutResume_SelectsPendingCampaignsInQueueOrder(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "all pending", want: []string{"pending-a", "pending-b"}},
		{name: "specific campaign", args: []string{"pending-b"}, want: []string{"pending-b"}},
		{name: "queue order before limit", args: []string{"pending-b", "pending-a", "--until", "1"}, want: []string{"pending-a"}},
		{name: "duplicates run once", args: []string{"pending-b", "pending-b"}, want: []string{"pending-b"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newStoreRunEnv(t)
			queue := &evaluation.CampaignQueue{Models: []evaluation.CampaignQueueModel{
				{CampaignID: "failed", Status: evaluation.QueueStatusFailed},
				{CampaignID: "pending-a", Status: evaluation.QueueStatusPending},
				{CampaignID: "verified", Status: evaluation.QueueStatusVerified, VerifiedRunID: "missing-run"},
				{CampaignID: "skipped", Status: evaluation.QueueStatusSkipped},
				{CampaignID: "pending-b", Status: evaluation.QueueStatusPending},
			}}
			require.NoError(t, evaluation.SaveRolloutQueue(context.Background(), env.fileSvc(t), queue))

			var plan rolloutRunPlanJSON
			args := append([]string{"rollout", "resume", "--dry-run"}, test.args...)
			require.NoError(t, env.runJSON(t, &plan, args...))
			var got []string
			for _, entry := range plan.Models {
				got = append(got, entry.CampaignID)
				assert.Equal(t, evaluation.QueueStatusPending, entry.Status)
			}
			assert.Equal(t, test.want, got)
			after, err := evaluation.LoadRolloutQueue(context.Background(), env.fileSvc(t))
			require.NoError(t, err)
			assert.Equal(t, queue, after, "dry-run leaves every queue entry unchanged")
		})
	}
}

func TestRolloutResume_RejectsCampaignsThatAreNotPending(t *testing.T) {
	for _, id := range []string{"unknown", "failed", "verified", "skipped"} {
		t.Run(id, func(t *testing.T) {
			env := newStoreRunEnv(t)
			queue := &evaluation.CampaignQueue{Models: []evaluation.CampaignQueueModel{
				{CampaignID: "pending", Status: evaluation.QueueStatusPending},
				{CampaignID: "failed", Status: evaluation.QueueStatusFailed},
				{CampaignID: "verified", Status: evaluation.QueueStatusVerified},
				{CampaignID: "skipped", Status: evaluation.QueueStatusSkipped},
			}}
			require.NoError(t, evaluation.SaveRolloutQueue(context.Background(), env.fileSvc(t), queue))
			_, err := env.run(t, "rollout", "resume", "pending", id, "--dry-run", "--until", "1")
			require.ErrorIs(t, err, constants.ErrEvaluationSelectionEmpty)
		})
	}
}

func TestRolloutResume_NoPendingCampaignsDoesNotExecute(t *testing.T) {
	env := newStoreRunEnv(t)
	assert.Contains(t, env.mustRun(t, "rollout", "resume"), "No queue entries selected")
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	env.mustRun(t, "rollout", "skip", "qwen3:4b")
	assert.Contains(t, env.mustRun(t, "rollout", "resume"), "No queue entries selected")
	var plan rolloutRunPlanJSON
	require.NoError(t, env.runJSON(t, &plan, "rollout", "resume", "--dry-run"))
	assert.Empty(t, plan.Models)
	var result rolloutRunResult
	require.NoError(t, env.runJSON(t, &result, "rollout", "resume"))
	assert.Equal(t, rolloutRunResult{}, result)
}

func TestRolloutResume_UsesQueueLock(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	held, err := evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Close()) })
	_, err = env.run(t, "rollout", "resume")
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	assert.Contains(t, env.mustRun(t, "rollout", "resume", "--dry-run"), "Rollout plan")
}

func TestRolloutResume_ValidatesFlags(t *testing.T) {
	for _, args := range [][]string{{"--until", "-1"}, {"--promote-on-pass"}} {
		env := newStoreRunEnv(t)
		_, err := env.run(t, append([]string{"rollout", "resume"}, args...)...)
		require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	}
}

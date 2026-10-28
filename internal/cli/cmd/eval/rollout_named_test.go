// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

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
)

func newNamedRolloutEnv(t *testing.T) *runEnv {
	t.Helper()
	env := newStoreRunEnv(t)
	n := 0
	env.deps.newID = func() (string, error) { n++; return fmt.Sprintf("id-%d", n), nil }
	for _, id := range []string{"control", "longitudinal"} {
		env.mustRun(t, "campaigns", "create", id, "qwen3:4b", "--condition", id, "--data-operator", func() string {
			if id == "longitudinal" {
				return "operator-a"
			}
			return ""
		}())
	}
	return env
}

func loadTestRollout(t *testing.T, env *runEnv, name string) *evaluation.NamedRollout {
	t.Helper()
	rollout, err := evaluation.LoadNamedRollout(context.Background(), env.fileSvc(t), name)
	require.NoError(t, err)
	return rollout
}

func TestNamedRollout_DefineListShowAndUpdate(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "longitudinal", "control", "longitudinal")
	env.mustRun(t, "rollout", "create", "quick", "control")
	var listed rolloutListJSON
	require.NoError(t, env.runJSON(t, &listed, "rollout", "list"))
	require.Len(t, listed.Rollouts, 2)
	assert.Equal(t, "nightly", listed.Rollouts[0].Name)
	assert.Equal(t, "quick", listed.Rollouts[1].Name)
	assert.Equal(t, []string{"control"}, loadTestRollout(t, env, "quick").Campaigns, "different named definitions stay independent")
	var shown evaluation.NamedRollout
	require.NoError(t, env.runJSON(t, &shown, "rollout", "show", "nightly"))
	assert.Equal(t, []string{"longitudinal", "control", "longitudinal"}, shown.Campaigns)
	_, err := env.run(t, "rollout", "create", "nightly", "control")
	require.ErrorIs(t, err, constants.ErrAlreadyExists)
	env.mustRun(t, "rollout", "update", "nightly", "control", "longitudinal")
	_, err = env.run(t, "rollout", "update", "absent", "control")
	require.ErrorIs(t, err, constants.ErrNotFound)
}

func TestNamedRollout_ListSeparatesDefinitionsFromModelQueue(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	out := env.mustRun(t, "rollout", "list")
	assert.Contains(t, out, "No named rollouts found")
	assert.Contains(t, out, "rollout create <name> <campaign...>")
	assert.NotContains(t, out, "qwen3:4b")
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	env.mustRun(t, "rollout", "create", "quick", "control")
	out = env.mustRun(t, "rollout", "list")
	assert.Contains(t, out, "NAME")
	assert.Contains(t, out, "CAMPAIGNS")
	assert.Contains(t, out, "nightly")
	assert.Contains(t, out, "quick")
	assert.NotContains(t, out, "qwen3:4b")
	var definitions, queue rolloutListJSON
	require.NoError(t, env.runJSON(t, &definitions, "rollout", "list"))
	assert.Len(t, definitions.Rollouts, 2)
	assert.Empty(t, definitions.Models)
	require.NoError(t, env.runJSON(t, &queue, "rollout", "list", "--queue"))
	assert.Len(t, queue.Models, 1)
	assert.Empty(t, queue.Rollouts)
	out = env.mustRun(t, "rollout", "list", "--queue")
	assert.Contains(t, out, "qwen3:4b")
	assert.NotContains(t, out, "nightly")
	_, err := env.run(t, "rollout", "list", "--status", "pending")
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

func TestNamedRollout_ListDoesNotRequireModelQueue(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control")
	files := env.fileSvc(t)
	require.NoError(t, files.WriteFile(context.Background(), evaluation.DefaultInitCampaignQueueRelPath, []byte("invalid queue"), 0o600))
	out := env.mustRun(t, "rollout", "list")
	assert.Contains(t, out, "nightly")
	_, err := env.run(t, "rollout", "list", "--queue")
	require.Error(t, err)
}

func TestNamedRollout_DeleteRetainsCampaignsAndOtherRollouts(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	env.mustRun(t, "rollout", "create", "quick", "control")
	before, err := env.store(t).LoadCampaignSpec(context.Background(), "control")
	require.NoError(t, err)
	var deleted rolloutDeleteJSON
	require.NoError(t, env.runJSON(t, &deleted, "rollout", "delete", "nightly"))
	assert.Equal(t, rolloutDeleteJSON{Name: "nightly", Deleted: true}, deleted)
	_, err = evaluation.LoadNamedRollout(context.Background(), env.fileSvc(t), "nightly")
	require.ErrorIs(t, err, constants.ErrNotFound)
	assert.Equal(t, []string{"control"}, loadTestRollout(t, env, "quick").Campaigns)
	after, err := env.store(t).LoadCampaignSpec(context.Background(), "control")
	require.NoError(t, err)
	assert.Equal(t, before.GetCampaignDigest(), after.GetCampaignDigest())
	_, err = env.run(t, "rollout", "delete", "nightly")
	require.ErrorIs(t, err, constants.ErrNotFound)
	_, err = env.run(t, "rollout", "delete", "../quick")
	require.ErrorIs(t, err, constants.ErrEvidencePathElementInvalid)
	func() {
		held, err := evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
		require.NoError(t, err)
		defer func() { _ = held.Close() }()
		_, err = env.run(t, "rollout", "delete", "quick")
		require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
		assert.Equal(t, []string{"control"}, loadTestRollout(t, env, "quick").Campaigns)
	}()
	assert.Contains(t, env.mustRun(t, "rollout", "delete", "quick"), "Deleted rollout quick")
}

func TestNamedRollout_HelpLabelsCampaignPlaceholders(t *testing.T) {
	env := newNamedRolloutEnv(t)
	for _, verb := range []string{"create", "update"} {
		out := env.mustRun(t, "rollout", verb, "--help")
		assert.Contains(t, out, "campaign-1 and campaign-2 are placeholders")
		assert.Contains(t, out, "my-rollout campaign-1 campaign-2")
		assert.NotContains(t, out, "nightly smoke system")
	}
}

func TestNamedRollout_ValidatesEveryCampaignBeforeSavingOrRunning(t *testing.T) {
	env := newNamedRolloutEnv(t)
	_, err := env.run(t, "rollout", "create", "bad", "control", "missing")
	require.Error(t, err)
	_, err = evaluation.LoadNamedRollout(context.Background(), env.fileSvc(t), "bad")
	require.ErrorIs(t, err, constants.ErrNotFound)
	_, err = env.run(t, "rollout", "create", "../outside", "control")
	require.ErrorIs(t, err, constants.ErrEvidencePathElementInvalid)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	env.mustRun(t, "campaigns", "archive", "longitudinal")
	called := false
	err = runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Until: 1}, func(*cobra.Command, string, string, bool) error {
		called = true
		return nil
	})
	require.ErrorIs(t, err, constants.ErrEvaluationArchived)
	assert.False(t, called)
	assert.Empty(t, loadTestRollout(t, env, "nightly").Executions)
}

func TestNamedRollout_DryRunRepeatsSequenceWithoutWriting(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal", "control")
	var plan namedRolloutPlan
	require.NoError(t, env.runJSON(t, &plan, "rollout", "run", "nightly", "--repeat", "2", "--until", "5", "--dry-run"))
	assert.Equal(t, []string{"control", "longitudinal", "control", "control", "longitudinal"}, plan.Campaigns)
	assert.Empty(t, loadTestRollout(t, env, "nightly").Executions)
	for _, args := range [][]string{
		{"run", "nightly", "--repeat", "0"},
		{"run", "nightly", "--gate-smoke"},
		{"run", "nightly", "--skip-verified=false"},
		{"run", "--repeat", "2"},
	} {
		_, err := env.run(t, append([]string{"rollout"}, args...)...)
		require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	}
}

func TestNamedRollout_MixesSuitesConditionsAndFormationLane(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "campaigns", "create", "held", "qwen3:4b", "--condition", "held-context", "--reps", "2", "--suite", evaluation.SmokeSuiteID)
	variants := evaluation.FormationCatalogFixtureVariants(func(string) string { return repeatTestHex('a') })
	writeTestFrozenInventory(t, env.root, variants...)
	env.mustRun(t, "campaigns", "create", "system", "--formations", "ultra-efficient-speedster")
	env.mustRun(t, "rollout", "create", "mixed", "control", "system", "held", "longitudinal")
	var plan namedRolloutPlan
	require.NoError(t, env.runJSON(t, &plan, "rollout", "run", "mixed", "--dry-run"))
	assert.Equal(t, []string{"control", "system", "held", "longitudinal"}, plan.Campaigns)
	for _, id := range plan.Campaigns {
		before, err := env.store(t).LoadCampaignSpec(context.Background(), id)
		require.NoError(t, err)
		require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "mixed", namedRolloutOptions{Repeat: 1, DryRun: true}, nil))
		after, err := env.store(t).LoadCampaignSpec(context.Background(), id)
		require.NoError(t, err)
		assert.Equal(t, before.GetCampaignDigest(), after.GetCampaignDigest())
	}
}

func TestNamedRollout_OrderLimitResumeSnapshotAndFreshRerun(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal", "control")
	var campaigns, runIDs []string
	runner := func(_ *cobra.Command, campaignID, runID string, resume bool) error {
		assert.False(t, resume)
		// The reference must be durable before entering governed execution.
		saved := loadTestRollout(t, env, "nightly")
		steps := saved.Executions[len(saved.Executions)-1].Steps
		found := false
		for _, step := range steps {
			if step.Status == evaluation.RolloutStepRunning && step.Attempts[len(step.Attempts)-1].RunID == runID {
				found = true
			}
		}
		assert.True(t, found)
		campaigns, runIDs = append(campaigns, campaignID), append(runIDs, runID)
		return nil
	}
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Until: 1}, runner))
	env.mustRun(t, "rollout", "update", "nightly", "longitudinal")
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Resume: true}, runner))
	assert.Equal(t, []string{"control", "longitudinal", "control"}, campaigns, "resume retains original definition snapshot")
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 2}, runner))
	assert.Equal(t, []string{"control", "longitudinal", "control", "longitudinal", "longitudinal"}, campaigns)
	assert.Len(t, loadTestRollout(t, env, "nightly").Executions, 2)
	assert.NotEqual(t, runIDs[1], runIDs[3], "reruns allocate fresh IDs even within the same second")
}

func TestNamedRollout_FailurePolicyAndRetry(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	var called []string
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1}, func(_ *cobra.Command, campaignID, _ string, _ bool) error {
		called = append(called, campaignID)
		if campaignID == "control" {
			return fmt.Errorf("private provider text: %w", constants.ErrEvalRunVerificationFailed)
		}
		return nil
	}))
	assert.Equal(t, []string{"control", "longitudinal"}, called)
	rollout := loadTestRollout(t, env, "nightly")
	assert.Equal(t, evaluation.ExecutionFailureVerification, rollout.Executions[0].Steps[0].Attempts[0].FailureClass)
	assert.NotContains(t, env.mustRun(t, "rollout", "show", "nightly"), "private provider text")
	called = nil
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Resume: true}, func(_ *cobra.Command, campaignID, _ string, resume bool) error {
		assert.False(t, resume, "failed campaigns start new runs")
		called = append(called, campaignID)
		return nil
	}))
	assert.Equal(t, []string{"control"}, called, "already successful later steps stay excluded")
	assert.Len(t, loadTestRollout(t, env, "nightly").Executions[0].Steps[0].Attempts, 2)
}

func TestNamedRollout_StopResumesSameRunAndNeverStartsLaterCampaign(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	var stoppedID string
	err := runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1}, func(_ *cobra.Command, campaignID, runID string, resume bool) error {
		assert.Equal(t, "control", campaignID)
		assert.False(t, resume)
		stoppedID = runID
		return constants.ErrEvaluationRunStopped
	})
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	saved := loadTestRollout(t, env, "nightly")
	assert.Equal(t, evaluation.RolloutStepStopped, saved.Executions[0].Steps[0].Status)
	assert.Equal(t, evaluation.RolloutStepPending, saved.Executions[0].Steps[1].Status)
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Resume: true}, func(_ *cobra.Command, campaignID, runID string, resume bool) error {
		if campaignID == "control" {
			assert.True(t, resume)
			assert.Equal(t, stoppedID, runID)
		} else {
			assert.False(t, resume)
		}
		return nil
	}))
	assert.Len(t, loadTestRollout(t, env, "nightly").Executions[0].Steps[0].Attempts, 1)
}

func TestNamedRollout_LockExcludesEditsAndOtherRunsButAllowsDryRun(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control")
	held, err := evaluation.LockRolloutQueue(context.Background(), env.fileSvc(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, held.Close()) })
	for _, args := range [][]string{
		{"rollout", "run", "nightly"},
		{"rollout", "resume", "nightly"},
		{"rollout", "create", "other", "control"},
		{"rollout", "update", "nightly", "longitudinal"},
	} {
		_, err := env.run(t, args...)
		require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	}
	var out bytes.Buffer
	env.cmd.SetOut(&out)
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, DryRun: true}, nil))
	assert.Contains(t, out.String(), "control")
}

func TestNamedRollout_ResumeGuardsRetainRunAndStopBatch(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	var originalID string
	err := runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1}, func(_ *cobra.Command, _, runID string, _ bool) error {
		originalID = runID
		return constants.ErrEvaluationRunStopped
	})
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	for _, guard := range []error{constants.ErrEvaluationRunLeaseHeld, constants.ErrEvaluationAssignmentUnresolved, constants.ErrEvaluationRunGovernanceChanged} {
		calls := 0
		err := runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Resume: true}, func(_ *cobra.Command, campaignID, runID string, resume bool) error {
			calls++
			assert.True(t, resume)
			assert.Equal(t, originalID, runID)
			assert.Equal(t, "control", campaignID)
			return guard
		})
		require.ErrorIs(t, err, guard)
		assert.Equal(t, 1, calls)
		assert.Len(t, loadTestRollout(t, env, "nightly").Executions[0].Steps[0].Attempts, 1)
	}
}

func TestNamedRollout_RequiresCompletePopulation(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "campaign", "partial-run")
	require.ErrorIs(t, requireNamedRolloutCoverage(env.cmd, env.deps, env.store(t), "partial-run"), constants.ErrEvalRunVerificationFailed)
}

func TestNamedRollout_CanceledCampaignEndsBatchAndRetriesFresh(t *testing.T) {
	env := newNamedRolloutEnv(t)
	env.mustRun(t, "rollout", "create", "nightly", "control", "longitudinal")
	var canceledID string
	err := runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1}, func(_ *cobra.Command, campaignID, runID string, _ bool) error {
		assert.Equal(t, "control", campaignID)
		canceledID = runID
		return &rolloutCanceledCampaignError{RunID: runID}
	})
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	saved := loadTestRollout(t, env, "nightly")
	assert.Equal(t, evaluation.RolloutStepFailed, saved.Executions[0].Steps[0].Status)
	assert.Equal(t, evaluation.RolloutStepPending, saved.Executions[0].Steps[1].Status)
	require.NoError(t, runNamedRollout(env.cmd, env.deps, env.fileSvc(t), "nightly", namedRolloutOptions{Repeat: 1, Resume: true, Until: 1}, func(_ *cobra.Command, campaignID, runID string, resume bool) error {
		assert.Equal(t, "control", campaignID)
		assert.False(t, resume, "canceled assignments cannot resume in the existing run")
		assert.NotEqual(t, canceledID, runID)
		return nil
	}))
}

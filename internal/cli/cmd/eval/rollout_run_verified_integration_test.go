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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// skipVerifiedRolloutPlan dry-runs the queue with --skip-verified and returns
// the variant IDs the plan would start.
func skipVerifiedRolloutPlan(t *testing.T, env *runEnv) []string {
	t.Helper()
	opts := rolloutRunOptions{SkipVerified: true, DryRun: true}
	var out bytes.Buffer
	env.cmd.SetOut(&out)
	env.cmd.SetErr(&bytes.Buffer{})
	require.NoError(t, runRollout(env.cmd, env.deps, opts))
	var planned []string
	for _, line := range strings.Split(out.String(), "\n") {
		entry, ok := strings.CutPrefix(line, "- ")
		if !ok {
			continue
		}
		variantID, _, _ := strings.Cut(entry, " ")
		planned = append(planned, variantID)
	}
	return planned
}

func TestRolloutRun_SkipVerifiedSkipsAnEntryVerifiedOnTheCurrentCatalog(t *testing.T) {
	env := newStoreRunEnv(t)
	markVerifiedOn(t, env, evaluation.DefaultSuiteID)

	assert.Empty(t, skipVerifiedRolloutPlan(t, env))
}

func TestRolloutRun_SkipVerifiedRequalifiesAnEntryVerifiedOnAnOlderCatalog(t *testing.T) {
	env := newStoreRunEnv(t)
	// The smoke suite stands in for a catalog that is no longer the built-in one.
	markVerifiedOn(t, env, evaluation.SmokeSuiteID)

	assert.Equal(t, []string{"qwen3-4b"}, skipVerifiedRolloutPlan(t, env))
}

func TestRolloutRun_SkipVerifiedRequalifiesAnEntryWhoseVerifiedRunIsGone(t *testing.T) {
	env := newStoreRunEnv(t)
	env.mustRun(t, "rollout", "add", "qwen3:4b")
	_, err := evaluation.MarkCampaignQueueEntry(evaluation.MarkCampaignQueueEntryRequest{
		FileService:   env.fileSvc(t),
		VariantID:     "qwen3-4b",
		Status:        evaluation.QueueStatusVerified,
		VerifiedRunID: "run-deleted",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"qwen3-4b"}, skipVerifiedRolloutPlan(t, env))
}

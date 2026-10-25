// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestNamedRolloutStorage_EncryptedBackupRoundTrip(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	rollout := &NamedRollout{
		RolloutDefinition: RolloutDefinition{Name: "nightly", Campaigns: []string{"control", "longitudinal"}},
		Executions: []RolloutExecution{{ID: "batch-1", StartedAt: time.Unix(1234, 0).UTC(), Steps: []RolloutStep{
			{CampaignID: "control", Status: RolloutStepFailed, Attempts: []RolloutAttempt{{RunID: "run-1", FailureClass: ExecutionFailureProvider}}},
			{CampaignID: "longitudinal", Status: RolloutStepPending},
		}}},
	}
	require.NoError(t, SaveNamedRollout(ctx, f.files, rollout))
	stored, err := f.raw.ReadFile(ctx, rolloutDefinitionPath(rollout.Name))
	require.NoError(t, err)
	assert.NotContains(t, string(stored), "longitudinal", "definitions and history must be encrypted by the evidence file service")
	report := f.create(t)
	assert.Contains(t, reportPaths(report.Files), rolloutDefinitionPath(rollout.Name))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	_, err = f.backup.Restore(ctx, report.SnapshotDir, false)
	require.NoError(t, err)
	loaded, err := LoadNamedRollout(ctx, f.files, rollout.Name)
	require.NoError(t, err)
	assert.Equal(t, rollout, loaded)
}

func TestNamedRolloutStorage_RejectsUnsafeOrInconsistentRecords(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		name string
		body string
	}{
		{"name mismatch", `{"name":"other","campaigns":["camp"]}`},
		{"empty definition", `{"name":"nightly","campaigns":[]}`},
		{"unsafe campaign", `{"name":"nightly","campaigns":["../camp"]}`},
		{"unknown step status", `{"name":"nightly","campaigns":["camp"],"executions":[{"id":"batch","steps":[{"campaign_id":"camp","status":"unknown"}]}]}`},
		{"missing run reference", `{"name":"nightly","campaigns":["camp"],"executions":[{"id":"batch","steps":[{"campaign_id":"camp","status":"succeeded"}]}]}`},
		{"unsafe run reference", `{"name":"nightly","campaigns":["camp"],"executions":[{"id":"batch","steps":[{"campaign_id":"camp","status":"stopped","attempts":[{"run_id":"../run"}]}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.write(t, rolloutDefinitionPath("nightly"), test.body)
			_, err := LoadNamedRollout(ctx, f.files, "nightly")
			require.Error(t, err)
		})
	}
	_, err := LoadNamedRollout(ctx, f.files, "../nightly")
	require.ErrorIs(t, err, constants.ErrEvidencePathElementInvalid)
}

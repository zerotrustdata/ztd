// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	ztdfs "github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func retiredBindingTestRun(runID string) *evalv1.EvaluationRun {
	return &evalv1.EvaluationRun{
		SchemaVersion: CampaignSchemaVersion,
		RunId:         runID,
		CampaignBinding: &evalv1.ModelCampaignBinding{
			CampaignId:                 "campaign-a",
			CampaignDigest:             "sha256:campaign",
			InferenceOperatorSessionId: "inference-session",
		},
		StartedAt: timestamppb.Now(),
	}
}

// writeRunRecordWithRetiredBinding stores run the way a binary before the
// harness roster wrote it: canonical, with ModelCampaignBinding field 7
// data_operator_session_id after field 6. extra is spliced in beside it.
func writeRunRecordWithRetiredBinding(t *testing.T, files ztdfs.RuntimeFileService, run *evalv1.EvaluationRun, extra string) []byte {
	t.Helper()
	body, err := evalv1.MarshalCanonical(run)
	require.NoError(t, err)
	field := `"inference_operator_session_id":"` + run.GetCampaignBinding().GetInferenceOperatorSessionId() + `"`
	require.Contains(t, string(body), field)
	legacy := []byte(strings.Replace(string(body), field, field+`,"data_operator_session_id":"retired-data-session"`+extra, 1))
	dir := path.Join(constants.EvaluationRunsPath, run.GetRunId())
	require.NoError(t, files.MkdirAll(context.Background(), dir, constants.PermDirPrivate))
	require.NoError(t, files.WriteFile(context.Background(), path.Join(dir, constants.EvaluationRunStateFilename), legacy, constants.PermFilePrivate))
	return legacy
}

func TestStore_RepairRunBindingRemovesRetiredField(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := retiredBindingTestRun("run-retired")
	writeRunRecordWithRetiredBinding(t, files, run, "")

	_, err := store.LoadRun(ctx, run.GetRunId())
	require.ErrorIs(t, err, constants.ErrEvaluationRunRetiredBindingField)
	assert.Contains(t, err.Error(), run.GetRunId())

	repaired, err := store.RepairRunBinding(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.True(t, repaired)

	loaded, err := store.LoadRun(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.True(t, proto.Equal(run, loaded), "repair keeps every other field of the run")

	repaired, err = store.RepairRunBinding(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.False(t, repaired, "a current record needs no repair")
}

func TestStore_RepairRunBindingRefusesOtherUnknownContent(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := retiredBindingTestRun("run-retired-extra")
	legacy := writeRunRecordWithRetiredBinding(t, files, run, `,"unknown_binding_field":"x"`)

	repaired, err := store.RepairRunBinding(ctx, run.GetRunId())
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.False(t, repaired)
	stored, err := files.ReadFile(ctx, path.Join(constants.EvaluationRunsPath, run.GetRunId(), constants.EvaluationRunStateFilename))
	require.NoError(t, err)
	assert.Equal(t, legacy, stored, "a refused repair leaves the record unchanged")
}

func TestStore_LoadRunKeepsMalformedErrorForOtherDecodeFailures(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	dir := path.Join(constants.EvaluationRunsPath, "run-garbage")
	require.NoError(t, files.MkdirAll(ctx, dir, constants.PermDirPrivate))
	require.NoError(t, files.WriteFile(ctx, path.Join(dir, constants.EvaluationRunStateFilename), []byte("not a run"), constants.PermFilePrivate))

	_, err := NewStore(files).LoadRun(ctx, "run-garbage")
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.NotErrorIs(t, err, constants.ErrEvaluationRunRetiredBindingField)
}

func TestStore_ListRunIDsNamesEveryRunWithRetiredBinding(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	require.NoError(t, store.SaveRun(ctx, retiredBindingTestRun("run-current")))
	writeRunRecordWithRetiredBinding(t, files, retiredBindingTestRun("run-old-1"), "")
	writeRunRecordWithRetiredBinding(t, files, retiredBindingTestRun("run-old-2"), "")

	_, err := store.ListRunIDs(ctx)
	require.ErrorIs(t, err, constants.ErrEvaluationRunRetiredBindingField)
	assert.Contains(t, err.Error(), "run-old-1")
	assert.Contains(t, err.Error(), "run-old-2")
	_, err = RunFeedVerificationDrift(ctx, store, map[string]string{"run-current": "verified"})
	require.ErrorIs(t, err, constants.ErrEvaluationRunRetiredBindingField, "public status reports the drift check as not run")

	for _, runID := range []string{"run-old-1", "run-old-2"} {
		repaired, err := store.RepairRunBinding(ctx, runID)
		require.NoError(t, err)
		require.True(t, repaired)
	}
	runIDs, err := store.ListRunIDs(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{"run-current", "run-old-1", "run-old-2"}, runIDs)
	_, err = RunFeedVerificationDrift(ctx, store, map[string]string{"run-current": "verified"})
	require.NoError(t, err)
}

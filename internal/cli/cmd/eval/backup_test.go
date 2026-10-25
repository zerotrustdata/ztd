// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestBackupRestore_RoundTripAfterWipe(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	fileSvc := env.fileSvc(t)
	ctx := context.Background()
	backupRoot := filepath.Join(testutil.TempDir(t), "my-eval-backups")

	var created evalBackupJSON
	require.NoError(t, env.runJSON(t, &created, "backup", "--output-dir", backupRoot))
	require.NotEmpty(t, created.Files)
	assert.Equal(t, len(created.Files), created.FileCount)
	assert.Equal(t, backupRoot, filepath.Dir(created.SnapshotDir))

	recordRel := filepath.ToSlash(filepath.Join(constants.EvaluationDataPath, constants.EvaluationRunsDirname, "run-a-1", constants.ConformanceRecordFilename))
	want, err := fileSvc.ReadFile(ctx, recordRel)
	if err != nil {
		recordRel = created.Files[0].Path
		want, err = fileSvc.ReadFile(ctx, recordRel)
	}
	require.NoError(t, err)

	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDirname))

	var restored evalRestoreJSON
	require.NoError(t, env.runJSON(t, &restored, "restore", created.SnapshotDir))
	assert.Len(t, restored.Restored, created.FileCount)
	assert.Empty(t, restored.Unchanged)

	got, err := fileSvc.ReadFile(ctx, recordRel)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestBackup_RejectsOutputDirInsideRuntime(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "backup", "--output-dir", env.fileSvc(t).Resolve("eval-backups"))
	assert.ErrorIs(t, err, constants.ErrEvaluationBackupDestinationInvalid)
}

func TestBackupRestore_DefaultsToProjectEvalBackups(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	fileSvc := env.fileSvc(t)
	ctx := context.Background()

	var created evalBackupJSON
	require.NoError(t, env.runJSON(t, &created, "backup"))
	assert.Equal(t, filepath.Join(env.root, "eval", "backups"), filepath.Dir(created.SnapshotDir))

	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDirname))

	var restored evalRestoreJSON
	require.NoError(t, env.runJSON(t, &restored, "restore"))
	assert.Equal(t, filepath.Dir(created.SnapshotDir), restored.SnapshotDir, "a default restore reports the backup directory it merged")
	assert.Equal(t, 1, restored.SnapshotCount)
	assert.Len(t, restored.Restored, created.FileCount)
}

// The newest snapshot is not a complete restore: evidence deleted or archived
// since an older snapshot is only in that older one.
func TestRestore_DefaultMergesEverySnapshotIncludingEvidenceOnlyAnOlderOneHolds(t *testing.T) {
	env := newStoreRunEnv(t)
	fileSvc := env.fileSvc(t)
	ctx := context.Background()

	env.prepareStoredRun(t, "eval-a", "run-a-1")
	var first evalBackupJSON
	require.NoError(t, env.runJSON(t, &first, "backup"))

	env.prepareStoredRun(t, "eval-b", "run-b-1")
	require.NoError(t, fileSvc.RemoveAll(ctx, path.Join(constants.EvaluationDataPath, constants.EvaluationCampaignsDirname, "eval-a")))
	require.NoError(t, fileSvc.RemoveAll(ctx, path.Join(constants.EvaluationDataPath, constants.EvaluationRunsDirname, "run-a-1")))
	var second evalBackupJSON
	require.NoError(t, env.runJSON(t, &second, "backup"))
	require.NotEqual(t, first.SnapshotDir, second.SnapshotDir)

	inSecond := map[string]bool{}
	for _, file := range second.Files {
		inSecond[file.Path] = true
	}
	var onlyInFirst []string
	for _, file := range first.Files {
		if !inSecond[file.Path] {
			onlyInFirst = append(onlyInFirst, file.Path)
		}
	}
	require.NotEmpty(t, onlyInFirst, "the fixture must leave evidence only the older snapshot holds")

	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, fileSvc.RemoveAll(ctx, constants.EvaluationDirname))

	var restored evalRestoreJSON
	require.NoError(t, env.runJSON(t, &restored, "restore"))

	assert.Equal(t, 2, restored.SnapshotCount)
	got := map[string]bool{}
	for _, file := range restored.Restored {
		got[file.Path] = true
	}
	for _, rel := range onlyInFirst {
		assert.True(t, got[rel], "%s exists only in the older snapshot and must be restored", rel)
		_, err := fileSvc.ReadFile(ctx, rel)
		require.NoError(t, err, rel)
	}
	for _, file := range second.Files {
		assert.True(t, got[file.Path], "%s is in the newest snapshot and must be restored", file.Path)
	}
}

func TestRestore_WithoutSnapshotAndNoDefaultBackupsFails(t *testing.T) {
	env := newStoreRunEnv(t)

	_, err := env.run(t, "restore")

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupNone)
}

func TestRunsStart_DoesNotBackUpEvidence(t *testing.T) {
	env := newGatewayRunEnv(t)
	env.createCampaign(t, "eval-a")

	out := env.mustRun(t, "runs", "start", "eval-a", "--daemon=false", "--publish=false")

	assert.Contains(t, out, "Executed 1 assignment(s)")
	assert.NotContains(t, out, "backed up")
	assert.NoDirExists(t, filepath.Join(env.root, "eval", "backups"))
}

func TestRunsResume_DoesNotBackUpEvidence(t *testing.T) {
	env := newGatewayRunEnv(t)
	runID := env.prepareRun(t)

	out := env.mustRun(t, "runs", "resume", runID)

	assert.Contains(t, out, "Executed 1 assignment(s)")
	assert.NotContains(t, out, "backed up")
	assert.NoDirExists(t, filepath.Join(env.root, "eval", "backups"))
}

func TestRunsResume_MissingRunDoesNotBackUpOtherEvidence(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "resume", "missing-run")

	require.Error(t, err)
	assert.NoDirExists(t, filepath.Join(env.root, "eval", "backups"))
}

func TestRestore_RejectsConflictingEvidenceWithoutOverwrite(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	backupRoot := filepath.Join(testutil.TempDir(t), "backups")
	var created evalBackupJSON
	require.NoError(t, env.runJSON(t, &created, "backup", "--output-dir", backupRoot))
	require.NoError(t, env.fileSvc(t).WriteFile(context.Background(), created.Files[0].Path, []byte("diverged"), constants.PermFilePrivate))

	_, err := env.run(t, "restore", created.SnapshotDir)
	require.ErrorIs(t, err, constants.ErrEvaluationBackupConflict)

	_, err = env.run(t, "restore", created.SnapshotDir, "--overwrite")
	require.NoError(t, err)
}

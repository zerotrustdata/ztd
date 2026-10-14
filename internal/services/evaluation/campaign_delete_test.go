// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// recordingPublicationStateStore records which runs had their publication
// discarded and can fail a discard.
type recordingPublicationStateStore struct {
	*memoryCampaignPublicationStateStore
	discarded []string
	failOn    string
}

func (s *recordingPublicationStateStore) Delete(ctx context.Context, runID string) error {
	if runID == s.failOn {
		return constants.ErrPublicFeedDisabled
	}
	s.discarded = append(s.discarded, runID)
	return s.memoryCampaignPublicationStateStore.Delete(ctx, runID)
}

func (f *archiveFixture) deleter(t *testing.T, live bool, publication CampaignPublicationStateStore) *Deleter {
	t.Helper()
	return f.deleterWithBackups(live, publication, filepath.Join(testutil.TempDir(t), "backups"))
}

func (f *archiveFixture) deleterWithBackups(live bool, publication CampaignPublicationStateStore, backupDir string) *Deleter {
	return NewDeleter(f.files, func(RunLease) bool { return live }, publication, backupDir)
}

// backUp snapshots the fixture's evidence into a fresh backup directory.
func (f *archiveFixture) backUp(t *testing.T) string {
	t.Helper()
	backupDir := filepath.Join(testutil.TempDir(t), "backups")
	_, err := NewEvalBackup(f.files, func() time.Time { return archiveTestNow }).Create(context.Background(), backupDir)
	require.NoError(t, err)
	return backupDir
}

// backedUpPaths lists every path any snapshot in backupDir holds.
func backedUpPaths(t *testing.T, backupDir string) []string {
	t.Helper()
	snapshots, err := AllEvalBackupSnapshots(backupDir)
	if errors.Is(err, constants.ErrEvaluationBackupNone) {
		return nil
	}
	require.NoError(t, err)
	var paths []string
	for _, snapshot := range snapshots {
		manifest, err := readEvalBackupManifest(snapshot)
		require.NoError(t, err)
		paths = append(paths, reportPaths(manifest.Files)...)
	}
	return paths
}

func assertNoBackedUpPathUnder(t *testing.T, backupDir string, roots ...string) {
	t.Helper()
	for _, backedUp := range backedUpPaths(t, backupDir) {
		for _, root := range roots {
			root = filepath.ToSlash(root)
			assert.False(t, backedUp == root || strings.HasPrefix(backedUp, root+"/"), "%s is still backed up", backedUp)
		}
	}
}

func assertBackedUpPathUnder(t *testing.T, backupDir, root string) {
	t.Helper()
	root = filepath.ToSlash(root)
	for _, backedUp := range backedUpPaths(t, backupDir) {
		if strings.HasPrefix(backedUp, root+"/") {
			return
		}
	}
	t.Errorf("nothing under %s is backed up", root)
}

func newRecordingPublicationStateStore() *recordingPublicationStateStore {
	return &recordingPublicationStateStore{memoryCampaignPublicationStateStore: NewMemoryCampaignPublicationStateStore()}
}

func TestDeleterDeleteRunDiscardsPublicationAndRemovesRun(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	publication := newRecordingPublicationStateStore()

	report, err := f.deleter(t, false, publication).DeleteRun(ctx, "run-a")
	require.NoError(t, err)
	assert.Equal(t, f.req.CampaignID, report.CampaignID)
	assert.Equal(t, []string{"run-a"}, report.RunIDs)
	assert.False(t, report.BackupsOnly)
	assert.Equal(t, []string{"run-a"}, publication.discarded)

	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.ErrorIs(t, err, constants.ErrNotFound)
	_, _, err = LocateRun(ctx, f.files, "run-b")
	require.NoError(t, err)
}

func TestDeleterDeleteRunRemovesArchivedRun(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	_, err := f.archiver(false).ArchiveRun(ctx, "run-a", "operator-1")
	require.NoError(t, err)

	_, err = f.deleter(t, false, newRecordingPublicationStateStore()).DeleteRun(ctx, "run-a")
	require.NoError(t, err)

	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.ErrorIs(t, err, constants.ErrNotFound)
	exists, err := f.files.FileExists(ctx, archiveManifestPath(archivedRunDir("run-a")))
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDeleterDeleteRunRejectsRunningRun(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	f.holdLease(t, "run-a")
	publication := newRecordingPublicationStateStore()

	_, err := f.deleter(t, true, publication).DeleteRun(ctx, "run-a")
	require.ErrorIs(t, err, constants.ErrEvaluationRunRunning)
	assert.Empty(t, publication.discarded)
	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.NoError(t, err)
}

func TestDeleterDeleteRunKeepsEvidenceWhenDiscardFails(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	publication := newRecordingPublicationStateStore()
	publication.failOn = "run-a"
	backupDir := f.backUp(t)
	before := backedUpPaths(t, backupDir)

	_, err := f.deleterWithBackups(false, publication, backupDir).DeleteRun(ctx, "run-a")
	require.Error(t, err)
	assert.True(t, errors.Is(err, constants.ErrPublicFeedDisabled))
	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.NoError(t, err)
	assert.Equal(t, before, backedUpPaths(t, backupDir), "a failed discard leaves the backups untouched")
}

func TestDeleterDeleteCampaignRemovesRunsCampaignAndInventory(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	_, err := f.archiver(false).ArchiveRun(ctx, "run-b", "operator-1")
	require.NoError(t, err)
	inventory := campaignInventoryPath(DefaultCampaignInventoryRelDirname, f.req.CampaignID)
	require.NoError(t, f.files.WriteFile(ctx, inventory, []byte("{}"), constants.PermFilePrivate))
	publication := newRecordingPublicationStateStore()

	report, err := f.deleter(t, false, publication).DeleteCampaign(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"run-a", "run-b"}, report.RunIDs)
	assert.ElementsMatch(t, []string{"run-a", "run-b"}, publication.discarded)

	_, _, err = LocateCampaign(ctx, f.files, f.req.CampaignID)
	require.ErrorIs(t, err, constants.ErrNotFound)
	for _, runID := range f.runIDs {
		_, _, err = LocateRun(ctx, f.files, runID)
		require.ErrorIs(t, err, constants.ErrNotFound)
	}
	exists, err := f.files.FileExists(ctx, inventory)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDeleterDeleteCampaignRemovesArchivedCampaign(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	_, err := f.archiver(false).ArchiveCampaign(ctx, f.req.CampaignID, "operator-1")
	require.NoError(t, err)

	report, err := f.deleter(t, false, newRecordingPublicationStateStore()).DeleteCampaign(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.ElementsMatch(t, f.runIDs, report.RunIDs)
	_, _, err = LocateCampaign(ctx, f.files, f.req.CampaignID)
	require.ErrorIs(t, err, constants.ErrNotFound)
	exists, err := f.files.FileExists(ctx, archiveManifestPath(archivedCampaignDir(f.req.CampaignID)))
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestDeleterDeleteCampaignFailsBeforeDeletingAnythingWhenARunIsRunning(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	f.holdLease(t, "run-b")
	publication := newRecordingPublicationStateStore()

	_, err := f.deleter(t, true, publication).DeleteCampaign(ctx, f.req.CampaignID)
	require.ErrorIs(t, err, constants.ErrEvaluationRunRunning)
	assert.Empty(t, publication.discarded)
	for _, runID := range f.runIDs {
		_, _, err = LocateRun(ctx, f.files, runID)
		require.NoError(t, err)
	}
}

func TestDeleterDeleteRunPurgesItFromBackups(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	backupDir := f.backUp(t)
	assertBackedUpPathUnder(t, backupDir, activeEvalLayout().runDir("run-a"))

	report, err := f.deleterWithBackups(false, newRecordingPublicationStateStore(), backupDir).DeleteRun(ctx, "run-a")
	require.NoError(t, err)
	assert.Positive(t, report.Backups.Files)
	assert.Equal(t, 1, report.Backups.Snapshots)
	assertNoBackedUpPathUnder(t, backupDir, runEvidencePaths("run-a")...)
	assertBackedUpPathUnder(t, backupDir, activeEvalLayout().runDir("run-b"))

	restored, err := NewEvalBackup(f.files, time.Now).RestoreAll(ctx, backupDir, false)
	require.NoError(t, err)
	assert.Empty(t, restored.Restored, "restore never brings a deleted run back")
	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.ErrorIs(t, err, constants.ErrNotFound)
}

func TestDeleterDeleteRunThatIsOnlyInBackups(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	backupDir := f.backUp(t)
	require.NoError(t, f.files.RemoveAll(ctx, activeEvalLayout().runDir("run-a")))
	publication := newRecordingPublicationStateStore()

	report, err := f.deleterWithBackups(false, publication, backupDir).DeleteRun(ctx, "run-a")
	require.NoError(t, err)
	assert.True(t, report.BackupsOnly)
	assert.Equal(t, []string{"run-a"}, report.RunIDs)
	assert.Equal(t, []string{"run-a"}, publication.discarded, "a run only in the backups is still withdrawn")
	assertNoBackedUpPathUnder(t, backupDir, runEvidencePaths("run-a")...)
}

func TestDeleterDeleteRunInNeitherRuntimeNorBackupsIsNotFound(t *testing.T) {
	f := newArchiveFixture(t)
	publication := newRecordingPublicationStateStore()

	_, err := f.deleterWithBackups(false, publication, f.backUp(t)).DeleteRun(context.Background(), "run-missing")
	require.ErrorIs(t, err, constants.ErrNotFound)
	assert.Empty(t, publication.discarded)
}

func TestDeleterDeleteRunFailsWhileABackupIsInProgress(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	backup, err := f.files.TryLockShared(ctx, constants.EvaluationBackupLockRelPath, constants.PermFilePrivate)
	require.NoError(t, err)
	t.Cleanup(func() { _ = backup.Close() })
	publication := newRecordingPublicationStateStore()

	_, err = f.deleter(t, false, publication).DeleteRun(ctx, "run-a")
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	assert.Empty(t, publication.discarded)
	_, _, err = LocateRun(ctx, f.files, "run-a")
	require.NoError(t, err)
}

func TestDeleterDeleteCampaignPurgesItsRunsFromBackups(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	inventory := campaignInventoryPath(DefaultCampaignInventoryRelDirname, f.req.CampaignID)
	require.NoError(t, f.files.WriteFile(ctx, inventory, []byte("{}"), constants.PermFilePrivate))
	backupDir := f.backUp(t)

	report, err := f.deleterWithBackups(false, newRecordingPublicationStateStore(), backupDir).DeleteCampaign(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.Positive(t, report.Backups.Files)
	roots := campaignEvidencePaths(f.req.CampaignID)
	for _, runID := range f.runIDs {
		roots = append(roots, runEvidencePaths(runID)...)
	}
	assertNoBackedUpPathUnder(t, backupDir, roots...)
	assert.Equal(t, 1, report.Backups.Snapshots)
	assert.Empty(t, backedUpPaths(t, backupDir), "the snapshot held only this campaign, so it is removed")

	_, err = NewEvalBackup(f.files, time.Now).RestoreAll(ctx, backupDir, false)
	require.ErrorIs(t, err, constants.ErrEvaluationBackupNone, "restore never brings a deleted campaign back")
}

func TestDeleterDeleteCampaignThatIsOnlyInBackups(t *testing.T) {
	f := newArchiveFixture(t)
	ctx := context.Background()
	backupDir := f.backUp(t)
	require.NoError(t, f.files.RemoveAll(ctx, activeEvalLayout().campaignDir(f.req.CampaignID)))
	publication := newRecordingPublicationStateStore()

	report, err := f.deleterWithBackups(false, publication, backupDir).DeleteCampaign(ctx, f.req.CampaignID)
	require.NoError(t, err)
	assert.True(t, report.BackupsOnly)
	assert.Empty(t, report.RunIDs)
	assert.Empty(t, publication.discarded)
	assertNoBackedUpPathUnder(t, backupDir, campaignEvidencePaths(f.req.CampaignID)...)
	assertBackedUpPathUnder(t, backupDir, activeEvalLayout().runDir("run-a"))
}

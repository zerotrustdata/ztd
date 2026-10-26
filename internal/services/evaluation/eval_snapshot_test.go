// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
)

const (
	snapshotAssignmentPath = "data/eval/runs/run-1/assignments/assign-1.json"
	snapshotTracePath      = "data/eval/runs/run-1/assignments/assign-1-agent-trace.json"
	snapshotResultPath     = "data/eval/runs/run-1/assignments/assign-1-result.json"
	snapshotEvidencePath   = "data/eval/runs/run-1/evidence/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.json"
	snapshotObsRefPath     = "data/eval/runs/run-1/evidence/provider-observations/att-1/window-ref.json"
	snapshotProvRefPath    = "data/eval/runs/run-1/evidence/model-provenance/att-1/window-ref.json"
	snapshotRolloutPath    = "data/eval/rollouts/v1.5.0.json"
)

func seedRichSnapshotEvidence(t *testing.T, f *evalBackupFixture) {
	t.Helper()
	f.write(t, snapshotAssignmentPath, `{"assignment_id":"assign-1"}`)
	f.write(t, snapshotTracePath, `{"schema_version":"agent-trace.v8"}`)
	f.write(t, snapshotResultPath, `{"verdict":"PASS"}`)
	f.write(t, snapshotEvidencePath, `{"window":"obs"}`)
	f.write(t, snapshotObsRefPath, `{"ref":"obs-1"}`)
	f.write(t, snapshotProvRefPath, `{"ref":"prov-1"}`)
	f.write(t, snapshotRolloutPath, `{"name":"v1.5.0"}`)
}

func TestEvalBackup_ManifestWrittenCompactWithoutIndentationBloat(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)

	raw, err := os.ReadFile(filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "\n  ", "manifest must be compact JSON without multi-space indentation bloat")
}

func TestEvalBackup_InspectSingleSnapshotAndBackupRoot(t *testing.T) {
	f := newEvalBackupFixture(t)
	seedRichSnapshotEvidence(t, f)
	ctx := context.Background()

	// Snapshot 1: initial rich state.
	first := f.create(t)

	// Snapshot 2: superset (updates run report, adds a second campaign).
	f.write(t, backupRunReportPath, `{"run":2}`)
	f.write(t, "data/eval/campaigns/camp-2/campaign-spec.json", `{"campaign":2}`)
	second, err := f.laterBackup(time.Minute).Create(ctx, f.outRoot)
	require.NoError(t, err)

	// Add an incomplete snapshot directory and an unreferenced orphan object.
	incompleteDir := filepath.Join(f.outRoot, constants.EvaluationBackupDirPrefix+"20990101T000000Z")
	require.NoError(t, os.MkdirAll(incompleteDir, constants.PermDirPrivate))
	orphanBody := []byte("unreferenced-orphan-object")
	orphanDigest := evalBackupDigest(orphanBody)
	require.NoError(t, storeEvalBackupObject(f.outRoot, EvalBackupFile{
		Path:   "data/eval/orphan.json",
		Size:   int64(len(orphanBody)),
		SHA256: orphanDigest,
	}, orphanBody))

	// Inspect single snapshot (first).
	singleInspect, err := f.backup.Inspect(ctx, first.SnapshotDir)
	require.NoError(t, err)
	assert.True(t, singleInspect.IsSingleSnapshot)
	assert.Equal(t, 1, singleInspect.SnapshotCount)
	assert.Equal(t, 1, singleInspect.ActiveSnapshotCount)
	assert.Zero(t, singleInspect.SupersededSnapshotCount)
	assert.Equal(t, len(first.Files), singleInspect.LogicalFileCount)
	assert.Equal(t, 1, singleInspect.Content.CampaignCount)
	assert.Equal(t, []string{"camp-1"}, singleInspect.Content.CampaignIDs)
	assert.Equal(t, 1, singleInspect.Content.RunCount)
	assert.Equal(t, []string{"run-1"}, singleInspect.Content.RunIDs)
	assert.Equal(t, 1, singleInspect.Content.AssignmentCount)
	assert.Equal(t, 1, singleInspect.Content.AgentTraceCount)
	assert.Equal(t, 1, singleInspect.Content.AssignmentResultCount)
	assert.Equal(t, 1, singleInspect.Content.WitnessEvidenceCount)
	assert.Equal(t, 1, singleInspect.Content.ProviderObservationRefs)
	assert.Equal(t, 1, singleInspect.Content.ModelProvenanceRefs)
	assert.Equal(t, 1, singleInspect.Content.RolloutCount)
	assert.Equal(t, 1, singleInspect.Content.InventoryCount)

	// Inspect backup root (f.outRoot).
	rootInspect, err := f.backup.Inspect(ctx, f.outRoot)
	require.NoError(t, err)
	assert.False(t, rootInspect.IsSingleSnapshot)
	assert.Equal(t, 2, rootInspect.SnapshotCount)
	assert.Equal(t, 1, rootInspect.ActiveSnapshotCount, "second snapshot is a strict path superset of first")
	assert.Equal(t, 1, rootInspect.SupersededSnapshotCount)
	assert.Equal(t, 1, rootInspect.IncompleteSnapshotCount)
	assert.Equal(t, second.SnapshotDir, rootInspect.LatestSnapshotDir)
	assert.Equal(t, len(second.Files), rootInspect.LogicalFileCount)
	assert.Equal(t, 2, rootInspect.Content.CampaignCount)
	assert.Equal(t, []string{"camp-1", "camp-2"}, rootInspect.Content.CampaignIDs)
	assert.Equal(t, 1, rootInspect.SupersededObjectCount, "the old version of backupRunReportPath is only in the superseded snapshot")
	assert.Equal(t, 1, rootInspect.UnreferencedObjectCount, "the orphan object is not in any manifest")
	assert.Greater(t, rootInspect.ReclaimableBytes, int64(len(orphanBody)))
}

func TestEvalBackup_CompareSnapshotsAndLiveRuntimeAcrossVaultReseal(t *testing.T) {
	f := newEvalBackupFixture(t)
	seedRichSnapshotEvidence(t, f)
	ctx := context.Background()

	// Snapshot 1 under vault #1.
	first := f.create(t)

	// Wipe runtime and recreate with a fresh vault (#2), then restore Snapshot 1.
	// Every sealed file now has a brand-new ciphertext and nonce on disk, but
	// identical plaintext content.
	f.wipeRuntime(t)
	f.initRuntime(t)
	_, err := f.backup.RestoreAll(ctx, f.outRoot, false)
	require.NoError(t, err)

	// Modify one file, remove one file, and add one new campaign in live .ztd/.
	f.write(t, backupRunReportPath, `{"run":"updated-in-live"}`)
	require.NoError(t, f.files.Remove(ctx, snapshotRolloutPath))
	f.write(t, "data/eval/campaigns/camp-2/campaign-spec.json", `{"campaign":2}`)

	// Compare Snapshot 1 against live .ztd/ runtime ("").
	liveDiff, err := f.backup.Compare(ctx, first.SnapshotDir, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"data/eval/campaigns/camp-2/campaign-spec.json"}, liveDiff.Added)
	assert.Equal(t, []string{snapshotRolloutPath}, liveDiff.Removed)
	assert.Equal(t, []string{backupRunReportPath}, liveDiff.Modified)
	assert.NotEmpty(t, liveDiff.ResealedUnchanged, "sealed files unchanged in plaintext after vault reseal must be reported as ResealedUnchanged, not Modified")
	assert.Contains(t, liveDiff.ResealedUnchanged, backupCampaignPath)
	assert.Contains(t, liveDiff.Unchanged, backupInventoryPath, "unsealed eval/ files have identical stored and plaintext digests")
	assert.Equal(t, []string{"camp-2"}, liveDiff.AddedCampaigns)
	assert.Empty(t, liveDiff.RemovedCampaigns)

	// Take Snapshot 2 under vault #2 and compare Snapshot 1 vs Snapshot 2.
	second, err := f.laterBackup(time.Minute).Create(ctx, f.outRoot)
	require.NoError(t, err)
	snapDiff, err := f.backup.Compare(ctx, first.SnapshotDir, second.SnapshotDir)
	require.NoError(t, err)
	assert.Equal(t, liveDiff.Added, snapDiff.Added)
	assert.Equal(t, liveDiff.Removed, snapDiff.Removed)
	assert.Equal(t, liveDiff.Modified, snapDiff.Modified)
	assert.Equal(t, liveDiff.ResealedUnchanged, snapDiff.ResealedUnchanged)
}

func TestEvalBackup_VerifyAndVerifyAllConfirmRestorabilityWithoutWriting(t *testing.T) {
	f := newEvalBackupFixture(t)
	seedRichSnapshotEvidence(t, f)
	ctx := context.Background()
	report := f.create(t)

	// Mutate live runtime: remove one file and alter another to create a conflict,
	// while keeping the rest intact.
	require.NoError(t, f.files.Remove(ctx, backupCampaignPath))
	f.write(t, backupRunReportPath, `{"run":"live-conflict"}`)

	verifyReport, err := f.backup.Verify(ctx, report.SnapshotDir)
	require.NoError(t, err)
	assert.True(t, verifyReport.Restorable)
	assert.Equal(t, 1, verifyReport.Snapshots)
	assert.Equal(t, len(report.Files), verifyReport.VerifiedFiles)
	assert.Equal(t, 1, verifyReport.KeyChainsVerified)
	assert.Equal(t, []string{backupCampaignPath}, reportPaths(verifyReport.WouldRestore))
	assert.Equal(t, []string{backupRunReportPath}, reportPaths(verifyReport.Conflicts))
	assert.Len(t, verifyReport.Unchanged, len(report.Files)-2)

	// Confirm Verify performed zero writes to .ztd/.
	exists, err := f.files.FileExists(ctx, backupCampaignPath)
	require.NoError(t, err)
	assert.False(t, exists, "Verify must never write missing files into the runtime")
	assert.JSONEq(t, `{"run":"live-conflict"}`, f.read(t, backupRunReportPath), "Verify must never overwrite conflicting files")

	// VerifyAll over the backup root returns the same verified state.
	verifyAllReport, err := f.backup.VerifyAll(ctx, f.outRoot)
	require.NoError(t, err)
	assert.True(t, verifyAllReport.Restorable)
	assert.Equal(t, verifyReport.VerifiedFiles, verifyAllReport.VerifiedFiles)
	assert.Equal(t, verifyReport.WouldRestore, verifyAllReport.WouldRestore)
	assert.Equal(t, verifyReport.Conflicts, verifyAllReport.Conflicts)

	// Corrupting an object fails Verify with ErrEvaluationBackupIntegrity.
	for _, file := range report.Files {
		if file.Path == backupInventoryPath {
			require.NoError(t, os.WriteFile(payloadPath(report.SnapshotDir, file), []byte("corrupt"), constants.PermFilePrivate))
		}
	}
	_, err = f.backup.Verify(ctx, report.SnapshotDir)
	require.ErrorIs(t, err, constants.ErrEvaluationBackupIntegrity)

	// Unopenable key chain fails Verify with ErrEvaluationBackupKeyChainInvalid.
	f2 := newEvalBackupFixture(t)
	r2 := f2.create(t)
	f2.wipeRuntime(t)
	f2.keyring = keystoretest.NewMemoryKeyring()
	f2.initRuntime(t)
	_, err = f2.backup.VerifyAll(ctx, f2.outRoot)
	require.ErrorIs(t, err, constants.ErrEvaluationBackupKeyChainInvalid)
	_, err = f2.backup.Verify(ctx, r2.SnapshotDir)
	require.ErrorIs(t, err, constants.ErrEvaluationBackupKeyChainInvalid)
}

func TestEvalBackup_CompactPrunesSupersededAndIncompleteSnapshotsAndOrphanedObjectsWhilePreservingUniqueOlderPaths(t *testing.T) {
	f := newEvalBackupFixture(t)
	seedRichSnapshotEvidence(t, f)
	ctx := context.Background()

	// Snapshot 1: has a unique file that will be removed before Snapshot 2,
	// so Snapshot 1 is NOT superseded and must be kept by Compact!
	const uniqueOlderPath = "data/eval/campaigns/camp-unique-old/campaign-spec.json"
	f.write(t, uniqueOlderPath, `{"campaign":"unique-old"}`)
	snap1 := f.create(t)

	// Snapshot 2: removes uniqueOlderPath, adds camp-2.
	require.NoError(t, f.files.RemoveAll(ctx, "data/eval/campaigns/camp-unique-old"))
	f.write(t, "data/eval/campaigns/camp-2/campaign-spec.json", `{"campaign":2}`)
	snap2, err := f.laterBackup(time.Minute).Create(ctx, f.outRoot)
	require.NoError(t, err)

	// Wipe runtime and reseal under a new vault (#2), restoring only snap2
	// so uniqueOlderPath stays only in snap1 while snap3 covers 100% of snap2!
	f.wipeRuntime(t)
	f.initRuntime(t)
	_, err = f.backup.Restore(ctx, snap2.SnapshotDir, false)
	require.NoError(t, err)
	snap3, err := f.laterBackup(2*time.Minute).Create(ctx, f.outRoot)
	require.NoError(t, err)

	// Also plant an incomplete snapshot directory and an unreferenced object.
	incompleteDir := filepath.Join(f.outRoot, constants.EvaluationBackupDirPrefix+"20990101T000000Z")
	require.NoError(t, os.MkdirAll(incompleteDir, constants.PermDirPrivate))
	orphanBody := []byte("orphan-content")
	orphanFile := EvalBackupFile{Path: "data/eval/orphan.json", Size: int64(len(orphanBody)), SHA256: evalBackupDigest(orphanBody)}
	require.NoError(t, storeEvalBackupObject(f.outRoot, orphanFile, orphanBody))

	// Run Compact:
	// - snap3 is newest -> kept.
	// - snap2 has 0 paths not in snap3 -> superseded and removed!
	// - snap1 has uniqueOlderPath (not in snap3) -> kept!
	// - incompleteDir -> removed!
	// - objects referenced only by snap2 (and not snap1 or snap3) + orphanFile -> removed!
	compactReport, err := f.backup.Compact(ctx, f.outRoot)
	require.NoError(t, err)
	assert.Equal(t, 2, compactReport.KeptSnapshots, "snap1 (unique older path) and snap3 (newest) must be kept")
	assert.Equal(t, 1, compactReport.RemovedSupersededSnapshots, "snap2 is 100%% covered by snap3 and must be pruned")
	assert.Equal(t, 1, compactReport.RemovedIncompleteSnapshots)
	assert.Positive(t, compactReport.RemovedObjects)
	assert.Positive(t, compactReport.TotalReclaimedBytes)

	assert.DirExists(t, snap1.SnapshotDir)
	assert.NoDirExists(t, snap2.SnapshotDir)
	assert.DirExists(t, snap3.SnapshotDir)
	assert.NoDirExists(t, incompleteDir)
	assert.NoFileExists(t, evalBackupObjectPath(f.outRoot, orphanFile.SHA256))

	// After Compact, Inspect must report 0 superseded snapshots, 0 incomplete snapshots,
	// 0 unreferenced objects, and 0 reclaimable bytes!
	afterInspect, err := f.backup.Inspect(ctx, f.outRoot)
	require.NoError(t, err)
	assert.Equal(t, 2, afterInspect.SnapshotCount)
	assert.Equal(t, 2, afterInspect.ActiveSnapshotCount)
	assert.Zero(t, afterInspect.SupersededSnapshotCount)
	assert.Zero(t, afterInspect.IncompleteSnapshotCount)
	assert.Zero(t, afterInspect.UnreferencedObjectCount)

	// And RestoreAll after a full wipe must still recover every file from snap3 PLUS uniqueOlderPath from snap1!
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))
	restored, err := f.backup.RestoreAll(ctx, f.outRoot, false)
	require.NoError(t, err)
	assert.Equal(t, 2, restored.Snapshots)
	assert.JSONEq(t, `{"campaign":"unique-old"}`, f.read(t, uniqueOlderPath))
	assert.JSONEq(t, `{"campaign":2}`, f.read(t, "data/eval/campaigns/camp-2/campaign-spec.json"))
}

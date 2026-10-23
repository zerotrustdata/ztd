// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

var evalBackupTestNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

const (
	backupRunReportPath  = "data/eval/runs/run-1/report.json"
	backupRunLeasePath   = "data/eval/runs/run-1/lease.json"
	backupActiveRunPath  = "data/eval/active-run.json"
	backupCampaignPath   = "data/eval/campaigns/camp-1/campaign-spec.json"
	backupInventoryPath  = "eval/model-inventory.json"
	backupQueueLogPath   = "eval/logs/queue.log"
	backupUnrelatedPath  = "data/ztd.db"
	backupSnapshotPrefix = "eval-backup-20260930T120000Z"
)

// evalBackupFixture is a runtime whose evaluation evidence is sealed by a real
// vault opened through the real key store over an in-memory OS key store, the
// part of the key chain that outlives the runtime tree.
type evalBackupFixture struct {
	raw     fs.RuntimeFileService
	files   fs.RuntimeFileService
	keyring keystore.Keyring
	backup  *EvalBackup
	outRoot string
}

func newEvalBackupFixture(t *testing.T) *evalBackupFixture {
	t.Helper()
	raw, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	f := &evalBackupFixture{
		raw:     raw,
		keyring: keystoretest.NewMemoryKeyring(),
		outRoot: filepath.Join(testutil.TempDir(t), "backups"),
	}
	f.initRuntime(t)
	f.write(t, backupRunReportPath, `{"run":1}`)
	f.write(t, backupRunLeasePath, `{"pid":1}`)
	f.write(t, backupActiveRunPath, `{"run":"run-1"}`)
	f.write(t, backupCampaignPath, `{"campaign":1}`)
	f.write(t, backupInventoryPath, `{"variants":[]}`)
	f.write(t, backupQueueLogPath, "queued\n")
	f.write(t, backupUnrelatedPath, "not evaluation data")
	return f
}

// initRuntime creates the runtime tree and a fresh vault in it, as a new
// install, or a reinstall after the runtime tree was wiped, does.
func (f *evalBackupFixture) initRuntime(t *testing.T) {
	t.Helper()
	f.files = newVaultedEvidenceFiles(t, f.raw, f.keyring)
	f.backup = NewEvalBackup(f.files, func() time.Time { return evalBackupTestNow })
}

// newVaultedEvidenceFiles creates raw's runtime tree and a fresh vault in it,
// and returns the evidence file service over it. The vault key is sealed under
// the master key keyring holds.
func newVaultedEvidenceFiles(t *testing.T, raw fs.RuntimeFileService, keyring keystore.Keyring) fs.RuntimeFileService {
	t.Helper()
	require.NoError(t, raw.CreateRuntimeTree(context.Background()))
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, raw)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())
	files, err := NewEvidenceFileService(raw, func(keys fs.RuntimeFileService) (EvidenceVault, error) {
		keyStore, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keyring, keys)
		if err != nil {
			return nil, err
		}
		return keyStore.OpenVault()
	})
	require.NoError(t, err)
	return files
}

// wipeRuntime removes the whole runtime tree, vault included.
func (f *evalBackupFixture) wipeRuntime(t *testing.T) {
	t.Helper()
	require.NoError(t, os.RemoveAll(f.raw.Resolve("")))
}

func (f *evalBackupFixture) write(t *testing.T, rel, body string) {
	t.Helper()
	require.NoError(t, f.files.WriteFile(context.Background(), rel, []byte(body), constants.PermFilePrivate))
}

func (f *evalBackupFixture) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := f.files.ReadFile(context.Background(), rel)
	require.NoError(t, err)
	return string(data)
}

func (f *evalBackupFixture) create(t *testing.T) *EvalBackupReport {
	t.Helper()
	report, err := f.backup.Create(context.Background(), f.outRoot)
	require.NoError(t, err)
	return report
}

// stored is the content the runtime holds on disk for rel: ciphertext for a
// sealed record.
func (f *evalBackupFixture) stored(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := f.raw.ReadFile(context.Background(), rel)
	require.NoError(t, err)
	return data
}

func reportPaths(files []EvalBackupFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

// payloadPath is where a snapshot keeps the content of one backed-up file.
func payloadPath(snapshotDir string, file EvalBackupFile) string {
	return evalBackupObjectPath(filepath.Dir(snapshotDir), file.SHA256)
}

func objectCount(t *testing.T, backupDir string) int {
	t.Helper()
	count := 0
	require.NoError(t, filepath.WalkDir(filepath.Join(backupDir, evalBackupObjectsDir), func(_ string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			count++
		}
		return err
	}))
	return count
}

func TestEvalBackup_CreateCopiesEvidenceOnly(t *testing.T) {
	f := newEvalBackupFixture(t)

	report := f.create(t)

	assert.Equal(t, filepath.Join(f.outRoot, backupSnapshotPrefix), report.SnapshotDir)
	assert.Equal(t, []string{backupCampaignPath, backupRunReportPath, backupQueueLogPath, backupInventoryPath}, reportPaths(report.Files))
	assert.Equal(t, []string{constants.VaultHeaderRelPath, constants.VaultKeyRelPath}, reportPaths(report.KeyChain))
	for _, file := range append(append([]EvalBackupFile{}, report.Files...), report.KeyChain...) {
		copied, err := os.ReadFile(payloadPath(report.SnapshotDir, file))
		require.NoError(t, err)
		assert.Equal(t, f.stored(t, file.Path), copied, "%s is copied as stored", file.Path)
		assert.Equal(t, evalBackupDigest(copied), file.SHA256)
	}
	sealed, err := os.ReadFile(payloadPath(report.SnapshotDir, report.Files[1]))
	require.NoError(t, err)
	assert.NotContains(t, string(sealed), `{"run":1}`, "sealed evidence leaves the runtime as ciphertext")
	snapshotEntries, err := os.ReadDir(report.SnapshotDir)
	require.NoError(t, err)
	require.Len(t, snapshotEntries, 1, "a snapshot holds only its manifest")
	assert.Equal(t, constants.EvaluationBackupManifestFilename, snapshotEntries[0].Name())
	assert.Equal(t, len(report.Files)+len(report.KeyChain), objectCount(t, f.outRoot), "excluded files (lease, active run, unrelated data) are never stored")
	var manifest EvalBackupManifest
	raw, err := os.ReadFile(filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &manifest))
	assert.Equal(t, report.Files, manifest.Files)
	assert.Equal(t, report.KeyChain, manifest.KeyChain)
	assert.Equal(t, evalBackupManifestVersion, manifest.SchemaVersion)
}

func TestEvalBackup_RepeatedBackupsStoreEachContentOnce(t *testing.T) {
	f := newEvalBackupFixture(t)
	first := f.create(t)
	stored := objectCount(t, f.outRoot)
	require.Equal(t, len(first.Files)+len(first.KeyChain), stored)

	f.create(t)
	assert.Equal(t, stored, objectCount(t, f.outRoot), "an identical backup copies nothing")

	f.write(t, backupRunReportPath, `{"run":2}`)
	f.create(t)
	assert.Equal(t, stored+1, objectCount(t, f.outRoot), "a changed file adds exactly one object")
}

func TestEvalBackup_CreateRejectsDestinationInsideRuntime(t *testing.T) {
	f := newEvalBackupFixture(t)

	for _, dest := range []string{
		f.files.Resolve(""),
		f.files.Resolve("data/eval/backups"),
		filepath.Join(f.files.Resolve("data"), "..", constants.RuntimeDirname, "x"),
	} {
		_, err := f.backup.Create(context.Background(), dest)
		assert.ErrorIs(t, err, constants.ErrEvaluationBackupDestinationInvalid, dest)
	}
	_, err := f.backup.Create(context.Background(), " ")
	assert.ErrorIs(t, err, constants.ErrEvaluationBackupDestinationInvalid)
}

func TestEvalBackup_CreateRejectsSymlinkedDestinationIntoRuntime(t *testing.T) {
	f := newEvalBackupFixture(t)
	link := filepath.Join(testutil.TempDir(t), "link")
	testutil.Symlink(t, f.files.Resolve(""), link)

	_, err := f.backup.Create(context.Background(), filepath.Join(link, "backups"))

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupDestinationInvalid)
}

func TestEvalBackup_CreateEmptyRuntimeFails(t *testing.T) {
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	require.NoError(t, files.CreateRuntimeTree(context.Background()))
	backup := NewEvalBackup(files, func() time.Time { return evalBackupTestNow })
	out := filepath.Join(testutil.TempDir(t), "backups")

	_, err = backup.Create(context.Background(), out)

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupEmpty)
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a failed backup must not leave a destination behind")
}

func TestEvalBackup_CreateSameSecondPreservesSnapshots(t *testing.T) {
	f := newEvalBackupFixture(t)
	first := f.create(t)
	f.write(t, backupRunReportPath, `{"run":2}`)

	second := f.create(t)
	third := f.create(t)

	assert.Equal(t, first.SnapshotDir+"-000001", second.SnapshotDir)
	assert.Equal(t, first.SnapshotDir+"-000002", third.SnapshotDir)
	digests := make(map[string]bool)
	for _, snapshot := range []*EvalBackupReport{first, second} {
		for _, file := range snapshot.Files {
			if file.Path == backupRunReportPath {
				assert.FileExists(t, payloadPath(snapshot.SnapshotDir, file))
				digests[file.SHA256] = true
			}
		}
	}
	assert.Len(t, digests, 2, "each snapshot keeps its own version of the changed file")
	latest, err := LatestEvalBackupSnapshot(f.outRoot)
	require.NoError(t, err)
	assert.Equal(t, third.SnapshotDir, latest)
}

func TestEvalBackup_CreateConcurrentSnapshots(t *testing.T) {
	f := newEvalBackupFixture(t)
	type outcome struct {
		report *EvalBackupReport
		err    error
	}
	const count = 4
	results := make(chan outcome, count)
	for i := 0; i < count; i++ {
		go func() {
			report, err := f.backup.Create(context.Background(), f.outRoot)
			results <- outcome{report, err}
		}()
	}
	directories := make(map[string]bool)
	for i := 0; i < count; i++ {
		result := <-results
		require.NoError(t, result.err)
		assert.False(t, directories[result.report.SnapshotDir])
		directories[result.report.SnapshotDir] = true
		assert.FileExists(t, filepath.Join(result.report.SnapshotDir, constants.EvaluationBackupManifestFilename))
	}
	assert.Len(t, directories, count)
}

func TestEvalBackup_RoundTripAfterWipe(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	ctx := context.Background()
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))

	restored, err := f.backup.Restore(ctx, report.SnapshotDir, false)

	require.NoError(t, err)
	assert.Equal(t, report.Files, restored.Restored)
	assert.Empty(t, restored.Unchanged)
	assert.Equal(t, `{"run":1}`, f.read(t, backupRunReportPath))
	assert.Equal(t, "queued\n", f.read(t, backupQueueLogPath))
	leaseExists, err := f.files.FileExists(ctx, backupRunLeasePath)
	require.NoError(t, err)
	assert.False(t, leaseExists, "lease state must not be resurrected")
}

// A snapshot outlives the runtime tree and the vault in it: after a wipe and a
// fresh install, the restore opens the snapshot's vault from its key chain and
// seals the evidence under the new runtime vault.
func TestEvalBackup_RestoreAfterRuntimeAndVaultAreRecreated(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	oldHeader := f.stored(t, constants.VaultHeaderRelPath)
	f.wipeRuntime(t)
	f.initRuntime(t)
	require.NotEqual(t, oldHeader, f.stored(t, constants.VaultHeaderRelPath), "the reinstall creates a new vault")

	restored, err := f.backup.RestoreAll(context.Background(), f.outRoot, false)

	require.NoError(t, err)
	assert.Equal(t, report.Files, restored.Restored)
	assert.Equal(t, `{"run":1}`, f.read(t, backupRunReportPath))
	assert.Equal(t, `{"campaign":1}`, f.read(t, backupCampaignPath))
	assert.Equal(t, "queued\n", f.read(t, backupQueueLogPath))
	assert.NotContains(t, string(f.stored(t, backupRunReportPath)), `{"run":1}`, "restored evidence is sealed again")

	again, err := f.backup.RestoreAll(context.Background(), f.outRoot, false)

	require.NoError(t, err)
	assert.Empty(t, again.Restored, "evidence sealed under another vault compares by content")
	assert.Equal(t, report.Files, again.Unchanged)
}

// Evidence a vault the key store cannot open sealed is never written back.
func TestEvalBackup_RestoreRejectsKeyChainTheKeyStoreCannotOpen(t *testing.T) {
	f := newEvalBackupFixture(t)
	f.create(t)
	f.wipeRuntime(t)
	f.keyring = keystoretest.NewMemoryKeyring()
	f.initRuntime(t)

	_, err := f.backup.RestoreAll(context.Background(), f.outRoot, false)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupKeyChainInvalid)
	exists, err := f.files.FileExists(context.Background(), backupCampaignPath)
	require.NoError(t, err)
	assert.False(t, exists, "nothing is written when a snapshot's vault cannot be opened")
}

func TestEvalBackup_RestoreRejectsSnapshotWithoutKeyChain(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	manifestPath := filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename)
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var manifest EvalBackupManifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	manifest.KeyChain = manifest.KeyChain[:1]
	raw, err = json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, raw, constants.PermFilePrivate))

	_, err = f.backup.Restore(context.Background(), report.SnapshotDir, true)

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupKeyChainMissing)
}

func TestEvalBackup_RestoreRequiresTheEvidenceFileService(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)

	_, err := NewEvalBackup(f.raw, time.Now).Restore(context.Background(), report.SnapshotDir, true)

	assert.ErrorIs(t, err, constants.ErrEvaluationEvidenceVaultRequired)
}

// foreignCiphertext is backupRunReportPath sealed under a vault no fixture
// key chain opens, as a record left behind by a lost vault is.
func foreignCiphertext(t *testing.T) []byte {
	t.Helper()
	return newEvalBackupFixture(t).stored(t, backupRunReportPath)
}

func TestEvalBackup_CreateRejectsEvidenceTheRuntimeVaultCannotOpen(t *testing.T) {
	f := newEvalBackupFixture(t)
	require.NoError(t, f.raw.WriteFile(context.Background(), backupRunReportPath, foreignCiphertext(t), constants.PermFilePrivate))

	_, err := f.backup.Create(context.Background(), f.outRoot)

	require.ErrorIs(t, err, constants.ErrEvaluationEvidenceDecryptFailed)
	assert.Contains(t, err.Error(), backupRunReportPath)
	_, err = AllEvalBackupSnapshots(f.outRoot)
	assert.ErrorIs(t, err, constants.ErrEvaluationBackupNone, "a snapshot never lists a record its key chain cannot open")
}

func TestEvalBackup_RestoreRejectsEvidenceItsKeyChainCannotOpen(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	ctx := context.Background()
	foreign := foreignCiphertext(t)
	foreignFile := EvalBackupFile{Path: backupRunReportPath, Size: int64(len(foreign)), SHA256: evalBackupDigest(foreign)}
	require.NoError(t, storeEvalBackupObject(f.outRoot, foreignFile, foreign))
	manifestPath := filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename)
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	var manifest EvalBackupManifest
	require.NoError(t, json.Unmarshal(raw, &manifest))
	for i := range manifest.Files {
		if manifest.Files[i].Path == backupRunReportPath {
			manifest.Files[i] = foreignFile
		}
	}
	raw, err = json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, raw, constants.PermFilePrivate))
	require.NoError(t, f.raw.Remove(ctx, backupRunReportPath))
	require.NoError(t, f.raw.Remove(ctx, backupCampaignPath))

	_, err = f.backup.RestoreAll(ctx, f.outRoot, true)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupEvidenceUnopenable)
	assert.Contains(t, err.Error(), backupRunReportPath)
	for _, rel := range []string{backupRunReportPath, backupCampaignPath} {
		exists, err := f.raw.FileExists(ctx, rel)
		require.NoError(t, err)
		assert.False(t, exists, "%s: a record the key chain cannot open restores nothing, not even as stored", rel)
	}
}

func TestEvalBackup_RestoreIsIdempotent(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)

	restored, err := f.backup.Restore(context.Background(), report.SnapshotDir, false)

	require.NoError(t, err)
	assert.Empty(t, restored.Restored)
	assert.Equal(t, report.Files, restored.Unchanged)
}

func TestEvalBackup_RestoreConflictWritesNothingUnlessOverwrite(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	ctx := context.Background()
	f.write(t, backupRunReportPath, `{"run":"changed"}`)
	require.NoError(t, f.files.Remove(ctx, backupCampaignPath))

	_, err := f.backup.Restore(ctx, report.SnapshotDir, false)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupConflict)
	assert.Contains(t, err.Error(), backupRunReportPath)
	assert.Equal(t, `{"run":"changed"}`, f.read(t, backupRunReportPath))
	campaignExists, err := f.files.FileExists(ctx, backupCampaignPath)
	require.NoError(t, err)
	assert.False(t, campaignExists, "a refused restore must not write the files that were not in conflict")

	restored, err := f.backup.Restore(ctx, report.SnapshotDir, true)

	require.NoError(t, err)
	assert.ElementsMatch(t, []string{backupRunReportPath, backupCampaignPath}, reportPaths(restored.Restored))
	assert.Equal(t, `{"run":1}`, f.read(t, backupRunReportPath))
}

func TestEvalBackup_RestoreRejectsTamperedFile(t *testing.T) {
	f := newEvalBackupFixture(t)
	report := f.create(t)
	ctx := context.Background()
	for _, file := range report.Files {
		if file.Path == backupInventoryPath {
			require.NoError(t, os.WriteFile(payloadPath(report.SnapshotDir, file), []byte("tampered"), constants.PermFilePrivate))
		}
	}
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))

	_, err := f.backup.Restore(ctx, report.SnapshotDir, false)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupIntegrity)
	exists, err := f.files.FileExists(ctx, backupRunReportPath)
	require.NoError(t, err)
	assert.False(t, exists, "integrity is verified for every file before any write")
}

func TestEvalBackup_RestoreRejectsMissingOrIncompleteManifest(t *testing.T) {
	f := newEvalBackupFixture(t)

	_, err := f.backup.Restore(context.Background(), filepath.Join(testutil.TempDir(t), "nothing"), false)
	assert.ErrorIs(t, err, constants.ErrEvaluationBackupManifestInvalid)

	report := f.create(t)
	require.NoError(t, os.Remove(filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename)))
	_, err = f.backup.Restore(context.Background(), report.SnapshotDir, false)
	assert.ErrorIs(t, err, constants.ErrEvaluationBackupManifestInvalid)
}

func TestEvalBackup_RestoreRejectsManifestPathsOutsideEvidenceTrees(t *testing.T) {
	f := newEvalBackupFixture(t)
	digest := evalBackupDigest([]byte("x"))
	for _, bad := range []string{
		constants.VaultKeyRelPath,
		"pki/operator.key",
		"data/ztd.db",
		"../escape",
		"/etc/passwd",
		"data/eval/../../pki/ca.key",
		"data/eval/runs/run-1/lease.json",
		"eval",
	} {
		t.Run(bad, func(t *testing.T) {
			snapshot := testutil.TempDir(t)
			raw, err := json.Marshal(EvalBackupManifest{
				SchemaVersion: evalBackupManifestVersion,
				Files:         []EvalBackupFile{{Path: bad, Size: 1, SHA256: digest}},
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(snapshot, constants.EvaluationBackupManifestFilename), raw, constants.PermFilePrivate))

			_, err = f.backup.Restore(context.Background(), snapshot, true)

			assert.ErrorIs(t, err, constants.ErrEvaluationBackupManifestInvalid)
		})
	}
	t.Run("key chain path outside the vault key chain", func(t *testing.T) {
		snapshot := testutil.TempDir(t)
		raw, err := json.Marshal(EvalBackupManifest{
			SchemaVersion: evalBackupManifestVersion,
			Files:         []EvalBackupFile{{Path: backupInventoryPath, Size: 1, SHA256: digest}},
			KeyChain:      []EvalBackupFile{{Path: "pki/ca.key", Size: 1, SHA256: digest}},
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(snapshot, constants.EvaluationBackupManifestFilename), raw, constants.PermFilePrivate))

		_, err = f.backup.Restore(context.Background(), snapshot, true)

		assert.ErrorIs(t, err, constants.ErrEvaluationBackupManifestInvalid)
	})
}

func (f *evalBackupFixture) laterBackup(after time.Duration) *EvalBackup {
	return NewEvalBackup(f.files, func() time.Time { return evalBackupTestNow.Add(after) })
}

func TestEvalBackup_CreateIfChangedSkipsIdenticalEvidence(t *testing.T) {
	f := newEvalBackupFixture(t)
	first := f.create(t)

	report, err := f.backup.CreateIfChanged(context.Background(), f.outRoot)

	require.NoError(t, err)
	assert.True(t, report.Unchanged)
	assert.Equal(t, first.SnapshotDir, report.SnapshotDir)
	assert.Equal(t, first.Files, report.Files)
	snapshots, err := AllEvalBackupSnapshots(f.outRoot)
	require.NoError(t, err)
	assert.Len(t, snapshots, 1, "an unchanged backup must not leave a second snapshot")
	assert.Equal(t, len(first.Files)+len(first.KeyChain), objectCount(t, f.outRoot))
}

func TestEvalBackup_CreateIfChangedKeepsSnapshotWhenEvidenceChanged(t *testing.T) {
	f := newEvalBackupFixture(t)
	first := f.create(t)
	f.write(t, backupRunReportPath, `{"run":2}`)

	report, err := f.backup.CreateIfChanged(context.Background(), f.outRoot)

	require.NoError(t, err)
	assert.False(t, report.Unchanged)
	assert.NotEqual(t, first.SnapshotDir, report.SnapshotDir)
	assert.FileExists(t, filepath.Join(first.SnapshotDir, constants.EvaluationBackupManifestFilename), "the earlier snapshot must survive")
	assert.FileExists(t, filepath.Join(report.SnapshotDir, constants.EvaluationBackupManifestFilename))
}

func TestEvalBackup_CreateIfChangedKeepsSnapshotWhenVaultChanged(t *testing.T) {
	f := newEvalBackupFixture(t)
	first := f.create(t)
	f.wipeRuntime(t)
	f.initRuntime(t)
	_, err := f.backup.RestoreAll(context.Background(), f.outRoot, false)
	require.NoError(t, err)

	report, err := f.backup.CreateIfChanged(context.Background(), f.outRoot)

	require.NoError(t, err)
	assert.False(t, report.Unchanged, "evidence resealed under a new vault is a new snapshot")
	assert.NotEqual(t, first.KeyChain, report.KeyChain)
}

func TestEvalBackup_CreateIfChangedCreatesFirstSnapshot(t *testing.T) {
	f := newEvalBackupFixture(t)

	report, err := f.backup.CreateIfChanged(context.Background(), f.outRoot)

	require.NoError(t, err)
	assert.False(t, report.Unchanged)
	assert.Equal(t, filepath.Join(f.outRoot, backupSnapshotPrefix), report.SnapshotDir)
}

func TestEvalBackup_CreateIfChangedRejectsDestinationInsideRuntime(t *testing.T) {
	f := newEvalBackupFixture(t)

	_, err := f.backup.CreateIfChanged(context.Background(), f.files.Resolve("backups"))

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupDestinationInvalid)
}

func TestLatestEvalBackupSnapshot_PicksNewestCompleteSnapshot(t *testing.T) {
	f := newEvalBackupFixture(t)
	older := f.create(t)
	newer, err := f.laterBackup(time.Minute).Create(context.Background(), f.outRoot)
	require.NoError(t, err)
	incomplete := filepath.Join(f.outRoot, constants.EvaluationBackupDirPrefix+"29990101T000000Z")
	require.NoError(t, os.Mkdir(incomplete, constants.PermDirPrivate))

	latest, err := LatestEvalBackupSnapshot(f.outRoot)

	require.NoError(t, err)
	assert.Equal(t, newer.SnapshotDir, latest)
	assert.NotEqual(t, older.SnapshotDir, latest)
}

func TestLatestEvalBackupSnapshot_NoneFound(t *testing.T) {
	for name, dir := range map[string]string{
		"missing directory": filepath.Join(testutil.TempDir(t), "absent"),
		"empty directory":   testutil.TempDir(t),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := LatestEvalBackupSnapshot(dir)
			assert.ErrorIs(t, err, constants.ErrEvaluationBackupNone)
		})
	}
}

func TestEvalBackup_CreateIfChangedComparesWithNewestSnapshot(t *testing.T) {
	f := newEvalBackupFixture(t)
	older := f.create(t)
	f.write(t, backupRunReportPath, `{"run":2}`)
	newer := f.create(t)

	report, err := f.backup.CreateIfChanged(context.Background(), f.outRoot)

	require.NoError(t, err)
	assert.True(t, report.Unchanged)
	assert.Equal(t, newer.SnapshotDir, report.SnapshotDir)
	snapshots, err := AllEvalBackupSnapshots(f.outRoot)
	require.NoError(t, err)
	assert.Equal(t, []string{older.SnapshotDir, newer.SnapshotDir}, snapshots)
}

func TestEvalBackup_RestoreAllMergesSnapshotsNewestWins(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	older := f.create(t)
	// The later snapshot changes one file and no longer lists another, as when
	// evidence is lost between backups.
	f.write(t, backupRunReportPath, `{"run":2}`)
	require.NoError(t, f.files.Remove(ctx, backupCampaignPath))
	newer := f.laterBackup(time.Minute)
	_, err := newer.Create(ctx, f.outRoot)
	require.NoError(t, err)
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))

	report, err := f.backup.RestoreAll(ctx, f.outRoot, false)

	require.NoError(t, err)
	assert.Equal(t, 2, report.Snapshots)
	assert.Equal(t, `{"run":2}`, f.read(t, backupRunReportPath), "the newest snapshot's content wins")
	assert.Equal(t, `{"campaign":1}`, f.read(t, backupCampaignPath), "evidence only an older snapshot holds is recovered")
	assert.Len(t, report.Restored, len(older.Files))
	assert.Empty(t, report.Unchanged)

	again, err := f.backup.RestoreAll(ctx, f.outRoot, false)

	require.NoError(t, err)
	assert.Empty(t, again.Restored, "a second restore has nothing to do")
	assert.Len(t, again.Unchanged, len(older.Files))
}

func TestEvalBackup_RestoreAllConflictWritesNothingUnlessOverwrite(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	f.create(t)
	f.write(t, backupRunReportPath, `{"run":"changed"}`)
	require.NoError(t, f.files.Remove(ctx, backupCampaignPath))

	_, err := f.backup.RestoreAll(ctx, f.outRoot, false)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupConflict)
	campaignExists, err := f.files.FileExists(ctx, backupCampaignPath)
	require.NoError(t, err)
	assert.False(t, campaignExists)

	_, err = f.backup.RestoreAll(ctx, f.outRoot, true)

	require.NoError(t, err)
	assert.Equal(t, `{"run":1}`, f.read(t, backupRunReportPath))
}

func TestEvalBackup_RestoreAllRejectsCorruptObjectBeforeAnyWrite(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	report := f.create(t)
	for _, file := range report.Files {
		if file.Path == backupInventoryPath {
			require.NoError(t, os.WriteFile(payloadPath(report.SnapshotDir, file), []byte("tampered"), constants.PermFilePrivate))
		}
	}
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))

	_, err := f.backup.RestoreAll(ctx, f.outRoot, false)

	require.ErrorIs(t, err, constants.ErrEvaluationBackupIntegrity)
	exists, err := f.files.FileExists(ctx, backupRunReportPath)
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestEvalBackup_RestoreAllWithoutSnapshotsFails(t *testing.T) {
	f := newEvalBackupFixture(t)

	_, err := f.backup.RestoreAll(context.Background(), f.outRoot, false)

	assert.ErrorIs(t, err, constants.ErrEvaluationBackupNone)
}

func TestEvalBackupPurge_RemovesEntriesAndObjectsOnlyTheyReference(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	const (
		sharedPath = "data/eval/runs/run-2/report.json"
		uniquePath = "data/eval/runs/run-1/unique.json"
	)
	f.write(t, sharedPath, `{"run":1}`)
	f.write(t, uniquePath, `{"only":"run-1"}`)
	older := f.create(t)
	f.write(t, backupCampaignPath, `{"campaign":2}`)
	newer, err := f.laterBackup(time.Minute).Create(ctx, f.outRoot)
	require.NoError(t, err)
	var dropped []EvalBackupFile
	for _, file := range older.Files {
		if strings.HasPrefix(file.Path, "data/eval/runs/run-1/") {
			dropped = append(dropped, file)
		}
	}
	require.Len(t, dropped, 2)
	objectsBefore := objectCount(t, f.outRoot)

	plan, err := planEvalBackupPurge(f.outRoot, []string{"data/eval/runs/run-1"})
	require.NoError(t, err)
	require.NoError(t, plan.apply())

	assert.Equal(t, EvalBackupPurgeReport{Files: 4, Snapshots: 2, Objects: 2}, plan.report)
	for _, snapshot := range []string{older.SnapshotDir, newer.SnapshotDir} {
		manifest, err := readEvalBackupManifest(snapshot)
		require.NoError(t, err)
		for _, file := range manifest.Files {
			assert.False(t, strings.HasPrefix(file.Path, "data/eval/runs/run-1/"), "%s still lists %s", snapshot, file.Path)
		}
		assert.Equal(t, older.KeyChain, manifest.KeyChain, "a kept snapshot keeps its key chain")
	}
	assert.Equal(t, objectsBefore-2, objectCount(t, f.outRoot), "only the content no kept file shares is removed")
	for _, file := range dropped {
		assert.NoFileExists(t, evalBackupObjectPath(f.outRoot, file.SHA256))
	}
	for _, file := range newer.Files {
		if !strings.HasPrefix(file.Path, "data/eval/runs/run-1/") {
			assert.FileExists(t, evalBackupObjectPath(f.outRoot, file.SHA256), "kept content is kept")
		}
	}
	for _, file := range older.KeyChain {
		assert.FileExists(t, evalBackupObjectPath(f.outRoot, file.SHA256))
	}

	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDataPath))
	restored, err := f.backup.RestoreAll(ctx, f.outRoot, false)
	require.NoError(t, err)
	assert.NotContains(t, reportPaths(restored.Restored), backupRunReportPath)
	assert.Contains(t, reportPaths(restored.Restored), sharedPath)
}

func TestEvalBackupPurge_RemovesTheKeyChainOfASnapshotLeftEmpty(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	require.NoError(t, f.files.RemoveAll(ctx, constants.EvaluationDirname))
	require.NoError(t, f.files.Remove(ctx, backupCampaignPath))
	only := f.create(t)
	require.Equal(t, []string{backupRunReportPath}, reportPaths(only.Files))

	plan, err := planEvalBackupPurge(f.outRoot, []string{"data/eval/runs/run-1"})
	require.NoError(t, err)
	require.NoError(t, plan.apply())

	assert.NoDirExists(t, only.SnapshotDir, "a snapshot left empty is removed")
	assert.Zero(t, objectCount(t, f.outRoot), "its key chain goes with it")
}

func TestEvalBackupPurge_WithoutSnapshotsHasNothingToDo(t *testing.T) {
	plan, err := planEvalBackupPurge(filepath.Join(testutil.TempDir(t), "missing"), []string{"data/eval/runs/run-1"})
	require.NoError(t, err)
	require.NoError(t, plan.apply())
	assert.Equal(t, EvalBackupPurgeReport{}, plan.report)
}

func TestEvalBackupPurge_RejectsPathsThatAreNotBeneathAnEvidenceTree(t *testing.T) {
	for _, relPath := range []string{"", ".", "data/eval", "eval", "data/eval/", "data/ztd.db", "/data/eval/runs/run-1", "data/eval/../ztd.db"} {
		_, err := planEvalBackupPurge(testutil.TempDir(t), []string{relPath})
		assert.ErrorIs(t, err, constants.ErrEvaluationBackupPurgePathInvalid, relPath)
	}
}

func TestEvalBackup_SnapshotsShareTheLockAndADeleteExcludesThem(t *testing.T) {
	f := newEvalBackupFixture(t)
	ctx := context.Background()
	shared, err := f.backup.sharedLock(ctx)
	require.NoError(t, err)
	report := f.create(t)
	assert.NotContains(t, reportPaths(report.Files), constants.EvaluationBackupLockRelPath, "the lock file is never backed up")

	_, err = f.backup.exclusiveLock(ctx)
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked, "a delete waits for snapshots to finish")
	require.NoError(t, shared.Close())

	exclusive, err := f.backup.exclusiveLock(ctx)
	require.NoError(t, err)
	_, err = f.backup.Create(ctx, f.outRoot)
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked, "no snapshot is taken while a delete purges the backups")
	_, err = f.backup.RestoreAll(ctx, f.outRoot, false)
	require.ErrorIs(t, err, constants.ErrRuntimeFileLocked)
	require.NoError(t, exclusive.Close())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// evalBackupManifestVersion names the snapshot layout: a snapshot holds only
// its manifest; file content lives once per distinct digest in the sibling
// evalBackupObjectsDir, shared by every snapshot, and the manifest names the
// vault key chain that sealed the evidence it lists.
const (
	evalBackupManifestVersion = "3.0.0"
	evalBackupObjectsDir      = "objects"
)

// evalBackupConflictSample bounds how many conflicting paths a restore error names.
const evalBackupConflictSample = 5

// EvalBackupFile is one file captured in a backup snapshot. Path is runtime-relative.
type EvalBackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// EvalBackupManifest is the integrity record written last into a snapshot. A
// snapshot directory without one is incomplete and is never restored. KeyChain
// lists the runtime vault key chain the evidence in Files was sealed under, so
// the snapshot stays readable after the runtime tree, and the vault in it, is
// wiped and recreated.
type EvalBackupManifest struct {
	SchemaVersion string           `json:"schema_version"`
	CreatedAt     string           `json:"created_at"`
	Files         []EvalBackupFile `json:"files"`
	KeyChain      []EvalBackupFile `json:"key_chain"`
}

// EvalBackupReport describes a completed backup.
type EvalBackupReport struct {
	SnapshotDir string
	CreatedAt   time.Time
	Files       []EvalBackupFile
	KeyChain    []EvalBackupFile
	TotalBytes  int64
	// Unchanged is set by CreateIfChanged when the evidence matched the newest
	// existing snapshot, in which case SnapshotDir names that snapshot and
	// nothing new was kept.
	Unchanged bool
}

// EvalRestoreReport describes a completed restore.
type EvalRestoreReport struct {
	// SnapshotDir is the restored snapshot, or the backup directory when every
	// snapshot was merged by RestoreAll.
	SnapshotDir string
	// Snapshots counts the snapshots the restored file set was drawn from.
	Snapshots int
	Restored  []EvalBackupFile
	Unchanged []EvalBackupFile
}

// evalBackupSource is one file to restore, where its verified content is
// read from, and the key chain of the snapshot it came from.
type evalBackupSource struct {
	file     EvalBackupFile
	payload  string
	keyChain *evalBackupKeyChain
}

// evalBackupKeyChain is the vault key chain one snapshot recorded. id names
// its content, so snapshots taken under the same vault open it once.
type evalBackupKeyChain struct {
	id         string
	backupRoot string
	files      []EvalBackupFile
}

// EvalBackup copies evaluation evidence between the runtime tree and a plain
// directory outside it. Runtime I/O goes through the file service; the
// snapshot side is ordinary host storage, not runtime state.
//
// Evidence under .ztd/data/eval/ is encrypted at rest with the runtime vault.
// A snapshot carries it in that form together with the vault key chain that
// sealed it (the vault header, the vault key sealed under the master key, and
// the key store's sealed master key blob where it keeps one), all ciphertext,
// so no plaintext leaves the runtime and taking a snapshot needs no vault. A
// restore opens each snapshot's vault from its key chain with the OS key store
// (or the user's SSH key), which outlives the runtime tree, and writes the
// evidence back through the evidence file service, sealing it under the
// current runtime vault. A snapshot whose key chain the key store cannot open
// restores nothing.
//
// Snapshots and restores hold the runtime's backup lock shared, so any number
// run at once. Purging deleted evidence holds it exclusively, so a snapshot
// never captures evidence that is being deleted and never references an
// object the purge removes.
type EvalBackup struct {
	fileSvc  fs.RuntimeFileService
	evidence *evidenceFiles
	now      func() time.Time
}

// NewEvalBackup returns a backup over fileSvc. Snapshots read the stored
// ciphertext beneath it; restores need it to be the evidence file service.
func NewEvalBackup(fileSvc fs.RuntimeFileService, now func() time.Time) *EvalBackup {
	evidence, _ := fileSvc.(*evidenceFiles)
	return &EvalBackup{fileSvc: SealedFiles(fileSvc), evidence: evidence, now: now}
}

// evalBackupRoots are the runtime-relative trees a backup covers: run and
// campaign evidence, and the rollout queue with the frozen model inventory.
func evalBackupRoots() []string {
	return []string{constants.EvaluationDataPath, constants.EvaluationDirname}
}

// evalBackupKeyChainPaths are the runtime-relative vault key chain files a
// snapshot records, in manifest order. The sealed master key blobs exist only
// for the key stores that keep one in the secrets directory.
func evalBackupKeyChainPaths() []string {
	return []string{constants.VaultHeaderRelPath, constants.VaultKeyRelPath, constants.MasterKeyDPAPIRelPath, constants.MasterKeySSHAgentRelPath}
}

// evalBackupKeyChainRequired are the key chain files without which no key
// store can open the vault that sealed a snapshot's evidence.
func evalBackupKeyChainRequired() []string {
	return []string{constants.VaultHeaderRelPath, constants.VaultKeyRelPath}
}

// evalBackupTransient reports process-local state that must not be carried
// across a wipe: a restored lease would make a dead run look held.
func evalBackupTransient(relPath string) bool {
	switch path.Base(relPath) {
	case constants.EvaluationRunLeaseFilename, constants.EvaluationActiveRunFilename, constants.EvaluationBackupLockFilename, constants.EvaluationRolloutQueueLockFilename:
		return true
	}
	return false
}

// Create writes a new timestamped snapshot under outputDir and returns it.
// File content is stored once per digest beneath outputDir/objects, so only
// files the backup directory has not seen before are copied.
func (b *EvalBackup) Create(ctx context.Context, outputDir string) (*EvalBackupReport, error) {
	destRoot, err := b.validateDestination(outputDir)
	if err != nil {
		return nil, err
	}
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	report, err := b.capture(ctx, destRoot)
	if err != nil {
		return nil, err
	}
	return b.commitSnapshot(ctx, destRoot, report)
}

// CreateIfChanged is Create for repeated automatic use: when the evidence is
// identical to the newest complete snapshot in outputDir, no snapshot is
// written and the report names the existing one with Unchanged set.
func (b *EvalBackup) CreateIfChanged(ctx context.Context, outputDir string) (*EvalBackupReport, error) {
	destRoot, err := b.validateDestination(outputDir)
	if err != nil {
		return nil, err
	}
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	previousDir, err := LatestEvalBackupSnapshot(destRoot)
	if err != nil && !errors.Is(err, constants.ErrEvaluationBackupNone) {
		return nil, err
	}
	report, err := b.capture(ctx, destRoot)
	if err != nil {
		return nil, err
	}
	if previousDir != "" {
		previous, err := readEvalBackupManifest(previousDir)
		if err != nil {
			return nil, err
		}
		if evalBackupSameFiles(previous.Files, report.Files) && evalBackupSameFiles(previous.KeyChain, report.KeyChain) {
			report.SnapshotDir = previousDir
			report.Unchanged = true
			return report, nil
		}
	}
	return b.commitSnapshot(ctx, destRoot, report)
}

// capture reads the evidence and stores any content the object store lacks.
// It writes no snapshot, so an unchanged tree costs reads and hashes only.
func (b *EvalBackup) capture(ctx context.Context, destRoot string) (*EvalBackupReport, error) {
	relPaths, err := b.collect(ctx)
	if err != nil {
		return nil, err
	}
	if len(relPaths) == 0 {
		return nil, constants.ErrEvaluationBackupEmpty
	}
	if err := os.MkdirAll(destRoot, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("evaluation: backup: create destination: %w", err)
	}
	report := &EvalBackupReport{CreatedAt: b.now().UTC()}
	for _, relPath := range relPaths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := b.fileSvc.ReadFile(ctx, relPath)
		if err != nil {
			return nil, fmt.Errorf("evaluation: backup: read %s: %w", relPath, err)
		}
		file := EvalBackupFile{Path: relPath, Size: int64(len(data)), SHA256: evalBackupDigest(data)}
		if err := storeEvalBackupObject(destRoot, file, data); err != nil {
			return nil, err
		}
		report.Files = append(report.Files, file)
		report.TotalBytes += file.Size
	}
	report.KeyChain, err = b.captureKeyChain(ctx, destRoot)
	if err != nil {
		return nil, err
	}
	if err := validateEvalBackupKeyChain(report.Files, report.KeyChain); err != nil {
		return nil, fmt.Errorf("evaluation: backup: %w", err)
	}
	return report, nil
}

// captureKeyChain stores the runtime vault key chain beside the evidence. The
// files are copied as stored: a wrapped header and sealed keys.
func (b *EvalBackup) captureKeyChain(ctx context.Context, destRoot string) ([]EvalBackupFile, error) {
	var chain []EvalBackupFile
	for _, relPath := range evalBackupKeyChainPaths() {
		data, err := b.fileSvc.ReadFile(ctx, relPath)
		if errors.Is(err, constants.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("evaluation: backup: read %s: %w", relPath, err)
		}
		file := EvalBackupFile{Path: relPath, Size: int64(len(data)), SHA256: evalBackupDigest(data)}
		if err := storeEvalBackupObject(destRoot, file, data); err != nil {
			return nil, err
		}
		chain = append(chain, file)
	}
	return chain, nil
}

// commitSnapshot reserves a snapshot directory and writes its manifest last,
// so a directory without one is an interrupted backup that is never restored.
func (b *EvalBackup) commitSnapshot(ctx context.Context, destRoot string, report *EvalBackupReport) (*EvalBackupReport, error) {
	snapshotBase := filepath.Join(destRoot, constants.EvaluationBackupDirPrefix+report.CreatedAt.Format(constants.EvaluationBackupTimestampLayout))
	snapshotDir := snapshotBase
	// Reserve each name atomically so simultaneous and same-second backups
	// cannot overwrite one another. Keep the unsuffixed name for the first.
	for sequence := 1; ; sequence++ {
		err := os.Mkdir(snapshotDir, constants.PermDirPrivate)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("evaluation: backup: create snapshot directory: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		snapshotDir = fmt.Sprintf("%s-%06d", snapshotBase, sequence)
	}
	manifest, err := json.MarshalIndent(EvalBackupManifest{
		SchemaVersion: evalBackupManifestVersion,
		CreatedAt:     report.CreatedAt.Format(time.RFC3339),
		Files:         report.Files,
		KeyChain:      report.KeyChain,
	}, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(snapshotDir, constants.EvaluationBackupManifestFilename), manifest, constants.PermFilePrivate)
	}
	if err != nil {
		// The directory was created by this call, so removing it cannot touch prior backups.
		_ = os.RemoveAll(snapshotDir)
		return nil, fmt.Errorf("evaluation: backup: write manifest: %w", err)
	}
	report.SnapshotDir = snapshotDir
	return report, nil
}

// sharedLock holds the runtime's backup lock for a snapshot or a restore. It
// fails only while a delete holds the lock to purge the backups.
func (b *EvalBackup) sharedLock(ctx context.Context) (io.Closer, error) {
	held, err := b.fileSvc.TryLockShared(ctx, constants.EvaluationBackupLockRelPath, constants.PermFilePrivate)
	if errors.Is(err, constants.ErrRuntimeFileLocked) {
		return nil, fmt.Errorf("evaluation: backup: an evaluation delete is purging the backups; retry when it finishes: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: backup: lock: %w", err)
	}
	return held, nil
}

// exclusiveLock holds the runtime's backup lock for purging deleted evidence.
// It fails while any snapshot or restore holds the lock.
func (b *EvalBackup) exclusiveLock(ctx context.Context) (io.Closer, error) {
	held, err := b.fileSvc.TryLock(ctx, constants.EvaluationBackupLockRelPath, constants.PermFilePrivate)
	if errors.Is(err, constants.ErrRuntimeFileLocked) {
		return nil, fmt.Errorf("evaluation: backup: a backup or restore is in progress; retry when it finishes: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("evaluation: backup: lock: %w", err)
	}
	return held, nil
}

// EvalBackupPurgeReport describes what purging deleted evidence removes from a
// backup directory.
type EvalBackupPurgeReport struct {
	// Files counts the manifest entries removed across all snapshots.
	Files int
	// Snapshots counts the snapshots rewritten, or removed when nothing else
	// remained in them.
	Snapshots int
	// Objects counts the stored contents that only removed entries referenced.
	Objects int
}

// evalBackupPurge is a planned removal of evidence from every complete
// snapshot in a backup directory. It is planned and applied under the
// exclusive backup lock, so the plan cannot go stale in between.
type evalBackupPurge struct {
	backupRoot string
	snapshots  []evalBackupSnapshotPurge
	orphaned   []string
	report     EvalBackupPurgeReport
}

// evalBackupSnapshotPurge is one snapshot's share of a purge: its manifest
// with only the kept files, and the files removed from it.
type evalBackupSnapshotPurge struct {
	dir      string
	manifest EvalBackupManifest
	dropped  []EvalBackupFile
}

// planEvalBackupPurge plans removing every file at or beneath the
// runtime-relative relPaths from each complete snapshot in backupDir. An
// object only those files reference is removed; content another snapshot or
// file still lists is kept. A directory with no snapshots has nothing to purge.
func planEvalBackupPurge(backupDir string, relPaths []string) (*evalBackupPurge, error) {
	roots := make([]string, 0, len(relPaths))
	for _, relPath := range relPaths {
		root := path.Clean(filepath.ToSlash(relPath))
		if !evalBackupBeneathRoots(root) {
			return nil, fmt.Errorf("%w: %q", constants.ErrEvaluationBackupPurgePathInvalid, relPath)
		}
		roots = append(roots, root)
	}
	plan := &evalBackupPurge{}
	snapshots, err := AllEvalBackupSnapshots(backupDir)
	if errors.Is(err, constants.ErrEvaluationBackupNone) {
		return plan, nil
	}
	if err != nil {
		return nil, err
	}
	plan.backupRoot = filepath.Dir(snapshots[0])
	referenced := make(map[string]struct{})
	candidates := make(map[string]struct{})
	for _, snapshotDir := range snapshots {
		manifest, err := readEvalBackupManifest(snapshotDir)
		if err != nil {
			return nil, err
		}
		entry := evalBackupSnapshotPurge{dir: snapshotDir, manifest: *manifest}
		entry.manifest.Files = nil
		for _, file := range manifest.Files {
			if evalBackupPathUnder(file.Path, roots) {
				entry.dropped = append(entry.dropped, file)
				candidates[file.SHA256] = struct{}{}
				continue
			}
			entry.manifest.Files = append(entry.manifest.Files, file)
			referenced[file.SHA256] = struct{}{}
		}
		// A snapshot left with no evidence is removed, and its key chain with it.
		keyChainUse := referenced
		if len(entry.dropped) > 0 && len(entry.manifest.Files) == 0 {
			keyChainUse = candidates
		}
		for _, file := range manifest.KeyChain {
			keyChainUse[file.SHA256] = struct{}{}
		}
		if len(entry.dropped) == 0 {
			continue
		}
		plan.snapshots = append(plan.snapshots, entry)
		plan.report.Files += len(entry.dropped)
	}
	for sha := range candidates {
		if _, kept := referenced[sha]; !kept {
			plan.orphaned = append(plan.orphaned, sha)
		}
	}
	sort.Strings(plan.orphaned)
	plan.report.Snapshots = len(plan.snapshots)
	plan.report.Objects = len(plan.orphaned)
	return plan, nil
}

// apply rewrites each affected snapshot, then removes the orphaned objects.
// Manifests change first, so an interruption leaves at worst unreferenced
// content behind, never a manifest that names missing content.
func (p *evalBackupPurge) apply() error {
	for _, snapshot := range p.snapshots {
		if err := snapshot.rewrite(); err != nil {
			return err
		}
	}
	for _, sha := range p.orphaned {
		if err := os.Remove(evalBackupObjectPath(p.backupRoot, sha)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("evaluation: backup purge: remove object %s: %w", sha, err)
		}
	}
	return nil
}

// rewrite replaces the snapshot's manifest with one listing only the kept
// files. A snapshot with nothing kept is removed.
func (s evalBackupSnapshotPurge) rewrite() error {
	if len(s.manifest.Files) == 0 {
		if err := os.RemoveAll(s.dir); err != nil {
			return fmt.Errorf("evaluation: backup purge: remove snapshot %s: %w", s.dir, err)
		}
		return nil
	}
	raw, err := json.MarshalIndent(s.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("evaluation: backup purge: encode manifest for %s: %w", s.dir, err)
	}
	temp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("evaluation: backup purge: rewrite manifest in %s: %w", s.dir, err)
	}
	tempName := temp.Name()
	_, err = temp.Write(raw)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tempName, constants.PermFilePrivate)
	}
	if err == nil {
		err = os.Rename(tempName, filepath.Join(s.dir, constants.EvaluationBackupManifestFilename))
	}
	if err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("evaluation: backup purge: rewrite manifest in %s: %w", s.dir, err)
	}
	return nil
}

// evalBackupPathUnder reports whether relPath is one of roots or beneath one.
func evalBackupPathUnder(relPath string, roots []string) bool {
	for _, root := range roots {
		if relPath == root || strings.HasPrefix(relPath, root+"/") {
			return true
		}
	}
	return false
}

// evalBackupBeneathRoots reports whether a clean relative path lies strictly
// beneath one of the backup roots, so a purge can never take a whole tree.
func evalBackupBeneathRoots(relPath string) bool {
	if path.IsAbs(relPath) {
		return false
	}
	for _, root := range evalBackupRoots() {
		if rest, ok := strings.CutPrefix(relPath, root+"/"); ok && rest != "" {
			return true
		}
	}
	return false
}

// evalBackupObjectPath is where the content for sha lives in the object store
// shared by the snapshots in backupDir.
func evalBackupObjectPath(backupDir, sha string) string {
	return filepath.Join(backupDir, evalBackupObjectsDir, sha[:2], sha)
}

// storeEvalBackupObject writes data to the object store unless an object of
// the expected size is already there. Objects appear atomically (temp file,
// then rename), so a present object is always complete.
func storeEvalBackupObject(backupDir string, file EvalBackupFile, data []byte) error {
	target := evalBackupObjectPath(backupDir, file.SHA256)
	if info, err := os.Stat(target); err == nil && info.Size() == file.Size {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(target), constants.PermDirPrivate); err != nil {
		return fmt.Errorf("evaluation: backup: create object directory for %s: %w", file.Path, err)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".tmp-*")
	if err != nil {
		return fmt.Errorf("evaluation: backup: store %s: %w", file.Path, err)
	}
	tempName := temp.Name()
	_, err = temp.Write(data)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tempName, constants.PermFilePrivate)
	}
	if err == nil {
		err = os.Rename(tempName, target)
	}
	if err != nil {
		_ = os.Remove(tempName)
		// Windows does not replace an existing file with Rename. Another
		// concurrent snapshot may have published this content-addressed object
		// after our initial Stat; in that case the desired result already exists.
		if info, statErr := os.Stat(target); statErr == nil && info.Size() == file.Size {
			return nil
		}
		return fmt.Errorf("evaluation: backup: store %s: %w", file.Path, err)
	}
	return nil
}

func evalBackupSameFiles(a, b []EvalBackupFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LatestEvalBackupSnapshot returns the newest complete snapshot directory in
// backupDir. A snapshot without a valid manifest is an interrupted backup and
// is skipped. It returns ErrEvaluationBackupNone when there is no such snapshot.
func LatestEvalBackupSnapshot(backupDir string) (string, error) {
	abs, names, err := evalBackupSnapshotNames(backupDir)
	if err != nil {
		return "", err
	}
	for i := len(names) - 1; i >= 0; i-- {
		dir := filepath.Join(abs, names[i])
		if _, err := readEvalBackupManifest(dir); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("%w: %s", constants.ErrEvaluationBackupNone, abs)
}

// AllEvalBackupSnapshots returns all complete snapshot directories in backupDir,
// sorted oldest first (creation order). A snapshot without a valid manifest is
// an interrupted backup and is skipped. It returns ErrEvaluationBackupNone when
// there are no such snapshots.
func AllEvalBackupSnapshots(backupDir string) ([]string, error) {
	abs, names, err := evalBackupSnapshotNames(backupDir)
	if err != nil {
		return nil, err
	}
	var snapshots []string
	for _, name := range names {
		dir := filepath.Join(abs, name)
		if _, err := readEvalBackupManifest(dir); err == nil {
			snapshots = append(snapshots, dir)
		}
	}
	if len(snapshots) == 0 {
		return nil, fmt.Errorf("%w: %s", constants.ErrEvaluationBackupNone, abs)
	}
	return snapshots, nil
}

// evalBackupSnapshotNames lists snapshot directory names oldest first. The
// timestamp layout is fixed-width UTC, so name order is creation order.
func evalBackupSnapshotNames(backupDir string) (string, []string, error) {
	abs, err := filepath.Abs(backupDir)
	if err != nil {
		return "", nil, fmt.Errorf("evaluation: backup: resolve directory: %w", err)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("%w: %s", constants.ErrEvaluationBackupNone, abs)
		}
		return "", nil, fmt.Errorf("evaluation: backup: list %s: %w", abs, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), constants.EvaluationBackupDirPrefix) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return abs, names, nil
}

// snapshotSources pairs each manifest entry with the object holding its
// content and the snapshot's key chain.
func snapshotSources(snapshotDir string, manifest *EvalBackupManifest) []evalBackupSource {
	backupRoot := filepath.Dir(snapshotDir)
	ids := make([]string, 0, len(manifest.KeyChain))
	for _, file := range manifest.KeyChain {
		ids = append(ids, file.Path+"="+file.SHA256)
	}
	chain := &evalBackupKeyChain{id: strings.Join(ids, ","), backupRoot: backupRoot, files: manifest.KeyChain}
	sources := make([]evalBackupSource, 0, len(manifest.Files))
	for _, file := range manifest.Files {
		sources = append(sources, evalBackupSource{file: file, payload: evalBackupObjectPath(backupRoot, file.SHA256), keyChain: chain})
	}
	return sources
}

// Restore restores one snapshot into the runtime tree. Existing files with
// identical content are left alone; differing ones fail the restore before any
// write unless overwrite is set.
func (b *EvalBackup) Restore(ctx context.Context, snapshotDir string, overwrite bool) (*EvalRestoreReport, error) {
	snapshotDir, err := filepath.Abs(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("evaluation: restore: resolve snapshot directory: %w", err)
	}
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	manifest, err := readEvalBackupManifest(snapshotDir)
	if err != nil {
		return nil, err
	}
	report, err := b.restoreSources(ctx, snapshotSources(snapshotDir, manifest), overwrite)
	if err != nil {
		return nil, err
	}
	report.SnapshotDir = snapshotDir
	report.Snapshots = 1
	return report, nil
}

// RestoreAll restores every file any complete snapshot in backupDir recorded,
// taking each path from the newest snapshot that holds it. Evidence a later
// snapshot no longer lists (lost in a wipe after an older snapshot, say) is
// still recovered, and the work is one pass over the merged set rather than
// one restore per snapshot. Evidence removed by a Deleter is purged from the
// backups too, so it is never restored.
func (b *EvalBackup) RestoreAll(ctx context.Context, backupDir string, overwrite bool) (*EvalRestoreReport, error) {
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	snapshots, err := AllEvalBackupSnapshots(backupDir)
	if err != nil {
		return nil, err
	}
	merged := make(map[string]evalBackupSource)
	for _, snapshotDir := range snapshots {
		manifest, err := readEvalBackupManifest(snapshotDir)
		if err != nil {
			return nil, err
		}
		for _, source := range snapshotSources(snapshotDir, manifest) {
			merged[source.file.Path] = source
		}
	}
	sources := make([]evalBackupSource, 0, len(merged))
	for _, source := range merged {
		sources = append(sources, source)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].file.Path < sources[j].file.Path })
	report, err := b.restoreSources(ctx, sources, overwrite)
	if err != nil {
		return nil, err
	}
	report.SnapshotDir = filepath.Dir(snapshots[0])
	report.Snapshots = len(snapshots)
	return report, nil
}

// restoreSources writes the sources into the runtime tree as the runtime held
// them: a record its snapshot's vault sealed is opened with that vault and
// written through the evidence file service, sealing it under the runtime
// vault whatever vault the snapshot was taken under; anything else, such as
// the disclosure-safe export ExportRun writes in the clear (INV-EVAL-EVID-04),
// is written back byte for byte. Content is read, verified against its digest,
// and opened one file at a time, so memory stays near one file. Every source
// is read and opened before the first write, so a corrupt backup or a key
// chain the key store cannot open changes nothing.
func (b *EvalBackup) restoreSources(ctx context.Context, sources []evalBackupSource, overwrite bool) (*EvalRestoreReport, error) {
	if b.evidence == nil {
		return nil, fmt.Errorf("evaluation: restore: %w", constants.ErrEvaluationEvidenceVaultRequired)
	}
	vaults := make(map[string]EvidenceVault)
	report := &EvalRestoreReport{}
	var pending []evalBackupSource
	var conflicts []EvalBackupFile
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := b.restoreContent(ctx, source, vaults)
		if err != nil {
			return nil, err
		}
		same, exists, err := b.matchesExisting(ctx, source.file.Path, content)
		if err != nil {
			return nil, err
		}
		switch {
		case exists && same:
			report.Unchanged = append(report.Unchanged, source.file)
		case exists && !overwrite:
			conflicts = append(conflicts, source.file)
		default:
			pending = append(pending, source)
		}
	}
	if len(conflicts) > 0 {
		return nil, evalBackupConflictError(conflicts)
	}
	for _, source := range pending {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := b.restoreContent(ctx, source, vaults)
		if err != nil {
			return nil, err
		}
		if err := content.files(b).WriteFile(ctx, source.file.Path, content.data, constants.PermFilePrivate); err != nil {
			return nil, fmt.Errorf("evaluation: restore: write %s: %w", source.file.Path, err)
		}
		report.Restored = append(report.Restored, source.file)
	}
	return report, nil
}

// evalRestoreContent is one file's content as a restore writes it: data is
// plaintext for a sealed record and the stored bytes otherwise.
type evalRestoreContent struct {
	data   []byte
	sealed bool
}

// files is the file service that writes and reads the content: the evidence
// file service seals a record, and the stored form is written as is.
func (c evalRestoreContent) files(b *EvalBackup) fs.RuntimeFileService {
	if c.sealed {
		return b.evidence
	}
	return b.fileSvc
}

// restoreContent returns a source's verified content. A file under a sealed
// root is opened with its snapshot's vault; one that vault did not seal was
// stored in the clear and is returned as stored.
func (b *EvalBackup) restoreContent(ctx context.Context, source evalBackupSource, vaults map[string]EvidenceVault) (evalRestoreContent, error) {
	data, err := readVerifiedEvalBackupPayload(source.file, source.payload)
	if err != nil {
		return evalRestoreContent{}, err
	}
	aad, sealedRoot := sealedPathAAD(source.file.Path)
	if !sealedRoot {
		return evalRestoreContent{data: data}, nil
	}
	v, err := b.keyChainVault(ctx, source.keyChain, vaults)
	if err != nil {
		return evalRestoreContent{}, err
	}
	plaintext, err := v.DecryptWithAAD(data, aad)
	if err != nil {
		//nolint:nilerr // intentional: the payload digest is already verified; a clear ExportRun bundle under a sealed root is restored as stored
		return evalRestoreContent{data: data}, nil
	}
	return evalRestoreContent{data: plaintext, sealed: true}, nil
}

// keyChainVault opens the vault a snapshot's key chain holds, once per
// distinct key chain.
func (b *EvalBackup) keyChainVault(ctx context.Context, chain *evalBackupKeyChain, vaults map[string]EvidenceVault) (EvidenceVault, error) {
	if v, ok := vaults[chain.id]; ok {
		return v, nil
	}
	files := make(map[string][]byte, len(chain.files))
	for _, file := range chain.files {
		data, err := readVerifiedEvalBackupPayload(file, evalBackupObjectPath(chain.backupRoot, file.SHA256))
		if err != nil {
			return nil, err
		}
		files[file.Path] = data
	}
	v, err := b.evidence.openKeyChain(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("evaluation: restore: %w", err)
	}
	vaults[chain.id] = v
	return v, nil
}

func readVerifiedEvalBackupPayload(file EvalBackupFile, payload string) ([]byte, error) {
	data, err := os.ReadFile(payload)
	if err != nil {
		return nil, fmt.Errorf("evaluation: restore: read %s: %w", file.Path, err)
	}
	if int64(len(data)) != file.Size || evalBackupDigest(data) != file.SHA256 {
		return nil, fmt.Errorf("evaluation: restore: %s: %w", file.Path, constants.ErrEvaluationBackupIntegrity)
	}
	return data, nil
}

// matchesExisting compares the runtime file at relPath with the content a
// restore would write, read the same way the restore writes it. A sealed
// record the runtime vault cannot open differs.
func (b *EvalBackup) matchesExisting(ctx context.Context, relPath string, want evalRestoreContent) (same, exists bool, err error) {
	files := want.files(b)
	exists, err = files.FileExists(ctx, relPath)
	if err != nil {
		return false, false, fmt.Errorf("evaluation: restore: check %s: %w", relPath, err)
	}
	if !exists {
		return false, false, nil
	}
	current, err := files.ReadFile(ctx, relPath)
	if errors.Is(err, constants.ErrEvaluationEvidenceDecryptFailed) {
		return false, true, nil
	}
	if err != nil {
		return false, true, fmt.Errorf("evaluation: restore: read existing %s: %w", relPath, err)
	}
	return bytes.Equal(current, want.data), true, nil
}

func evalBackupConflictError(conflicts []EvalBackupFile) error {
	names := make([]string, 0, evalBackupConflictSample)
	for i, file := range conflicts {
		if i == evalBackupConflictSample {
			break
		}
		names = append(names, file.Path)
	}
	more := ""
	if extra := len(conflicts) - len(names); extra > 0 {
		more = fmt.Sprintf(" and %d more", extra)
	}
	return fmt.Errorf("evaluation: restore: %d file(s) differ from the backup (%s%s); pass --overwrite to replace them: %w",
		len(conflicts), strings.Join(names, ", "), more, constants.ErrEvaluationBackupConflict)
}

// collect lists every runtime-relative file under the backup roots, sorted.
func (b *EvalBackup) collect(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var files []string
	for _, root := range evalBackupRoots() {
		if err := b.walk(ctx, root, func(relPath string) {
			if _, dup := seen[relPath]; dup {
				return
			}
			seen[relPath] = struct{}{}
			files = append(files, relPath)
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

func (b *EvalBackup) walk(ctx context.Context, relDir string, visit func(string)) error {
	entries, err := b.fileSvc.ReadDir(ctx, relDir)
	if err != nil {
		if errors.Is(err, constants.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("evaluation: backup: list %s: %w", relDir, err)
	}
	for _, entry := range entries {
		relPath := path.Join(relDir, entry.Name())
		switch {
		case entry.IsDir():
			if err := b.walk(ctx, relPath, visit); err != nil {
				return err
			}
		case entry.Type().IsRegular():
			if !evalBackupTransient(relPath) {
				visit(relPath)
			}
		default:
			return fmt.Errorf("evaluation: backup: %s: %w", relPath, constants.ErrEvaluationBackupUnsupportedEntry)
		}
	}
	return nil
}

// validateDestination resolves outputDir to an absolute path and rejects any
// location inside the runtime directory, where a wipe or the backup's own
// source walk would reach it.
func (b *EvalBackup) validateDestination(outputDir string) (string, error) {
	if strings.TrimSpace(outputDir) == "" {
		return "", fmt.Errorf("%w: empty path", constants.ErrEvaluationBackupDestinationInvalid)
	}
	abs, err := filepath.Abs(outputDir)
	if err != nil {
		return "", fmt.Errorf("evaluation: backup: resolve destination: %w", err)
	}
	runtimeDir := b.fileSvc.Resolve("")
	if evalBackupWithin(runtimeDir, abs) || evalBackupWithin(evalBackupResolveExisting(runtimeDir), evalBackupResolveExisting(abs)) {
		return "", fmt.Errorf("%w: %s", constants.ErrEvaluationBackupDestinationInvalid, abs)
	}
	return abs, nil
}

func evalBackupWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// evalBackupResolveExisting resolves symlinks in the longest existing prefix of
// p, so a not-yet-created destination beneath a symlinked directory still resolves.
func evalBackupResolveExisting(p string) string {
	remainder := ""
	for current := p; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return p
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func readEvalBackupManifest(snapshotDir string) (*EvalBackupManifest, error) {
	raw, err := os.ReadFile(filepath.Join(snapshotDir, constants.EvaluationBackupManifestFilename))
	if err != nil {
		return nil, fmt.Errorf("evaluation: restore: read manifest in %s: %w: %w", snapshotDir, constants.ErrEvaluationBackupManifestInvalid, err)
	}
	var manifest EvalBackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("evaluation: restore: decode manifest: %w: %w", constants.ErrEvaluationBackupManifestInvalid, err)
	}
	if manifest.SchemaVersion != evalBackupManifestVersion {
		return nil, fmt.Errorf("evaluation: restore: manifest schema %q: %w", manifest.SchemaVersion, constants.ErrEvaluationBackupManifestInvalid)
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		if err := validateEvalBackupEntry(file); err != nil {
			return nil, err
		}
		if _, dup := seen[file.Path]; dup {
			return nil, fmt.Errorf("evaluation: restore: duplicate manifest path %q: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
		}
		seen[file.Path] = struct{}{}
	}
	if err := validateEvalBackupKeyChain(manifest.Files, manifest.KeyChain); err != nil {
		return nil, fmt.Errorf("evaluation: restore: %s: %w", snapshotDir, err)
	}
	return &manifest, nil
}

// validateEvalBackupKeyChain confines key chain entries to the vault key chain
// files, and requires the files that open the vault when any listed evidence
// is sealed.
func validateEvalBackupKeyChain(files, chain []EvalBackupFile) error {
	present := make(map[string]struct{}, len(chain))
	for _, file := range chain {
		if !slices.Contains(evalBackupKeyChainPaths(), file.Path) {
			return fmt.Errorf("key chain path %q is not a vault key chain file: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
		}
		if _, dup := present[file.Path]; dup {
			return fmt.Errorf("duplicate key chain path %q: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
		}
		if file.Size < 0 || len(file.SHA256) != sha256.Size*2 {
			return fmt.Errorf("key chain entry %q has an invalid size or digest: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
		}
		present[file.Path] = struct{}{}
	}
	sealed := slices.ContainsFunc(files, func(file EvalBackupFile) bool {
		_, ok := sealedPathAAD(file.Path)
		return ok
	})
	if !sealed {
		return nil
	}
	for _, required := range evalBackupKeyChainRequired() {
		if _, ok := present[required]; !ok {
			return fmt.Errorf("%w: %s", constants.ErrEvaluationBackupKeyChainMissing, required)
		}
	}
	return nil
}

// validateEvalBackupEntry confines a manifest path to the backup roots so a
// tampered manifest cannot direct a write elsewhere in the runtime tree.
func validateEvalBackupEntry(file EvalBackupFile) error {
	if cleaned := path.Clean(file.Path); cleaned != file.Path || path.IsAbs(file.Path) || file.Path == "." {
		return fmt.Errorf("evaluation: restore: manifest path %q is not a clean relative path: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
	}
	inRoot := false
	for _, root := range evalBackupRoots() {
		if strings.HasPrefix(file.Path, root+"/") {
			inRoot = true
			break
		}
	}
	if !inRoot || evalBackupTransient(file.Path) {
		return fmt.Errorf("evaluation: restore: manifest path %q is outside the evaluation evidence trees: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
	}
	if file.Size < 0 || len(file.SHA256) != sha256.Size*2 {
		return fmt.Errorf("evaluation: restore: manifest entry %q has an invalid size or digest: %w", file.Path, constants.ErrEvaluationBackupManifestInvalid)
	}
	return nil
}

func evalBackupDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

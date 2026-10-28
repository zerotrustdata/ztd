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
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// EvalSnapshotCategoryStats records the file count and stored byte total for
// one logical category of evaluation evidence inside a snapshot.
type EvalSnapshotCategoryStats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// EvalSnapshotContentSummary breaks down the logical contents of an evaluation
// snapshot or merged snapshot set by domain entity and file category.
type EvalSnapshotContentSummary struct {
	TotalFiles              int                       `json:"total_files"`
	TotalBytes              int64                     `json:"total_bytes"`
	CampaignCount           int                       `json:"campaign_count"`
	CampaignIDs             []string                  `json:"campaign_ids"`
	Campaigns               EvalSnapshotCategoryStats `json:"campaigns"`
	RunCount                int                       `json:"run_count"`
	RunIDs                  []string                  `json:"run_ids"`
	Runs                    EvalSnapshotCategoryStats `json:"runs"`
	AssignmentCount         int                       `json:"assignment_count"`
	AgentTraceCount         int                       `json:"agent_trace_count"`
	AssignmentResultCount   int                       `json:"assignment_result_count"`
	Assignments             EvalSnapshotCategoryStats `json:"assignments"`
	WitnessEvidenceCount    int                       `json:"witness_evidence_count"`
	ProviderObservationRefs int                       `json:"provider_observation_refs"`
	ModelProvenanceRefs     int                       `json:"model_provenance_refs"`
	WitnessEvidence         EvalSnapshotCategoryStats `json:"witness_evidence"`
	RolloutCount            int                       `json:"rollout_count"`
	Rollouts                EvalSnapshotCategoryStats `json:"rollouts"`
	InventoryCount          int                       `json:"inventory_count"`
	Inventories             EvalSnapshotCategoryStats `json:"inventories"`
	Other                   EvalSnapshotCategoryStats `json:"other"`
	KeyChain                EvalSnapshotCategoryStats `json:"key_chain"`
}

// EvalSnapshotInspectReport describes the storage layout, deduplication
// efficiency, bloat, and content summary of a single snapshot directory or a
// multi-snapshot backup root.
type EvalSnapshotInspectReport struct {
	TargetDir               string                     `json:"target_dir"`
	IsSingleSnapshot        bool                       `json:"is_single_snapshot"`
	LatestSnapshotDir       string                     `json:"latest_snapshot_dir"`
	SnapshotCount           int                        `json:"snapshot_count"`
	ActiveSnapshotCount     int                        `json:"active_snapshot_count"`
	SupersededSnapshotCount int                        `json:"superseded_snapshot_count"`
	IncompleteSnapshotCount int                        `json:"incomplete_snapshot_count"`
	DistinctKeyChains       int                        `json:"distinct_key_chains"`
	LogicalFileCount        int                        `json:"logical_file_count"`
	LogicalBytes            int64                      `json:"logical_bytes"`
	ReferencedObjectCount   int                        `json:"referenced_object_count"`
	ReferencedObjectBytes   int64                      `json:"referenced_object_bytes"`
	SupersededObjectCount   int                        `json:"superseded_object_count"`
	SupersededObjectBytes   int64                      `json:"superseded_object_bytes"`
	UnreferencedObjectCount int                        `json:"unreferenced_object_count"`
	UnreferencedObjectBytes int64                      `json:"unreferenced_object_bytes"`
	TotalDiskObjectCount    int                        `json:"total_disk_object_count"`
	TotalDiskObjectBytes    int64                      `json:"total_disk_object_bytes"`
	TotalManifestBytes      int64                      `json:"total_manifest_bytes"`
	ActiveManifestBytes     int64                      `json:"active_manifest_bytes"`
	ReclaimableBytes        int64                      `json:"reclaimable_bytes"`
	Content                 EvalSnapshotContentSummary `json:"content"`
}

// EvalSnapshotCompareReport describes the size and content differences between
// two snapshots (or between a snapshot/backup directory and the live runtime).
type EvalSnapshotCompareReport struct {
	LeftTarget        string                     `json:"left_target"`
	RightTarget       string                     `json:"right_target"`
	FileCountDelta    int                        `json:"file_count_delta"`
	LogicalBytesDelta int64                      `json:"logical_bytes_delta"`
	Added             []string                   `json:"added"`
	Removed           []string                   `json:"removed"`
	Modified          []string                   `json:"modified"`
	Unchanged         []string                   `json:"unchanged"`
	ResealedUnchanged []string                   `json:"resealed_unchanged"`
	AddedCampaigns    []string                   `json:"added_campaigns"`
	RemovedCampaigns  []string                   `json:"removed_campaigns"`
	AddedRuns         []string                   `json:"added_runs"`
	RemovedRuns       []string                   `json:"removed_runs"`
	LeftContent       EvalSnapshotContentSummary `json:"left_content"`
	RightContent      EvalSnapshotContentSummary `json:"right_content"`
}

// EvalSnapshotVerifyReport describes a dry-run cryptographic and structural
// verification of a snapshot or merged backup directory against the host key
// store and live runtime tree, without writing to the runtime tree.
type EvalSnapshotVerifyReport struct {
	SnapshotDir       string                     `json:"snapshot_dir"`
	Snapshots         int                        `json:"snapshot_count"`
	Restorable        bool                       `json:"restorable"`
	VerifiedFiles     int                        `json:"verified_files"`
	VerifiedBytes     int64                      `json:"verified_bytes"`
	KeyChainsVerified int                        `json:"key_chains_verified"`
	WouldRestore      []EvalBackupFile           `json:"would_restore"`
	Unchanged         []EvalBackupFile           `json:"unchanged"`
	Conflicts         []EvalBackupFile           `json:"conflicts"`
	Content           EvalSnapshotContentSummary `json:"content"`
}

// EvalBackupCompactReport describes the snapshots and unreferenced objects
// pruned when compacting a backup directory.
type EvalBackupCompactReport struct {
	BackupDir                  string `json:"backup_dir"`
	LatestSnapshotDir          string `json:"latest_snapshot_dir"`
	KeptSnapshots              int    `json:"kept_snapshots"`
	RemovedSupersededSnapshots int    `json:"removed_superseded_snapshots"`
	RemovedIncompleteSnapshots int    `json:"removed_incomplete_snapshots"`
	RemovedObjects             int    `json:"removed_objects"`
	ReclaimedManifestBytes     int64  `json:"reclaimed_manifest_bytes"`
	ReclaimedObjectBytes       int64  `json:"reclaimed_object_bytes"`
	TotalReclaimedBytes        int64  `json:"total_reclaimed_bytes"`
}

// SummarizeEvalSnapshotFiles classifies the files and key chain of a snapshot
// into campaign, run, assignment, trace, witness evidence, rollout, and
// inventory counts and byte totals.
func SummarizeEvalSnapshotFiles(files, keyChain []EvalBackupFile) EvalSnapshotContentSummary {
	summary := EvalSnapshotContentSummary{
		TotalFiles:  len(files),
		CampaignIDs: []string{},
		RunIDs:      []string{},
	}
	campaignSet := make(map[string]struct{})
	runSet := make(map[string]struct{})

	campaignsPrefix := filepath.ToSlash(constants.EvaluationCampaignsPath) + "/"
	runsPrefix := filepath.ToSlash(constants.EvaluationRunsPath) + "/"
	rolloutsPrefix := filepath.ToSlash(constants.EvaluationRolloutsPath) + "/"
	inventoriesPrefix := path.Join(constants.EvaluationDirname, constants.EvaluationInventoriesDirname) + "/"
	modelInventoryPath := filepath.ToSlash(constants.EvaluationModelInventoryPath)

	for _, file := range files {
		p := filepath.ToSlash(file.Path)
		summary.TotalBytes += file.Size

		switch {
		case strings.HasPrefix(p, campaignsPrefix):
			summary.Campaigns.Files++
			summary.Campaigns.Bytes += file.Size
			rest := strings.TrimPrefix(p, campaignsPrefix)
			if campaignID, _, ok := strings.Cut(rest, "/"); ok && campaignID != "" {
				campaignSet[campaignID] = struct{}{}
			}

		case strings.HasPrefix(p, runsPrefix):
			summary.Runs.Files++
			summary.Runs.Bytes += file.Size
			rest := strings.TrimPrefix(p, runsPrefix)
			runID, subPath, ok := strings.Cut(rest, "/")
			if ok && runID != "" {
				runSet[runID] = struct{}{}
			}
			switch {
			case strings.HasPrefix(subPath, "assignments/"):
				summary.Assignments.Files++
				summary.Assignments.Bytes += file.Size
				base := path.Base(subPath)
				switch {
				case strings.HasSuffix(base, "-agent-trace"+constants.FileExtJSON):
					summary.AgentTraceCount++
				case strings.HasSuffix(base, "-result"+constants.FileExtJSON):
					summary.AssignmentResultCount++
				case strings.HasSuffix(base, constants.FileExtJSON):
					summary.AssignmentCount++
				}
			case strings.HasPrefix(subPath, "evidence/"):
				summary.WitnessEvidence.Files++
				summary.WitnessEvidence.Bytes += file.Size
				evRest := strings.TrimPrefix(subPath, "evidence/")
				switch {
				case strings.HasPrefix(evRest, "provider-observations/") && path.Base(evRest) == "window-ref.json":
					summary.ProviderObservationRefs++
				case strings.HasPrefix(evRest, "model-provenance/") && path.Base(evRest) == "window-ref.json":
					summary.ModelProvenanceRefs++
				case !strings.Contains(evRest, "/") && strings.HasSuffix(evRest, constants.FileExtJSON):
					summary.WitnessEvidenceCount++
				}
			}

		case strings.HasPrefix(p, rolloutsPrefix):
			summary.Rollouts.Files++
			summary.Rollouts.Bytes += file.Size
			if strings.HasSuffix(p, constants.FileExtJSON) {
				summary.RolloutCount++
			}

		case p == modelInventoryPath || strings.HasPrefix(p, inventoriesPrefix):
			summary.Inventories.Files++
			summary.Inventories.Bytes += file.Size
			if strings.HasSuffix(p, constants.FileExtJSON) {
				summary.InventoryCount++
			}

		default:
			summary.Other.Files++
			summary.Other.Bytes += file.Size
		}
	}

	for _, file := range keyChain {
		summary.KeyChain.Files++
		summary.KeyChain.Bytes += file.Size
	}

	for id := range campaignSet {
		summary.CampaignIDs = append(summary.CampaignIDs, id)
	}
	sort.Strings(summary.CampaignIDs)
	summary.CampaignCount = len(summary.CampaignIDs)

	for id := range runSet {
		summary.RunIDs = append(summary.RunIDs, id)
	}
	sort.Strings(summary.RunIDs)
	summary.RunCount = len(summary.RunIDs)

	return summary
}

// classifySnapshotsNewestFirst partitions complete snapshots (sorted oldest to
// newest) into active snapshots (those contributing at least one newest path to
// a merged restore) and superseded snapshots (those whose paths are 100%
// covered by newer snapshots). Kept snapshots are returned in creation order.
func classifySnapshotsNewestFirst(complete []loadedEvalSnapshot) (kept, superseded []loadedEvalSnapshot) {
	coveredPaths := make(map[string]struct{})
	var keptRev []loadedEvalSnapshot
	for i := len(complete) - 1; i >= 0; i-- {
		snap := complete[i]
		contributes := i == len(complete)-1
		for _, file := range snap.manifest.Files {
			if _, seen := coveredPaths[file.Path]; !seen {
				contributes = true
				coveredPaths[file.Path] = struct{}{}
			}
		}
		if contributes {
			keptRev = append(keptRev, snap)
		} else {
			superseded = append(superseded, snap)
		}
	}
	kept = make([]loadedEvalSnapshot, 0, len(keptRev))
	for i := len(keptRev) - 1; i >= 0; i-- {
		kept = append(kept, keptRev[i])
	}
	return kept, superseded
}

// resolveSnapshotTarget loads either a single snapshot directory (when
// targetDir directly contains eval-backup.json) or a backup root directory.
func resolveSnapshotTarget(targetDir string) (absTarget, backupRoot string, isSingle bool, complete []loadedEvalSnapshot, incomplete []string, err error) {
	absTarget, err = filepath.Abs(targetDir)
	if err != nil {
		return "", "", false, nil, nil, fmt.Errorf("evaluation: snapshot: resolve %s: %w", targetDir, err)
	}
	manifestPath := filepath.Join(absTarget, constants.EvaluationBackupManifestFilename)
	if info, statErr := os.Stat(manifestPath); statErr == nil && info.Mode().IsRegular() {
		manifest, err := readEvalBackupManifest(absTarget)
		if err != nil {
			return "", "", true, nil, nil, err
		}
		return absTarget, filepath.Dir(absTarget), true, []loadedEvalSnapshot{{
			dir:           absTarget,
			manifest:      manifest,
			manifestBytes: info.Size(),
		}}, nil, nil
	}
	complete, incomplete, err = loadEvalBackupSnapshots(absTarget)
	if err != nil {
		return "", "", false, nil, nil, err
	}
	return absTarget, absTarget, false, complete, incomplete, nil
}

// Inspect analyzes a single snapshot directory or a multi-snapshot backup root,
// reporting snapshot counts, storage efficiency, reclaimable bloat, and a
// structured breakdown of campaigns, runs, traces, witness evidence, rollouts,
// and inventories.
func (b *EvalBackup) Inspect(ctx context.Context, targetDir string) (*EvalSnapshotInspectReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absTarget, backupRoot, isSingle, complete, incomplete, err := resolveSnapshotTarget(targetDir)
	if err != nil {
		return nil, err
	}
	kept, superseded := classifySnapshotsNewestFirst(complete)
	sources := mergeEvalBackupSources(kept)
	activeFiles := make([]EvalBackupFile, 0, len(sources))
	for _, s := range sources {
		activeFiles = append(activeFiles, s.file)
	}

	activeDigests := make(map[string]int64)
	allDigests := make(map[string]int64)
	keyChainIDs := make(map[string]struct{})
	var totalManifestBytes, activeManifestBytes, compactActiveManifestBytes int64

	for _, snap := range complete {
		totalManifestBytes += snap.manifestBytes
		var kcParts []string
		for _, kc := range snap.manifest.KeyChain {
			kcParts = append(kcParts, kc.Path+"="+kc.SHA256)
			allDigests[kc.SHA256] = kc.Size
		}
		keyChainIDs[strings.Join(kcParts, ",")] = struct{}{}
		for _, f := range snap.manifest.Files {
			allDigests[f.SHA256] = f.Size
		}
	}

	for _, snap := range kept {
		activeManifestBytes += snap.manifestBytes
		if compactRaw, mErr := json.Marshal(snap.manifest); mErr == nil {
			compactActiveManifestBytes += int64(len(compactRaw))
		} else {
			compactActiveManifestBytes += snap.manifestBytes
		}
		for _, kc := range snap.manifest.KeyChain {
			activeDigests[kc.SHA256] = kc.Size
		}
		for _, f := range snap.manifest.Files {
			activeDigests[f.SHA256] = f.Size
		}
	}

	latest := complete[len(complete)-1]
	report := &EvalSnapshotInspectReport{
		TargetDir:               absTarget,
		IsSingleSnapshot:        isSingle,
		LatestSnapshotDir:       latest.dir,
		SnapshotCount:           len(complete),
		ActiveSnapshotCount:     len(kept),
		SupersededSnapshotCount: len(superseded),
		IncompleteSnapshotCount: len(incomplete),
		DistinctKeyChains:       len(keyChainIDs),
		LogicalFileCount:        len(activeFiles),
		TotalManifestBytes:      totalManifestBytes,
		ActiveManifestBytes:     activeManifestBytes,
		Content:                 SummarizeEvalSnapshotFiles(activeFiles, latest.manifest.KeyChain),
	}
	report.LogicalBytes = report.Content.TotalBytes

	objectsRoot := filepath.Join(backupRoot, evalBackupObjectsDir)
	if !isSingle {
		if walkErr := filepath.WalkDir(objectsRoot, func(_ string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrNotExist) {
					return nil
				}
				return walkErr
			}
			if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".tmp-") {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			sz := info.Size()
			sha := entry.Name()
			report.TotalDiskObjectCount++
			report.TotalDiskObjectBytes += sz
			if _, ok := activeDigests[sha]; ok {
				report.ReferencedObjectCount++
				report.ReferencedObjectBytes += sz
			} else if _, ok := allDigests[sha]; ok {
				report.SupersededObjectCount++
				report.SupersededObjectBytes += sz
			} else {
				report.UnreferencedObjectCount++
				report.UnreferencedObjectBytes += sz
			}
			return nil
		}); walkErr != nil {
			return nil, fmt.Errorf("evaluation: inspect objects in %s: %w", objectsRoot, walkErr)
		}
		reclaimableManifest := max(int64(0), totalManifestBytes-compactActiveManifestBytes)
		reclaimableObjects := max(int64(0), report.TotalDiskObjectBytes-report.ReferencedObjectBytes)
		report.ReclaimableBytes = reclaimableManifest + reclaimableObjects
	} else {
		for _, sz := range activeDigests {
			report.ReferencedObjectCount++
			report.ReferencedObjectBytes += sz
		}
	}

	return report, nil
}

// Verify performs a complete cryptographic and structural dry-run restore check
// of a single snapshot directory without writing anything to the runtime tree.
func (b *EvalBackup) Verify(ctx context.Context, snapshotDir string) (*EvalSnapshotVerifyReport, error) {
	snapshotDir, err := filepath.Abs(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("evaluation: verify snapshot: resolve directory: %w", err)
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
	sources := snapshotSources(snapshotDir, manifest)
	return b.verifySources(ctx, snapshotDir, 1, sources, manifest.KeyChain)
}

// VerifyAll performs a complete cryptographic and structural dry-run restore
// check across the merged snapshot set in backupDir without writing anything to
// the runtime tree.
func (b *EvalBackup) VerifyAll(ctx context.Context, backupDir string) (*EvalSnapshotVerifyReport, error) {
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	loaded, _, err := loadEvalBackupSnapshots(backupDir)
	if err != nil {
		return nil, err
	}
	sources := mergeEvalBackupSources(loaded)
	latestKeyChain := loaded[len(loaded)-1].manifest.KeyChain
	return b.verifySources(ctx, filepath.Dir(loaded[0].dir), len(loaded), sources, latestKeyChain)
}

func (b *EvalBackup) verifySources(ctx context.Context, targetDir string, snapshotCount int, sources []evalBackupSource, latestKeyChain []EvalBackupFile) (*EvalSnapshotVerifyReport, error) {
	vaults := make(map[string]EvidenceVault)
	plan, err := b.planRestoreSources(ctx, sources, vaults)
	if err != nil {
		return nil, err
	}
	files := make([]EvalBackupFile, 0, len(sources))
	for _, s := range sources {
		files = append(files, s.file)
	}
	wouldRestore := make([]EvalBackupFile, 0, len(plan.wouldRestore))
	for _, s := range plan.wouldRestore {
		wouldRestore = append(wouldRestore, s.file)
	}
	conflicts := make([]EvalBackupFile, 0, len(plan.conflicts))
	for _, s := range plan.conflicts {
		conflicts = append(conflicts, s.file)
	}
	unchanged := plan.unchanged
	if unchanged == nil {
		unchanged = []EvalBackupFile{}
	}
	return &EvalSnapshotVerifyReport{
		SnapshotDir:       targetDir,
		Snapshots:         snapshotCount,
		Restorable:        true,
		VerifiedFiles:     len(sources),
		VerifiedBytes:     plan.totalBytes,
		KeyChainsVerified: len(vaults),
		WouldRestore:      wouldRestore,
		Unchanged:         unchanged,
		Conflicts:         conflicts,
		Content:           SummarizeEvalSnapshotFiles(files, latestKeyChain),
	}, nil
}

// evalComparableEntry holds both the stored (ciphertext) digest and the
// authenticated plaintext digest of a file so Compare can distinguish actual
// content changes from vault reseals.
type evalComparableEntry struct {
	file            EvalBackupFile
	storedSHA256    string
	plaintextSHA256 string
}

func (b *EvalBackup) loadComparableTarget(ctx context.Context, targetDir string, vaults map[string]EvidenceVault) (string, map[string]evalComparableEntry, EvalSnapshotContentSummary, error) {
	if strings.TrimSpace(targetDir) == "" {
		// Compare against the live runtime tree.
		if b.evidence == nil {
			return "", nil, EvalSnapshotContentSummary{}, fmt.Errorf("evaluation: compare runtime: %w", constants.ErrEvaluationEvidenceVaultRequired)
		}
		paths, err := b.collect(ctx)
		if err != nil {
			return "", nil, EvalSnapshotContentSummary{}, err
		}
		entries := make(map[string]evalComparableEntry, len(paths))
		files := make([]EvalBackupFile, 0, len(paths))
		for _, relPath := range paths {
			if err := ctx.Err(); err != nil {
				return "", nil, EvalSnapshotContentSummary{}, err
			}
			stored, err := b.fileSvc.ReadFile(ctx, relPath)
			if err != nil {
				return "", nil, EvalSnapshotContentSummary{}, fmt.Errorf("evaluation: compare runtime: read %s: %w", relPath, err)
			}
			storedDigest := evalBackupDigest(stored)
			plainDigest := storedDigest
			if _, sealed := sealedPathAAD(relPath); sealed {
				plain, err := b.evidence.ReadFile(ctx, relPath)
				if err != nil {
					return "", nil, EvalSnapshotContentSummary{}, err
				}
				plainDigest = evalBackupDigest(plain)
			}
			f := EvalBackupFile{Path: relPath, Size: int64(len(stored)), SHA256: storedDigest}
			files = append(files, f)
			entries[relPath] = evalComparableEntry{
				file:            f,
				storedSHA256:    storedDigest,
				plaintextSHA256: plainDigest,
			}
		}
		var keyChain []EvalBackupFile
		for _, kcPath := range evalBackupKeyChainPaths() {
			data, err := b.fileSvc.ReadFile(ctx, kcPath)
			if err == nil {
				keyChain = append(keyChain, EvalBackupFile{Path: kcPath, Size: int64(len(data)), SHA256: evalBackupDigest(data)})
			}
		}
		return b.fileSvc.Resolve(""), entries, SummarizeEvalSnapshotFiles(files, keyChain), nil
	}

	absTarget, _, _, complete, _, err := resolveSnapshotTarget(targetDir)
	if err != nil {
		return "", nil, EvalSnapshotContentSummary{}, err
	}
	kept, _ := classifySnapshotsNewestFirst(complete)
	sources := mergeEvalBackupSources(kept)
	entries := make(map[string]evalComparableEntry, len(sources))
	files := make([]EvalBackupFile, 0, len(sources))
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return "", nil, EvalSnapshotContentSummary{}, err
		}
		content, err := b.restoreContent(ctx, source, vaults)
		if err != nil {
			return "", nil, EvalSnapshotContentSummary{}, err
		}
		files = append(files, source.file)
		entries[source.file.Path] = evalComparableEntry{
			file:            source.file,
			storedSHA256:    source.file.SHA256,
			plaintextSHA256: evalBackupDigest(content.data),
		}
	}
	latestKeyChain := complete[len(complete)-1].manifest.KeyChain
	return absTarget, entries, SummarizeEvalSnapshotFiles(files, latestKeyChain), nil
}

// Compare compares two snapshot targets (or a snapshot target against the live
// runtime when rightDir is empty) by authenticated plaintext digest.
func (b *EvalBackup) Compare(ctx context.Context, leftDir, rightDir string) (*EvalSnapshotCompareReport, error) {
	if b.evidence == nil {
		return nil, fmt.Errorf("evaluation: compare: %w", constants.ErrEvaluationEvidenceVaultRequired)
	}
	held, err := b.sharedLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()

	vaults := make(map[string]EvidenceVault)
	leftLabel, leftEntries, leftSummary, err := b.loadComparableTarget(ctx, leftDir, vaults)
	if err != nil {
		return nil, err
	}
	rightLabel, rightEntries, rightSummary, err := b.loadComparableTarget(ctx, rightDir, vaults)
	if err != nil {
		return nil, err
	}

	report := &EvalSnapshotCompareReport{
		LeftTarget:        leftLabel,
		RightTarget:       rightLabel,
		FileCountDelta:    rightSummary.TotalFiles - leftSummary.TotalFiles,
		LogicalBytesDelta: rightSummary.TotalBytes - leftSummary.TotalBytes,
		Added:             []string{},
		Removed:           []string{},
		Modified:          []string{},
		Unchanged:         []string{},
		ResealedUnchanged: []string{},
		AddedCampaigns:    diffSortedSlices(leftSummary.CampaignIDs, rightSummary.CampaignIDs),
		RemovedCampaigns:  diffSortedSlices(rightSummary.CampaignIDs, leftSummary.CampaignIDs),
		AddedRuns:         diffSortedSlices(leftSummary.RunIDs, rightSummary.RunIDs),
		RemovedRuns:       diffSortedSlices(rightSummary.RunIDs, leftSummary.RunIDs),
		LeftContent:       leftSummary,
		RightContent:      rightSummary,
	}

	allPaths := make(map[string]struct{}, len(leftEntries)+len(rightEntries))
	for p := range leftEntries {
		allPaths[p] = struct{}{}
	}
	for p := range rightEntries {
		allPaths[p] = struct{}{}
	}
	sortedPaths := make([]string, 0, len(allPaths))
	for p := range allPaths {
		sortedPaths = append(sortedPaths, p)
	}
	sort.Strings(sortedPaths)

	for _, p := range sortedPaths {
		l, inLeft := leftEntries[p]
		r, inRight := rightEntries[p]
		switch {
		case !inLeft && inRight:
			report.Added = append(report.Added, p)
		case inLeft && !inRight:
			report.Removed = append(report.Removed, p)
		case l.plaintextSHA256 != r.plaintextSHA256:
			report.Modified = append(report.Modified, p)
		case l.storedSHA256 != r.storedSHA256:
			report.ResealedUnchanged = append(report.ResealedUnchanged, p)
		default:
			report.Unchanged = append(report.Unchanged, p)
		}
	}

	return report, nil
}

// diffSortedSlices returns elements present in after that are absent from before.
func diffSortedSlices(before, after []string) []string {
	beforeSet := make(map[string]struct{}, len(before))
	for _, s := range before {
		beforeSet[s] = struct{}{}
	}
	diff := []string{}
	for _, s := range after {
		if _, exists := beforeSet[s]; !exists {
			diff = append(diff, s)
		}
	}
	sort.Strings(diff)
	return diff
}

// Compact prunes superseded snapshots (those whose paths are 100% covered by
// newer complete snapshots), removes incomplete snapshot directories, rewrites
// kept manifests in compact JSON, and garbage-collects unreferenced objects
// from the shared object store.
func (b *EvalBackup) Compact(ctx context.Context, backupDir string) (*EvalBackupCompactReport, error) {
	destRoot, err := b.validateDestination(backupDir)
	if err != nil {
		return nil, err
	}
	held, err := b.exclusiveLock(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()

	complete, incomplete, err := loadEvalBackupSnapshots(destRoot)
	if err != nil {
		return nil, err
	}
	kept, superseded := classifySnapshotsNewestFirst(complete)

	// Verify that every object referenced by kept snapshots exists on disk with
	// the expected size before removing any snapshot or object.
	keptDigests := make(map[string]int64)
	for _, snap := range kept {
		for _, f := range snap.manifest.Files {
			keptDigests[f.SHA256] = f.Size
		}
		for _, kc := range snap.manifest.KeyChain {
			keptDigests[kc.SHA256] = kc.Size
		}
	}
	for sha, wantSize := range keptDigests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		objPath := evalBackupObjectPath(destRoot, sha)
		info, err := os.Stat(objPath)
		if err != nil || info.Size() != wantSize {
			return nil, fmt.Errorf("evaluation: compact: object %s: %w", sha, constants.ErrEvaluationBackupIntegrity)
		}
	}

	report := &EvalBackupCompactReport{
		BackupDir:                  destRoot,
		LatestSnapshotDir:          complete[len(complete)-1].dir,
		KeptSnapshots:              len(kept),
		RemovedSupersededSnapshots: len(superseded),
		RemovedIncompleteSnapshots: len(incomplete),
	}

	// Rewrite kept manifests in compact JSON if they carry indentation overhead.
	for _, snap := range kept {
		written, err := writeEvalBackupManifestAtomic(snap.dir, *snap.manifest)
		if err != nil {
			return nil, fmt.Errorf("evaluation: compact: %w", err)
		}
		if snap.manifestBytes > written {
			report.ReclaimedManifestBytes += snap.manifestBytes - written
		}
	}

	// Remove superseded complete snapshots.
	for _, snap := range superseded {
		if err := os.RemoveAll(snap.dir); err != nil {
			return nil, fmt.Errorf("evaluation: compact: remove superseded snapshot %s: %w", snap.dir, err)
		}
		report.ReclaimedManifestBytes += snap.manifestBytes
	}

	// Remove incomplete snapshot directories.
	for _, dir := range incomplete {
		if err := os.RemoveAll(dir); err != nil {
			return nil, fmt.Errorf("evaluation: compact: remove incomplete snapshot %s: %w", dir, err)
		}
	}

	// Garbage-collect unreferenced objects in objects/.
	objectsRoot := filepath.Join(destRoot, evalBackupObjectsDir)
	var orphanPaths []string
	var orphanSizes []int64
	if walkErr := filepath.WalkDir(objectsRoot, func(p string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".tmp-") {
			return nil
		}
		if _, referenced := keptDigests[entry.Name()]; !referenced {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			orphanPaths = append(orphanPaths, p)
			orphanSizes = append(orphanSizes, info.Size())
		}
		return nil
	}); walkErr != nil {
		return nil, fmt.Errorf("evaluation: compact: scan objects in %s: %w", objectsRoot, walkErr)
	}

	for i, p := range orphanPaths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("evaluation: compact: remove unreferenced object %s: %w", p, err)
		}
		report.RemovedObjects++
		report.ReclaimedObjectBytes += orphanSizes[i]
	}

	report.TotalReclaimedBytes = report.ReclaimedManifestBytes + report.ReclaimedObjectBytes
	return report, nil
}

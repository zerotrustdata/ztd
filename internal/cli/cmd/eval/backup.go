// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// defaultEvalBackupDir is where backups go unless --output-dir says otherwise:
// eval/backups under the project root, outside the .ztd/ runtime tree.
func defaultEvalBackupDir(cfg *config.Config) string {
	return shared.DefaultEvalBackupDir(cfg)
}

type evalBackupJSON struct {
	SnapshotDir string                      `json:"snapshot_dir"`
	CreatedAt   string                      `json:"created_at"`
	FileCount   int                         `json:"file_count"`
	TotalBytes  int64                       `json:"total_bytes"`
	Files       []evaluation.EvalBackupFile `json:"files"`
}

type evalRestoreJSON struct {
	SnapshotDir   string                      `json:"snapshot_dir"`
	SnapshotCount int                         `json:"snapshot_count"`
	Restored      []evaluation.EvalBackupFile `json:"restored"`
	Unchanged     []evaluation.EvalBackupFile `json:"unchanged"`
}

func backupEvalCmd(deps nativeEvalDeps) *cobra.Command {
	var (
		outputDir string
		status    bool
		compare   bool
		compact   bool
	)
	cmd := &cobra.Command{
		Use:   "backup [snapshot-or-backup-dir] [compare-target]",
		Short: "Create, inspect, compare, or compact evaluation evidence snapshots",
		Long: `Record all evaluation evidence (runs, campaigns, archives, the rollout queue,
and the frozen model inventory) as a new timestamped snapshot beneath
--output-dir (default: eval/backups under the project root). A snapshot is a
compact SHA-256 manifest; file content is stored once per digest in the shared
objects/ directory, so only files not already backed up are copied. Evidence is
copied encrypted, together with the vault key chain that sealed it, so the
snapshot can be restored after .ztd/ is wiped and its vault recreated. A
sealed file the runtime vault cannot open fails the backup before anything is
written; delete the runs or campaigns that hold it.

Snapshot inspection, comparison, and compaction modes:
  --status [target]:  Confirm snapshot contents (campaigns, runs, traces,
                      witness evidence, rollouts, inventories) and compare
                      logical vs deduplicated object vs manifest vs reclaimable
                      bloat sizes without creating a new snapshot.
  --compare [a] [b]:  Compare a snapshot or backup root against the live .ztd/
                      runtime (or compare two snapshots) by decrypted plaintext
                      digest, distinguishing real modifications from vault reseals.
  --compact [target]: Prune superseded snapshots whose paths are 100% covered
                      by newer complete snapshots, remove incomplete snapshot
                      directories, compact manifests, and delete unreferenced
                      objects from objects/.

The destination must be outside .ztd/ so it survives deleting the runtime tree.
Run and lease state is not copied. Run commands do not create backups;
invoke ztd eval backup explicitly. A backup cannot run while runs delete or
campaigns delete is purging the backups. Put a snapshot back with:
ztd eval restore`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			modeCount := 0
			for _, active := range []bool{status, compare, compact} {
				if active {
					modeCount++
				}
			}
			if modeCount > 1 {
				return fmt.Errorf("evaluation: backup: --status, --compare, and --compact are mutually exclusive: %w", constants.ErrEvaluationFlagsInvalid)
			}

			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			if outputDir == "" {
				outputDir = defaultEvalBackupDir(cfg)
			}
			backup := evaluation.NewEvalBackup(fileSvc, deps.now)

			switch {
			case status:
				if len(args) > 1 {
					return fmt.Errorf("evaluation: backup --status accepts at most 1 target argument: %w", constants.ErrEvaluationFlagsInvalid)
				}
				target := outputDir
				if len(args) == 1 {
					target = args[0]
				}
				report, err := backup.Inspect(cmd.Context(), target)
				if err != nil {
					return fmt.Errorf("evaluation: backup status: %w", err)
				}
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), report)
				}
				_, err = fmt.Fprintf(
					cmd.OutOrStdout(),
					"Snapshot status for %s:\n  Snapshots: %d complete (%d active, %d superseded, %d incomplete), %d key chain(s)\n  Latest:    %s\n  Contents:  %d file(s) (%d bytes) — %d campaign(s), %d run(s), %d assignment(s), %d trace(s), %d witness file(s), %d rollout(s), %d inventory file(s)\n  Storage:   %d referenced object(s) (%d bytes), %d superseded object(s) (%d bytes), %d unreferenced object(s) (%d bytes)\n  Manifests: %d bytes total (%d bytes active)\n  Reclaimable bloat: %d bytes\n",
					report.TargetDir,
					report.SnapshotCount, report.ActiveSnapshotCount, report.SupersededSnapshotCount, report.IncompleteSnapshotCount, report.DistinctKeyChains,
					report.LatestSnapshotDir,
					report.LogicalFileCount, report.LogicalBytes,
					report.Content.CampaignCount, report.Content.RunCount, report.Content.AssignmentCount, report.Content.AgentTraceCount,
					report.Content.WitnessEvidence.Files, report.Content.RolloutCount, report.Content.InventoryCount,
					report.ReferencedObjectCount, report.ReferencedObjectBytes,
					report.SupersededObjectCount, report.SupersededObjectBytes,
					report.UnreferencedObjectCount, report.UnreferencedObjectBytes,
					report.TotalManifestBytes, report.ActiveManifestBytes,
					report.ReclaimableBytes,
				)
				return err

			case compare:
				left := outputDir
				right := ""
				if len(args) >= 1 {
					left = args[0]
				}
				if len(args) == 2 {
					right = args[1]
				}
				report, err := backup.Compare(cmd.Context(), left, right)
				if err != nil {
					return fmt.Errorf("evaluation: backup compare: %w", err)
				}
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), report)
				}
				_, err = fmt.Fprintf(
					cmd.OutOrStdout(),
					"Snapshot comparison:\n  Left:  %s (%d files, %d bytes, %d campaigns, %d runs)\n  Right: %s (%d files, %d bytes, %d campaigns, %d runs)\n  Delta: %+d files, %+d bytes\n  Paths: %d added, %d removed, %d modified, %d unchanged (%d resealed with identical plaintext)\n  Campaigns: +%d / -%d, Runs: +%d / -%d\n",
					report.LeftTarget, report.LeftContent.TotalFiles, report.LeftContent.TotalBytes, report.LeftContent.CampaignCount, report.LeftContent.RunCount,
					report.RightTarget, report.RightContent.TotalFiles, report.RightContent.TotalBytes, report.RightContent.CampaignCount, report.RightContent.RunCount,
					report.FileCountDelta, report.LogicalBytesDelta,
					len(report.Added), len(report.Removed), len(report.Modified), len(report.Unchanged), len(report.ResealedUnchanged),
					len(report.AddedCampaigns), len(report.RemovedCampaigns), len(report.AddedRuns), len(report.RemovedRuns),
				)
				return err

			case compact:
				if len(args) > 1 {
					return fmt.Errorf("evaluation: backup --compact accepts at most 1 target argument: %w", constants.ErrEvaluationFlagsInvalid)
				}
				target := outputDir
				if len(args) == 1 {
					target = args[0]
				}
				report, err := backup.Compact(cmd.Context(), target)
				if err != nil {
					return fmt.Errorf("evaluation: backup compact: %w", err)
				}
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), report)
				}
				_, err = fmt.Fprintf(
					cmd.OutOrStdout(),
					"Compacted %s: kept %d snapshot(s), removed %d superseded and %d incomplete snapshot(s), removed %d unreferenced object(s), reclaimed %d bytes (%d manifest bytes, %d object bytes)\n",
					report.BackupDir,
					report.KeptSnapshots,
					report.RemovedSupersededSnapshots,
					report.RemovedIncompleteSnapshots,
					report.RemovedObjects,
					report.TotalReclaimedBytes,
					report.ReclaimedManifestBytes,
					report.ReclaimedObjectBytes,
				)
				return err

			default:
				if len(args) > 0 {
					return fmt.Errorf("evaluation: backup: unexpected arguments without --status, --compare, or --compact: %w", constants.ErrEvaluationFlagsInvalid)
				}
				report, err := backup.Create(cmd.Context(), outputDir)
				if err != nil {
					return fmt.Errorf("evaluation: backup: %w", err)
				}
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), evalBackupJSON{
						SnapshotDir: report.SnapshotDir,
						CreatedAt:   report.CreatedAt.Format(time.RFC3339),
						FileCount:   len(report.Files),
						TotalBytes:  report.TotalBytes,
						Files:       report.Files,
					})
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Backed up %d file(s), %d bytes, to %s\n", len(report.Files), report.TotalBytes, report.SnapshotDir)
				return err
			}
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory outside .ztd/ to write or inspect snapshots in (default: <project>/eval/backups)")
	cmd.Flags().BoolVar(&status, "status", false, "Inspect snapshot contents, storage deduplication, and reclaimable bloat without writing")
	cmd.Flags().BoolVar(&compare, "compare", false, "Compare a snapshot or backup directory against the live runtime (or another snapshot)")
	cmd.Flags().BoolVar(&compact, "compact", false, "Prune superseded/incomplete snapshots, compact manifests, and delete unreferenced objects")
	return cmd
}

func restoreEvalCmd(deps nativeEvalDeps) *cobra.Command {
	var (
		overwrite bool
		verify    bool
	)
	cmd := &cobra.Command{
		Use:   "restore [snapshot-dir]",
		Short: "Restore or verify evaluation evidence from backup snapshot(s)",
		Long: `Verify every file in snapshot(s) created by ztd eval backup against their
manifests, open each snapshot's vault from the key chain it carries with this
host's key store, then write the evidence back into .ztd/ sealed under the
current runtime vault. A snapshot whose vault the key store cannot open
restores nothing, and a sealed file its snapshot's vault cannot open fails the
restore before anything is written. Files already present with identical content are skipped.
If any existing file differs, nothing is written unless --overwrite is passed.

Pass --verify to confirm full cryptographic and structural restorability
(manifest schema, object SHA-256 digests, key-chain access through the host key
store, sealed evidence decryption, and live conflict detection) without writing
anything to .ztd/.

Without <snapshot-dir>, every complete snapshot in eval/backups under the
project root is merged and restored in one pass: each file comes from the
newest snapshot that holds it, so evidence a later snapshot no longer lists is
still recovered. Runs and campaigns removed with runs delete or campaigns
delete are purged from the snapshots and never come back. Pass a specific
<snapshot-dir> to restore or verify only that snapshot.

Restoring puts host evidence back; it does not rebuild the Gateway mirror. After
re-enrolling a fresh stack, run this command:
ztd public restore --queue`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			backup := evaluation.NewEvalBackup(fileSvc, deps.now)
			if verify {
				var report *evaluation.EvalSnapshotVerifyReport
				if len(args) == 1 {
					report, err = backup.Verify(cmd.Context(), args[0])
				} else {
					report, err = backup.VerifyAll(cmd.Context(), defaultEvalBackupDir(cfg))
				}
				if err != nil {
					return fmt.Errorf("evaluation: restore verify: %w", err)
				}
				if output.JSONEnabled(cmd) {
					return output.WriteJSON(cmd.OutOrStdout(), report)
				}
				_, err = fmt.Fprintf(
					cmd.OutOrStdout(),
					"Verified %d file(s) (%d bytes) across %d snapshot(s) in %s (%d key chain(s) opened): %d would restore, %d already identical, %d conflict(s) — %d campaign(s), %d run(s)\n",
					report.VerifiedFiles, report.VerifiedBytes, report.Snapshots, report.SnapshotDir, report.KeyChainsVerified,
					len(report.WouldRestore), len(report.Unchanged), len(report.Conflicts),
					report.Content.CampaignCount, report.Content.RunCount,
				)
				return err
			}

			var report *evaluation.EvalRestoreReport
			if len(args) == 1 {
				report, err = backup.Restore(cmd.Context(), args[0], overwrite)
			} else {
				report, err = backup.RestoreAll(cmd.Context(), defaultEvalBackupDir(cfg), overwrite)
			}
			if err != nil {
				return fmt.Errorf("evaluation: restore: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), evalRestoreJSON{
					SnapshotDir:   report.SnapshotDir,
					SnapshotCount: report.Snapshots,
					Restored:      nonNilBackupFiles(report.Restored),
					Unchanged:     nonNilBackupFiles(report.Unchanged),
				})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restored %d file(s) from %d snapshot(s) in %s (%d already identical)\n",
				len(report.Restored), report.Snapshots, report.SnapshotDir, len(report.Unchanged))
			return err
		},
	}
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "Replace existing files whose content differs from the backup")
	cmd.Flags().BoolVar(&verify, "verify", false, "Verify snapshot integrity, key-chain access, and sealed evidence decryption without writing to .ztd/")
	return cmd
}

func nonNilBackupFiles(files []evaluation.EvalBackupFile) []evaluation.EvalBackupFile {
	if files == nil {
		return []evaluation.EvalBackupFile{}
	}
	return files
}

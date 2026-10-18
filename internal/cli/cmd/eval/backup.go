// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

// defaultEvalBackupDir is where backups go unless --output-dir says otherwise:
// eval/backups under the project root, outside the .ztd/ runtime tree.
func defaultEvalBackupDir(cfg *config.Config) string {
	return shared.DefaultEvalBackupDir(cfg)
}

// autoBackupEval snapshots evaluation evidence into the default backup
// directory once a run has finished, whatever its outcome: a failed or
// cancelled run's evidence is worth keeping too. A backup failure never masks
// the run's own result, so it is reported as a warning. Text output goes to
// stdout only outside JSON mode, which must stay machine-parseable.
func autoBackupEval(cmd *cobra.Command, deps nativeEvalDeps, jsonOutput bool) {
	// The run may have been cancelled, so the backup must not inherit that.
	ctx := context.WithoutCancel(cmd.Context())
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err == nil {
		var report *evaluation.EvalBackupReport
		report, err = evaluation.NewEvalBackup(fileSvc, deps.now).CreateIfChanged(ctx, defaultEvalBackupDir(cfg))
		if err == nil {
			if jsonOutput {
				return
			}
			if report.Unchanged {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Evaluation evidence already backed up in %s\n", report.SnapshotDir)
				return
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Backed up evaluation evidence (%d file(s), %d bytes) to %s\n", len(report.Files), report.TotalBytes, report.SnapshotDir)
			return
		}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: evaluation: automatic backup failed: %v\n", err)
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
	var outputDir string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Copy evaluation evidence to a directory outside the runtime tree",
		Long: `Record all evaluation evidence (runs, campaigns, archives, the rollout queue,
and the frozen model inventory) as a new timestamped snapshot beneath
--output-dir (default: eval/backups under the project root). A snapshot is a
SHA-256 manifest; file content is stored once per digest in the shared objects/
directory, so only files not already backed up are copied. Evidence is
copied encrypted, together with the vault key chain that sealed it, so the
snapshot can be restored after .ztd/ is wiped and its vault recreated. A
sealed file the runtime vault cannot open fails the backup before anything is
written; delete the runs or campaigns that hold it.

The destination must be outside .ztd/ so it survives deleting the runtime tree. Run and lease state is not copied. Runs made by
ztd eval runs start or resume back up to the default directory
automatically when they finish. A backup cannot run while runs delete or
campaigns delete is purging the backups. Put a snapshot back with:
ztd eval restore`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
			if err != nil {
				return err
			}
			if outputDir == "" {
				outputDir = defaultEvalBackupDir(cfg)
			}
			report, err := evaluation.NewEvalBackup(fileSvc, deps.now).Create(cmd.Context(), outputDir)
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
		},
	}
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Directory outside .ztd/ to write the snapshot into (default: <project>/eval/backups)")
	return cmd
}

func restoreEvalCmd(deps nativeEvalDeps) *cobra.Command {
	var overwrite bool
	cmd := &cobra.Command{
		Use:   "restore [snapshot-dir]",
		Short: "Restore evaluation evidence from backup snapshot(s)",
		Long: `Verify every file in snapshot(s) created by ztd eval backup against their
manifests, open each snapshot's vault from the key chain it carries with this
host's key store, then write the evidence back into .ztd/ sealed under the
current runtime vault. A snapshot whose vault the key store cannot open
restores nothing, and a sealed file its snapshot's vault cannot open fails the
restore before anything is written. Files already present with identical content are skipped.
If any existing file differs, nothing is written unless --overwrite is passed.

Without <snapshot-dir>, every complete snapshot in eval/backups under the
project root is merged and restored in one pass: each file comes from the
newest snapshot that holds it, so evidence a later snapshot no longer lists is
still recovered. Runs and campaigns removed with runs delete or campaigns
delete are purged from the snapshots and never come back. Pass a specific
<snapshot-dir> to restore only that snapshot.

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
	return cmd
}

func nonNilBackupFiles(files []evaluation.EvalBackupFile) []evaluation.EvalBackupFile {
	if files == nil {
		return []evaluation.EvalBackupFile{}
	}
	return files
}

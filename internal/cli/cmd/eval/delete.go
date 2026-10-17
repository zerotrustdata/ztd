// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/gwremote"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

type deleteResultJSON struct {
	Kind            string   `json:"kind"`
	ID              string   `json:"id"`
	CampaignID      string   `json:"campaign_id,omitempty"`
	RunIDs          []string `json:"run_ids"`
	BackupsOnly     bool     `json:"backups_only"`
	BackupFiles     int      `json:"backup_files"`
	BackupSnapshots int      `json:"backup_snapshots"`
	BackupObjects   int      `json:"backup_objects"`
}

func newDeleteResultJSON(kind evaluation.ArchiveKind, id string, report *evaluation.DeleteReport) deleteResultJSON {
	return deleteResultJSON{
		Kind:            string(kind),
		ID:              id,
		CampaignID:      report.CampaignID,
		RunIDs:          report.RunIDs,
		BackupsOnly:     report.BackupsOnly,
		BackupFiles:     report.Backups.Files,
		BackupSnapshots: report.Backups.Snapshots,
		BackupObjects:   report.Backups.Objects,
	}
}

// writeBackupPurge reports what a delete removed from the backups.
func writeBackupPurge(cmd *cobra.Command, report *evaluation.DeleteReport) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "Removed %d file(s) from %d backup snapshot(s), freeing %d stored object(s)\n",
		report.Backups.Files, report.Backups.Snapshots, report.Backups.Objects)
	return err
}

// newDeleter builds the deleter for a command: it refuses to delete a run
// whose lease holder is alive, discards each run's publication through the
// Gateway, and purges the default backup directory.
func newDeleter(cmd *cobra.Command, deps nativeEvalDeps) (*evaluation.Deleter, error) {
	cfg, fileSvc, err := nativeEvalEnvironment(cmd, deps)
	if err != nil {
		return nil, err
	}
	control, err := deps.runControl.process(fileSvc)
	if err != nil {
		return nil, err
	}
	publication, err := gwremote.NewCampaignPublicationStateStore(fileSvc, cfg)
	if err != nil {
		return nil, err
	}
	return evaluation.NewDeleter(fileSvc, leaseLiveness(control), publication, defaultEvalBackupDir(cfg)), nil
}

func runsDeleteCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <run>",
		Short: "Delete one run and withdraw it from the public mirror",
		Long: `Delete one run, active or archived, and its evidence.

The Gateway first withdraws the run's dataset from anonymous public mirror
reads, prunes its proof artifacts, and clears its publication state; the
signed feed chain is not rewritten. The run is then removed from every
snapshot in eval/backups, so ztd eval restore never brings it back, and from
the runtime tree. Nothing is kept: use archive to keep a run. A run that is
only in the backups is removed from them. A running run cannot be deleted;
cancel it first, and a backup in progress must finish.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deleter, err := newDeleter(cmd, deps)
			if err != nil {
				return err
			}
			report, err := deleter.DeleteRun(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("evaluation: runs delete: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), newDeleteResultJSON(evaluation.ArchiveKindRun, args[0], report))
			}
			where := ""
			if report.BackupsOnly {
				where = " (it was only in the backups)"
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted run %s%s and withdrew it from the public mirror\n", args[0], where); err != nil {
				return err
			}
			return writeBackupPurge(cmd, report)
		},
	}
}

func campaignsDeleteCmd(deps nativeEvalDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <campaign>",
		Short: "Delete a campaign and all of its runs, and withdraw them from the public mirror",
		Long: `Delete a campaign, active or archived, every one of its runs, and its
inventory under .ztd/eval/inventories/.

Each run is withdrawn from the public mirror first and removed from every
snapshot in eval/backups, as by runs delete. The operation fails before
deleting anything if any run is running; cancel it first. A campaign that is
only in the backups loses its campaign tree and inventory there; its runs
cannot be listed without the runtime tree, so delete them with runs delete.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			deleter, err := newDeleter(cmd, deps)
			if err != nil {
				return err
			}
			report, err := deleter.DeleteCampaign(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("evaluation: campaigns delete: %w", err)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), newDeleteResultJSON(evaluation.ArchiveKindCampaign, args[0], report))
			}
			if report.BackupsOnly {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted campaign %s from the backups (it was not in the runtime tree); delete its runs with runs delete\n", args[0])
			} else {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Deleted campaign %s and %d run(s), and withdrew them from the public mirror\n", args[0], len(report.RunIDs))
			}
			if err != nil {
				return err
			}
			return writeBackupPurge(cmd, report)
		},
	}
}

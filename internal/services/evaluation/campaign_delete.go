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
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// Deleter removes campaigns and runs, active or archived, from the runtime
// tree and from every snapshot in the backup directory, so no restore brings
// them back. It discards each run's publication first
// (CampaignPublicationStateStore Delete withdraws the run from the public
// mirror and prunes its proof artifacts), so the mirror never serves a run
// whose evidence is gone. It holds the backup lock exclusively throughout, so
// no snapshot captures evidence while it is being deleted.
type Deleter struct {
	files       fs.RuntimeFileService
	live        LeaseLiveness
	publication CampaignPublicationStateStore
	backupDir   string
}

// DeleteReport describes a completed delete.
type DeleteReport struct {
	// CampaignID is the deleted run's campaign, or the deleted campaign. It is
	// empty for a run that was only in the backups.
	CampaignID string
	// RunIDs are the deleted runs.
	RunIDs []string
	// Backups is what was purged from the backup directory.
	Backups EvalBackupPurgeReport
	// BackupsOnly is set when the run or campaign was no longer in the runtime
	// tree and was deleted from the backups alone.
	BackupsOnly bool
}

// NewDeleter returns a Deleter. live reports whether a run lease is held by a
// running process; backupDir is the backup directory to purge.
func NewDeleter(files fs.RuntimeFileService, live LeaseLiveness, publication CampaignPublicationStateStore, backupDir string) *Deleter {
	return &Deleter{files: files, live: live, publication: publication, backupDir: backupDir}
}

// DeleteRun discards one run's publication and removes the run from the
// runtime tree and the backups. A running run must be cancelled first. A run
// that is only in the backups is deleted from them; a run in neither is not
// found.
func (d *Deleter) DeleteRun(ctx context.Context, runID string) (*DeleteReport, error) {
	if err := d.validate(runID); err != nil {
		return nil, fmt.Errorf("evaluation: delete run: %w", err)
	}
	held, err := NewEvalBackup(d.files, nil).exclusiveLock(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: delete run %s: %w", runID, err)
	}
	defer func() { _ = held.Close() }()
	purge, err := planEvalBackupPurge(d.backupDir, runEvidencePaths(runID))
	if err != nil {
		return nil, fmt.Errorf("evaluation: delete run %s: %w", runID, err)
	}
	store, archived, err := LocateRun(ctx, d.files, runID)
	if errors.Is(err, constants.ErrNotFound) && purge.report.Files > 0 {
		if err := d.discardAndPurge(ctx, []string{runID}, purge); err != nil {
			return nil, fmt.Errorf("evaluation: delete run %s: %w", runID, err)
		}
		return &DeleteReport{RunIDs: []string{runID}, Backups: purge.report, BackupsOnly: true}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := rejectRunningRun(ctx, store, d.live, runID); err != nil {
		return nil, err
	}
	run, err := store.LoadRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := d.discardAndPurge(ctx, []string{runID}, purge); err != nil {
		return nil, fmt.Errorf("evaluation: delete run %s: %w", runID, err)
	}
	if err := d.removeRun(ctx, runID, archived); err != nil {
		return nil, err
	}
	return &DeleteReport{CampaignID: run.GetCampaignBinding().GetCampaignId(), RunIDs: []string{runID}, Backups: purge.report}, nil
}

// DeleteCampaign deletes every run of a campaign, active or archived, then the
// campaign and its inventory, from the runtime tree and the backups. It fails
// before deleting anything if any run is running.
//
// A campaign that is only in the backups loses its campaign tree and
// inventory there. Its runs cannot be listed without the runtime tree, so
// they are deleted by run ID.
func (d *Deleter) DeleteCampaign(ctx context.Context, campaignID string) (*DeleteReport, error) {
	if err := d.validate(campaignID); err != nil {
		return nil, fmt.Errorf("evaluation: delete campaign: %w", err)
	}
	held, err := NewEvalBackup(d.files, nil).exclusiveLock(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, err)
	}
	defer func() { _ = held.Close() }()
	_, campaignArchived, err := LocateCampaign(ctx, d.files, campaignID)
	if errors.Is(err, constants.ErrNotFound) {
		purge, planErr := planEvalBackupPurge(d.backupDir, campaignEvidencePaths(campaignID))
		if planErr != nil {
			return nil, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, planErr)
		}
		if purge.report.Files == 0 {
			return nil, err
		}
		if err := purge.apply(); err != nil {
			return nil, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, err)
		}
		return &DeleteReport{CampaignID: campaignID, RunIDs: []string{}, Backups: purge.report, BackupsOnly: true}, nil
	}
	if err != nil {
		return nil, err
	}
	type located struct {
		runID    string
		store    *Store
		archived bool
	}
	var runs []located
	for _, candidate := range []struct {
		store    *Store
		archived bool
	}{
		{NewStore(d.files), false},
		{NewArchivedRunStore(d.files), true},
	} {
		runIDs, err := candidate.store.ListCampaignRunIDs(ctx, campaignID)
		if err != nil {
			return nil, err
		}
		for _, runID := range runIDs {
			runs = append(runs, located{runID: runID, store: candidate.store, archived: candidate.archived})
		}
	}
	runIDs := make([]string, 0, len(runs))
	paths := campaignEvidencePaths(campaignID)
	for _, run := range runs {
		if err := rejectRunningRun(ctx, run.store, d.live, run.runID); err != nil {
			return nil, err
		}
		runIDs = append(runIDs, run.runID)
		paths = append(paths, runEvidencePaths(run.runID)...)
	}
	purge, err := planEvalBackupPurge(d.backupDir, paths)
	if err != nil {
		return nil, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, err)
	}
	if err := d.discardAndPurge(ctx, runIDs, purge); err != nil {
		return nil, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, err)
	}
	report := &DeleteReport{CampaignID: campaignID, RunIDs: make([]string, 0, len(runs)), Backups: purge.report}
	for _, run := range runs {
		if err := d.removeRun(ctx, run.runID, run.archived); err != nil {
			return report, err
		}
		report.RunIDs = append(report.RunIDs, run.runID)
	}
	campaignDir := activeEvalLayout().campaignDir(campaignID)
	if campaignArchived {
		campaignDir = archivedCampaignDir(campaignID)
		if err := d.files.Remove(ctx, archiveManifestPath(campaignDir)); err != nil {
			return report, fmt.Errorf("evaluation: delete campaign %s: remove archive manifest: %w", campaignID, err)
		}
	}
	if err := d.files.RemoveAll(ctx, campaignDir); err != nil {
		return report, fmt.Errorf("evaluation: delete campaign %s: %w", campaignID, err)
	}
	if err := d.files.Remove(ctx, campaignInventoryPath(DefaultCampaignInventoryRelDirname, campaignID)); err != nil {
		return report, fmt.Errorf("evaluation: delete campaign %s: remove inventory: %w", campaignID, err)
	}
	return report, nil
}

func (d *Deleter) validate(id string) error {
	if d == nil || d.files == nil || d.publication == nil || d.backupDir == "" {
		return constants.ErrMissingRequiredField
	}
	if !complianceevidence.ValidPathElement(id) {
		return fmt.Errorf("%w: %q", constants.ErrEvidenceArtifactMalformed, id)
	}
	return nil
}

// discardAndPurge discards each run's publication, then purges the backups.
// A failed discard leaves the backups and the runtime tree untouched.
func (d *Deleter) discardAndPurge(ctx context.Context, runIDs []string, purge *evalBackupPurge) error {
	for _, runID := range runIDs {
		if err := d.publication.Delete(ctx, runID); err != nil {
			return fmt.Errorf("discard publication of %s: %w", runID, err)
		}
	}
	return purge.apply()
}

// removeRun removes one run's directory and, for an archived run, its archive
// manifest.
func (d *Deleter) removeRun(ctx context.Context, runID string, archived bool) error {
	runDir := activeEvalLayout().runDir(runID)
	if archived {
		runDir = archivedRunDir(runID)
		if err := d.files.Remove(ctx, archiveManifestPath(runDir)); err != nil {
			return fmt.Errorf("evaluation: delete run %s: remove archive manifest: %w", runID, err)
		}
	}
	if err := d.files.RemoveAll(ctx, runDir); err != nil {
		return fmt.Errorf("evaluation: delete run %s: %w", runID, err)
	}
	return nil
}

// runEvidencePaths are the runtime-relative paths a run's evidence occupies,
// active or archived.
func runEvidencePaths(runID string) []string {
	archived := archivedRunDir(runID)
	return []string{activeEvalLayout().runDir(runID), archived, archiveManifestPath(archived)}
}

// campaignEvidencePaths are the runtime-relative paths a campaign's own
// evidence occupies, active or archived, and its inventory.
func campaignEvidencePaths(campaignID string) []string {
	archived := archivedCampaignDir(campaignID)
	return []string{
		activeEvalLayout().campaignDir(campaignID),
		archived,
		archiveManifestPath(archived),
		campaignInventoryPath(DefaultCampaignInventoryRelDirname, campaignID),
	}
}

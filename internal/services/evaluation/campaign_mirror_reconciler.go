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
	"io/fs"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// CampaignMirrorProbe reports whether one explorer dataset is present in the
// gateway-owned public mirror feed.
type CampaignMirrorProbe interface {
	DatasetPresent(ctx context.Context, datasetID string) (bool, error)
}

// CampaignMirrorReconcileResult summarizes one store or run reconcile pass.
type CampaignMirrorReconcileResult struct {
	RestoredRunIDs    []string          `json:"restored_run_ids"`
	RepublishedRunIDs []string          `json:"republished_run_ids,omitempty"`
	SkippedRunIDs     []string          `json:"skipped_run_ids"`
	MissingRunIDs     []string          `json:"missing_run_ids,omitempty"`
	IneligibleRunIDs  []string          `json:"ineligible_run_ids,omitempty"`
	HostAbsentRunIDs  []string          `json:"host_absent_run_ids"`
	FailedRuns        map[string]string `json:"failed_runs,omitempty"`
	PublishedRecords  int               `json:"published_records"`
}

type CampaignMirrorReconcileStatus string

const (
	CampaignMirrorReconcileIneligible  CampaignMirrorReconcileStatus = "ineligible"
	CampaignMirrorReconcileChecking    CampaignMirrorReconcileStatus = "checking"
	CampaignMirrorReconcileRestored    CampaignMirrorReconcileStatus = "restored"
	CampaignMirrorReconcileRepublished CampaignMirrorReconcileStatus = "republished"
	CampaignMirrorReconcilePresent     CampaignMirrorReconcileStatus = "already_present"
	CampaignMirrorReconcileMissing     CampaignMirrorReconcileStatus = "missing"
	CampaignMirrorReconcileHostAbsent  CampaignMirrorReconcileStatus = "host_absent"
	CampaignMirrorReconcileFailed      CampaignMirrorReconcileStatus = "failed"
)

type CampaignMirrorReconcileProgress struct {
	Index            int
	Total            int
	RunID            string
	Status           CampaignMirrorReconcileStatus
	PublishedRecords int
	Err              error
}

type CampaignMirrorReconcileProgressFunc func(CampaignMirrorReconcileProgress)

// CampaignMirrorReconciler compares verified runs against the public
// mirror and republishes canonical host artifacts when datasets are missing.
type CampaignMirrorReconciler struct {
	publication *CampaignPublicationCoordinator
	store       CampaignStore
	probe       CampaignMirrorProbe
}

// NewCampaignMirrorReconciler wires one publication coordinator and optional
// mirror probe for drift detection.
func NewCampaignMirrorReconciler(publication *CampaignPublicationCoordinator, store CampaignStore, probe CampaignMirrorProbe) *CampaignMirrorReconciler {
	return &CampaignMirrorReconciler{
		publication: publication,
		store:       store,
		probe:       probe,
	}
}

// ReconcileAllVerifiedRunsFromStore walks the run store and reconciles all runs
// that have valid verification reports. When restoreMissing is true, it
// republishes canonical host artifacts for datasets that are absent from the
// mirror. Run discovery uses the persisted store, including on restored backups.
func (r *CampaignMirrorReconciler) ReconcileAllVerifiedRunsFromStore(ctx context.Context, runTimeout time.Duration, restoreMissing bool, force bool, progress CampaignMirrorReconcileProgressFunc) (*CampaignMirrorReconcileResult, error) {
	if r == nil || r.publication == nil || r.store == nil || runTimeout <= 0 {
		return nil, fmt.Errorf("evaluation: reconcile verified runs from store: dependencies: %w", constants.ErrMissingRequiredField)
	}
	runIDs, err := r.store.ListRunIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: reconcile verified runs from store: list runs: %w", err)
	}
	return r.reconcileRuns(ctx, runIDs, runTimeout, restoreMissing, force, progress)
}

type runCheckResult struct {
	err        error
	absent     bool
	ineligible bool
	present    bool
}

func (r *CampaignMirrorReconciler) reconcileRuns(ctx context.Context, runIDs []string, runTimeout time.Duration, restoreMissing, force bool, progress CampaignMirrorReconcileProgressFunc) (*CampaignMirrorReconcileResult, error) {
	result := &CampaignMirrorReconcileResult{FailedRuns: map[string]string{}}
	if len(runIDs) == 0 {
		return result, nil
	}

	checks := make([]runCheckResult, len(runIDs))
	workerCount := 4
	if len(runIDs) < workerCount {
		workerCount = len(runIDs)
	}
	jobs := make(chan int, len(runIDs))
	var wg sync.WaitGroup
	checkCtx, checkCancel := context.WithCancel(ctx)
	defer checkCancel()

	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				select {
				case <-checkCtx.Done():
					return
				default:
				}
				runID := runIDs[idx]
				effectiveTimeout := runTimeout
				if restoreMissing {
					effectiveTimeout = CampaignMirrorReconcileRunTimeout(runTimeout, assignmentCountForMirrorTimeout(checkCtx, r.store, runID))
				}
				runCtx, cancel := context.WithTimeout(checkCtx, effectiveTimeout)
				exists, err := r.store.RunExists(runCtx, runID)
				if err != nil {
					cancel()
					checks[idx] = runCheckResult{err: err}
					continue
				}
				if !exists {
					cancel()
					checks[idx] = runCheckResult{absent: true}
					continue
				}
				eligible, err := r.completedAndVerified(runCtx, runID)
				if err != nil {
					cancel()
					checks[idx] = runCheckResult{err: err}
					continue
				}
				if !eligible {
					cancel()
					checks[idx] = runCheckResult{ineligible: true}
					continue
				}
				if r.probe != nil && !force {
					present, err := r.probe.DatasetPresent(runCtx, CampaignDatasetID(runID))
					if err != nil {
						cancel()
						checks[idx] = runCheckResult{err: err}
						continue
					}
					if present {
						cancel()
						checks[idx] = runCheckResult{present: true}
						continue
					}
				}
				cancel()
			}
		}()
	}

	for i := range runIDs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	for index, runID := range runIDs {
		update := func(status CampaignMirrorReconcileStatus, published int, err error) {
			if progress != nil {
				progress(CampaignMirrorReconcileProgress{Index: index + 1, Total: len(runIDs), RunID: runID, Status: status, PublishedRecords: published, Err: err})
			}
		}
		update(CampaignMirrorReconcileChecking, 0, nil)
		check := checks[index]
		if check.err != nil {
			result.FailedRuns[runID] = check.err.Error()
			update(CampaignMirrorReconcileFailed, 0, check.err)
			continue
		}
		if check.absent {
			result.HostAbsentRunIDs = append(result.HostAbsentRunIDs, runID)
			update(CampaignMirrorReconcileHostAbsent, 0, nil)
			continue
		}
		if check.ineligible {
			result.IneligibleRunIDs = append(result.IneligibleRunIDs, runID)
			update(CampaignMirrorReconcileIneligible, 0, nil)
			continue
		}
		if check.present {
			result.SkippedRunIDs = append(result.SkippedRunIDs, runID)
			update(CampaignMirrorReconcilePresent, 0, nil)
			continue
		}
		if !restoreMissing {
			result.MissingRunIDs = append(result.MissingRunIDs, runID)
			update(CampaignMirrorReconcileMissing, 0, nil)
			continue
		}
		effectiveTimeout := runTimeout
		effectiveTimeout = CampaignMirrorReconcileRunTimeout(runTimeout, assignmentCountForMirrorTimeout(ctx, r.store, runID))
		runCtx, cancel := context.WithTimeout(ctx, effectiveTimeout)
		published, err := r.restoreRun(runCtx, runID, force)
		cancel()
		if err != nil {
			result.FailedRuns[runID] = err.Error()
			update(CampaignMirrorReconcileFailed, published, err)
			if logging.IsTransportDown(err) {
				for _, remainingRunID := range runIDs[index+1:] {
					result.FailedRuns[remainingRunID] = "skipped after gateway publication failure"
				}
				break
			}
			continue
		}
		result.RestoredRunIDs = append(result.RestoredRunIDs, runID)
		update(CampaignMirrorReconcileRestored, published, nil)
		result.PublishedRecords += published
	}
	return result, nil
}

// ReconcileRun restores one run when its dataset is missing from the mirror.
// When force is true, it republishes even when the dataset is already present.
func (r *CampaignMirrorReconciler) ReconcileRun(ctx context.Context, runID string, force bool) (int, error) {
	if r == nil || r.publication == nil || r.store == nil || runID == "" {
		return 0, fmt.Errorf("evaluation: reconcile run: dependencies: %w", constants.ErrMissingRequiredField)
	}
	eligible, err := r.completedAndVerified(ctx, runID)
	if err != nil {
		return 0, err
	}
	if !eligible {
		return 0, fmt.Errorf("evaluation: run %q is not completed and verified: %w", runID, constants.ErrNotFound)
	}
	if r.probe != nil && !force {
		present, err := r.probe.DatasetPresent(ctx, CampaignDatasetID(runID))
		if err != nil {
			return 0, err
		}
		if present {
			return 0, nil
		}
	}
	return r.restoreRun(ctx, runID, force)
}

func (r *CampaignMirrorReconciler) completedAndVerified(ctx context.Context, runID string) (bool, error) {
	summary, err := NewCampaignController(r.store, nil, nil, nil).RunSummary(ctx, runID)
	if err != nil {
		return false, err
	}
	if CampaignRunStatus(summary, nil, false) != "completed" {
		return false, nil
	}
	report, err := r.store.LoadCampaignVerification(ctx, runID)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, constants.ErrNotFound) {
		return false, nil
	}
	if err != nil || report == nil || report.GetStatus() != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
		return false, err
	}
	return true, nil
}

func (r *CampaignMirrorReconciler) restoreRun(ctx context.Context, runID string, force bool) (int, error) {
	report, err := r.store.LoadCampaignVerification(ctx, runID)
	if err != nil {
		return 0, fmt.Errorf("evaluation: restore run verification: %w", err)
	}
	if report == nil || report.GetStatus() != evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
		return 0, fmt.Errorf("evaluation: restore run requires passing verification: %w", constants.ErrEvidenceScopeMismatch)
	}
	if force {
		if err := r.publication.ResetFeedPublicationIdempotency(ctx, runID); err != nil {
			return 0, err
		}
	}
	r.publication.NoteDatasetAbsent(runID)
	defer r.publication.NoteDatasetAbsent("")

	if legacyCampaignVerificationReport(report) {
		return r.publication.PublishRunCatchUp(ctx, runID)
	}
	return r.publication.PublishRunCatchUpWithVerification(ctx, runID, report)
}

func legacyCampaignVerificationReport(report *evalv1.EvaluationVerificationReport) bool {
	return report != nil && report.GetStatus() == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS && report.GetVerifiedPopulationDigest() == ""
}

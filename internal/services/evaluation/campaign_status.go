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
	stdfs "io/fs"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// RunFeedVerificationDrift compares each active run's stored verification report
// with the verifier state the feed publishes for it, in run ID order. A run the
// feed has never carried, a run with no report, and a partial report on an
// unfinished run have nothing to compare.
func RunFeedVerificationDrift(ctx context.Context, store *Store, published map[string]string) ([]models.PublicFeedVerificationDrift, error) {
	runIDs, err := store.ListRunIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("evaluation: feed verification drift: %w", err)
	}
	var drift []models.PublicFeedVerificationDrift
	for _, runID := range runIDs {
		feedState, carried := published[runID]
		if !carried {
			continue
		}
		report, err := store.LoadCampaignVerification(ctx, runID)
		if errors.Is(err, stdfs.ErrNotExist) || errors.Is(err, constants.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("evaluation: feed verification drift: %w", err)
		}
		if entry, drifted := feedVerificationDriftEntry(runID, report, feedState); drifted {
			drift = append(drift, entry)
		}
	}
	return drift, nil
}

func feedVerificationDriftEntry(runID string, report *evalv1.EvaluationVerificationReport, feedState string) (models.PublicFeedVerificationDrift, bool) {
	if report.GetVerifiedAssignmentCount() != report.GetExpectedAssignmentCount() {
		return models.PublicFeedVerificationDrift{}, false
	}
	var localStatus string
	switch report.GetStatus() {
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS:
		localStatus = "verified"
	case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL:
		localStatus = "verify_failed"
	default:
		return models.PublicFeedVerificationDrift{}, false
	}
	if feedState == VerifierStateFromVerificationReport(report) {
		return models.PublicFeedVerificationDrift{}, false
	}
	return models.PublicFeedVerificationDrift{RunID: runID, LocalStatus: localStatus, FeedVerifierState: feedState}, true
}

// CampaignRunStatus derives a human-readable campaign run status from canonical
// assignment counts, an optional persisted verification report, and whether a
// live process holds the run's lease.
//
// A run is "running" only while its lease holder is alive. An assignment left
// running with no live holder, as after a crash or a restored backup, reports
// "interrupted".
func CampaignRunStatus(summary *CampaignRunSummary, verification *evalv1.EvaluationVerificationReport, executing bool) string {
	if summary == nil {
		return "unknown"
	}
	if verification != nil {
		switch verification.GetStatus() {
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS:
			return "verified"
		case evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL:
			return "verify_failed"
		default:
			return strings.ToLower(strings.TrimPrefix(verification.GetStatus().String(), "EVALUATION_VERDICT_STATUS_"))
		}
	}
	scheduled := summary.QueuedCount + summary.RunningCount + summary.TerminalCount
	if scheduled == 0 {
		return "initialized"
	}
	if executing {
		return "running"
	}
	if summary.StoppedCount > 0 && summary.QueuedCount == 0 && summary.RunningCount == 0 {
		return "cancelled"
	}
	if summary.RunningCount > 0 {
		return "interrupted"
	}
	if summary.ExpectedAssignment > 0 && uint64(summary.TerminalCount) >= summary.ExpectedAssignment {
		return "completed"
	}
	if summary.TerminalCount == 0 && summary.QueuedCount > 0 {
		return "scheduled"
	}
	return "in_progress"
}

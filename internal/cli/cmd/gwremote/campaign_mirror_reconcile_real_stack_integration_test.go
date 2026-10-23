// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// newMirrorRunReconciler wires the production reconciler over one run's publication,
// with a fresh mirror probe so every presence check reads the mirror as it stands.
func newMirrorRunReconciler(t *testing.T, e *evalRunStack) *evaluation.CampaignMirrorReconciler {
	t.Helper()
	probe := NewHTTPCampaignMirrorProbe(t.Context())
	return evaluation.NewCampaignMirrorReconciler(newEvalRunPublication(t, e, probe), e.store, probe)
}

// mirrorHasDataset reports whether a fresh probe finds one run's dataset in the
// mirror that the gwremote readers currently point at.
func mirrorHasDataset(t *testing.T, runID string) bool {
	t.Helper()
	present, err := NewHTTPCampaignMirrorProbe(t.Context()).DatasetPresent(t.Context(), evaluation.CampaignDatasetID(runID))
	require.NoError(t, err)
	return present
}

// TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStoreReportsProgressAndBoundsEachRun
// runs the reconcile against a mirror that accepts connections and never answers.
// The per-run deadline must end the check, report the run failed, and surface the
// deadline in the progress stream.
func TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStoreReportsProgressAndBoundsEachRun(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID

	hung := hungHTTPListener(t)
	setPublicMirrorOrigin(t, hung)
	probe := NewHTTPCampaignMirrorProbe(t.Context())
	reconciler := evaluation.NewCampaignMirrorReconciler(newEvalRunPublication(t, e, probe), e.store, probe)
	var progress []evaluation.CampaignMirrorReconcileProgress

	result, err := reconciler.ReconcileAllVerifiedRunsFromStore(t.Context(), 50*time.Millisecond, true, false, func(update evaluation.CampaignMirrorReconcileProgress) {
		progress = append(progress, update)
	})

	require.NoError(t, err)
	assert.Contains(t, result.FailedRuns[runID], context.DeadlineExceeded.Error())
	require.Len(t, progress, 2)
	assert.Equal(t, evaluation.CampaignMirrorReconcileChecking, progress[0].Status)
	assert.Equal(t, evaluation.CampaignMirrorReconcileFailed, progress[1].Status)
	assert.ErrorIs(t, progress[1].Err, context.DeadlineExceeded)
}

// TestCampaignMirrorReconcilerRealStack_RestoresMissingVerifiedRuns reconciles a
// verified run against a fresh mirror that does not hold its dataset. The run must
// be restored, and the mirror must hold the dataset afterwards.
//
// The stub test's second leg (a legacy report restored after the dataset went
// missing again) is not mirrored: the mirror cannot lose a dataset it already
// serves, and a second restore against the same feed is idempotent by design.
func TestCampaignMirrorReconcilerRealStack_RestoresMissingVerifiedRuns(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)
	require.False(t, mirrorHasDataset(t, runID), "a fresh mirror must not hold the dataset")

	result, err := newMirrorRunReconciler(t, e).ReconcileAllVerifiedRunsFromStore(ctx, time.Minute, true, false, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{runID}, result.RestoredRunIDs)
	assert.Positive(t, result.PublishedRecords)
	assert.True(t, mirrorHasDataset(t, runID), "the restored dataset must be served by the mirror")
}

// TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStoreForceSkipsPresentDataset
// publishes a verified run, then reconciles with force. The dataset is present, so the
// run is skipped: nothing is restored or republished, and the mirror does not advance.
func TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStoreForceSkipsPresentDataset(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	report := e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)

	_, err := newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUpWithVerification(ctx, runID, report)
	require.NoError(t, err)
	require.True(t, mirrorHasDataset(t, runID))
	highWater := mirrorHighWaterSequence(t, e.stack.Gateway)

	result, err := newMirrorRunReconciler(t, e).ReconcileAllVerifiedRunsFromStore(ctx, time.Minute, true, true, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{runID}, result.SkippedRunIDs)
	assert.Empty(t, result.RestoredRunIDs)
	assert.Empty(t, result.RepublishedRunIDs)
	assert.Equal(t, highWater, mirrorHighWaterSequence(t, e.stack.Gateway), "a skipped run must publish nothing")
}

// TestCampaignMirrorReconcilerRealStack_ReconcileRunRestoresMissingDataset restores one
// verified run whose dataset is absent from a fresh mirror.
func TestCampaignMirrorReconcilerRealStack_ReconcileRunRestoresMissingDataset(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)

	published, err := newMirrorRunReconciler(t, e).ReconcileRun(ctx, runID, false)
	require.NoError(t, err)
	assert.Positive(t, published)
	assert.True(t, mirrorHasDataset(t, runID))
}

// TestCampaignMirrorReconcilerRealStack_ReconcileRunSkipsPresentDataset reconciles a run
// whose dataset the mirror already serves. Nothing is published.
func TestCampaignMirrorReconcilerRealStack_ReconcileRunSkipsPresentDataset(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	report := e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)

	_, err := newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUpWithVerification(ctx, runID, report)
	require.NoError(t, err)
	highWater := mirrorHighWaterSequence(t, e.stack.Gateway)

	published, err := newMirrorRunReconciler(t, e).ReconcileRun(ctx, runID, false)
	require.NoError(t, err)
	assert.Zero(t, published)
	assert.Equal(t, highWater, mirrorHighWaterSequence(t, e.stack.Gateway))
}

// TestCampaignMirrorReconcilerRealStack_ReconcileRunForceRepublishesPresentDataset
// republishes a run whose dataset is already present when force is set. The mirror
// must advance.
func TestCampaignMirrorReconcilerRealStack_ReconcileRunForceRepublishesPresentDataset(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)

	_, err := newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUp(ctx, runID)
	require.NoError(t, err)
	highWater := mirrorHighWaterSequence(t, e.stack.Gateway)
	require.Positive(t, highWater)
	require.True(t, mirrorHasDataset(t, runID))

	published, err := newMirrorRunReconciler(t, e).ReconcileRun(ctx, runID, true)
	require.NoError(t, err)
	assert.Positive(t, published)
	assert.Greater(t, mirrorHighWaterSequence(t, e.stack.Gateway), highWater, "force must republish into the mirror")
}

// TestCampaignPublicationCoordinatorRealStack_ResetsIdempotencyWhenMirrorDatasetMissing
// publishes a run, then checks idempotency against the mirror. While the mirror
// serves the dataset, publishing again changes nothing. When the mirror does not
// serve it, the publication resets its idempotency and republishes.
//
// The second Gateway's mirror stands in for a mirror that lost the dataset. It is
// a real Gateway that never received the run, and the coordinator's exporter still
// publishes to the run's Gateway.
func TestCampaignPublicationCoordinatorRealStack_ResetsIdempotencyWhenMirrorDatasetMissing(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	report := e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)
	emptyMirror := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:              "mirror-without-dataset",
		Posture:               constants.PostureDoctrine,
		NoDownstream:          true,
		EnablePublicSpectator: true,
	})
	emptyMirror.WaitForReady(t)

	_, err := newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUpWithVerification(ctx, runID, report)
	require.NoError(t, err)
	published := mirrorHighWaterSequence(t, e.stack.Gateway)
	require.Positive(t, published)

	_, err = newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUp(ctx, runID)
	require.NoError(t, err)
	assert.Equal(t, published, mirrorHighWaterSequence(t, e.stack.Gateway), "a present dataset must not republish")

	pointMirrorAt(t, emptyMirror)
	_, err = newEvalRunPublication(t, e, NewHTTPCampaignMirrorProbe(ctx)).PublishRunCatchUp(ctx, runID)
	require.NoError(t, err)
	assert.Greater(t, mirrorHighWaterSequence(t, e.stack.Gateway), published, "a missing dataset must republish")
}

// TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStorePresenceOnlyReportsMissing
// reconciles without restore. A verified run whose dataset is absent is reported
// missing, and nothing is published.
func TestCampaignMirrorReconcilerRealStack_ReconcileAllVerifiedRunsFromStorePresenceOnlyReportsMissing(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID
	pointMirrorAt(t, e.stack.Gateway)

	result, err := newMirrorRunReconciler(t, e).ReconcileAllVerifiedRunsFromStore(ctx, time.Minute, false, false, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{runID}, result.MissingRunIDs)
	assert.Empty(t, result.RestoredRunIDs)
	assert.False(t, mirrorHasDataset(t, runID), "presence-only reconcile must not publish")
}

// TestCampaignMirrorReconcilerRealStack_EligibilityUsesCanonicalCampaignState restores
// only runs the canonical campaign state makes eligible: completed and carrying a
// passing verification. Every other state is ineligible, is never published, and
// stays out of the mirror even with force set.
func TestCampaignMirrorReconcilerRealStack_EligibilityUsesCanonicalCampaignState(t *testing.T) {
	for _, state := range []string{"verified", "unfinished", "unverified", "verification_failed"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
			e.initialize(t)
			runID := e.req.RunID
			switch state {
			case "verified":
				e.executeOnce(t)
				e.verifyAndSave(t)
			case "unverified":
				e.executeOnce(t)
			case "verification_failed":
				e.executeOnce(t)
				report, err := evaluation.NewCampaignRunVerifier(e.clock).VerifyRun(ctx, e.store, runID, e.req.Catalog, e.req.ScenarioArtifacts)
				require.NoError(t, err)
				report.Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
				require.NoError(t, e.store.SaveCampaignVerification(ctx, runID, report))
			case "unfinished":
				// The assignment stays queued, so the run is not completed even though
				// a passing report exists for it.
				require.NoError(t, e.store.SaveCampaignVerification(ctx, runID, &evalv1.EvaluationVerificationReport{
					SchemaVersion: evaluation.CampaignSchemaVersion,
					ReportId:      runID,
					RunId:         runID,
					Status:        evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
					VerifiedAt:    timestamppb.New(time.Unix(1_700_000_300, 0).UTC()),
				}))
			}
			pointMirrorAt(t, e.stack.Gateway)
			reconciler := newMirrorRunReconciler(t, e)
			var updates []evaluation.CampaignMirrorReconcileProgress
			result, err := reconciler.ReconcileAllVerifiedRunsFromStore(ctx, time.Minute, true, true, func(update evaluation.CampaignMirrorReconcileProgress) {
				updates = append(updates, update)
			})
			require.NoError(t, err)
			require.Len(t, updates, 2)

			if state == "verified" {
				assert.Equal(t, []string{runID}, result.RestoredRunIDs)
				assert.Positive(t, result.PublishedRecords)
				assert.True(t, mirrorHasDataset(t, runID))
				return
			}
			assert.Empty(t, result.RestoredRunIDs, "force must preserve eligibility requirements")
			assert.Equal(t, []string{runID}, result.IneligibleRunIDs)
			assert.Empty(t, result.FailedRuns)
			_, err = reconciler.ReconcileRun(ctx, runID, true)
			require.Error(t, err)
			assert.False(t, mirrorHasDataset(t, runID), "an ineligible run must not reach the mirror")
		})
	}
}

// corruptCampaignVerificationReport overwrites one run's persisted verification report
// with bytes that do not decode. The write goes through the evidence file service the
// run store seals into, at the run's campaign-verification path.
func corruptCampaignVerificationReport(t *testing.T, e *evalRunStack, runID string) {
	t.Helper()
	path := filepath.Join(constants.DataDirname, constants.EvaluationDirname, constants.EvaluationRunsDirname, runID, constants.CampaignVerificationFilename)
	require.NoError(t, e.files.WriteFile(t.Context(), path, []byte("invalid"), constants.PermFilePrivate))
}

// TestCampaignMirrorReconcilerRealStack_ForceRejectsCorruptVerificationReport publishes
// nothing for a verified run whose persisted verification report cannot be decoded.
// Even with force, the run is reported failed with the malformed-artifact error, the
// mirror does not advance, and a single-run reconcile rejects it the same way.
func TestCampaignMirrorReconcilerRealStack_ForceRejectsCorruptVerificationReport(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, publicMirror: true, replies: evalRunShellReplies()})
	e.initialize(t)
	e.executeOnce(t)
	e.verifyAndSave(t)
	runID := e.req.RunID
	corruptCampaignVerificationReport(t, e, runID)
	pointMirrorAt(t, e.stack.Gateway)
	require.False(t, mirrorHasDataset(t, runID), "a fresh mirror must not hold the dataset")
	highWater := mirrorHighWaterSequence(t, e.stack.Gateway)

	var updates []evaluation.CampaignMirrorReconcileProgress
	result, err := newMirrorRunReconciler(t, e).ReconcileAllVerifiedRunsFromStore(ctx, time.Minute, true, true, func(update evaluation.CampaignMirrorReconcileProgress) {
		updates = append(updates, update)
	})

	require.NoError(t, err)
	require.Len(t, updates, 2)
	assert.ErrorIs(t, updates[1].Err, constants.ErrEvidenceArtifactMalformed)
	assert.Contains(t, result.FailedRuns, runID)
	assert.Empty(t, result.RestoredRunIDs)
	assert.False(t, mirrorHasDataset(t, runID), "force must preserve eligibility requirements")
	assert.Equal(t, highWater, mirrorHighWaterSequence(t, e.stack.Gateway), "a rejected run must publish nothing")

	_, err = newMirrorRunReconciler(t, e).ReconcileRun(ctx, runID, true)
	assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	assert.False(t, mirrorHasDataset(t, runID), "a rejected run must not reach the mirror")
}

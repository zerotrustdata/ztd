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
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// scoredAttemptIDs returns the provider attempt ID of every model inference in one
// completed result.
func scoredAttemptIDs(result *evalv1.EvaluationAssignmentResult) []string {
	var ids []string
	for _, inference := range result.GetModelInferences() {
		if id := inference.GetProviderAttemptId(); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// newUnreachableWitnessCapture builds the witness remotes over the CLI runtime
// without the health gate NewCampaignWitnessCapture applies, so the reads reach
// whatever endpoint the CLI config currently names.
func newUnreachableWitnessCapture(t *testing.T, e *evalRunStack) *evaluation.CampaignWitnessCapture {
	t.Helper()
	providers, err := NewProviderObservationRemote(e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	provenance, err := newModelProvenanceRemote(e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	capture, err := evaluation.NewCampaignWitnessCapture(e.store, providers, provenance)
	require.NoError(t, err)
	return capture
}

// logoutCLISession revokes every session of the owner, including the CLI session
// that a live witness watch is following.
func logoutCLISession(ctx context.Context, e *evalRunStack) error {
	client := auth.NewEnrollmentClient(e.cliConfig, func() (string, error) { return "gwremote-real-stack", nil })
	_, err := client.Logout(ctx, e.cliFiles, constants.LogoutScopeAll)
	return err
}

// TestCampaignWitnessCaptureRealStack_RejectsMissingDependenciesAndEmptyRunID maps
// the constructor's required-remote checks and the empty-run-ID guards of every
// run-level and assignment-level method.
func TestCampaignWitnessCaptureRealStack_RejectsMissingDependenciesAndEmptyRunID(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	ctx := t.Context()

	providers, err := NewProviderObservationRemote(e.cliFiles, e.cliConfig)
	require.NoError(t, err)
	provenance, err := newModelProvenanceRemote(e.cliFiles, e.cliConfig)
	require.NoError(t, err)

	_, err = evaluation.NewCampaignWitnessCapture(nil, providers, provenance)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField, "missing store")
	_, err = evaluation.NewCampaignWitnessCapture(e.store, nil, provenance)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField, "missing provider observation remote")
	_, err = evaluation.NewCampaignWitnessCapture(e.store, providers, nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField, "missing model provenance remote")

	capture := newEvalWitnessCapture(t, e)
	require.ErrorIs(t, capture.CaptureRunProviderObservationEvidence(ctx, ""), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.CaptureRunModelProvenanceEvidence(ctx, ""), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.CaptureAssignmentEvidence(ctx, nil), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.WaitForRunModelProvenanceEvidence(ctx, ""), constants.ErrMissingRequiredField)
	require.ErrorIs(t, capture.WaitForRunProviderObservationEvidence(ctx, ""), constants.ErrMissingRequiredField)
}

// TestCampaignWitnessCaptureRealStack_SealsBothArtifacts runs one real assignment,
// then seals the Gateway's observation window, attempt record and attestation
// window for its scored attempt. The sealed copies must verify with no Gateway.
func TestCampaignWitnessCaptureRealStack_SealsBothArtifacts(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	result := e.executeOnce(t)
	attemptIDs := scoredAttemptIDs(result)
	require.NotEmpty(t, attemptIDs, "the executed assignment must name its provider attempt")
	attemptID := attemptIDs[0]
	runID := e.req.RunID

	require.NoError(t, newEvalWitnessCapture(t, e).CaptureAssignmentEvidence(ctx, result))

	// Capture seals the fetched evidence into the run and never writes the
	// production inference stores.
	inferenceExists, err := e.files.FileExists(ctx, filepath.Join(constants.DataDirname, constants.InferenceDirname))
	require.NoError(t, err)
	assert.False(t, inferenceExists, "capture must not write under data/inference")

	_, err = e.store.LoadProviderObservationWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	_, err = e.store.LoadProviderAttemptRecord(ctx, runID, attemptID)
	require.NoError(t, err)
	_, err = e.store.LoadModelProvenanceWindow(ctx, runID, attemptID)
	require.NoError(t, err)

	observations, err := evaluation.NewCampaignProviderObservationReader(e.store)
	require.NoError(t, err)
	failures, unavailable := observations.VerifyAssignmentProviderObservations(ctx, result, evaluation.ProviderObservationPolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
	provenance, err := evaluation.NewCampaignModelProvenanceReader(e.store)
	require.NoError(t, err)
	failures, unavailable = provenance.VerifyAssignmentModelProvenance(ctx, result, evaluation.ModelProvenancePolicyStrict)
	assert.Empty(t, failures)
	assert.Empty(t, unavailable)
}

// TestCampaignWitnessCaptureRealStack_SkipsAttemptsAlreadySealed re-captures a
// sealed assignment with the Gateway unreachable. Success with no sealed copy
// changed proves the capture made no remote read; a never-run attempt over the same
// unreachable endpoint must still fail, so the endpoint is really down.
func TestCampaignWitnessCaptureRealStack_SkipsAttemptsAlreadySealed(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	result := e.executeOnce(t)
	attemptID := scoredAttemptIDs(result)[0]
	runID := e.req.RunID
	require.NoError(t, newEvalWitnessCapture(t, e).CaptureAssignmentEvidence(ctx, result))

	windowBefore, err := e.store.LoadProviderObservationWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	provenanceBefore, err := e.store.LoadModelProvenanceWindow(ctx, runID, attemptID)
	require.NoError(t, err)

	cmdtest.WithEndpointOverride(t, closedLoopbackURL(t))
	replay := newUnreachableWitnessCapture(t, e)
	require.NoError(t, replay.CaptureAssignmentEvidence(ctx, result), "a sealed attempt must not be read from the Gateway")

	windowAfter, err := e.store.LoadProviderObservationWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(windowBefore, windowAfter), "the sealed observation window must be unchanged")
	provenanceAfter, err := e.store.LoadModelProvenanceWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	assert.True(t, proto.Equal(provenanceBefore, provenanceAfter), "the sealed attestation window must be unchanged")

	unsealed := &evalv1.EvaluationAssignmentResult{
		RunId:           runID,
		ModelInferences: []*evalv1.ModelInferenceRecord{{ProviderAttemptId: "attempt-never"}},
	}
	require.Error(t, replay.CaptureAssignmentEvidence(ctx, unsealed), "an unsealed attempt must be read, and the unreachable Gateway must fail it")
}

// TestCampaignWitnessCaptureRealStack_WaitForRunProvenanceSealsOnRecordingEvent
// waits for a real run's attestation windows. The Gateway records them as the
// Inference Operator finishes, and the wait seals them from the Gateway's
// attestation-recorded stream.
func TestCampaignWitnessCaptureRealStack_WaitForRunProvenanceSealsOnRecordingEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	result := e.executeOnce(t)
	attemptID := scoredAttemptIDs(result)[0]

	require.NoError(t, newEvalWitnessCapture(t, e).WaitForRunModelProvenanceEvidence(ctx, e.req.RunID))
	_, err := e.store.LoadModelProvenanceWindow(ctx, e.req.RunID, attemptID)
	require.NoError(t, err)
}

// TestCampaignWitnessCaptureRealStack_WaitForRunProviderObservationSealsOnRecordingEvent
// is the observation counterpart: the Gateway's recorded observation window and
// attempt record are sealed for a real run's attempt.
func TestCampaignWitnessCaptureRealStack_WaitForRunProviderObservationSealsOnRecordingEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	result := e.executeOnce(t)
	attemptID := scoredAttemptIDs(result)[0]

	require.NoError(t, newEvalWitnessCapture(t, e).WaitForRunProviderObservationEvidence(ctx, e.req.RunID))
	_, err := e.store.LoadProviderObservationWindow(ctx, e.req.RunID, attemptID)
	require.NoError(t, err)
	_, err = e.store.LoadProviderAttemptRecord(ctx, e.req.RunID, attemptID)
	require.NoError(t, err)
}

// TestCampaignWitnessCaptureRealStack_WaitForRunModelProvenanceReportsMissingOnDeadline
// waits on an attempt that never ran. The Gateway answers not-found, and the wait
// reports the window as missing when its short deadline passes.
func TestCampaignWitnessCaptureRealStack_WaitForRunModelProvenanceReportsMissingOnDeadline(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	saveRunAssignmentNaming(t, e.store, "run-never", "assignment-1", "attempt-never")
	capture := newEvalWitnessCapture(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := capture.WaitForRunModelProvenanceEvidence(ctx, "run-never")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.ErrorContains(t, err, "1 missing")
	assert.ErrorContains(t, err, "attempt-never")
}

// TestCampaignWitnessCaptureRealStack_WaitForRunProviderObservationReportsMissingOnDeadline
// is the observation counterpart of the attestation missing-window deadline.
func TestCampaignWitnessCaptureRealStack_WaitForRunProviderObservationReportsMissingOnDeadline(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	saveRunAssignmentNaming(t, e.store, "run-never", "assignment-1", "attempt-never")
	capture := newEvalWitnessCapture(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := capture.WaitForRunProviderObservationEvidence(ctx, "run-never")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.ErrorContains(t, err, "provider observation")
	assert.ErrorContains(t, err, "attempt-never")
}

// TestCampaignWitnessCaptureRealStack_WaitForProvenancePropagatesReadFailure points
// the witness at a closed port. The immediate capture surfaces the transport
// failure. The stream-driven wait never connects, so it cannot read, and it reports
// the window as unavailable at its deadline.
func TestCampaignWitnessCaptureRealStack_WaitForProvenancePropagatesReadFailure(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	saveRunAssignmentNaming(t, e.store, "run-unreachable", "assignment-1", "attempt-failed")
	cmdtest.WithEndpointOverride(t, closedLoopbackURL(t))
	capture := newUnreachableWitnessCapture(t, e)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := capture.WaitForRunModelProvenanceEvidence(ctx, "run-unreachable")
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "attempt-failed")

	err = capture.CaptureRunModelProvenanceEvidence(t.Context(), "run-unreachable")
	require.Error(t, err)
	require.NotErrorIs(t, err, constants.ErrNotFound, "a transport failure is not an absent window")
	assert.ErrorContains(t, err, "capture model provenance attempt-failed")
}

// TestCampaignWitnessCaptureRealStack_WaitForProvenanceIgnoresOtherAttempts records a
// real attempt on the Gateway, then waits on a different run whose attempt never
// ran. The recorded window belongs to another run, so the wait must still report
// its own pending attempt as missing and must not seal the other attempt.
func TestCampaignWitnessCaptureRealStack_WaitForProvenanceIgnoresOtherAttempts(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	recorded := scoredAttemptIDs(e.executeOnce(t))[0]

	saveRunAssignmentNaming(t, e.store, "run-pending", "assignment-pending", "attempt-pending")
	capture := newEvalWitnessCapture(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := capture.WaitForRunModelProvenanceEvidence(ctx, "run-pending")
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.ErrorContains(t, err, "1 missing")
	assert.ErrorContains(t, err, "attempt-pending")
	assert.NotContains(t, err.Error(), recorded)

	_, err = e.store.LoadModelProvenanceWindow(t.Context(), "run-pending", recorded)
	require.Error(t, err, "a window recorded for another run must not be sealed into this one")
}

// TestCampaignWitnessCaptureRealStack_WaitForProvenanceReconcilesOnConnect waits on
// a real run whose attestation window the Gateway recorded before the wait began.
// The store holds nothing for the attempt beforehand, so the window must be found
// on connect, with no later recording needed.
func TestCampaignWitnessCaptureRealStack_WaitForProvenanceReconcilesOnConnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)
	attemptID := scoredAttemptIDs(e.executeOnce(t))[0]
	_, err := e.store.LoadModelProvenanceWindow(ctx, e.req.RunID, attemptID)
	require.Error(t, err, "nothing is sealed before the wait")

	require.NoError(t, newEvalWitnessCapture(t, e).WaitForRunModelProvenanceEvidence(ctx, e.req.RunID))
	_, err = e.store.LoadModelProvenanceWindow(ctx, e.req.RunID, attemptID)
	require.NoError(t, err)
}

// TestCampaignWitnessCaptureRealStack_WaitForProvenanceFailsWhenStreamEnds revokes
// the owner's CLI sessions while a witness watch on a never-run attempt is open.
// The Gateway rejects the reconnect, and the wait must end with the session error.
func TestCampaignWitnessCaptureRealStack_WaitForProvenanceFailsWhenStreamEnds(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	saveRunAssignmentNaming(t, e.store, "run-orphaned", "assignment-1", "attempt-orphaned")
	capture := newEvalWitnessCapture(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	// The watch has to be open before the session is revoked; a one-second head
	// start is the only timing this test relies on.
	revoked := make(chan error, 1)
	go func() {
		time.Sleep(time.Second)
		revoked <- logoutCLISession(t.Context(), e)
	}()
	err := capture.WaitForRunModelProvenanceEvidence(ctx, "run-orphaned")
	require.NoError(t, <-revoked, "revoking the CLI session")
	require.ErrorIs(t, err, constants.ErrCLISessionRefreshRequired)
	require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	assert.ErrorContains(t, err, "attempt-orphaned")
}

// TestCampaignWitnessCaptureRealStack_WaitForProvenanceHonorsCancellation cancels a
// wait on a never-run attempt while it is open, and requires the cancellation to
// end it.
func TestCampaignWitnessCaptureRealStack_WaitForProvenanceHonorsCancellation(t *testing.T) {
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine})
	saveRunAssignmentNaming(t, e.store, "run-cancelled", "assignment-1", "attempt-cancelled")
	capture := newEvalWitnessCapture(t, e)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	timer := time.AfterFunc(300*time.Millisecond, cancel)
	defer timer.Stop()

	require.ErrorIs(t, capture.WaitForRunModelProvenanceEvidence(ctx, "run-cancelled"), context.Canceled)
}

// TestCampaignWitnessCaptureRealStack_DecoupledObservationAndProvenance seals a real
// run's observation and attestation windows through the production capture, then
// verifies the run from its sealed copies alone with the interim policies.
func TestCampaignWitnessCaptureRealStack_DecoupledObservationAndProvenance(t *testing.T) {
	ctx := t.Context()
	e := newEvalRunStack(t, evalRunOptions{posture: constants.PostureDoctrine, replies: evalRunShellReplies()})
	e.initialize(t)

	// Idempotent re-creation across the same source must succeed; parameter drift
	// must conflict.
	recreated, err := e.controller.CreateCampaign(ctx, evaluation.CampaignCreateRequest{
		CampaignID:        e.req.CampaignID,
		Catalog:           e.req.Catalog,
		Inventory:         e.req.Inventory,
		ScenarioArtifacts: e.req.ScenarioArtifacts,
		RepetitionCount:   e.req.RepetitionCount,
		Platform:          e.req.Platform,
	})
	require.NoError(t, err)
	require.NotEmpty(t, recreated.GetCampaignDigest())
	_, err = e.controller.CreateCampaign(ctx, evaluation.CampaignCreateRequest{
		CampaignID:        e.req.CampaignID,
		Catalog:           e.req.Catalog,
		Inventory:         e.req.Inventory,
		ScenarioArtifacts: e.req.ScenarioArtifacts,
		RepetitionCount:   e.req.RepetitionCount + 1,
		Platform:          e.req.Platform,
	})
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignConflict)

	result := e.executeOnce(t)
	attemptID := scoredAttemptIDs(result)[0]
	runID := e.req.RunID
	witness := newEvalWitnessCapture(t, e)

	require.NoError(t, witness.CaptureRunProviderObservationEvidence(ctx, runID))
	require.NoError(t, witness.CaptureRunModelProvenanceEvidence(ctx, runID))
	_, err = e.store.LoadProviderObservationWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	_, err = e.store.LoadModelProvenanceWindow(ctx, runID, attemptID)
	require.NoError(t, err)
	inferenceExists, err := e.files.FileExists(ctx, filepath.Join(constants.DataDirname, constants.InferenceDirname))
	require.NoError(t, err)
	assert.False(t, inferenceExists, "capture must not write under data/inference")

	// Verification reads only the sealed copies.
	observations, err := evaluation.NewCampaignProviderObservationReader(e.store)
	require.NoError(t, err)
	provenance, err := evaluation.NewCampaignModelProvenanceReader(e.store)
	require.NoError(t, err)
	verifier := evaluation.NewCampaignRunVerifier(e.clock).
		WithProviderObservationReader(observations, evaluation.ProviderObservationPolicyInterim).
		WithModelProvenanceReader(provenance, evaluation.ModelProvenancePolicyInterim)
	report, err := verifier.VerifyRun(ctx, e.store, runID, e.req.Catalog, e.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Equal(t, runID, report.GetRunId())
}

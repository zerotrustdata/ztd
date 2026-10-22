// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func TestRunsCancel_StopsEveryAssignmentOfAnIdleRun(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.prepareRun(t)

	out := env.mustRun(t, "runs", "cancel", "run-a-1", "--publish=false")
	assert.Contains(t, out, fmt.Sprintf("Cancelled run run-a-1: stopped %d assignment(s)", testModelRoleCells(t)))

	var show runShowJSON
	require.NoError(t, env.runJSON(t, &show, "runs", "show", "run-a-1"))
	assert.Equal(t, "cancelled", show.Status)
	assert.Equal(t, uint32(testModelRoleCells(t)), show.Stopped)
	assert.Zero(t, show.Queued)
}

func TestRunsCancel_JSONReportsWhatItStopped(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.prepareRun(t)

	var payload runCancelJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "cancel", "run-a-1", "--publish=false"))
	assert.Equal(t, "run-a-1", payload.RunID)
	assert.Equal(t, testModelRoleCells(t), payload.Stopped)
	assert.False(t, payload.WasRunning)
	assert.False(t, payload.ClearedStale)
}

func TestRunsCancel_ClearsAStaleLeaseAndReportsIt(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.prepareRun(t)
	env.holdRun(t, "run-a-1")
	env.control.setAlive(otherHostPID, false)

	out := env.mustRun(t, "runs", "cancel", "run-a-1", "--publish=false")
	assert.Contains(t, out, "Cleared stale lease held by process 9999 on test-host")

	_, err := env.store(t).LoadRunLease(context.Background(), "run-a-1")
	assert.True(t, isMissingRecord(err))
}

// An idle run has no process to report its release, so cancel reports the
// cancelled status itself, under the CLI session, for a watcher to see.
func TestRunsCancel_PostsCancelledForAnIdleRun(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	env.mustRun(t, "runs", "cancel", runID, "--publish=false")
	event := awaitGatewayRunEvent(t, events)
	require.NotNil(t, event.Status)
	assert.Equal(t, models.RunLifecycleStatusCancelled, event.Status.Status)
	assert.Equal(t, runID, event.Status.RunID)
	assert.Equal(t, models.RunKindEval, event.Status.RunKind)
}
func TestRunsCancel_OfASettledRunPostsNothing(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	assignments, err := env.store(t).ListAssignments(context.Background(), runID)
	require.NoError(t, err)
	for _, assignment := range assignments {
		env.saveGradedResult(t, assignment, nil)
	}
	events := observeGatewayRunEvents(t, env, runID)
	var payload runCancelJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "cancel", runID, "--publish=false"))
	assert.Zero(t, payload.Stopped)
	assert.False(t, payload.WasRunning)
	assert.False(t, payload.ClearedStale)
	requireNoGatewayRunEvent(t, events)
}
func TestRunsCancel_FailsClosedWhenGatewayRejectsTerminalTransition(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusFailed)
	seeded := awaitGatewayRunEvent(t, events)
	require.NotNil(t, seeded.Status)

	_, err := env.run(t, "runs", "cancel", runID, "--publish=false")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	var show runShowJSON
	require.NoError(t, env.runJSON(t, &show, "runs", "show", runID))
	assert.Equal(t, "cancelled", show.Status, "the local cancellation remains recorded even when the Gateway rejects its projection")
}
func TestRunsCancel_AsksALiveProcessToStopAndWaitsForItToRelease(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	env.holdRun(t, runID)
	events := observeGatewayRunEvents(t, env, runID)
	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := env.run(t, "runs", "cancel", runID, "--publish=false")
		done <- outcome{out, err}
	}()
	stop := awaitGatewayRunEvent(t, events)
	require.NotNil(t, stop.Stop)
	assert.Equal(t, runID, stop.Stop.RunID)
	select {
	case <-done:
		t.Fatal("cancel returned while the process still held the run")
	case <-time.After(50 * time.Millisecond):
	}
	require.NoError(t, env.store(t).ClearRunLease(context.Background(), runID))
	env.control.setAlive(otherHostPID, false)
	// A holder asked to stop leaves its queued assignments resumable, so it
	// projects waiting; only cancel, which stops them, projects cancelled.
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusWaiting)
	released := awaitGatewayRunEvent(t, events)
	require.NotNil(t, released.Status)
	select {
	case result := <-done:
		require.NoError(t, result.err, result.out)
		assert.Contains(t, result.out, fmt.Sprintf("stopped %d assignment(s)", testModelRoleCells(t)))
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not finish after the process released the run")
	}
	posted := awaitGatewayRunEvent(t, events)
	require.NotNil(t, posted.Status)
	assert.Equal(t, models.RunLifecycleStatusCancelled, posted.Status.Status)
	_, err := env.store(t).LoadRunLease(context.Background(), runID)
	assert.True(t, isMissingRecord(err))
}
func TestRunsCancel_ReturnsWhenTheHolderExitsWithoutReleasing(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	env.holdRun(t, runID)
	events := observeGatewayRunEvents(t, env, runID)
	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := env.run(t, "runs", "cancel", runID, "--publish=false")
		done <- outcome{out, err}
	}()
	stop := awaitGatewayRunEvent(t, events)
	require.NotNil(t, stop.Stop)
	env.control.exit(otherHostPID)
	select {
	case result := <-done:
		require.NoError(t, result.err, result.out)
		assert.Contains(t, result.out, "Process 9999 exited; cleared its lease on run "+runID)
		assert.Contains(t, result.out, fmt.Sprintf("stopped %d assignment(s)", testModelRoleCells(t)))
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not return after the holder exited")
	}
	_, err := env.store(t).LoadRunLease(context.Background(), runID)
	assert.True(t, isMissingRecord(err))
}
func TestRunsCancel_PostsCancelledWhenTheHolderExited(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	env.holdRun(t, runID)
	events := observeGatewayRunEvents(t, env, runID)
	type outcome struct {
		out string
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		out, err := env.run(t, "runs", "cancel", runID, "--publish=false")
		done <- outcome{out, err}
	}()
	stop := awaitGatewayRunEvent(t, events)
	require.NotNil(t, stop.Stop)
	env.control.exit(otherHostPID)
	select {
	case result := <-done:
		require.NoError(t, result.err, result.out)
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not return after the holder exited")
	}
	posted := awaitGatewayRunEvent(t, events)
	require.NotNil(t, posted.Status)
	assert.Equal(t, models.RunLifecycleStatusCancelled, posted.Status.Status)
	assert.Equal(t, runID, posted.Status.RunID)
	requireNoGatewayRunEvent(t, events)
}
func TestRunsCancel_RejectsMissingAndArchivedRuns(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "cancel", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runs cancel")

	env.mustRun(t, "runs", "archive", "run-a-1")
	_, err = env.run(t, "runs", "cancel", "run-a-1", "--publish=false")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationArchived)
}

func TestRunsResume_DoesNotResumeAStoppedRun(t *testing.T) {
	env := newStoreRunEnv(t)
	runID := env.prepareStoredRun(t, "eval-a", "run-a-1")
	_, err := evaluation.NewCampaignController(env.store(t), nil, env.deps.now, adaptNewID(env.deps.newID)).CancelRun(context.Background(), runID)
	require.NoError(t, err)

	store := env.store(t)
	controller := evaluation.NewCampaignController(store, nil, env.deps.now, func(prefix string) string { return prefix + "-id" })
	assignment, ok, err := controller.ResumeNextAssignment(context.Background(), runID)
	assert.False(t, ok, "cancelled run has no queued assignments to resume")
	assert.Nil(t, assignment)
	assert.NoError(t, err)
}

func TestRunsResume_ReportsItsLifecycleToTheGateway(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	out, err := env.run(t, "runs", "resume", runID, "--no-auto-bind")
	require.NoError(t, err, out)
	first := awaitGatewayRunEvent(t, events)
	require.NotNil(t, first.Status)
	assert.Equal(t, models.RunLifecycleStatusRunning, first.Status.Status)
	second := awaitGatewayRunEvent(t, events)
	require.NotNil(t, second.Status)
	assert.Equal(t, runID, second.Status.RunID)
	assert.Contains(t, []models.RunLifecycleStatus{models.RunLifecycleStatusWaiting, models.RunLifecycleStatusCompleted}, second.Status.Status)
}
func TestRunsResume_OfACompletedRunIsRejectedByTheGateway(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusCompleted)
	seeded := awaitGatewayRunEvent(t, events)
	require.NotNil(t, seeded.Status)
	_, err := env.run(t, "runs", "resume", runID, "--no-auto-bind")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	var transition *models.ObserveTransitionError
	require.ErrorAs(t, err, &transition, "the Gateway's refused transition reaches the CLI typed")
	assert.Equal(t, &models.ObserveTransitionError{
		Kind: models.ObserveProjectionKindRun, ID: runID,
		From: string(models.RunLifecycleStatusCompleted), To: string(models.RunLifecycleStatusRunning),
	}, transition)
	var show runShowJSON
	require.NoError(t, env.runJSON(t, &show, "runs", "show", runID))
	assert.Equal(t, uint32(testModelRoleCells(t)), show.Queued)
	assert.Zero(t, show.Terminal)
	requireNoGatewayRunEvent(t, events)
}

// A paused run whose Gateway projection is wrongly terminal cannot resume until
// repair --projection reopens it from the authoritative local store.
func TestRunsRepair_ProjectionReopensACancelledProjectionOfAResumableRun(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusCancelled)
	seeded := awaitGatewayRunEvent(t, events)
	require.NotNil(t, seeded.Status)

	_, err := env.run(t, "runs", "resume", runID, "--no-auto-bind")
	require.Error(t, err)
	var transition *models.ObserveTransitionError
	require.ErrorAs(t, err, &transition)
	assert.Contains(t, err.Error(), "ztd eval runs repair "+runID+" --projection")

	var repaired runRepairJSON
	require.NoError(t, env.runJSON(t, &repaired, "runs", "repair", runID, "--projection"))
	assert.Equal(t, models.RunLifecycleStatusCancelled, repaired.ProjectionReopened)
	reopened := awaitGatewayRunEvent(t, events)
	require.NotNil(t, reopened.Status)
	assert.Equal(t, models.RunLifecycleStatusWaiting, reopened.Status.Status)

	out := env.mustRun(t, "runs", "repair", runID, "--projection")
	assert.Contains(t, out, "needed no reopen", "a projection already matching the store is left alone")
	repeated := awaitGatewayRunEvent(t, events)
	require.NotNil(t, repeated.Status)
	assert.Equal(t, models.RunLifecycleStatusWaiting, repeated.Status.Status)
}

func TestRunsRepair_ProjectionLeavesACancelledLocalRunAlone(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	env.mustRun(t, "runs", "cancel", runID, "--publish=false")
	events := observeGatewayRunEvents(t, env, runID)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusCancelled)
	require.NotNil(t, awaitGatewayRunEvent(t, events).Status)

	var repaired runRepairJSON
	require.NoError(t, env.runJSON(t, &repaired, "runs", "repair", runID, "--projection"))
	assert.Empty(t, repaired.ProjectionReopened, "a cancelled local run is not resumable")
	requireNoGatewayRunEvent(t, events)
}

func TestRunsResume_FailsClosedWhenGatewayRejectsRunning(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	events := observeGatewayRunEvents(t, env, runID)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusFailed)
	seeded := awaitGatewayRunEvent(t, events)
	require.NotNil(t, seeded.Status)
	_, err := env.run(t, "runs", "resume", runID, "--no-auto-bind")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrHTTPStatusError)
	var show runShowJSON
	require.NoError(t, env.runJSON(t, &show, "runs", "show", runID))
	assert.Equal(t, uint32(testModelRoleCells(t)), show.Queued)
	assert.Zero(t, show.Terminal)
	requireNoGatewayRunEvent(t, events)
}
func TestRunsShow_WatchReturnsForASettledRun(t *testing.T) {
	env, _ := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	env.prepareRun(t)
	env.mustRun(t, "runs", "cancel", "run-a-1", "--publish=false")

	out := env.mustRun(t, "runs", "show", "run-a-1", "--watch")
	assert.Contains(t, out, "Status: cancelled")
}

func TestRunsShow_WatchFollowsARunUntilItSettles(t *testing.T) {
	env, stack := newGatewayRunEnvWithPosture(t, constants.PostureDoctrine)
	runID := env.prepareRun(t)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusRunning)
	done := make(chan string, 1)
	go func() {
		out, _ := env.run(t, "runs", "show", runID, "--watch")
		done <- out
	}()
	select {
	case <-done:
		t.Fatal("watch returned for a run that has not settled")
	case <-time.After(100 * time.Millisecond):
	}
	store := env.store(t)
	_, err := evaluation.NewCampaignController(store, nil, env.deps.now, adaptNewID(env.deps.newID)).CancelRun(context.Background(), runID)
	require.NoError(t, err)
	publishGatewayRunState(t, env, stack, runID, models.RunLifecycleStatusCancelled)
	select {
	case out := <-done:
		assert.Contains(t, out, "cancelled")
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not return after the run settled")
	}
}
func TestRunsArchive_RoundTrip(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	out := env.mustRun(t, "runs", "archive", "run-a-1")
	assert.Contains(t, out, "Archived run run-a-1")
	assert.Contains(t, out, "public record", "archiving must say the public projections remain")

	assert.Contains(t, env.mustRun(t, "runs", "list"), "No runs found")

	var listed runListJSON
	require.NoError(t, env.runJSON(t, &listed, "runs", "list", "--archived"))
	require.Len(t, listed.Runs, 1)
	assert.True(t, listed.Runs[0].Archived)

	var show runShowJSON
	require.NoError(t, env.runJSON(t, &show, "runs", "show", "run-a-1"))
	assert.True(t, show.Archived)
	assert.Equal(t, "scheduled", show.Status)

	out = env.mustRun(t, "runs", "unarchive", "run-a-1")
	assert.Contains(t, out, "Unarchived run run-a-1")
	require.NoError(t, env.runJSON(t, &listed, "runs", "list"))
	require.Len(t, listed.Runs, 1)
	assert.False(t, listed.Runs[0].Archived)
}

func TestRunsArchive_RecordsWhoArchivedAndWhen(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	var payload archiveResultJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "archive", "run-a-1"))
	assert.Equal(t, "run", payload.Kind)
	assert.Equal(t, "run-a-1", payload.ID)
	assert.Equal(t, "eval-a", payload.CampaignID)
	assert.Equal(t, "local-operator", payload.ArchivedBy)
	assert.NotEmpty(t, payload.ArchivedAt)
	assert.True(t, payload.Archived)
}

func TestRunsArchive_ArchivedRunsStayReadable(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.startStoredRun(t, "eval-a", "run-a-2")
	env.mustRun(t, "runs", "archive", "run-a-1")

	_, err := env.run(t, "runs", "verify", "run-a-1", "--coverage")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvalRunVerificationFailed, "an incomplete archived run is still verifiable, and reports as incomplete")

	env.mustRun(t, "runs", "export", "run-a-1", "--output-dir", "export/run-a-1")

	var compared runCompareJSON
	require.NoError(t, env.runJSON(t, &compared, "runs", "compare", "run-a-1", "run-a-2"))
	assert.Equal(t, testModelRoleCells(t), compared.Left.Cells)
	assert.Equal(t, testModelRoleCells(t), compared.Right.Cells)
}

func TestRunsArchive_ArchivedRunsAreRejectedByMutatingCommands(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.mustRun(t, "runs", "archive", "run-a-1")

	for _, args := range [][]string{
		{"runs", "resume", "run-a-1", "--no-auto-bind"},
		{"runs", "publish", "run-a-1"},
		{"runs", "repair", "run-a-1"},
	} {
		_, err := env.run(t, args...)
		require.Error(t, err, strings.Join(args, " "))
		assert.ErrorIs(t, err, constants.ErrEvaluationArchived, strings.Join(args, " "))
	}
}

func TestRunsArchive_RefusesARunThatIsRunning(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.holdRun(t, "run-a-1")

	_, err := env.run(t, "runs", "archive", "run-a-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationRunRunning)

	var listed runListJSON
	require.NoError(t, env.runJSON(t, &listed, "runs", "list"))
	require.Len(t, listed.Runs, 1, "a refused archive moves nothing")
}

func TestRunsArchive_ArchivesARunWhoseLeaseIsStale(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.holdRun(t, "run-a-1")
	env.control.setAlive(otherHostPID, false)

	env.mustRun(t, "runs", "archive", "run-a-1")
}

func TestRunsArchive_RejectsBadTargets(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")

	_, err := env.run(t, "runs", "archive", "missing")
	require.Error(t, err)

	env.mustRun(t, "runs", "archive", "run-a-1")
	_, err = env.run(t, "runs", "archive", "run-a-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationArchived)

	env.mustRun(t, "runs", "unarchive", "run-a-1")
	_, err = env.run(t, "runs", "unarchive", "run-a-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationNotArchived)
}

func TestRunsArchive_ARunArchivedAloneStaysListedUnderItsActiveCampaign(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.startStoredRun(t, "eval-a", "run-a-2")
	env.mustRun(t, "runs", "archive", "run-a-1")

	var payload campaignShowJSON
	require.NoError(t, env.runJSON(t, &payload, "campaigns", "show", "eval-a"))
	require.Len(t, payload.Runs, 2)
	archived := map[string]bool{}
	for _, row := range payload.Runs {
		archived[row.RunID] = row.Archived
	}
	assert.Equal(t, map[string]bool{"run-a-1": true, "run-a-2": false}, archived)
}

func TestCampaignsArchive_ArchivesTheCampaignAndAllOfItsRuns(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.startStoredRun(t, "eval-a", "run-a-2")
	env.prepareStoredRun(t, "eval-b", "run-b-1")

	out := env.mustRun(t, "campaigns", "archive", "eval-a")
	assert.Contains(t, out, "Archived campaign eval-a and 2 run(s)")
	assert.Contains(t, out, "public record")

	var campaigns campaignListJSON
	require.NoError(t, env.runJSON(t, &campaigns, "campaigns", "list"))
	require.Len(t, campaigns.Campaigns, 1)
	assert.Equal(t, "eval-b", campaigns.Campaigns[0].CampaignID)

	require.NoError(t, env.runJSON(t, &campaigns, "campaigns", "list", "--archived"))
	require.Len(t, campaigns.Campaigns, 2)

	var runs runListJSON
	require.NoError(t, env.runJSON(t, &runs, "runs", "list"))
	require.Len(t, runs.Runs, 1)
	assert.Equal(t, "run-b-1", runs.Runs[0].RunID)

	var show campaignShowJSON
	require.NoError(t, env.runJSON(t, &show, "campaigns", "show", "eval-a"))
	assert.True(t, show.Archived)
	require.NotNil(t, show.Archive)
	assert.Equal(t, "local-operator", show.Archive.ArchivedBy)
	assert.Len(t, show.Runs, 2)
}

func TestCampaignsArchive_RoundTrip(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.mustRun(t, "campaigns", "archive", "eval-a")

	out := env.mustRun(t, "campaigns", "unarchive", "eval-a")
	assert.Contains(t, out, "Unarchived campaign eval-a and 1 run(s)")

	var campaigns campaignListJSON
	require.NoError(t, env.runJSON(t, &campaigns, "campaigns", "list"))
	require.Len(t, campaigns.Campaigns, 1)
	assert.False(t, campaigns.Campaigns[0].Archived)
	var runs runListJSON
	require.NoError(t, env.runJSON(t, &runs, "runs", "list"))
	require.Len(t, runs.Runs, 1)
	assert.Equal(t, "run-a-1", runs.Runs[0].RunID)

	_, err := env.run(t, "campaigns", "unarchive", "eval-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationNotArchived)
}

func TestCampaignsArchive_MovesNothingWhenARunIsRunning(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.startStoredRun(t, "eval-a", "run-a-2")
	env.holdRun(t, "run-a-2")

	_, err := env.run(t, "campaigns", "archive", "eval-a")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationRunRunning)

	var runs runListJSON
	require.NoError(t, env.runJSON(t, &runs, "runs", "list"))
	assert.Len(t, runs.Runs, 2, "no run moved to the archive")
	var campaigns campaignListJSON
	require.NoError(t, env.runJSON(t, &campaigns, "campaigns", "list"))
	require.Len(t, campaigns.Campaigns, 1)
	assert.False(t, campaigns.Campaigns[0].Archived)
}

func TestCampaignsArchive_ARunUnarchivedAloneNeedsItsCampaignActive(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.mustRun(t, "campaigns", "archive", "eval-a")

	_, err := env.run(t, "runs", "unarchive", "run-a-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationArchived)
	assert.Contains(t, err.Error(), "unarchive the campaign first")
}

func TestCampaignsCreate_RejectsAnArchivedCampaignID(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	env.mustRun(t, "campaigns", "archive", "eval-a")

	_, err := env.run(t, "campaigns", "create", "eval-a", "qwen3:4b")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrEvaluationArchived)
}

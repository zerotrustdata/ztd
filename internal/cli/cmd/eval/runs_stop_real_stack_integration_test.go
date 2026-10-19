// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	stopRealStackCampaignID = "real-stop-campaign"
	stopRealStackRunID      = "real-stop-run"
)

// newStopRealStackRun prepares a two-assignment doctrine run on the real stack,
// not yet executing. Its process boundary is a fake, so a cancel signals no real
// process: the executing run and the cancel share this test's process.
func newStopRealStackRun(t *testing.T) *realStackRun {
	t.Helper()
	run := newRealStackRun(t, constants.PostureDoctrine, stopRealStackCampaignID, 2, testRunControl(newFakeProcessControl()))
	result, err := runStartFlow(run.command(), run.deps, runStartFlowOptions{CampaignID: stopRealStackCampaignID, RunID: stopRealStackRunID, PrepareOnly: true})
	require.NoError(t, err)
	require.True(t, result.Prepared)
	assignments, err := run.store().ListAssignments(run.ctx, stopRealStackRunID)
	require.NoError(t, err)
	require.Len(t, assignments, 2)
	return run
}

func (r *realStackRun) store() *evaluation.Store { return evaluation.NewStore(r.fileSvc) }

// command is a fresh command for one in-process call, rooted at the run's project.
func (r *realStackRun) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(r.ctx)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.Flags().String("project-root", r.root, "")
	return cmd
}

// heldAssignment makes a run's first assignment wait, before its model turn,
// until the test releases it. A stop can then land while that assignment is in
// flight.
type heldAssignment struct {
	inner   evaluation.CampaignAssignmentExecutor
	entered atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (h *heldAssignment) ExecuteAssignment(ctx context.Context, req evaluation.AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	if h.entered.CompareAndSwap(false, true) {
		close(h.started)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.inner.ExecuteAssignment(ctx, req)
}

// holdFirstAssignment routes the run's executor through a heldAssignment. It must
// be called before the run executes.
func (r *realStackRun) holdFirstAssignment() *heldAssignment {
	held := &heldAssignment{started: make(chan struct{}), release: make(chan struct{})}
	r.deps.agentExecutorFactory = func(in campaignAgentExecutorInputs) (evaluation.CampaignAssignmentExecutor, error) {
		inner, err := newProductionCampaignAgentExecutor(in)
		if err != nil {
			return nil, err
		}
		held.inner = inner
		return held, nil
	}
	return held
}

type executeOutcome struct {
	executed int
	err      error
}

// executeAsync runs executeRun in a goroutine, the way `runs resume --daemon`
// runs in its own process.
func (r *realStackRun) executeAsync() <-chan executeOutcome {
	done := make(chan executeOutcome, 1)
	deps := r.deps
	cmd := r.command()
	go func() {
		executed, err := executeRun(cmd, deps, runExecuteOptions{RunID: stopRealStackRunID, Daemon: true})
		done <- executeOutcome{executed: executed, err: err}
	}()
	return done
}

type cancelOutcome struct {
	out string
	err error
}

// cancelAsync runs `runs cancel` in a goroutine, the way an operator runs it from
// another process.
func (r *realStackRun) cancelAsync() <-chan cancelOutcome {
	done := make(chan cancelOutcome, 1)
	deps := r.deps
	root := r.root
	go func() {
		ctx, cancel := context.WithTimeout(testVersionContext(), 3*time.Minute)
		defer cancel()
		var out bytes.Buffer
		cmd := evalCmdWithConfig(deps)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"runs", "cancel", stopRealStackRunID, "--publish=false", "--project-root", root})
		err := cmd.ExecuteContext(ctx)
		done <- cancelOutcome{out: out.String(), err: err}
	}()
	return done
}

// stopSignal returns a run-stop hook that signals once per stop recorded on the
// executing run.
func stopSignal() (func(), <-chan struct{}) {
	stopped := make(chan struct{}, 1)
	return func() {
		select {
		case stopped <- struct{}{}:
		default:
		}
	}, stopped
}

// awaitSignal waits for a signal, failing the test after a bound that covers a
// real model turn.
func awaitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Minute):
		require.FailNow(t, what)
	}
}

// awaitResult receives the next value from ch, or fails the test after the same
// bound awaitSignal uses.
func awaitResult[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Minute):
		require.FailNow(t, what)
		var zero T
		return zero
	}
}

// TestRunsCancel_RealStack_StopsTheRunAfterItsCurrentAssignment covers the stop
// path end to end: cancel records the request and posts it, the Gateway delivers
// it on the run's stream, and the executing run finishes the assignment it is on
// and starts no other.
func TestRunsCancel_RealStack_StopsTheRunAfterItsCurrentAssignment(t *testing.T) {
	run := newStopRealStackRun(t)
	held := run.holdFirstAssignment()
	onStop, stopped := stopSignal()
	run.deps.runControl.onStopRequested = onStop

	execDone := run.executeAsync()
	awaitSignal(t, held.started, "the first assignment never started")
	cancelDone := run.cancelAsync()
	awaitSignal(t, stopped, "the stop request never reached the executing run")
	close(held.release)

	exec := awaitResult(t, execDone, "the executing run did not finish")
	require.ErrorIs(t, exec.err, constants.ErrEvaluationRunStopped)
	assert.Equal(t, 1, exec.executed)
	assert.Equal(t, 1, run.stack.Provider.ChatCount(), "a stopped run must not start its next assignment")

	cancel := awaitResult(t, cancelDone, "cancel did not return")
	require.NoError(t, cancel.err, cancel.out)
	assert.Contains(t, cancel.out, "Cancelled run "+stopRealStackRunID+": stopped 1 assignment(s)")
}

// TestRunsCancel_RealStack_ReturnsOnlyAfterTheRunIsReleased shows cancel waits on
// the released status and not on the stop request: while the executing run still
// holds its assignment, cancel keeps waiting, and it returns once the run has
// released its lease.
func TestRunsCancel_RealStack_ReturnsOnlyAfterTheRunIsReleased(t *testing.T) {
	run := newStopRealStackRun(t)
	held := run.holdFirstAssignment()
	onStop, stopped := stopSignal()
	run.deps.runControl.onStopRequested = onStop

	execDone := run.executeAsync()
	awaitSignal(t, held.started, "the first assignment never started")
	cancelDone := run.cancelAsync()
	awaitSignal(t, stopped, "the stop request never reached the executing run")

	select {
	case <-cancelDone:
		t.Fatal("cancel returned while the executing run still held its assignment")
	case <-time.After(200 * time.Millisecond):
	}
	close(held.release)

	exec := awaitResult(t, execDone, "the executing run did not finish")
	require.ErrorIs(t, exec.err, constants.ErrEvaluationRunStopped)
	cancel := awaitResult(t, cancelDone, "cancel did not return after the release")
	require.NoError(t, cancel.err, cancel.out)
	_, err := run.store().LoadRunLease(context.Background(), stopRealStackRunID)
	assert.True(t, isMissingRecord(err), "cancel returns only once the lease is released")
}

// TestRunsCancel_RealStack_CatchesAStopRequestedBeforeSubscribing shows a stop
// that lands before the run subscribes to its stream is not lost. The lease
// records the request, the stop event goes to a stream nobody holds yet, and the
// connect reconcile reads the lease and stops the run before its first assignment.
func TestRunsCancel_RealStack_CatchesAStopRequestedBeforeSubscribing(t *testing.T) {
	run := newStopRealStackRun(t)
	onStop, stopped := stopSignal()
	run.deps.runControl.onStopRequested = onStop
	run.deps.runControl.onLeaseAcquired = func() {
		_, err := run.store().RequestRunCancel(run.ctx, stopRealStackRunID, time.Now())
		require.NoError(t, err)
		authContext, err := run.deps.authLoader(run.fileSvc, run.cfg)
		require.NoError(t, err)
		client, err := inferenceEvalAppClient(run.ctx, io.Discard, run.deps, run.fileSvc, run.cfg, authContext)
		require.NoError(t, err)
		require.NoError(t, client.RequestRunStop(run.ctx, models.ObserveProducerStopRequest{
			SchemaVersion: constants.ObserveEventPayloadSchemaVersion,
			CampaignID:    stopRealStackCampaignID,
			RunID:         stopRealStackRunID,
			StopReason:    models.StopReasonGraceful,
			StopScope:     models.StopScopeCycle,
			CLISessionID:  authContext.CLISessionID,
		}))
	}

	executed, err := executeRun(run.command(), run.deps, runExecuteOptions{RunID: stopRealStackRunID, Daemon: true})
	require.ErrorIs(t, err, constants.ErrEvaluationRunStopped)
	assert.Zero(t, executed, "the reconcile runs before the first assignment, so none starts")
	assert.Zero(t, run.stack.Provider.ChatCount())
	select {
	case <-stopped:
	default:
		require.FailNow(t, "the connect reconcile did not mark the run stop requested")
	}
	_, leaseErr := run.store().LoadRunLease(context.Background(), stopRealStackRunID)
	assert.True(t, isMissingRecord(leaseErr), "the stopped run releases its lease")
}

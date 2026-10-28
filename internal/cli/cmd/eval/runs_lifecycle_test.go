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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const otherHostPID = 9999

func TestRunsCancel_PublicationIsOptIn(t *testing.T) {
	cmd := runsCancelCmd(nativeEvalDeps{})
	flag := cmd.Flags().Lookup("publish")
	require.NotNil(t, flag)
	assert.Equal(t, "false", flag.DefValue)
}

// rejectingRunStateReporter is a Gateway run projection that refuses every update.
type rejectingRunStateReporter struct {
	err error
}

func (r rejectingRunStateReporter) UpdateRunState(context.Context, models.ObserveProducerRunStateRequest) error {
	return r.err
}

// recordingRunStateReporter is a Gateway run projection that accepts every update
// and keeps the statuses it was sent.
type recordingRunStateReporter struct {
	posts    []models.RunLifecycleStatus
	requests []models.ObserveProducerRunStateRequest
}

func (r *recordingRunStateReporter) UpdateRunState(_ context.Context, req models.ObserveProducerRunStateRequest) error {
	r.posts = append(r.posts, req.Status)
	r.requests = append(r.requests, req)
	return nil
}

// A release that executed nothing posts nothing. A drained run whose verification
// has failed is such a release, and the Gateway would refuse its terminal status
// after a completed projection. The release reads no run state here, so the
// lifecycle has no store.
func TestRunLifecycle_ReleaseOfANoOpRunPostsNothing(t *testing.T) {
	reporter := &recordingRunStateReporter{}
	lifecycle := &runLifecycle{
		client: reporter,
		deps:   nativeEvalDeps{now: func() time.Time { return time.Unix(1789657337, 0).UTC() }},
		runID:  "run-a-1",
	}

	require.NoError(t, lifecycle.reportTerminal(context.Background(), false))
	assert.Empty(t, reporter.posts)
}

func TestRunLifecycle_RejectedPostIsReturnedToTheCommand(t *testing.T) {
	lifecycle := runLifecycle{
		client:    rejectingRunStateReporter{err: fmt.Errorf("observe producer run-state: %w", constants.ErrHTTPStatusError)},
		deps:      nativeEvalDeps{now: func() time.Time { return time.Unix(1789657337, 0).UTC() }},
		runID:     "run-a-1",
		startedAt: time.Unix(1789657000, 0).UTC(),
	}

	err := lifecycle.post(context.Background(), models.RunLifecycleStatusRunning, &runRow{Queued: 1})
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.Contains(t, err.Error(), "run-a-1")
	assert.Contains(t, err.Error(), string(models.RunLifecycleStatusRunning))
}

func TestRunLifecycle_ObservedAtNeverMovesBackward(t *testing.T) {
	startedAt := time.Unix(1789657338, 0).UTC()
	readings := []time.Time{startedAt.Add(time.Second), startedAt.Add(-time.Second), startedAt.Add(3 * time.Second)}
	index := 0
	reporter := &recordingRunStateReporter{}
	lifecycle := runLifecycle{client: reporter, runID: "run-a-1", startedAt: startedAt, deps: nativeEvalDeps{now: func() time.Time {
		value := readings[index]
		index++
		return value
	}}}

	require.NoError(t, lifecycle.post(context.Background(), models.RunLifecycleStatusRunning, &runRow{Queued: 1}))
	endedAt := startedAt.Add(-2 * time.Second)
	require.NoError(t, lifecycle.send(context.Background(), models.RunLifecycleStatusWaiting, &runRow{Queued: 1}, &endedAt, ""))
	require.NoError(t, lifecycle.post(context.Background(), models.RunLifecycleStatusRunning, &runRow{Queued: 1}))

	require.Len(t, reporter.requests, 3)
	assert.Equal(t, readings[0], reporter.requests[0].ObservedAt)
	assert.Equal(t, readings[0], reporter.requests[1].ObservedAt, "a wall-clock step cannot make a later lifecycle update stale")
	assert.Equal(t, readings[2], reporter.requests[2].ObservedAt, "a later forward reading is preserved")
	require.NotNil(t, reporter.requests[1].EndedAt)
	assert.GreaterOrEqual(t, *reporter.requests[1].EndedAt, startedAt)
	assert.LessOrEqual(t, *reporter.requests[1].EndedAt, reporter.requests[1].ObservedAt)
}

// holdRun records a lease on a run for a process that the fake process control
// reports as alive.
func (e *runEnv) holdRun(t *testing.T, runID string) {
	t.Helper()
	_, err := e.store(t).AcquireRunLease(context.Background(), evaluation.RunLease{
		RunID:     runID,
		PID:       otherHostPID,
		Host:      testHost,
		StartedAt: e.deps.now(),
	}, nil)
	require.NoError(t, err)
	e.control.setAlive(otherHostPID, true)
}

// The holder's process exits and no released status ever arrives. The exit
// settles the lease, and the wait ends on the exit alone.
func TestRunsCancel_ReturnsWhenTheHolderExitsBeforeAnyReleasedEvent(t *testing.T) {
	ctx := context.Background()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), testutil.NewTestLogger())
	require.NoError(t, err)
	store := evaluation.NewStore(files)
	control := newFakeProcessControl()
	control.setAlive(otherHostPID, true)
	_, err = store.AcquireRunLease(ctx, evaluation.RunLease{RunID: "run-a-1", PID: otherHostPID, Host: testHost, StartedAt: time.Now().UTC()}, nil)
	require.NoError(t, err)
	live := leaseLiveness(control)

	holderExit := watchHolderExit(ctx, control, otherHostPID)
	control.exit(otherHostPID)
	// Nothing is delivered on the released or events channels, and the stream stays
	// open, so only the holder's exit can end the wait.
	stream := &runEventStream{done: make(chan struct{})}
	cleared, err := awaitRunRelease(ctx, nil, nil, stream, holderExit, func() (bool, bool, error) {
		return settleRunLease(ctx, store, live, "run-a-1")
	})
	require.NoError(t, err)
	assert.True(t, cleared)
	_, err = store.LoadRunLease(ctx, "run-a-1")
	assert.True(t, isMissingRecord(err))
}

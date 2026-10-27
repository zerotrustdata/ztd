// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// The watch reloads the run on its connect and on each run event. Here the
// connect and two events arrive in either order, and the third reload settles.
func TestRunsShow_WatchEmitsOnConnectAndOnStatusAndStopsOnTerminal(t *testing.T) {
	stream := &runEventStream{done: make(chan struct{})}
	reconnects := make(chan struct{}, 1)
	events := make(chan runEvent)
	states := []*runShowJSON{
		{runRow: runRow{Status: "running", ExpectedAssignments: 2, Running: 1}},
		{runRow: runRow{Status: "running", ExpectedAssignments: 2, Terminal: 1, Running: 1}},
		{runRow: runRow{Status: "cancelled", ExpectedAssignments: 2, Terminal: 2}},
	}
	reconnects <- struct{}{}
	go func() {
		events <- runEvent{Status: &models.RunStatusUpdatedPayload{RunID: "run-a-1", Status: models.RunLifecycleStatusRunning}}
		events <- runEvent{Status: &models.RunStatusUpdatedPayload{RunID: "run-a-1", Status: models.RunLifecycleStatusCancelled}}
	}()

	var reloads int
	reload := func() (*runShowJSON, error) {
		show := states[reloads]
		reloads++
		return show, nil
	}
	var emitted []string
	emit := func(show *runShowJSON) error {
		emitted = append(emitted, show.Status)
		return nil
	}
	require.NoError(t, followRun(context.Background(), stream, reconnects, events, reload, emit))
	assert.Equal(t, []string{"running", "running", "cancelled"}, emitted)
	assert.Equal(t, 3, reloads)
}

// A stream that stops on its own ends the watch with the stream's error, after
// no further reload.
func TestRunsShow_WatchReturnsTheStreamError(t *testing.T) {
	streamErr := errors.New("gateway refused the CLI session")
	stream := &runEventStream{done: make(chan struct{}), err: streamErr}
	close(stream.done)
	reload := func() (*runShowJSON, error) {
		t.Fatal("the watch reloaded after its stream stopped")
		return nil, nil
	}
	err := followRun(context.Background(), stream, make(chan struct{}), make(chan runEvent), reload, func(*runShowJSON) error { return nil })
	require.ErrorIs(t, err, streamErr)
}

func markFirstAssignmentRunning(t *testing.T, env *runEnv) {
	t.Helper()
	ctx := context.Background()
	store := env.store(t)
	assignments, err := store.ListAssignments(ctx, "run-a-1")
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	assignments[0].LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING
	assignments[0].StartedAt = timestamppb.New(env.deps.now())
	require.NoError(t, store.SaveAssignment(ctx, assignments[0]))
}

func TestRunCommands_DoNotExposeOllamaEndpointOverride(t *testing.T) {
	t.Parallel()
	commands := []*cobra.Command{
		runsStartCmd(nativeEvalDeps{}),
		runsResumeCmd(nativeEvalDeps{}),
		rolloutRunCmd(nativeEvalDeps{}),
		rolloutResumeCmd(nativeEvalDeps{}),
	}
	for _, command := range commands {
		assert.Nil(t, command.Flags().Lookup("ollama-endpoint"), command.Use)
	}
}

func TestVerificationReportHelpers(t *testing.T) {
	report := &evalv1.EvaluationVerificationReport{
		Status:         evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		FailureCount:   2,
		FailureReasons: []string{"missing witness", "digest mismatch"},
	}

	assert.Equal(t, report.GetStatus().String(), verificationStatusString(report))
	assert.Equal(t, 2, verificationFailureCount(report))
	assert.Equal(t, []string{"missing witness", "digest mismatch"}, verificationFailureReasons(report))

	assert.Empty(t, verificationStatusString(nil))
	assert.Zero(t, verificationFailureCount(nil))
	assert.Nil(t, verificationFailureReasons(nil))
}

// An assignment has no independent wall-clock deadline unless the operator
// requests one. Parent cancellation and earlier parent deadlines still apply.
func TestAssignmentContext_NoImplicitDeadline(t *testing.T) {
	parent, cancelParent := context.WithCancel(t.Context())
	defer cancelParent()
	ctx, cancel := assignmentContext(parent, 0)
	defer cancel()
	_, hasDeadline := ctx.Deadline()
	assert.False(t, hasDeadline)
	require.NoError(t, ctx.Err())
	cancelParent()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestAssignmentContext_ExplicitTimeout(t *testing.T) {
	before := time.Now()
	ctx, cancel := assignmentContext(t.Context(), time.Minute)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.False(t, deadline.Before(before.Add(time.Minute)))
	assert.False(t, deadline.After(time.Now().Add(time.Minute)))
	cancel()
	require.ErrorIs(t, ctx.Err(), context.Canceled)
}

func TestAssignmentContext_PreservesParentDeadline(t *testing.T) {
	parent, cancelParent := context.WithTimeout(t.Context(), time.Minute)
	defer cancelParent()
	want, _ := parent.Deadline()
	for _, timeout := range []time.Duration{0, time.Hour} {
		ctx, cancel := assignmentContext(parent, timeout)
		got, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.Equal(t, want, got)
		cancel()
		require.NoError(t, parent.Err(), "ending an assignment must not cancel the run")
	}
}

func TestRunsFlags_AssignmentTimeoutFlag(t *testing.T) {
	startCmd := runsStartCmd(nativeEvalDeps{})
	resumeCmd := runsResumeCmd(nativeEvalDeps{})
	for _, cmd := range []*cobra.Command{startCmd, resumeCmd} {
		flag := cmd.Flags().Lookup("assignment-timeout")
		require.NotNil(t, flag)
		assert.Equal(t, "0s", flag.DefValue)
		assert.Contains(t, flag.Usage, "0 adds no assignment deadline")
	}
	assert.Contains(t, resumeCmd.Aliases, "execute")
}

func TestRunsRejectNegativeAssignmentTimeoutBeforeLoadingRuntime(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	_, err := runStartFlow(cmd, nativeEvalDeps{}, runStartFlowOptions{AssignmentTimeout: -time.Second})
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	_, err = executeRun(cmd, nativeEvalDeps{}, runExecuteOptions{AssignmentTimeout: -time.Second})
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
}

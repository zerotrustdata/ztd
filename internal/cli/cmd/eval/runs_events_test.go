// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/models"
)

func TestRunStopWatcher_StopEventRequestsWithoutCancelling(t *testing.T) {
	events := make(chan runEvent)
	hardCtx, hardCancel := context.WithCancel(context.Background())
	defer hardCancel()
	hooked := make(chan struct{}, 1)
	watcher := startRunStopWatcher(context.Background(), events, nil, hardCancel, nil, func() {
		hooked <- struct{}{}
	})
	defer watcher.Stop()

	assert.False(t, watcher.Requested())
	// A status event is not a stop request. Events are handled in order, so the
	// stop after it is handled once the status has been drained.
	events <- runEvent{Status: &models.RunStatusUpdatedPayload{Status: models.RunLifecycleStatusCancelled}}
	events <- runEvent{Stop: &models.EvalStopRequestedPayload{RunID: "run-a-1"}}
	<-hooked

	assert.True(t, watcher.Requested())
	assert.NoError(t, hardCtx.Err(), "a stop request lets the current assignment finish and never hard-cancels")
}

func TestReleasedRunStatus_ReportsWhereTheRunStands(t *testing.T) {
	tests := []struct {
		name          string
		stopRequested bool
		row           *runRow
		want          models.RunLifecycleStatus
	}{
		{name: "a stop request cancels the run", stopRequested: true, row: &runRow{Status: "interrupted", Queued: 1, Terminal: 1}, want: models.RunLifecycleStatusCancelled},
		{name: "a stopped run with nothing left is cancelled", row: &runRow{Status: "cancelled", Stopped: 2, Terminal: 2}, want: models.RunLifecycleStatusCancelled},
		{name: "queued assignments leave an errored run waiting for resume", row: &runRow{Status: "in_progress", Queued: 1, Terminal: 1}, want: models.RunLifecycleStatusWaiting},
		{name: "queued assignments outrank a failed verification", row: &runRow{Status: "verify_failed", Queued: 1, Terminal: 1}, want: models.RunLifecycleStatusWaiting},
		{name: "an interrupted run waits for resume", row: &runRow{Status: "interrupted", Running: 1, Terminal: 1}, want: models.RunLifecycleStatusWaiting},
		{name: "a finished run completes", row: &runRow{Status: "completed", Terminal: 2}, want: models.RunLifecycleStatusCompleted},
		{name: "a verified run completes", row: &runRow{Status: "verified", Terminal: 2}, want: models.RunLifecycleStatusCompleted},
		{name: "a run whose verification failed is failed", row: &runRow{Status: "verify_failed", Terminal: 2}, want: models.RunLifecycleStatusFailed},
		{name: "a run that was never scheduled waits", row: &runRow{Status: "initialized"}, want: models.RunLifecycleStatusWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, releasedRunStatus(tt.stopRequested, tt.row))
		})
	}
}

func TestRunStatusSettled_OnlyQueuedAndRunningAreStillInFlight(t *testing.T) {
	assert.False(t, runStatusSettled(models.RunLifecycleStatusQueued))
	assert.False(t, runStatusSettled(models.RunLifecycleStatusRunning))
	for _, status := range []models.RunLifecycleStatus{
		models.RunLifecycleStatusWaiting,
		models.RunLifecycleStatusCompleted,
		models.RunLifecycleStatusFailed,
		models.RunLifecycleStatusCancelled,
	} {
		assert.True(t, runStatusSettled(status), string(status))
	}
}

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
)

const otherHostPID = 9999

// rejectingRunStateReporter is a Gateway run projection that refuses every update.
type rejectingRunStateReporter struct {
	err error
}

func (r rejectingRunStateReporter) UpdateRunState(context.Context, models.ObserveProducerRunStateRequest) error {
	return r.err
}

func TestRunLifecycle_RejectedPostIsReturnedToTheCommand(t *testing.T) {
	lifecycle := runLifecycle{
		client:    rejectingRunStateReporter{err: fmt.Errorf("observe producer run-state: %w", constants.ErrHTTPStatusError)},
		deps:      nativeEvalDeps{now: func() time.Time { return time.Unix(1789657337, 0).UTC() }},
		runID:     "run-a-1",
		startedAt: time.Unix(1789657000, 0).UTC(),
	}

	err := lifecycle.post(context.Background(), models.RunLifecycleStatusRunning, &runRow{Queued: 1}, nil)
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	assert.Contains(t, err.Error(), "run-a-1")
	assert.Contains(t, err.Error(), string(models.RunLifecycleStatusRunning))
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

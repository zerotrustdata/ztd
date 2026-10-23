// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestObserveProducerRejection_EveryCodeRoundTripsItsSentinel(t *testing.T) {
	t.Parallel()
	seen := map[ObserveProducerRejectionCode]bool{}
	for _, entry := range observeProducerRejectionCodes {
		require.False(t, seen[entry.code], "code %s appears twice", entry.code)
		seen[entry.code] = true

		rejection := NewObserveProducerRejection(fmt.Errorf("op: %w", entry.sentinel))
		assert.Equal(t, entry.code, rejection.Code)
		data, err := json.Marshal(rejection)
		require.NoError(t, err)
		var decoded ObserveProducerRejection
		require.NoError(t, json.Unmarshal(data, &decoded))
		assert.ErrorIs(t, decoded.Cause(), entry.sentinel, string(entry.code))
	}
}

func TestObserveProducerRejection_TransitionCrossesTheWire(t *testing.T) {
	t.Parallel()
	refused := &ObserveTransitionError{
		Kind: ObserveProjectionKindRun, ID: "run-a",
		From: string(RunLifecycleStatusCancelled), To: string(RunLifecycleStatusRunning),
	}
	rejection := NewObserveProducerRejection(fmt.Errorf("observe producer: update run state: %w", refused))
	assert.Equal(t, ObserveProducerRejectionInvalidTransition, rejection.Code)
	assert.Contains(t, rejection.Message, "cancelled -> running")

	data, err := json.Marshal(rejection)
	require.NoError(t, err)
	var decoded ObserveProducerRejection
	require.NoError(t, json.Unmarshal(data, &decoded))
	var transition *ObserveTransitionError
	require.ErrorAs(t, decoded.Cause(), &transition)
	assert.Equal(t, refused, transition)
	assert.ErrorIs(t, decoded.Cause(), constants.ErrObserveInvalidTransition)
}

func TestObserveProducerRejection_StaleUpdateCrossesTheWire(t *testing.T) {
	t.Parallel()
	projected := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	refused := &ObserveStaleUpdateError{Kind: ObserveProjectionKindAgent, ID: "agent-a", ObservedAt: projected.Add(-time.Minute), ProjectedAt: projected}
	rejection := NewObserveProducerRejection(fmt.Errorf("observe producer: update agent state: %w", refused))
	assert.Equal(t, ObserveProducerRejectionStaleUpdate, rejection.Code)

	data, err := json.Marshal(rejection)
	require.NoError(t, err)
	var decoded ObserveProducerRejection
	require.NoError(t, json.Unmarshal(data, &decoded))
	var stale *ObserveStaleUpdateError
	require.ErrorAs(t, decoded.Cause(), &stale)
	assert.Equal(t, refused, stale)
}

func TestObserveProducerRejection_UnclassifiedIsInternalWithoutDetail(t *testing.T) {
	t.Parallel()
	rejection := NewObserveProducerRejection(fmt.Errorf("docstore write /secret/path: %w", errors.New("disk full")))
	assert.Equal(t, ObserveProducerRejectionInternal, rejection.Code)
	assert.Equal(t, constants.ErrInternal.Error(), rejection.Message)
}

func TestObserveProducerRejection_ForbiddenCarriesNoDetail(t *testing.T) {
	t.Parallel()
	rejection := NewObserveProducerRejection(constants.ErrForbidden)
	assert.Equal(t, ObserveProducerRejectionForbidden, rejection.Code)
	assert.Equal(t, constants.ErrForbidden.Error(), rejection.Message)
	assert.Nil(t, rejection.Transition)
	assert.Nil(t, rejection.Stale)
}

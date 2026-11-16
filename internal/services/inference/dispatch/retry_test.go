// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultTransientRetry_Delays(t *testing.T) {
	policy := DefaultTransientRetry()
	require.NotNil(t, policy.Wait)
	assert.Equal(t, uint32(3), MaxTransientRetries())

	_, ok := policy.Delay(0)
	assert.False(t, ok, "the first attempt is not a retry")
	for retry, want := range map[uint32]time.Duration{1: 5 * time.Second, 2: 20 * time.Second, 3: 40 * time.Second} {
		delay, ok := policy.Delay(retry)
		require.True(t, ok, "retry %d", retry)
		assert.Equal(t, want, delay, "retry %d", retry)
	}
	_, ok = policy.Delay(MaxTransientRetries() + 1)
	assert.False(t, ok, "the policy bounds the retries")
}

func TestWaitDelay_ReturnsContextErrorWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, WaitDelay(ctx, time.Hour), context.Canceled)
	require.NoError(t, WaitDelay(context.Background(), 0))
}

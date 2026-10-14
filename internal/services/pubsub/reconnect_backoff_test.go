// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package pubsub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNextReconnectDelay_ExponentialProgression(t *testing.T) {
	t.Parallel()
	base := 1 * time.Second
	max := 30 * time.Second

	delay := base
	expected := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // capped
		30 * time.Second, // still capped
	}
	for _, exp := range expected {
		delay = nextReconnectDelay(delay, max)
		assert.Equal(t, exp, delay)
	}
}

func TestNextReconnectDelay_CapsAtMax(t *testing.T) {
	t.Parallel()
	max := 30 * time.Second
	delay := nextReconnectDelay(20*time.Second, max)
	assert.Equal(t, max, delay, "20s*2=40s should cap at 30s")
}

func TestNextReconnectDelay_ExactDoubleBelowCap(t *testing.T) {
	t.Parallel()
	max := 30 * time.Second
	delay := nextReconnectDelay(8*time.Second, max)
	assert.Equal(t, 16*time.Second, delay, "8s*2=16s is below cap")
}

func TestWaitForReconnect_ContextCancellationInterruptsDelay(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.False(t, waitForReconnect(ctx, time.Hour))
}

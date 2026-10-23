// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContext_ExpiresAndCancelsWithTheTest(t *testing.T) {
	t.Parallel()

	canceled := make(chan error, 1)
	t.Run("scoped", func(t *testing.T) {
		ctx := Context(t)
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(DefaultTestTimeout), deadline, time.Second)
		assert.NoError(t, ctx.Err())
		go func() {
			<-ctx.Done()
			canceled <- ctx.Err()
		}()
	})

	select {
	case err := <-canceled:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("context was not canceled when the subtest returned")
	}
}

func TestContextWithTimeout_UsesTheRequestedBudget(t *testing.T) {
	t.Parallel()

	ctx := ContextWithTimeout(t, 2*time.Second)
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(2*time.Second), deadline, 200*time.Millisecond)
}

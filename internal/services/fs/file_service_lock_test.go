// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestTryLock_ExcludesASecondHolderUntilClosed(t *testing.T) {
	svc := setupTestFS(t)
	ctx := context.Background()

	held, err := svc.TryLock(ctx, "nested/run.lock", constants.PermFilePrivate)
	require.NoError(t, err)
	exists, err := svc.FileExists(ctx, "nested/run.lock")
	require.NoError(t, err)
	assert.True(t, exists)

	_, err = svc.TryLock(ctx, "nested/run.lock", constants.PermFilePrivate)
	assert.ErrorIs(t, err, constants.ErrRuntimeFileLocked)

	require.NoError(t, held.Close())
	again, err := svc.TryLock(ctx, "nested/run.lock", constants.PermFilePrivate)
	require.NoError(t, err, "a lock file left on disk does not block the next holder")
	require.NoError(t, again.Close())
}

func TestTryLock_CancelledContextReturnsContextError(t *testing.T) {
	svc := setupTestFS(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := svc.TryLock(ctx, "run.lock", constants.PermFilePrivate)
	assert.ErrorIs(t, err, context.Canceled)
}

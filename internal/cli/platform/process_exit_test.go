// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A live process that is never asked to exit keeps the wait open, so the
// context is what ends it.
func TestWaitForProcessExit_ContextEndsTheWaitForALiveProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, WaitForProcessExit(ctx, os.Getpid()), context.DeadlineExceeded)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"

	"github.com/zerotrustdata/ztd/internal/pkg/processwatch"
)

// ProcessExitWatch observes one process exit without polling.
type ProcessExitWatch = processwatch.ProcessExitWatch

// WatchProcessExit opens an exit watch on pid.
func WatchProcessExit(pid int) (*ProcessExitWatch, error) {
	return processwatch.WatchProcessExit(pid)
}

// WaitForProcessExit blocks until the process with pid has exited and returns nil.
func WaitForProcessExit(ctx context.Context, pid int) error {
	return processwatch.WaitForProcessExit(ctx, pid)
}

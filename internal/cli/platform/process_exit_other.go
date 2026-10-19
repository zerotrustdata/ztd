//go:build !linux && !windows && !darwin

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
	"syscall"
	"time"
)

// WaitForProcessExit blocks until the process with pid has exited and returns
// nil, or until ctx ends and returns ctx.Err(). This platform has no exit
// notification the stop code uses, so the process is checked at a fixed
// interval.
func WaitForProcessExit(ctx context.Context, pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.Signal(syscall.Signal(0)); err != nil {
				return nil
			}
		}
	}
}

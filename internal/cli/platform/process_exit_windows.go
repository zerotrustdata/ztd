//go:build windows

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"golang.org/x/sys/windows"
)

// ProcessExitWatch observes one process through a SYNCHRONIZE handle. The handle
// pins the process object, so the PID cannot be reused while the watch is open.
type ProcessExitWatch struct {
	pid    int
	handle windows.Handle
}

// WatchProcessExit opens an exit watch on pid. It returns an error wrapping
// constants.ErrProcessNotFound when no process has that PID.
func WatchProcessExit(pid int) (*ProcessExitWatch, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, fmt.Errorf("platform: watch PID %d exit: %w", pid, constants.ErrProcessNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("platform: open PID %d: %w", pid, err)
	}
	return &ProcessExitWatch{pid: pid, handle: handle}, nil
}

// Wait blocks until the watched process has exited and returns nil. It returns
// ctx.Err() when ctx ends first, and an error when the exit cannot be observed.
// The wait is WaitForMultipleObjects on the process handle and a manual-reset
// event that ctx sets. No timer runs.
func (w *ProcessExitWatch) Wait(ctx context.Context) error {
	wake, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("platform: create wake event: %w", err)
	}
	defer func() { _ = windows.CloseHandle(wake) }()

	// The waker sets the event only while this call waits. The waker ends before
	// the event closes.
	done := make(chan struct{})
	var waker sync.WaitGroup
	waker.Add(1)
	go func() {
		defer waker.Done()
		select {
		case <-ctx.Done():
			_ = windows.SetEvent(wake)
		case <-done:
		}
	}()

	event, err := windows.WaitForMultipleObjects([]windows.Handle{w.handle, wake}, false, windows.INFINITE)
	close(done)
	waker.Wait()
	if err != nil {
		return fmt.Errorf("platform: wait for PID %d: %w", w.pid, err)
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		return nil
	case windows.WAIT_OBJECT_0 + 1:
		return ctx.Err()
	default:
		return fmt.Errorf("platform: wait for PID %d: unexpected wait result %d", w.pid, event)
	}
}

// Close releases the process handle. The watch must not be waited on afterwards.
func (w *ProcessExitWatch) Close() error {
	return windows.CloseHandle(w.handle)
}

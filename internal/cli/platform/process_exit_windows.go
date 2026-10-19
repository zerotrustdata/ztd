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

	"golang.org/x/sys/windows"
)

// WaitForProcessExit blocks until the process with pid has exited and returns
// nil. It returns ctx.Err() when ctx ends first, and an error when the exit
// cannot be observed. A process that is already gone returns nil at once.
//
// The wait is WaitForMultipleObjects on the process handle and a manual-reset
// event that ctx sets. No timer runs.
func WaitForProcessExit(ctx context.Context, pid int) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("platform: open PID %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	wake, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("platform: create wake event: %w", err)
	}
	defer func() { _ = windows.CloseHandle(wake) }()

	// The waker sets the event only while this call waits. The waker ends before
	// the handles close.
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

	event, err := windows.WaitForMultipleObjects([]windows.Handle{handle, wake}, false, windows.INFINITE)
	close(done)
	waker.Wait()
	if err != nil {
		return fmt.Errorf("platform: wait for PID %d: %w", pid, err)
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		return nil
	case windows.WAIT_OBJECT_0 + 1:
		return ctx.Err()
	default:
		return fmt.Errorf("platform: wait for PID %d: unexpected wait result %d", pid, event)
	}
}

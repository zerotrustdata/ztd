//go:build linux

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"golang.org/x/sys/unix"
)

// ProcessExitWatch observes one process through a pidfd. The pidfd refers to the
// process that held the PID when the watch opened, so a PID reused later does
// not redirect the wait.
type ProcessExitWatch struct {
	pid   int
	pidfd int
}

// WatchProcessExit opens an exit watch on pid. It returns an error wrapping
// constants.ErrProcessNotFound when no process has that PID.
func WatchProcessExit(pid int) (*ProcessExitWatch, error) {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil, fmt.Errorf("platform: watch PID %d exit: %w", pid, constants.ErrProcessNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("platform: open pidfd for PID %d: %w", pid, err)
	}
	return &ProcessExitWatch{pid: pid, pidfd: pidfd}, nil
}

// Wait blocks until the watched process has exited and returns nil. It returns
// ctx.Err() when ctx ends first, and an error when the exit cannot be observed.
// The wait is one blocking poll(2) on the pidfd and an eventfd that ctx ends by
// writing to. No timer or sleep runs.
func (w *ProcessExitWatch) Wait(ctx context.Context) error {
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("platform: create wake fd: %w", err)
	}
	defer func() { _ = unix.Close(wake) }()

	// The waker writes to the eventfd only while this call waits. The waker ends
	// before the eventfd closes, so a write can never reach a reused descriptor.
	done := make(chan struct{})
	var waker sync.WaitGroup
	waker.Add(1)
	go func() {
		defer waker.Done()
		select {
		case <-ctx.Done():
			var one [8]byte
			binary.NativeEndian.PutUint64(one[:], 1)
			_, _ = unix.Write(wake, one[:])
		case <-done:
		}
	}()

	fds := []unix.PollFd{{Fd: int32(w.pidfd), Events: unix.POLLIN}, {Fd: int32(wake), Events: unix.POLLIN}}
	for {
		_, err = unix.Poll(fds, -1)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		break
	}
	close(done)
	waker.Wait()
	if err != nil {
		return fmt.Errorf("platform: wait for PID %d: %w", w.pid, err)
	}
	if fds[0].Revents != 0 {
		return nil
	}
	return ctx.Err()
}

// Close releases the pidfd. The watch must not be waited on afterwards.
func (w *ProcessExitWatch) Close() error {
	return unix.Close(w.pidfd)
}

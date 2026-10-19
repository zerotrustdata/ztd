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

	"golang.org/x/sys/unix"
)

// WaitForProcessExit blocks until the process with pid has exited and returns
// nil. It returns ctx.Err() when ctx ends first, and an error when the exit
// cannot be observed. A process that is already gone returns nil at once.
//
// The kernel reports the exit on a pidfd, which refers to the process that held
// pid when the wait began, so a PID reused later does not end the wait. The wait
// is one blocking poll(2) on the pidfd and an eventfd that ctx ends by writing
// to. No timer or sleep runs.
func WaitForProcessExit(ctx context.Context, pid int) error {
	pidfd, err := unix.PidfdOpen(pid, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("platform: open pidfd for PID %d: %w", pid, err)
	}
	defer func() { _ = unix.Close(pidfd) }()
	wake, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	if err != nil {
		return fmt.Errorf("platform: create wake fd: %w", err)
	}
	defer func() { _ = unix.Close(wake) }()

	// The waker writes to the eventfd only while this call waits. The waker ends
	// before the fds close, so a write can never reach a reused descriptor.
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

	fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}, {Fd: int32(wake), Events: unix.POLLIN}}
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
		return fmt.Errorf("platform: wait for PID %d: %w", pid, err)
	}
	if fds[0].Revents != 0 {
		return nil
	}
	return ctx.Err()
}

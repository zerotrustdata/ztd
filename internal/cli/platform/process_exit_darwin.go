//go:build darwin

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

	"golang.org/x/sys/unix"
)

// exitWakeIdent names the user event that ends a WaitForProcessExit when its
// context does.
const exitWakeIdent = 1

// WaitForProcessExit blocks until the process with pid has exited and returns
// nil. It returns ctx.Err() when ctx ends first, and an error when the exit
// cannot be observed. A process that is already gone returns nil at once.
//
// The exit is a kqueue NOTE_EXIT registered on the PID. Registration happens
// before the call blocks, so an exit between the caller's check and the wait is
// still reported. A user event that ctx triggers ends the wait. No timer runs.
func WaitForProcessExit(ctx context.Context, pid int) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return fmt.Errorf("platform: create kqueue: %w", err)
	}
	defer func() { _ = unix.Close(kq) }()

	var wake unix.Kevent_t
	unix.SetKevent(&wake, exitWakeIdent, unix.EVFILT_USER, unix.EV_ADD|unix.EV_CLEAR)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{wake}, nil, nil); err != nil {
		return fmt.Errorf("platform: register wake event: %w", err)
	}
	var exit unix.Kevent_t
	unix.SetKevent(&exit, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	exit.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{exit}, nil, nil); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return fmt.Errorf("platform: watch PID %d exit: %w", pid, err)
	}

	// The waker triggers the user event only while this call waits. The waker
	// ends before the kqueue closes.
	done := make(chan struct{})
	var waker sync.WaitGroup
	waker.Add(1)
	go func() {
		defer waker.Done()
		select {
		case <-ctx.Done():
			var trigger unix.Kevent_t
			unix.SetKevent(&trigger, exitWakeIdent, unix.EVFILT_USER, 0)
			trigger.Fflags = unix.NOTE_TRIGGER
			_, _ = unix.Kevent(kq, []unix.Kevent_t{trigger}, nil, nil)
		case <-done:
		}
	}()

	events := make([]unix.Kevent_t, 1)
	var n int
	for {
		n, err = unix.Kevent(kq, nil, events, nil)
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
	if n > 0 && events[0].Filter == unix.EVFILT_PROC {
		return nil
	}
	return ctx.Err()
}

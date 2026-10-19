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

	"github.com/zerotrustdata/ztd/internal/constants"
	"golang.org/x/sys/unix"
)

// exitWakeIdent names the user event that ends a Wait when its context does.
const exitWakeIdent = 1

// ProcessExitWatch observes one process through a kqueue NOTE_EXIT registered on
// its PID. Registration happens in WatchProcessExit, before the caller checks or
// waits, so an exit in between is still reported.
type ProcessExitWatch struct {
	pid    int
	kq     int
	exited bool
}

// WatchProcessExit opens an exit watch on pid. It returns an error wrapping
// constants.ErrProcessNotFound when no process has that PID.
func WatchProcessExit(pid int) (*ProcessExitWatch, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("platform: create kqueue: %w", err)
	}
	var wake unix.Kevent_t
	unix.SetKevent(&wake, exitWakeIdent, unix.EVFILT_USER, unix.EV_ADD|unix.EV_CLEAR)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{wake}, nil, nil); err != nil {
		_ = unix.Close(kq)
		return nil, fmt.Errorf("platform: register wake event: %w", err)
	}
	var exit unix.Kevent_t
	unix.SetKevent(&exit, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	exit.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{exit}, nil, nil); err != nil {
		_ = unix.Close(kq)
		if errors.Is(err, unix.ESRCH) {
			return nil, fmt.Errorf("platform: watch PID %d exit: %w", pid, constants.ErrProcessNotFound)
		}
		return nil, fmt.Errorf("platform: watch PID %d exit: %w", pid, err)
	}
	return &ProcessExitWatch{pid: pid, kq: kq}, nil
}

// Wait blocks until the watched process has exited and returns nil. It returns
// ctx.Err() when ctx ends first, and an error when the exit cannot be observed.
// A user event that ctx triggers ends the wait. No timer runs.
//
// NOTE_EXIT is one-shot, so the first exit reported is recorded and later calls
// return nil without reading the kqueue again.
func (w *ProcessExitWatch) Wait(ctx context.Context) error {
	if w.exited {
		return nil
	}

	// The waker triggers the user event only while this call waits, and ends
	// before Wait returns. A trigger that a call returns past, because the exit
	// was read first, stays pending. The next call reads it as stale and keeps
	// waiting while its own context is live.
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
			_, _ = unix.Kevent(w.kq, []unix.Kevent_t{trigger}, nil, nil)
		case <-done:
		}
	}()

	// Two slots let an exit that is already queued be read alongside a wake, so
	// the exit is never hidden behind a trigger.
	events := make([]unix.Kevent_t, 2)
	var err error
	for {
		var n int
		n, err = unix.Kevent(w.kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			break
		}
		for _, ev := range events[:n] {
			if ev.Filter == unix.EVFILT_PROC {
				w.exited = true
			}
		}
		if w.exited || ctx.Err() != nil {
			break
		}
	}
	close(done)
	waker.Wait()
	if err != nil {
		return fmt.Errorf("platform: wait for PID %d: %w", w.pid, err)
	}
	if w.exited {
		return nil
	}
	return ctx.Err()
}

// Close releases the kqueue. The watch must not be waited on afterwards.
func (w *ProcessExitWatch) Close() error {
	return unix.Close(w.kq)
}

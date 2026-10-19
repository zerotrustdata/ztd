// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// processControl is the process boundary a run lease depends on: who this
// process is, whether another is alive, how to ask it to stop, and how to learn
// that it has exited.
type processControl interface {
	PID() int
	Hostname() (string, error)
	Alive(pid int) bool
	Interrupt(pid int) error
	// WaitExit blocks until the process with pid has exited, returning nil, or
	// until ctx ends, returning ctx.Err().
	WaitExit(ctx context.Context, pid int) error
}

// runControlDeps carries the process boundary and the test hooks used by
// commands that start, follow, or stop a run. Every run event arrives on the
// Gateway stream, so no hook sets a cadence.
type runControlDeps struct {
	processFactory func(fs.RuntimeFileService) (processControl, error)
	// notifyInterrupts registers for interrupt signals and returns the channel
	// and the function that unregisters it.
	notifyInterrupts func() (<-chan os.Signal, func())
	// onSignalHandled is an optional test hook called after an interrupt signal
	// is registered by the stop watcher.
	onSignalHandled func()
	// onStopRequested is an optional test hook called after the stop watcher
	// records a stop request from the event stream or from the run's lease.
	onStopRequested func()
	// onLeaseAcquired is an optional test hook called once a run's lease is held,
	// before the process subscribes to the run's events.
	onLeaseAcquired func()
}

// notifyProcessInterrupts registers for SIGINT and SIGTERM.
func notifyProcessInterrupts() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	return signals, func() { signal.Stop(signals) }
}

func defaultRunControlDeps() runControlDeps {
	return runControlDeps{
		processFactory:   newOSProcessControl,
		notifyInterrupts: notifyProcessInterrupts,
	}
}

func (d runControlDeps) interrupts() (<-chan os.Signal, func()) {
	if d.notifyInterrupts == nil {
		return nil, func() {}
	}
	return d.notifyInterrupts()
}

func (d runControlDeps) process(fileSvc fs.RuntimeFileService) (processControl, error) {
	if d.processFactory == nil {
		return nil, fmt.Errorf("evaluation: run control: %w", constants.ErrMissingRequiredField)
	}
	return d.processFactory(fileSvc)
}

type osProcessControl struct {
	manager *platform.ProcessManager
}

func newOSProcessControl(fileSvc fs.RuntimeFileService) (processControl, error) {
	manager, err := platform.NewProcessManager(fileSvc)
	if err != nil {
		return nil, fmt.Errorf("evaluation: run control: %w", err)
	}
	return osProcessControl{manager: manager}, nil
}

func (p osProcessControl) PID() int { return os.Getpid() }

func (p osProcessControl) Hostname() (string, error) { return os.Hostname() }

func (p osProcessControl) Alive(pid int) bool { return p.manager.IsProcessRunning(pid) }

// Interrupt asks the process to stop after its current assignment. Platforms
// that cannot deliver the signal rely on the cancel request in the lease.
func (p osProcessControl) Interrupt(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}

// WaitExit waits on the operating system's exit notification for pid.
func (p osProcessControl) WaitExit(ctx context.Context, pid int) error {
	return platform.WaitForProcessExit(ctx, pid)
}

// leaseLiveness reports a lease as live only when this host holds it and its
// process is running. A lease from another host, or whose process is gone, is
// stale.
func leaseLiveness(control processControl) evaluation.LeaseLiveness {
	host, hostErr := control.Hostname()
	return func(lease evaluation.RunLease) bool {
		if hostErr != nil || lease.Host != host {
			return false
		}
		return control.Alive(lease.PID)
	}
}

// watchHolderExit reports on the returned channel when the process holding a
// run's lease exits: nil on exit, or the error that stopped the wait. The
// channel receives nothing when ctx ends first, so a caller that cancels ctx
// after it stops waiting leaves no goroutine behind.
func watchHolderExit(ctx context.Context, control processControl, pid int) <-chan error {
	exited := make(chan error, 1)
	go func() {
		err := control.WaitExit(ctx, pid)
		if err != nil && ctx.Err() != nil {
			return
		}
		exited <- err
	}()
	return exited
}

// runStopWatcher observes the two ways an executing run is asked to stop: a stop
// event on the run's event stream, which the lease reconcile on every connection
// also feeds, and an interrupt signal. The first request lets the current
// assignment finish. A second signal cancels the running assignment through the
// context.
type runStopWatcher struct {
	requested       atomic.Bool
	cancel          context.CancelFunc
	onStopRequested func()
	wg              sync.WaitGroup
}

// startRunStopWatcher starts the watcher goroutine. It drains events from the
// run's stream and records a stop event as a request. hardCancel is called on a
// second interrupt. The caller owns the watcher and must call Stop.
func startRunStopWatcher(ctx context.Context, events <-chan runEvent, signals <-chan os.Signal, hardCancel context.CancelFunc, onSignalHandled, onStopRequested func()) *runStopWatcher {
	watchCtx, cancel := context.WithCancel(ctx)
	watcher := &runStopWatcher{cancel: cancel, onStopRequested: onStopRequested}
	watcher.wg.Add(1)
	go func() {
		defer watcher.wg.Done()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-signals:
				isSecond := watcher.requested.Swap(true)
				if onSignalHandled != nil {
					onSignalHandled()
				}
				if isSecond {
					hardCancel()
					return
				}
			case ev := <-events:
				if ev.Stop != nil {
					watcher.requestStop()
				}
			}
		}
	}()
	return watcher
}

// requestStop records a stop request. It never cancels the running assignment:
// the assignment finishes, and the run stops before the next one.
func (w *runStopWatcher) requestStop() {
	w.requested.Store(true)
	if w.onStopRequested != nil {
		w.onStopRequested()
	}
}

// Requested reports whether the run was asked to stop.
func (w *runStopWatcher) Requested() bool { return w != nil && w.requested.Load() }

// Stop ends the watcher and waits for its goroutine.
func (w *runStopWatcher) Stop() {
	if w == nil {
		return
	}
	w.cancel()
	w.wg.Wait()
}

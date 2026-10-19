// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !windows
// +build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// processFinder is an interface for finding processes
type processFinder interface {
	FindProcess(pid int) (process, error)
}

// process is an interface for process operations
type process interface {
	Signal(sig syscall.Signal) error
}

// osProcess wraps os.Process to implement the process interface
type osProcess struct {
	*os.Process
}

func (p *osProcess) Signal(sig syscall.Signal) error {
	return p.Process.Signal(sig)
}

// osProcessFinder wraps os.FindProcess to implement processFinder
type osProcessFinder struct{}

func (f osProcessFinder) FindProcess(pid int) (process, error) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	return &osProcess{p}, nil
}

// realCommandExecutor implements CommandExecutor using actual exec package
type realCommandExecutor struct{}

func (c realCommandExecutor) Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func (c realCommandExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	return cmd.Output()
}

func (c realCommandExecutor) Run(cmd *exec.Cmd) error {
	return cmd.Run()
}

// exitWatcher is the exit observation stopProcessWithDeps waits on.
// *ProcessExitWatch satisfies it; tests inject a fake.
type exitWatcher interface {
	Wait(ctx context.Context) error
	Close() error
}

// exitWatchOpener opens an exit watch on a PID (for testing)
type exitWatchOpener interface {
	Open(pid int) (exitWatcher, error)
}

// osExitWatchOpener wraps WatchProcessExit to implement exitWatchOpener
type osExitWatchOpener struct{}

func (osExitWatchOpener) Open(pid int) (exitWatcher, error) {
	watch, err := WatchProcessExit(pid)
	if err != nil {
		return nil, err
	}
	return watch, nil
}

// setProcessGroup sets the process group for Unix systems
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
}

// isProcessRunning checks if a process with the given PID is running on Unix systems.
// It uses syscall.Signal(0) which doesn't actually send a signal but checks if the process exists.
func (pm *ProcessManager) isProcessRunning(pid int) bool {
	return pm.isProcessRunningWithFinder(pid, osProcessFinder{})
}

// isProcessRunningWithFinder checks if a process is running using a provided processFinder (for testing)
func (pm *ProcessManager) isProcessRunningWithFinder(pid int, finder processFinder) bool {
	if pid == 0 {
		return false
	}

	proc, err := finder.FindProcess(pid)
	if err != nil {
		return false
	}

	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// findProcessOnPort finds the PID of the process listening on the given port on Unix systems.
// It uses lsof to find the process ID.
func (pm *ProcessManager) findProcessOnPort(port int) int {
	return pm.findProcessOnPortWithFactory(port, realCommandExecutor{})
}

// findProcessOnPortWithFactory finds the PID using a provided CommandExecutor (for testing)
func (pm *ProcessManager) findProcessOnPortWithFactory(port int, executor CommandExecutor) int {
	cmd := executor.Command("lsof", "-ti", fmt.Sprintf(":%d", port))
	output, err := executor.Output(cmd)
	if err != nil {
		return 0
	}

	var pid int
	if _, err := fmt.Sscanf(string(output), "%d", &pid); err != nil {
		return 0
	}

	return pid
}

// findOperatorProcess finds the PID of the running ztd operator process using pgrep.
// This is used as a fallback when the PID file is missing or stale.
func (pm *ProcessManager) findOperatorProcess() int {
	return pm.findOperatorProcessWithExecutor(realCommandExecutor{})
}

// findOperatorProcessWithExecutor finds the PID using a provided CommandExecutor (for testing).
// The pattern is anchored to the executable so a shell or wrapper whose
// command line merely mentions `ztd gw start` never matches, and the calling
// process is skipped so a `gw start --data-dir <dir>` never finds itself.
func (pm *ProcessManager) findOperatorProcessWithExecutor(executor CommandExecutor) int {
	pattern := fmt.Sprintf("^[^[:space:]]*ztd gw start.*--data-dir %s([[:space:]]|$)", pm.fileSvc.Resolve(constants.DataDirname))
	cmd := executor.Command("pgrep", "-f", pattern)
	output, err := executor.Output(cmd)
	if err != nil {
		return 0
	}

	self := os.Getpid()
	for _, line := range strings.Fields(string(output)) {
		pid, err := strconv.Atoi(line)
		if err != nil {
			return 0
		}
		if pid != self {
			return pid
		}
	}
	return 0
}

// stopProcess stops a process with the given PID on Unix systems.
// It sends SIGTERM first, then SIGKILL if the process doesn't exit within the timeout.
func (pm *ProcessManager) stopProcess(pid int, name string) error {
	_ = name
	return pm.stopProcessWithDeps(pid, osProcessFinder{}, osExitWatchOpener{}, ShutdownTimeout)
}

// stopProcessWithDeps stops a process using injected dependencies (for testing).
// The exit watch opens before SIGTERM, so an exit between the signal and the
// wait is still observed. A PID with no process has no watch and is already gone.
func (pm *ProcessManager) stopProcessWithDeps(pid int, finder processFinder, exits exitWatchOpener, timeoutDur time.Duration) error {
	if pid == 0 {
		return nil
	}

	watch, err := exits.Open(pid)
	if errors.Is(err, constants.ErrProcessNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("process_manager: watch process exit: %w", err)
	}
	defer func() { _ = watch.Close() }()

	proc, err := finder.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("process_manager: find process: %w", err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("process_manager: send SIGTERM: %w", err)
	}

	exited, err := waitExit(watch, timeoutDur)
	if err != nil {
		return fmt.Errorf("process_manager: wait for process exit: %w", err)
	}
	if exited {
		return nil
	}

	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("process_manager: send SIGKILL: %w", err)
	}
	exited, err = waitExit(watch, SigKillExitTimeout)
	if err != nil {
		return fmt.Errorf("process_manager: wait for process exit: %w", err)
	}
	if !exited {
		return fmt.Errorf("process_manager: %w (pid %d)", constants.ErrProcessSigKillTimeout, pid)
	}
	return nil
}

// waitExit waits up to d for watch to report the process exit. It returns
// exited false when d passes first, and an error only when the exit cannot be
// observed.
func waitExit(watch exitWatcher, d time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	err := watch.Wait(ctx)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, context.DeadlineExceeded):
		return false, nil
	default:
		return false, err
	}
}

//go:build linux

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"golang.org/x/sys/unix"
)

// Hold pidfds before inspecting identity so PID reuse cannot redirect a signal.
// Inspect argv tokens, never a substring of a shell command or a parent PID.
func discoverLocalOperators() ([]localOperatorProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local workers: %w", err)
	}
	var processes []localOperatorProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			continue
		}
		// Avoid requiring pidfd support for unrelated processes.
		raw, err := os.ReadFile(filepath.Join(root, "cmdline"))
		if err != nil || !isLocalOperatorArgv(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")) {
			continue
		}
		// The pidfd is the signal handle and the watch observes the exit. Both are
		// held before identity is inspected.
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			for _, p := range processes {
				p.close()
			}
			return nil, fmt.Errorf("operator stop: open PID %d: %w", pid, err)
		}
		watch, err := platform.WatchProcessExit(pid)
		if errors.Is(err, constants.ErrProcessNotFound) {
			_ = unix.Close(fd)
			continue
		}
		if err != nil {
			_ = unix.Close(fd)
			for _, p := range processes {
				p.close()
			}
			return nil, fmt.Errorf("operator stop: watch PID %d: %w", pid, err)
		}
		raw, err = os.ReadFile(filepath.Join(root, "cmdline"))
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		executable, exeErr := os.Readlink(filepath.Join(root, "exe"))
		if err != nil || exeErr != nil || !isLocalOperatorArgv(args) || !isZtdExecutable(filepath.Base(strings.TrimSuffix(executable, " (deleted)"))) {
			_ = unix.Close(fd)
			_ = watch.Close()
			continue
		}
		cwd, _ := os.Readlink(filepath.Join(root, "cwd"))
		dir := localOperatorDir(args, cwd)
		processes = append(processes, localOperatorProcess{
			pid: pid, dir: dir, sessionID: localOperatorSessionID(dir),
			close: func() {
				_ = unix.Close(fd)
				_ = watch.Close()
			},
			signal: func(force bool) error {
				sig := unix.SIGTERM
				if force {
					sig = unix.SIGKILL
				}
				err := unix.PidfdSendSignal(fd, sig, nil, 0)
				if errors.Is(err, unix.ESRCH) {
					return nil
				}
				return err
			},
			wait: exitWait(watch),
		})
	}
	return processes, nil
}

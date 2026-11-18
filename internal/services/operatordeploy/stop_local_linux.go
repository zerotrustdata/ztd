//go:build linux

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/processwatch"
	"golang.org/x/sys/unix"
)

// DiscoverLocalOperators holds pidfds before inspecting identity so PID reuse
// cannot redirect a signal. Inspects argv tokens, never a shell substring.
func DiscoverLocalOperators() ([]LocalOperatorProcess, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local Operators: %w", err)
	}
	var processes []LocalOperatorProcess
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
		raw, err := os.ReadFile(filepath.Join(root, "cmdline"))
		if err != nil || !IsLocalOperatorArgv(strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")) {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			for _, p := range processes {
				p.Close()
			}
			return nil, fmt.Errorf("operator stop: open PID %d: %w", pid, err)
		}
		watch, err := processwatch.WatchProcessExit(pid)
		if errors.Is(err, constants.ErrProcessNotFound) {
			_ = unix.Close(fd)
			continue
		}
		if err != nil {
			_ = unix.Close(fd)
			for _, p := range processes {
				p.Close()
			}
			return nil, fmt.Errorf("operator stop: watch PID %d: %w", pid, err)
		}
		raw, err = os.ReadFile(filepath.Join(root, "cmdline"))
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		executable, exeErr := os.Readlink(filepath.Join(root, "exe"))
		if err != nil || exeErr != nil || !IsLocalOperatorArgv(args) || !IsZtdExecutable(filepath.Base(strings.TrimSuffix(executable, " (deleted)"))) {
			_ = unix.Close(fd)
			_ = watch.Close()
			continue
		}
		cwd, _ := os.Readlink(filepath.Join(root, "cwd"))
		dir := LocalOperatorDir(args, cwd)
		processes = append(processes, LocalOperatorProcess{
			PID: pid, Dir: dir, SessionID: LocalOperatorSessionID(dir),
			Close: func() {
				_ = unix.Close(fd)
				_ = watch.Close()
			},
			Signal: func(force bool) error {
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
			Wait: ExitWait(watch),
		})
	}
	return processes, nil
}

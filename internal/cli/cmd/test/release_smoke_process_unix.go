// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build unix

package testcmd

import (
	"errors"
	"os/exec"
	"syscall"
)

// smokeProcessGroup is the process group an owned process leads.
type smokeProcessGroup struct {
	pgid int
}

func smokeProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func newSmokeProcessGroup(cmd *exec.Cmd) (smokeProcessGroup, error) {
	return smokeProcessGroup{pgid: cmd.Process.Pid}, nil
}

// terminate sends SIGTERM to the group.
func (g smokeProcessGroup) terminate() error { return g.signal(syscall.SIGTERM) }

// kill sends SIGKILL to the group.
func (g smokeProcessGroup) kill() error { return g.signal(syscall.SIGKILL) }

func (g smokeProcessGroup) signal(sig syscall.Signal) error {
	if err := syscall.Kill(-g.pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (g smokeProcessGroup) exists() bool {
	err := syscall.Kill(-g.pgid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build unix || windows

package testcmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/config"
)

// smokeProcessOwner tracks the OS processes the release smoke runner starts
// itself. Each process leads its own process group (a Job Object on Windows),
// so stopping it reaches the whole tree and never a process the runner did not
// start.
type smokeProcessOwner struct {
	mu    sync.Mutex
	procs []*smokeOwnedProcess
}

// smokeOwnedProcess is one child started by the runner.
type smokeOwnedProcess struct {
	name       string
	cmd        *exec.Cmd
	group      smokeProcessGroup
	stdoutPath string
	exited     chan struct{}
	waitErr    error
	// logs are the runner's stdout/stderr handles. They stay open until the
	// whole group has exited and the child is reaped (closeLogs), so Windows
	// never sees them released while a process in the job still runs.
	logs      []*os.File
	closeOnce sync.Once
}

// start starts argv as a new process group in dir. Stdout and stderr are
// written to <logPrefix>.stdout.log and <logPrefix>.stderr.log, created with
// owner-only permissions, so command output stays separable and inspectable.
func (o *smokeProcessOwner) start(name, dir, logPrefix string, argv, env []string) (*smokeOwnedProcess, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("release smoke: empty argv for %s", name)
	}
	if err := os.MkdirAll(filepath.Dir(logPrefix), 0o700); err != nil {
		return nil, fmt.Errorf("release smoke: create log dir: %w", err)
	}
	stdoutPath := logPrefix + ".stdout.log"
	stderrPath := logPrefix + ".stderr.log"
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("release smoke: open log %s: %w", stdoutPath, err)
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdout.Close()
		return nil, fmt.Errorf("release smoke: open log %s: %w", stderrPath, err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(config.WithoutUserEnv(os.Environ()), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = smokeProcessGroupAttr()
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("release smoke: start %s: %w", name, err)
	}
	group, err := newSmokeProcessGroup(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, fmt.Errorf("release smoke: contain %s: %w", name, err)
	}
	p := &smokeOwnedProcess{
		name: name, cmd: cmd, group: group, stdoutPath: stdoutPath,
		exited: make(chan struct{}), logs: []*os.File{stdout, stderr},
	}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()
	o.mu.Lock()
	o.procs = append(o.procs, p)
	o.mu.Unlock()
	return p, nil
}

// exitedCh is closed once the process has exited and been reaped.
func (p *smokeOwnedProcess) exitedCh() <-chan struct{} { return p.exited }

// closeLogs releases the runner's log handles. Call it only after the group
// has exited and the child has been reaped.
func (p *smokeOwnedProcess) closeLogs() {
	p.closeOnce.Do(func() {
		for _, f := range p.logs {
			_ = f.Close()
		}
	})
}

// wait blocks until the owned process exits or ctx ends. When ctx ends first
// the process group is stopped before the error is returned, so cancellation
// never leaves an owned child behind. When the process exits and nothing else
// remains in its group, the log handles are released immediately; a group that
// outlives its leader (gw start leaving a Gateway) keeps them until stop.
func (o *smokeProcessOwner) wait(ctx context.Context, p *smokeOwnedProcess) error {
	select {
	case <-p.exited:
		if !p.group.exists() {
			p.closeLogs()
		}
		if p.waitErr != nil {
			return fmt.Errorf("release smoke: %s exited: %w", p.name, p.waitErr)
		}
		return nil
	case <-ctx.Done():
		if err := o.stop(p, 5*time.Second); err != nil {
			return errors.Join(ctx.Err(), err)
		}
		return ctx.Err()
	}
}

// stop asks the process group to terminate, waits up to grace, then kills it.
// Once the group is gone and the child reaped it closes the log handles. It is
// idempotent for a process that has already exited.
func (o *smokeProcessOwner) stop(p *smokeOwnedProcess, grace time.Duration) error {
	if err := o.stopGroup(p, grace); err != nil {
		return err
	}
	select {
	case <-p.exited:
	case <-time.After(grace):
		return fmt.Errorf("release smoke: %s: process not reaped after group exit", p.name)
	}
	p.closeLogs()
	return nil
}

func (o *smokeProcessOwner) stopGroup(p *smokeOwnedProcess, grace time.Duration) error {
	if err := p.group.terminate(); err != nil {
		return fmt.Errorf("release smoke: %s: terminate: %w", p.name, err)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) && p.group.exists() {
		time.Sleep(25 * time.Millisecond)
	}
	if !p.group.exists() {
		return nil
	}
	if err := p.group.kill(); err != nil {
		return fmt.Errorf("release smoke: %s: kill: %w", p.name, err)
	}
	deadline = time.Now().Add(grace)
	for time.Now().Before(deadline) && p.group.exists() {
		time.Sleep(25 * time.Millisecond)
	}
	if p.group.exists() {
		return fmt.Errorf("release smoke: %s: process group still running after kill", p.name)
	}
	return nil
}

// stopAll stops every process the owner started that is still running.
func (o *smokeProcessOwner) stopAll(grace time.Duration) error {
	o.mu.Lock()
	procs := append([]*smokeOwnedProcess(nil), o.procs...)
	o.mu.Unlock()
	var errs []error
	for i := len(procs) - 1; i >= 0; i-- {
		if err := o.stop(procs[i], grace); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

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
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func startSleeper(t *testing.T, r *releaseSmokeRun) *smokeOwnedProcess {
	t.Helper()
	argv := []string{"/bin/sleep", "60"}
	if runtime.GOOS == "windows" {
		// cmd.exe runs ping as a grandchild, so stopping must reach the whole job.
		argv = []string{"cmd.exe", "/c", "ping", "-n", "61", "127.0.0.1"}
	}
	proc, err := r.owner.start("sleeper", t.TempDir(), filepath.Join(r.logDir, "sleeper"), argv, nil)
	if err != nil {
		t.Fatalf("start owned process: %v", err)
	}
	return proc
}

func assertExited(t *testing.T, proc *smokeOwnedProcess) {
	t.Helper()
	select {
	case <-proc.exitedCh():
	case <-time.After(5 * time.Second):
		t.Fatalf("owned process %s still running after stop", proc.name)
	}
}

func TestOwnedProcessStopsOnCancellation(t *testing.T) {
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: filepath.Join(t.TempDir(), "run")})
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	ctx, cancel := context.WithCancel(context.Background())
	var proc *smokeOwnedProcess
	phases := []smokePhase{{"wait for owned child", func(ctx context.Context) error {
		proc = startSleeper(t, r)
		go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		return r.owner.wait(ctx, proc)
	}}}

	err := r.execute(ctx, phases)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	assertExited(t, proc)
}

func TestOwnedProcessStopsOnTimeout(t *testing.T) {
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: filepath.Join(t.TempDir(), "run")})
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var proc *smokeOwnedProcess
	err := r.execute(ctx, []smokePhase{{"long step", func(ctx context.Context) error {
		proc = startSleeper(t, r)
		return r.owner.wait(ctx, proc)
	}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	assertExited(t, proc)
}

func TestOwnedProcessStopsOnFailureAndIsIdempotent(t *testing.T) {
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: filepath.Join(t.TempDir(), "run")})
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	var proc *smokeOwnedProcess
	err := r.execute(context.Background(), []smokePhase{
		{"start", func(context.Context) error { proc = startSleeper(t, r); return nil }},
		{"fail", func(context.Context) error { return errors.New("step exploded") }},
	})
	if err == nil || !strings.Contains(err.Error(), "step exploded") {
		t.Fatalf("err = %v, want step failure", err)
	}
	assertExited(t, proc)
	if err := r.owner.stop(proc, time.Second); err != nil {
		t.Fatalf("second stop of exited process: %v", err)
	}
}

func TestOwnerDoesNotTouchProcessesItDidNotStart(t *testing.T) {
	r := newReleaseSmokeRun(releaseSmokeOptions{RunDir: t.TempDir()})
	r.logDir = filepath.Join(r.opts.RunDir, "logs")
	unrelated := startSleeper(t, r)
	other := newReleaseSmokeRun(releaseSmokeOptions{RunDir: t.TempDir()})
	if err := other.owner.stopAll(time.Second); err != nil {
		t.Fatalf("stopAll on empty owner: %v", err)
	}
	select {
	case <-unrelated.exitedCh():
		t.Fatal("process owned by another run was stopped")
	default:
	}
	if err := r.owner.stopAll(2 * time.Second); err != nil {
		t.Fatalf("cleanup stopAll: %v", err)
	}
	assertExited(t, unrelated)
}

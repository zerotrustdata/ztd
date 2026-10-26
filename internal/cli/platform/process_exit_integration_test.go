//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

// startReexecChild starts this test binary in the gateway child mode, which
// holds for a while, so a test can end it at a time of its choosing.
func startReexecChild(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "gw")
	cmd.Env = append(os.Environ(), string(constants.EnvVar.TestReexec)+"="+mode)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestWaitForProcessExit_ReturnsWhenTheChildIsKilled(t *testing.T) {
	cmd := startReexecChild(t, "hold")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := make(chan error, 1)
	go func() { result <- WaitForProcessExit(ctx, cmd.Process.Pid) }()
	require.NoError(t, cmd.Process.Kill())

	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("the wait did not return after the child was killed")
	}
}

func TestWaitForProcessExit_ReturnsAtOnceForAGoneProcess(t *testing.T) {
	cmd := startReexecChild(t, "error")
	pid := cmd.Process.Pid
	_ = cmd.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, WaitForProcessExit(ctx, pid))
}

// A watch outlives its first wait: it times out while the child lives, and it
// keeps reporting the exit on every later wait once the child is killed.
func TestWaitForProcessExit_WatchReportsTheExitOnEveryWait(t *testing.T) {
	cmd := startReexecChild(t, "hold")
	watch, err := WatchProcessExit(cmd.Process.Pid)
	require.NoError(t, err)
	defer func() { _ = watch.Close() }()

	expired, cancelExpired := context.WithTimeout(context.Background(), 0)
	defer cancelExpired()
	require.ErrorIs(t, watch.Wait(expired), context.DeadlineExceeded)

	require.NoError(t, cmd.Process.Kill())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, watch.Wait(ctx))
	require.NoError(t, watch.Wait(expired))
}

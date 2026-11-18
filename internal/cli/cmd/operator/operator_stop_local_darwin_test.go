//go:build darwin

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestMain(m *testing.M) {
	if os.Getenv("ZTD_STOP_TEST_WORKER") == "1" {
		signal.Ignore(syscall.SIGTERM)
		fmt.Println("ready")
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// startStopTestWorker runs this test binary as a TERM-ignoring "ztd operator
// start" worker in dir. The binary is copied, not hardlinked: proc_pidpath
// reports whichever name the kernel cached for a shared vnode, which may be
// the test binary's own name rather than "ztd".
func startStopTestWorker(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	workerPath := filepath.Join(testutil.TempDir(t), "ztd")
	require.NoError(t, CopyFile(executable, workerPath))
	child := exec.Command(workerPath, append([]string{"operator", "start"}, args...)...)
	child.Dir = dir
	child.Env = append(os.Environ(), "ZTD_STOP_TEST_WORKER=1")
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		require.True(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	return child
}

func discoverStopTestWorker(t *testing.T, pid int) operatordeploy.LocalOperatorProcess {
	t.Helper()
	processes, err := operatordeploy.DiscoverLocalOperators()
	require.NoError(t, err)
	var worker *operatordeploy.LocalOperatorProcess
	for i := range processes {
		if processes[i].PID == pid {
			worker = &processes[i]
			continue
		}
		processes[i].Close()
	}
	require.NotNil(t, worker)
	t.Cleanup(worker.Close)
	return *worker
}

func TestLocalOperatorDiscoveryAndKill(t *testing.T) {
	child := startStopTestWorker(t, testutil.TempDir(t), "--working-dir=/tmp/ztd-stop-test")
	worker := discoverStopTestWorker(t, child.Process.Pid)
	require.Equal(t, "/tmp/ztd-stop-test", worker.Dir)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	result := stopLocalOperator(cmd, worker, models.StopOperatorResponse{Success: true}, nil, 10*time.Millisecond)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "KILL", result.Method)
	exitCtx, cancelExit := context.WithTimeout(context.Background(), time.Second)
	defer cancelExit()
	require.NoError(t, platform.WaitForProcessExit(exitCtx, child.Process.Pid))
}

func TestLocalOperatorDiscoveryUsesWorkerCwd(t *testing.T) {
	dir, err := filepath.EvalSymlinks(testutil.TempDir(t))
	require.NoError(t, err)
	child := startStopTestWorker(t, dir)
	worker := discoverStopTestWorker(t, child.Process.Pid)
	require.Equal(t, dir, worker.Dir)
	exited, err := worker.Wait(0)
	require.NoError(t, err)
	require.False(t, exited)
}

func TestLocalOperatorArgv(t *testing.T) {
	require.True(t, operatordeploy.IsLocalOperatorArgv([]string{"/opt/ztd/ztd", "operator", "start"}))
	require.True(t, operatordeploy.IsLocalOperatorArgv([]string{"ztd", "operators", "start"}))
	for _, args := range [][]string{{"sh", "-c", "ztd operator start"}, {"ztd", "gw", "start"}, {"ztd", "operator", "stop"}, {"language_server", "operator", "start"}} {
		require.False(t, operatordeploy.IsLocalOperatorArgv(args))
	}
	require.Equal(t, "/tmp/foo", operatordeploy.OperatorArgValue([]string{"ztd", "operator", "start", "--working-dir", "/tmp/foo"}, "--working-dir"))
}

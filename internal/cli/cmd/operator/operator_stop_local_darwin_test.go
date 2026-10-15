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
	"github.com/zerotrustdata/ztd/internal/models"
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

func discoverStopTestWorker(t *testing.T, pid int) localOperatorProcess {
	t.Helper()
	processes, err := discoverLocalOperators()
	require.NoError(t, err)
	var worker *localOperatorProcess
	for i := range processes {
		if processes[i].pid == pid {
			worker = &processes[i]
			continue
		}
		processes[i].close()
	}
	require.NotNil(t, worker)
	t.Cleanup(worker.close)
	return *worker
}

func TestLocalOperatorDiscoveryAndKill(t *testing.T) {
	child := startStopTestWorker(t, testutil.TempDir(t), "--working-dir=/tmp/ztd-stop-test")
	worker := discoverStopTestWorker(t, child.Process.Pid)
	require.Equal(t, "/tmp/ztd-stop-test", worker.dir)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	result := stopLocalOperator(cmd, worker, models.StopOperatorResponse{Success: true}, nil, 10*time.Millisecond)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "KILL", result.Method)
	exited, err := waitPIDTermination(child.Process.Pid, time.Second)
	require.NoError(t, err)
	require.True(t, exited)
}

func TestLocalOperatorDiscoveryUsesWorkerCwd(t *testing.T) {
	dir, err := filepath.EvalSymlinks(testutil.TempDir(t))
	require.NoError(t, err)
	child := startStopTestWorker(t, dir)
	worker := discoverStopTestWorker(t, child.Process.Pid)
	require.Equal(t, dir, worker.dir)
	exited, err := worker.wait(0)
	require.NoError(t, err)
	require.False(t, exited)
}

func TestLocalOperatorArgv(t *testing.T) {
	require.True(t, isLocalOperatorArgv([]string{"/opt/ztd/ztd", "operator", "start"}))
	require.True(t, isLocalOperatorArgv([]string{"ztd", "operators", "start"}))
	for _, args := range [][]string{{"sh", "-c", "ztd operator start"}, {"ztd", "gw", "start"}, {"ztd", "operator", "stop"}, {"language_server", "operator", "start"}} {
		require.False(t, isLocalOperatorArgv(args))
	}
	require.Equal(t, "/tmp/foo", operatorArgValue([]string{"ztd", "operator", "start", "--working-dir", "/tmp/foo"}, "--working-dir"))
}

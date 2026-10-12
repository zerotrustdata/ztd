//go:build linux

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

func TestLocalOperatorDiscoveryAndKill(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	workerPath := filepath.Join(t.TempDir(), "ztd")
	require.NoError(t, os.Link(executable, workerPath))
	child := exec.Command(workerPath, "operator", "start", "--working-dir=/tmp/ztd-stop-test")
	child.Env = append(os.Environ(), "ZTD_STOP_TEST_WORKER=1")
	stdout, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "ready" }()
	select {
	case ok := <-ready:
		require.True(t, ok)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	processes, err := discoverLocalOperators()
	require.NoError(t, err)
	defer func() {
		for _, p := range processes {
			p.close()
		}
	}()
	var worker *localOperatorProcess
	for i := range processes {
		if processes[i].pid == child.Process.Pid {
			worker = &processes[i]
			break
		}
	}
	require.NotNil(t, worker)
	require.Equal(t, "/tmp/ztd-stop-test", worker.dir)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	result := stopLocalOperator(cmd, *worker, models.StopOperatorResponse{Success: true}, nil, 10*time.Millisecond)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "KILL", result.Method)
}

func TestLocalOperatorArgv(t *testing.T) {
	require.True(t, isLocalOperatorArgv([]string{"/opt/ztd/ztd", "operator", "start"}))
	require.True(t, isLocalOperatorArgv([]string{"ztd", "operators", "start"}))
	for _, args := range [][]string{{"sh", "-c", "ztd operator start"}, {"ztd", "gw", "start"}, {"ztd", "operator", "stop"}, {"language_server", "operator", "start"}} {
		require.False(t, isLocalOperatorArgv(args))
	}
	require.Equal(t, "/tmp/foo", operatorArgValue([]string{"ztd", "operator", "start", "--working-dir", "/tmp/foo"}, "--working-dir"))
}

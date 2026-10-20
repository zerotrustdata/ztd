// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows && integration

package operatorcmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// The worker is this test binary copied to ztd.exe; TestMain keeps it alive
// as a stand-in "ztd.exe operator start --working-dir <dir>" process.
func TestLocalOperatorDiscoveryAndTerminateWindows(t *testing.T) {
	bin, err := os.Executable()
	require.NoError(t, err)
	dir := testutil.TempDir(t)
	worker := filepath.Join(dir, "ztd.exe")
	require.NoError(t, CopyFile(bin, worker))
	child := exec.Command(worker, "operator", "start", "--working-dir", dir)
	require.NoError(t, child.Start())
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "ready"))
		return err == nil
	}, 10*time.Second, 25*time.Millisecond)

	processes, err := discoverLocalOperators()
	require.NoError(t, err)
	defer func() {
		for _, p := range processes {
			p.close()
		}
	}()
	var found *localOperatorProcess
	for i := range processes {
		if processes[i].pid == child.Process.Pid {
			found = &processes[i]
			break
		}
	}
	require.NotNil(t, found, "worker PID %d not discovered", child.Process.Pid)
	require.Equal(t, filepath.Clean(dir), found.dir)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	result := stopLocalOperator(cmd, *found, models.StopOperatorResponse{Success: true}, nil, 10*time.Millisecond)
	require.True(t, result.Success, result.Error)
	require.Equal(t, "TERM", result.Method)
	exitCtx, cancelExit := context.WithTimeout(context.Background(), time.Second)
	defer cancelExit()
	require.NoError(t, platform.WaitForProcessExit(exitCtx, child.Process.Pid))
}

func TestLocalOperatorArgvWindows(t *testing.T) {
	require.True(t, isLocalOperatorArgv([]string{`D:\ztd\ztd.exe`, "operator", "start"}))
	require.True(t, isLocalOperatorArgv([]string{"./ztd", "operator", "start"}))
	require.True(t, isLocalOperatorArgv([]string{`.\ZTD.EXE`, "operators", "start"}))
	for _, args := range [][]string{{"ztd.exe", "gw", "start"}, {"ztdx.exe", "operator", "start"}, {"cmd.exe", "/c", "ztd operator start"}} {
		require.False(t, isLocalOperatorArgv(args))
	}
}

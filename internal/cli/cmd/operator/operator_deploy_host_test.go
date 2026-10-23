// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestExecuteDeployHostPrepare(t *testing.T) {
	tempHome := testutil.TempDir(t)
	t.Setenv("HOME", tempHome)
	t.Setenv("USERPROFILE", tempHome) // os.UserHomeDir on Windows

	ctx := context.Background()

	t.Run("creates and resolves dirs including home expansion", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		tempDir := testutil.TempDir(t)
		relDir := filepath.Join(tempDir, "relative", "sub")
		homeDir := "~/ztd-test-deploy"

		resolved, err := ExecuteDeployHostPrepare(ctx, []string{relDir, homeDir})
		require.NoError(t, err)
		require.Len(t, resolved, 2)

		// Check first dir
		info1, err := os.Stat(resolved[0])
		require.NoError(t, err)
		require.True(t, info1.IsDir())
		require.Equal(t, testutil.FileMode(constants.PermDirPrivate, true), info1.Mode().Perm())

		// Check home dir
		expectedHomeSub := filepath.Join(tempHome, "ztd-test-deploy")
		evalHomeSub, err := filepath.EvalSymlinks(expectedHomeSub)
		require.NoError(t, err)
		require.Equal(t, evalHomeSub, resolved[1])

		info2, err := os.Stat(resolved[1])
		require.NoError(t, err)
		require.True(t, info2.IsDir())
		require.Equal(t, testutil.FileMode(constants.PermDirPrivate, true), info2.Mode().Perm())
	})

	t.Run("empty dirs error", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		_, err := ExecuteDeployHostPrepare(ctx, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "dirs list must not be empty")

		_, err = ExecuteDeployHostPrepare(ctx, []string{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "dirs list must not be empty")
	})

	t.Run("context cancelled", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := ExecuteDeployHostPrepare(cancelCtx, []string{testutil.TempDir(t)})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestExecuteDeployHostInstall(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("copies binary atomically and sets executable permissions", func(t *testing.T) {
		t.Parallel()
		srcDir := testutil.TempDir(t)
		destDir := testutil.TempDir(t)

		srcFile := filepath.Join(srcDir, "source-bin")
		content := []byte("#!/bin/sh\necho test\n")
		require.NoError(t, os.WriteFile(srcFile, content, 0o600))

		targetFile := filepath.Join(destDir, "bin", "target-bin")
		err := ExecuteDeployHostInstall(ctx, srcFile, targetFile)
		require.NoError(t, err)

		targetContent, err := os.ReadFile(targetFile)
		require.NoError(t, err)
		require.Equal(t, content, targetContent)

		info, err := os.Stat(targetFile)
		require.NoError(t, err)
		require.Equal(t, testutil.FileMode(constants.PermFileExecutable, false), info.Mode().Perm())
	})

	t.Run("missing source", func(t *testing.T) {
		t.Parallel()
		destDir := testutil.TempDir(t)
		err := ExecuteDeployHostInstall(ctx, "", filepath.Join(destDir, "bin"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "source path required")
	})

	t.Run("missing target", func(t *testing.T) {
		t.Parallel()
		srcDir := testutil.TempDir(t)
		err := ExecuteDeployHostInstall(ctx, filepath.Join(srcDir, "bin"), "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "target path required")
	})

	t.Run("source file does not exist", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		err := ExecuteDeployHostInstall(ctx, filepath.Join(tempDir, "nonexistent"), filepath.Join(tempDir, "target"))
		require.Error(t, err)
		require.Contains(t, err.Error(), "open source binary")
	})

	t.Run("context cancelled", func(t *testing.T) {
		t.Parallel()
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		err := ExecuteDeployHostInstall(cancelCtx, "src", "target")
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestExecuteDeployHostLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("creates hard link via staging", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		binDir := filepath.Join(tempDir, "bin")
		destDir := filepath.Join(tempDir, "dest")
		require.NoError(t, os.MkdirAll(binDir, constants.PermDirPrivate))

		srcBin := filepath.Join(binDir, "ztd")
		require.NoError(t, os.WriteFile(srcBin, []byte("binary-payload"), constants.PermFileExecutable))

		err := ExecuteDeployHostLink(ctx, binDir, destDir, "ztd")
		require.NoError(t, err)

		destBin := filepath.Join(destDir, "ztd")
		srcInfo, err := os.Stat(srcBin)
		require.NoError(t, err)
		destInfo, err := os.Stat(destBin)
		require.NoError(t, err)
		require.True(t, os.SameFile(srcInfo, destInfo))
	})

	t.Run("default binary name ztd", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		binDir := filepath.Join(tempDir, "bin")
		destDir := filepath.Join(tempDir, "dest")
		require.NoError(t, os.MkdirAll(binDir, constants.PermDirPrivate))

		srcBin := filepath.Join(binDir, "ztd")
		require.NoError(t, os.WriteFile(srcBin, []byte("binary-payload"), constants.PermFileExecutable))

		err := ExecuteDeployHostLink(ctx, binDir, destDir, "")
		require.NoError(t, err)

		destBin := filepath.Join(destDir, "ztd")
		srcInfo, err := os.Stat(srcBin)
		require.NoError(t, err)
		destInfo, err := os.Stat(destBin)
		require.NoError(t, err)
		require.True(t, os.SameFile(srcInfo, destInfo))
	})

	t.Run("missing binary_dir", func(t *testing.T) {
		t.Parallel()
		err := ExecuteDeployHostLink(ctx, "", "dest", "ztd")
		require.Error(t, err)
		require.Contains(t, err.Error(), "binary_dir required")
	})

	t.Run("missing dest_dir", func(t *testing.T) {
		t.Parallel()
		err := ExecuteDeployHostLink(ctx, "bin", "", "ztd")
		require.Error(t, err)
		require.Contains(t, err.Error(), "dest_dir required")
	})

	t.Run("source binary not found", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		err := ExecuteDeployHostLink(ctx, tempDir, filepath.Join(tempDir, "dest"), "ztd")
		require.Error(t, err)
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("context cancelled", func(t *testing.T) {
		t.Parallel()
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		err := ExecuteDeployHostLink(cancelCtx, "bin", "dest", "ztd")
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestExecuteDeployHostStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock worker script uses POSIX shell")
	}

	ctx := context.Background()

	t.Run("starts worker detached and writes operator.pid and start.log", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("identity-bound local worker discovery is only exercised on Linux and macOS")
		}
		workingDir := testutil.TempDir(t)
		executable, err := os.Executable()
		require.NoError(t, err)
		mockWorker := filepath.Join(workingDir, "ztd")
		if runtime.GOOS == "darwin" {
			require.NoError(t, CopyFile(executable, mockWorker))
		} else {
			require.NoError(t, os.Link(executable, mockWorker))
		}
		t.Setenv("ZTD_STOP_TEST_WORKER", "1")

		args := []string{"operator", "start", "--working-dir=" + workingDir}
		pid, err := ExecuteDeployHostStart(ctx, workingDir, "ztd", args)
		require.NoError(t, err)
		require.Greater(t, pid, 1)

		t.Cleanup(func() {
			stopCtx, cancelStop := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancelStop()
			if _, stopErr := ExecuteDeployHostStop(stopCtx, "", workingDir); stopErr != nil {
				t.Errorf("stop test worker in %s during cleanup: %v", workingDir, stopErr)
			}
		})

		// Verify pid file exists and matches
		pidFile := filepath.Join(workingDir, constants.OperatorPIDFilename)
		pidData, err := os.ReadFile(pidFile)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("%d\n", pid), string(pidData))

		// Verify start.log exists
		logPath := filepath.Join(workingDir, constants.DeployStartLogFilename)
		_, err = os.Stat(logPath)
		require.NoError(t, err)

		// Calling start again in same workingDir stops previous worker first
		pid2, err := ExecuteDeployHostStart(ctx, workingDir, "ztd", args)
		require.NoError(t, err)
		require.Greater(t, pid2, 1)
		require.NotEqual(t, pid, pid2)

		exitCtx, cancelExit := context.WithTimeout(ctx, 3*time.Second)
		defer cancelExit()
		require.NoError(t, platform.WaitForProcessExit(exitCtx, pid))
	})

	t.Run("missing working_dir", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		_, err := ExecuteDeployHostStart(ctx, "", "ztd", nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "working_dir required")
	})

	t.Run("noncanonical worker binary name rejected", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		tempDir := testutil.TempDir(t)
		_, err := ExecuteDeployHostStart(ctx, tempDir, "custom-ztd", nil)
		require.ErrorIs(t, err, constants.ErrOperatorDeployFailed)
		require.Contains(t, err.Error(), "must be named ztd")
	})

	t.Run("canonical binary missing", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		tempDir := testutil.TempDir(t)
		_, err := ExecuteDeployHostStart(ctx, tempDir, "ztd", nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "not found")
	})

	t.Run("context cancelled", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := ExecuteDeployHostStart(cancelCtx, testutil.TempDir(t), "ztd", nil)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestExecuteDeployHostState(t *testing.T) { //nolint:paralleltest // mutates package-level state

	ctx := context.Background()

	t.Run("reads deployment state successfully", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		fileSvc, _ := cmdtest.NewCmdTestEnv(t)
		now := time.Now().UTC().Truncate(time.Second)
		stateJSON := fmt.Sprintf(`{
			"launch_id": "launch-123",
			"phase": "ready",
			"operator_session_id": "session-456",
			"request_id": "req-789",
			"updated_at": %q
		}`, now.Format(time.RFC3339))
		statePath := filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator)
		require.NoError(t, fileSvc.WriteFile(ctx, statePath, []byte(stateJSON), constants.PermFilePrivate))

		state, err := ExecuteDeployHostState(ctx, fileSvc)
		require.NoError(t, err)
		require.NotNil(t, state)
		require.Equal(t, "launch-123", state.LaunchID)
		require.Equal(t, models.OperatorDeploymentPhaseReady, state.Phase)
		require.Equal(t, "session-456", state.OperatorSessionID)
		require.Equal(t, "req-789", state.RequestID)
		require.Equal(t, now, state.UpdatedAt)
	})

	t.Run("file not found returns nil without error", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		fileSvc, _ := cmdtest.NewCmdTestEnv(t)
		state, err := ExecuteDeployHostState(ctx, fileSvc)
		require.NoError(t, err)
		require.Nil(t, state)
	})

	t.Run("invalid state json returns error", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		fileSvc, _ := cmdtest.NewCmdTestEnv(t)
		statePath := filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator)
		require.NoError(t, fileSvc.WriteFile(ctx, statePath, []byte("invalid-json"), constants.PermFilePrivate))

		state, err := ExecuteDeployHostState(ctx, fileSvc)
		require.ErrorIs(t, err, constants.ErrOperatorDeployFailed)
		require.Nil(t, state)
	})
}

func TestExecuteDeployHostPreflight(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("invalid host fails validation", func(t *testing.T) {
		t.Parallel()
		err := ExecuteDeployHostPreflight(ctx, []string{"invalid$$host"})
		require.Error(t, err)
		require.ErrorIs(t, err, constants.ErrPathValidation)
	})

	t.Run("no args fails validation", func(t *testing.T) {
		t.Parallel()
		err := ExecuteDeployHostPreflight(ctx, []string{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "accepts 1 arg(s)")
	})
}

func TestExecuteDeployHostStop(t *testing.T) { //nolint:paralleltest // mutates package-level state

	ctx := context.Background()

	t.Run("discovery mock handles matches and escalates TERM to KILL", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		destDir := testutil.TempDir(t)
		workingDir := filepath.Join(destDir, "worker-1")
		otherDir := testutil.TempDir(t)
		require.NoError(t, os.MkdirAll(workingDir, constants.PermDirPrivate))
		require.NoError(t, os.MkdirAll(otherDir, constants.PermDirPrivate))

		// Create PID files
		pid1File := filepath.Join(workingDir, constants.OperatorPIDFilename)
		require.NoError(t, os.WriteFile(pid1File, []byte("101\n"), constants.PermFilePrivate))

		var signals1, signals2 []bool
		closed3 := false

		p1 := localOperatorProcess{
			pid: 101,
			dir: workingDir,
			wait: func(time.Duration) (bool, error) {
				return true, nil
			},
			signal: func(force bool) error {
				signals1 = append(signals1, force)
				return nil
			},
		}

		p2 := localOperatorProcess{
			pid: 102,
			dir: destDir,
			wait: func(time.Duration) (bool, error) {
				// First check (TERM) returns false; second check (KILL) returns true
				return len(signals2) > 1, nil
			},
			signal: func(force bool) error {
				signals2 = append(signals2, force)
				return nil
			},
		}

		p3 := localOperatorProcess{
			pid: 103,
			dir: otherDir,
			close: func() {
				closed3 = true
			},
		}

		origDiscoverer := defaultLocalOperatorDiscoverer
		defaultLocalOperatorDiscoverer = func() ([]localOperatorProcess, error) {
			return []localOperatorProcess{p1, p2, p3}, nil
		}
		t.Cleanup(func() {
			defaultLocalOperatorDiscoverer = origDiscoverer
		})

		pids, err := ExecuteDeployHostStop(ctx, destDir, "")
		require.NoError(t, err)
		require.Equal(t, []int{101, 102}, pids)
		require.Equal(t, []bool{false}, signals1)       // TERM was sufficient
		require.Equal(t, []bool{false, true}, signals2) // TERM then KILL
		require.True(t, closed3)                        // Non-matching process closed

		// PID file in workingDir should have been cleaned up
		_, err = os.Stat(pid1File)
		require.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("PID metadata cannot authorize termination", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		if runtime.GOOS == "windows" {
			t.Skip("uses a POSIX subprocess to verify a recorded PID remains alive")
		}
		for _, tc := range []struct {
			name     string
			discover func() ([]localOperatorProcess, error)
			wantErr  bool
		}{
			{name: "discovery error", discover: func() ([]localOperatorProcess, error) { return nil, errors.New("discovery unavailable") }, wantErr: true},
			{name: "successful discovery with no matching worker", discover: func() ([]localOperatorProcess, error) { return nil, nil }},
		} {
			t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest // mutates package-level state

				origDiscoverer := defaultLocalOperatorDiscoverer
				defaultLocalOperatorDiscoverer = tc.discover
				t.Cleanup(func() { defaultLocalOperatorDiscoverer = origDiscoverer })

				tempDir := testutil.TempDir(t)
				workerDir := filepath.Join(tempDir, "worker")
				require.NoError(t, os.MkdirAll(workerDir, constants.PermDirPrivate))
				cmd := exec.Command("sleep", "30")
				require.NoError(t, cmd.Start())
				t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
				pidFile := filepath.Join(workerDir, constants.OperatorPIDFilename)
				require.NoError(t, os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), constants.PermFilePrivate))

				stopped, err := ExecuteDeployHostStop(ctx, tempDir, "")
				if tc.wantErr {
					require.Error(t, err)
					require.Empty(t, stopped)
				} else {
					require.NoError(t, err)
					require.Empty(t, stopped)
				}
				require.NoError(t, cmd.Process.Signal(syscall.Signal(0)), "recorded PID must remain alive")
				_, err = os.Stat(pidFile)
				require.NoError(t, err, "unverified PID metadata remains untouched")
			})
		}
	})

	t.Run("signal and wait failures are returned", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		workerDir := testutil.TempDir(t)
		for _, tc := range []struct {
			name string
			proc localOperatorProcess
			want string
		}{
			{name: "signal", proc: localOperatorProcess{pid: 301, dir: workerDir, signal: func(bool) error { return errors.New("signal failed") }}, want: "send TERM"},
			{name: "wait", proc: localOperatorProcess{pid: 302, dir: workerDir, signal: func(bool) error { return nil }, wait: func(time.Duration) (bool, error) { return false, errors.New("wait failed") }}, want: "wait for Operator PID"},
		} {
			t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest // mutates package-level state

				origDiscoverer := defaultLocalOperatorDiscoverer
				defaultLocalOperatorDiscoverer = func() ([]localOperatorProcess, error) { return []localOperatorProcess{tc.proc}, nil }
				t.Cleanup(func() { defaultLocalOperatorDiscoverer = origDiscoverer })
				pids, err := ExecuteDeployHostStop(ctx, "", workerDir)
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.want)
				require.Empty(t, pids)
			})
		}
	})

	t.Run("cancelled context stops before discovery", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		called := false
		origDiscoverer := defaultLocalOperatorDiscoverer
		defaultLocalOperatorDiscoverer = func() ([]localOperatorProcess, error) {
			called = true
			return nil, nil
		}
		t.Cleanup(func() { defaultLocalOperatorDiscoverer = origDiscoverer })
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		pids, err := ExecuteDeployHostStop(cancelled, "", testutil.TempDir(t))
		require.ErrorIs(t, err, context.Canceled)
		require.Empty(t, pids)
		require.False(t, called)
	})

	t.Run("missing both dest_dir and working_dir returns error", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		_, err := ExecuteDeployHostStop(ctx, "", "")
		require.Error(t, err)
		require.Contains(t, err.Error(), "dest_dir or working_dir required")
	})
}

func TestExecuteDeployHost_Router(t *testing.T) { //nolint:paralleltest // mutates package-level state

	ctx := context.Background()

	t.Run("dispatches prepare action", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		req := models.DeployHostRequest{
			Action: models.DeployHostActionPrepare,
			Dirs:   []string{filepath.Join(tempDir, "dir1")},
		}
		resp, err := ExecuteDeployHost(ctx, req, nil)
		require.NoError(t, err)
		require.Len(t, resp.ResolvedDirs, 1)
	})

	t.Run("dispatches install action with target", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		src := filepath.Join(tempDir, "src")
		target := filepath.Join(tempDir, "target")
		require.NoError(t, os.WriteFile(src, []byte("data"), constants.PermFileExecutable))

		req := models.DeployHostRequest{
			Action: models.DeployHostActionInstall,
			Source: src,
			Target: target,
		}
		_, err := ExecuteDeployHost(ctx, req, nil)
		require.NoError(t, err)

		_, err = os.Stat(target)
		require.NoError(t, err)
	})

	t.Run("dispatches install action with DestDir fallback", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		src := filepath.Join(tempDir, "src")
		destDir := filepath.Join(tempDir, "dest")
		require.NoError(t, os.WriteFile(src, []byte("data"), constants.PermFileExecutable))

		req := models.DeployHostRequest{
			Action:  models.DeployHostActionInstall,
			Source:  src,
			DestDir: destDir,
		}
		_, err := ExecuteDeployHost(ctx, req, nil)
		require.NoError(t, err)

		expectedTarget := filepath.Join(destDir, constants.DeployBinDirname, "ztd")
		_, err = os.Stat(expectedTarget)
		require.NoError(t, err)
	})

	t.Run("dispatches link action", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		binDir := filepath.Join(tempDir, "bin")
		destDir := filepath.Join(tempDir, "dest")
		require.NoError(t, os.MkdirAll(binDir, constants.PermDirPrivate))
		srcBin := filepath.Join(binDir, "ztd")
		require.NoError(t, os.WriteFile(srcBin, []byte("data"), constants.PermFileExecutable))

		req := models.DeployHostRequest{
			Action:    models.DeployHostActionLink,
			BinaryDir: binDir,
			DestDir:   destDir,
			Binary:    "ztd",
		}
		_, err := ExecuteDeployHost(ctx, req, nil)
		require.NoError(t, err)

		_, err = os.Stat(filepath.Join(destDir, "ztd"))
		require.NoError(t, err)
	})

	t.Run("dispatches state action", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		fileSvc, _ := cmdtest.NewCmdTestEnv(t)
		now := time.Now().UTC().Truncate(time.Second)
		stateJSON := fmt.Sprintf(`{
			"launch_id": "launch-123",
			"phase": "ready",
			"operator_session_id": "session-456",
			"updated_at": %q
		}`, now.Format(time.RFC3339))
		statePath := filepath.Join(constants.DeploymentDirname, constants.DeploymentStateFileOperator)
		require.NoError(t, fileSvc.WriteFile(ctx, statePath, []byte(stateJSON), constants.PermFilePrivate))

		req := models.DeployHostRequest{
			Action:     models.DeployHostActionState,
			WorkingDir: "work",
		}
		factory := func(string, *slog.Logger) (fs.RuntimeFileService, error) {
			return fileSvc, nil
		}
		resp, err := ExecuteDeployHost(ctx, req, factory)
		require.NoError(t, err)
		require.NotNil(t, resp.State)
		require.Equal(t, models.OperatorDeploymentPhaseReady, resp.State.Phase)
	})

	t.Run("dispatches stop action", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		origDiscoverer := defaultLocalOperatorDiscoverer
		defaultLocalOperatorDiscoverer = func() ([]localOperatorProcess, error) {
			return nil, nil
		}
		t.Cleanup(func() {
			defaultLocalOperatorDiscoverer = origDiscoverer
		})

		req := models.DeployHostRequest{
			Action:     models.DeployHostActionStop,
			WorkingDir: testutil.TempDir(t),
		}
		resp, err := ExecuteDeployHost(ctx, req, nil)
		require.NoError(t, err)
		require.Equal(t, 0, resp.StoppedCount)
	})

	t.Run("unknown action returns error", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		req := models.DeployHostRequest{
			Action: "invalid-action",
		}
		_, err := ExecuteDeployHost(ctx, req, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unknown deploy-host action")
	})
}

func TestOperatorDeployHostCmd_Protocol(t *testing.T) {
	t.Parallel()
	t.Run("valid json request on stdin returns json response on stdout", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		req := models.DeployHostRequest{
			Action: models.DeployHostActionPrepare,
			Dirs:   []string{filepath.Join(tempDir, "sub")},
		}
		reqBytes, err := json.Marshal(req)
		require.NoError(t, err)

		cmd := operatorDeployHostCmd()
		var stdout bytes.Buffer
		cmd.SetIn(bytes.NewReader(reqBytes))
		cmd.SetOut(&stdout)
		cmd.SetErr(io.Discard)

		err = cmd.Execute()
		require.NoError(t, err)

		var resp models.DeployHostResponse
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
		require.True(t, resp.Success)
		require.Len(t, resp.ResolvedDirs, 1)
	})

	t.Run("invalid json on stdin returns error response", func(t *testing.T) {
		t.Parallel()
		cmd := operatorDeployHostCmd()
		var stdout bytes.Buffer
		cmd.SetIn(strings.NewReader("not-valid-json"))
		cmd.SetOut(&stdout)
		cmd.SetErr(io.Discard)

		err := cmd.Execute()
		require.Error(t, err)

		var resp models.DeployHostResponse
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
		require.False(t, resp.Success)
		require.Contains(t, resp.Error, "decode deploy-host request")
	})

	t.Run("action failure returns error response and command error", func(t *testing.T) {
		t.Parallel()
		req := models.DeployHostRequest{
			Action: "unknown-action",
		}
		reqBytes, err := json.Marshal(req)
		require.NoError(t, err)

		cmd := operatorDeployHostCmd()
		var stdout bytes.Buffer
		cmd.SetIn(bytes.NewReader(reqBytes))
		cmd.SetOut(&stdout)
		cmd.SetErr(io.Discard)

		err = cmd.Execute()
		require.Error(t, err)

		var resp models.DeployHostResponse
		require.NoError(t, json.Unmarshal(stdout.Bytes(), &resp))
		require.False(t, resp.Success)
		require.Contains(t, resp.Error, "unknown deploy-host action")
	})
}

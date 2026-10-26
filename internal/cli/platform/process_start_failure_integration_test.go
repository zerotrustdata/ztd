// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// These tests exercise StartOperator's failed-start cleanup paths. They
// require real process management (copyBinaryToBinDir, cmd.Start, Kill, Wait,
// PID file I/O) and therefore belong in Tier 2 (integration). The test binary
// is re-executed by StartOperator as a subprocess; TestMain (in
// testmain_helper_integration_test.go) detects the re-execution via os.Args[1] == "gw"
// and exits immediately, so the subprocess never becomes healthy. This lets
// the tests exercise the PID-write-failure and process-death-during-health-
// check cleanup paths without a real Gateway binary.

package platform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newStartFailureFileSvc creates a temp-rooted RuntimeFileService with the
// runtime tree already created. Each test gets its own isolated directory.
func newStartFailureFileSvc(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	baseDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(baseDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	return fileSvc
}

// pidRelPath returns the relative path to the operator PID file, constructed
// from path constants.
func pidRelPath() string {
	return filepath.Join(constants.PidDirname, constants.OperatorPIDFilename)
}

// minimalStartOpts returns OperatorStartOptions with a minimal valid config
// suitable for exercising the start-failure paths. The actual gateway never
// becomes healthy (TestMain exits immediately), so the config values do not
// matter beyond passing initial validation.
func minimalStartOpts() *OperatorStartOptions {
	return &OperatorStartOptions{
		GatewayConfig: serve.GatewayConfig{
			Posture:  "doctrine",
			LogLevel: "info",
		},
	}
}

// assertNoLiveChildAndNoPIDFile verifies that StartOperator's failure cleanup
// left no running child process and no stale PID file behind. OperatorStatus
// reads the PID file; if it returns running=false and pid=0, the PID file is
// gone (or never written). FileExists provides an independent check.
func assertNoLiveChildAndNoPIDFile(t *testing.T, pm *ProcessManager, fileSvc fs.RuntimeFileService) {
	t.Helper()
	running, pid, err := pm.OperatorStatus()
	require.NoError(t, err, "OperatorStatus should not error after cleanup")
	assert.False(t, running, "no Gateway process should be running after failed start")
	assert.Zero(t, pid, "no PID should be recorded after failed start")

	exists, err := fileSvc.FileExists(context.Background(), pidRelPath())
	require.NoError(t, err)
	assert.False(t, exists, "no stale PID file should remain after failed start")
}

type pidWriteFailingFileSvc struct {
	fs.RuntimeFileService
}

func (s *pidWriteFailingFileSvc) WriteFile(ctx context.Context, relPath string, data []byte, mode os.FileMode) error {
	if relPath == pidRelPath() {
		return os.ErrPermission
	}
	return s.RuntimeFileService.WriteFile(ctx, relPath, data, mode)
}

// Inject only the PID write failure: chmod on a directory does not prevent
// writes on Windows. Binary copying, process creation, and cleanup stay real.
func TestStartOperator_PIDWriteFailureTerminatesChildAndLeavesNoPIDFile(t *testing.T) {
	fileSvc := &pidWriteFailingFileSvc{RuntimeFileService: newStartFailureFileSvc(t)}

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)

	startErr := pm.StartOperator(t.Context(), minimalStartOpts())
	require.Error(t, startErr, "StartOperator should fail when writePID fails")
	require.ErrorIs(t, startErr, constants.ErrPIDWriteFailed,
		"error should wrap ErrPIDWriteFailed, got: %v", startErr)

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

// TestStartOperator_ProcessDeathDuringHealthCheckRemovesPIDFile verifies that
// when the child process exits before becoming healthy (detected by
// isProcessRunning on the first health-check iteration), stopFailedStart
// terminates/reaps the child and deletePID removes the PID file. The test
// binary exits immediately when re-executed (TestMain detects os.Args[1] ==
// "gw"), so the subprocess dies within the first HealthCheckInterval.
func TestStartOperator_ProcessDeathDuringHealthCheckRemovesPIDFile(t *testing.T) {
	fileSvc := newStartFailureFileSvc(t)

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)

	startErr := pm.StartOperator(t.Context(), minimalStartOpts())
	require.Error(t, startErr, "StartOperator should fail when the process never becomes healthy")
	require.ErrorIs(t, startErr, constants.ErrProcessStartFailed,
		"error should wrap ErrProcessStartFailed, got: %v", startErr)

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

// TestStartOperator_ProcessDeathReturnsChildDiagnostic verifies that a
// background child failure is actionable at the invoking terminal. The log
// remains the complete record, but the process manager must return the
// diagnostic emitted by this start attempt instead of only telling the user
// to inspect the log manually.
func TestStartOperator_ProcessDeathReturnsChildDiagnostic(t *testing.T) {
	t.Setenv(string(constants.EnvVar.TestReexec), "error")
	fileSvc := newStartFailureFileSvc(t)

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)

	startErr := pm.StartOperator(t.Context(), minimalStartOpts())
	require.Error(t, startErr)
	require.ErrorIs(t, startErr, constants.ErrProcessStartFailed)
	require.ErrorContains(t, startErr, "gateway child startup diagnostic")

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

func TestStartOperator_TimeoutReturnsChildDiagnostic(t *testing.T) {
	t.Setenv(string(constants.EnvVar.TestReexec), "verification")
	fileSvc := newStartFailureFileSvc(t)
	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)
	pm.SetHealthCheckParameters(100*time.Millisecond, 150)
	startErr := pm.StartOperator(t.Context(), minimalStartOpts())
	require.ErrorIs(t, startErr, constants.ErrProcessStartFailed)
	require.ErrorIs(t, startErr, context.DeadlineExceeded)
	require.ErrorContains(t, startErr, "stage=snapshots batches=42")
	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

type cancelAfterPIDWriteFileSvc struct {
	fs.RuntimeFileService
	cancel context.CancelFunc
}

func (s *cancelAfterPIDWriteFileSvc) WriteFile(ctx context.Context, relPath string, data []byte, mode os.FileMode) error {
	err := s.RuntimeFileService.WriteFile(ctx, relPath, data, mode)
	if err == nil && relPath == pidRelPath() {
		s.cancel()
	}
	return err
}

func TestStartOperator_CancellationStopsChild(t *testing.T) {
	t.Setenv(string(constants.EnvVar.TestReexec), "hold")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fileSvc := &cancelAfterPIDWriteFileSvc{RuntimeFileService: newStartFailureFileSvc(t), cancel: cancel}
	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)
	err = pm.StartOperator(ctx, minimalStartOpts())
	require.ErrorIs(t, err, constants.ErrProcessStartFailed)
	require.ErrorIs(t, err, context.Canceled)
	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

func TestStartupDiagnostic_ReadsTailOfCurrentAttempt(t *testing.T) {
	fileSvc := newStartFailureFileSvc(t)
	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)
	log, err := pm.logSvc.OpenLogForAppend(t.Context())
	require.NoError(t, err)
	_, err = log.WriteString("old attempt diagnostic\n")
	require.NoError(t, err)
	start, err := log.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	_, err = log.Write(bytes.Repeat([]byte("progress\n"), maxStartupDiagnosticBytes))
	require.NoError(t, err)
	_, err = log.WriteString("latest verification diagnostic\n")
	require.NoError(t, err)
	require.NoError(t, log.Close())
	assert.Equal(t, "latest verification diagnostic", pm.startupDiagnostic(start))
}

// TestStartOperator_KeyStoreApprovalExtendsHealthWait verifies that a child
// waiting on the user to approve OS key store access (a macOS Keychain prompt
// for a rebuilt binary) is not killed when the normal health budget runs out:
// the user is told what to approve, and the start waits for the child.
func TestStartOperator_KeyStoreApprovalExtendsHealthWait(t *testing.T) {
	t.Setenv(string(constants.EnvVar.TestReexec), "keystore-approval")
	fileSvc := newStartFailureFileSvc(t)

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)
	// A race-instrumented re-exec can take several seconds to reach TestMain
	// under full-suite load. Allow that startup time, while keeping the normal
	// health budget shorter than the child's 20s approval hold. Reaching the
	// child's denial still proves the marker extended the health wait.
	pm.SetHealthCheckParameters(100*time.Millisecond, 150)

	opts := minimalStartOpts()
	var notices bytes.Buffer
	opts.Notices = &notices
	startErr := pm.StartOperator(t.Context(), opts)
	require.Error(t, startErr)
	require.ErrorIs(t, startErr, constants.ErrProcessStartFailed)
	require.ErrorContains(t, startErr, "key store approval denied",
		"the start must wait past the health budget for the child's outcome")
	assert.Contains(t, notices.String(), "Keychain prompt")

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

// freeTCPPort returns a currently free loopback TCP port.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// bindForeignHealthServer binds a minimal health endpoint on port that
// reports a real but wrong PID, emulating a colliding process that answers
// ztd-shaped health checks (for example a re-executed test binary from
// another package that claimed the same free port).
func bindForeignHealthServer(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc(constants.APIPaths.Health, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":"ok","mode":"gateway","posture":"doctrine","pid":%d}`, os.Getpid())
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// bindForeignHealthServerAfterPIDWrite waits until StartOperator has
// completed its port scan and written the PID file, then claims the selected
// port with a foreign health server. The PID file is written after
// findAvailablePort returns, so the scan result is fixed at that point. The
// goroutine gives up silently once StartOperator returns or the deadline
// passes.
func bindForeignHealthServerAfterPIDWrite(t *testing.T, fileSvc fs.RuntimeFileService, port int, done <-chan struct{}) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return
		default:
		}
		exists, err := fileSvc.FileExists(context.Background(), pidRelPath())
		if err != nil {
			return
		}
		if exists {
			bindForeignHealthServer(t, port)
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// startOptsOnFreePorts returns OperatorStartOptions pinned to currently free
// ports so the test knows in advance which port StartOperator will select.
func startOptsOnFreePorts(t *testing.T) *OperatorStartOptions {
	t.Helper()
	opts := minimalStartOpts()
	opts.HTTPPort = freeTCPPort(t)
	opts.HTTPSPort = freeTCPPort(t)
	return opts
}

// TestStartOperator_ForeignHealthServerAfterChildExitIsRejected verifies that
// a foreign process answering ztd-shaped health checks on the selected port is
// not mistaken for the started child. The re-executed child exits immediately
// without binding the port, so a colliding listener can claim it; the health
// check must still fail closed because the responder is not the child.
func TestStartOperator_ForeignHealthServerAfterChildExitIsRejected(t *testing.T) {
	fileSvc := newStartFailureFileSvc(t)
	opts := startOptsOnFreePorts(t)
	httpPort := opts.HTTPPort

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)

	done := make(chan struct{})
	defer close(done)
	startErrCh := make(chan error, 1)
	go func() { startErrCh <- pm.StartOperator(t.Context(), opts) }()
	go bindForeignHealthServerAfterPIDWrite(t, fileSvc, httpPort, done) //nolint:testifylint // shared helper keeps require for the test goroutine

	select {
	case startErr := <-startErrCh:
		require.Error(t, startErr, "StartOperator must not accept a foreign health server as the started child")
		require.ErrorIs(t, startErr, constants.ErrProcessStartFailed,
			"error should wrap ErrProcessStartFailed, got: %v", startErr)
	case <-time.After(60 * time.Second):
		t.Fatal("StartOperator did not return")
	}

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

// TestStartOperator_ForeignHealthServerWhileChildAliveIsRejected verifies the
// same fail-closed behavior while the child is still running: the re-executed
// child in "hold" mode stays alive without binding the selected port, a
// foreign server claims it, and its health response must not satisfy the
// start health check.
func TestStartOperator_ForeignHealthServerWhileChildAliveIsRejected(t *testing.T) {
	t.Setenv(string(constants.EnvVar.TestReexec), "hold")
	fileSvc := newStartFailureFileSvc(t)
	opts := startOptsOnFreePorts(t)
	httpPort := opts.HTTPPort

	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)
	pm.SetHealthCheckParameters(20*time.Millisecond, 3)

	done := make(chan struct{})
	defer close(done)
	startErrCh := make(chan error, 1)
	go func() { startErrCh <- pm.StartOperator(t.Context(), opts) }()
	go bindForeignHealthServerAfterPIDWrite(t, fileSvc, httpPort, done) //nolint:testifylint // shared helper keeps require for the test goroutine

	select {
	case startErr := <-startErrCh:
		require.Error(t, startErr, "StartOperator must not accept a foreign health server as the started child")
		require.ErrorIs(t, startErr, constants.ErrProcessStartFailed,
			"error should wrap ErrProcessStartFailed, got: %v", startErr)
	case <-time.After(60 * time.Second):
		t.Fatal("StartOperator did not return")
	}

	assertNoLiveChildAndNoPIDFile(t, pm, fileSvc)
}

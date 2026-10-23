// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !windows
// +build !windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// mockProcess is a mock implementation of the process interface
type mockProcess struct {
	signalFunc func(sig syscall.Signal) error
}

func (m *mockProcess) Signal(sig syscall.Signal) error {
	if m.signalFunc != nil {
		return m.signalFunc(sig)
	}
	return nil
}

// mockProcessFinder is a mock implementation of the processFinder interface
type mockProcessFinder struct {
	findProcessFunc func(pid int) (process, error)
}

func (m *mockProcessFinder) FindProcess(pid int) (process, error) {
	if m.findProcessFunc != nil {
		return m.findProcessFunc(pid)
	}
	return &mockProcess{}, nil
}

// mockCommandExecutor is a mock implementation of the CommandExecutor interface
type mockCommandExecutor struct {
	commandFunc func(name string, args ...string) *exec.Cmd
	outputFunc  func(cmd *exec.Cmd) ([]byte, error)
	runFunc     func(cmd *exec.Cmd) error
}

func (m *mockCommandExecutor) Command(name string, args ...string) *exec.Cmd {
	if m.commandFunc != nil {
		return m.commandFunc(name, args...)
	}
	// Return a dummy command so we don't return nil
	return exec.Command("echo")
}

func (m *mockCommandExecutor) Output(cmd *exec.Cmd) ([]byte, error) {
	if m.outputFunc != nil {
		return m.outputFunc(cmd)
	}
	return []byte{}, nil
}

func (m *mockCommandExecutor) Run(cmd *exec.Cmd) error {
	if m.runFunc != nil {
		return m.runFunc(cmd)
	}
	return nil
}

// mockExitWatcher is a mock implementation of the exitWatcher interface.
// Its default Wait reports the process as already exited.
type mockExitWatcher struct {
	waitFunc  func(ctx context.Context) error
	closeFunc func() error
}

func (m *mockExitWatcher) Wait(ctx context.Context) error {
	if m.waitFunc != nil {
		return m.waitFunc(ctx)
	}
	return nil
}

func (m *mockExitWatcher) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

// mockExitWatchOpener is a mock implementation of the exitWatchOpener interface
type mockExitWatchOpener struct {
	openFunc func(pid int) (exitWatcher, error)
}

func (m *mockExitWatchOpener) Open(pid int) (exitWatcher, error) {
	if m.openFunc != nil {
		return m.openFunc(pid)
	}
	return &mockExitWatcher{}, nil
}

func TestIsProcessRunningWithFinder(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.TempDir(t)
	fileSvc := newPlatformTestFileSvc(t, tmpDir)
	pm, err := NewProcessManager(fileSvc)
	if err != nil {
		t.Fatalf("NewProcessManager failed: %v", err)
	}

	t.Run("returns false for PID 0", func(t *testing.T) {
		t.Parallel()
		finder := &mockProcessFinder{}
		result := pm.isProcessRunningWithFinder(0, finder)
		if result {
			t.Error("expected false for PID 0")
		}
	})

	t.Run("returns false when FindProcess fails", func(t *testing.T) {
		t.Parallel()
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return nil, errors.New("process not found")
			},
		}
		result := pm.isProcessRunningWithFinder(123, finder)
		if result {
			t.Error("expected false when FindProcess fails")
		}
	})

	t.Run("returns false when Signal fails", func(t *testing.T) {
		t.Parallel()
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return &mockProcess{
					signalFunc: func(sig syscall.Signal) error {
						return errors.New("signal failed")
					},
				}, nil
			},
		}
		result := pm.isProcessRunningWithFinder(123, finder)
		if result {
			t.Error("expected false when Signal fails")
		}
	})

	t.Run("returns true when Signal succeeds", func(t *testing.T) {
		t.Parallel()
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return &mockProcess{
					signalFunc: func(sig syscall.Signal) error {
						return nil
					},
				}, nil
			},
		}
		result := pm.isProcessRunningWithFinder(123, finder)
		if !result {
			t.Error("expected true when Signal succeeds")
		}
	})

	t.Run("returns true for Signal(0) success", func(t *testing.T) {
		t.Parallel()
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return &mockProcess{
					signalFunc: func(sig syscall.Signal) error {
						if sig == syscall.Signal(0) {
							return nil
						}
						return errors.New("unexpected signal")
					},
				}, nil
			},
		}
		result := pm.isProcessRunningWithFinder(123, finder)
		if !result {
			t.Error("expected true for Signal(0) success")
		}
	})
}

func TestFindProcessOnPortWithFactory(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.TempDir(t)
	fileSvc := newPlatformTestFileSvc(t, tmpDir)
	pm, err := NewProcessManager(fileSvc)
	if err != nil {
		t.Fatalf("NewProcessManager failed: %v", err)
	}

	t.Run("returns 0 when command fails", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return nil, errors.New("lsof failed")
			},
		}
		result := pm.findProcessOnPortWithFactory(8080, executor)
		if result != 0 {
			t.Errorf("expected 0 when command fails, got %d", result)
		}
	})

	t.Run("returns 0 when output is malformed", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("not-a-number"), nil
			},
		}
		result := pm.findProcessOnPortWithFactory(8080, executor)
		if result != 0 {
			t.Errorf("expected 0 for malformed output, got %d", result)
		}
	})

	t.Run("returns 0 when output is empty", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte(""), nil
			},
		}
		result := pm.findProcessOnPortWithFactory(8080, executor)
		if result != 0 {
			t.Errorf("expected 0 for empty output, got %d", result)
		}
	})

	t.Run("returns valid PID when output is valid", func(t *testing.T) {
		t.Parallel()
		expectedPID := 12345
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("12345"), nil
			},
		}
		result := pm.findProcessOnPortWithFactory(8080, executor)
		if result != expectedPID {
			t.Errorf("expected %d, got %d", expectedPID, result)
		}
	})

	t.Run("returns valid PID with whitespace", func(t *testing.T) {
		t.Parallel()
		expectedPID := 67890
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("  67890  "), nil
			},
		}
		result := pm.findProcessOnPortWithFactory(8080, executor)
		if result != expectedPID {
			t.Errorf("expected %d, got %d", expectedPID, result)
		}
	})

	t.Run("passes correct port to command", func(t *testing.T) {
		t.Parallel()
		testPort := 9090
		executor := &mockCommandExecutor{
			commandFunc: func(name string, args ...string) *exec.Cmd {
				if name != "lsof" {
					t.Errorf("expected command 'lsof', got '%s'", name)
				}
				if len(args) != 2 || args[0] != "-ti" || args[1] != ":9090" {
					t.Errorf("expected args ['-ti', ':9090'], got %v", args)
				}
				return exec.Command(name, args...)
			},
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("12345"), nil
			},
		}
		pm.findProcessOnPortWithFactory(testPort, executor)
	})
}

func TestFindOperatorProcessWithExecutor(t *testing.T) {
	t.Parallel()
	tmpDir := testutil.TempDir(t)
	fileSvc := newPlatformTestFileSvc(t, tmpDir)
	pm, err := NewProcessManager(fileSvc)
	if err != nil {
		t.Fatalf("NewProcessManager failed: %v", err)
	}

	t.Run("returns 0 when command fails", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return nil, errors.New("pgrep failed")
			},
		}
		result := pm.findOperatorProcessWithExecutor(executor)
		if result != 0 {
			t.Errorf("expected 0 when command fails, got %d", result)
		}
	})

	t.Run("returns 0 when output is malformed", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("invalid"), nil
			},
		}
		result := pm.findOperatorProcessWithExecutor(executor)
		if result != 0 {
			t.Errorf("expected 0 for malformed output, got %d", result)
		}
	})

	t.Run("returns valid PID when output is valid", func(t *testing.T) {
		t.Parallel()
		expectedPID := 54321
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("54321"), nil
			},
		}
		result := pm.findOperatorProcessWithExecutor(executor)
		if result != expectedPID {
			t.Errorf("expected %d, got %d", expectedPID, result)
		}
	})

	t.Run("skips the calling process", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte(fmt.Sprintf("%d\n54321\n", os.Getpid())), nil
			},
		}
		if result := pm.findOperatorProcessWithExecutor(executor); result != 54321 {
			t.Errorf("expected 54321, got %d", result)
		}
	})

	t.Run("returns 0 when only the calling process matches", func(t *testing.T) {
		t.Parallel()
		executor := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte(fmt.Sprintf("%d\n", os.Getpid())), nil
			},
		}
		if result := pm.findOperatorProcessWithExecutor(executor); result != 0 {
			t.Errorf("expected 0, got %d", result)
		}
	})

	t.Run("passes correct arguments to command", func(t *testing.T) {
		t.Parallel()
		expectedPattern := fmt.Sprintf("^[^[:space:]]*ztd gw start.*--data-dir %s([[:space:]]|$)", fileSvc.Resolve(constants.DataDirname))
		executor := &mockCommandExecutor{
			commandFunc: func(name string, args ...string) *exec.Cmd {
				if name != "pgrep" {
					t.Errorf("expected command 'pgrep', got '%s'", name)
				}
				if len(args) != 2 || args[0] != "-f" || args[1] != expectedPattern {
					t.Errorf("expected args ['-f', '%s'], got %v", expectedPattern, args)
				}
				return exec.Command(name, args...)
			},
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return []byte("12345"), nil
			},
		}
		pm.findOperatorProcessWithExecutor(executor)
	})
}

func TestStopProcessWithDeps(t *testing.T) {
	t.Parallel()

	tmpDir := testutil.TempDir(t)
	fileSvc := newPlatformTestFileSvc(t, tmpDir)
	pm, err := NewProcessManager(fileSvc)
	if err != nil {
		t.Fatalf("NewProcessManager failed: %v", err)
	}

	// signalRecorder returns a finder whose processes append each signal to
	// signals and fail with failErr when they receive failOn.
	signalRecorder := func(signals *[]syscall.Signal, failOn syscall.Signal, failErr error) *mockProcessFinder {
		return &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return &mockProcess{
					signalFunc: func(sig syscall.Signal) error {
						*signals = append(*signals, sig)
						if sig == failOn {
							return failErr
						}
						return nil
					},
				}, nil
			},
		}
	}

	// blockUntilDeadline stands in for an exit wait that never sees an exit.
	blockUntilDeadline := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}

	t.Run("returns nil for PID 0", func(t *testing.T) {
		t.Parallel()

		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				t.Error("exit watch should not open for PID 0")
				return nil, errors.New("unexpected open")
			},
		}
		err := pm.stopProcessWithDeps(0, &mockProcessFinder{}, exits, 10*time.Second)
		if err != nil {
			t.Errorf("expected nil for PID 0, got %v", err)
		}
	})

	t.Run("returns nil when process is not running", func(t *testing.T) {
		t.Parallel()

		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return nil, fmt.Errorf("watch: %w", constants.ErrProcessNotFound)
			},
		}
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				t.Error("no process should be signaled when the PID has no process")
				return nil, errors.New("unexpected find")
			},
		}
		err := pm.stopProcessWithDeps(123, finder, exits, 10*time.Second)
		if err != nil {
			t.Errorf("expected nil when process not running, got %v", err)
		}
	})

	t.Run("returns error when exit watch cannot open", func(t *testing.T) {
		t.Parallel()

		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return nil, errors.New("watch unavailable")
			},
		}
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				t.Error("no process should be signaled without an exit watch")
				return nil, errors.New("unexpected find")
			},
		}
		err := pm.stopProcessWithDeps(123, finder, exits, 10*time.Second)
		if err == nil {
			t.Error("expected error when exit watch cannot open")
		}
	})

	t.Run("returns error when FindProcess fails after watch opens", func(t *testing.T) {
		t.Parallel()

		closed := false
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return &mockExitWatcher{
					closeFunc: func() error {
						closed = true
						return nil
					},
				}, nil
			},
		}
		finder := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return nil, errors.New("process not found")
			},
		}
		err := pm.stopProcessWithDeps(123, finder, exits, 10*time.Second)
		if err == nil {
			t.Error("expected error when FindProcess fails")
		}
		if !closed {
			t.Error("exit watch should be closed on return")
		}
	})

	t.Run("returns error when SIGTERM fails", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		finder := signalRecorder(&signals, syscall.SIGTERM, errors.New("SIGTERM failed"))
		err := pm.stopProcessWithDeps(123, finder, &mockExitWatchOpener{}, 10*time.Second)
		if err == nil {
			t.Error("expected error when SIGTERM fails")
		}
		if len(signals) != 1 || signals[0] != syscall.SIGTERM {
			t.Errorf("expected SIGTERM only, got %v", signals)
		}
	})

	t.Run("opens exit watch before SIGTERM and returns nil when process exits", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				if len(signals) != 0 {
					t.Error("exit watch must open before any signal is sent")
				}
				// The default Wait reports the exit at once.
				return &mockExitWatcher{}, nil
			},
		}
		finder := signalRecorder(&signals, syscall.SIGTERM, nil)
		err := pm.stopProcessWithDeps(123, finder, exits, 10*time.Second)
		if err != nil {
			t.Errorf("expected nil when process exits after SIGTERM, got %v", err)
		}
		if len(signals) != 1 || signals[0] != syscall.SIGTERM {
			t.Errorf("expected SIGTERM only, got %v", signals)
		}
	})

	t.Run("sends SIGKILL after timeout", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		waits := 0
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return &mockExitWatcher{
					waitFunc: func(ctx context.Context) error {
						waits++
						if waits == 1 {
							return blockUntilDeadline(ctx)
						}
						return nil
					},
				}, nil
			},
		}
		finder := signalRecorder(&signals, syscall.SIGKILL, nil)
		err := pm.stopProcessWithDeps(123, finder, exits, 50*time.Millisecond)
		if err != nil {
			t.Errorf("expected nil when process exits after SIGKILL, got %v", err)
		}
		if len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
			t.Errorf("expected SIGTERM then SIGKILL, got %v", signals)
		}
	})

	t.Run("returns error when SIGKILL fails", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return &mockExitWatcher{waitFunc: blockUntilDeadline}, nil
			},
		}
		finder := signalRecorder(&signals, syscall.SIGKILL, errors.New("SIGKILL failed"))
		err := pm.stopProcessWithDeps(123, finder, exits, 50*time.Millisecond)
		if err == nil {
			t.Error("expected error when SIGKILL fails")
		}
	})

	t.Run("returns SIGKILL timeout error when process survives SIGKILL", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		waits := 0
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return &mockExitWatcher{
					waitFunc: func(ctx context.Context) error {
						waits++
						if waits == 1 {
							return blockUntilDeadline(ctx)
						}
						// The SIGKILL wait is bounded; the test does not sit out its budget.
						if _, ok := ctx.Deadline(); !ok {
							t.Error("SIGKILL wait should be bounded by a deadline")
						}
						return context.DeadlineExceeded
					},
				}, nil
			},
		}
		finder := signalRecorder(&signals, syscall.SIGTERM, nil)
		err := pm.stopProcessWithDeps(123, finder, exits, 50*time.Millisecond)
		if !errors.Is(err, constants.ErrProcessSigKillTimeout) {
			t.Errorf("expected ErrProcessSigKillTimeout, got %v", err)
		}
	})

	t.Run("returns error without SIGKILL when exit cannot be observed", func(t *testing.T) {
		t.Parallel()

		var signals []syscall.Signal
		exits := &mockExitWatchOpener{
			openFunc: func(pid int) (exitWatcher, error) {
				return &mockExitWatcher{
					waitFunc: func(ctx context.Context) error {
						return errors.New("poll failed")
					},
				}, nil
			},
		}
		finder := signalRecorder(&signals, syscall.SIGTERM, nil)
		err := pm.stopProcessWithDeps(123, finder, exits, 10*time.Second)
		if err == nil {
			t.Error("expected error when exit cannot be observed")
		}
		if len(signals) != 1 || signals[0] != syscall.SIGTERM {
			t.Errorf("expected SIGTERM only, got %v", signals)
		}
	})
}

func TestMockProcess(t *testing.T) {
	t.Parallel()
	t.Run("mockProcess Signal returns nil by default", func(t *testing.T) {
		t.Parallel()
		p := &mockProcess{}
		err := p.Signal(syscall.SIGTERM)
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("mockProcess Signal uses custom function", func(t *testing.T) {
		t.Parallel()
		expectedErr := errors.New("custom error")
		p := &mockProcess{
			signalFunc: func(sig syscall.Signal) error {
				return expectedErr
			},
		}
		err := p.Signal(syscall.SIGTERM)
		if err != expectedErr {
			t.Errorf("expected %v, got %v", expectedErr, err)
		}
	})
}

func TestMockProcessFinder(t *testing.T) {
	t.Parallel()

	t.Run("mockProcessFinder FindProcess returns mock by default", func(t *testing.T) {
		t.Parallel()

		f := &mockProcessFinder{}
		p, err := f.FindProcess(123)
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
		if p == nil {
			t.Error("expected non-nil process")
		}
	})

	t.Run("mockProcessFinder FindProcess uses custom function", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("custom error")
		f := &mockProcessFinder{
			findProcessFunc: func(pid int) (process, error) {
				return nil, expectedErr
			},
		}
		_, err := f.FindProcess(123)
		if err != expectedErr {
			t.Errorf("expected %v, got %v", expectedErr, err)
		}
	})
}

func TestMockCommandExecutor(t *testing.T) {
	t.Parallel()
	t.Run("mockCommandExecutor Output returns empty by default", func(t *testing.T) {
		t.Parallel()
		e := &mockCommandExecutor{}
		output, err := e.Output(nil)
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
		if len(output) != 0 {
			t.Errorf("expected empty output, got %v", output)
		}
	})

	t.Run("mockCommandExecutor Output uses custom function", func(t *testing.T) {
		t.Parallel()
		expectedOutput := []byte("test output")
		expectedErr := errors.New("custom error")
		e := &mockCommandExecutor{
			outputFunc: func(cmd *exec.Cmd) ([]byte, error) {
				return expectedOutput, expectedErr
			},
		}
		output, err := e.Output(nil)
		if err != expectedErr {
			t.Errorf("expected %v, got %v", expectedErr, err)
		}
		if string(output) != string(expectedOutput) {
			t.Errorf("expected %v, got %v", expectedOutput, output)
		}
	})
}

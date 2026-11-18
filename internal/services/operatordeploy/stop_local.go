// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/processwatch"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// LocalOperatorProcess is an OS-discovered local `ztd operator start` worker
// pinned by a process handle or pidfd.
type LocalOperatorProcess struct {
	PID       int
	Dir       string
	SessionID string
	Wait      func(time.Duration) (bool, error)
	Signal    func(bool) error // false: TERM, true: KILL
	Close     func()
}

// DefaultLocalOperatorDiscoverer discovers running local Operator workers.
var DefaultLocalOperatorDiscoverer = DiscoverLocalOperators

// ExitWait returns a wait function for watch that reports whether the worker
// exited within timeout. A timeout of zero or less checks the current state.
func ExitWait(watch *processwatch.ProcessExitWatch) func(time.Duration) (bool, error) {
	return func(timeout time.Duration) (bool, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		err := watch.Wait(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return false, nil
		}
		return err == nil, err
	}
}

// LocalOperatorSessionID reads the session the worker in dir recorded at
// enrollment, or returns "" to fall back to hostname and working-directory matching.
func LocalOperatorSessionID(dir string) string {
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	if err != nil {
		return ""
	}
	state, err := ReadOperatorDeploymentState(context.Background(), fileSvc)
	if err != nil || state == nil {
		return ""
	}
	return state.OperatorSessionID
}

// IsZtdExecutable reports whether base names the ztd binary.
func IsZtdExecutable(base string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(base, constants.BinaryImageName) || strings.EqualFold(base, constants.BinaryImageNameWindows)
	}
	return base == constants.BinaryImageName
}

// IsLocalOperatorArgv reports whether argv invokes `ztd operator start`.
func IsLocalOperatorArgv(args []string) bool {
	return len(args) >= 3 && IsZtdExecutable(filepath.Base(args[0])) && (args[1] == "operator" || args[1] == "operators") && args[2] == "start"
}

// OperatorArgValue returns the value passed for flag in argv, or "".
func OperatorArgValue(args []string, flag string) string {
	for i := 3; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1]
		}
		if value, ok := strings.CutPrefix(args[i], flag+"="); ok {
			return value
		}
	}
	return ""
}

// LocalOperatorDir resolves a local worker's directory the way operator start
// does: --working-dir with ~ expanded and relative paths taken from cwd.
func LocalOperatorDir(args []string, cwd string) string {
	dir := OperatorArgValue(args, "--working-dir")
	if expanded, err := ResolveHomePath(dir); err == nil {
		dir = expanded
	}
	if dir == "" {
		dir = cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir)
}

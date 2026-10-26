// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/spf13/cobra"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

type localOperatorProcess struct {
	pid       int
	dir       string
	sessionID string
	wait      func(time.Duration) (bool, error)
	signal    func(bool) error // false: TERM, true: KILL
	close     func()
}

// exitWait returns the wait for a watched worker. It reports whether the worker
// exited within the timeout, and a timeout of zero or less checks the current
// state only.
func exitWait(watch *platform.ProcessExitWatch) func(time.Duration) (bool, error) {
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

func (p localOperatorProcess) matches(op *operatorv1.OperatorDocument) bool {
	if p.sessionID != "" {
		return p.sessionID == op.OperatorSessionId
	}
	hostname, err := os.Hostname()
	return err == nil && operatorHostnameValue(op) == hostname && op.LocalDir != "" && filepath.IsAbs(op.LocalDir) && filepath.Clean(op.LocalDir) == p.dir
}

// localOperatorSessionID reads the session the worker in dir recorded at
// enrollment. Each launch resets the record, so it names the live process;
// "" falls back to hostname and working-directory matching.
func localOperatorSessionID(dir string) string {
	fileSvc, err := fs.NewRuntimeFileService(dir, slog.Default())
	if err != nil {
		return ""
	}
	state, err := readOperatorDeploymentState(context.Background(), fileSvc)
	if err != nil || state == nil {
		return ""
	}
	return state.OperatorSessionID
}

// isZtdExecutable reports whether base names the ztd binary. Windows file
// names carry .exe and are case-insensitive.
func isZtdExecutable(base string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(base, constants.BinaryImageName) || strings.EqualFold(base, constants.BinaryImageNameWindows)
	}
	return base == constants.BinaryImageName
}

func isLocalOperatorArgv(args []string) bool {
	return len(args) >= 3 && isZtdExecutable(filepath.Base(args[0])) && (args[1] == "operator" || args[1] == "operators") && args[2] == "start"
}

func operatorArgValue(args []string, flag string) string {
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

// localOperatorDir resolves a local worker's directory the way operator start
// does: --working-dir with ~ expanded and relative paths taken from the
// worker's cwd, else the cwd itself.
func localOperatorDir(args []string, cwd string) string {
	dir := operatorArgValue(args, "--working-dir")
	if expanded, err := resolveHomePath(dir); err == nil {
		dir = expanded
	}
	if dir == "" {
		dir = cwd
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return filepath.Clean(dir)
}

type operatorStopResult struct {
	models.StopOperatorResponse
	PID           int    `json:"pid"`
	Method        string `json:"method"`
	Error         string `json:"error,omitempty"`
	GovernedError string `json:"governed_error,omitempty"`
}

func stopLocalOperator(cmd *cobra.Command, p localOperatorProcess, response models.StopOperatorResponse, governedErr error, grace time.Duration) operatorStopResult {
	result := operatorStopResult{StopOperatorResponse: response, PID: p.pid}
	if governedErr != nil {
		result.GovernedError = governedErr.Error()
	}
	fail := func(err error) operatorStopResult {
		result.Success = false
		result.Method = "failed"
		result.Error = err.Error()
		return result
	}
	// Even an unsuccessful request might have delivered shutdown before the response failed.
	exited, err := p.wait(grace)
	if err != nil {
		return fail(err)
	}
	if exited {
		result.Success = true
		result.Method = "exited"
		return result
	}
	if err := cmd.Context().Err(); err != nil {
		return fail(err)
	}
	if err := p.signal(false); err != nil {
		return fail(fmt.Errorf("send TERM: %w", err))
	}
	exited, err = p.wait(grace)
	if err != nil {
		return fail(err)
	}
	if exited {
		result.Success = true
		result.Method = "TERM"
		return result
	}
	if err := cmd.Context().Err(); err != nil {
		return fail(err)
	}
	if err := p.signal(true); err != nil {
		return fail(fmt.Errorf("send KILL: %w", err))
	}
	exited, err = p.wait(2 * time.Second)
	if err != nil {
		return fail(err)
	}
	if !exited {
		return fail(constants.ErrProcessSigKillTimeout)
	}
	result.Success = true
	result.Method = "KILL"
	return result
}

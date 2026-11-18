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
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// EndpointPattern restricts Gateway hostnames/IPs accepted by preflight and
// worker startup flags (ports travel separately).
var EndpointPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// RemoteDirPattern restricts remote staging directories interpolated into SSH
// commands; a leading ~ stays unquoted so the remote shell expands it.
var RemoteDirPattern = regexp.MustCompile(`^(~|~?/?[A-Za-z0-9_.-]+)(/[A-Za-z0-9_.-]+)*/?$`)

// ExecuteDeployHost executes a typed DeployHostRequest and returns a typed DeployHostResponse.
func ExecuteDeployHost(ctx context.Context, req models.DeployHostRequest, factory func(string, *slog.Logger) (fs.RuntimeFileService, error)) (models.DeployHostResponse, error) {
	if factory == nil {
		factory = func(workingDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
			return fs.NewRuntimeFileService(workingDir, logger)
		}
	}
	var resp models.DeployHostResponse

	switch req.Action {
	case models.DeployHostActionPrepare:
		dirs, err := ExecuteDeployHostPrepare(ctx, req.Dirs)
		if err != nil {
			return resp, err
		}
		resp.ResolvedDirs = dirs
		return resp, nil

	case models.DeployHostActionInstall:
		target := req.Target
		if target == "" && req.DestDir != "" {
			target = filepath.Join(req.DestDir, constants.DeployBinDirname, constants.BinaryImageName)
		}
		if err := ExecuteDeployHostInstall(ctx, req.Source, target); err != nil {
			return resp, err
		}
		resolvedTarget, _ := ResolveHomePath(target)
		if absTarget, err := filepath.Abs(resolvedTarget); err == nil {
			resp.ResolvedDirs = []string{absTarget}
		}
		return resp, nil

	case models.DeployHostActionLink:
		binaryDir := req.BinaryDir
		if binaryDir == "" && req.Source != "" {
			binaryDir = req.Source
		}
		destDir := req.DestDir
		if destDir == "" && req.Target != "" {
			destDir = req.Target
		}
		if destDir == "" && req.WorkingDir != "" {
			destDir = req.WorkingDir
		}
		if err := ExecuteDeployHostLink(ctx, binaryDir, destDir, req.Binary); err != nil {
			return resp, err
		}
		return resp, nil

	case models.DeployHostActionStart:
		workingDir := req.WorkingDir
		if workingDir == "" {
			workingDir = req.DestDir
		}
		pid, err := ExecuteDeployHostStart(ctx, workingDir, req.Binary, req.Args)
		if err != nil {
			return resp, err
		}
		resp.PID = pid
		return resp, nil

	case models.DeployHostActionState:
		workingDir := req.WorkingDir
		if workingDir == "" {
			workingDir = req.DestDir
		}
		fileSvc, err := factory(workingDir, slog.Default())
		if err != nil {
			return resp, fmt.Errorf("operator deploy host: %w: %w", constants.ErrFileServiceInit, err)
		}
		state, err := ReadOperatorDeploymentState(ctx, fileSvc)
		if err != nil {
			return resp, err
		}
		resp.State = state
		return resp, nil

	case models.DeployHostActionPreflight:
		args := req.PreflightArgs
		if len(args) == 0 {
			args = req.Args
		}
		if err := ExecuteDeployHostPreflight(ctx, args); err != nil {
			return resp, err
		}
		return resp, nil

	case models.DeployHostActionStop:
		pids, err := ExecuteDeployHostStop(ctx, req.DestDir, req.WorkingDir)
		if err != nil {
			return resp, err
		}
		resp.StoppedPIDs = pids
		resp.StoppedCount = len(pids)
		return resp, nil

	default:
		return resp, fmt.Errorf("unknown deploy-host action: %q: %w", req.Action, constants.ErrOperatorDeployFailed)
	}
}

// ExecuteDeployHostPrepare resolves ~ with os.UserHomeDir, creates directories with PermDirPrivate,
// and returns their absolute resolved paths.
func ExecuteDeployHostPrepare(ctx context.Context, dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, fmt.Errorf("operator deploy-host prepare: dirs list must not be empty: %w", constants.ErrMissingRequiredField)
	}
	var resolvedDirs []string
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clean, err := ResolveHomePath(dir)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(clean, constants.PermDirPrivate); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", clean, err)
		}
		absDir, err := filepath.Abs(clean)
		if err != nil {
			return nil, fmt.Errorf("resolve abs dir for %s: %w", clean, err)
		}
		resolved, err := filepath.EvalSymlinks(absDir)
		if err != nil {
			return nil, fmt.Errorf("eval symlinks for %s: %w", absDir, err)
		}
		resolvedDirs = append(resolvedDirs, resolved)
	}
	return resolvedDirs, nil
}

// ExecuteDeployHostInstall copies a binary from source to target atomically and ETXTBSY-safe.
func ExecuteDeployHostInstall(ctx context.Context, source, target string) error {
	if source == "" {
		return fmt.Errorf("operator deploy-host install: source path required: %w", constants.ErrMissingRequiredField)
	}
	if target == "" {
		return fmt.Errorf("operator deploy-host install: target path required: %w", constants.ErrMissingRequiredField)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	source, err = ResolveHomePath(source)
	if err != nil {
		return err
	}
	target, err = ResolveHomePath(target)
	if err != nil {
		return err
	}

	targetDir := filepath.Dir(target)
	if err := os.MkdirAll(targetDir, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("create target dir %s: %w", targetDir, err)
	}

	srcFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source binary %s: %w", source, err)
	}
	defer srcFile.Close()

	tmp, err := os.CreateTemp(targetDir, constants.OperatorInstallStagingTempPattern)
	if err != nil {
		return fmt.Errorf("create install staging in %s: %w", targetDir, err)
	}
	staging := tmp.Name()
	defer func() { _ = os.Remove(staging) }()

	if _, err := io.Copy(tmp, srcFile); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("copy binary to staging %s: %w", staging, err)
	}
	if err := tmp.Chmod(constants.PermFileExecutable); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod executable staging %s: %w", staging, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close staging file %s: %w", staging, err)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("rename %s to %s: %w", staging, target, err)
	}
	return nil
}

// ExecuteDeployHostLink hard-links a binary from binaryDir into destDir/binary via a staging name.
func ExecuteDeployHostLink(ctx context.Context, binaryDir, destDir, binary string) error {
	if binary == "" {
		binary = constants.BinaryImageName
	}
	if binaryDir == "" {
		return fmt.Errorf("operator deploy-host link: binary_dir required: %w", constants.ErrMissingRequiredField)
	}
	if destDir == "" {
		return fmt.Errorf("operator deploy-host link: dest_dir required: %w", constants.ErrMissingRequiredField)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	binaryDir, err = ResolveHomePath(binaryDir)
	if err != nil {
		return err
	}
	destDir, err = ResolveHomePath(destDir)
	if err != nil {
		return err
	}

	sourceBinary := binaryDir
	if filepath.Base(binaryDir) != binary {
		sourceBinary = filepath.Join(binaryDir, binary)
	}
	if _, err := os.Stat(sourceBinary); err != nil {
		return fmt.Errorf("source binary %s not found: %w", sourceBinary, err)
	}

	if err := os.MkdirAll(destDir, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("create dest dir %s: %w", destDir, err)
	}

	tmp, err := os.CreateTemp(destDir, constants.OperatorLinkStagingTempPattern)
	if err != nil {
		return fmt.Errorf("create temp link staging in %s: %w", destDir, err)
	}
	staging := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(staging) }()
	_ = os.Remove(staging)

	if err := os.Link(sourceBinary, staging); err != nil {
		return fmt.Errorf("hard link %s to %s: %w", sourceBinary, staging, err)
	}
	destBinary := filepath.Join(destDir, binary)
	if err := os.Rename(staging, destBinary); err != nil {
		return fmt.Errorf("rename link %s to %s: %w", staging, destBinary, err)
	}
	return nil
}

// ExecuteDeployHostStart stops any previous worker in workingDir, starts a new worker detached,
// writes operator.pid, and returns the new PID.
func ExecuteDeployHostStart(ctx context.Context, workingDir, binary string, args []string) (int, error) {
	if workingDir == "" {
		return 0, fmt.Errorf("operator deploy-host start: working_dir required: %w", constants.ErrMissingRequiredField)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	resolvedWorkingDir, err := ResolveHomePath(workingDir)
	if err != nil {
		return 0, err
	}
	absWorkingDir, err := filepath.Abs(resolvedWorkingDir)
	if err != nil {
		return 0, fmt.Errorf("resolve abs working dir for %s: %w", workingDir, err)
	}
	workingDir = filepath.Clean(absWorkingDir)

	binName := constants.BinaryImageName
	if runtime.GOOS == "windows" {
		binName = constants.BinaryImageNameWindows
	}
	binPath := binary
	if binPath == "" {
		binPath = filepath.Join(workingDir, binName)
	} else if !filepath.IsAbs(binPath) {
		binPath = filepath.Join(workingDir, binPath)
	}
	if !IsZtdExecutable(filepath.Base(binPath)) {
		return 0, fmt.Errorf("operator deploy-host start: %w: Operator executable must be named ztd", constants.ErrOperatorDeployFailed)
	}
	if _, err := os.Stat(binPath); err != nil {
		return 0, fmt.Errorf("operator binary %s not found: %w", binPath, err)
	}

	if _, err := ExecuteDeployHostStop(ctx, "", workingDir); err != nil {
		return 0, fmt.Errorf("stop previous Operator in %s: %w", workingDir, err)
	}

	logPath := filepath.Join(workingDir, constants.DeployStartLogFilename)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, constants.PermFilePrivate)
	if err != nil {
		return 0, fmt.Errorf("open start log %s: %w", logPath, err)
	}
	defer logFile.Close()

	var cmdArgs []string
	if len(args) >= 2 && (args[0] == "operator" || args[0] == "operators") && args[1] == "start" {
		cmdArgs = append(cmdArgs, args...)
	} else if len(args) >= 1 && (args[0] == "operator" || args[0] == "operators") {
		cmdArgs = append([]string{args[0], "start"}, args[1:]...)
	} else {
		cmdArgs = append([]string{"operator", "start"}, args...)
	}

	hasWorkingDir := false
	for _, a := range cmdArgs {
		if a == "--working-dir" || strings.HasPrefix(a, "--working-dir=") {
			hasWorkingDir = true
			break
		}
	}
	if !hasWorkingDir {
		cmdArgs = append(cmdArgs, "--working-dir", workingDir)
	}

	worker := exec.Command(binPath, cmdArgs...)
	if runtime.GOOS == "windows" {
		worker.Args[0] = `.\` + filepath.Base(binPath)
	} else {
		worker.Args[0] = "./" + filepath.Base(binPath)
	}
	worker.Dir = workingDir
	worker.Stdout = logFile
	worker.Stderr = logFile
	DetachDeployedOperator(worker)

	if err := worker.Start(); err != nil {
		return 0, fmt.Errorf("start Operator process: %w", err)
	}

	pid := worker.Process.Pid
	pidFile := filepath.Join(workingDir, constants.OperatorPIDFilename)
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", pid)), constants.PermFilePrivate); err != nil {
		_ = worker.Process.Kill()
		_ = worker.Wait()
		return 0, fmt.Errorf("write pid file %s: %w", pidFile, err)
	}
	_ = worker.Process.Release()
	return pid, nil
}

// ExecuteDeployHostPreflight parses gateway-preflight flags and verifies the
// Gateway bootstrap websocket and mTLS port are reachable.
func ExecuteDeployHostPreflight(ctx context.Context, preflightArgs []string) error {
	fs := pflag.NewFlagSet("gateway-preflight", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var httpPort, httpsPort int
	fs.IntVar(&httpPort, "gateway-http-port", 0, "")
	fs.IntVar(&httpsPort, "gateway-https-port", 0, "")
	if err := fs.Parse(preflightArgs); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) != 1 {
		return fmt.Errorf("gateway-preflight requires 1 argument (got %d): %w", len(args), constants.ErrMissingRequiredField)
	}
	if !EndpointPattern.MatchString(args[0]) {
		return fmt.Errorf("%w: gateway host %q must match %s", constants.ErrPathValidation, args[0], EndpointPattern)
	}
	if err := ValidateGatewayPort("gateway-http-port", httpPort); err != nil {
		return err
	}
	if err := ValidateGatewayPort("gateway-https-port", httpsPort); err != nil {
		return err
	}
	return RunGatewayPreflight(ctx, args[0], httpPort, httpsPort)
}

// ExecuteDeployHostStop stops every worker under destDir or matching workingDir concurrently.
func ExecuteDeployHostStop(ctx context.Context, destDir, workingDir string) ([]int, error) {
	if destDir == "" && workingDir == "" {
		return nil, fmt.Errorf("operator deploy-host stop: dest_dir or working_dir required: %w", constants.ErrMissingRequiredField)
	}

	var cleanDest, cleanWork string
	if destDir != "" {
		if r, err := ResolveHomePath(destDir); err == nil {
			destDir = r
		}
		abs, err := filepath.Abs(destDir)
		if err == nil {
			cleanDest = filepath.Clean(abs)
		} else {
			cleanDest = filepath.Clean(destDir)
		}
	}
	if workingDir != "" {
		if r, err := ResolveHomePath(workingDir); err == nil {
			workingDir = r
		}
		abs, err := filepath.Abs(workingDir)
		if err == nil {
			cleanWork = filepath.Clean(abs)
		} else {
			cleanWork = filepath.Clean(workingDir)
		}
	}

	matchesDir := func(dir string) bool {
		c := filepath.Clean(dir)
		if cleanWork != "" && c == cleanWork {
			return true
		}
		if cleanDest != "" && (c == cleanDest || strings.HasPrefix(c, cleanDest+string(filepath.Separator))) {
			return true
		}
		return false
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var stoppedPIDs []int
	var mu sync.Mutex

	// PID files are metadata only. Platform discovery returns process handles
	// bound to verified local Operator identities and is the sole stop authority.
	locals, err := DefaultLocalOperatorDiscoverer()
	if err != nil {
		return nil, fmt.Errorf("operator deploy-host stop: discover local Operators: %w", err)
	}
	var matched []LocalOperatorProcess
	for _, p := range locals {
		if matchesDir(p.Dir) {
			matched = append(matched, p)
		} else if p.Close != nil {
			p.Close()
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	errCh := make(chan error, len(matched))
	for _, p := range matched {
		wg.Add(1)
		go func(proc LocalOperatorProcess) {
			defer wg.Done()
			defer func() {
				if proc.Close != nil {
					proc.Close()
				}
			}()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			}
			if err := ctx.Err(); err != nil {
				errCh <- err
				return
			}
			if err := proc.Signal(false); err != nil {
				errCh <- fmt.Errorf("send TERM to Operator PID %d: %w", proc.PID, err)
				return
			}
			exited, err := proc.Wait(2 * time.Second)
			if err != nil {
				errCh <- fmt.Errorf("wait for Operator PID %d after TERM: %w", proc.PID, err)
				return
			}
			if !exited {
				if err := ctx.Err(); err != nil {
					errCh <- err
					return
				}
				if err := proc.Signal(true); err != nil {
					errCh <- fmt.Errorf("send KILL to Operator PID %d: %w", proc.PID, err)
					return
				}
				exited, err = proc.Wait(2 * time.Second)
				if err != nil {
					errCh <- fmt.Errorf("wait for Operator PID %d after KILL: %w", proc.PID, err)
					return
				}
				if !exited {
					errCh <- fmt.Errorf("%w: Operator PID %d did not exit after KILL", constants.ErrProcessStopFailed, proc.PID)
					return
				}
			}
			if err := os.Remove(filepath.Join(proc.Dir, constants.OperatorPIDFilename)); err != nil && !errors.Is(err, os.ErrNotExist) {
				errCh <- fmt.Errorf("remove Operator PID metadata for PID %d: %w", proc.PID, err)
				return
			}
			mu.Lock()
			stoppedPIDs = append(stoppedPIDs, proc.PID)
			mu.Unlock()
		}(p)
	}
	wg.Wait()
	close(errCh)
	var stopErr error
	for err := range errCh {
		stopErr = errors.Join(stopErr, err)
	}
	if stopErr != nil {
		return nil, stopErr
	}

	sort.Ints(stoppedPIDs)
	return stoppedPIDs, nil
}

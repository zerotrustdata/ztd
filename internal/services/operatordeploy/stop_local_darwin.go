//go:build darwin

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ebitengine/purego"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/processwatch"
	"golang.org/x/sys/unix"
)

const (
	libSystemPath = "/usr/lib/libSystem.B.dylib"

	procPIDVnodePathInfo    = 9
	procVnodeInfoSize       = 152
	procVnodePathInfoSize   = 2 * (procVnodeInfoSize + unix.PathMax)
	procPIDPathInfoMaxSize  = 4 * unix.PathMax
	procArgsArgcFieldLength = 4
)

type libprocAPI struct {
	pidPath func(pid int32, buffer *byte, size uint32) int32
	pidInfo func(pid int32, flavor int32, arg uint64, buffer *byte, size int32) int32
}

var loadLibproc = sync.OnceValues(func() (*libprocAPI, error) {
	lib, err := purego.Dlopen(libSystemPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("operator stop: load libproc: %w", err)
	}
	api := &libprocAPI{}
	for name, fptr := range map[string]any{"proc_pidpath": &api.pidPath, "proc_pidinfo": &api.pidInfo} {
		sym, err := purego.Dlsym(lib, name)
		if err != nil {
			return nil, fmt.Errorf("operator stop: %s: %w", name, err)
		}
		purego.RegisterFunc(fptr, sym)
	}
	return api, nil
})

// DiscoverLocalOperators discovers running local Operator processes via sysctl and libproc.
func DiscoverLocalOperators() ([]LocalOperatorProcess, error) {
	api, err := loadLibproc()
	if err != nil {
		return nil, err
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local Operators: %w", err)
	}
	var processes []LocalOperatorProcess
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 1 || pid == os.Getpid() {
			continue
		}
		if args, err := processArgs(pid); err != nil || !IsLocalOperatorArgv(args) {
			continue
		}
		p, ok, err := watchLocalOperator(api, pid, procs[i].Proc.P_starttime)
		if err != nil {
			for _, p := range processes {
				p.Close()
			}
			return nil, err
		}
		if ok {
			processes = append(processes, p)
		}
	}
	return processes, nil
}

func watchLocalOperator(api *libprocAPI, pid int, started unix.Timeval) (LocalOperatorProcess, bool, error) {
	watch, err := processwatch.WatchProcessExit(pid)
	if errors.Is(err, constants.ErrProcessNotFound) {
		return LocalOperatorProcess{}, false, nil
	}
	if err != nil {
		return LocalOperatorProcess{}, false, fmt.Errorf("operator stop: watch PID %d: %w", pid, err)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	args, argsErr := processArgs(pid)
	executable, exeErr := processPath(api, pid)
	if err != nil || argsErr != nil || exeErr != nil || info.Proc.P_starttime != started ||
		!IsLocalOperatorArgv(args) || !IsZtdExecutable(filepath.Base(executable)) {
		_ = watch.Close()
		return LocalOperatorProcess{}, false, nil
	}
	cwd, _ := processCwd(api, pid)
	dir := LocalOperatorDir(args, cwd)

	wait := ExitWait(watch)
	return LocalOperatorProcess{
		PID: pid, Dir: dir, SessionID: LocalOperatorSessionID(dir),
		Close: func() { _ = watch.Close() },
		Signal: func(force bool) error {
			if done, err := wait(0); err != nil || done {
				return err
			}
			sig := unix.SIGTERM
			if force {
				sig = unix.SIGKILL
			}
			if err := unix.Kill(pid, sig); err != nil && !errors.Is(err, unix.ESRCH) {
				return err
			}
			return nil
		},
		Wait: wait,
	}, true, nil
}

func processArgs(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(raw) < procArgsArgcFieldLength {
		return nil, unix.EINVAL
	}
	argc := int(binary.NativeEndian.Uint32(raw[:procArgsArgcFieldLength]))
	rest := raw[procArgsArgcFieldLength:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil, unix.EINVAL
	}
	rest = bytes.TrimLeft(rest[end:], "\x00")
	args := make([]string, 0, argc)
	for len(args) < argc {
		end = bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, unix.EINVAL
		}
		args = append(args, string(rest[:end]))
		rest = rest[end+1:]
	}
	return args, nil
}

func processPath(api *libprocAPI, pid int) (string, error) {
	buf := make([]byte, procPIDPathInfoMaxSize)
	n := api.pidPath(int32(pid), &buf[0], uint32(len(buf)))
	if n <= 0 {
		return "", fmt.Errorf("proc_pidpath %d: %w", pid, unix.ESRCH)
	}
	return string(buf[:n]), nil
}

func processCwd(api *libprocAPI, pid int) (string, error) {
	buf := make([]byte, procVnodePathInfoSize)
	if n := api.pidInfo(int32(pid), procPIDVnodePathInfo, 0, &buf[0], int32(len(buf))); n != int32(len(buf)) {
		return "", fmt.Errorf("proc_pidinfo %d: %w", pid, unix.ESRCH)
	}
	path := buf[procVnodeInfoSize : procVnodeInfoSize+unix.PathMax]
	return unix.ByteSliceToString(path), nil
}

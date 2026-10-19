//go:build darwin

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const (
	libSystemPath = "/usr/lib/libSystem.B.dylib"

	// <sys/proc_info.h>: PROC_PIDVNODEPATHINFO returns struct
	// proc_vnodepathinfo, two vnode_info_path records (cwd, then root), each a
	// 152-byte vnode_info followed by a MAXPATHLEN path.
	procPIDVnodePathInfo    = 9
	procVnodeInfoSize       = 152
	procVnodePathInfoSize   = 2 * (procVnodeInfoSize + unix.PathMax)
	procPIDPathInfoMaxSize  = 4 * unix.PathMax
	procArgsArgcFieldLength = 4
)

// libprocAPI is the libproc surface discovery needs, bound at runtime without cgo.
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

// Register a kqueue exit watch before confirming identity, so a PID reused
// between listing and registration is detected by its start time, and a
// recorded exit stops any later signal. Inspect argv tokens, never a substring
// of a shell command or a parent PID.
func discoverLocalOperators() ([]localOperatorProcess, error) {
	api, err := loadLibproc()
	if err != nil {
		return nil, err
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Geteuid())
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local workers: %w", err)
	}
	var processes []localOperatorProcess
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 1 || pid == os.Getpid() {
			continue
		}
		if args, err := processArgs(pid); err != nil || !isLocalOperatorArgv(args) {
			continue
		}
		p, ok, err := watchLocalOperator(api, pid, procs[i].Proc.P_starttime)
		if err != nil {
			for _, p := range processes {
				p.close()
			}
			return nil, err
		}
		if ok {
			processes = append(processes, p)
		}
	}
	return processes, nil
}

func watchLocalOperator(api *libprocAPI, pid int, started unix.Timeval) (localOperatorProcess, bool, error) {
	kq, err := watchExit(pid)
	if errors.Is(err, unix.ESRCH) {
		return localOperatorProcess{}, false, nil
	}
	if err != nil {
		return localOperatorProcess{}, false, fmt.Errorf("operator stop: watch PID %d: %w", pid, err)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	args, argsErr := processArgs(pid)
	executable, exeErr := processPath(api, pid)
	if err != nil || argsErr != nil || exeErr != nil || info.Proc.P_starttime != started ||
		!isLocalOperatorArgv(args) || !isZtdExecutable(filepath.Base(executable)) {
		_ = unix.Close(kq)
		return localOperatorProcess{}, false, nil
	}
	cwd, _ := processCwd(api, pid)
	dir := localOperatorDir(args, cwd)

	exited := false
	wait := func(timeout time.Duration) (bool, error) {
		if exited {
			return true, nil
		}
		ok, err := waitExit(kq, timeout)
		exited = ok
		return ok, err
	}
	return localOperatorProcess{
		pid: pid, dir: dir, sessionID: localOperatorSessionID(dir),
		close: func() { _ = unix.Close(kq) },
		signal: func(force bool) error {
			// Never signal a PID whose watched process already exited: the
			// number may belong to another process by now.
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
		wait: wait,
	}, true, nil
}

// watchExit returns a kqueue that reports NOTE_EXIT for pid.
func watchExit(pid int) (int, error) {
	kq, err := unix.Kqueue()
	if err != nil {
		return -1, err
	}
	var change unix.Kevent_t
	unix.SetKevent(&change, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	change.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, nil, nil); err != nil {
		_ = unix.Close(kq)
		return -1, err
	}
	return kq, nil
}

// waitExit reports whether the process watched by kq exited within timeout.
func waitExit(kq int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(max(timeout, 0))
	events := make([]unix.Kevent_t, 1)
	for {
		ts := unix.NsecToTimespec(max(time.Until(deadline), 0).Nanoseconds())
		n, err := unix.Kevent(kq, nil, events, &ts)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}

// processArgs reads argv from kern.procargs2: a native int argc, the exec path,
// NUL padding, then argc NUL-terminated arguments.
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

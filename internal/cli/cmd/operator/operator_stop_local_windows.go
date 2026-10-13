//go:build windows

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const localOperatorProcessAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_VM_READ |
	windows.SYNCHRONIZE | windows.PROCESS_TERMINATE

// Hold a process handle before inspecting identity so PID reuse cannot
// redirect termination. Inspect argv tokens, never a substring of a shell
// command or a parent PID. Processes owned by another user, or whose memory
// this process cannot read (another bitness, elevated), are skipped.
func discoverLocalOperators() ([]localOperatorProcess, error) {
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("operator stop: read current user: %w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local workers: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	var processes []localOperatorProcess
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		pid := int(entry.ProcessID)
		if pid == os.Getpid() || !isZtdExecutable(windows.UTF16ToString(entry.ExeFile[:])) {
			continue
		}
		handle, openErr := windows.OpenProcess(localOperatorProcessAccess, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		p, ok := inspectLocalOperator(handle, pid, self.User.Sid)
		if !ok {
			_ = windows.CloseHandle(handle)
			continue
		}
		processes = append(processes, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		for _, p := range processes {
			p.close()
		}
		return nil, fmt.Errorf("operator stop: discover local workers: %w", err)
	}
	return processes, nil
}

func inspectLocalOperator(handle windows.Handle, pid int, owner *windows.SID) (localOperatorProcess, bool) {
	if !processOwnedBy(handle, owner) {
		return localOperatorProcess{}, false
	}
	// A rebuild while the worker runs renames its busy image aside (Git for
	// Windows cp leaves ztd.exe~), the counterpart of Linux " (deleted)".
	image, err := processImagePath(handle)
	if err != nil || !strings.HasPrefix(strings.ToLower(filepath.Base(image)), "ztd.exe") {
		return localOperatorProcess{}, false
	}
	commandLine, cwd, err := processCommandLineAndDir(handle)
	if err != nil {
		return localOperatorProcess{}, false
	}
	args, err := windows.DecomposeCommandLine(commandLine)
	if err != nil || !isLocalOperatorArgv(args) {
		return localOperatorProcess{}, false
	}
	dir := localOperatorDir(args, cwd)
	wait := func(timeout time.Duration) (bool, error) {
		ms := uint32(min(timeout.Milliseconds(), int64(windows.INFINITE-1)))
		if timeout > 0 && ms == 0 {
			ms = 1
		}
		event, err := windows.WaitForSingleObject(handle, ms)
		switch {
		case err != nil:
			return false, err
		case event == windows.WAIT_OBJECT_0:
			return true, nil
		default:
			return false, nil
		}
	}
	return localOperatorProcess{
		pid: pid, dir: dir, sessionID: localOperatorSessionID(dir),
		close: func() { _ = windows.CloseHandle(handle) },
		// Windows has no catchable TERM for a process outside this console. The
		// governed shutdown request is the graceful path, so both escalation
		// steps terminate the process through the held handle.
		signal: func(bool) error {
			if exited, err := wait(0); err == nil && exited {
				return nil
			}
			return windows.TerminateProcess(handle, 1)
		},
		wait: wait,
	}, true
}

func processOwnedBy(handle windows.Handle, owner *windows.SID) bool {
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	return err == nil && user.User.Sid.Equals(owner)
}

func processImagePath(handle windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// processCommandLineAndDir reads the command line and current directory from
// the process environment block of the process behind handle.
func processCommandLineAndDir(handle windows.Handle) (string, string, error) {
	var info windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(handle, windows.ProcessBasicInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return "", "", err
	}
	if info.PebBaseAddress == nil {
		return "", "", windows.ERROR_INVALID_DATA
	}
	var peb windows.PEB
	if err := readProcessMemory(handle, uintptr(unsafe.Pointer(info.PebBaseAddress)), unsafe.Pointer(&peb), unsafe.Sizeof(peb)); err != nil {
		return "", "", err
	}
	if peb.ProcessParameters == nil {
		return "", "", windows.ERROR_INVALID_DATA
	}
	// Read only through CommandLine; later fields vary across Windows releases.
	var params windows.RTL_USER_PROCESS_PARAMETERS
	if err := readProcessMemory(handle, uintptr(unsafe.Pointer(peb.ProcessParameters)), unsafe.Pointer(&params), unsafe.Offsetof(params.Environment)); err != nil {
		return "", "", err
	}
	commandLine, err := readProcessUnicodeString(handle, params.CommandLine)
	if err != nil {
		return "", "", err
	}
	cwd, err := readProcessUnicodeString(handle, params.CurrentDirectory.DosPath)
	if err != nil {
		return "", "", err
	}
	return commandLine, cwd, nil
}

func readProcessUnicodeString(handle windows.Handle, s windows.NTUnicodeString) (string, error) {
	if s.Length == 0 || s.Buffer == nil {
		return "", nil
	}
	buf := make([]uint16, s.Length/2)
	if err := readProcessMemory(handle, uintptr(unsafe.Pointer(s.Buffer)), unsafe.Pointer(&buf[0]), uintptr(s.Length)); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf), nil
}

func readProcessMemory(handle windows.Handle, address uintptr, dst unsafe.Pointer, size uintptr) error {
	var read uintptr
	if err := windows.ReadProcessMemory(handle, address, (*byte)(dst), size, &read); err != nil {
		return err
	}
	if read != size {
		return windows.ERROR_PARTIAL_COPY
	}
	return nil
}

// waitPIDTermination waits for the process with the given PID to exit, up to timeout.
func waitPIDTermination(pid int, timeout time.Duration) (bool, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	event, err := windows.WaitForSingleObject(handle, uint32(min(timeout.Milliseconds(), int64(windows.INFINITE-1))))
	if err != nil {
		return false, err
	}
	return event == windows.WAIT_OBJECT_0, nil
}

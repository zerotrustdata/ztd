//go:build windows

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pkg/processwatch"
	"golang.org/x/sys/windows"
)

const localOperatorProcessAccess = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.PROCESS_VM_READ |
	windows.PROCESS_TERMINATE

// DiscoverLocalOperators discovers running local Operator processes via process snapshot and PEB inspection.
func DiscoverLocalOperators() ([]LocalOperatorProcess, error) {
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("operator stop: read current user: %w", err)
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("operator stop: discover local Operators: %w", err)
	}
	defer func() { _ = windows.CloseHandle(snapshot) }()

	var processes []LocalOperatorProcess
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		pid := int(entry.ProcessID)
		if pid == os.Getpid() || !IsZtdExecutable(windows.UTF16ToString(entry.ExeFile[:])) {
			continue
		}
		handle, openErr := windows.OpenProcess(localOperatorProcessAccess, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		watch, watchErr := processwatch.WatchProcessExit(pid)
		if errors.Is(watchErr, constants.ErrProcessNotFound) {
			_ = windows.CloseHandle(handle)
			continue
		}
		if watchErr != nil {
			_ = windows.CloseHandle(handle)
			for _, p := range processes {
				p.Close()
			}
			return nil, fmt.Errorf("operator stop: watch PID %d: %w", pid, watchErr)
		}
		p, ok := inspectLocalOperator(handle, watch, pid, self.User.Sid)
		if !ok {
			_ = windows.CloseHandle(handle)
			_ = watch.Close()
			continue
		}
		processes = append(processes, p)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		for _, p := range processes {
			p.Close()
		}
		return nil, fmt.Errorf("operator stop: discover local Operators: %w", err)
	}
	return processes, nil
}

func inspectLocalOperator(handle windows.Handle, watch *processwatch.ProcessExitWatch, pid int, owner *windows.SID) (LocalOperatorProcess, bool) {
	if !processOwnedBy(handle, owner) {
		return LocalOperatorProcess{}, false
	}
	image, err := processImagePath(handle)
	if err != nil || !strings.HasPrefix(strings.ToLower(filepath.Base(image)), "ztd.exe") {
		return LocalOperatorProcess{}, false
	}
	commandLine, cwd, err := processCommandLineAndDir(handle)
	if err != nil {
		return LocalOperatorProcess{}, false
	}
	args, err := windows.DecomposeCommandLine(commandLine)
	if err != nil || !IsLocalOperatorArgv(args) {
		return LocalOperatorProcess{}, false
	}
	dir := LocalOperatorDir(args, cwd)
	wait := ExitWait(watch)
	return LocalOperatorProcess{
		PID: pid, Dir: dir, SessionID: LocalOperatorSessionID(dir),
		Close: func() {
			_ = windows.CloseHandle(handle)
			_ = watch.Close()
		},
		Signal: func(bool) error {
			if exited, err := wait(0); err == nil && exited {
				return nil
			}
			return windows.TerminateProcess(handle, 1)
		},
		Wait: wait,
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

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package testcmd

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

// smokeProcessGroup is the Job Object holding an owned process and every
// process it starts. The job kills its processes when the last handle closes,
// so an exiting runner never leaves an owned child behind; the handle stays
// open until then because a finished command (gw start) may leave a Gateway
// running in its job until cleanup stops it.
type smokeProcessGroup struct {
	job windows.Handle
	pid uint32
}

// The process starts suspended so it cannot start children before it is in
// its job, and in its own console process group so CTRL_BREAK reaches only it.
func smokeProcessGroupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
}

func newSmokeProcessGroup(cmd *exec.Cmd) (smokeProcessGroup, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return smokeProcessGroup{}, fmt.Errorf("create job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return smokeProcessGroup{}, fmt.Errorf("configure job object: %w", err)
	}
	pid := uint32(cmd.Process.Pid)
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME, false, pid)
	if err != nil {
		_ = windows.CloseHandle(job)
		return smokeProcessGroup{}, fmt.Errorf("open process %d: %w", pid, err)
	}
	defer func() { _ = windows.CloseHandle(process) }()
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = windows.CloseHandle(job)
		return smokeProcessGroup{}, fmt.Errorf("assign process %d to job: %w", pid, err)
	}
	if status, _, _ := procNtResumeProcess.Call(uintptr(process)); status != 0 {
		_ = windows.CloseHandle(job)
		return smokeProcessGroup{}, fmt.Errorf("resume process %d: %w", pid, windows.NTStatus(status))
	}
	return smokeProcessGroup{job: job, pid: pid}, nil
}

// terminate sends CTRL_BREAK, which Go programs receive as an interrupt, to
// the process group. A runner without a console cannot deliver it; kill
// follows after the grace period either way.
func (g smokeProcessGroup) terminate() error {
	if g.exists() {
		_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, g.pid)
	}
	return nil
}

// kill terminates every process in the job.
func (g smokeProcessGroup) kill() error {
	if !g.exists() {
		return nil
	}
	return windows.TerminateJobObject(g.job, 1)
}

// jobBasicAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type jobBasicAccounting struct {
	TotalUserTime, TotalKernelTime                     int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses                uint32
	ActiveProcesses, TotalTerminatedProcesses          uint32
}

func (g smokeProcessGroup) exists() bool {
	var info jobBasicAccounting
	if err := windows.QueryInformationJobObject(g.job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return true
	}
	return info.ActiveProcesses > 0
}

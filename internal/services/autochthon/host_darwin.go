// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package autochthon

import (
	"golang.org/x/sys/unix"

	"github.com/zerotrustdata/ztd/internal/services/system"
)

func readHostFacts() (h hostFacts) {
	if load, ok := darwinLoad1(); ok {
		h.load1, h.loadOK = load, true
	}
	if used, ok := darwinMemUsed(); ok {
		h.memUsed, h.memOK = used, true
	}
	if n, ok := darwinProcesses(); ok {
		h.tasks, h.tasksOK = n, true
	}
	return h
}

func darwinLoad1() (float64, bool) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, false
	}
	return decodeDarwinLoadavg(raw)
}

func darwinProcesses() (int, bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil || len(procs) == 0 {
		return 0, false
	}
	return len(procs), true
}

// darwinMemUsed is the fraction of physical RAM that is not reclaimable
// without swapping, the same counters the evaluation host collector uses.
func darwinMemUsed() (float32, bool) {
	total, available, ok := system.ReclaimableRAM()
	if !ok || total == 0 {
		return 0, false
	}
	return clamp01(float32(float64(total-available) / float64(total))), true
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build linux

package autochthon

import (
	"os"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func readHostFacts() (h hostFacts) {
	if b, err := os.ReadFile(constants.PathProcLoadAvg); err == nil {
		if load, ok := parseLoadavg(string(b)); ok {
			h.load1, h.loadOK = load, true
		}
	}
	if b, err := os.ReadFile(constants.PathProcMemInfo); err == nil {
		if used, ok := parseMemUsed(string(b)); ok {
			h.memUsed, h.memOK = used, true
		}
	}
	if n, ok := linuxProcesses(); ok {
		h.tasks, h.tasksOK = n, true
	}
	return h
}

// linuxProcesses counts numeric directory names under /proc. Each is one
// process. Threads of a process live under /proc/<pid>/task and are not
// separate entries here.
func linuxProcesses() (int, bool) {
	entries, err := os.ReadDir(constants.PathProc)
	if err != nil {
		return 0, false
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	n := countProcNames(names)
	return n, n > 0
}

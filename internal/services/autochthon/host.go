// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"encoding/binary"
	"math"
	"strconv"
	"strings"
)

// hostFacts is one reading of the three host quantities sample writes into
// senses 0, 1, and 6. load1 is the one-minute load average in runnable tasks.
// memUsed is the fraction of physical RAM that is not available to allocate
// without swapping, in [0, 1]. tasks is the number of processes the kernel
// lists. A false ok flag means that reading failed and the sense stays zero.
type hostFacts struct {
	load1   float64
	memUsed float32
	tasks   int
	loadOK  bool
	memOK   bool
	tasksOK bool
}

// senses scales the readings onto the network's [0, 1] inputs. A reading
// whose ok flag is false contributes zero.
func (h hostFacts) senses(cpus int) (load, mem, procs float32) {
	if cpus < 1 {
		cpus = 1
	}
	if h.loadOK {
		load = loadSense(h.load1, cpus)
	}
	if h.memOK {
		mem = clamp01(h.memUsed)
	}
	if h.tasksOK {
		procs = procSense(h.tasks)
	}
	return load, mem, procs
}

// missing names the host quantities this reading could not take.
func (h hostFacts) missing() []string {
	var missing []string
	if !h.loadOK {
		missing = append(missing, "load")
	}
	if !h.memOK {
		missing = append(missing, "memory")
	}
	if !h.tasksOK {
		missing = append(missing, "processes")
	}
	return missing
}

// procSense maps a process count to n/(n+512). Zero and negative counts
// sense as zero, which is also the value of a failed reading.
func procSense(n int) float32 {
	if n <= 0 {
		return 0
	}
	return float32(n) / float32(n+procHalf)
}

// parseLoadavg reads the one-minute load average from a /proc/loadavg line.
// The rest of the line is the five- and fifteen-minute averages, the
// runnable/thread counts, and the last pid. Those are not the process sense:
// process count is the process table, counted separately.
func parseLoadavg(text string) (float64, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || load < 0 || math.IsNaN(load) || math.IsInf(load, 0) {
		return 0, false
	}
	return load, true
}

// parseMemUsed reads the fraction of physical RAM that is not available to
// allocate without swapping. MemAvailable is that quantity when the kernel
// exports it, including when it is zero. When the key is absent the reading
// is MemFree + Buffers + Cached. A file with no total, or with neither
// available nor the fallback keys, is not a reading.
func parseMemUsed(text string) (float32, bool) {
	var total, avail, free, buffers, cached float64
	var hasTotal, hasAvail, hasFree, hasBuffers, hasCached bool
	for line := range strings.Lines(text) {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total, hasTotal = n, true
		case "MemAvailable":
			avail, hasAvail = n, true
		case "MemFree":
			free, hasFree = n, true
		case "Buffers":
			buffers, hasBuffers = n, true
		case "Cached":
			cached, hasCached = n, true
		}
	}
	if !hasTotal || total <= 0 {
		return 0, false
	}
	if !hasAvail {
		if !hasFree && !hasBuffers && !hasCached {
			return 0, false
		}
		avail = free + buffers + cached
	}
	if avail < 0 {
		avail = 0
	}
	if avail > total {
		avail = total
	}
	return clamp01(float32(1 - avail/total)), true
}

// decodeDarwinLoadavg reads the one-minute load from the Darwin vm.loadavg
// sysctl. The kernel returns struct loadavg: three uint32 fixed-point
// averages, four bytes of padding so the scale is 8-aligned, then the
// uint64 scale. The one-minute average is the first word divided by the scale.
func decodeDarwinLoadavg(raw []byte) (float64, bool) {
	if len(raw) < 24 {
		return 0, false
	}
	scale := binary.LittleEndian.Uint64(raw[16:24])
	if scale == 0 {
		return 0, false
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(scale), true
}

// countProcNames counts process-table names. A Linux /proc entry is a
// process when its name is a pid: digits, not zero.
func countProcNames(names []string) int {
	n := 0
	for _, name := range names {
		if isPIDName(name) {
			n++
		}
	}
	return n
}

func isPIDName(name string) bool {
	if name == "" || name[0] < '1' || name[0] > '9' {
		return false
	}
	for i := 1; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			return false
		}
	}
	return true
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"crypto/sha256"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

const (
	senseN = 8
	inputN = senseN + outN + loadAgeN
)

// loadAgeN is how many samples after a load change that change stays
// visible to the network. The kernel refreshes /proc/loadavg every five
// seconds, so at one sample a second a change at age 4 is the last one
// before the next refresh.
const loadAgeN = 5

// procHalf is the process count that senses as 0.5. The scale does not
// saturate, so a busy host still senses a change in its process count.
const procHalf = 512

// targetIdx are the senses the network predicts: load, memory, TERM, processes.
var targetIdx = [outN]int{0, 1, 2, 6}

// sense is one look at the runtime this process can see.
//
//	[0] load l/(l+CPUs)       [1] memory in use
//	[2] TERM                  [3] hour
//	[4] weekday               [5] minute
//	[6] processes n/(n+512)   [7] bias 1
//
// An empty TERM is 0. A set TERM is a stable hash in (0, 1]. TERM is read
// from the process environment, so it is fixed for one process and lets the
// culture condition on the terminal it was started from. d is the change in
// target since the previous sample, zero for the first sample of a run.
// loadAge is the number of samples since the load sense last changed and
// loadStep is the change in its target then. Both are zero until the first
// change of a run.
type sense struct {
	at       time.Time
	term     string
	v        [senseN]float32
	d        [outN]float32
	loadAge  int
	loadStep float32
}

// since records the change in target from prev, the sample before this one,
// and carries the last load change forward.
func (s *sense) since(prev *sense) {
	now, was := s.target(), prev.target()
	for i := range s.d {
		s.d[i] = now[i] - was[i]
	}
	if s.d[0] != 0 {
		s.loadAge, s.loadStep = 0, s.d[0]
	} else {
		s.loadAge, s.loadStep = prev.loadAge+1, prev.loadStep
	}
}

// input is what the network sees: the senses, the change in target since
// the previous sample, and the last load change in the slot for its age,
// zero once it is loadAgeN samples old. Each change is on the scale of the
// change being predicted, so a weight near -1 or 1 on it is a whole
// forecast: reversion or momentum, or, through the age slots, the next step
// of a load average that refreshes on a fixed period.
func (s *sense) input() (x [inputN]float32) {
	copy(x[:], s.v[:])
	copy(x[senseN:], s.d[:])
	if s.loadAge < loadAgeN {
		x[senseN+outN+s.loadAge] = s.loadStep
	}
	return x
}

// sample reads the host. Missing /proc files leave those senses at zero.
func sample() sense {
	now := time.Now().UTC()
	s := sense{at: now, term: os.Getenv(string(constants.EnvVar.Term))}
	s.v[0], s.v[6] = readLoadavg()
	s.v[1] = readMemInUse()
	s.v[2] = termUnit(s.term)
	s.v[3] = float32(now.Hour()) / 23
	s.v[4] = float32(now.Weekday()) / 6
	s.v[5] = float32(now.Minute()) / 59
	s.v[7] = 1
	return s
}

// target maps the predicted senses from [0, 1] onto tanh's [-1, 1].
func (s *sense) target() (t [outN]float32) {
	for i, j := range targetIdx {
		t[i] = s.v[j]*2 - 1
	}
	return t
}

func termUnit(term string) float32 {
	if term == "" {
		return 0
	}
	sum := sha256.Sum256([]byte(term))
	return float32(int(sum[0])+1) / 256
}

func readLoadavg() (load, procs float32) {
	b, err := os.ReadFile(constants.PathProcLoadAvg)
	if err != nil {
		return 0, 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 4 {
		return 0, 0
	}
	if l, err := strconv.ParseFloat(fields[0], 64); err == nil {
		load = loadSense(l, runtime.NumCPU())
	}
	if _, total, ok := strings.Cut(fields[3], "/"); ok {
		if n, err := strconv.Atoi(total); err == nil {
			procs = float32(n) / float32(n+procHalf)
		}
	}
	return load, procs
}

// loadSense maps the one-minute load average to l/(l+cpus): half when the
// run queue matches the CPUs, and never flat, so a host loaded past its CPUs
// still shows the load rising and decaying.
func loadSense(l float64, cpus int) float32 {
	return float32(l / (l + float64(cpus)))
}

func readMemInUse() float32 {
	b, err := os.ReadFile(constants.PathProcMemInfo)
	if err != nil {
		return 0
	}
	var total, avail float64
	for line := range strings.Lines(string(b)) {
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
			total = n
		case "MemAvailable":
			avail = n
		}
	}
	if total <= 0 || avail <= 0 {
		return 0
	}
	return clamp01(float32(1 - avail/total))
}

func clamp01(v float32) float32 {
	return min(max(v, 0), 1)
}

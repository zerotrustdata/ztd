// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthon

import (
	"encoding/hex"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostFactsSensesScaleTheSameOnEveryPlatform(t *testing.T) {
	t.Parallel()
	h := hostFacts{load1: 8, memUsed: 0.25, tasks: procHalf, loadOK: true, memOK: true, tasksOK: true}
	load, mem, procs := h.senses(8)
	assert.InDelta(t, 0.5, load, 1e-6)
	assert.InDelta(t, 0.25, mem, 1e-6)
	assert.InDelta(t, 0.5, procs, 1e-6)

	load, mem, procs = hostFacts{}.senses(8)
	assert.Zero(t, load)
	assert.Zero(t, mem)
	assert.Zero(t, procs)
	assert.Equal(t, []string{"load", "memory", "processes"}, hostFacts{}.missing())
	assert.Empty(t, h.missing())
}

func TestProcSense(t *testing.T) {
	t.Parallel()
	assert.Zero(t, procSense(0))
	assert.Zero(t, procSense(-3))
	assert.InDelta(t, 0.5, procSense(procHalf), 1e-6)
	assert.Greater(t, procSense(procHalf+1), procSense(procHalf))
}

func TestParseLoadavg(t *testing.T) {
	t.Parallel()
	load, ok := parseLoadavg("0.15 0.10 0.05 1/234 99\n")
	require.True(t, ok)
	assert.InDelta(t, 0.15, load, 1e-9)

	_, ok = parseLoadavg("")
	assert.False(t, ok)
	_, ok = parseLoadavg("not-a-load 0 0 1/2 3")
	assert.False(t, ok)
	_, ok = parseLoadavg("-1 0 0 1/2 3")
	assert.False(t, ok)
}

func TestParseMemUsed(t *testing.T) {
	t.Parallel()
	used, ok := parseMemUsed("MemTotal: 1000 kB\nMemAvailable: 250 kB\n")
	require.True(t, ok)
	assert.InDelta(t, 0.75, used, 1e-6)

	used, ok = parseMemUsed("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 0 kB\n")
	require.True(t, ok)
	assert.InDelta(t, 1, used, 1e-6)

	used, ok = parseMemUsed("MemTotal: 1000 kB\nMemFree: 100 kB\nBuffers: 50 kB\nCached: 50 kB\n")
	require.True(t, ok)
	assert.InDelta(t, 0.8, used, 1e-6)

	_, ok = parseMemUsed("MemTotal: 1000 kB\n")
	assert.False(t, ok)
	_, ok = parseMemUsed("MemAvailable: 10 kB\n")
	assert.False(t, ok)
	_, ok = parseMemUsed("")
	assert.False(t, ok)
}

func TestDecodeDarwinLoadavg(t *testing.T) {
	t.Parallel()
	raw, err := hex.DecodeString("b52700005f1f000055210000000000000008000000000000")
	require.NoError(t, err)
	load, ok := decodeDarwinLoadavg(raw)
	require.True(t, ok)
	assert.InDelta(t, 10165.0/2048.0, load, 1e-9)

	_, ok = decodeDarwinLoadavg(raw[:16])
	assert.False(t, ok)
	for i := 16; i < 24; i++ {
		raw[i] = 0
	}
	_, ok = decodeDarwinLoadavg(raw)
	assert.False(t, ok)
}

func TestCountProcNames(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 2, countProcNames([]string{"1", "self", "cpuinfo", "42", "0", "", "12a"}))
}

func TestReadHostFactsFromThisHost(t *testing.T) {
	t.Parallel()
	h := readHostFacts()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		assert.Equal(t, []string{"load", "memory", "processes"}, h.missing())
		return
	}
	require.Empty(t, h.missing())
	assert.GreaterOrEqual(t, h.load1, 0.0)
	assert.Greater(t, h.memUsed, float32(0))
	assert.LessOrEqual(t, h.memUsed, float32(1))
	assert.Greater(t, h.tasks, 1)

	load, mem, procs := h.senses(runtime.NumCPU())
	assert.GreaterOrEqual(t, load, float32(0))
	assert.Less(t, load, float32(1))
	assert.Greater(t, mem, float32(0))
	assert.Greater(t, procs, float32(0))
}

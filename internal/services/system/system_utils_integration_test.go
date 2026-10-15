//go:build integration && !windows

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package system

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetNetworkLatency(t *testing.T) {

	latency := GetNetworkLatency()

	assert.GreaterOrEqual(t, latency, 0.0)
	assert.LessOrEqual(t, latency, 1000.0)
	rounded := math.Round(latency*100) / 100
	assert.InDelta(t, latency, rounded, 0.001, "GetNetworkLatency() should be rounded to 2 decimal places")
}

func TestSystemUtilsIntegration(t *testing.T) {

	t.Run("all functions execute without panic", func(t *testing.T) {
		require.NotNil(t, GetConnectivityStatus())
		assert.NotEmpty(t, GetUptime())
		assert.GreaterOrEqual(t, GetUptimeSeconds(), int64(0))

		cpuPercent := GetCPUPercent()
		assert.GreaterOrEqual(t, cpuPercent, 0.0)
		assert.LessOrEqual(t, cpuPercent, 100.0)

		memPercent := GetMemoryPercent()
		assert.GreaterOrEqual(t, memPercent, 0.0)
		assert.LessOrEqual(t, memPercent, 100.0)

		assert.GreaterOrEqual(t, GetNetworkLatency(), 0.0)
	})
}

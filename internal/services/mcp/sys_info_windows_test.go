// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package mcp

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/services/system"
)

func TestSysInfoTool_Windows_Execute(t *testing.T) {
	tool := &SysInfoTool{}
	ctx := context.Background()

	result, err := tool.Execute(ctx, json.RawMessage("{}"))
	require.NoError(t, err)
	require.Len(t, result.Content, 1)
	require.Equal(t, "text", result.Content[0].Type)

	var sysInfo SysInfoResult
	err = json.Unmarshal([]byte(result.Content[0].Text), &sysInfo)
	require.NoError(t, err)

	expectedHostname, _ := os.Hostname()
	require.Equal(t, expectedHostname, sysInfo.Hostname)

	require.Equal(t, "windows", sysInfo.OS.OS)
	require.Equal(t, runtime.GOARCH, sysInfo.OS.Arch)

	// Kernel version should start with "Windows "
	require.NotEmpty(t, sysInfo.OS.Kernel)
	require.Contains(t, sysInfo.OS.Kernel, "Windows ")

	// OSVersion should be populated (e.g. "Windows 11 ...")
	require.NotEmpty(t, sysInfo.OS.OSVersion)

	// Uptime should match format: \d+d \d+h \d+m
	uptimeRegex := regexp.MustCompile(`^\d+d \d+h \d+m$`)
	require.True(t, uptimeRegex.MatchString(sysInfo.OS.Uptime), "uptime %q should match %%dd %%dh %%dm format", sysInfo.OS.Uptime)

	// LoadAverage is unsupported on Windows, must be "unknown"
	require.Equal(t, "unknown", sysInfo.OS.LoadAverage)
}

func TestSysInfo_Windows_RelabelVersion(t *testing.T) {
	tests := []struct {
		name        string
		productName string
		buildNum    int
		expected    string
	}{
		{
			name:        "Windows 10 with build >= 22000 relabeled to Windows 11",
			productName: "Windows 10 Pro",
			buildNum:    22000,
			expected:    "Windows 11 Pro",
		},
		{
			name:        "Windows 10 Pro for Workstations build 26300 relabeled",
			productName: "Windows 10 Pro for Workstations",
			buildNum:    26300,
			expected:    "Windows 11 Pro for Workstations",
		},
		{
			name:        "Windows 10 with build < 22000 unchanged",
			productName: "Windows 10 Home",
			buildNum:    19045,
			expected:    "Windows 10 Home",
		},
		{
			name:        "Windows 11 already named correctly unchanged",
			productName: "Windows 11 Enterprise",
			buildNum:    22631,
			expected:    "Windows 11 Enterprise",
		},
		{
			name:        "Windows Server unchanged",
			productName: "Windows Server 2022 Standard",
			buildNum:    20348,
			expected:    "Windows Server 2022 Standard",
		},
		{
			name:        "Empty product name unchanged",
			productName: "",
			buildNum:    22000,
			expected:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := relabelWindowsVersion(tt.productName, tt.buildNum)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestSysInfo_Windows_GetUptime(t *testing.T) {
	ctx := context.Background()
	uptime := getUptime(ctx)
	require.NotEmpty(t, uptime)

	uptimeSeconds := system.GetUptimeSeconds()
	expected := formatUptime(uptimeSeconds)
	require.Equal(t, expected, uptime)
}

func TestSysInfo_Windows_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Equal(t, "unknown", getKernelVersion(ctx))
	require.Equal(t, "unknown", getOSVersion(ctx))
	require.Equal(t, "unknown", getUptime(ctx))
	require.Equal(t, "unknown", getLoadAverage(ctx))
}

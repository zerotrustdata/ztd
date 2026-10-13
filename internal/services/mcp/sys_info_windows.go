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
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/system"
)

func collectSysInfo(ctx context.Context) OSInfo {
	return OSInfo{
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Kernel:      getKernelVersion(ctx),
		OSVersion:   getOSVersion(ctx),
		Uptime:      getUptime(ctx),
		LoadAverage: getLoadAverage(ctx),
	}
}

// getKernelVersion returns the Windows kernel version string.
func getKernelVersion(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	kernel := system.GetWindowsKernelVersion()
	if kernel == "" || kernel == "Windows Unknown" {
		return "unknown"
	}
	return kernel
}

// getOSVersion returns the human-readable Windows OS version.
// If the registry ProductName says 'Windows 10' and CurrentBuild >= 22000,
// it is relabeled to 'Windows 11'.
func getOSVersion(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	productName, buildNum, err := readWindowsProductAndBuild()
	if err == nil && productName != "" {
		return relabelWindowsVersion(productName, buildNum)
	}

	winVer := system.GetWindowsVersion()
	if winVer != "" && winVer != "Unknown" {
		return "Windows " + winVer
	}
	return "unknown"
}

// relabelWindowsVersion relabels 'Windows 10' to 'Windows 11' if build >= 22000.
func relabelWindowsVersion(productName string, buildNum int) string {
	if strings.Contains(productName, "Windows 10") && buildNum >= 22000 {
		return strings.Replace(productName, "Windows 10", "Windows 11", 1)
	}
	return productName
}

// readWindowsProductAndBuild reads ProductName and build number from the Windows registry.
func readWindowsProductAndBuild() (string, int, error) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, constants.PathWindowsRegistryCurrentVersion, registry.READ)
	if err != nil {
		return "", 0, err
	}
	defer key.Close()

	productName, _, _ := key.GetStringValue(constants.PathWindowsRegistryProductName)

	buildStr, _, err := key.GetStringValue(constants.PathWindowsRegistryCurrentBuild)
	if err != nil || buildStr == "" {
		buildStr, _, _ = key.GetStringValue(constants.PathWindowsRegistryCurrentBuildNumber)
	}

	var buildNum int
	if buildStr != "" {
		buildNum, _ = strconv.Atoi(buildStr)
	}

	if buildNum == 0 {
		ver := windows.RtlGetVersion()
		if ver != nil {
			buildNum = int(ver.BuildNumber)
		}
	}

	return productName, buildNum, nil
}

// getUptime returns system uptime in human-readable format.
func getUptime(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	uptimeSeconds := system.GetUptimeSeconds()
	return formatUptime(uptimeSeconds)
}

// getLoadAverage returns "unknown" since load averages are not supported on Windows.
func getLoadAverage(ctx context.Context) string {
	_ = ctx
	return "unknown"
}

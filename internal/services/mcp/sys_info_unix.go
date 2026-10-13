// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !windows

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
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

// getKernelVersion returns the kernel version from /proc/version or uname.
func getKernelVersion(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	data, err := os.ReadFile(constants.PathProcVersion)
	if err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 3 {
			return strings.Join(fields[2:], " ")
		}
	}

	cmd := exec.CommandContext(ctx, "uname", "-r")
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out))
	}

	return "unknown"
}

// getOSVersion returns the OS version from /etc/os-release.
func getOSVersion(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	data, err := os.ReadFile(constants.PathEtcOSRelease)
	if err != nil {
		return "unknown"
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			value := strings.TrimPrefix(line, "PRETTY_NAME=")
			value = strings.Trim(value, "\"")
			return value
		}
	}
	return "unknown"
}

// getUptime returns system uptime in human-readable format.
func getUptime(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	data, err := os.ReadFile(constants.PathProcUptime)
	if err != nil {
		return "unknown"
	}

	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return "unknown"
	}

	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "unknown"
	}

	return formatUptime(int64(seconds))
}

// getLoadAverage returns the 1, 5, and 15 minute load averages.
func getLoadAverage(ctx context.Context) string {
	if ctx.Err() != nil {
		return "unknown"
	}

	data, err := os.ReadFile(constants.PathProcLoadAvg)
	if err != nil {
		return "unknown"
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return "unknown"
	}

	return fmt.Sprintf("%s %s %s", fields[0], fields[1], fields[2])
}

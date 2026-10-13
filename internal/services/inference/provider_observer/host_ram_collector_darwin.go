// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package provider_observer

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"golang.org/x/sys/unix"
)

// darwinAvailablePageSysctls count the pages macOS reclaims without swapping.
var darwinAvailablePageSysctls = []string{
	"vm.page_free_count",
	"vm.page_speculative_count",
	"vm.page_pageable_external_count",
	"vm.page_purgeable_count",
}

// DarwinHostRAMCollector reads host RAM usage from the kernel VM sysctls.
type DarwinHostRAMCollector struct{}

func newPlatformHostRAMCollector() HardwareCollector {
	return &DarwinHostRAMCollector{}
}

func (c *DarwinHostRAMCollector) Collect(_ context.Context, observedAt time.Time) (*evalv1.ProviderBoundaryHardwareSample, error) {
	if c == nil {
		return unavailableRAMSample(observedAt), nil
	}
	totalBytes, usedBytes, ok := readDarwinMemory()
	if !ok {
		return unavailableRAMSample(observedAt), nil
	}
	return &evalv1.ProviderBoundaryHardwareSample{
		ObservedAtUnixNanos: uint64(observedAt.UTC().UnixNano()),
		HostRamAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
		HostRamUsedBytes:    usedBytes,
		HostRamTotalBytes:   totalBytes,
	}, nil
}

// readDarwinMemory counts free, speculative, file-backed, and purgeable pages
// as available, the pages macOS reclaims without swapping; the rest of
// hw.memsize (app, wired, and compressed memory) is used. The vm.page_* counts
// are in kernel pages (16 KiB on Apple silicon), the size the Go runtime reads
// at startup; hw.pagesize is a 64-bit sysctl, so SysctlUint32 cannot read it.
func readDarwinMemory() (totalBytes, usedBytes uint64, ok bool) {
	totalBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil || totalBytes == 0 {
		return 0, 0, false
	}
	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return 0, 0, false
	}
	var availablePages uint64
	for _, name := range darwinAvailablePageSysctls {
		pages, err := sysctlCount(name)
		if err != nil {
			return 0, 0, false
		}
		availablePages += pages
	}
	availableBytes := min(availablePages*uint64(pageSize), totalBytes)
	return totalBytes, totalBytes - availableBytes, true
}

// sysctlCount reads an unsigned counter sysctl. The kernel exports VM counters
// as 32- or 64-bit integers depending on the counter and release, so the width
// comes from the returned length rather than a fixed-size read.
func sysctlCount(name string) (uint64, error) {
	raw, err := unix.SysctlRaw(name)
	if err != nil {
		return 0, fmt.Errorf("read sysctl %s: %w", name, err)
	}
	switch len(raw) {
	case 4:
		return uint64(binary.LittleEndian.Uint32(raw)), nil
	case 8:
		return binary.LittleEndian.Uint64(raw), nil
	default:
		return 0, fmt.Errorf("read sysctl %s: %d-byte value: %w", name, len(raw), unix.EIO)
	}
}

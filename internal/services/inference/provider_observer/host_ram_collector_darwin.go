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
	"time"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"golang.org/x/sys/unix"
)

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
// hw.memsize (app, wired, and compressed memory) is used.
func readDarwinMemory() (totalBytes, usedBytes uint64, ok bool) {
	totalBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil || totalBytes == 0 {
		return 0, 0, false
	}
	pageSize, err := unix.SysctlUint32("hw.pagesize")
	if err != nil || pageSize == 0 {
		return 0, 0, false
	}
	var availablePages uint64
	for _, name := range []string{"vm.page_free_count", "vm.page_speculative_count", "vm.page_pageable_external_count", "vm.page_purgeable_count"} {
		pages, err := unix.SysctlUint32(name)
		if err != nil {
			return 0, 0, false
		}
		availablePages += uint64(pages)
	}
	availableBytes := min(availablePages*uint64(pageSize), totalBytes)
	return totalBytes, totalBytes - availableBytes, true
}

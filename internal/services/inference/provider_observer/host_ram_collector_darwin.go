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

	"github.com/zerotrustdata/ztd/internal/services/system"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
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

// readDarwinMemory is the host RAM the evaluation collector reports. Available
// bytes are the pages macOS reclaims without swapping; the rest of hw.memsize
// is in use.
func readDarwinMemory() (totalBytes, usedBytes uint64, ok bool) {
	totalBytes, available, ok := system.ReclaimableRAM()
	if !ok {
		return 0, 0, false
	}
	return totalBytes, totalBytes - available, true
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package system

import (
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// reclaimablePages are the pages macOS will hand out without swapping:
// free, speculative, file-backed, and purgeable.
var reclaimablePages = []string{
	"vm.page_free_count",
	"vm.page_speculative_count",
	"vm.page_pageable_external_count",
	"vm.page_purgeable_count",
}

// ReclaimableRAM returns physical memory and the bytes that can be allocated
// without swapping. The page counters are in kernel pages. os.Getpagesize is
// that size, 4 KiB on Intel and 16 KiB on Apple silicon. hw.pagesize is a
// 64-bit sysctl, so a fixed 32-bit read cannot take it. ok is false when any
// counter is missing.
func ReclaimableRAM() (total, available uint64, ok bool) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil || total == 0 {
		return 0, 0, false
	}
	pageSize := os.Getpagesize()
	if pageSize <= 0 {
		return 0, 0, false
	}
	var pages uint64
	for _, name := range reclaimablePages {
		n, err := sysctlUint(name)
		if err != nil {
			return 0, 0, false
		}
		pages += n
	}
	available = pages * uint64(pageSize)
	if available > total {
		available = total
	}
	return total, available, true
}

// sysctlUint reads an unsigned counter. The kernel exports these as 32- or
// 64-bit integers depending on the counter and release, so the width comes
// from the returned length.
func sysctlUint(name string) (uint64, error) {
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

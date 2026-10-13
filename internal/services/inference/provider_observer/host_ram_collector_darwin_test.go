// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package provider_observer

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Names the sysctl that fails, which the collector's UNAVAILABLE sample hides.
func TestReadDarwinMemory_ReadsEverySysctl(t *testing.T) {
	for _, name := range darwinAvailablePageSysctls {
		_, err := sysctlCount(name)
		require.NoError(t, err, name)
	}
	assert.Positive(t, os.Getpagesize())

	totalBytes, usedBytes, ok := readDarwinMemory()
	require.True(t, ok)
	assert.Positive(t, totalBytes)
	assert.LessOrEqual(t, usedBytes, totalBytes)
}

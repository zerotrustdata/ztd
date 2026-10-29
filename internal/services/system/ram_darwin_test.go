// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package system

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReclaimableRAMReadsEveryCounter(t *testing.T) {
	t.Parallel()
	for _, name := range reclaimablePages {
		_, err := sysctlUint(name)
		require.NoError(t, err, name)
	}
	total, available, ok := ReclaimableRAM()
	require.True(t, ok)
	assert.Positive(t, total)
	assert.LessOrEqual(t, available, total)
	assert.Positive(t, total-available)
}

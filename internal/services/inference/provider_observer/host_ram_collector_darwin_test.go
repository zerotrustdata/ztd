// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build darwin

package provider_observer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadDarwinMemory(t *testing.T) {
	t.Parallel()
	totalBytes, usedBytes, ok := readDarwinMemory()
	require.True(t, ok)
	assert.Positive(t, totalBytes)
	assert.LessOrEqual(t, usedBytes, totalBytes)
	assert.Positive(t, usedBytes)
}

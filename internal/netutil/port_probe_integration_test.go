// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package netutil

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCheckTCPPortAvailable_RejectsInvalidPorts(t *testing.T) {
	for _, tc := range []struct {
		name string
		port int
	}{
		{"negative", -1},
		{"ephemeral", 0},
		{"above maximum", 65536},
		{"far above maximum", 70000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, CheckTCPPortAvailable(tc.port), constants.ErrPortUnavailable)
		})
	}
}

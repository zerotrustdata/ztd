// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package profiling

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateListenAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:6060", true},
		{"localhost:6060", true},
		{"[::1]:6060", true},
		{"127.0.0.2:6060", true},
		{"0.0.0.0:6060", false},
		{"[::]:6060", false},
		{":6060", false},
		{"192.168.1.10:6060", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			err := ValidateListenAddress(tc.addr)
			if tc.ok {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, constants.ErrProfilingListenAddress)
		})
	}
}

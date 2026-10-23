// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package commonv1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPlatformComponentKindRetiredValueStaysReserved guards the retired
// PLATFORM_COMPONENT_KIND_ENSEMBLE value (number 2): its number and name must
// stay reserved in the descriptor so they are never reused, and no live enum
// value may carry either of them.
func TestPlatformComponentKindRetiredValueStaysReserved(t *testing.T) { //nolint:paralleltest // mutates package-level state

	enum := File_ztd_common_v1_common_proto.Enums().ByName("PlatformComponentKind")
	if !assert.NotNil(t, enum) {
		return
	}

	assert.Nil(t, enum.Values().ByNumber(2), "retired number 2 must not be a live enum value")
	assert.Nil(t, enum.Values().ByName("PLATFORM_COMPONENT_KIND_ENSEMBLE"), "retired name must not be a live enum value")

	reservedNumber := false
	for i := 0; i < enum.ReservedRanges().Len(); i++ {
		r := enum.ReservedRanges().Get(i)
		if r[0] <= 2 && 2 <= r[1] {
			reservedNumber = true
		}
	}
	assert.True(t, reservedNumber, "retired number 2 must be reserved")

	reservedName := false
	for i := 0; i < enum.ReservedNames().Len(); i++ {
		if enum.ReservedNames().Get(i) == "PLATFORM_COMPONENT_KIND_ENSEMBLE" {
			reservedName = true
		}
	}
	assert.True(t, reservedName, "retired name must be reserved")
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestNewLoadedModel_RequiresAReportedWindow(t *testing.T) {
	for _, window := range []int64{0, -1, maxInferenceContextLimit + 1} {
		_, err := newLoadedModel("test_backend", "m", window, new(int64(0)))
		require.ErrorIs(t, err, constants.ErrInferenceContextWindowUnavailable, window)
		require.ErrorIs(t, err, constants.ErrInferenceProviderResponseInvalid, window)
	}
	loaded, err := newLoadedModel("test_backend", "m", 32_768, new(int64(5)))
	require.NoError(t, err)
	assert.Equal(t, int32(32_768), loaded.ContextWindow)
	assert.Equal(t, int64(5), *loaded.LoadDurationNS)
}

func TestOutputTokenLimit_IsTheStatedLimitOrTheLoadedWindow(t *testing.T) {
	loaded := loadedModel{ContextWindow: 8192}
	assert.Equal(t, int32(8192), outputTokenLimit(0, loaded), "an unstated limit runs to the window")
	assert.Equal(t, int32(512), outputTokenLimit(512, loaded))
	assert.Equal(t, int32(8192), outputTokenLimit(8192, loaded))
	assert.Equal(t, int32(8192), outputTokenLimit(1<<20, loaded), "no limit exceeds the window")
}

func TestSumLoadDurations_UnknownPartLeavesTotalUnknown(t *testing.T) {
	assert.Equal(t, int64(7), *sumLoadDurations(loadedModel{LoadDurationNS: new(int64(5))}, new(int64(2))))
	assert.Nil(t, sumLoadDurations(loadedModel{LoadDurationNS: new(int64(5))}, nil))
	assert.Nil(t, sumLoadDurations(loadedModel{}, new(int64(2))))
}

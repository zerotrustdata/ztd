// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestMarshalResult_PreservesEncodingFailure(t *testing.T) {
	result, err := marshalResult(constants.MCPToolNetEndpointPing, NetEndpointPingResult{LatencyMs: math.Inf(1)})
	require.ErrorIs(t, err, constants.ErrMCPMarshalResult)
	var encodingErr *json.UnsupportedValueError
	require.ErrorAs(t, err, &encodingErr)
	assert.Empty(t, result.Content)
	assert.Contains(t, err.Error(), constants.MCPToolNetEndpointPing)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperatorCapabilitiesValidateNormalizeAndRejectUnknownValues(t *testing.T) {
	var capabilities OperatorCapabilities
	require.NoError(t, capabilities.Set("observer,data,inference"))
	require.NoError(t, capabilities.Set("provenance,data"))
	require.Equal(t, "data,inference,provenance,observer", capabilities.String())
	before := capabilities.String()
	// embedded and remote are OperatorType values, never capabilities.
	for _, invalid := range []string{"embedded", "remote", "cloud", "system", "node", "", "data,invalid"} {
		require.ErrorIs(t, capabilities.Set(invalid), ErrOperatorCapabilityInvalid)
		require.Equal(t, before, capabilities.String())
	}
	var decoded OperatorCapabilities
	require.NoError(t, json.Unmarshal([]byte(`["observer","data","observer"]`), &decoded))
	require.Equal(t, "data,observer", decoded.String())
	require.ErrorIs(t, json.Unmarshal([]byte(`["cloud"]`), &decoded), ErrOperatorCapabilityInvalid)
}

func TestOperatorCapabilitiesDefaultToDataAndPreserveExplicitCapabilities(t *testing.T) {
	require.Equal(t, OperatorCapabilities{OperatorCapabilityData}, OperatorCapabilities{}.WithDefault())
	require.Equal(t, OperatorCapabilities{OperatorCapabilityObserver}, OperatorCapabilities{OperatorCapabilityObserver}.WithDefault())
}

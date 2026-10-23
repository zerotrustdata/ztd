// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestValidateConsensusEndpoint(t *testing.T) {
	for _, endpoint := range []string{"https://app.example/deliberate", "https://[::1]:8443/deliberate?version=1"} {
		require.NoError(t, ValidateConsensusEndpoint(endpoint))
	}
	for _, endpoint := range []string{"", "/deliberate", "http://app.example", "https:///deliberate", "https://:8443", "https://user:password@app.example", "https://app.example#fragment", "https:app.example", "https://app.example:invalid"} {
		t.Run(endpoint, func(t *testing.T) {
			require.ErrorIs(t, ValidateConsensusEndpoint(endpoint), constants.ErrConsensusEndpointInvalid)
		})
	}
}

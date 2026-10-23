// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
)

func TestOperatorBootstrapRuntimeConfig_KeepsDeclaredHeartbeatInterval(t *testing.T) {
	cfg, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":120000,"capabilities":["inference"]}`),
	})
	require.NoError(t, err)
	assert.EqualValues(t, 120000, cfg.GetHeartbeatIntervalMs())
	assert.True(t, operatorcapability.ResolveOperatorCapabilities(cfg).Has(constants.OperatorCapabilityInference))
}

func TestOperatorBootstrapRuntimeConfig_AcceptsTheMaximumInterval(t *testing.T) {
	cfg, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":300000}`),
	})
	require.NoError(t, err)
	assert.EqualValues(t, constants.OperatorHeartbeatMaxInterval.Milliseconds(), cfg.GetHeartbeatIntervalMs())
}

func TestOperatorBootstrapRuntimeConfig_RejectsIntervalPastMaximum(t *testing.T) {
	_, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":300001}`),
	})
	require.ErrorIs(t, err, constants.ErrOperatorHeartbeatIntervalInvalid)
}

func TestOperatorBootstrapRuntimeConfig_RejectsUndecodableConfig(t *testing.T) {
	_, err := operatorBootstrapRuntimeConfig(&models.OperatorBootstrapRequest{
		RuntimeConfig: json.RawMessage(`{"heartbeat_interval_ms":"soon"}`),
	})
	require.ErrorIs(t, err, constants.ErrPlatformEnrollmentInvalidPayload)
}

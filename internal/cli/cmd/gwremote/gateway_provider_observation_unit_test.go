// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func TestRemoteProviderObservationClient_Load_RejectsMissingInputs(t *testing.T) {
	t.Parallel()
	_, _, err := (&remoteProviderObservationClient{}).Load(context.Background(), "attempt-1")
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrNotFound)

	client := &remoteProviderObservationClient{client: &cmdtest.MockAPIClient{}}
	_, _, err = client.Load(context.Background(), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrNotFound)
}

func TestNewProviderObservationRemote_ReturnsClient(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)

	remote, err := NewProviderObservationRemote(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, remote)
}

func TestNewCampaignWitnessCapture_ReturnsGatewayNotReachableWhenUnhealthy(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	cfg.Paths.Host = "http://127.0.0.1:1"

	capture, err := NewCampaignWitnessCapture(fileSvc, evaluation.NewStore(fileSvc), cfg)
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Nil(t, capture)
}

func TestNewCampaignWitnessCapture_WiresRemotesWhenHealthy(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	WithConfiguredGatewayHealthCheck(t, true)

	capture, err := NewCampaignWitnessCapture(fileSvc, evaluation.NewStore(fileSvc), cfg)
	require.NoError(t, err)
	require.NotNil(t, capture)
}

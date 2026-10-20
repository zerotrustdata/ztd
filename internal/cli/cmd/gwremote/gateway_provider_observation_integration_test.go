//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

func TestProviderObservationGatewayRead_UnknownAttemptIsNotFound(t *testing.T) {
	gateway := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:     "provider-observation-read",
		Posture:      constants.PostureDoctrine,
		NoDownstream: true,
	})
	gateway.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, gateway, "provider-observation-read-owner")
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	identity.InstallCLIRuntime(t, gateway, fileSvc, cfg)

	remote, err := NewProviderObservationRemote(fileSvc, cfg)
	require.NoError(t, err)
	require.NotNil(t, remote)

	_, _, err = remote.Load(t.Context(), "missing-attempt-integration-test")
	require.ErrorIs(t, err, constants.ErrHTTPStatusError)
	var statusErr *api.StatusError
	require.ErrorAs(t, err, &statusErr)
	require.Equal(t, http.StatusNotFound, statusErr.StatusCode)
}

// TestProviderObservationGatewayRead_UnreachableGatewayIsGatewayReadError points the
// observation read at a loopback port with nothing listening. The transport failure
// must surface as a gateway read.
func TestProviderObservationGatewayRead_UnreachableGatewayIsGatewayReadError(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	cmdtest.WithEndpointOverride(t, closedLoopbackURL(t))
	remote, err := NewProviderObservationRemote(fileSvc, cfg)
	require.NoError(t, err)

	_, _, err = remote.Load(t.Context(), "attempt-unreachable")
	require.Error(t, err)
	assert.ErrorContains(t, err, "provider observation: gateway read")
}

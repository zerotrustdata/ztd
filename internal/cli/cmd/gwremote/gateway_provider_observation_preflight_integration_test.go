// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gwremote

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestPreflightProviderObservationDelivery_AgainstGateway runs the preflight
// against real Gateways. Cases the old httptest fake simulated and that the
// real Gateway cannot produce (a non-ready status body, a non-object JSON body)
// are not reproduced; see the report for the dropped cases.
func TestPreflightProviderObservationDelivery_AgainstGateway(t *testing.T) {
	t.Run("accepts a Gateway whose provider-boundary observer is enrolled and deliverable", func(t *testing.T) {
		stack := fixtures.NewEvalStack(t, fixtures.EvalStackOptions{Posture: constants.PostureDoctrine})
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		stack.Identity.InstallCLIRuntime(t, stack.Gateway, fileSvc, cfg)

		require.NoError(t, PreflightProviderObservationDelivery(fileSvc, cfg))
	})

	t.Run("reports a Gateway without a provider-boundary observer as HTTP 503", func(t *testing.T) {
		gateway := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
			TestName:     "preflight-no-observer",
			Posture:      constants.PostureDoctrine,
			NoDownstream: true,
		})
		gateway.WaitForReady(t)
		identity := fixtures.BootstrapCLIIdentity(t, gateway, "preflight-no-observer-owner")
		fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
		identity.InstallCLIRuntime(t, gateway, fileSvc, cfg)

		err := PreflightProviderObservationDelivery(fileSvc, cfg)
		require.Error(t, err)
		require.ErrorContains(t, err, "provider observation preflight")
		require.ErrorIs(t, err, constants.ErrHTTPStatusError)
		var statusErr *api.StatusError
		require.ErrorAs(t, err, &statusErr)
		assert.Equal(t, http.StatusServiceUnavailable, statusErr.StatusCode)
		assert.Contains(t, statusErr.Body, constants.ErrProviderBoundaryObserverNotFound.Error())
	})

	t.Run("treats an unreachable Gateway endpoint as observation unavailable", func(t *testing.T) {
		cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
		// Reserve a loopback port, then release it so nothing listens there.
		port, reservation, err := netutil.ReserveTCPPort()
		require.NoError(t, err)
		require.NoError(t, reservation.Close())
		cmdtest.WithEndpointOverride(t, network.LocalhostHTTPURL(port))

		err = PreflightProviderObservationDelivery(fileSvc, cfg)
		assert.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
	})
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// TestPlatformEnrollment_Headless verifies the gateway-only deployment mode
// (headless): the gateway is running and bootstrapped, but no operator
// workloads are running. The user starts only the
// gateway process (./ztd gw start), bootstraps the owner identity, then runs:
//
//	./ztd test e2e --run TestPlatformEnrollment_Headless
//
// The test asserts that the gateway health and CA bundle endpoints succeed,
// that the authenticated pending enrollment list is empty (no workloads are
// running to submit requests), and that the operator list is empty (no
// operators are registered). This is the headless deployment mode: a
// usable gateway with no pending workloads or registered operators.
//
// This replaces the prior TestPlatformEnrollment_HeadlessGatewayOnly_E2E
// which used a per-test fixture and inspected container state directly. This version asserts the same invariants through typed
// gateway APIs and endpoint reachability checks.
func TestPlatformEnrollment_Headless(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultClientTimeout)
	defer cancel()

	// Precondition: this test requires a gateway-only deployment (headless
	// mode). If an Operator is registered, the platform is not in headless
	// mode and this test would produce false results — fail fast with an
	// actionable message.
	registered, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err, "authenticated operator list must succeed")
	if len(registered.Operators) > 0 {
		t.Fatalf("TestPlatformEnrollment_Headless requires a gateway-only deployment. " +
			"An Operator is registered, which means the full stack is running. " +
			"Start only the gateway (./ztd gw start), " +
			"bootstrap the owner (./ztd auth enroll user --headless), " +
			"then run: ./ztd test e2e --run TestPlatformEnrollment_Headless")
	}

	// Gateway health must succeed in headless mode. The gateway is the only
	// running service and must be reachable on its HTTP port.
	health, err := e2eClient.GetHealth(ctx, e2eCfg.gatewayHTTPURL)
	require.NoError(t, err, "gateway health must succeed in headless mode")
	assert.Equal(t, constants.GatewayModeStatusOK, health.Status,
		"gateway must report ok status in headless mode")
	t.Logf("gateway healthy in headless mode: status=%s version=%s", health.Status, health.Version)

	// CA bundle must be available in headless mode. The gateway serves the
	// trust bundle from the well-known PKI endpoint regardless of workload
	// state.
	bundle, err := e2eClient.GetCABundle(ctx, e2eCfg.gatewayHTTPURL)
	require.NoError(t, err, "CA bundle endpoint must succeed in headless mode")
	require.NotEmpty(t, bundle, "CA bundle must not be empty in headless mode")
	assert.Contains(t, string(bundle), "BEGIN CERTIFICATE",
		"CA bundle must contain PEM certificates in headless mode")
	t.Logf("CA bundle available in headless mode: %d bytes", len(bundle))

	// Authenticated pending list must be empty. No workloads are running,
	// so no platform enrollment requests have been submitted. The owner
	// authentication (mTLS + session header) must still succeed — headless
	// mode does not disable owner authentication.
	pending, err := e2eClient.GetPendingEnrollments(ctx)
	require.NoError(t, err,
		"authenticated pending list must succeed in headless mode — owner mTLS must work without workloads")
	assert.Empty(t, pending.Requests,
		"pending enrollment list must be empty in headless mode — no workloads are running to submit requests")
	t.Logf("pending enrollment list is empty in headless mode")

	// Operator list must be empty. No operators are registered because no
	// operator workload is running and no enrollment has been approved.
	operators, err := e2eClient.ListOperators(ctx)
	require.NoError(t, err,
		"authenticated operator list must succeed in headless mode — owner mTLS must work without workloads")
	assert.True(t, operators.Success, "operator list response must report success")
	assert.Empty(t, operators.Operators,
		"operator list must be empty in headless mode — no operators are registered")
	t.Logf("operator list is empty in headless mode")
}

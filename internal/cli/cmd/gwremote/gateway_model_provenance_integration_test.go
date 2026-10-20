//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gwremote

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
)

// TestRemoteModelProvenanceClient_Load_UnreachableGatewayIsGatewayReadError points
// the attestation read at a loopback port with nothing listening. The transport
// failure must surface as a gateway read.
func TestRemoteModelProvenanceClient_Load_UnreachableGatewayIsGatewayReadError(t *testing.T) {
	cfg, _, fileSvc := cmdtest.SetupApproveSSETestEnv(t)
	cmdtest.WithEndpointOverride(t, closedLoopbackURL(t))
	remote, err := newModelProvenanceRemote(fileSvc, cfg)
	require.NoError(t, err)

	_, err = remote.Load(t.Context(), "attempt-unreachable")
	require.Error(t, err)
	assert.ErrorContains(t, err, "model provenance: gateway read")
}

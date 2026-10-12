// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package platform

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCheckSpectatorListenersFree_RejectsBoundListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	pm := &ProcessManager{}
	cfg := serve.GatewayConfig{
		PublicSpectatorEnabled:     true,
		PublicSpectatorPrivateAddr: ln.Addr().String(),
		PublicSpectatorPublicAddr:  "127.0.0.1:0",
	}
	err = pm.checkSpectatorListenersFree(cfg)
	assert.ErrorIs(t, err, constants.ErrPortUnavailable)
}

func TestCheckSpectatorListenersFree_SkipsWhenSpectatorDisabled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	pm := &ProcessManager{}
	cfg := serve.GatewayConfig{
		PublicSpectatorEnabled:     false,
		PublicSpectatorPrivateAddr: ln.Addr().String(),
	}
	assert.NoError(t, pm.checkSpectatorListenersFree(cfg))
}

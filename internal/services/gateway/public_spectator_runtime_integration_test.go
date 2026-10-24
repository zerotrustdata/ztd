// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestPublicSpectatorRuntime_StartsMirrorListeners(t *testing.T) {
	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime, err := NewPublicSpectatorRuntime(PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  "127.0.0.1:0",
		PublicListenAddress:   "127.0.0.1:0",
		ExplorerListenAddress: "",
		TrustedProxyCIDRs:     []string{"172.28.0.1/32"},
	}, fileSvc, ks, logger)
	require.NoError(t, err)
	require.NoError(t, runtime.Start(ctx))

	requireBootstrapHealthy(t, "http://"+runtime.PublicListenAddress()+"/bootstrap")
	require.Len(t, runtime.mirror.trustedProxyNetworks, 1)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, runtime.Stop(stopCtx))
}

func TestPublicSpectatorRuntime_StartsDedicatedExplorerListener(t *testing.T) {
	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runtime, err := NewPublicSpectatorRuntime(PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  "127.0.0.1:0",
		PublicListenAddress:   "127.0.0.1:0",
		ExplorerListenAddress: "localhost:0",
	}, fileSvc, ks, logger)
	require.NoError(t, err)
	require.NoError(t, runtime.Start(ctx))

	requireBootstrapHealthy(t, "http://"+runtime.PublicListenAddress()+"/bootstrap")
	requireExplorerRuntime(t, "http://"+runtime.cfg.ExplorerListenAddress+"/runtime.json", "http://"+runtime.PublicListenAddress())

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	require.NoError(t, runtime.Stop(stopCtx))
}

func TestNewPublicSpectatorRuntime_ContainerBindRequiresExplicitConfig(t *testing.T) {
	cfg := PublicSpectatorConfig{
		Enabled:               true,
		PrivateListenAddress:  "0.0.0.0:0",
		PublicListenAddress:   "0.0.0.0:0",
		ExplorerListenAddress: "",
	}

	fileSvc := newProducerFileSvc(t)
	logger := testutil.NewTestLogger()
	ks := newTestKeystore(t, fileSvc, logger)

	_, err := NewPublicSpectatorRuntime(cfg, fileSvc, ks, logger)
	require.ErrorIs(t, err, constants.ErrPublicFeedListenAddress)

	cfg.AllowContainerBind = true
	_, err = NewPublicSpectatorRuntime(cfg, fileSvc, ks, logger)
	require.NoError(t, err)
}

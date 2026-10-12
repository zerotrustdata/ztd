// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package gw

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
)

func TestGatewayMasterKey_FlagPersistsAcrossRestart(t *testing.T) {
	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	resolved := resolveGatewayFlags(GatewayFlags{MasterKeyFile: " /run/secrets/original ", Posture: "doctrine", HTTPPort: 8080, HTTPSPort: 8443, CertIdentityMode: "localhost", LogLevel: "info"})
	require.Equal(t, "/run/secrets/original", resolved.MasterKeyFile)
	cfg := gatewayFlagsToServeConfig(resolved)
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, cfg))
	profile, err := serve.ReadLaunchProfile(fileSvc)
	require.NoError(t, err)
	require.Equal(t, "/run/secrets/original", profile.Config.MasterKeyFile)
	require.Empty(t, resolveGatewayFlags(GatewayFlags{MasterKeyFile: " "}).MasterKeyFile)
	require.Equal(t, "/explicit", resolveGatewayFlags(GatewayFlags{MasterKeyFile: " /explicit "}).MasterKeyFile)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// These tests drive `gw start` on a stopped Gateway through a real
// StartOperator success. They re-execute the test binary as the Gateway
// subprocess with ZTD_TEST_REEXEC=serve, so they perform real process
// management and network listeners and belong in Tier 2 (integration).

package gw

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
)

// unusedLoopbackPort returns a loopback port that was free when probed.
func unusedLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort(constants.LocalhostIP, "0"))
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

// The Gateway does not need Ollama to start: `gw start` must neither contact
// the configured inference endpoint nor print startup diagnostics about it.
// The endpoint must still reach the launch profile so the Operator that runs
// inference can use it later.
func TestGatewayStart_StoppedGatewayStartsWithoutContactingOllama(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	withServeReExec(t, fileSvc)
	startZombieReaper(t)

	var ollamaRequests atomic.Int32
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ollamaRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ollama.Close)

	cmd := gatewayStartCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc), nil)
	cmd.SetContext(t.Context())
	cmd.SetArgs([]string{
		"--posture", string(constants.PostureDoctrine),
		"--http-port", strconv.Itoa(unusedLoopbackPort(t)),
		"--https-port", strconv.Itoa(unusedLoopbackPort(t)),
		"--inference-ollama-endpoint", ollama.URL,
		// The public mirror listens on fixed loopback ports a developer
		// Gateway may hold.
		"--public-spectator=false",
	})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	require.NoError(t, cmd.Execute(), "stdout: %s\nstderr: %s", stdout.String(), stderr.String())

	assert.Contains(t, stdout.String(), "Gateway started")
	assert.Empty(t, stderr.String(), "a clean start prints no diagnostics")
	assert.Zero(t, ollamaRequests.Load(), "gw start must not contact the Ollama endpoint")

	pm, err := platform.NewProcessManager(fileSvc)
	require.NoError(t, err)
	running, _, err := pm.OperatorStatus()
	require.NoError(t, err)
	assert.True(t, running)

	profile, err := serve.ReadLaunchProfile(fileSvc)
	require.NoError(t, err)
	assert.Equal(t, ollama.URL, profile.Config.InferenceOllamaEndpoint)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestDiagnosticFromChildOutput_PrefersErrorRecordCause(t *testing.T) {
	t.Parallel()
	output := `2026-10-12T13:54:23.121-07:00 INFO: Gateway servers starting
  - component: gateway
2026-10-12T13:54:23.124-07:00 ERROR: Gateway service failed
  - component: gateway
  - error: gateway: start public spectator: listen 127.0.0.1:8081: bind: address already in use
2026-10-12T13:54:23.127-07:00 INFO: Gateway mode stopped
  - component: gateway
  - error: unrelated
`
	assert.Equal(t,
		"Gateway service failed: gateway: start public spectator: listen 127.0.0.1:8081: bind: address already in use",
		diagnosticFromChildOutput(output))
}

func TestDiagnosticFromChildOutput_ErrorRecordWithoutCause(t *testing.T) {
	t.Parallel()
	output := "2026-10-12T13:54:23.124-07:00 ERROR: Gateway service failed\n  - component: gateway\n"
	assert.Equal(t, "Gateway service failed", diagnosticFromChildOutput(output))
}

func TestDiagnosticFromChildOutput_PlainStderrFallsBackToLastLine(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "gateway child startup diagnostic", diagnosticFromChildOutput("noise\ngateway child startup diagnostic\n"))
}

func TestSpectatorListenerPorts_IncludesEvaluationExplorer(t *testing.T) {
	t.Parallel()
	ports, err := spectatorListenerPorts(serve.GatewayConfig{PublicSpectatorEnabled: true})
	require.NoError(t, err)
	assert.Equal(t, []spectatorListenerPort{
		{name: "public spectator ingest", port: constants.PublicSpectatorPrivatePort},
		{name: "public spectator", port: constants.PublicSpectatorPublicPort},
		{name: "evaluation explorer", port: constants.EvalExplorerDefaultPort},
	}, ports)

	ports, err = spectatorListenerPorts(serve.GatewayConfig{
		PublicSpectatorEnabled: true,
		EvalExplorerAddr:       "127.0.0.1:5999",
	})
	require.NoError(t, err)
	assert.Equal(t, 5999, ports[2].port, "a custom --eval-explorer-listen port must be the one probed")
}

func TestSpectatorListenerPorts_DisabledAndMalformed(t *testing.T) {
	t.Parallel()

	ports, err := spectatorListenerPorts(serve.GatewayConfig{EvalExplorerAddr: "127.0.0.1:5999"})
	require.NoError(t, err)
	assert.Empty(t, ports, "a disabled spectator binds no listeners")

	_, err = spectatorListenerPorts(serve.GatewayConfig{PublicSpectatorEnabled: true, EvalExplorerAddr: "127.0.0.1"})
	assert.ErrorIs(t, err, constants.ErrPortUnavailable)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
)

// newOutputCmd creates a cobra.Command whose output is captured in the returned buffer.
func newOutputCmd() (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

func TestPrintNextSteps_LoopbackOffersOnlyLocalEndpoints(t *testing.T) {
	t.Parallel()
	cmd, buf := newOutputCmd()
	printNextSteps(cmd, constants.PostureDoctrine, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "192.168.1.100")

	out := buf.String()
	bin := getBinaryName()
	assert.Contains(t, out, "Console  https://localhost:8443/console/")
	assert.Contains(t, out, bin+" login ")
	assert.Contains(t, out, bin+" login web")
	assert.Contains(t, out, bin+" operator start --endpoint localhost --working-dir ~/.ztd-operator")
	assert.Contains(t, out, bin+" auth enroll pending")
	assert.Contains(t, out, bin+" gw status")
	// A loopback Gateway is unreachable from the LAN, so no LAN address is offered.
	assert.NotContains(t, out, "192.168.1.100")
	assert.NotContains(t, out, "stateless relay")
	assert.NotContains(t, out, "auth enroll user")
}

func TestPrintNextSteps_PublicOffersRemoteEnrollment(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.10"} {
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, constants.PostureDoctrine, serve.GatewayConfig{ListenHost: host}, "192.168.1.100")
			out := buf.String()
			assert.Contains(t, out, getBinaryName()+" operator start -e 192.168.1.100 --working-dir ~/.ztd-operator")
			assert.NotContains(t, out, "--endpoint localhost")
		})
	}
}

func TestPrintNextSteps_PasskeyGuidanceFollowsPosture(t *testing.T) {
	t.Parallel()
	postures := []struct {
		posture  constants.Posture
		required bool
	}{
		{constants.PostureDoctrine, false},
		{constants.PostureConsensus, false},
		{constants.PostureRatify, true},
		{constants.PostureNotary, true},
	}
	for _, p := range postures {
		t.Run(string(p.posture), func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, p.posture, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "")
			out := buf.String()
			if p.required {
				assert.Contains(t, out, "Register a passkey (required: "+string(p.posture)+" posture enforces L3)")
			} else {
				assert.Contains(t, out, "Sign in headless (CLI and TUI)")
				assert.NotContains(t, out, "required")
			}
		})
	}
}

// Under an L2 posture, binding a consensus is the first next step: governed
// actions fail closed until one is bound. Doctrine and ratify do not gate on L2.
func TestPrintNextSteps_ConsensusBindLeadsUnderL2Postures(t *testing.T) {
	t.Parallel()
	for _, p := range []constants.Posture{constants.PostureConsensus, constants.PostureNotary} {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, p, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "")
			out := buf.String()
			assert.Contains(t, out, getBinaryName()+" consensus enroll tribunal --model <tag>")
			assert.True(t, strings.HasPrefix(out[strings.Index(out, "Next steps:"):], "Next steps:\n  Bind a consensus if none is bound"), out)
		})
	}
	for _, p := range []constants.Posture{constants.PostureDoctrine, constants.PostureRatify} {
		t.Run(string(p), func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, p, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "")
			assert.NotContains(t, buf.String(), "Bind a consensus")
		})
	}
}

// A Gateway that shifted off busy default ports must advertise the ports it
// bound, or every printed command dials a port nothing listens on.
func TestPrintNextSteps_ShiftedPortsAreSpelledOut(t *testing.T) {
	t.Parallel()
	shifted := serve.GatewayConfig{HTTPPort: 8087, HTTPSPort: 8450}

	cmd, buf := newOutputCmd()
	loopback := shifted
	loopback.ListenHost = constants.LocalhostIP
	printNextSteps(cmd, constants.PostureDoctrine, loopback, "192.168.1.100")
	out := buf.String()
	assert.Contains(t, out, "Console  https://localhost:8450/console/")
	assert.Contains(t, out, getBinaryName()+" operator start --endpoint localhost:8087 --port 8450 --working-dir ~/.ztd-operator")

	cmd, buf = newOutputCmd()
	public := shifted
	public.ListenHost = "0.0.0.0"
	printNextSteps(cmd, constants.PostureDoctrine, public, "192.168.1.100")
	assert.Contains(t, buf.String(), getBinaryName()+" operator start -e 192.168.1.100:8087 --port 8450 --working-dir ~/.ztd-operator")
}

func TestConsoleOriginFor(t *testing.T) {
	t.Parallel()
	assert.Equal(t, network.LocalhostHTTPSURL(constants.Ports.OperatorHttps), consoleOriginFor("", 0))
	assert.Equal(t, network.LocalhostHTTPSURL(8450), consoleOriginFor("", 8450))
	assert.Equal(t, "https://gw.example.com", consoleOriginFor("https://gw.example.com/", 8450))
}

func TestPrintNetworkIdentity_OnlyForRoutableListeners(t *testing.T) {
	t.Parallel()
	identity := &network.NetworkIdentity{IPs: []string{"192.168.1.10"}}

	cmd, buf := newOutputCmd()
	printNetworkIdentity(cmd, constants.LocalhostIP, identity)
	assert.Empty(t, buf.String())

	cmd, buf = newOutputCmd()
	printNetworkIdentity(cmd, "0.0.0.0", identity)
	assert.Contains(t, buf.String(), "192.168.1.10")
}

func TestPrintListenHostNotice(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"", constants.LocalhostIP, "127.0.0.2", "::1", "localhost"} {
		t.Run("loopback "+host, func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printListenHostNotice(cmd, host)
			assert.Contains(t, buf.String(), "Listening on loopback only")
			assert.Contains(t, buf.String(), "--listen-host 0.0.0.0")
		})
	}
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.10", "gateway.example"} {
		t.Run("reachable "+host, func(t *testing.T) {
			t.Parallel()
			cmd, buf := newOutputCmd()
			printListenHostNotice(cmd, host)
			assert.Empty(t, buf.String())
		})
	}
}

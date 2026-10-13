// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
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
	cmd, buf := newOutputCmd()
	printNextSteps(cmd, &governance.DoctrinePosture{}, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "192.168.1.100")

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
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.10"} {
		t.Run(host, func(t *testing.T) {
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, &governance.DoctrinePosture{}, serve.GatewayConfig{ListenHost: host}, "192.168.1.100")
			out := buf.String()
			assert.Contains(t, out, getBinaryName()+" operator start -e 192.168.1.100 --working-dir ~/.ztd-operator")
			assert.NotContains(t, out, "--endpoint localhost")
		})
	}
}

func TestPrintNextSteps_PasskeyGuidanceFollowsPosture(t *testing.T) {
	postures := []struct {
		posture  governance.GovernancePosture
		required bool
	}{
		{&governance.DoctrinePosture{}, false},
		{&governance.ConsensusPosture{}, false},
		{&governance.RatifyPosture{}, true},
		{&governance.NotaryPosture{}, true},
	}
	for _, p := range postures {
		t.Run(p.posture.Name(), func(t *testing.T) {
			cmd, buf := newOutputCmd()
			printNextSteps(cmd, p.posture, serve.GatewayConfig{ListenHost: constants.LocalhostIP}, "")
			out := buf.String()
			if p.required {
				assert.Contains(t, out, "Register a passkey (required: "+p.posture.Name()+" posture enforces L3)")
			} else {
				assert.Contains(t, out, "Sign in headless (CLI and TUI)")
				assert.NotContains(t, out, "required")
			}
		})
	}
}

// A Gateway that shifted off busy default ports must advertise the ports it
// bound, or every printed command dials a port nothing listens on.
func TestPrintNextSteps_ShiftedPortsAreSpelledOut(t *testing.T) {
	shifted := serve.GatewayConfig{HTTPPort: 8087, HTTPSPort: 8450}

	cmd, buf := newOutputCmd()
	loopback := shifted
	loopback.ListenHost = constants.LocalhostIP
	printNextSteps(cmd, &governance.DoctrinePosture{}, loopback, "192.168.1.100")
	out := buf.String()
	assert.Contains(t, out, "Console  https://localhost:8450/console/")
	assert.Contains(t, out, getBinaryName()+" operator start --endpoint localhost:8087 --port 8450 --working-dir ~/.ztd-operator")

	cmd, buf = newOutputCmd()
	public := shifted
	public.ListenHost = "0.0.0.0"
	printNextSteps(cmd, &governance.DoctrinePosture{}, public, "192.168.1.100")
	assert.Contains(t, buf.String(), getBinaryName()+" operator start -e 192.168.1.100:8087 --port 8450 --working-dir ~/.ztd-operator")
}

func TestConsoleOriginFor(t *testing.T) {
	assert.Equal(t, network.LocalhostHTTPSURL(constants.Ports.OperatorHttps), consoleOriginFor("", 0))
	assert.Equal(t, network.LocalhostHTTPSURL(8450), consoleOriginFor("", 8450))
	assert.Equal(t, "https://gw.example.com", consoleOriginFor("https://gw.example.com/", 8450))
}

func TestPrintNetworkIdentity_OnlyForRoutableListeners(t *testing.T) {
	identity := &network.NetworkIdentity{IPs: []string{"192.168.1.10"}}

	cmd, buf := newOutputCmd()
	printNetworkIdentity(cmd, constants.LocalhostIP, identity)
	assert.Empty(t, buf.String())

	cmd, buf = newOutputCmd()
	printNetworkIdentity(cmd, "0.0.0.0", identity)
	assert.Contains(t, buf.String(), "192.168.1.10")
}

func TestPrintListenHostNotice(t *testing.T) {
	for _, host := range []string{"", constants.LocalhostIP, "127.0.0.2", "::1", "localhost"} {
		t.Run("loopback "+host, func(t *testing.T) {
			cmd, buf := newOutputCmd()
			printListenHostNotice(cmd, host)
			assert.Contains(t, buf.String(), "Listening on loopback only")
			assert.Contains(t, buf.String(), "--listen-host 0.0.0.0")
		})
	}
	for _, host := range []string{"0.0.0.0", "::", "192.168.1.10", "gateway.example"} {
		t.Run("reachable "+host, func(t *testing.T) {
			cmd, buf := newOutputCmd()
			printListenHostNotice(cmd, host)
			assert.Empty(t, buf.String())
		})
	}
}

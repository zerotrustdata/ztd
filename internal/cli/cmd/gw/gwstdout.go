// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
)

// isLoopbackListenHost reports whether the HTTP/HTTPS listeners bind loopback
// only. An empty listenHost comes from a launch profile written before
// --listen-host existed, which the Gateway resolves to loopback.
func isLoopbackListenHost(listenHost string) bool {
	if listenHost == "" || listenHost == "localhost" {
		return true
	}
	ip := net.ParseIP(listenHost)
	return ip != nil && ip.IsLoopback()
}

// printListenHostNotice warns when the HTTP/HTTPS listeners are loopback-only,
// because the Gateway is then reachable from this host alone.
func printListenHostNotice(cmd *cobra.Command, listenHost string) {
	if !isLoopbackListenHost(listenHost) {
		return
	}
	if listenHost == "" {
		listenHost = constants.LocalhostIP
	}
	bin := getBinaryName()
	cmd.Printf("[ztd] Listening on loopback only (%s). Remote Operators, workstations, and LAN\n", listenHost)
	cmd.Println("      clients cannot reach this Gateway. To listen on all interfaces, run these")
	cmd.Println("      two commands in order:")
	cmd.Println()
	cmd.Printf("%s gw stop\n", bin)
	cmd.Printf("%s gw start --listen-host 0.0.0.0\n", bin)
	cmd.Println()
}

// printNetworkIdentity shows the detected network identity only when the
// listeners bind a routable interface, where the addresses tell the user what
// remote Operators can reach. A loopback Gateway is reachable as localhost
// alone, so the LAN inventory would only advertise unreachable addresses.
func printNetworkIdentity(cmd *cobra.Command, listenHost string, identity *network.NetworkIdentity) {
	if identity == nil || isLoopbackListenHost(listenHost) {
		return
	}
	cmd.Println(identity.FormatForDisplay())
	cmd.Println()
}

// nextStep is one command in the post-start guidance.
type nextStep struct {
	command string
	purpose string
}

// printNextSteps outputs where the Gateway is reachable and the commands that
// come next. Endpoints follow the listen host and the ports the Gateway
// actually bound: remote enrollment is offered only when the listeners bind a
// routable interface, and the Operator command carries the ports whenever
// startup shifted them off the defaults. Ratify and notary enforce L3 human
// proof, so they lead with the passkey sign-in; doctrine and consensus lead
// with the headless sign-in. A same-host Operator gets its own working
// directory, because Operator start refuses the Gateway's runtime tree.
func printNextSteps(cmd *cobra.Command, posture constants.Posture, cfg serve.GatewayConfig, externalIP string) {
	bin := getBinaryName()
	var steps []nextStep
	if posture.RequiresL2() {
		steps = append(steps, nextStep{bin + " consensus enroll tribunal --model <tag>", "Bind a consensus if none is bound (L2 posture: governed actions fail closed until one is bound)"})
	}
	if posture.RequiresL3() {
		steps = append(steps,
			nextStep{bin + " login web", "Register a passkey (required: " + string(posture) + " posture enforces L3)"},
			nextStep{bin + " login", "Headless CLI sign-in (Console and L3 approvals need a passkey)"})
	} else {
		steps = append(steps,
			nextStep{bin + " login", "Sign in headless (CLI and TUI)"},
			nextStep{bin + " login web", "Sign in with a browser passkey (enables the Console)"})
	}
	if isLoopbackListenHost(cfg.ListenHost) {
		steps = append(steps, nextStep{bin + " operator start " + operatorEndpointArgs("--endpoint", constants.LocalhostHostname, cfg) + " --working-dir ~/.ztd-operator", "Add an Operator on this host"})
	} else {
		steps = append(steps, nextStep{bin + " operator start " + operatorEndpointArgs("-e", externalIP, cfg) + " --working-dir ~/.ztd-operator", "Add an Operator (run on the Operator host)"})
	}
	steps = append(steps,
		nextStep{bin + " auth enroll pending", "Approve pending Operator enrollments"},
		nextStep{bin + " gw status", "Gateway, Operators, and enrollments"})

	cmd.Printf("Console  %s/console/\n", consoleOriginFor(cfg.PublicBaseURL, cfg.HTTPSPort))
	cmd.Println()
	cmd.Println("Next steps:")
	for i, step := range steps {
		if i > 0 {
			cmd.Println()
		}
		cmd.Println("  " + step.purpose + ":")
		cmd.Println(step.command)
	}
	cmd.Println()
}

// operatorEndpointArgs renders the Operator's Gateway endpoint flags. The bare
// host suffices on the default ports; a Gateway that bound other ports needs
// both spelled out, because the Operator would otherwise dial the defaults.
func operatorEndpointArgs(flag, host string, cfg serve.GatewayConfig) string {
	httpPort, httpsPort := effectivePort(cfg.HTTPPort, constants.Ports.OperatorHttp), effectivePort(cfg.HTTPSPort, constants.Ports.OperatorHttps)
	if httpPort == constants.Ports.OperatorHttp && httpsPort == constants.Ports.OperatorHttps {
		return flag + " " + host
	}
	return fmt.Sprintf("%s %s --port %d", flag, net.JoinHostPort(host, strconv.Itoa(httpPort)), httpsPort)
}

// effectivePort returns port, or the default when the config leaves it unset.
func effectivePort(port, defaultPort int) int {
	if port == 0 {
		return defaultPort
	}
	return port
}

// consoleOriginFor returns the origin that serves the browser Console: the
// configured public base URL when set, otherwise the loopback HTTPS origin on
// the port the Gateway bound (the default when httpsPort is unset).
func consoleOriginFor(publicBaseURL string, httpsPort int) string {
	if publicBaseURL != "" {
		return strings.TrimRight(publicBaseURL, "/")
	}
	return network.LocalhostHTTPSURL(effectivePort(httpsPort, constants.Ports.OperatorHttps))
}

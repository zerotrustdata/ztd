// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"log/slog"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
)

const (
	gatewayProbeWait = 1500 * time.Millisecond
	wslAddressWait   = 5 * time.Second
)

// gatewayDiscovery holds the host-specific lookups behind endpoint discovery
// so tests can replace them.
type gatewayDiscovery struct {
	goos         string
	reachable    func(ctx context.Context, host string, port int) bool
	wslAddresses func(ctx context.Context) []string
}

func defaultGatewayDiscovery() gatewayDiscovery {
	return gatewayDiscovery{
		goos:         runtime.GOOS,
		reachable:    tcpReachable,
		wslAddresses: queryWSLAddresses,
	}
}

// discoverImplicitGatewayHost keeps host when the Operator was given an
// explicit endpoint or the default loopback Gateway answers. A Windows
// Operator with no endpoint whose loopback Gateway is silent is usually
// talking to a Gateway inside WSL2, where Windows forwards localhost over
// IPv6 only and the Operator dials IPv4; the WSL VM's own IPv4 address is
// reachable, so it returns the first VM address that answers on port.
func (d gatewayDiscovery) discoverImplicitGatewayHost(ctx context.Context, explicit bool, host string, port int, logger *slog.Logger) string {
	if explicit || d.goos != "windows" || host != constants.DefaultEndpoint {
		return host
	}
	if d.reachable(ctx, host, port) {
		return host
	}
	for _, addr := range d.wslAddresses(ctx) {
		if d.reachable(ctx, addr, port) {
			logger.Info("Gateway not reachable on localhost; using the WSL VM address",
				"endpoint", addr, "port", port)
			return addr
		}
	}
	return host
}

func tcpReachable(ctx context.Context, host string, port int) bool {
	ctx, cancel := context.WithTimeout(ctx, gatewayProbeWait)
	defer cancel()
	conn, err := httpclient.IPv4DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// queryWSLAddresses asks the default WSL distribution for its IPv4 addresses.
// It returns nothing when WSL is absent or has no running distribution.
func queryWSLAddresses(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, wslAddressWait)
	defer cancel()
	out, err := exec.CommandContext(ctx, "wsl.exe", "-e", "hostname", "-I").Output()
	if err != nil {
		return nil
	}
	return parseWSLAddresses(string(out))
}

// parseWSLAddresses extracts the IPv4 addresses from `hostname -I` output.
func parseWSLAddresses(out string) []string {
	var addrs []string
	for _, field := range strings.Fields(out) {
		if ip := net.ParseIP(field); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			addrs = append(addrs, ip.String())
		}
	}
	return addrs
}

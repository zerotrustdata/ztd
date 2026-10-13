// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package netutil

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// SplitEndpoint splits an endpoint written as host, host:port, [v6], or
// [v6]:port into its host (brackets removed) and port. The port is 0 when the
// endpoint carries none. A bare IPv6 address has several colons and no
// brackets, so it is taken as a host with no port.
func SplitEndpoint(endpoint string) (string, int, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !hasPortSuffix(endpoint) {
		host, err := stripBrackets(endpoint)
		return host, 0, err
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %q: %w", constants.ErrGatewayEndpointInvalid, endpoint, err)
	}
	port, err := strconv.Atoi(portText)
	if host == "" || err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("%w: %q", constants.ErrGatewayEndpointInvalid, endpoint)
	}
	return host, port, nil
}

// IsLoopbackEndpoint reports whether endpoint names the local host: localhost,
// or a loopback or unspecified IP. A URL scheme and port are ignored. A
// malformed endpoint is not loopback.
func IsLoopbackEndpoint(endpoint string) bool {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return false
	}
	if idx := strings.Index(ep, "://"); idx != -1 {
		ep = ep[idx+3:]
	}
	host, _, err := SplitEndpoint(ep)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, constants.LocalhostHostname) {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// hasPortSuffix reports whether endpoint ends in ":port": "[v6]:port", or
// "name:port" with exactly one colon.
func hasPortSuffix(endpoint string) bool {
	if strings.HasPrefix(endpoint, "[") {
		return strings.Contains(endpoint, "]:")
	}
	return strings.Count(endpoint, ":") == 1
}

// stripBrackets returns the host of a bracketed "[v6]" endpoint, or the
// endpoint unchanged when it is not bracketed.
func stripBrackets(endpoint string) (string, error) {
	if !strings.HasPrefix(endpoint, "[") {
		return endpoint, nil
	}
	if !strings.HasSuffix(endpoint, "]") {
		return "", fmt.Errorf("%w: %q: unterminated IPv6 literal", constants.ErrGatewayEndpointInvalid, endpoint)
	}
	return endpoint[1 : len(endpoint)-1], nil
}

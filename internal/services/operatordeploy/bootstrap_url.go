// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package operatordeploy stages, starts, and watches Operator deployments
// locally and over SSH.
package operatordeploy

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
	"github.com/zerotrustdata/ztd/internal/netutil"
)

// GatewayPreflightTimeout bounds each Gateway preflight dial.
const GatewayPreflightTimeout = 10 * time.Second

// OperatorBootstrapURL returns the Gateway bootstrap websocket URL for an
// endpoint flag value. An http(s) scheme maps to ws(s); an endpoint port wins,
// otherwise httpPort (or the Operator HTTP default when zero) is used.
func OperatorBootstrapURL(endpoint string, httpPort int) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	var base string
	switch {
	case strings.HasPrefix(endpoint, "https://"):
		base = "wss://" + strings.TrimPrefix(endpoint, "https://")
	case strings.HasPrefix(endpoint, "http://"):
		base = "ws://" + strings.TrimPrefix(endpoint, "http://")
	case strings.Contains(endpoint, "://"):
		base = endpoint
	default:
		if host, port, err := netutil.SplitEndpoint(endpoint); err == nil && port == 0 {
			if httpPort == 0 {
				httpPort = constants.Ports.OperatorHttp
			}
			endpoint = net.JoinHostPort(host, strconv.Itoa(httpPort))
		}
		base = "ws://" + endpoint
	}
	return strings.TrimRight(base, "/") + constants.APIPaths.AuthOperatorBootstrapWebSocket
}

// ValidateGatewayPort reports whether a Gateway HTTP or HTTPS port value is valid.
func ValidateGatewayPort(flag string, port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("--%s %d: %w", flag, port, constants.ErrOperatorGatewayPortInvalid)
	}
	return nil
}

// RunGatewayPreflight opens the bootstrap websocket a worker enrolls over and
// closes it before sending a request frame, so no enrollment request is
// created, then opens a TCP connection to the mTLS port the enrolled worker
// dials. The worker verifies that port's certificate against the trust its
// enrollment delivers; the preflight has no trust to check it against and
// proves reachability only.
func RunGatewayPreflight(ctx context.Context, host string, httpPort, httpsPort int) error {
	if httpsPort == 0 {
		httpsPort = constants.Ports.OperatorHttps
	}
	ctx, cancel := context.WithTimeout(ctx, GatewayPreflightTimeout)
	defer cancel()

	bootstrapURL := OperatorBootstrapURL(host, httpPort)
	conn, resp, err := httpclient.WebSocketDialerWithTLS(nil).DialContext(ctx, bootstrapURL, nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("%w: gateway preflight: bootstrap websocket %s: %w", constants.ErrGatewayNotReachable, bootstrapURL, err)
	}
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	if err := conn.Close(); err != nil {
		return fmt.Errorf("gateway preflight: close bootstrap websocket: %w", err)
	}

	mtlsAddr := net.JoinHostPort(host, strconv.Itoa(httpsPort))
	tcp, err := (&net.Dialer{}).DialContext(ctx, "tcp", mtlsAddr)
	if err != nil {
		return fmt.Errorf("%w: gateway preflight: mTLS port %s: %w", constants.ErrGatewayNotReachable, mtlsAddr, err)
	}
	return tcp.Close()
}

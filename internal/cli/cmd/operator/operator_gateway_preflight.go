// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package operatorcmd

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/httpclient"
)

const operatorGatewayPreflightTimeout = 10 * time.Second

// operatorGatewayPreflightCmd checks, from the host or container a worker will
// run in, both Gateway connections the worker opens. operator deploy runs it on
// each target before starting workers so an unreachable Gateway stops the
// rollout.
func operatorGatewayPreflightCmd() *cobra.Command {
	var httpPort, httpsPort int
	cmd := &cobra.Command{
		Use:   "gateway-preflight <gateway-host>",
		Short: "Verify the Gateway bootstrap websocket and mTLS port are reachable from this host",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !operatorDeployEndpointPattern.MatchString(args[0]) {
				return fmt.Errorf("%w: gateway host %q must match %s", constants.ErrPathValidation, args[0], operatorDeployEndpointPattern)
			}
			if err := validateGatewayPort("gateway-http-port", httpPort); err != nil {
				return err
			}
			if err := validateGatewayPort("gateway-https-port", httpsPort); err != nil {
				return err
			}
			return runGatewayPreflight(cmd.Context(), args[0], httpPort, httpsPort)
		},
	}
	cmd.Flags().IntVar(&httpPort, "gateway-http-port", 0, "Gateway bootstrap websocket port to dial (default: platform default)")
	cmd.Flags().IntVar(&httpsPort, "gateway-https-port", 0, "Gateway mTLS command-channel port to dial (default: platform default)")
	return cmd
}

// runGatewayPreflight opens the bootstrap websocket a worker enrolls over and
// closes it before sending a request frame, so no enrollment request is
// created, then opens a TCP connection to the mTLS port the enrolled worker
// dials. The worker verifies that port's certificate against the trust its
// enrollment delivers; the preflight has no trust to check it against and
// proves reachability only.
func runGatewayPreflight(ctx context.Context, host string, httpPort, httpsPort int) error {
	if httpsPort == 0 {
		httpsPort = constants.Ports.OperatorHttps
	}
	ctx, cancel := context.WithTimeout(ctx, operatorGatewayPreflightTimeout)
	defer cancel()

	bootstrapURL := serve.OperatorBootstrapURL(host, httpPort)
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

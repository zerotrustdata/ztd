// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package operatorcmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

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
			if !operatordeploy.EndpointPattern.MatchString(args[0]) {
				return fmt.Errorf("%w: gateway host %q must match %s", constants.ErrPathValidation, args[0], operatordeploy.EndpointPattern)
			}
			if err := operatordeploy.ValidateGatewayPort("gateway-http-port", httpPort); err != nil {
				return err
			}
			if err := operatordeploy.ValidateGatewayPort("gateway-https-port", httpsPort); err != nil {
				return err
			}
			return operatordeploy.RunGatewayPreflight(cmd.Context(), args[0], httpPort, httpsPort)
		},
	}
	cmd.Flags().IntVar(&httpPort, "gateway-http-port", 0, "Gateway bootstrap websocket port to dial (default: platform default)")
	cmd.Flags().IntVar(&httpsPort, "gateway-https-port", 0, "Gateway mTLS command-channel port to dial (default: platform default)")
	return cmd
}

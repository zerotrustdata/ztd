// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/output"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func operatorTerminateCmd() *cobra.Command {
	return operatorTerminateCmdWithConfig(shared.LoadConfig, authcmd.DefaultAPIClientFactory, shared.NewFileSvc)
}

func operatorTerminateCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory authcmd.APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:          "terminate <operator-id>",
		Short:        "Permanently retire an Operator record and release its capabilities",
		SilenceUsage: true,
		Long: `Mark one of your remote Operator records terminated in the Gateway registry.
A terminated Operator can no longer authenticate, and the capabilities it held
(inference, provenance, observer) become free for another enrollment. Use it
for a stale or abandoned Operator whose identity no longer matches a running
runtime. It does not stop a process; use 'ztd operator stop' for that. The
operator ID is the OPERATOR ID column of 'ztd gw status' or 'ztd operator list'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			operatorID := strings.TrimSpace(args[0])
			if operatorID == "" {
				return constants.ErrRegistrationOperatorIDRequired
			}
			cfg, err := configLoader("")
			if err != nil {
				return err
			}
			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			if creds, err := auth.LoadCredentials(fileSvc, cfg); err != nil || creds == nil {
				return constants.ErrNotAuthenticated
			}
			client, err := clientFactory(fileSvc, cfg)
			if err != nil {
				return err
			}
			body, err := client.Post(constants.APIPaths.OperatorsByID+url.PathEscape(operatorID),
				models.TerminateOperatorRequest{OperatorID: operatorID, Reason: strings.TrimSpace(reason)})
			if err != nil {
				return fmt.Errorf("operator terminate %s: %w", operatorID, err)
			}
			var response models.TerminateOperatorResponse
			if err := json.Unmarshal(body, &response); err != nil {
				return fmt.Errorf("operator terminate %s: decode response: %w", operatorID, err)
			}
			if !response.Success {
				return fmt.Errorf("operator terminate %s: %s", operatorID, response.Message)
			}
			if output.JSONEnabled(cmd) {
				return output.WriteJSON(cmd.OutOrStdout(), response)
			}
			cmd.Printf("Operator %s terminated; its capabilities are released.\n", operatorID)
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "Reason recorded with the termination")
	return cmd
}

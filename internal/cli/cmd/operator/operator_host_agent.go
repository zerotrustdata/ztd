// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

func operatorDeployHostCmd() *cobra.Command {
	return operatorDeployHostCmdWithFactory(shared.NewFileSvc)
}

func operatorDeployHostCmdWithFactory(factory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "deploy-host",
		Short:        "Remote host agent for operator deployment and management",
		Hidden:       true,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req models.DeployHostRequest
			decoder := json.NewDecoder(cmd.InOrStdin())
			if err := decoder.Decode(&req); err != nil {
				resp := models.DeployHostResponse{
					Success: false,
					Error:   fmt.Sprintf("decode deploy-host request: %v", err),
				}
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
				return fmt.Errorf("decode deploy-host request: %w", err)
			}

			resp, err := operatordeploy.ExecuteDeployHost(cmd.Context(), req, factory)
			if err != nil {
				resp.Success = false
				resp.Error = err.Error()
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
				return err
			}

			resp.Success = true
			return json.NewEncoder(cmd.OutOrStdout()).Encode(resp)
		},
	}
	return cmd
}

// ExecuteDeployHostStop stops every worker under destDir or matching workingDir concurrently.
func ExecuteDeployHostStop(ctx context.Context, destDir, workingDir string) ([]int, error) {
	return operatordeploy.ExecuteDeployHostStop(ctx, destDir, workingDir)
}

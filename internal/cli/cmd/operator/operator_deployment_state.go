// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.

package operatorcmd

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/operatordeploy"
)

func operatorDeploymentStateCmd() *cobra.Command {
	return operatorDeploymentStateCmdWithFactory(shared.NewFileSvc)
}

func operatorDeploymentStateCmdWithFactory(factory func(string, *slog.Logger) (fs.RuntimeFileService, error)) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use: "deployment-state", Short: "Read local non-secret Operator deployment progress as JSON", Args: cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fileSvc, err := factory(dir, slog.Default())
			if err != nil {
				return fmt.Errorf("operator deployment state: %w: %w", constants.ErrFileServiceInit, err)
			}
			state, err := operatordeploy.ReadOperatorDeploymentState(cmd.Context(), fileSvc)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(state)
		},
	}
	cmd.Flags().StringVar(&dir, "working-dir", "", "Operator working directory (default: current directory)")
	return cmd
}

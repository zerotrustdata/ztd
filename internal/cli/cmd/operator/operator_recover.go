// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
)

func operatorRecoverCmd() *cobra.Command {
	return operatorRecoverCmdWithConfig(shared.LoadConfig, shared.NewFileSvc)
}

func operatorRecoverCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
) *cobra.Command {
	var workingDir string
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Install a saved operator recovery key",
		Long: `Install the recovery key saved when this operator's master key was created.

The key is read from the terminal and is not logged. Pass --working-dir for the
operator runtime you are restoring. An OS key store that already holds a
different master key is left unchanged. If this runtime has sealed secrets, the
key must open them or nothing is stored.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveOperatorWorkingDir(workingDir)
			if err != nil {
				return fmt.Errorf("operator recover: --working-dir: %w", err)
			}
			cfg, err := configLoader("")
			if err != nil {
				return fmt.Errorf("operator recover: load config: %w", err)
			}
			fileSvc, err := fileSvcFactory(resolved, slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			return shared.RecoverRuntimeKey(cmd.InOrStdin(), cmd.ErrOrStderr(), fileSvc, cfg.Keyring, keystore.RecoveryRoleOperator)
		},
	}
	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Operator runtime directory (default: current directory)")
	return cmd
}

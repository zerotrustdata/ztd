// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

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

func gatewayRecoverCmd() *cobra.Command {
	return gatewayRecoverCmdWithConfig(shared.LoadConfig, shared.NewFileSvc)
}

func gatewayRecoverCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Install a saved gateway recovery key",
		Long: `Install the recovery key saved when this gateway's master key was created.

The key is read from the terminal and is not logged. An OS key store that
already holds a different master key is left unchanged. If this runtime has
sealed secrets, the key must open them or nothing is stored.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := configLoader("")
			if err != nil {
				return fmt.Errorf("gateway: load config: %w", err)
			}
			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}
			return shared.RecoverRuntimeKey(cmd.InOrStdin(), cmd.ErrOrStderr(), fileSvc, cfg.Keyring, keystore.RecoveryRoleGateway)
		},
	}
	return cmd
}

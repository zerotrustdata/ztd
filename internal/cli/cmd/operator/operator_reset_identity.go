// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatorcmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/identityreset"
	"github.com/zerotrustdata/ztd/internal/models"
)

func operatorResetIdentityCmd() *cobra.Command {
	return operatorResetIdentityCmdWithDiscovery(discoverLocalOperators)
}

func operatorResetIdentityCmdWithDiscovery(discover func() ([]localOperatorProcess, error)) *cobra.Command {
	var directory string
	var yes bool
	cmd := &cobra.Command{
		Use: "reset-identity", Short: "Stop a local operator and remove its enrollment identity", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(directory) == "" {
				return fmt.Errorf("--working-dir is required")
			}
			dir, err := resolveOperatorWorkingDir(directory)
			if err != nil {
				return err
			}
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			} else {
				return err
			}
			proceed, err := shared.ConfirmDestructive(cmd, shared.DestructiveOptions{
				Effects: []string{"Stop local operator workers in " + dir, "Remove CLI session credentials, the CLI certificate and key, and cached Gateway trust", "Preserve working data, model files, vault keys, configuration, and logs; fresh enrollment requires gateway approval"}, AssumeYes: yes,
			})
			if err != nil || !proceed {
				return err
			}
			workers, err := discover()
			if err != nil {
				return err
			}
			defer func() {
				for _, worker := range workers {
					worker.close()
				}
			}()
			for _, worker := range workers {
				if worker.dir != dir {
					continue
				}
				result := stopLocalOperator(cmd, worker, models.StopOperatorResponse{}, nil, time.Second)
				if !result.Success {
					return fmt.Errorf("reset identity: %s", result.Error)
				}
			}
			if err := identityreset.Reset(dir); err != nil {
				return err
			}
			cmd.Printf("Identity reset: %s. Start the operator to request fresh enrollment.\n", dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&directory, "working-dir", "", "Operator working directory (required)")
	_ = cmd.MarkFlagRequired("working-dir")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm identity reset")
	return cmd
}

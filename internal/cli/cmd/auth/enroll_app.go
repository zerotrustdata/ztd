// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package authcmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// AppEnroller is the interface satisfied by *auth.AppPlatformEnrollmentClient.
type AppEnroller interface {
	Enroll(ctx context.Context, out io.Writer) (*models.PlatformEnrollmentCompleteResponse, error)
}

// AppEnrollerFactory creates an AppEnroller instance.
type AppEnrollerFactory func(appName string, fileSvc fs.RuntimeFileService, cfg *config.Config, logger *slog.Logger, opts ...auth.AppEnrollmentOptions) (AppEnroller, error)

// DefaultAppEnrollerFactory creates a production AppPlatformEnrollmentClient.
func DefaultAppEnrollerFactory(appName string, fileSvc fs.RuntimeFileService, cfg *config.Config, logger *slog.Logger, opts ...auth.AppEnrollmentOptions) (AppEnroller, error) {
	return auth.NewAppPlatformEnrollmentClient(appName, fileSvc, cfg, logger, opts...)
}

func enrollAppCmd() *cobra.Command {
	return enrollAppCmdWithConfig(shared.LoadConfig, shared.NewFileSvc, DefaultAppEnrollerFactory)
}

func enrollAppCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
	enrollerFactory AppEnrollerFactory,
) *cobra.Command {
	var signingKey bool
	cmd := &cobra.Command{
		Use:   "app <name>",
		Short: "Enroll an application workload with the running Gateway via owner-approved platform enrollment",
		Long: `Enroll an application workload with the running Gateway via owner-approved platform enrollment.

The authenticated owner approves inline under doctrine and consensus. Ratify and notary require
passkey approval at the printed URL. The client holds a status request until the owner decides.
Without an owner CLI identity, approve with ztd auth enroll approve <request-id>.

Use --signing-key to generate an independent Ed25519 L2 signing identity alongside the P-256
transport identity. Both keys are bound to owner approval and completion. Private material is
sealed in managed runtime storage, including interrupted attempts.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appName := strings.TrimSpace(args[0])

			cfg, err := configLoader("")
			if err != nil {
				return err
			}

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			opt := auth.AppEnrollmentOptions{GenerateSigningKey: signingKey}
			if client, clientErr := DefaultAPIClientFactory(fileSvc, cfg); clientErr == nil {
				opt.Approver = auth.OwnerAppEnrollmentApprover(client, "ztd auth enroll app: enrolled by the authenticated owner")
			}

			enroller, err := enrollerFactory(appName, fileSvc, cfg, slog.Default(), opt)
			if err != nil {
				return err
			}

			_, err = enroller.Enroll(cmd.Context(), cmd.OutOrStdout())
			return err
		},
	}

	cmd.Flags().BoolVar(&signingKey, "signing-key", false, "Generate and enroll a separate sealed Ed25519 L2 signing identity")
	return cmd
}

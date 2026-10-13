// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package autochthoncmd

import (
	"fmt"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/autochthon"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/logging"
)

// autochthonFlags are the only flags ztd autochthon accepts. The inherited
// root --endpoint and --port configure a Gateway and are refused.
var autochthonFlags = []string{"working-dir", "log"}

// statusFlags are the only flags ztd autochthon status accepts.
var statusFlags = []string{"working-dir", "generations", "surprises", "anchor", "json"}

// Cmd runs the Gateway-less autochthon in the foreground until SIGINT or SIGTERM.
func Cmd() *cobra.Command {
	var workingDir string
	var logLevel string

	cmd := &cobra.Command{
		Use:   "autochthon",
		Short: "Run with no Gateway: sample this host and breed a local prior",
		Long: `Run the autochthon in the foreground. It does not dial a Gateway, enroll, or listen, and it accepts no envelope. It samples this host, including TERM, once a second and breeds a local prior under .ztd/autochthon/ in the working directory.

Only --working-dir and --log are accepted; any Gateway-facing flag, including the inherited --endpoint and --port, fails before any file is opened. Verify and report on a runtime root with:
ztd autochthon status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, workingDir, logLevel)
		},
	}

	cmd.Flags().StringVar(&workingDir, "working-dir", "", "Runtime root; state is written under .ztd/autochthon/")
	cmd.Flags().StringVarP(&logLevel, "log", "l", "info", "Log level: info, error, debug")

	cmd.AddCommand(statusCmd())
	return cmd
}

// refuseFlags fails on any set flag, local or inherited, not in allowed.
func refuseFlags(cmd *cobra.Command, allowed []string) error {
	var refused error
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if refused == nil && !slices.Contains(allowed, f.Name) {
			refused = fmt.Errorf("%w: --%s", constants.ErrAutochthonGatewayFlag, f.Name)
		}
	})
	return refused
}

// run refuses Gateway-facing flags, then runs until SIGINT or SIGTERM.
func run(cmd *cobra.Command, workingDir, logLevel string) error {
	if err := refuseFlags(cmd, autochthonFlags); err != nil {
		return err
	}
	logger, err := logging.NewStdoutLogger(logLevel)
	if err != nil {
		return err
	}
	files, err := fs.NewRuntimeFileService(workingDir, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return autochthon.Run(ctx, files, logger, autochthon.Options{})
}

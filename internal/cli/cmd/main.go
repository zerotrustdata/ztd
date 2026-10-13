// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/audit"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	autochthoncmd "github.com/zerotrustdata/ztd/internal/cli/cmd/autochthon"
	compliancecmd "github.com/zerotrustdata/ztd/internal/cli/cmd/compliance"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/eval"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gw"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/mcp"
	operatorcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/operator"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/public"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/report"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/swagger"
	testcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/test"
	tuicmd "github.com/zerotrustdata/ztd/internal/cli/cmd/tui"
	vaultcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/vault"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/version"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
)

var osExit = os.Exit

func NewRootCmd(cliVersion string, vi serve.VersionInfo) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "ztd",
		Version:       cliVersion,
		Short:         "ztd Platform Manager - CLI for the ztd Gateway, ztd Operator, and platform setup",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `ztd is a zero-trust execution platform for agentic infrastructure.
The CLI manages the ztd Gateway (ztdg), ztd Operator (ztdo), and platform setup.

Launch the Tactical Governance Console (TUI) with:
ztd tui`,
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			endpoint, err := cmd.Flags().GetString("endpoint")
			if err != nil {
				return fmt.Errorf("root: get endpoint flag: %w", err)
			}
			// Read the root persistent flag directly: subcommands such as
			// operator deploy define their own --port for SSH, which shadows
			// this Gateway HTTPS port in cmd.Flags().
			port, err := cmd.Root().PersistentFlags().GetInt("port")
			if err != nil {
				return fmt.Errorf("root: get port flag: %w", err)
			}
			// A subcommand's own --master-key-file shadows the root flag in
			// cmd.Flags(); either way the same value seals the client identity
			// keys, which share the runtime tree's master key.
			if masterKeyFlag := cmd.Flags().Lookup("master-key-file"); masterKeyFlag != nil {
				config.SetMasterKeyFileOverride(masterKeyFlag.Value.String())
			}
			if endpoint != "" {
				if port > 0 {
					if strings.Contains(endpoint, "://") {
						config.SetEndpointOverride(endpoint)
					} else {
						host := endpoint
						if h, _, err := net.SplitHostPort(host); err == nil {
							host = h
						}
						config.SetHTTPEndpointOverride(endpoint)
						config.SetHTTPSEndpointOverride(fmt.Sprintf("%s:%d", host, port))
					}
				} else {
					config.SetEndpointOverride(endpoint)
				}
			} else if port > 0 {
				config.SetHTTPEndpointOverride("localhost")
				config.SetHTTPSEndpointOverride(fmt.Sprintf("localhost:%d", port))
			}
			return nil
		},
	}

	ctx := rootCmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	rootCmd.SetContext(shared.ContextWithVersionInfo(ctx, vi))

	rootCmd.PersistentFlags().StringP("endpoint", "e", "", "Gateway HTTP discovery endpoint (host or host:port) for remote enrollment")
	rootCmd.PersistentFlags().IntP("port", "p", 0, "Gateway HTTPS/mTLS port (overrides default 8443; use with --endpoint)")
	rootCmd.PersistentFlags().String("master-key-file", "", "Absolute path to a provisioned base64 32-byte master key outside the runtime root; Linux mode 0400 or 0600. Blank uses the OS key store. Seals and unseals the CLI and app identity keys under .ztd/. No fallback on failure.")
	rootCmd.PersistentFlags().Bool("json", false, "Emit machine-readable JSON output (pretty-printed)")

	rootCmd.AddCommand(
		gw.Cmd(),
		authcmd.LoginCmd(),
		authcmd.LogoutCmd(),
		authcmd.Cmd(),
		mcp.Cmd(),
		operatorcmd.Cmd(),
		autochthoncmd.Cmd(),
		vaultcmd.Cmd(),
		testcmd.Cmd(),
		audit.Cmd(),
		report.Cmd(),
		public.Cmd(),
		swagger.Cmd(),
		tuicmd.Cmd(),
		version.Cmd(),
		compliancecmd.Cmd(),
		eval.Cmd(),
	)

	return rootCmd
}

func ExecuteWithVersionInfo(vi serve.VersionInfo) {
	rootCmd := NewRootCmd(vi.Version, vi)
	rootCmd.SetVersionTemplate(`{{with .Version}}{{printf "ztd version %s\n" .}}{{end}}`)
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)

	if err := config.LoadEnvFile(constants.EnvFilename); err != nil {
		fmt.Fprintf(rootCmd.ErrOrStderr(), "Error: %v\n", err)
		osExit(1)
		return
	}
	if err := executeRootCmd(rootCmd); err != nil {
		osExit(1)
	}
}

func executeRootCmd(rootCmd *cobra.Command) error {
	err := rootCmd.Execute()
	if err != nil {
		fmt.Fprintf(rootCmd.ErrOrStderr(), "Error: %v\n", err)
	}
	return err
}

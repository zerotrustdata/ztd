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
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// renewCLIIdentity renews the local CLI certificate before the command runs
// once it is past its renewal time, keeping its CLI session
// (auth.RenewCLIIdentityIfDue). Short-lived commands are how a CLI identity
// is used, so this is where it renews, the way an Operator renews its own
// leaf. A runtime root without a complete CLI identity, or one that is not
// due, is left alone without contacting the Gateway, and a command that runs
// excluded by shared.NeedsCLIRenewal reads no identity file. A
// failed renewal is reported and the command still runs: the certificate stays
// valid for the final third of its lifetime and the next command tries again.
func renewCLIIdentity(cmd *cobra.Command) {
	if !shared.NeedsCLIRenewal(cmd) {
		return
	}
	cfg, fileSvc, err := cliRenewalRuntime(cmd)
	if err != nil {
		return
	}
	renewed, err := auth.RenewCLIIdentityIfDue(shared.CommandContext(cmd), fileSvc, cfg, auth.NewEnrollmentClient(cfg, nil), auth.FileKeyProvider{}, time.Now)
	if err != nil {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: CLI certificate renewal failed; the next ztd command retries: %v\n", err)
		return
	}
	if renewed {
		_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "Renewed the CLI certificate (CLI session unchanged).")
	}
}

// wrapCLIRenewal scopes background renewal to each command's Run lifetime,
// including commands that return an error. Every Gateway client command gets
// the same lifecycle, including MCP, TUI, evaluations, and approval streams.
func wrapCLIRenewal(cmd *cobra.Command) {
	for _, child := range cmd.Commands() {
		wrapCLIRenewal(child)
	}
	if !shared.NeedsCLIRenewal(cmd) {
		return
	}
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			stop := startCLIRenewal(cmd)
			defer stop()
			return run(cmd, args)
		}
	} else if run := cmd.Run; run != nil {
		cmd.Run = func(cmd *cobra.Command, args []string) {
			stop := startCLIRenewal(cmd)
			defer stop()
			run(cmd, args)
		}
	}
}

func startCLIRenewal(cmd *cobra.Command) func() {
	if !shared.NeedsCLIRenewal(cmd) {
		return func() {}
	}
	cfg, fileSvc, err := cliRenewalRuntime(cmd)
	if err != nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(shared.CommandContext(cmd))
	done := make(chan struct{})
	go func() {
		defer close(done)
		auth.CLIRenewalLoop(ctx, fileSvc, cfg, auth.NewEnrollmentClient(cfg, nil), auth.FileKeyProvider{}, slog.Default())
	}()
	return func() {
		cancel()
		<-done
	}
}

func cliRenewalRuntime(cmd *cobra.Command) (*config.Config, fs.RuntimeFileService, error) {
	projectRoot := ""
	if cmd.Flags().Lookup("project-root") != nil {
		var err error
		projectRoot, err = cmd.Flags().GetString("project-root")
		if err != nil {
			return nil, nil, fmt.Errorf("CLI renewal: project root: %w", err)
		}
	}
	cfg, err := shared.LoadConfig(projectRoot)
	if err != nil {
		return nil, nil, err
	}
	fileSvc, err := shared.NewFileSvc(cfg.ProjectRoot, slog.Default())
	return cfg, fileSvc, err
}

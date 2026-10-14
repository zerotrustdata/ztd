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
	"log/slog"
	"os"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/spf13/cobra"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

type APIClient interface {
	Get(path string) ([]byte, error)
	Post(path string, body interface{}) ([]byte, error)
	Put(path string, body interface{}) ([]byte, error)
	Delete(path string) ([]byte, error)
}

type APIClientFactory func(fs.RuntimeFileService, *config.Config) (APIClient, error)

func DefaultAPIClientFactory(fileSvc fs.RuntimeFileService, cfg *config.Config) (APIClient, error) {
	return api.NewClient(fileSvc, cfg)
}

func approveCmd() *cobra.Command {
	return approveCmdWithConfig(shared.LoadConfig, DefaultAPIClientFactory, shared.NewFileSvc, platform.OpenBrowser)
}

func approveCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
	openBrowser func(string) error,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <transaction_hash>",
		Short: "Approve a suspended L3 transaction via browser WebAuthn",
		Long: `Approve a suspended transaction by opening the gateway's browser-based approval page.
The browser handles the WebAuthn/passkey ceremony; the CLI subscribes to the
gateway's SSE stream and waits for the approval.completed event. CLI credentials
(mTLS) are required for L3 approval flows. L3 approval always needs a passkey
assertion; there is no CLI-only approval.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			txHash := args[0]
			cfg, err := configLoader("")
			if err != nil {
				return err
			}

			fileSvc, err := fileSvcFactory("", slog.Default())
			if err != nil {
				return fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)
			}

			client, err := clientFactory(fileSvc, cfg)
			if err != nil {
				return fmt.Errorf("approve: create API client: %w", err)
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Subscribe before the browser opens: the approver can complete the approval as
			// soon as the page is up, and the stream must already hold that event.
			approvals, err := subscribeApproval(ctx, fileSvc, cfg)
			if err != nil {
				return err
			}
			defer approvals.Close()

			// Build the browser approval URL (public endpoint, redirects to console SPA)
			approvalURL := auth.ApprovalPageURL(cfg, txHash)

			cmd.Printf("Opening browser for WebAuthn approval...\n")
			cmd.Printf("  Transaction: %s\n", txHash)
			cmd.Printf("  URL: %s\n", approvalURL)

			if err := openBrowser(approvalURL); err != nil {
				cmd.Printf("Failed to auto-open browser: %v\n", err)
				fmt.Fprintf(os.Stderr, "\n[ztd] Please visit: %s\n", approvalURL)
			}

			return waitForApprovalAndVerify(ctx, cmd, approvals, client, txHash)
		},
	}

	return cmd
}

// subscribeApproval loads CLI credentials, builds an mTLS SSE client, and opens the
// approval.completed subscription for the CLI session. CLI credentials are required —
// there is no polling fallback.
func subscribeApproval(ctx context.Context, fileSvc fs.RuntimeFileService, cfg *config.Config) (*auth.ApprovalSubscription, error) {
	creds, err := auth.LoadCredentials(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("approve: load credentials: %w", err)
	}
	if creds == nil || creds.UserID == "" {
		return nil, fmt.Errorf("approve: %w", constants.ErrNotAuthenticated)
	}

	sseClient, err := auth.BuildMTLSClient(fileSvc, cfg, 0)
	if err != nil {
		return nil, fmt.Errorf("approve: build mTLS client: %w", err)
	}

	approvals, err := auth.SubscribeApprovals(ctx, sseClient, cfg.OperatorPublicURL(), creds.CLISessionID)
	if err != nil {
		return nil, fmt.Errorf("approve: %w", err)
	}
	return approvals, nil
}

// waitForApprovalAndVerify waits on the approval subscription for the browser decision,
// then verifies the approval status via the mTLS status endpoint.
func waitForApprovalAndVerify(ctx context.Context, cmd *cobra.Command, approvals *auth.ApprovalSubscription, client APIClient, txHash string) error {
	cmd.Printf("\nWaiting for browser approval (SSE)...\n")
	if _, err := approvals.Wait(ctx, txHash, ""); err != nil {
		return fmt.Errorf("approve: %w", err)
	}

	resp, err := client.Get(auth.ApprovalStatusPath(txHash))
	if err != nil {
		return fmt.Errorf("approve: verify status: %w", err)
	}

	status, err := auth.VerifyApprovalStatus(txHash, resp)
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	cmd.Printf("\n✓ Transaction %s approved successfully\n", txHash)
	if status.ToolName != "" {
		cmd.Printf("  Tool: %s\n", status.ToolName)
	}
	return nil
}

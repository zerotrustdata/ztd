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

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
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

// approvalAPIClientFactory waits for the resumed execution's final receipt.
// Inference can take the full governed provider deadline after L3 approval.
func approvalAPIClientFactory(fileSvc fs.RuntimeFileService, cfg *config.Config) (APIClient, error) {
	return api.NewClientWithTimeout(fileSvc, cfg, inferencedispatch.RequestDeadline)
}

func approveCmd() *cobra.Command {
	return approveCmdWithConfig(shared.LoadConfig, approvalAPIClientFactory, shared.NewFileSvc, platform.OpenBrowser)
}

func approveCmdWithConfig(
	configLoader func(string) (*config.Config, error),
	clientFactory APIClientFactory,
	fileSvcFactory func(string, *slog.Logger) (fs.RuntimeFileService, error),
	openBrowser func(string) error,
) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "approve <transaction_hash>",
		Short: "Approve a suspended L3 transaction via browser WebAuthn or enrolled CLI key",
		Long: `Approve a suspended transaction using the browser passkey flow by default, or an enrolled
CLI certificate signature with --cli.

By default, the CLI opens the Gateway approval page and waits for its completion event. With
--cli, it signs the transaction hash using the enrolled CLI certificate and submits the signature
over mTLS. Both flows require enrolled CLI credentials. The --cli flow waits for the final
execution receipt and allows the governed inference request deadline for that response.

With --deny, the enrolled CLI session denies the transaction over mTLS instead, and the command
prints the Operator-signed L3_DENIED rejection receipt that ends it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			txHash := args[0]
			cliApproval, err := cmd.Flags().GetBool("cli")
			if err != nil {
				return fmt.Errorf("approve: read CLI approval flag: %w", err)
			}
			deny, err := cmd.Flags().GetBool("deny")
			if err != nil {
				return fmt.Errorf("approve: read deny flag: %w", err)
			}
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
			if deny {
				return denyWithCLI(cmd, client, txHash)
			}
			if cliApproval {
				return approveWithCLI(cmd, fileSvc, cfg, client, txHash)
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
	cmd.Flags().Bool("cli", false, "sign the transaction hash with the enrolled CLI certificate")
	cmd.Flags().Bool("deny", false, "deny the transaction from the enrolled CLI session instead of approving it")
	cmd.MarkFlagsMutuallyExclusive("cli", "deny")

	return cmd
}

// denyWithCLI denies txHash from the enrolled CLI session. The Gateway grounds
// the deny in the mTLS session and answers with the Operator-signed L3_DENIED
// rejection, which must name the denied transaction.
func denyWithCLI(cmd *cobra.Command, client APIClient, txHash string) error {
	resp, err := client.Post(constants.APIPaths.ApprovalsRespond, models.ApprovalRespondRequest{
		ApprovalID: txHash,
		Approved:   false,
	})
	if err != nil {
		return fmt.Errorf("approve: submit CLI deny: %w", err)
	}
	var receipt operatorv1.ActionReceipt
	if err := protojson.Unmarshal(resp, &receipt); err != nil {
		return fmt.Errorf("approve: parse CLI deny receipt: %w", err)
	}
	if receipt.GetTransactionHash() != txHash {
		return fmt.Errorf("approve: deny receipt transaction hash %q does not match requested hash: %w", receipt.GetTransactionHash(), constants.ErrTxTransactionHashMismatch)
	}
	if receipt.GetRejectionCode() != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED {
		return fmt.Errorf("approve: transaction %s deny receipt carries rejection %s, want L3_DENIED: %w", txHash, receipt.GetRejectionCode(), constants.ErrValidationFailed)
	}
	cmd.Printf("\n✗ Transaction %s denied (signed L3_DENIED receipt)\n", txHash)
	return nil
}

func approveWithCLI(cmd *cobra.Command, fileSvc fs.RuntimeFileService, cfg *config.Config, client APIClient, txHash string) error {
	clientAuth, err := auth.LoadClientAuthContext(fileSvc, cfg)
	if err != nil {
		return fmt.Errorf("approve: load enrolled CLI identity: %w", err)
	}
	proof, err := governance.SignCLIApproval(txHash, clientAuth.Certificate)
	if err != nil {
		return fmt.Errorf("approve: sign transaction hash: %w", err)
	}
	resp, err := client.Post(constants.APIPaths.ApprovalsRespond, models.ApprovalRespondRequest{
		ApprovalID:   txHash,
		Approved:     true,
		CLISignature: proof.GetCliSignature(),
	})
	if err != nil {
		return fmt.Errorf("approve: submit CLI approval: %w", err)
	}
	var receipt operatorv1.ActionReceipt
	if err := protojson.Unmarshal(resp, &receipt); err != nil {
		return fmt.Errorf("approve: parse CLI approval receipt: %w", err)
	}
	if receipt.GetTransactionHash() != txHash {
		return fmt.Errorf("approve: receipt transaction hash %q does not match requested hash: %w", receipt.GetTransactionHash(), constants.ErrTxTransactionHashMismatch)
	}
	if receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return fmt.Errorf("approve: transaction %s did not complete (status %s): %w", txHash, receipt.GetStatus(), constants.ErrActionExecutionFailed)
	}
	if receipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
		return fmt.Errorf("approve: transaction %s did not confirm required L3 proof (status %s): %w", txHash, receipt.GetL3Status(), constants.ErrTxL3ProofInvalid)
	}
	cmd.Printf("\n✓ Transaction %s approved successfully\n", txHash)
	cmd.Printf("  Receipt: %s (L3 %s)\n", receipt.GetStatus(), receipt.GetL3Status())
	if receipt.GetResultSummary() != "" {
		cmd.Printf("  Result: %s\n", receipt.GetResultSummary())
	}
	return nil
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
	receipt, err := approvals.Wait(ctx, txHash, "")
	if err != nil {
		return fmt.Errorf("approve: %w", err)
	}
	// A deny resolves the wait with the Operator-signed L3_DENIED rejection.
	if receipt.GetRejectionCode() == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED {
		return fmt.Errorf("approve: transaction %s: %w", txHash, constants.ErrTxL3Denied)
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

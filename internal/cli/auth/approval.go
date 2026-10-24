// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package auth

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ApprovalPageURL returns the Gateway's public browser approval page for a
// suspended L3 transaction. The page runs the WebAuthn ceremony and redirects
// into the console SPA; completion is announced on the CLI session's SSE
// stream as approval.completed.
func ApprovalPageURL(cfg *config.Config, txHash string) string {
	return cfg.OperatorPublicURL() + constants.APIPaths.ApprovePagePrefix + txHash
}

// ValidateApprovalReceipt checks the execution outcome received over an
// authenticated Gateway response or approval stream. The Gateway owns receipt
// signature verification at its execution-result boundary.
func ValidateApprovalReceipt(txHash string, receipt *operatorv1.ActionReceipt) error {
	if receipt == nil || txHash == "" {
		return fmt.Errorf("L3 approval: %w", constants.ErrActionReceiptMissing)
	}
	if receipt.GetTransactionHash() != txHash {
		return fmt.Errorf("L3 approval: receipt transaction hash does not match %s: %w", txHash, constants.ErrTxTransactionHashMismatch)
	}
	if receipt.GetRejectionCode() == operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED {
		return fmt.Errorf("L3 approval: transaction %s: %w", txHash, constants.ErrTxL3Denied)
	}
	if receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED || receipt.GetRejectionCode() != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED {
		return fmt.Errorf("L3 approval: transaction %s did not complete (status %s): %w", txHash, receipt.GetStatus(), constants.ErrActionExecutionFailed)
	}
	if receipt.GetL3Status() != operatorv1.L3Status_L3_STATUS_REQUIRED_VALID {
		return fmt.Errorf("L3 approval: transaction %s did not confirm required L3 proof (status %s): %w", txHash, receipt.GetL3Status(), constants.ErrTxL3ProofInvalid)
	}
	return nil
}

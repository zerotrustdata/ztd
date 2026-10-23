// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestClassify(t *testing.T) {
	t.Parallel()
	signer := oracletest.NewSigner(t)
	executedLink := &Link{Prior: "prior-0", Hash: "hash-1", TransactionID: "tx-1"}

	tests := []struct {
		name    string
		build   func(t *testing.T) *operatorv1.ActionReceipt
		want    Outcome
		wantErr bool
	}{
		{
			name: "executed",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
			},
			want: Outcome{
				Kind:       OutcomeExecuted,
				L2Status:   operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
				Commitment: executedLink,
			},
		},
		{
			name: "execution failed",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
				receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION).Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
				signer.Resign(t, receipt)
				return receipt
			},
			want: Outcome{
				Kind:       OutcomeExecutionFailed,
				L2Status:   operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
				Commitment: executedLink,
			},
		},
		{
			name: "rejected with code",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return signer.Rejected(t, verifyOpts("op-1"), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
			},
			want: Outcome{
				Kind:          OutcomeRejected,
				RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE,
				L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status:      operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
			},
		},
		{
			name: "rejected with unclassified code is still a rejection",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return signer.Rejected(t, verifyOpts("op-1"), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED)
			},
			want: Outcome{
				Kind:          OutcomeRejected,
				RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED,
				L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status:      operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
			},
		},
		{
			name: "rejected with an L5 execution stage",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Rejected(t, verifyOpts("op-1"), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
				receipt.DeterministicStageEvidence = append(receipt.DeterministicStageEvidence, &operatorv1.DeterministicStageEvidence{
					StageId: "tx-1:L5", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION,
					Outcome: operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
				})
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "rejected with a commitment-append stage",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Rejected(t, verifyOpts("op-1"), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
				receipt.DeterministicStageEvidence = append(receipt.DeterministicStageEvidence, &operatorv1.DeterministicStageEvidence{
					StageId: "tx-1:COMMIT", Kind: operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND,
					Outcome:        operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
					CommitmentHash: "hash-1", PriorCommitmentHash: "prior-0",
				})
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "executed with no commitment stage",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				stages := receipt.DeterministicStageEvidence[:0]
				for _, stage := range receipt.DeterministicStageEvidence {
					if stage.GetKind() != operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND {
						stages = append(stages, stage)
					}
				}
				receipt.DeterministicStageEvidence = stages
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "executed with an empty commitment hash",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND).CommitmentHash = ""
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "executed genesis commitment (empty prior hash)",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				return signer.Executed(t, verifyOpts("op-1"), "", "hash-1")
			},
			want: Outcome{
				Kind:       OutcomeExecuted,
				L2Status:   operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
				L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
				Commitment: &Link{Prior: "", Hash: "hash-1", TransactionID: "tx-1"},
			},
		},
		{
			name: "executed with an empty commitment hash",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				findStage(receipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND).CommitmentHash = ""
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "rejection code on a completed receipt",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				receipt.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "failure code on a completed receipt",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "executing status is not terminal",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Executed(t, verifyOpts("op-1"), "prior-0", "hash-1")
				receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{
			name: "failed receipt with no L5 stage and no governance failure",
			build: func(t *testing.T) *operatorv1.ActionReceipt {
				t.Helper()
				receipt := signer.Rejected(t, verifyOpts("op-1"), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
				receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
				receipt.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED
				signer.Resign(t, receipt)
				return receipt
			},
			wantErr: true,
		},
		{name: "nil receipt", build: func(t *testing.T) *operatorv1.ActionReceipt { t.Helper(); return nil }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Classify(tt.build(t))
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, constants.ErrConformanceReceiptUnclassifiable)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

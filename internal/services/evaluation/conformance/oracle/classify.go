// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// OutcomeKind names the single shape a verified receipt can take.
type OutcomeKind int

const (
	OutcomeUnknown         OutcomeKind = iota
	OutcomeExecuted                    // COMPLETED, L5_EXECUTION stage COMPLETED, COMMITMENT_APPEND present
	OutcomeExecutionFailed             // FAILED, L5_EXECUTION stage FAILED, failure_code != GOVERNANCE_REJECTED
	OutcomeRejected                    // FAILED, failure_code GOVERNANCE_REJECTED, no L5_EXECUTION stage
)

// Outcome is what a verified receipt says happened.
type Outcome struct {
	Kind          OutcomeKind
	RejectionCode operatorv1.GovernanceRejectionCode
	L2Status      operatorv1.L2Status
	L3Status      operatorv1.L3Status
	Commitment    *Link // nil for a rejection
}

// Link is one commitment-ledger step taken from a COMMITMENT_APPEND stage.
type Link struct{ Prior, Hash, TransactionID string }

// Classify reads what a verified receipt says happened. Call it only on a
// receipt VerifyReceipt accepted.
func Classify(receipt *operatorv1.ActionReceipt) (Outcome, error) {
	if receipt == nil {
		return Outcome{}, unclassifiable("receipt is nil")
	}
	stages, err := stagesByKind(receipt)
	if err != nil {
		return Outcome{}, err
	}
	l5 := stages[operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION]
	commit := stages[operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND]
	failure := receipt.GetFailureCode()
	rejection := receipt.GetRejectionCode()
	base := Outcome{L2Status: receipt.GetL2Status(), L3Status: receipt.GetL3Status()}

	switch receipt.GetStatus() {
	case operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED:
		if failure != operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED {
			return Outcome{}, unclassifiable("completed receipt carries a failure code")
		}
		if rejection != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED {
			return Outcome{}, unclassifiable("rejection code on a non-rejection")
		}
		if l5 == nil || l5.GetOutcome() != operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED {
			return Outcome{}, unclassifiable("completed receipt has no completed L5 execution stage")
		}
		link, err := commitmentLink(receipt, commit)
		if err != nil {
			return Outcome{}, err
		}
		base.Kind = OutcomeExecuted
		base.Commitment = link
		return base, nil

	case operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED:
		if failure == operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED {
			if l5 != nil || commit != nil {
				return Outcome{}, unclassifiable("governance rejection carries an L5 execution or commitment stage")
			}
			base.Kind = OutcomeRejected
			base.RejectionCode = rejection
			return base, nil
		}
		if rejection != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED {
			return Outcome{}, unclassifiable("rejection code on a non-rejection")
		}
		if l5 == nil || l5.GetOutcome() != operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED {
			return Outcome{}, unclassifiable("failed execution has no failed L5 execution stage")
		}
		link, err := commitmentLink(receipt, commit)
		if err != nil {
			return Outcome{}, err
		}
		base.Kind = OutcomeExecutionFailed
		base.Commitment = link
		return base, nil

	default:
		return Outcome{}, unclassifiable(fmt.Sprintf("status %s is not terminal", receipt.GetStatus().String()))
	}
}

// stagesByKind indexes a receipt's stages by kind and rejects a duplicate kind.
func stagesByKind(receipt *operatorv1.ActionReceipt) (map[operatorv1.DeterministicStageKind]*operatorv1.DeterministicStageEvidence, error) {
	stages := make(map[operatorv1.DeterministicStageKind]*operatorv1.DeterministicStageEvidence, len(receipt.GetDeterministicStageEvidence()))
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if _, exists := stages[stage.GetKind()]; exists {
			return nil, unclassifiable(fmt.Sprintf("duplicate %s stage", stage.GetKind().String()))
		}
		stages[stage.GetKind()] = stage
	}
	return stages, nil
}

// commitmentLink returns the ledger step an executed receipt took. Every
// executed receipt appends exactly one commitment, so a missing commitment
// stage or hash is unclassifiable. An empty prior hash is the runtime's first
// commitment (the ledger's genesis rule); VerifyBracket rejects it anywhere a
// prior link is expected.
func commitmentLink(receipt *operatorv1.ActionReceipt, commit *operatorv1.DeterministicStageEvidence) (*Link, error) {
	if commit == nil {
		return nil, unclassifiable("executed receipt has no commitment-append stage")
	}
	if commit.GetOutcome() != operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED {
		return nil, unclassifiable("commitment-append stage is not completed")
	}
	if commit.GetCommitmentHash() == "" {
		return nil, unclassifiable("commitment-append stage names no commitment hash")
	}
	return &Link{
		Prior:         commit.GetPriorCommitmentHash(),
		Hash:          commit.GetCommitmentHash(),
		TransactionID: receipt.GetTransactionId(),
	}, nil
}

func unclassifiable(reason string) error {
	return fmt.Errorf("oracle: classify receipt: %s: %w", reason, constants.ErrConformanceReceiptUnclassifiable)
}

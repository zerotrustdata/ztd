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

// OutcomeKind names the single shape a verified event receipt can take.
type OutcomeKind int

const (
	OutcomeUnknown         OutcomeKind = iota
	OutcomeExecuted                    // COMPLETED, L5_EXECUTION stage COMPLETED, COMMITMENT_APPEND present
	OutcomeExecutionFailed             // FAILED, L5_EXECUTION stage FAILED, failure_code != GOVERNANCE_REJECTED
	OutcomeRejected                    // FAILED, failure_code GOVERNANCE_REJECTED, no L5_EXECUTION stage
)

// Outcome is what a verified event receipt says happened.
type Outcome struct {
	Kind          OutcomeKind
	RejectionCode operatorv1.GovernanceRejectionCode
	L2Status      operatorv1.L2Status
	L3Status      operatorv1.L3Status
	Commitment    *Link // nil for a rejection
}

// Link is one commitment-ledger step taken from a COMMITMENT_APPEND stage.
type Link struct{ Prior, Hash, TransactionID string }

// Classify reads what a verified event receipt says happened. Call it only on a
// event receipt VerifyEventReceipt accepted.
func Classify(eventReceipt *operatorv1.EventReceipt) (Outcome, error) {
	if eventReceipt == nil {
		return Outcome{}, unclassifiable("event receipt is nil")
	}
	stages, err := stagesByKind(eventReceipt)
	if err != nil {
		return Outcome{}, err
	}
	l5 := stages[operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION]
	commit := stages[operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND]
	failure := eventReceipt.GetFailureCode()
	rejection := eventReceipt.GetRejectionCode()
	base := Outcome{L2Status: eventReceipt.GetL2Status(), L3Status: eventReceipt.GetL3Status()}

	switch eventReceipt.GetStatus() {
	case operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED:
		if failure != operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_UNSPECIFIED {
			return Outcome{}, unclassifiable("completed event receipt carries a failure code")
		}
		if rejection != operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED {
			return Outcome{}, unclassifiable("rejection code on a non-rejection")
		}
		if l5 == nil || l5.GetOutcome() != operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED {
			return Outcome{}, unclassifiable("completed event receipt has no completed L5 execution stage")
		}
		link, err := commitmentLink(eventReceipt, commit)
		if err != nil {
			return Outcome{}, err
		}
		base.Kind = OutcomeExecuted
		base.Commitment = link
		return base, nil

	case operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED:
		if failure == operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED {
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
		link, err := commitmentLink(eventReceipt, commit)
		if err != nil {
			return Outcome{}, err
		}
		base.Kind = OutcomeExecutionFailed
		base.Commitment = link
		return base, nil

	default:
		return Outcome{}, unclassifiable(fmt.Sprintf("status %s is not terminal", eventReceipt.GetStatus().String()))
	}
}

// stagesByKind indexes an event receipt's stages by kind and rejects a duplicate kind.
func stagesByKind(eventReceipt *operatorv1.EventReceipt) (map[operatorv1.DeterministicStageKind]*operatorv1.DeterministicStageEvidence, error) {
	stages := make(map[operatorv1.DeterministicStageKind]*operatorv1.DeterministicStageEvidence, len(eventReceipt.GetDeterministicStageEvidence()))
	for _, stage := range eventReceipt.GetDeterministicStageEvidence() {
		if _, exists := stages[stage.GetKind()]; exists {
			return nil, unclassifiable(fmt.Sprintf("duplicate %s stage", stage.GetKind().String()))
		}
		stages[stage.GetKind()] = stage
	}
	return stages, nil
}

// commitmentLink returns the ledger step an executed event receipt took. Every
// executed event receipt appends exactly one commitment, so a missing commitment
// stage or hash is unclassifiable. An empty prior hash is the runtime's first
// commitment (the ledger's genesis rule); VerifyBracket rejects it anywhere a
// prior link is expected.
func commitmentLink(eventReceipt *operatorv1.EventReceipt, commit *operatorv1.DeterministicStageEvidence) (*Link, error) {
	if commit == nil {
		return nil, unclassifiable("executed event receipt has no commitment-append stage")
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
		TransactionID: eventReceipt.GetTransactionId(),
	}, nil
}

func unclassifiable(reason string) error {
	return fmt.Errorf("oracle: classify event_receipt: %s: %w", reason, constants.ErrConformanceEventReceiptUnclassifiable)
}

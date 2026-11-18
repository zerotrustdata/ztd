// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracle

import (
	"fmt"
	"slices"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ValidateConsensusInference checks one member's vote record: the
// subject-bound attempt, member identity, signed result digest, and, for a
// vote that needed retries, the chain of superseded attempts behind it. The
// record's retry_count equals the number of superseded attempts, within
// inferencedispatch.MaxTransientRetries; attempt i has retry_count i, the
// attempt ID for its ordinal, the record's member and subject, no result, no
// attempts of its own, and a distinct signed FAILED receipt whose provider
// asked to retry later. Callers must also verify every attempt's receipts
// against pinned keys (governance.ConsensusVoteAttempts walks them).
func ValidateConsensusInference(call *operatorv1.ConsensusVoteInference) error {
	if call == nil || call.GetSubjectTransactionId() == "" || call.GetSubjectTransactionHash() == "" || !slices.Contains(constants.TribunalConsensusMemberAppIDs(), call.GetMemberAppId()) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	superseded := call.GetSupersededAttempts()
	if call.GetRetryCount() != uint32(len(superseded)) || call.GetRetryCount() > inferencedispatch.MaxTransientRetries() {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	if err := validateConsensusAttempt(call); err != nil {
		return err
	}
	transactions := make(map[string]bool, len(superseded)+1)
	if call.GetTransactionId() != "" {
		transactions[call.GetTransactionId()] = true
	}
	for i, attempt := range superseded {
		receipt := attempt.GetReceipt()
		if attempt.GetSubjectTransactionId() != call.GetSubjectTransactionId() || attempt.GetSubjectTransactionHash() != call.GetSubjectTransactionHash() ||
			attempt.GetMemberAppId() != call.GetMemberAppId() || attempt.GetRetryCount() != uint32(i) || len(attempt.GetSupersededAttempts()) != 0 ||
			attempt.GetFailure() == nil || attempt.GetResult() != nil || !governance.IsTransientFailureReceipt(receipt) {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
		// A failed attempt has no result binding its receipt to its attempt
		// ID, so one signed receipt must not stand in for two attempts.
		if transactions[receipt.GetTransactionId()] {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
		transactions[receipt.GetTransactionId()] = true
		if err := validateConsensusAttempt(attempt); err != nil {
			return err
		}
	}
	return nil
}

// validateConsensusAttempt checks one provider attempt of a vote: the attempt
// ID for its retry ordinal and, with a receipt, that the receipt is the
// attempt's own INFERENCE execution, signed as the outcome the attempt
// records.
func validateConsensusAttempt(call *operatorv1.ConsensusVoteInference) error {
	if call.GetProviderAttemptId() != governance.ConsensusVoteAttemptID(call.GetSubjectTransactionHash(), call.GetMemberAppId(), call.GetRetryCount()) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	receipt := call.GetReceipt()
	if receipt == nil {
		if call.GetFailure() == nil || call.GetResult() != nil {
			return constants.ErrEventReceiptMissing
		}
		return nil
	}
	if receipt.GetActionType() != string(constants.ActionTypeInference) || receipt.GetTransactionId() == "" || receipt.GetTransactionId() != call.GetTransactionId() {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetActingAppId() != call.GetMemberAppId() {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
	}
	if result := call.GetResult(); result != nil {
		digest, err := models.ComputeInferenceResultDigest(result)
		if err != nil {
			return fmt.Errorf("consensus result digest: %w", err)
		}
		if result.GetProviderAttemptId() != call.GetProviderAttemptId() || result.GetRetryCount() != call.GetRetryCount() || result.GetResultDigest() != digest || receipt.GetResultSummary() != digest || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED || call.GetFailure() != nil {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
	} else if call.GetFailure() == nil || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	return nil
}

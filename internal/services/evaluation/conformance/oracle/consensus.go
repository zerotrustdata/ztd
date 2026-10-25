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
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ValidateConsensusInference checks the subject-bound attempt, member identity,
// and signed result digest. Callers must also verify receipts against pinned keys.
func ValidateConsensusInference(call *operatorv1.ConsensusVoteInference) error {
	if call == nil || call.GetSubjectTransactionId() == "" || call.GetSubjectTransactionHash() == "" || !slices.Contains(constants.TribunalConsensusMemberAppIDs(), call.GetMemberAppId()) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	if call.GetProviderAttemptId() != governance.ConsensusVoteAttemptID(call.GetSubjectTransactionHash(), call.GetMemberAppId()) {
		return constants.ErrEvaluationEvidenceTraceMismatch
	}
	receipt := call.GetReceipt()
	if receipt == nil {
		if call.GetFailure() == nil || call.GetResult() != nil {
			return constants.ErrEventReceiptMissing
		}
	} else {
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
			if result.GetProviderAttemptId() != call.GetProviderAttemptId() || result.GetResultDigest() != digest || receipt.GetResultSummary() != digest || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED || call.GetFailure() != nil {
				return constants.ErrEvaluationEvidenceTraceMismatch
			}
		} else if call.GetFailure() == nil || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
			return constants.ErrEvaluationEvidenceTraceMismatch
		}
	}
	return nil
}

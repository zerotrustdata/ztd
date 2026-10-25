// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"slices"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// verifyConsensusInferenceEvidence claims the L2 vote executions and their
// observations on the bracketed Data runtime. Concurrent members are ordered
// by their signed ledger links, rather than by the panel's member order.
func verifyConsensusInferenceEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, tool AgentToolCall) ([]oracle.Outcome, error) {
	var claims []oracle.Outcome
	seen := make(map[string]bool)
	for _, evidence := range tool.ConsensusInferences {
		call := evidence.ConsensusVoteInference
		if call == nil || call.GetSubjectTransactionId() == "" || call.GetSubjectTransactionHash() == "" || !slices.Contains(constants.TribunalConsensusMemberAppIDs(), call.GetMemberAppId()) || seen[call.GetMemberAppId()] {
			return nil, constants.ErrEvaluationEvidenceTraceMismatch
		}
		seen[call.GetMemberAppId()] = true
		if tool.Receipt != nil && (call.GetSubjectTransactionId() != tool.Receipt.GetTransactionId() || call.GetSubjectTransactionHash() != tool.Receipt.GetTransactionHash()) {
			return nil, constants.ErrEvaluationEvidenceTraceMismatch
		}
		if call.GetProviderAttemptId() != governance.ConsensusVoteAttemptID(call.GetSubjectTransactionHash(), call.GetMemberAppId()) {
			return nil, constants.ErrEvaluationEvidenceTraceMismatch
		}
		receipt := call.GetReceipt()
		if receipt == nil {
			if call.GetFailure() == nil || call.GetResult() != nil {
				return nil, constants.ErrEventReceiptMissing
			}
		} else {
			if receipt.GetActionType() != string(constants.ActionTypeInference) || receipt.GetTransactionId() == "" || receipt.GetTransactionId() != call.GetTransactionId() {
				return nil, constants.ErrEvaluationEvidenceTraceMismatch
			}
			for _, stage := range receipt.GetDeterministicStageEvidence() {
				if stage.GetActingAppId() != call.GetMemberAppId() {
					return nil, constants.ErrEvaluationEvidenceTraceMismatch
				}
			}
			if result := call.GetResult(); result != nil {
				digest, err := models.ComputeInferenceResultDigest(result)
				if err != nil {
					return nil, fmt.Errorf("consensus result digest: %w", err)
				}
				if result.GetProviderAttemptId() != call.GetProviderAttemptId() || result.GetResultDigest() != digest || receipt.GetResultSummary() != digest || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED || call.GetFailure() != nil {
					return nil, constants.ErrEvaluationEvidenceTraceMismatch
				}
			} else if call.GetFailure() == nil || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
				return nil, constants.ErrEvaluationEvidenceTraceMismatch
			}
			// The trace pins its Inference and Data execution boundaries. A decision
			// provider may be a third runtime; its receipts are retained, but its
			// separate ledger is outside this bracket, just as remote observations are.
			var runtime *AgentTraceRuntime
			switch receipt.GetSignerKeyId() {
			case trace.Data.Anchor.GetKeyId():
				runtime = &trace.Data
			case trace.Inference.Anchor.GetKeyId():
				runtime = &trace.Inference
			}
			if runtime != nil {
				outcome, err := verifiedOutcome(anchors, runtime.OperatorID, receipt)
				if err != nil {
					return nil, err
				}
				if err := verifyReceiptSession(receipt, runtime.SessionID); err != nil {
					return nil, err
				}
				if runtime == &trace.Data && outcome.Commitment != nil {
					claims = append(claims, outcome)
				}
			}
		}
		observed := AgentModelCall{}
		for _, receipt := range call.GetObservationReceipts() {
			observed.ObservationReceipts = append(observed.ObservationReceipts, AgentTraceReceipt{EventReceipt: receipt})
		}
		observations, err := verifyObservationEvidence(anchors, trace.Data, observed)
		if err != nil {
			return nil, err
		}
		claims = append(claims, observations...)
	}
	return claims, nil
}

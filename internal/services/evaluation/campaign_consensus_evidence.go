// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
)

// verifyConsensusInferenceEvidence claims the L2 vote executions and their
// observations on the bracketed Data runtime. Concurrent members are ordered
// by their signed ledger links, rather than by the panel's member order.
func verifyConsensusInferenceEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, tool AgentToolCall) ([]oracle.Outcome, error) {
	var claims []oracle.Outcome
	seen := make(map[string]bool)
	finalSubjectFound := tool.Receipt == nil || len(tool.ConsensusInferences) == 0
	for _, evidence := range tool.ConsensusInferences {
		call := evidence.ConsensusVoteInference
		if err := oracle.ValidateConsensusInference(call); err != nil {
			return nil, err
		}
		identity := call.GetSubjectTransactionHash() + "/" + call.GetMemberAppId()
		if seen[identity] {
			return nil, constants.ErrEvaluationEvidenceTraceMismatch
		}
		seen[identity] = true
		if tool.Receipt != nil && call.GetSubjectTransactionId() == tool.Receipt.GetTransactionId() && call.GetSubjectTransactionHash() == tool.Receipt.GetTransactionHash() {
			finalSubjectFound = true
		}
		receipt := call.GetReceipt()
		if receipt != nil {
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
	if !finalSubjectFound {
		return nil, constants.ErrEvaluationEvidenceTraceMismatch
	}
	return claims, nil
}

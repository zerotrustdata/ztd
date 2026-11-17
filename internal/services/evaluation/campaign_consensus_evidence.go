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
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// verifyConsensusInferenceEvidence claims the L2 vote executions and their
// observations on the bracketed Data runtime. Concurrent members are ordered
// by their signed ledger links, rather than by the panel's member order. A
// member's vote record is one record however many provider attempts the
// vote needed: every attempt, the superseded ones a provider's transient
// failure retried as well as the final one, is an execution on its Operator's
// ledger, so each is verified and claimed.
func verifyConsensusInferenceEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, tool AgentToolCall) ([]oracle.Outcome, error) {
	var claims []oracle.Outcome
	seen := make(map[string]bool)
	finalSubjectFound := tool.Receipt == nil || len(tool.ConsensusInferences) == 0
	for _, evidence := range tool.ConsensusInferences {
		vote := evidence.ConsensusVoteInference
		if err := oracle.ValidateConsensusInference(vote); err != nil {
			return nil, err
		}
		identity := vote.GetSubjectTransactionHash() + "/" + vote.GetMemberAppId()
		if seen[identity] {
			return nil, constants.ErrEvaluationEvidenceTraceMismatch
		}
		seen[identity] = true
		if tool.Receipt != nil && vote.GetSubjectTransactionId() == tool.Receipt.GetTransactionId() && vote.GetSubjectTransactionHash() == tool.Receipt.GetTransactionHash() {
			finalSubjectFound = true
		}
		for _, call := range governance.ConsensusVoteAttempts(vote) {
			attempt, err := verifyConsensusAttemptEvidence(anchors, trace, call)
			if err != nil {
				return nil, err
			}
			claims = append(claims, attempt...)
		}
	}
	if !finalSubjectFound {
		return nil, constants.ErrEvaluationEvidenceTraceMismatch
	}
	return claims, nil
}

// verifyConsensusAttemptEvidence verifies one vote attempt's receipt and
// observation receipts against the pinned keys and returns the ledger
// commitments it claims on the Data runtime.
func verifyConsensusAttemptEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, call *operatorv1.ConsensusVoteInference) ([]oracle.Outcome, error) {
	var claims []oracle.Outcome
	if receipt := call.GetReceipt(); receipt != nil {
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
	return append(claims, observations...), nil
}

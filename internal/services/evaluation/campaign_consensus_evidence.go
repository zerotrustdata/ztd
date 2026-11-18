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
func verifyConsensusInferenceEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, tool AgentToolCall) ([]rosterClaim, error) {
	var claims []rosterClaim
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
// commitments it claims on roster members.
func verifyConsensusAttemptEvidence(anchors *oracle.AnchorSet, trace *AgentTrace, call *operatorv1.ConsensusVoteInference) ([]rosterClaim, error) {
	var claims []rosterClaim
	if receipt := call.GetReceipt(); receipt != nil {
		// The trace pins its Inference runtime and every roster member. A
		// decision provider is a separate runtime by design (consensus provider
		// isolation); its receipts are retained, but its ledger is outside
		// every bracket, just as remote observations are.
		if member, ok := rosterSigner(trace.Roster, receipt.GetSignerKeyId()); ok {
			outcome, err := verifiedOutcome(anchors, member.Runtime.OperatorID, receipt)
			if err != nil {
				return nil, err
			}
			if err := verifyReceiptSession(receipt, member.Runtime.SessionID); err != nil {
				return nil, err
			}
			if outcome.Commitment != nil {
				claims = append(claims, rosterClaim{index: member.Index, outcome: outcome})
			}
		} else if receipt.GetSignerKeyId() == trace.Inference.Anchor.GetKeyId() {
			if _, err := verifiedOutcome(anchors, trace.Inference.OperatorID, receipt); err != nil {
				return nil, err
			}
			if err := verifyReceiptSession(receipt, trace.Inference.SessionID); err != nil {
				return nil, err
			}
		}
	}
	observed := AgentModelCall{}
	for _, receipt := range call.GetObservationReceipts() {
		observed.ObservationReceipts = append(observed.ObservationReceipts, AgentTraceReceipt{EventReceipt: receipt})
	}
	observations, err := verifyObservationEvidence(anchors, trace.Roster, observed)
	if err != nil {
		return nil, err
	}
	return append(claims, observations...), nil
}

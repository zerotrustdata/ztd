// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// agentReceiptSchemaRef names the message a receipt evidence reference points at.
const agentReceiptSchemaRef = "ztd.operator.v1.ActionReceipt"

// VerifyAgentTraceEvidence proves, from the sealed trace alone, that every
// governed tool call is backed by a receipt the Data Operator signed, and that
// the Data Operator's ledger between the two sentinels holds exactly the
// executions the tool calls claim.
func VerifyAgentTraceEvidence(trace *AgentTrace) error {
	if trace == nil {
		return agentEvidenceError("trace", constants.ErrMissingRequiredField)
	}
	if trace.DataOperatorAnchor == nil || trace.DataOperatorAnchor.ConformanceAnchor == nil {
		return agentEvidenceError("data operator anchor", constants.ErrConformanceAnchorMissing)
	}
	anchor := trace.DataOperatorAnchor.ConformanceAnchor
	if anchor.GetRuntimeId() != trace.DataOperatorID {
		return agentEvidenceError("data operator anchor", fmt.Errorf("anchor runtime %q is not the trace data operator %q: %w", anchor.GetRuntimeId(), trace.DataOperatorID, constants.ErrConformanceAnchorUnknown))
	}
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{anchor})
	if err != nil {
		return agentEvidenceError("data operator anchor", err)
	}
	if trace.Bracket == nil {
		return agentEvidenceError("ledger bracket", constants.ErrMissingRequiredField)
	}
	before, err := verifiedSentinelOutcome(anchors, trace.DataOperatorID, trace.Bracket.Before.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket before", err)
	}
	after, err := verifiedSentinelOutcome(anchors, trace.DataOperatorID, trace.Bracket.After.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket after", err)
	}
	claimed := make([]oracle.Outcome, 0, len(trace.ToolCalls))
	for _, call := range trace.ToolCalls {
		outcome, err := verifyToolCallEvidence(anchors, trace.DataOperatorID, call)
		if err != nil {
			return agentEvidenceError("tool call "+call.CallID, err)
		}
		if outcome != nil && outcome.Commitment != nil {
			claimed = append(claimed, *outcome)
		}
	}
	if err := oracle.VerifyBracket(before, after, claimed); err != nil {
		return agentEvidenceError("ledger bracket", err)
	}
	return nil
}

// verifyToolCallEvidence checks one tool call against its receipt. A call the
// Gateway admitted must carry a verified receipt for its own transaction. A
// refusal carries none. It returns the verified outcome, or nil when the call
// has no receipt.
func verifyToolCallEvidence(anchors *oracle.AnchorSet, runtimeID string, call AgentToolCall) (*oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.ActionReceipt == nil {
		if call.TransactionID != "" || call.PolicyDecision == agentPolicyDecisionAllow {
			return nil, fmt.Errorf("admitted call has no signed receipt: %w", constants.ErrActionReceiptMissing)
		}
		return nil, nil
	}
	receipt := call.Receipt.ActionReceipt
	outcome, err := verifiedOutcome(anchors, runtimeID, receipt)
	if err != nil {
		return nil, err
	}
	if receipt.GetTransactionId() != call.TransactionID {
		return nil, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	switch outcome.Kind {
	case oracle.OutcomeExecuted, oracle.OutcomeExecutionFailed:
		if call.PolicyDecision != agentPolicyDecisionAllow {
			return nil, fmt.Errorf("executed receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case oracle.OutcomeRejected:
		if call.PolicyDecision != agentPolicyDecisionDeny {
			return nil, fmt.Errorf("rejected receipt on a call recorded as %q: %w", call.PolicyDecision, constants.ErrEvaluationEvidenceTraceMismatch)
		}
	default:
		return nil, fmt.Errorf("receipt outcome %d is not a tool call outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return &outcome, nil
}

// verifiedSentinelOutcome verifies one harness sentinel receipt. A sentinel
// must be an executed read: anything else leaves the bracket unconnected.
func verifiedSentinelOutcome(anchors *oracle.AnchorSet, runtimeID string, receipt *operatorv1.ActionReceipt) (oracle.Outcome, error) {
	if receipt == nil {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is missing: %w", constants.ErrActionReceiptMissing)
	}
	outcome, err := verifiedOutcome(anchors, runtimeID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if outcome.Kind != oracle.OutcomeExecuted {
		return oracle.Outcome{}, fmt.Errorf("sentinel receipt is not an executed read: %w", constants.ErrConformanceLedgerGap)
	}
	return outcome, nil
}

// verifiedOutcome verifies a receipt against the runtime's pinned key and
// classifies what it says happened.
func verifiedOutcome(anchors *oracle.AnchorSet, runtimeID string, receipt *operatorv1.ActionReceipt) (oracle.Outcome, error) {
	if _, err := oracle.VerifyReceipt(anchors, runtimeID, receipt); err != nil {
		return oracle.Outcome{}, err
	}
	return oracle.Classify(receipt)
}

// agentReceiptEvidenceReference names one verified receipt. The receipt lives
// inside the sealed trace, so the reference carries the trace artifact's ID and
// the digest of the receipt's canonical protojson.
func agentReceiptEvidenceReference(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, call AgentToolCall) (*compliancev1.ComplianceEvidenceReference, error) {
	if traceEvidence == nil || call.Receipt == nil || call.Receipt.ActionReceipt == nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", constants.ErrMissingRequiredField)
	}
	encoded, err := evalv1.MarshalCanonical(call.Receipt.ActionReceipt)
	if err != nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return &compliancev1.ComplianceEvidenceReference{
		ArtifactId:    traceEvidence.GetArtifactId(),
		ArtifactType:  string(complianceevidence.ArtifactTypeEvalReceipt),
		SchemaRef:     agentReceiptSchemaRef,
		MediaType:     constants.MediaTypeJSON,
		Sha256:        hex.EncodeToString(digest[:]),
		TransactionId: call.TransactionID,
		RunId:         trace.RunID,
		AttemptId:     trace.EvaluationAttemptID,
		ScenarioId:    trace.ScenarioID,
	}, nil
}

func agentEvidenceError(subject string, cause error) error {
	return fmt.Errorf("%w: %s: %w", constants.ErrCampaignToolEvidenceInvalid, subject, cause)
}

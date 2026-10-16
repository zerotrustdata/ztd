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
	"slices"

	"google.golang.org/protobuf/proto"

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
// governed inference is backed by a receipt the Inference runtime signed,
// every governed tool call by a receipt the Data runtime signed, and that the
// Data runtime's ledger between the two sentinels holds exactly the executions
// the trace claims. When one Operator holds both capabilities its ledger also
// records every inference, so the bracket claims the model calls as well as
// the tool calls, in ledger order.
func VerifyAgentTraceEvidence(trace *AgentTrace) error {
	if trace == nil {
		return agentEvidenceError("trace", constants.ErrMissingRequiredField)
	}
	anchors, err := agentTraceAnchorSet(trace)
	if err != nil {
		return err
	}
	if trace.Bracket == nil {
		return agentEvidenceError("ledger bracket", constants.ErrMissingRequiredField)
	}
	before, err := verifiedSentinelOutcome(anchors, trace.Data.OperatorID, trace.Bracket.Before.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket before", err)
	}
	after, err := verifiedSentinelOutcome(anchors, trace.Data.OperatorID, trace.Bracket.After.ActionReceipt)
	if err != nil {
		return agentEvidenceError("ledger bracket after", err)
	}
	var modelClaims, toolClaims []oracle.Outcome
	for _, call := range trace.ModelCalls {
		outcome, err := verifyModelCallEvidence(anchors, trace.Inference.OperatorID, call)
		if err != nil {
			return agentEvidenceError("model call "+call.ProviderAttemptID, err)
		}
		if trace.SharedRuntime() && outcome.Commitment != nil {
			modelClaims = append(modelClaims, outcome)
		}
	}
	for _, call := range trace.ToolCalls {
		outcome, err := verifyToolCallEvidence(anchors, trace.Data.OperatorID, call)
		if err != nil {
			return agentEvidenceError("tool call "+call.CallID, err)
		}
		if outcome != nil && outcome.Commitment != nil {
			toolClaims = append(toolClaims, *outcome)
		}
	}
	ordered, err := oracle.OrderLedger(before, after, append(slices.Clone(modelClaims), toolClaims...))
	if err == nil {
		err = oracle.VerifyBracket(before, after, ordered)
	}
	if err != nil {
		return agentEvidenceError("ledger bracket", err)
	}
	// Grading reads each role's calls in trace order, so that order must be
	// the order the ledger committed them in.
	for _, claims := range [][]oracle.Outcome{modelClaims, toolClaims} {
		if !inLedgerOrder(ordered, claims) {
			return agentEvidenceError("ledger bracket", fmt.Errorf("trace records claimed executions out of ledger order: %w", constants.ErrConformanceLedgerGap))
		}
	}
	return nil
}

// inLedgerOrder reports whether claims appear in ordered in the same relative
// order.
func inLedgerOrder(ordered, claims []oracle.Outcome) bool {
	next := 0
	for _, outcome := range ordered {
		if next < len(claims) && outcome.Commitment.Hash == claims[next].Commitment.Hash {
			next++
		}
	}
	return next == len(claims)
}

// agentTraceAnchorSet pins each traced runtime's key. Both roles must carry an
// anchor bound to their own runtime; a shared runtime must pin one key for
// both, so a receipt verifies the same whichever role claims it.
func agentTraceAnchorSet(trace *AgentTrace) (*oracle.AnchorSet, error) {
	roles := []struct {
		name    string
		runtime AgentTraceRuntime
	}{{"inference runtime anchor", trace.Inference}, {"data runtime anchor", trace.Data}}
	for _, role := range roles {
		if role.runtime.OperatorID == "" || role.runtime.Anchor == nil || role.runtime.Anchor.ConformanceAnchor == nil {
			return nil, agentEvidenceError(role.name, constants.ErrConformanceAnchorMissing)
		}
		if runtimeID := role.runtime.Anchor.GetRuntimeId(); runtimeID != role.runtime.OperatorID {
			return nil, agentEvidenceError(role.name, fmt.Errorf("anchor runtime %q is not the traced runtime %q: %w", runtimeID, role.runtime.OperatorID, constants.ErrConformanceAnchorUnknown))
		}
	}
	pinned := []*evalv1.ConformanceAnchor{trace.Data.Anchor.ConformanceAnchor}
	if trace.SharedRuntime() {
		if !proto.Equal(trace.Inference.Anchor.ConformanceAnchor, trace.Data.Anchor.ConformanceAnchor) {
			return nil, agentEvidenceError("inference runtime anchor", fmt.Errorf("shared runtime %q pins two keys: %w", trace.Data.OperatorID, constants.ErrConformanceAnchorInvalid))
		}
	} else {
		pinned = append(pinned, trace.Inference.Anchor.ConformanceAnchor)
	}
	anchors, err := oracle.NewAnchorSet(pinned)
	if err != nil {
		return nil, agentEvidenceError("runtime anchors", err)
	}
	return anchors, nil
}

// verifyModelCallEvidence checks one governed inference against its receipt.
// Every recorded call must carry a receipt the Inference runtime signed for
// its own transaction. A completed call's receipt must be an execution whose
// result_summary is the call's result digest; a failed call's receipt may be
// a failed execution, which still holds a ledger commitment, or a governance
// rejection, which holds none.
func verifyModelCallEvidence(anchors *oracle.AnchorSet, runtimeID string, call AgentModelCall) (oracle.Outcome, error) {
	if call.Receipt == nil || call.Receipt.ActionReceipt == nil {
		return oracle.Outcome{}, fmt.Errorf("governed inference has no signed receipt: %w", constants.ErrActionReceiptMissing)
	}
	receipt := call.Receipt.ActionReceipt
	outcome, err := verifiedOutcome(anchors, runtimeID, receipt)
	if err != nil {
		return oracle.Outcome{}, err
	}
	if call.TransactionID == "" || receipt.GetTransactionId() != call.TransactionID {
		return oracle.Outcome{}, fmt.Errorf("receipt transaction %q is not the call transaction %q: %w", receipt.GetTransactionId(), call.TransactionID, constants.ErrEvaluationEvidenceTraceMismatch)
	}
	switch {
	case call.Completed():
		if outcome.Kind != oracle.OutcomeExecuted {
			return oracle.Outcome{}, fmt.Errorf("completed inference has a receipt that is not an execution: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
		if receipt.GetResultSummary() != call.ResultDigest {
			return oracle.Outcome{}, fmt.Errorf("receipt result summary is not the call result digest: %w", constants.ErrEvaluationEvidenceTraceMismatch)
		}
	case outcome.Kind == oracle.OutcomeExecuted, outcome.Kind == oracle.OutcomeExecutionFailed, outcome.Kind == oracle.OutcomeRejected:
	default:
		return oracle.Outcome{}, fmt.Errorf("receipt outcome %d is not an inference outcome: %w", outcome.Kind, constants.ErrConformanceReceiptUnclassifiable)
	}
	return outcome, nil
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
func agentReceiptEvidenceReference(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, transactionID string, receipt *AgentTraceReceipt) (*compliancev1.ComplianceEvidenceReference, error) {
	if traceEvidence == nil || receipt == nil || receipt.ActionReceipt == nil {
		return nil, fmt.Errorf("evaluation: agent receipt reference: %w", constants.ErrMissingRequiredField)
	}
	encoded, err := evalv1.MarshalCanonical(receipt.ActionReceipt)
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
		TransactionId: transactionID,
		RunId:         trace.RunID,
		AttemptId:     trace.EvaluationAttemptID,
		ScenarioId:    trace.ScenarioID,
	}, nil
}

// agentToolGovernedAction binds one tool call's signed receipt as a governed
// action on the Data Operator. A refused call has no receipt and binds nothing.
func agentToolGovernedAction(traceEvidence *compliancev1.ComplianceEvidenceReference, trace *AgentTrace, assignmentID string, call AgentToolCall) (*evalv1.GovernedActionBinding, error) {
	if call.Receipt == nil {
		return nil, nil
	}
	ref, err := agentReceiptEvidenceReference(traceEvidence, trace, call.TransactionID, call.Receipt)
	if err != nil {
		return nil, err
	}
	return &evalv1.GovernedActionBinding{
		BindingId:         call.CallID,
		AssignmentId:      assignmentID,
		TransactionId:     call.TransactionID,
		OperatorId:        trace.Data.OperatorID,
		OperatorSessionId: trace.Data.SessionID,
		ReceiptRef:        ref,
		PolicyDecision:    call.PolicyDecision,
		ActionType:        call.Receipt.GetActionType(),
	}, nil
}

func agentEvidenceError(subject string, cause error) error {
	return fmt.Errorf("%w: %s: %w", constants.ErrCampaignToolEvidenceInvalid, subject, cause)
}

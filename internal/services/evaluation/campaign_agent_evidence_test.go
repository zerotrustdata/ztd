// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	evidenceOperatorID = "operator-data-1"
	evidenceSessionID  = "session-data-1"
)

// evidenceLedger is the Data Operator's commitment ledger as a test double. An
// executed receipt links to the head before it, so a bracket built from it
// connects the way the real ledger does. A rejection appends nothing.
type evidenceLedger struct {
	t          testing.TB
	signer     oracletest.Signer
	operatorID string
	head       string
	seq        int
}

func newEvidenceLedger(t testing.TB, signer oracletest.Signer) *evidenceLedger {
	return &evidenceLedger{t: t, signer: signer, operatorID: evidenceOperatorID, head: "h-0"}
}

func (l *evidenceLedger) opts(txID, actionType string) oracletest.ReceiptOpts {
	return oracletest.ReceiptOpts{
		TransactionID: txID, OperatorID: l.operatorID, SessionID: evidenceSessionID, ActionType: actionType,
		L2: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

// executed appends one commitment for txID and returns its signed receipt.
func (l *evidenceLedger) executed(txID, actionType string) *operatorv1.ActionReceipt {
	l.seq++
	hash := fmt.Sprintf("h-%d", l.seq)
	receipt := l.signer.Executed(l.t, l.opts(txID, actionType), l.head, hash)
	l.head = hash
	return receipt
}

// rejected returns a signed governance rejection for txID.
func (l *evidenceLedger) rejected(txID, actionType string) *operatorv1.ActionReceipt {
	return l.signer.Rejected(l.t, l.opts(txID, actionType), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED)
}

// validEvidenceTrace returns a trace whose evidence holds: an opening sentinel,
// one executed call, one rejected call, and a closing sentinel that connects.
func validEvidenceTrace(t *testing.T) (*AgentTrace, oracletest.Signer) {
	t.Helper()
	signer := oracletest.NewSigner(t)
	ledger := newEvidenceLedger(t, signer)
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	executed := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	rejected := ledger.rejected("tx-2", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	return &AgentTrace{
		RunID:               "run-1",
		AssignmentID:        "assignment-1",
		EvaluationAttemptID: "attempt-1",
		ScenarioID:          "scenario-1",
		DataOperatorID:      evidenceOperatorID,
		DataSessionID:       evidenceSessionID,
		DataOperatorAnchor:  &AgentTraceAnchor{ConformanceAnchor: signer.Anchor(evidenceOperatorID)},
		ToolCalls: []AgentToolCall{
			{CallID: "call-1", TransactionID: "tx-1", PolicyDecision: agentPolicyDecisionAllow, Receipt: &AgentTraceReceipt{ActionReceipt: executed}},
			{CallID: "call-2", TransactionID: "tx-2", PolicyDecision: agentPolicyDecisionDeny, Receipt: &AgentTraceReceipt{ActionReceipt: rejected}},
		},
		Bracket: &AgentTraceBracket{Before: AgentTraceReceipt{ActionReceipt: before}, After: AgentTraceReceipt{ActionReceipt: after}},
	}, signer
}

// flipFirstHexDigit changes the first hex digit of s, keeping it valid hex.
func flipFirstHexDigit(s string) string {
	if s[0] == '0' {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}

func TestVerifyAgentTraceEvidence_AcceptsConnectedBracketWithExecutedAndRejectedCalls(t *testing.T) {
	trace, _ := validEvidenceTrace(t)

	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestVerifyAgentTraceEvidence_RejectsFlippedSignatureByteInCallReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	receipt := trace.ToolCalls[0].Receipt.ActionReceipt
	receipt.Signature = flipFirstHexDigit(receipt.Signature)

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrActionReceiptSignatureInvalid)
}

func TestVerifyAgentTraceEvidence_RejectsReceiptSignedByAnotherKey(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	stranger := oracletest.NewSigner(t)
	trace.ToolCalls[0].Receipt = &AgentTraceReceipt{ActionReceipt: stranger.Executed(t, oracletest.ReceiptOpts{
		TransactionID: "tx-1", OperatorID: evidenceOperatorID, SessionID: evidenceSessionID,
		ActionType: string(constants.ActionTypeExecuteBash),
		L2:         operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}, "h-0", "h-1")}

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsAnchorForAnotherRuntime(t *testing.T) {
	trace, signer := validEvidenceTrace(t)
	trace.DataOperatorAnchor = &AgentTraceAnchor{ConformanceAnchor: signer.Anchor("operator-other")}

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsMissingDataOperatorAnchor(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.DataOperatorAnchor = nil

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrConformanceAnchorMissing)
}

func TestVerifyAgentTraceEvidence_RejectsReceiptTransactionThatDiffersFromCall(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].TransactionID = "tx-other"

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsAdmittedCallWithNoReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].Receipt = nil

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrActionReceiptMissing)
}

func TestVerifyAgentTraceEvidence_RejectsExecutedReceiptOnCallRecordedAsDeny(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].PolicyDecision = agentPolicyDecisionDeny

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsUnclaimedExecutionBetweenSentinels(t *testing.T) {
	signer := oracletest.NewSigner(t)
	ledger := newEvidenceLedger(t, signer)
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	claimed := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	_ = ledger.executed("tx-unclaimed", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := &AgentTrace{
		RunID: "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1", ScenarioID: "scenario-1",
		DataOperatorID: evidenceOperatorID, DataSessionID: evidenceSessionID,
		DataOperatorAnchor: &AgentTraceAnchor{ConformanceAnchor: signer.Anchor(evidenceOperatorID)},
		ToolCalls: []AgentToolCall{
			{CallID: "call-1", TransactionID: "tx-1", PolicyDecision: agentPolicyDecisionAllow, Receipt: &AgentTraceReceipt{ActionReceipt: claimed}},
		},
		Bracket: &AgentTraceBracket{Before: AgentTraceReceipt{ActionReceipt: before}, After: AgentTraceReceipt{ActionReceipt: after}},
	}

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrConformanceUnclaimedExecution)
}

func TestVerifyAgentTraceEvidence_RejectsClaimedCallsSwappedInTraceAsLedgerGap(t *testing.T) {
	signer := oracletest.NewSigner(t)
	ledger := newEvidenceLedger(t, signer)
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	first := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	second := ledger.executed("tx-2", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := &AgentTrace{
		RunID: "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1", ScenarioID: "scenario-1",
		DataOperatorID: evidenceOperatorID, DataSessionID: evidenceSessionID,
		DataOperatorAnchor: &AgentTraceAnchor{ConformanceAnchor: signer.Anchor(evidenceOperatorID)},
		ToolCalls: []AgentToolCall{
			{CallID: "call-2", TransactionID: "tx-2", PolicyDecision: agentPolicyDecisionAllow, Receipt: &AgentTraceReceipt{ActionReceipt: second}},
			{CallID: "call-1", TransactionID: "tx-1", PolicyDecision: agentPolicyDecisionAllow, Receipt: &AgentTraceReceipt{ActionReceipt: first}},
		},
		Bracket: &AgentTraceBracket{Before: AgentTraceReceipt{ActionReceipt: before}, After: AgentTraceReceipt{ActionReceipt: after}},
	}

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrConformanceLedgerGap)
}

func TestVerifyAgentTraceEvidence_RejectsMissingBracket(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.Bracket = nil

	err := VerifyAgentTraceEvidence(trace)

	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

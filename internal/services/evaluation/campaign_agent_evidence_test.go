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
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	evidenceOperatorID          = "operator-data-1"
	evidenceSessionID           = "session-data-1"
	evidenceInferenceOperatorID = "operator-inference-1"
	evidenceInferenceSessionID  = "session-inference-1"
)

// evidenceLedger is one Operator runtime's commitment ledger as a test double.
// An executed or execution-failed receipt links to the head before it, so a
// bracket built from it connects the way the real ledger does. A rejection
// appends nothing.
type evidenceLedger struct {
	t          testing.TB
	signer     oracletest.Signer
	operatorID string
	sessionID  string
	head       string
	seq        int
}

func newEvidenceLedger(t testing.TB, signer oracletest.Signer) *evidenceLedger {
	return newRuntimeLedger(t, signer, evidenceOperatorID, evidenceSessionID)
}

func newRuntimeLedger(t testing.TB, signer oracletest.Signer, operatorID, sessionID string) *evidenceLedger {
	return &evidenceLedger{t: t, signer: signer, operatorID: operatorID, sessionID: sessionID, head: "h-0"}
}

// runtime is the trace record of this ledger's runtime, anchored to its key.
func (l *evidenceLedger) runtime() AgentTraceRuntime {
	return AgentTraceRuntime{
		OperatorID: l.operatorID,
		SessionID:  l.sessionID,
		Anchor:     &AgentTraceAnchor{ConformanceAnchor: l.signer.Anchor(l.operatorID)},
	}
}

func (l *evidenceLedger) opts(txID, actionType string) oracletest.ReceiptOpts {
	return oracletest.ReceiptOpts{
		TransactionID: txID, OperatorID: l.operatorID, SessionID: l.sessionID, ActionType: actionType,
		L2: operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, L3: operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

func (l *evidenceLedger) advance() (prior, hash string) {
	l.seq++
	prior, hash = l.head, fmt.Sprintf("h-%d", l.seq)
	l.head = hash
	return prior, hash
}

// executed appends one commitment for txID and returns its signed receipt.
func (l *evidenceLedger) executed(txID, actionType string) *operatorv1.ActionReceipt {
	prior, hash := l.advance()
	return l.signer.Executed(l.t, l.opts(txID, actionType), prior, hash)
}

// executedInSession appends one commitment for txID, signed as if an Operator
// session other than the ledger's own had executed it on the same runtime.
func (l *evidenceLedger) executedInSession(txID, actionType, sessionID string) *operatorv1.ActionReceipt {
	prior, hash := l.advance()
	opts := l.opts(txID, actionType)
	opts.SessionID = sessionID
	return l.signer.Executed(l.t, opts, prior, hash)
}

// rejected returns a signed governance rejection for txID.
func (l *evidenceLedger) rejected(txID, actionType string) *operatorv1.ActionReceipt {
	return l.signer.Rejected(l.t, l.opts(txID, actionType), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED)
}

// inference appends one completed inference whose result_summary is the
// call's result digest.
func (l *evidenceLedger) inference(txID string) *operatorv1.ActionReceipt {
	receipt := l.executed(txID, string(constants.ActionTypeInference))
	receipt.ResultSummary = evidenceResultDigest(txID)
	l.signer.Resign(l.t, receipt)
	return receipt
}

// inferenceFailed appends one inference whose provider failed. The handler
// ran, so the runtime still commits it.
func (l *evidenceLedger) inferenceFailed(txID string) *operatorv1.ActionReceipt {
	prior, hash := l.advance()
	return l.signer.ExecutionFailed(l.t, l.opts(txID, string(constants.ActionTypeInference)), prior, hash)
}

func evidenceResultDigest(txID string) string {
	return "digest-" + txID
}

func completedModelCall(txID string, receipt *operatorv1.ActionReceipt) AgentModelCall {
	return AgentModelCall{
		ProviderAttemptID: "attempt-" + txID,
		TransactionID:     txID,
		ResultDigest:      evidenceResultDigest(txID),
		Receipt:           &AgentTraceReceipt{ActionReceipt: receipt},
	}
}

func failedModelCall(txID string, receipt *operatorv1.ActionReceipt) AgentModelCall {
	return AgentModelCall{
		ProviderAttemptID: "attempt-" + txID,
		TransactionID:     txID,
		Error:             "provider failed",
		Receipt:           &AgentTraceReceipt{ActionReceipt: receipt},
	}
}

func allowedToolCall(txID string, receipt *operatorv1.ActionReceipt) AgentToolCall {
	return AgentToolCall{CallID: "call-" + txID, TransactionID: txID, PolicyDecision: agentPolicyDecisionAllow, Receipt: &AgentTraceReceipt{ActionReceipt: receipt}}
}

func bracketOf(before, after *operatorv1.ActionReceipt) *AgentTraceBracket {
	return &AgentTraceBracket{Before: AgentTraceReceipt{ActionReceipt: before}, After: AgentTraceReceipt{ActionReceipt: after}}
}

func evidenceTrace(inference, data AgentTraceRuntime) *AgentTrace {
	return &AgentTrace{
		SchemaVersion: agentTraceSchemaVersionV6,
		RunID:         "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1", ScenarioID: "scenario-1",
		Inference: inference, Data: data, Input: testAgentTraceInput(),
	}
}

// testAgentTraceInput is the minimal recorded model input a sealed trace
// requires.
func testAgentTraceInput() *AgentTraceInput {
	return &AgentTraceInput{
		Messages: []AgentTraceMessage{
			{Role: "INFERENCE_MESSAGE_ROLE_SYSTEM", Text: "You are the primary investigator."},
			{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Why is checkout failing?"},
		},
		Tools: []AgentTraceTool{{Name: "inspect", Description: "Inspect one target.", JSONSchema: `{"type":"object"}`}},
	}
}

// validEvidenceTrace returns a trace on distinct Inference and Data runtimes
// whose evidence holds: one completed inference on the Inference runtime, and
// on the Data runtime an opening sentinel, one executed call, one rejected
// call, and a closing sentinel that connects.
func validEvidenceTrace(t *testing.T) (*AgentTrace, oracletest.Signer) {
	t.Helper()
	signer := oracletest.NewSigner(t)
	data := newEvidenceLedger(t, signer)
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	model := inference.inference("tx-m1")
	before := data.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	executed := data.executed("tx-1", string(constants.ActionTypeExecuteBash))
	rejected := data.rejected("tx-2", string(constants.ActionTypeExecuteBash))
	after := data.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(inference.runtime(), data.runtime())
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", model)}
	trace.ToolCalls = []AgentToolCall{
		allowedToolCall("tx-1", executed),
		{CallID: "call-2", TransactionID: "tx-2", PolicyDecision: agentPolicyDecisionDeny, Receipt: &AgentTraceReceipt{ActionReceipt: rejected}},
	}
	trace.Bracket = bracketOf(before, after)
	return trace, signer
}

// sharedRuntimeTrace returns a trace whose Inference and Data roles share one
// Operator runtime, as the Gateway's embedded data,inference Operator does:
// two model turns interleave with one tool call on one ledger.
func sharedRuntimeTrace(t *testing.T) (*AgentTrace, *evidenceLedger) {
	t.Helper()
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	first := ledger.inference("tx-m1")
	tool := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	second := ledger.inference("tx-m2")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", first), completedModelCall("tx-m2", second)}
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", tool)}
	trace.Bracket = bracketOf(before, after)
	return trace, ledger
}

// flipFirstHexDigit changes the first hex digit of s, keeping it valid hex.
func flipFirstHexDigit(s string) string {
	if s[0] == '0' {
		return "1" + s[1:]
	}
	return "0" + s[1:]
}

func requireEvidenceError(t *testing.T, trace *AgentTrace, cause error) {
	t.Helper()
	err := VerifyAgentTraceEvidence(trace)
	assert.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, cause)
}

func TestVerifyAgentTraceEvidence_AcceptsConnectedBracketWithExecutedAndRejectedCalls(t *testing.T) {
	trace, _ := validEvidenceTrace(t)

	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

// Published legacy traces stay verifiable directly: the live import's schema pin
// is the gate that stops an earlier-version trace from being graded live.
func TestVerifyAgentTraceEvidence_AcceptsLegacyV6Trace(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	require.Equal(t, agentTraceSchemaVersionV6, trace.SchemaVersion)
	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestVerifyAgentTraceEvidence_RejectsFlippedSignatureByteInCallReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	receipt := trace.ToolCalls[0].Receipt.ActionReceipt
	receipt.Signature = flipFirstHexDigit(receipt.Signature)

	requireEvidenceError(t, trace, constants.ErrActionReceiptSignatureInvalid)
}

func TestVerifyAgentTraceEvidence_RejectsReceiptSignedByAnotherKey(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	stranger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace.ToolCalls[0].Receipt = &AgentTraceReceipt{ActionReceipt: stranger.executed("tx-1", string(constants.ActionTypeExecuteBash))}

	requireEvidenceError(t, trace, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsAnchorForAnotherRuntime(t *testing.T) {
	trace, signer := validEvidenceTrace(t)
	trace.Data.Anchor = &AgentTraceAnchor{ConformanceAnchor: signer.Anchor("operator-other")}

	requireEvidenceError(t, trace, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsMissingRuntimeAnchor(t *testing.T) {
	for name, clear := range map[string]func(*AgentTrace){
		"data":      func(trace *AgentTrace) { trace.Data.Anchor = nil },
		"inference": func(trace *AgentTrace) { trace.Inference.Anchor = nil },
	} {
		t.Run(name, func(t *testing.T) {
			trace, _ := validEvidenceTrace(t)
			clear(trace)

			requireEvidenceError(t, trace, constants.ErrConformanceAnchorMissing)
		})
	}
}

func TestVerifyAgentTraceEvidence_RejectsReceiptTransactionThatDiffersFromCall(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].TransactionID = "tx-other"

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsAdmittedCallWithNoReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].Receipt = nil

	requireEvidenceError(t, trace, constants.ErrActionReceiptMissing)
}

func TestVerifyAgentTraceEvidence_RejectsExecutedReceiptOnCallRecordedAsDeny(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.ToolCalls[0].PolicyDecision = agentPolicyDecisionDeny

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

// A foreign-session receipt on the Data runtime's ledger still connects the
// bracket, so only the session comparison can reject it.
func TestVerifyAgentTraceEvidence_RejectsClaimedToolReceiptFromAnotherSession(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	foreign := ledger.executedInSession("tx-1", string(constants.ActionTypeExecuteBash), "session-other")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", foreign)}
	trace.Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsClaimedInferenceReceiptFromAnotherSession(t *testing.T) {
	signer := oracletest.NewSigner(t)
	data := newEvidenceLedger(t, signer)
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	model := inference.executedInSession("tx-m1", string(constants.ActionTypeInference), "session-other")
	model.ResultSummary = evidenceResultDigest("tx-m1")
	inference.signer.Resign(t, model)
	before := data.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	after := data.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(inference.runtime(), data.runtime())
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", model)}
	trace.Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsUnclaimedExecutionBetweenSentinels(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	claimed := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	_ = ledger.executed("tx-unclaimed", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", claimed)}
	trace.Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrConformanceUnclaimedExecution)
}

func TestVerifyAgentTraceEvidence_RejectsClaimedCallsSwappedInTraceAsLedgerGap(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	model := ledger.inference("tx-m1")
	first := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	second := ledger.executed("tx-2", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", model)}
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-2", second), allowedToolCall("tx-1", first)}
	trace.Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrConformanceLedgerGap)
}

func TestVerifyAgentTraceEvidence_RejectsMissingBracket(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.Bracket = nil

	requireEvidenceError(t, trace, constants.ErrMissingRequiredField)
}

// A runtime holding both capabilities commits every inference to the ledger
// the Data bracket reads. Claiming the model calls alongside the tool calls,
// in ledger order, accounts for the whole bracket.
func TestVerifyAgentTraceEvidence_SharedRuntimeClaimsInterleavedModelAndToolCalls(t *testing.T) {
	trace, _ := sharedRuntimeTrace(t)

	require.True(t, trace.SharedRuntime())
	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

// The regression behind a run where every assignment ended INVALID_EVIDENCE: a
// shared runtime's inferences sit inside the Data bracket, so a trace that
// drops its model calls leaves them unclaimed.
func TestVerifyAgentTraceEvidence_SharedRuntimeRejectsInferenceTheTraceDoesNotClaim(t *testing.T) {
	trace, _ := sharedRuntimeTrace(t)
	trace.ModelCalls = trace.ModelCalls[:1]

	requireEvidenceError(t, trace, constants.ErrConformanceUnclaimedExecution)
}

// On distinct runtimes the Inference ledger is not bracketed, so its
// commitments are verified per call but never claimed in the Data bracket.
func TestVerifyAgentTraceEvidence_DistinctRuntimesDoNotClaimInferenceInDataBracket(t *testing.T) {
	trace, _ := validEvidenceTrace(t)

	require.False(t, trace.SharedRuntime())
	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

// A failed provider inference still ran its handler, so the runtime signs a
// FAILED receipt and commits it. The trace carries that receipt and the
// bracket claims it.
func TestVerifyAgentTraceEvidence_SharedRuntimeClaimsFailedInference(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	completed := ledger.inference("tx-m1")
	tool := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	failed := ledger.inferenceFailed("tx-m2")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	trace.ModelCalls = []AgentModelCall{completedModelCall("tx-m1", completed), failedModelCall("tx-m2", failed)}
	trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", tool)}
	trace.Bracket = bracketOf(before, after)

	require.NoError(t, VerifyAgentTraceEvidence(trace))

	trace.ModelCalls = trace.ModelCalls[:1]
	requireEvidenceError(t, trace, constants.ErrConformanceUnclaimedExecution)
}

// A shared runtime that also observes its own provider attempts commits the
// observer's BEGIN and FINALIZE around each inference. The Gateway waits for
// those receipts and the model call carries them, so the bracket claims them.
// A receipt another runtime signed sits on that runtime's ledger and is not
// claimed here; a non-observation receipt cannot ride in as an observation.
func TestVerifyAgentTraceEvidence_SharedRuntimeClaimsObservationReceipts(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	begin := ledger.executed("tx-observe-begin", string(constants.ActionTypeProviderBoundaryObservation))
	model := ledger.inference("tx-m1")
	finalize := ledger.executed("tx-observe-finalize", string(constants.ActionTypeModelProvenanceObservation))
	tool := ledger.executed("tx-1", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	observer := newRuntimeLedger(t, oracletest.NewSigner(t), "operator-observer-1", "session-observer-1")
	foreign := observer.executed("tx-observe-foreign", string(constants.ActionTypeProviderBoundaryObservation))
	newTrace := func(observations ...*operatorv1.ActionReceipt) *AgentTrace {
		trace := evidenceTrace(ledger.runtime(), ledger.runtime())
		call := completedModelCall("tx-m1", model)
		for _, receipt := range observations {
			call.ObservationReceipts = append(call.ObservationReceipts, AgentTraceReceipt{ActionReceipt: receipt})
		}
		trace.ModelCalls = []AgentModelCall{call}
		trace.ToolCalls = []AgentToolCall{allowedToolCall("tx-1", tool)}
		trace.Bracket = bracketOf(before, after)
		return trace
	}

	require.NoError(t, VerifyAgentTraceEvidence(newTrace(begin, foreign, finalize)))
	requireEvidenceError(t, newTrace(begin), constants.ErrConformanceUnclaimedExecution)
	requireEvidenceError(t, newTrace(begin, finalize, tool), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_AcceptsRejectedInferenceWithoutCommitment(t *testing.T) {
	trace, ledger := sharedRuntimeTrace(t)
	rejected := ledger.rejected("tx-m3", string(constants.ActionTypeInference))
	trace.ModelCalls = append(trace.ModelCalls, failedModelCall("tx-m3", rejected))

	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestVerifyAgentTraceEvidence_RejectsModelCallEvidence(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *AgentTrace)
		cause   error
	}{
		{
			name:    "recorded call has no receipt",
			corrupt: func(_ *testing.T, trace *AgentTrace) { trace.ModelCalls[0].Receipt = nil },
			cause:   constants.ErrActionReceiptMissing,
		},
		{
			name:    "result digest differs from the signed result summary",
			corrupt: func(_ *testing.T, trace *AgentTrace) { trace.ModelCalls[0].ResultDigest = "digest-forged" },
			cause:   constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name:    "receipt is for another transaction",
			corrupt: func(_ *testing.T, trace *AgentTrace) { trace.ModelCalls[0].TransactionID = "tx-other" },
			cause:   constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name: "completed call carries a failed execution receipt",
			corrupt: func(t *testing.T, trace *AgentTrace) {
				failed := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
				trace.Inference = failed.runtime()
				trace.ModelCalls[0].Receipt = &AgentTraceReceipt{ActionReceipt: failed.inferenceFailed("tx-m1")}
			},
			cause: constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name: "inference receipt signed by a key the runtime did not enroll",
			corrupt: func(t *testing.T, trace *AgentTrace) {
				impostor := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
				trace.ModelCalls[0].Receipt = &AgentTraceReceipt{ActionReceipt: impostor.inference("tx-m1")}
			},
			cause: constants.ErrConformanceAnchorUnknown,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace, _ := validEvidenceTrace(t)
			test.corrupt(t, trace)

			requireEvidenceError(t, trace, test.cause)
		})
	}
}

func TestVerifyAgentTraceEvidence_SharedRuntimeMustPinOneKey(t *testing.T) {
	trace, _ := sharedRuntimeTrace(t)
	trace.Inference.Anchor = &AgentTraceAnchor{ConformanceAnchor: oracletest.NewSigner(t).Anchor(trace.Inference.OperatorID)}

	requireEvidenceError(t, trace, constants.ErrConformanceAnchorInvalid)
}

// shellEpisodeTrace returns a v7 trace whose one signed agent turn requested
// run_shell_command with raw, declared from the native catalog, and whose
// recorded tool call carries recorded as its arguments.
func shellEpisodeTrace(t *testing.T, raw, recorded string) *AgentTrace {
	t.Helper()
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	scope, err := catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	require.NoError(t, err)
	input, err := newAgentTraceInput(nil, scope.Declarations())
	require.NoError(t, err)
	result := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{{
		Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "call-1", Name: constants.MCPToolRunShellCommand, ArgumentsJson: raw}},
	}}}
	return &AgentTrace{
		SchemaVersion: agentTraceSchemaVersionV7,
		Input:         input,
		ModelCalls: []AgentModelCall{{
			ProviderAttemptID: "attempt-1", TransactionID: "tx-m1", Purpose: agentModelCallPurposeTurn,
			InferenceResult: &AgentTraceInferenceResult{InferenceResult: result},
		}},
		ToolCalls: []AgentToolCall{{ModelCallID: "attempt-1", ProviderCallID: "call-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: recorded}},
	}
}

func TestVerifyV7EpisodeOutputs_BindsToolArgumentsThroughTypedRequest(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		recorded string
		cause    error
	}{
		{name: "typed shell request records canonical form", raw: `{"args":["hi"],"command":"echo"}`, recorded: models.RunShellCommandRequest{Command: "echo", Args: []string{"hi"}}.CanonicalJSON()},
		{name: "schema-invalid shell request records generic form", raw: `{"args": ["hi"]}`, recorded: `{"args":["hi"]}`},
		{name: "recorded arguments differ from the signed request", raw: `{"command":"echo"}`, recorded: `{"command":"rm"}`, cause: constants.ErrEvaluationEvidenceTraceMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := verifyV7EpisodeOutputs(shellEpisodeTrace(t, test.raw, test.recorded))
			if test.cause == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, test.cause)
		})
	}

	t.Run("tool calls without recorded declarations", func(t *testing.T) {
		trace := shellEpisodeTrace(t, `{"command":"echo"}`, `{"command":"echo"}`)
		trace.Input = nil
		require.ErrorIs(t, verifyV7EpisodeOutputs(trace), constants.ErrEvaluationEvidenceTraceMismatch)
	})

	t.Run("undecodable declaration", func(t *testing.T) {
		trace := shellEpisodeTrace(t, `{"command":"echo"}`, `{"command":"echo"}`)
		trace.Input.Tools[0].JSONSchema = "{"
		require.ErrorIs(t, verifyV7EpisodeOutputs(trace), constants.ErrEvaluationEvidenceTraceMismatch)
	})
}

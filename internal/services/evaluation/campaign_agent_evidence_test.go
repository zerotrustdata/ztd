// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govhash "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
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

func newEvidenceLedger(tb testing.TB, signer oracletest.Signer) *evidenceLedger {
	tb.Helper()
	return newRuntimeLedger(tb, signer, evidenceOperatorID, evidenceSessionID)
}

func newRuntimeLedger(tb testing.TB, signer oracletest.Signer, operatorID, sessionID string) *evidenceLedger {
	tb.Helper()
	return &evidenceLedger{t: tb, signer: signer, operatorID: operatorID, sessionID: sessionID, head: "h-0"}
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
func (l *evidenceLedger) executed(txID, actionType string) *operatorv1.EventReceipt {
	prior, hash := l.advance()
	return l.signer.Executed(l.t, l.opts(txID, actionType), prior, hash)
}

// executedInSession appends one commitment for txID, signed as if an Operator
// session other than the ledger's own had executed it on the same runtime.
func (l *evidenceLedger) executedInSession(txID, actionType, sessionID string) *operatorv1.EventReceipt {
	prior, hash := l.advance()
	opts := l.opts(txID, actionType)
	opts.SessionID = sessionID
	return l.signer.Executed(l.t, opts, prior, hash)
}

// rejected returns a signed governance rejection for txID.
func (l *evidenceLedger) rejected(txID, actionType string) *operatorv1.EventReceipt {
	return l.signer.Rejected(l.t, l.opts(txID, actionType), operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED)
}

// inference appends one completed inference whose result_summary is the
// call's result digest.
func (l *evidenceLedger) inference(txID string) *operatorv1.EventReceipt {
	receipt := l.executed(txID, string(constants.ActionTypeInference))
	receipt.ResultSummary = evidenceResultDigest(txID)
	l.signer.Resign(l.t, receipt)
	return receipt
}

// inferenceFailed appends one inference whose provider failed. The handler
// ran, so the runtime still commits it.
func (l *evidenceLedger) inferenceFailed(txID string) *operatorv1.EventReceipt {
	prior, hash := l.advance()
	return l.signer.ExecutionFailed(l.t, l.opts(txID, string(constants.ActionTypeInference)), prior, hash)
}

func evidenceResultDigest(txID string) string {
	return "digest-" + txID
}

func buildModelTurnResult(tb testing.TB, trace *AgentTrace, attemptID, text string, tools ...*operatorv1.InferenceToolCall) *operatorv1.InferenceResult {
	tb.Helper()
	var parts []*operatorv1.InferenceResponsePart
	if text != "" {
		parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_Text{Text: text}})
	}
	for _, tc := range tools {
		parts = append(parts, &operatorv1.InferenceResponsePart{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: tc}})
	}
	result := &operatorv1.InferenceResult{
		PromptTokens:         2,
		CompletionTokens:     3,
		TotalTokens:          5,
		FinishReason:         "stop",
		Model:                trace.ServedModelTag,
		Parts:                parts,
		UsageReported:        true,
		ProviderAttemptId:    attemptID,
		RequestedModel:       trace.ServedModelTag,
		RequestedModelDigest: trace.ModelDigest,
		ServedModelDigest:    trace.ModelDigest,
		CampaignId:           trace.CampaignID,
		RunId:                trace.RunID,
		AssignmentId:         trace.AssignmentID,
		EvaluationAttemptId:  trace.EvaluationAttemptID,
		ScenarioId:           trace.ScenarioID,
		ModelRegistryDigest:  trace.ModelRegistryDigest,
		LoadState:            operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNSPECIFIED,
		RetryClassification:  operatorv1.InferenceRetryClassification_INFERENCE_RETRY_CLASSIFICATION_UNSPECIFIED,
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(tb, err)
	result.ResultDigest = digest
	return result
}

func modelCallFromResult(txID string, loopTurn int, trace *AgentTrace, result *operatorv1.InferenceResult, receipt *operatorv1.EventReceipt, toolsCount int, text string) AgentModelCall {
	return AgentModelCall{
		ProviderAttemptID: result.GetProviderAttemptId(),
		TransactionID:     txID,
		Purpose:           agentModelCallPurposeTurn,
		LoopTurn:          loopTurn,
		ModelVariantID:    "variant-a",
		ModelRole:         operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(),
		ServedModelTag:    trace.ServedModelTag,
		ModelDigest:       trace.ModelDigest,
		ToolsDeclared:     []string{},
		ResultDigest:      result.GetResultDigest(),
		UsageReported:     true,
		PromptTokens:      2,
		CompletionTokens:  3,
		FinishReason:      "stop",
		LoadState:         operatorLoadStateToEvaluation(result.GetLoadState()).String(),
		Text:              text,
		ToolCallCount:     toolsCount,
		Receipt:           &AgentTraceReceipt{EventReceipt: receipt},
		InferenceResult:   &AgentTraceInferenceResult{InferenceResult: result},
	}
}

func sealModelTurn(tb testing.TB, ledger *evidenceLedger, trace *AgentTrace, txID string, loopTurn int, tools ...*operatorv1.InferenceToolCall) AgentModelCall {
	tb.Helper()
	result := buildModelTurnResult(tb, trace, "attempt-"+txID, "", tools...)
	receipt := ledger.executed(txID, string(constants.ActionTypeInference))
	receipt.ResultSummary = result.GetResultDigest()
	ledger.signer.Resign(tb, receipt)
	return modelCallFromResult(txID, loopTurn, trace, result, receipt, len(tools), "")
}

func sealFailedModelTurn(txID string, loopTurn int, trace *AgentTrace, receipt *operatorv1.EventReceipt) AgentModelCall {
	return AgentModelCall{
		ProviderAttemptID: "attempt-" + txID,
		TransactionID:     txID,
		Purpose:           agentModelCallPurposeTurn,
		LoopTurn:          loopTurn,
		ModelVariantID:    "variant-a",
		ModelRole:         operatorv1.ModelRole_MODEL_ROLE_PRIMARY.String(),
		ServedModelTag:    trace.ServedModelTag,
		ModelDigest:       trace.ModelDigest,
		Error:             "provider failed",
		Receipt:           &AgentTraceReceipt{EventReceipt: receipt},
	}
}

func prepareShellToolCall(tb testing.TB, trace *AgentTrace, modelTxID, callID, command string) (*operatorv1.InferenceToolCall, AgentToolCall) {
	tb.Helper()
	const targetIndex = 1
	args := (models.RunShellCommandRequest{Command: command}).CanonicalJSON()
	member := trace.Roster[targetIndex-1]
	spec, err := catalogToolSpec(constants.MCPToolRunShellCommand)
	require.NoError(tb, err)
	dispatchArgs, failure, err := harnessDispatchArguments(spec, args, member, trace.InvestigationID)
	require.NoError(tb, err)
	require.Nil(tb, failure)
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{
		ToolName:      constants.MCPToolRunShellCommand,
		ArgumentsJson: dispatchArgs,
		ExecutionId:   callID,
	})
	require.NoError(tb, err)
	envelope := &commonv1.GovernanceEnvelope{
		ActionType:        string(constants.ActionTypeMcpCall),
		TargetResource:    constants.MCPToolRunShellCommand,
		Payload:           payload,
		OperatorId:        member.Runtime.OperatorID,
		OperatorSessionId: member.Runtime.SessionID,
		InvestigationId:   trace.InvestigationID,
		TaskId:            trace.EvaluationAttemptID,
	}
	hash, err := govhash.GenerateMessageID(envelope)
	require.NoError(tb, err)
	envelope.TransactionHash = hash
	requestedArgs := args
	var targetOperators []string
	if len(trace.Roster) > 1 {
		target := fmt.Sprintf("%d", targetIndex)
		targetOperators = []string{target}
		requestedArgs = fmt.Sprintf(`{"command":%q,"target_operators":[%q]}`, command, target)
	}
	call := AgentToolCall{
		CallID:          callID,
		GroupID:         callID,
		ModelCallID:     "attempt-" + modelTxID,
		ProviderCallID:  "provider-" + callID,
		ToolName:        constants.MCPToolRunShellCommand,
		TargetOperators: targetOperators,
		TargetIndex:     targetIndex,
		ArgumentsJSON:   args,
		LoopTurn:        1,
		ActionType:      string(constants.ActionTypeMcpCall),
		RequestEnvelope: &AgentTraceEnvelope{GovernanceEnvelope: envelope},
	}
	requested := &operatorv1.InferenceToolCall{
		CallId:        call.ProviderCallID,
		Name:          constants.MCPToolRunShellCommand,
		ArgumentsJson: requestedArgs,
	}
	return requested, call
}

func attachShellToolReceipt(tb testing.TB, ledger *evidenceLedger, call AgentToolCall, txID string, receipt *operatorv1.EventReceipt) AgentToolCall {
	tb.Helper()
	envelope := call.RequestEnvelope.GovernanceEnvelope
	envelope.Id = txID
	receipt.TransactionHash = envelope.GetTransactionHash()
	receipt.ResultSummary = `{"exit_code":0,"stdout":"ok"}`
	for _, stage := range receipt.DeterministicStageEvidence {
		stage.TransactionHash = envelope.GetTransactionHash()
	}
	ledger.signer.Resign(tb, receipt)
	call.TransactionID = txID
	call.PolicyDecision = agentPolicyDecisionAllow
	call.Receipt = &AgentTraceReceipt{EventReceipt: receipt}
	completed, err := (&agentRun{}).completeNativeToolCall(call)
	require.NoError(tb, err)
	return completed
}

func commitShellToolCall(tb testing.TB, ledger *evidenceLedger, call AgentToolCall, txID string) AgentToolCall {
	tb.Helper()
	return attachShellToolReceipt(tb, ledger, call, txID, ledger.executed(txID, string(constants.ActionTypeMcpCall)))
}

func rejectShellToolCall(tb testing.TB, ledger *evidenceLedger, call AgentToolCall, txID string) AgentToolCall {
	tb.Helper()
	rejected := ledger.rejected(txID, string(constants.ActionTypeMcpCall))
	call.TransactionID = txID
	call.PolicyDecision = agentPolicyDecisionDeny
	call.RequestEnvelope = nil
	call.Receipt = &AgentTraceReceipt{EventReceipt: rejected}
	return call
}

func bracketOf(before, after *operatorv1.EventReceipt) *AgentTraceBracket {
	return &AgentTraceBracket{Before: AgentTraceReceipt{EventReceipt: before}, After: AgentTraceReceipt{EventReceipt: after}}
}

func evidenceTrace(inference, data AgentTraceRuntime) *AgentTrace {
	trace := &AgentTrace{
		SchemaVersion:       agentTraceSchemaVersion,
		CampaignID:          "campaign-1",
		RunID:               "run-1",
		AssignmentID:        "assignment-1",
		EvaluationAttemptID: "attempt-1",
		ScenarioID:          "scenario-1",
		DesignatedRole:      "primary",
		ServedModelTag:      "model-a:latest",
		ModelDigest:         strings.Repeat("a", 64),
		ModelRegistryDigest: strings.Repeat("b", 64),
		Inference:           inference,
		Roster:              rosterOfOne(data, AgentTraceWorkspace{Root: "/work", OperatorWorkingDirectory: "/"}),
		Input:               testAgentTraceInput(),
	}
	withRosterPrompt(trace)
	return trace
}

// testAgentTraceInput is the minimal recorded model input a sealed trace
// requires.
func testAgentTraceInput() *AgentTraceInput {
	catalog, err := newAgentToolCatalog()
	if err != nil {
		panic(err)
	}
	scope, err := catalog.Scope(ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolRunShellCommand}})
	if err != nil {
		panic(err)
	}
	input, err := newAgentTraceInput([]*operatorv1.InferenceMessage{
		agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, "You are the primary investigator."),
		agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, "Why is checkout failing?"),
	}, scope.Declarations())
	if err != nil {
		panic(err)
	}
	return input
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
	trace := evidenceTrace(inference.runtime(), data.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	req2, call2 := prepareShellToolCall(t, trace, "tx-m1", "call-2", "rm")
	m1 := sealModelTurn(t, inference, trace, "tx-m1", 1, req1, req2)
	before := data.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	call1 = commitShellToolCall(t, data, call1, "tx-1")
	call2 = rejectShellToolCall(t, data, call2, "tx-2")
	after := data.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1}
	trace.ToolCalls = []AgentToolCall{call1, call2}
	trace.Roster[0].Bracket = bracketOf(before, after)
	return trace, signer
}

// sharedRuntimeTrace returns a trace whose Inference and Data roles share one
// Operator runtime, as the Gateway's embedded data,inference Operator does:
// two model turns interleave with one tool call on one ledger.
func sharedRuntimeTrace(t *testing.T) (*AgentTrace, *evidenceLedger) {
	t.Helper()
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	m1 := sealModelTurn(t, ledger, trace, "tx-m1", 1, req1)
	call1 = commitShellToolCall(t, ledger, call1, "tx-1")
	m2 := sealModelTurn(t, ledger, trace, "tx-m2", 2)
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1, m2}
	trace.ToolCalls = []AgentToolCall{call1}
	trace.Roster[0].Bracket = bracketOf(before, after)
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
	require.ErrorIs(t, err, constants.ErrCampaignToolEvidenceInvalid)
	assert.ErrorIs(t, err, cause)
}

func TestVerifyAgentTraceEvidence_AcceptsConnectedBracketWithExecutedAndRejectedCalls(t *testing.T) {
	trace, _ := validEvidenceTrace(t)

	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestVerifyAgentTraceEvidence_RejectsFlippedSignatureByteInCallReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	receipt := trace.ToolCalls[0].Receipt.EventReceipt
	receipt.Signature = flipFirstHexDigit(receipt.Signature)

	requireEvidenceError(t, trace, constants.ErrEventReceiptSignatureInvalid)
}

func TestVerifyAgentTraceEvidence_RejectsReceiptSignedByAnotherKey(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	stranger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace.ToolCalls[0] = attachShellToolReceipt(t, stranger, trace.ToolCalls[0], "tx-1", stranger.executed("tx-1", string(constants.ActionTypeMcpCall)))

	requireEvidenceError(t, trace, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsAnchorForAnotherRuntime(t *testing.T) {
	trace, signer := validEvidenceTrace(t)
	trace.Roster[0].Runtime.Anchor = &AgentTraceAnchor{ConformanceAnchor: signer.Anchor("operator-other")}

	requireEvidenceError(t, trace, constants.ErrConformanceAnchorUnknown)
}

func TestVerifyAgentTraceEvidence_RejectsMissingRuntimeAnchor(t *testing.T) {
	for name, clear := range map[string]func(*AgentTrace){
		"data":      func(trace *AgentTrace) { trace.Roster[0].Runtime.Anchor = nil },
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

	requireEvidenceError(t, trace, constants.ErrEventReceiptMissing)
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
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	m1 := sealModelTurn(t, ledger, trace, "tx-m1", 1, req1)
	foreign := ledger.executedInSession("tx-1", string(constants.ActionTypeMcpCall), "session-other")
	call1 = attachShellToolReceipt(t, ledger, call1, "tx-1", foreign)
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1}
	trace.ToolCalls = []AgentToolCall{call1}
	trace.Roster[0].Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsClaimedInferenceReceiptFromAnotherSession(t *testing.T) {
	signer := oracletest.NewSigner(t)
	data := newEvidenceLedger(t, signer)
	inference := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
	trace := evidenceTrace(inference.runtime(), data.runtime())
	result := buildModelTurnResult(t, trace, "attempt-tx-m1", "")
	model := inference.executedInSession("tx-m1", string(constants.ActionTypeInference), "session-other")
	model.ResultSummary = result.GetResultDigest()
	inference.signer.Resign(t, model)
	before := data.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	after := data.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{modelCallFromResult("tx-m1", 1, trace, result, model, 0, "")}
	trace.Roster[0].Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestVerifyAgentTraceEvidence_RejectsUnclaimedExecutionBetweenSentinels(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	m1 := sealModelTurn(t, ledger, trace, "tx-m1", 1, req1)
	call1 = commitShellToolCall(t, ledger, call1, "tx-1")
	_ = ledger.executed("tx-unclaimed", string(constants.ActionTypeExecuteBash))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1}
	trace.ToolCalls = []AgentToolCall{call1}
	trace.Roster[0].Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrConformanceUnclaimedExecution)
}

func TestVerifyAgentTraceEvidence_RejectsClaimedCallsSwappedInTraceAsLedgerGap(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	req2, call2 := prepareShellToolCall(t, trace, "tx-m1", "call-2", "pwd")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	m1 := sealModelTurn(t, ledger, trace, "tx-m1", 1, req1, req2)
	call1 = commitShellToolCall(t, ledger, call1, "tx-1")
	call2 = commitShellToolCall(t, ledger, call2, "tx-2")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1}
	trace.ToolCalls = []AgentToolCall{call2, call1}
	trace.Roster[0].Bracket = bracketOf(before, after)

	requireEvidenceError(t, trace, constants.ErrConformanceLedgerGap)
}

func TestVerifyAgentTraceEvidence_RejectsMissingBracket(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	trace.Roster[0].Bracket = nil

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
	trace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, trace, "tx-m1", "call-1", "echo")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	m1 := sealModelTurn(t, ledger, trace, "tx-m1", 1, req1)
	call1 = commitShellToolCall(t, ledger, call1, "tx-1")
	failed := ledger.inferenceFailed("tx-m2")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	trace.ModelCalls = []AgentModelCall{m1, sealFailedModelTurn("tx-m2", 2, trace, failed)}
	trace.ToolCalls = []AgentToolCall{call1}
	trace.Roster[0].Bracket = bracketOf(before, after)

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
	baseTrace := evidenceTrace(ledger.runtime(), ledger.runtime())
	req1, call1 := prepareShellToolCall(t, baseTrace, "tx-m1", "call-1", "echo")
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	begin := ledger.executed("tx-observe-begin", string(constants.ActionTypeProviderBoundaryObservation))
	m1 := sealModelTurn(t, ledger, baseTrace, "tx-m1", 1, req1)
	finalize := ledger.executed("tx-observe-finalize", string(constants.ActionTypeModelProvenanceObservation))
	call1 = commitShellToolCall(t, ledger, call1, "tx-1")
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	observer := newRuntimeLedger(t, oracletest.NewSigner(t), "operator-observer-1", "session-observer-1")
	foreign := observer.executed("tx-observe-foreign", string(constants.ActionTypeProviderBoundaryObservation))
	newTrace := func(observations ...*operatorv1.EventReceipt) *AgentTrace {
		trace := evidenceTrace(ledger.runtime(), ledger.runtime())
		call := m1
		call.ObservationReceipts = nil
		for _, receipt := range observations {
			call.ObservationReceipts = append(call.ObservationReceipts, AgentTraceReceipt{EventReceipt: receipt})
		}
		trace.ModelCalls = []AgentModelCall{call}
		trace.ToolCalls = []AgentToolCall{call1}
		trace.Roster[0].Bracket = bracketOf(before, after)
		return trace
	}

	require.NoError(t, VerifyAgentTraceEvidence(newTrace(begin, foreign, finalize)))
	requireEvidenceError(t, newTrace(begin), constants.ErrConformanceUnclaimedExecution)
	requireEvidenceError(t, newTrace(begin, finalize, call1.Receipt.EventReceipt), constants.ErrEvaluationEvidenceTraceMismatch)
}

// An observer whose handler failed still commits on the ledger it signs for, so
// the bracket claims that commitment; without the claim it reads as an unclaimed
// execution. A rejected observation committed nothing and claims nothing.
func TestVerifyAgentTraceEvidence_SharedRuntimeClaimsFailedObservationReceipts(t *testing.T) {
	ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
	before := ledger.executed("tx-sentinel-before", string(constants.ActionTypeFsRead))
	begin := ledger.executed("tx-observe-begin", string(constants.ActionTypeModelProvenanceObservation))
	model := ledger.inferenceFailed("tx-m1")
	prior, hash := ledger.advance()
	finalize := ledger.signer.ExecutionFailed(t, ledger.opts("tx-observe-finalize", string(constants.ActionTypeModelProvenanceObservation)), prior, hash)
	rejected := ledger.rejected("tx-observe-rejected", string(constants.ActionTypeModelProvenanceObservation))
	after := ledger.executed("tx-sentinel-after", string(constants.ActionTypeFsRead))
	newTrace := func(observations ...*operatorv1.EventReceipt) *AgentTrace {
		trace := evidenceTrace(ledger.runtime(), ledger.runtime())
		call := sealFailedModelTurn("tx-m1", 1, trace, model)
		for _, receipt := range observations {
			call.ObservationReceipts = append(call.ObservationReceipts, AgentTraceReceipt{EventReceipt: receipt})
		}
		trace.ModelCalls = []AgentModelCall{call}
		trace.Roster[0].Bracket = bracketOf(before, after)
		return trace
	}

	require.NoError(t, VerifyAgentTraceEvidence(newTrace(begin, finalize)))
	require.NoError(t, VerifyAgentTraceEvidence(newTrace(begin, finalize, rejected)))
	requireEvidenceError(t, newTrace(begin), constants.ErrConformanceUnclaimedExecution)
}

func TestVerifyAgentTraceEvidence_AcceptsRejectedInferenceWithoutCommitment(t *testing.T) {
	trace, ledger := sharedRuntimeTrace(t)
	rejected := ledger.rejected("tx-m3", string(constants.ActionTypeInference))
	trace.ModelCalls = append(trace.ModelCalls, sealFailedModelTurn("tx-m3", 3, trace, rejected))

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
			cause:   constants.ErrEventReceiptMissing,
		},
		{
			name:    "result digest differs from the signed result summary",
			corrupt: func(_ *testing.T, trace *AgentTrace) { trace.ModelCalls[0].ResultDigest = "digest-forged" },
			cause:   constants.ErrInferenceResultDigest,
		},
		{
			name:    "receipt is for another transaction",
			corrupt: func(_ *testing.T, trace *AgentTrace) { trace.ModelCalls[0].TransactionID = "tx-other" },
			cause:   constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name: "completed call carries a failed event receipt",
			corrupt: func(t *testing.T, trace *AgentTrace) {
				t.Helper()
				failed := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
				trace.Inference = failed.runtime()
				receipt := failed.inferenceFailed("tx-m1")
				receipt.ResultSummary = trace.ModelCalls[0].ResultDigest
				failed.signer.Resign(t, receipt)
				trace.ModelCalls[0].Receipt = &AgentTraceReceipt{EventReceipt: receipt}
			},
			cause: constants.ErrEvaluationEvidenceTraceMismatch,
		},
		{
			name: "inference receipt signed by a key the runtime did not enroll",
			corrupt: func(t *testing.T, trace *AgentTrace) {
				t.Helper()
				impostor := newRuntimeLedger(t, oracletest.NewSigner(t), evidenceInferenceOperatorID, evidenceInferenceSessionID)
				trace.ModelCalls[0].Receipt = &AgentTraceReceipt{EventReceipt: impostor.inference("tx-m1")}
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

// shellEpisodeTrace returns a v14 trace whose one signed agent turn requested
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
		SchemaVersion: agentTraceSchemaVersion,
		Input:         input,
		ModelCalls: []AgentModelCall{{
			ProviderAttemptID: "attempt-1", TransactionID: "tx-m1", Purpose: agentModelCallPurposeTurn,
			InferenceResult: &AgentTraceInferenceResult{InferenceResult: result},
		}},
		Roster:    rosterOfOne(AgentTraceRuntime{OperatorID: "data-op", SessionID: "data-session"}, AgentTraceWorkspace{}),
		ToolCalls: []AgentToolCall{{CallID: "tool-call-1", GroupID: "tool-call-1", TargetIndex: shellEpisodeTarget(t, raw), ModelCallID: "attempt-1", ProviderCallID: "call-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: recorded}},
	}
}

// shellEpisodeTarget is the roster target a roster of one resolves raw to:
// [1] when the request satisfies its contract, none when it does not.
func shellEpisodeTarget(t *testing.T, raw string) int {
	t.Helper()
	spec, err := catalogToolSpec(constants.MCPToolRunShellCommand)
	require.NoError(t, err)
	if decodeAgentToolRequest(spec, true, raw).Failure != nil {
		return 0
	}
	return 1
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

func TestVerifyV7EpisodeOutputs_RequiresMatchingProviderSignature(t *testing.T) {
	tests := []struct {
		name          string
		dispatchedSig string
		recordedSig   string
		wantErr       bool
	}{
		{name: "matching provider signature", dispatchedSig: "sig-dispatched", recordedSig: "sig-dispatched"},
		{name: "differing provider signature", dispatchedSig: "sig-dispatched", recordedSig: "sig-forged", wantErr: true},
		{name: "dropped provider signature", dispatchedSig: "sig-dispatched", recordedSig: "", wantErr: true},
		{name: "injected provider signature", dispatchedSig: "", recordedSig: "sig-injected", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			trace := shellEpisodeTrace(t, `{"command":"echo"}`, `{"command":"echo"}`)
			trace.ModelCalls[0].InferenceResult.GetParts()[0].GetToolCall().ProviderSignature = test.dispatchedSig
			trace.ToolCalls[0].ProviderSignature = test.recordedSig

			err := verifyV7EpisodeOutputs(trace)
			if !test.wantErr {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
		})
	}
}

func TestVerifyAgentTraceEvidence_SharedRuntimeClaimsConsensusVotes(t *testing.T) {
	for _, test := range []struct {
		name    string
		corrupt func(*AgentTrace)
		wantErr bool
	}{
		{name: "completed and failed votes in concurrent ledger order"},
		{name: "missing vote leaves an unclaimed execution", corrupt: func(trace *AgentTrace) {
			trace.ToolCalls[0].ConsensusInferences = trace.ToolCalls[0].ConsensusInferences[:1]
		}, wantErr: true},
		{name: "tampered vote signature", corrupt: func(trace *AgentTrace) { trace.ToolCalls[0].ConsensusInferences[0].Receipt.Signature = "00" }, wantErr: true},
		{name: "vote belongs to another subject", corrupt: func(trace *AgentTrace) {
			trace.ToolCalls[0].ConsensusInferences[0].SubjectTransactionHash = "another-subject"
		}, wantErr: true},
		{name: "vote result changed", corrupt: func(trace *AgentTrace) { trace.ToolCalls[0].ConsensusInferences[1].Result.Model = "changed" }, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
			trace := evidenceTrace(ledger.runtime(), ledger.runtime())
			reqTool, toolCall := prepareShellToolCall(t, trace, "turn", "call-tool", "echo")
			hash := toolCall.RequestEnvelope.GetTransactionHash()
			before := ledger.executed("before", string(constants.ActionTypeFsRead))
			m1 := sealModelTurn(t, ledger, trace, "turn", 1, reqTool)
			members := constants.TribunalConsensusMemberAppIDs()
			result := &operatorv1.InferenceResult{ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, members[0], 0)}
			digest, err := models.ComputeInferenceResultDigest(result)
			require.NoError(t, err)
			result.ResultDigest = digest
			completed := ledger.inference("vote-completed")
			completed.ResultSummary = digest
			for _, stage := range completed.DeterministicStageEvidence {
				stage.ActingAppId = members[0]
			}
			ledger.signer.Resign(t, completed)
			observation := ledger.executed("vote-observation", string(constants.ActionTypeProviderBoundaryObservation))
			failed := ledger.inferenceFailed("vote-failed")
			for _, stage := range failed.DeterministicStageEvidence {
				stage.ActingAppId = members[1]
			}
			ledger.signer.Resign(t, failed)
			toolCall = commitShellToolCall(t, ledger, toolCall, "tool")
			after := ledger.executed("after", string(constants.ActionTypeFsRead))
			trace.Roster[0].Bracket = bracketOf(before, after)
			trace.ModelCalls = []AgentModelCall{m1}
			trace.ToolCalls = []AgentToolCall{toolCall}
			// Binding order differs from the order concurrent members committed.
			trace.ToolCalls[0].ConsensusInferences = governance.WrapConsensusInferences([]*operatorv1.ConsensusVoteInference{
				{SubjectTransactionId: "tool", SubjectTransactionHash: hash, MemberAppId: members[1], ProviderAttemptId: governance.ConsensusVoteAttemptID(hash, members[1], 0), TransactionId: "vote-failed", Receipt: failed, Failure: &operatorv1.InferenceDispatchFailure{Receipt: failed}},
				{SubjectTransactionId: "tool", SubjectTransactionHash: hash, MemberAppId: members[0], ProviderAttemptId: result.ProviderAttemptId, TransactionId: "vote-completed", Receipt: completed, Result: result, ObservationReceipts: []*operatorv1.EventReceipt{observation}},
			})
			if test.corrupt != nil {
				test.corrupt(trace)
			}
			err = VerifyAgentTraceEvidence(trace)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestVerifyV7EpisodeOutputs_RosterJoinsAndPreJoinTargeting(t *testing.T) {
	catalog, err := newAgentToolCatalog()
	require.NoError(t, err)
	scope, err := catalog.Scope(ScenarioToolExpectations{
		AllowedTools: []string{constants.MCPToolOperatorDeploy, constants.MCPToolReadFile},
	})
	require.NoError(t, err)

	roster := fleetRoster()
	systemRole := operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM.String()
	deployArgs := `{"hostnames":["app-03"],"target_operators":["1"]}`
	readArgs := `{"path":"./app/config.yaml","target_operators":["3"]}`

	buildTrace := func(sameTurn bool) *AgentTrace {
		input, err := newAgentTraceInput([]*operatorv1.InferenceMessage{
			agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, "System.\n"+renderRosterPrompt(roster[:2])),
			agentTextMessage(operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, "Deploy and read."),
		}, scope.Declarations())
		require.NoError(t, err)

		if sameTurn {
			res := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
				{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: constants.MCPToolOperatorDeploy, ArgumentsJson: deployArgs}}},
				{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c2", Name: constants.MCPToolReadFile, ArgumentsJson: readArgs}}},
			}}
			return &AgentTrace{
				SchemaVersion: agentTraceSchemaVersion,
				Input:         input,
				Roster:        roster,
				RosterJoins:   []AgentTraceRosterJoin{{Index: 3, Runtime: roster[2].Runtime, JoinedAfterCall: "tc-1"}},
				ModelCalls: []AgentModelCall{{
					ProviderAttemptID: "m1", TransactionID: "tx-m1", Purpose: agentModelCallPurposeTurn, LoopTurn: 1,
					InferenceResult: &AgentTraceInferenceResult{InferenceResult: res},
				}},
				ToolCalls: []AgentToolCall{
					{CallID: "tc-1", GroupID: "tc-1", TargetIndex: 1, ModelCallID: "m1", ProviderCallID: "c1", ToolName: constants.MCPToolOperatorDeploy, TargetOperators: []string{"1"}, ArgumentsJSON: `{"hostnames":["app-03"]}`, Success: true, LoopTurn: 1},
					{CallID: "tc-2", GroupID: "tc-2", TargetIndex: 3, ModelCallID: "m1", ProviderCallID: "c2", ToolName: constants.MCPToolReadFile, TargetOperators: []string{"3"}, ArgumentsJSON: `{"path":"./app/config.yaml"}`, Success: true, LoopTurn: 1},
				},
			}
		}

		res1 := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c1", Name: constants.MCPToolOperatorDeploy, ArgumentsJson: deployArgs}}},
		}}
		res2 := &operatorv1.InferenceResult{Parts: []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "c2", Name: constants.MCPToolReadFile, ArgumentsJson: readArgs}}},
		}}
		return &AgentTrace{
			SchemaVersion: agentTraceSchemaVersion,
			Input:         input,
			Roster:        roster,
			RosterJoins:   []AgentTraceRosterJoin{{Index: 3, Runtime: roster[2].Runtime, JoinedAfterCall: "tc-1"}},
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "m1", TransactionID: "tx-m1", Purpose: agentModelCallPurposeTurn, LoopTurn: 1, InferenceResult: &AgentTraceInferenceResult{InferenceResult: res1}},
				{ProviderAttemptID: "m2", TransactionID: "tx-m2", Purpose: agentModelCallPurposeTurn, LoopTurn: 2, InferenceResult: &AgentTraceInferenceResult{InferenceResult: res2}},
			},
			ToolCalls: []AgentToolCall{
				{CallID: "tc-1", GroupID: "tc-1", TargetIndex: 1, ModelCallID: "m1", ProviderCallID: "c1", ToolName: constants.MCPToolOperatorDeploy, TargetOperators: []string{"1"}, ArgumentsJSON: `{"hostnames":["app-03"]}`, Success: true, LoopTurn: 1},
				{CallID: "tc-2", GroupID: "tc-2", TargetIndex: 3, ModelCallID: "m2", ProviderCallID: "c2", ToolName: constants.MCPToolReadFile, TargetOperators: []string{"3"}, ArgumentsJSON: `{"path":"./app/config.yaml"}`, Success: true, LoopTurn: 2},
			},
		}
	}

	t.Run("valid join followed by targeting in next turn passes", func(t *testing.T) {
		trace := buildTrace(false)
		require.NoError(t, verifyRosterPrompt(trace))
		require.NoError(t, verifyV7EpisodeOutputs(trace))
	})

	t.Run("targeting joined member in the same turn as operator_deploy fails", func(t *testing.T) {
		trace := buildTrace(true)
		require.ErrorIs(t, verifyV7EpisodeOutputs(trace), constants.ErrHarnessFanOutMismatch)
	})

	t.Run("targeting joined member in a turn before operator_deploy fails", func(t *testing.T) {
		trace := buildTrace(false)
		// Swap order so read targeting [3] happens in m1 before deploy in m2.
		trace.ModelCalls[0], trace.ModelCalls[1] = trace.ModelCalls[1], trace.ModelCalls[0]
		trace.ModelCalls[0].LoopTurn, trace.ModelCalls[1].LoopTurn = 1, 2
		trace.ToolCalls[0], trace.ToolCalls[1] = trace.ToolCalls[1], trace.ToolCalls[0]
		trace.ToolCalls[0].LoopTurn, trace.ToolCalls[1].LoopTurn = 1, 2
		require.ErrorIs(t, verifyV7EpisodeOutputs(trace), constants.ErrHarnessFanOutMismatch)
	})

	t.Run("verifyRosterPrompt requires initial roster prompt, not full post-join roster", func(t *testing.T) {
		trace := buildTrace(false)
		trace.Input.Messages[0] = AgentTraceMessage{Role: systemRole, Text: "System.\n" + renderRosterPrompt(roster)}
		require.ErrorIs(t, verifyRosterPrompt(trace), constants.ErrHarnessRosterMismatch)
	})
}

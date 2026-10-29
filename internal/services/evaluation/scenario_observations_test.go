// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govhash "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestScenarioObservationEvidenceRejectsFinalReadBeforeAgentActivity(t *testing.T) {
	for _, finalFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "final read after activity", true: "premature final read"}[finalFirst], func(t *testing.T) {
			// Exercise the receipt/ledger verifier directly, with the established
			// pre-identity fixture so this test isolates evidence chronology.
			ledger := newEvidenceLedger(t, oracletest.NewSigner(t))
			trace := evidenceTrace(ledger.runtime(), ledger.runtime())
			trace.InvestigationID = "case-1"
			before := ledger.executed("before", string(constants.ActionTypeFsRead))
			args := `{"path":"/work/result"}`
			payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolReadFile, ArgumentsJson: args, ExecutionId: "final-read"})
			require.NoError(t, err)
			envelope := &commonv1.GovernanceEnvelope{ActionType: string(constants.ActionTypeMcpCall), TargetResource: constants.MCPToolReadFile,
				Payload: payload, OperatorId: ledger.operatorID, OperatorSessionId: ledger.sessionID, InvestigationId: trace.InvestigationID, TaskId: trace.EvaluationAttemptID}
			hash, err := govhash.GenerateMessageID(envelope)
			require.NoError(t, err)
			envelope.TransactionHash, envelope.Id = hash, hash
			var final, model *operatorv1.EventReceipt
			if finalFirst {
				final = ledger.executed(hash, string(constants.ActionTypeMcpCall))
				model = ledger.inference("agent-turn")
			} else {
				model = ledger.inference("agent-turn")
				final = ledger.executed(hash, string(constants.ActionTypeMcpCall))
			}
			final.TransactionHash, final.ResultSummary = hash, `{"content":"healthy"}`
			for _, stage := range final.DeterministicStageEvidence {
				stage.TransactionHash = hash
			}
			ledger.signer.Resign(t, final)
			trace.ModelCalls = []AgentModelCall{completedModelCall("agent-turn", model)}
			trace.Observations = []AgentScenarioObservation{{Kind: scenarioObservationFile, Call: AgentToolCall{
				CallID: "final-read", ToolName: constants.MCPToolReadFile, ArgumentsJSON: args,
				TransactionID: hash, ActionType: string(constants.ActionTypeMcpCall), PolicyDecision: agentPolicyDecisionAllow,
				Success: true, ResultJSON: final.ResultSummary, RequestEnvelope: &AgentTraceEnvelope{GovernanceEnvelope: envelope}, Receipt: &AgentTraceReceipt{EventReceipt: final},
			}}}
			trace.Bracket = bracketOf(before, ledger.executed("after", string(constants.ActionTypeFsRead)))
			if finalFirst {
				require.ErrorIs(t, VerifyAgentTraceEvidence(trace), constants.ErrConformanceLedgerGap, "a signed early read cannot prove final state")
			} else {
				require.NoError(t, VerifyAgentTraceEvidence(trace))
			}
		})
	}
}

func TestAgentTraceHistoricalVersionRejectsNewToolEvidenceFields(t *testing.T) {
	t.Run("request envelope field on v8 trace", func(t *testing.T) {
		trace, _ := validEvidenceTrace(t)
		trace.SchemaVersion = agentTraceSchemaVersionV8
		trace.ToolCalls[0].RequestEnvelope = &AgentTraceEnvelope{GovernanceEnvelope: &commonv1.GovernanceEnvelope{Id: "new-field"}}
		var err error
		trace.EvidenceDigest, err = ComputeAgentTraceDigest(trace)
		require.NoError(t, err)
		require.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrEvidenceArtifactMalformed)
	})
	t.Run("ingress refusal code on v8 trace", func(t *testing.T) {
		trace, _ := validEvidenceTrace(t)
		trace.SchemaVersion = agentTraceSchemaVersionV8
		trace.ToolCalls[0].IngressRefusalCode = operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE
		var err error
		trace.EvidenceDigest, err = ComputeAgentTraceDigest(trace)
		require.NoError(t, err)
		require.ErrorIs(t, ValidateAgentTraceDigest(trace), constants.ErrEvidenceArtifactMalformed)
	})
}

func TestToolEvidenceCannotClaimSuccessWithoutAnExecutedReceipt(t *testing.T) {
	trace, _ := validEvidenceTrace(t)
	anchors, err := agentTraceAnchorSet(trace)
	require.NoError(t, err)
	_, err = verifyToolCallEvidence(anchors, trace.Data, AgentToolCall{Success: true})
	require.ErrorIs(t, err, constants.ErrEventReceiptMissing)
	denied := trace.ToolCalls[1]
	denied.Success = true
	_, err = verifyToolCallEvidence(anchors, trace.Data, denied)
	require.ErrorIs(t, err, constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestScenarioObservationRequestBindsCommandAndInvestigationToReceiptHash(t *testing.T) {
	trace := &AgentTrace{InvestigationID: "case-1", EvaluationAttemptID: "attempt-1",
		Data:      AgentTraceRuntime{OperatorID: "data-1", SessionID: "session-1"},
		Workspace: AgentTraceWorkspace{Root: "/work/case", OperatorWorkingDirectory: "/work"}}
	arguments := (models.RunShellCommandRequest{Command: "sh", Args: []string{"./check.sh"}, WorkingDir: "/work/case"}).CanonicalJSON()
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolRunShellCommand, ArgumentsJson: arguments, ExecutionId: "observation-1"})
	require.NoError(t, err)
	envelope := &commonv1.GovernanceEnvelope{ActionType: string(constants.ActionTypeMcpCall), TargetResource: constants.MCPToolRunShellCommand,
		Payload: payload, OperatorId: "data-1", OperatorSessionId: "session-1", InvestigationId: "case-1", TaskId: "attempt-1"}
	hash, err := govhash.GenerateMessageID(envelope)
	require.NoError(t, err)
	envelope.TransactionHash, envelope.Id = hash, hash
	call := AgentToolCall{CallID: "observation-1", ToolName: constants.MCPToolRunShellCommand, ArgumentsJSON: arguments, TransactionID: hash,
		RequestEnvelope: &AgentTraceEnvelope{GovernanceEnvelope: envelope}, Receipt: &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{TransactionHash: hash}}}
	require.NoError(t, verifyNativeToolCallRequest(trace, call))

	call.ArgumentsJSON = (models.RunShellCommandRequest{Command: "true", WorkingDir: "/work/case"}).CanonicalJSON()
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, call), constants.ErrEvaluationEvidenceTraceMismatch, "a successful unrelated command cannot stand in for the declared probe")
	call.ArgumentsJSON = arguments
	trace.InvestigationID = "unrelated-case"
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, call), constants.ErrEvaluationEvidenceTraceMismatch)
	trace.InvestigationID = "case-1"
	envelope.Payload = []byte("altered request")
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, call), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestScenarioObservationContractRequiresFrozenSeedsAndFinalChecks(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	for _, bp := range blueprints {
		t.Run(bp.ScenarioID, func(t *testing.T) {
			ws, err := NewScenarioWorkspace("/work", "run-1", "attempt-1")
			require.NoError(t, err)
			trace := &AgentTrace{Workspace: AgentTraceWorkspace(ws)}
			trace.Observations, err = finalScenarioObservationRequests(bp.Gold, ws)
			require.NoError(t, err)
			req := AssignmentExecutionRequest{ScenarioInput: bp.Input, ScenarioGold: bp.Gold}
			require.NoError(t, verifyScenarioObservationContract(req, trace))
			trace.Observations = trace.Observations[:len(trace.Observations)-1]
			require.ErrorIs(t, verifyScenarioObservationContract(req, trace), constants.ErrEvaluationEvidenceTraceMismatch)
		})
	}
	seed := recoveryErrorGuidedRetry()
	require.ErrorIs(t, verifyScenarioObservationContract(AssignmentExecutionRequest{ScenarioInput: seed.Input}, &AgentTrace{}), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestAgentRunCaseMemoryPath_IsInsideSeededWorkspace(t *testing.T) {
	run := &agentRun{workspace: ScenarioWorkspace{Root: "/work/ws/"}}
	got, err := run.caseMemoryPath()
	require.NoError(t, err)
	require.Equal(t, "/work/ws/"+scenarioCaseMemoryFile, got)
}

// A model's file edit is verified as the typed FILE_EDIT the executor built
// from its recorded arguments and the trace's own Operator session. A payload
// bound to another session, a changed edit, or an MCP_CALL wrapper naming the
// tool are all evidence mismatches.
func TestGovernedToolRequestBindsTypedActionAndSession(t *testing.T) {
	trace := &AgentTrace{InvestigationID: "case-1", EvaluationAttemptID: "attempt-1",
		Data: AgentTraceRuntime{OperatorID: "data-1", SessionID: "session-1"}}
	arguments := `{"content":"tls_verify: true\n","file_path":"/work/case/config.yaml","justification":"restore TLS verification","operation":"write"}`
	tool, ok, err := mcp.LookupGovernedActionTool(constants.MCPToolFileEdit)
	require.NoError(t, err)
	require.True(t, ok)
	signed := func(t *testing.T, action constants.ActionType, target string, payload []byte) AgentToolCall {
		t.Helper()
		envelope := &commonv1.GovernanceEnvelope{ActionType: string(action), TargetResource: target, Payload: payload,
			OperatorId: "data-1", OperatorSessionId: "session-1", InvestigationId: "case-1", TaskId: "attempt-1"}
		hash, err := govhash.GenerateMessageID(envelope)
		require.NoError(t, err)
		envelope.TransactionHash, envelope.Id = hash, hash
		return AgentToolCall{CallID: "edit-1", ToolName: constants.MCPToolFileEdit, ArgumentsJSON: arguments, TransactionID: hash,
			RequestEnvelope: &AgentTraceEnvelope{GovernanceEnvelope: envelope}, Receipt: &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{TransactionHash: hash}}}
	}

	request, payload, err := governedToolAction(tool, arguments, "session-1", "edit-1")
	require.NoError(t, err)
	require.NoError(t, verifyNativeToolCallRequest(trace, signed(t, request.ActionType, request.TargetResource, payload)))

	restore, ok, err := mcp.LookupGovernedActionTool(constants.MCPToolRestoreFile)
	require.NoError(t, err)
	require.True(t, ok)
	restoreArgs := `{"commit_hash":"abc123","file_path":"/work/case/config.yaml"}`
	restoreRequest, ownLedger, err := governedToolAction(restore, restoreArgs, "session-1", "edit-1")
	require.NoError(t, err)
	restoreCall := signed(t, restoreRequest.ActionType, restoreRequest.TargetResource, ownLedger)
	restoreCall.ToolName, restoreCall.ArgumentsJSON = constants.MCPToolRestoreFile, restoreArgs
	require.NoError(t, verifyNativeToolCallRequest(trace, restoreCall))
	_, otherLedger, err := governedToolAction(restore, restoreArgs, "session-2", "edit-1")
	require.NoError(t, err)
	restoreCall = signed(t, restoreRequest.ActionType, restoreRequest.TargetResource, otherLedger)
	restoreCall.ToolName, restoreCall.ArgumentsJSON = constants.MCPToolRestoreFile, restoreArgs
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, restoreCall), constants.ErrEvaluationEvidenceTraceMismatch,
		"a restore from another session's ledger is not this attempt's restore")

	changed := signed(t, request.ActionType, request.TargetResource, payload)
	changed.ArgumentsJSON = `{"content":"tls_verify: false\n","file_path":"/work/case/config.yaml","justification":"restore TLS verification","operation":"write"}`
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, changed), constants.ErrEvaluationEvidenceTraceMismatch, "the signed edit must be the recorded edit")

	wrapped, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolFileEdit, ArgumentsJson: arguments, ExecutionId: "edit-1"})
	require.NoError(t, err)
	require.ErrorIs(t, verifyNativeToolCallRequest(trace, signed(t, constants.ActionTypeMcpCall, constants.MCPToolFileEdit, wrapped)), constants.ErrEvaluationEvidenceTraceMismatch,
		"an MCP_CALL wrapper skips the FILE_EDIT checks and is never a model's edit")
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the Tribunal runs through the production Gateway, Tribunal
// service, and Inference Operator. Only the model provider is scripted.
package evaluation_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	tribunalRealTool       = constants.MCPToolRunShellCommand
	tribunalRealRequest    = `{"command":"echo","args":["HEALTHY"]}`
	tribunalRealGuidelines = "read-only; no writes outside the workspace"
	tribunalRealCommand    = `{"command":"echo","args":["HEALTHY"]}`
	tribunalRealLowRisk    = `{"risk": "LOW", "rationale": "read-only file read", "feedback": ""}`
	tribunalRealHighRisk   = `{"risk": "HIGH", "rationale": "deletes the workspace", "feedback": "read the file instead"}`
	tribunalRealApprove    = `{"verdict": "APPROVE", "swap_to_cluster": "", "rationale": "correct"}`
)

// tribunalTurn is one scripted provider reply with plain text.
func tribunalTurn(text string) []fixtures.OllamaReply {
	return []fixtures.OllamaReply{{Content: text}}
}

// tribunalIntent is one primary turn that asks for a command by intent.
func tribunalIntent(callID string, proposed ...string) []fixtures.OllamaReply {
	arguments := tribunalRealRequest
	if len(proposed) > 0 {
		arguments = proposed[0]
	}
	return []fixtures.OllamaReply{{ToolCalls: []fixtures.OllamaToolCall{{ID: callID, Name: tribunalRealTool, Arguments: json.RawMessage(arguments)}}}}
}

// tribunalPanel is one generation round: each member's answer in canonical
// member order.
func tribunalPanel(commands ...string) []fixtures.OllamaReply {
	replies := make([]fixtures.OllamaReply, 0, len(commands))
	for _, command := range commands {
		replies = append(replies, fixtures.OllamaReply{Content: command})
	}
	return replies
}

func tribunalUnanimous(command string) []fixtures.OllamaReply {
	return tribunalPanel(command, command, command, command, command)
}

// tribunalScenario scopes the scenario to the native run_shell_command tool routed through Tribunal authoring.
func tribunalScenario(scenario *evalv1.EvaluationScenarioDefinition) {
	scenario.AllowedTools = []string{tribunalRealTool}
	scenario.ForbiddenTools = nil
	scenario.TribunalCommands = true
	scenario.ExpectedTools = nil
}

// tribunalRun initializes a one-assignment campaign on the real stack whose
// provider script is the concatenation of the given rounds, in order.
func tribunalRun(t *testing.T, suffix string, rounds ...[]fixtures.OllamaReply) *realStackAgentRun {
	return tribunalRunWithPosture(t, suffix, constants.PostureDoctrine, false, rounds...)
}

func tribunalRunWithPosture(t *testing.T, suffix string, posture constants.Posture, deny bool, rounds ...[]fixtures.OllamaReply) *realStackAgentRun {
	t.Helper()
	var replies []fixtures.OllamaReply
	for _, round := range rounds {
		replies = append(replies, round...)
	}
	return newRealStackAgentRun(t, realStackOptions{
		posture: posture, singleRole: true, suffix: suffix, replies: replies,
		scenario: tribunalScenario, denyHumanApprovals: deny,
	})
}

// tribunalWireModels lists the model each /api/chat body named, in arrival order.
func tribunalWireModels(t *testing.T, env *realStackAgentRun) []string {
	t.Helper()
	var models []string
	for _, raw := range env.stack.Provider.ChatRequests() {
		var body struct {
			Model string `json:"model"`
		}
		require.NoError(t, json.Unmarshal(raw, &body))
		models = append(models, body.Model)
	}
	return models
}

// tribunalLocalReceipts reads the Data Operator's authoritative local receipt
// list for this investigation, excluding the list query itself.
func tribunalLocalReceipts(t *testing.T, env *realStackAgentRun, investigationID string) []*operatorv1.ActionReceipt {
	t.Helper()
	args, err := json.Marshal(mcp.AuditReceiptListRequest{
		OperatorSessionID: env.dataSessionID,
		InvestigationID:   investigationID,
		ExcludeAuditReads: true,
		Limit:             100,
	})
	require.NoError(t, err)
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{
		ToolName:      constants.MCPToolAuditReceiptList,
		ArgumentsJson: string(args),
	})
	require.NoError(t, err)
	out, err := evaluation.NewCommandLane(env.gateway, env.persona).DispatchAgentTool(
		t.Context(), evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID},
		"tribunal-ledger-read", investigationID, "tribunal-ledger-read",
		constants.ActionTypeMcpCall, constants.MCPToolAuditReceiptList, payload,
	)
	require.NoError(t, err)
	queryReceipt := out.Outcome.Observation.GetReceipt()
	require.NotNil(t, queryReceipt)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, queryReceipt.GetStatus())
	var result models.OperatorAuditReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(queryReceipt.GetResultSummary()), &result))
	assert.Equal(t, env.dataOperatorID, result.OperatorID)
	assert.Equal(t, "operator_local_ledger", result.Authority)
	assert.Equal(t, investigationID, result.InvestigationID)
	assert.Equal(t, len(result.Receipts), result.Count)
	return result.Receipts
}

func tribunalVerifyLocalReceipt(t *testing.T, env *realStackAgentRun, receipt *operatorv1.ActionReceipt) *models.ActionReceiptRecord {
	t.Helper()
	operator, err := env.stack.Gateway.Service.GetRegistrationService().GetOperator(t.Context(), env.dataOperatorID)
	require.NoError(t, err)
	publicKey, err := models.DecodeActuatorPublicKey(operator.GetActuatorPublicKey())
	require.NoError(t, err)
	require.NoError(t, governance.VerifyActionReceiptSignature(receipt, publicKey))
	require.NoError(t, governance.VerifyReceiptPersistenceAttestation(receipt, publicKey))
	files, err := fs.NewRuntimeFileService(env.stack.DataHost.WorkingDir, testutil.NewTestLogger())
	require.NoError(t, err)
	local, err := storage.ReadLocalActionReceipt(files, testutil.NewTestLogger(), keystore.Options{Keyring: env.stack.DataHost.Keyring}, receipt.GetTransactionId())
	require.NoError(t, err)
	require.NotNil(t, local)
	assert.True(t, proto.Equal(receipt, local.ActionReceipt), "receipt is present in the Data Operator's authoritative local store")
	return local
}

func tribunalExpectedApprovedArguments(trace *evaluation.AgentTrace) string {
	return (models.RunShellCommandRequest{
		Command: "echo", Args: []string{"HEALTHY"},
		Timeout:    constants.DefaultShellCommandTimeout,
		WorkingDir: trace.Workspace.OperatorWorkingDirectory,
	}).CanonicalJSON()
}

func tribunalCaptureMCPCall(t *testing.T, env *realStackAgentRun) <-chan *operatorv1.McpCallRequested {
	t.Helper()
	captured := make(chan *operatorv1.McpCallRequested, 1)
	channel := pubsub.CmdChannel(env.dataOperatorID, env.dataSessionID)
	unregister := env.stack.Gateway.Service.GetGatewayWebSocketHandler().RegisterHandler(channel, func(_ string, data []byte) {
		envelope := &commonv1.GovernanceEnvelope{}
		if err := protojson.Unmarshal(data, envelope); err != nil || envelope.GetActionType() != string(constants.ActionTypeMcpCall) {
			return
		}
		request := &operatorv1.McpCallRequested{}
		if err := proto.Unmarshal(envelope.GetPayload(), request); err == nil {
			select {
			case captured <- request:
			default:
			}
		}
	})
	t.Cleanup(unregister)
	return captured
}

func TestCampaignTribunalIntegration_DeclaresNativeShellTool(t *testing.T) {
	env := tribunalRun(t, "declares", tribunalTurn("HEALTHY"))

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 1)
	require.Len(t, bodies[0].Tools, 1)
	assert.Equal(t, tribunalRealTool, bodies[0].Tools[0].Function.Name)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	require.NoError(t, json.Unmarshal(bodies[0].Tools[0].Function.Parameters, &schema))
	assert.Contains(t, schema.Properties, "command")
	assert.Contains(t, schema.Properties, "args")
	assert.NotContains(t, schema.Properties, "request")
	assert.NotContains(t, schema.Properties, "guidelines")
}

func TestCampaignTribunalIntegration_TribunalCommandsWithoutRunCommandsFailsClosed(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "tribunal-no-run", replies: tribunalTurn("unused"),
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{"read_file"}
			scenario.ForbiddenTools = nil
			scenario.TribunalCommands = true
			scenario.ExpectedTools = nil
		},
	})

	_, _, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)
	assert.Zero(t, env.stack.Provider.ChatCount(), "no model call before the tool surface resolves")
}

func TestCampaignTribunalIntegration_ApprovedCommandDispatchesWithGovernedDeliberation(t *testing.T) {
	env := tribunalRun(t, "approved",
		tribunalIntent("call-1"),
		tribunalUnanimous(tribunalRealCommand),
		tribunalTurn(tribunalRealLowRisk),
		tribunalTurn(tribunalRealApprove),
		tribunalTurn("HEALTHY"),
	)
	dispatchedCalls := tribunalCaptureMCPCall(t, env)

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	assert.Empty(t, res.GetInvalidEvidenceDetail())
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 9)
	for _, body := range bodies[1:8] {
		assert.Empty(t, body.Tools, "a Tribunal call declares no tools")
	}

	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	tool := trace.ToolCalls[0]
	assert.True(t, tool.Success, "tool error: %s", tool.Error)
	assert.Equal(t, tribunalRealRequest, tool.ArgumentsJSON)
	approved := tribunalExpectedApprovedArguments(trace)
	assert.Equal(t, approved, tool.ApprovedArgumentsJSON, "Gateway-owned timeout and working directory are composed before review")
	require.NotNil(t, tool.Receipt, "the dispatched command keeps its signed receipt")
	assert.Equal(t, tool.TransactionID, tool.Receipt.GetTransactionId())
	assert.Equal(t, string(constants.ActionTypeMcpCall), tool.ActionType)
	assert.JSONEq(t, tribunalRealRequest, tool.ArgumentsJSON, "the trace records the native proposal")
	var dispatched *operatorv1.McpCallRequested
	select {
	case dispatched = <-dispatchedCalls:
	case <-t.Context().Done():
		t.Fatal("Gateway did not dispatch the approved MCP_CALL")
	}
	assert.Equal(t, tribunalRealTool, dispatched.GetToolName())
	assert.Equal(t, tool.CallID, dispatched.GetExecutionId())
	assert.Equal(t, tool.ApprovedArgumentsJSON, dispatched.GetArgumentsJson(), "the exact approved bytes reach the Operator")

	localReceipts := tribunalLocalReceipts(t, env, trace.InvestigationID)
	var shellReceipts []*operatorv1.ActionReceipt
	for _, receipt := range localReceipts {
		if receipt.GetActionType() == string(constants.ActionTypeMcpCall) {
			shellReceipts = append(shellReceipts, receipt)
		}
		assert.NotEqual(t, string(constants.ActionTypeExecuteBash), receipt.GetActionType(), "the Operator ledger contains no EXECUTE_BASH receipt")
	}
	require.Len(t, shellReceipts, 1, "one Tribunal intent executes one native MCP_CALL")
	local := tribunalVerifyLocalReceipt(t, env, shellReceipts[0])
	assert.Equal(t, tool.TransactionID, local.TransactionID)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, local.Status)
	assert.Equal(t, string(constants.ActionTypeMcpCall), string(local.ActionType))
	var shellResult mcp.RunShellCommandResult
	require.NoError(t, json.Unmarshal([]byte(local.ResultSummary), &shellResult), "the signed local receipt keeps the native result shape")
	assert.Equal(t, 0, shellResult.ExitCode)
	assert.Equal(t, "HEALTHY\n", shellResult.Stdout)
	assert.Empty(t, shellResult.Stderr)
	assert.Empty(t, shellResult.Error)

	// Each member call's attempt id is the intent's prefix numbered in
	// deliberation order.
	require.Len(t, trace.ModelCalls, 9)
	assert.True(t, trace.ModelCalls[0].IsTurn())
	assert.True(t, trace.ModelCalls[8].IsTurn())
	wantPersonas := []string{"axiom", "concord", "variance", "pragma", "nemesis", tribunal.PersonaMarshal, tribunal.PersonaAuditor}
	wantPurposes := []string{"tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_marshal", "tribunal_audit"}
	prefix := trace.ModelCalls[1].ProviderAttemptID
	require.NotEmpty(t, prefix)
	prefix = prefix[:len(prefix)-len("-1")]
	for i, call := range trace.ModelCalls[1:8] {
		assert.False(t, call.IsTurn())
		assert.Equal(t, wantPurposes[i], call.Purpose)
		assert.Equal(t, wantPersonas[i], call.Persona)
		assert.Equal(t, tool.CallID, call.ToolCallID, "every Tribunal call hangs off its tool call")
		assert.Equal(t, 1, call.LoopTurn)
		assert.Empty(t, call.ToolsDeclared)
		assert.Equal(t, fmt.Sprintf("%s-%d", prefix, i+1), call.ProviderAttemptID, "each member call is numbered in deliberation order")
		require.NotNil(t, call.Receipt, "every Tribunal call keeps its signed receipt")
		assert.True(t, call.Completed())
	}
	assert.Contains(t, trace.ModelCalls[1].Text, `"command":"echo"`)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace), "Tribunal commitments are claimed in ledger order")
	require.NoError(t, evaluation.ValidateAgentTraceDigest(trace))

	require.Len(t, trace.Steps, 8)
	wantRoles := []evalv1.AgentRole{
		evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR,
		evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR,
		evalv1.AgentRole_AGENT_ROLE_CANDIDATE_GENERATOR, evalv1.AgentRole_AGENT_ROLE_CANDIDATE_VOTER,
		evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST, evalv1.AgentRole_AGENT_ROLE_REVIEWER,
	}
	for i, step := range trace.Steps {
		assert.Equal(t, uint32(i+1), step.GetIndex())
		assert.Equal(t, wantRoles[i], step.GetRole())
		assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, step.GetResolvedTier())
		assert.Equal(t, realStackModelTag, step.GetModelTag())
	}
	vote := trace.Steps[5].GetVote()
	require.NotNil(t, vote)
	assert.True(t, vote.GetConsensusReached())
assert.JSONEq(t, tribunalExpectedApprovedArguments(trace), vote.GetSelectedCommand())
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, trace.Steps[7].GetReview().GetVerdict())

	conversation, err := trace.Conversation()
	require.NoError(t, err)
	for _, message := range conversation {
		assert.NotContains(t, message.Text, "Tribunal", "Tribunal calls are not conversation turns")
	}

	actions := res.GetGovernedActions()
	require.Len(t, actions, 10, "the turn, its tool call's Tribunal inferences, the tool call, then the next turn")
	assert.Equal(t, trace.ModelCalls[0].TransactionID, actions[0].GetTransactionId())
	for i := 1; i <= 7; i++ {
		assert.Equal(t, trace.ModelCalls[i].TransactionID, actions[i].GetTransactionId())
		assert.Equal(t, string(constants.ActionTypeInference), actions[i].GetActionType())
	}
	assert.Equal(t, tool.TransactionID, actions[8].GetTransactionId())
	assert.Equal(t, trace.ModelCalls[8].TransactionID, actions[9].GetTransactionId())

	inferences := res.GetModelInferences()
	require.Len(t, inferences, 9)
	assert.Equal(t, "axiom", inferences[1].GetAgentPersona())
	assert.Equal(t, "tribunal:generate", inferences[1].GetCallSite())
	assert.Equal(t, "tribunal:marshal", inferences[6].GetCallSite())
	assert.Equal(t, "tribunal:audit", inferences[7].GetCallSite())
}

func TestCampaignTribunalIntegration_L1ScreensApprovedTypedArguments(t *testing.T) {
	const dangerous = `{"command":"rm","args":["-rf","/"]}`
	env := tribunalRun(t, "l1-doctrine",
		tribunalIntent("call-1", dangerous),
		tribunalUnanimous(dangerous),
		tribunalTurn(tribunalRealLowRisk),
		tribunalTurn(tribunalRealApprove),
		tribunalTurn("The destructive command was blocked by policy."),
	)

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Contains(t, call.Error, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE.String())
	assert.Empty(t, call.TransactionID, "L1 refuses the typed call before an envelope reaches the Operator")
	assert.Nil(t, call.Receipt)
	assert.Empty(t, call.PolicyDecision)
	assert.Empty(t, trace.ToolCalls[0].ApprovedArgumentsJSON, "a refused call has no dispatched approved bytes")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

func TestCampaignTribunalIntegration_RatifyAndNotaryApprovalAndDenial(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture)+"-approval", func(t *testing.T) {
			env := tribunalRunWithPosture(t, "l3-approve-"+string(posture), posture, false,
				tribunalIntent("call-1"), tribunalUnanimous(tribunalRealCommand),
				tribunalTurn(tribunalRealLowRisk), tribunalTurn(tribunalRealApprove),
				tribunalTurn("HEALTHY"),
			)
			res := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, res)
			require.Len(t, trace.ToolCalls, 1)
			assert.True(t, trace.ToolCalls[0].Success)
			assert.Equal(t, string(constants.ActionTypeMcpCall), trace.ToolCalls[0].ActionType)
			assert.Equal(t, tribunalExpectedApprovedArguments(trace), trace.ToolCalls[0].ApprovedArgumentsJSON)
			assert.Equal(t, "HEALTHY", trace.FinalOutput)
			assert.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
		})

		t.Run(string(posture)+"-denial", func(t *testing.T) {
			env := tribunalRunWithPosture(t, "l3-deny-"+string(posture), posture, true,
				tribunalIntent("call-1"), tribunalUnanimous(tribunalRealCommand),
				tribunalTurn(tribunalRealLowRisk), tribunalTurn(tribunalRealApprove),
				tribunalTurn("The owner denied the command."),
			)
			res := env.executeNext(t)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, res)
			require.Len(t, trace.ToolCalls, 1)
			call := trace.ToolCalls[0]
			assert.False(t, call.Success)
			assert.Equal(t, evaluation.ExportedAgentPolicyDecisionDeny, call.PolicyDecision)
			require.NotNil(t, call.Receipt)
			assert.True(t, governance.IsGovernanceRejectionReceipt(call.Receipt.ActionReceipt))
			assert.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
		})
	}
}

func TestCampaignTribunalIntegration_MarshalBlockFailsCallAndSecondBlockIsConflict(t *testing.T) {
	const destructive = "rm -rf ."
	env := tribunalRun(t, "marshal-block",
		tribunalIntent("call-1"),
		tribunalUnanimous(destructive),
		tribunalTurn(tribunalRealHighRisk),
		tribunalIntent("call-2"),
		tribunalUnanimous(destructive),
		tribunalTurn(tribunalRealHighRisk),
		tribunalTurn("I could not read the file safely."),
	)

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "a blocked command is a scored answer, not a harness failure")
	assert.Empty(t, res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 2)
	first, second := trace.ToolCalls[0], trace.ToolCalls[1]
	assert.False(t, first.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorPipeline, first.ErrorType)
	assert.Equal(t, "MARSHAL BLOCKED: deletes the workspace (feedback: read the file instead)", first.Error)
	assert.False(t, second.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorPipeline, second.ErrorType)
	assert.Equal(t, evaluation.ExportedTribunalMarshalConflict, second.Error)
	for _, call := range []evaluation.AgentToolCall{first, second} {
		assert.Empty(t, call.TransactionID, "a blocked command never dispatches")
		assert.Nil(t, call.Receipt, "a blocked command never dispatches, so there is no receipt")
	}
	assert.Equal(t, evaluation.AgentTerminalClassificationPipelineOutcome, trace.TerminalClassification)
	require.Len(t, trace.Steps, 14, "two deliberations of five candidates, a vote, and a risk step each")
	risk := trace.Steps[6].GetRisk()
	require.NotNil(t, risk)
	assert.True(t, risk.GetBlocked())
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	_, err := trace.Conversation()
	require.NoError(t, err)
}

func TestCampaignTribunalIntegration_NoConsensusFailsCallWithoutDispatch(t *testing.T) {
	env := tribunalRun(t, "no-consensus",
		tribunalIntent("call-1"),
		tribunalPanel("cat a", "cat b", "cat c", "cat d", "cat e"),
		tribunalPanel("cat f", "cat g", "cat h", "cat i", "cat j"),
		tribunalTurn("No command was agreed."),
	)

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, evaluation.ExportedAgentToolErrorExecution, trace.ToolCalls[0].ErrorType)
	assert.Equal(t, evaluation.ExportedTribunalNoConsensusError, trace.ToolCalls[0].Error)
	assert.Empty(t, trace.ToolCalls[0].TransactionID, "no command was agreed, so nothing dispatches")
	require.Len(t, trace.ModelCalls, 12, "two turns and two generation rounds")
	require.Len(t, trace.Steps, 11, "two rounds of candidates and the deciding vote")
	vote := trace.Steps[10].GetVote()
	require.NotNil(t, vote)
	assert.False(t, vote.GetConsensusReached())
	assert.Equal(t, uint32(2), vote.GetRounds())
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

func TestCampaignTribunalIntegration_AuditFailureFailsCallWithoutDispatch(t *testing.T) {
	const unknownVerdict = `{"verdict": "MAYBE", "swap_to_cluster": "", "revised_arguments": null, "rationale": "unsure"}`
	env := tribunalRun(t, "audit-failure",
		tribunalIntent("call-1"),
		tribunalUnanimous(tribunalRealCommand),
		tribunalTurn(tribunalRealLowRisk),
		tribunalTurn(unknownVerdict),
		tribunalTurn(unknownVerdict),
		tribunalTurn("I could not get a review of that command."),
	)

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "an audit that fails closed is a scored answer, not a harness failure")
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	tool := trace.ToolCalls[0]
	assert.False(t, tool.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorExecution, tool.ErrorType)
	assert.Equal(t, fmt.Sprintf(evaluation.ExportedTribunalAuditFailedError, `unknown verdict "MAYBE"`), tool.Error)
	assert.Empty(t, tool.TransactionID, "an unreviewed command never dispatches")
	require.Len(t, trace.ModelCalls, 10, "the turn, five candidates, the risk check, two Auditor answers, and the closing turn")
	assert.Equal(t, "tribunal_audit", trace.ModelCalls[7].Purpose)
	assert.Equal(t, "tribunal_audit", trace.ModelCalls[8].Purpose)
	require.Len(t, trace.Steps, 8, "five candidates, the vote, the risk check, and the review")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

func TestCampaignTribunalIntegration_TribunalProviderFailureEndsLoopAndVerifies(t *testing.T) {
	env := tribunalRun(t, "tribunal-provider-fail",
		tribunalIntent("call-1"),
		tribunalTurn(tribunalRealCommand),
		[]fixtures.OllamaReply{{Failure: "provider unreachable"}},
	)

	res := env.executeNext(t)

	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus(), "a provider failure is a scored outcome, not a harness failure")
	assert.Empty(t, res.GetInvalidEvidenceDetail())
	require.Len(t, env.stack.Provider.ChatRequests(), 3, "the deliberation stops at the failed call")
	trace := executorLoadTrace(t, env, res)
	assert.NotEmpty(t, trace.ProviderFailure)
	assert.Equal(t, string(inference.AttemptFailureGenerateFailed), trace.ProviderFailureClass, "the member's typed failure code classifies the provider failure")
	assert.Equal(t, evaluation.AgentTerminalClassificationHarnessFailure, trace.TerminalClassification)
	require.Len(t, trace.ModelCalls, 3, "the failed Tribunal call is recorded because its runtime committed it")
	failed := trace.ModelCalls[2]
	assert.Equal(t, "tribunal_generate", failed.Purpose)
	assert.False(t, failed.Completed())
	require.NotNil(t, failed.Receipt)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.Receipt.GetStatus())
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, fmt.Sprintf(evaluation.ExportedTribunalInferenceFailedMsg, trace.ProviderFailure), trace.ToolCalls[0].Error)
	assert.Empty(t, trace.Steps, "an interrupted deliberation records no steps")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	_, err := trace.Conversation()
	require.NoError(t, err, "the interrupted turn ends the conversation")
}

// tribunalFormationVariants is the formation catalog's variant registry. Each
// variant declares the digest the stack's Provenance Operator attests for its
// tag when the run serves evaluation.FormationCatalogServedTags().
func tribunalFormationVariants(t *testing.T) []*evalv1.ModelVariant {
	t.Helper()
	return evaluation.FormationCatalogFixtureVariants(func(tag string) string {
		return fixtures.ScriptedModelDigest(t, tag)
	})
}

func tribunalVariantByID(t *testing.T, variants []*evalv1.ModelVariant, id string) *evalv1.ModelVariant {
	t.Helper()
	for _, variant := range variants {
		if variant.GetVariantId() == id {
			return variant
		}
	}
	t.Fatalf("formation variant %q not found", id)
	return nil
}

// The system lane runs the primary turn on the stack's primary model and the
// Tribunal on the assistant tier. The member calls go to the assistant tag, and
// the second member fails, so the deliberation stops with one answer recorded.
func TestCampaignTribunalIntegration_SystemLaneRoutesTurnAndTribunalToFrozenRoles(t *testing.T) {
	variants := tribunalFormationVariants(t)
	set, err := evaluation.GenerateFormationCatalogStackSet(evaluation.FormationCatalogStackGenerationRequest{
		CampaignID: realStackCampaignID, Variants: variants, FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	require.Len(t, set.Stacks, 1)
	stack := set.Stacks[0]
	primary := tribunalVariantByID(t, variants, stack.GetPrimarySlot().GetVariantId())
	assistant := tribunalVariantByID(t, variants, stack.GetAssistantSlot().GetVariantId())

	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "system-lane",
		replies: append(append(tribunalIntent("call-1"), tribunalTurn("assistant tribunal answer")...),
			fixtures.OllamaReply{Failure: "provider unreachable"}),
		scenario:  tribunalScenario,
		modelTags: evaluation.FormationCatalogServedTags(),
		request: func(req *evaluation.CampaignInitRequest, _ string) {
			inventory, err := evaluation.MaterializeModelRegistry(realStackCampaignID, variants)
			require.NoError(t, err)
			req.Inventory = inventory
			req.FormationStackSet = set
			req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
		},
	})

	res := env.executeNext(t)

	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus())
	assert.Equal(t, []string{primary.GetServedModelTag(), assistant.GetServedModelTag(), assistant.GetServedModelTag()}, tribunalWireModels(t, env),
		"the turn runs on the primary tag; every Tribunal member runs on the assistant tag")
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ModelCalls, 3)
	assert.Equal(t, primary.GetVariantId(), trace.ModelCalls[0].ModelVariantID)
	assert.Equal(t, primary.GetServedModelTag(), trace.ModelCalls[0].ServedModelTag)
	assert.Equal(t, "MODEL_ROLE_PRIMARY", trace.ModelCalls[0].ModelRole)
	assert.Equal(t, assistant.GetVariantId(), trace.ModelCalls[1].ModelVariantID)
	assert.Equal(t, assistant.GetServedModelTag(), trace.ModelCalls[1].ServedModelTag)
	assert.Equal(t, assistant.GetModelDigest(), trace.ModelCalls[1].ModelDigest)
	assert.Equal(t, "MODEL_ROLE_ASSISTANT", trace.ModelCalls[1].ModelRole)
	assert.Equal(t, "assistant tribunal answer", trace.ModelCalls[1].Text)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

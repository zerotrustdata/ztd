// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the executor runs on the real eval stack through the rig in
// agent_evidence_real_stack_integration_test.go. Only the model provider is
// scripted; the Gateway, both Operators, the ledgers and the sentinel are real.
package evaluation_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/protocol"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// executorChatBody is the subset of one /api/chat body the assertions read.
type executorChatBody struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name       string          `json:"name"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

// executorChatBodies decodes every /api/chat body the Inference Operator sent,
// in arrival order.
func executorChatBodies(t *testing.T, env *realStackAgentRun) []executorChatBody {
	t.Helper()
	var bodies []executorChatBody
	for _, raw := range env.stack.Provider.ChatRequests() {
		var body executorChatBody
		require.NoError(t, json.Unmarshal(raw, &body))
		bodies = append(bodies, body)
	}
	return bodies
}

// executorToolNames lists the declared tool names of one chat body, in order.
func executorToolNames(body executorChatBody) []string {
	var names []string
	for _, tool := range body.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

// executorTraceToolNames lists the tool names of a sealed trace's input.
func executorTraceToolNames(tools []evaluation.AgentTraceTool) []string {
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// executorScrubbedToken is the placeholder the Inference boundary substitutes for
// a sensitive value (the random operator directory) before the provider sees it.
var executorScrubbedToken = regexp.MustCompile(`\{\{UEI_\d+\}\}`)

// executorAssertDispatchedText asserts that the trace's dispatched text equals the
// provider's copy of it, where each scrub token on the wire stands for the value
// the executor dispatched. Every other byte must match exactly.
func executorAssertDispatchedText(t *testing.T, wire, dispatched string) {
	t.Helper()
	var pattern strings.Builder
	pattern.WriteString("(?s)^")
	last := 0
	for _, loc := range executorScrubbedToken.FindAllStringIndex(wire, -1) {
		pattern.WriteString(regexp.QuoteMeta(wire[last:loc[0]]))
		pattern.WriteString("(.+?)")
		last = loc[1]
	}
	pattern.WriteString(regexp.QuoteMeta(wire[last:]))
	pattern.WriteString("$")
	assert.Regexp(t, pattern.String(), dispatched)
}

// executorLoadTrace loads the sealed trace the executor persisted for res.
func executorLoadTrace(t *testing.T, env *realStackAgentRun, res *evalv1.EvaluationAssignmentResult) *evaluation.AgentTrace {
	t.Helper()
	trace, err := env.store.LoadAssignmentAgentTrace(t.Context(), env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	return trace
}

// executorStopDataOperator kills the Data Operator from inside a provider reply,
// so the governed dispatch or sentinel read that follows reaches a dead runtime.
func executorStopDataOperator(t *testing.T, env *realStackAgentRun) {
	t.Helper()
	if err := env.stack.Data.Stop(t.Context()); err != nil {
		t.Errorf("stop data operator: %v", err)
	}
}

// executorForeignInference dispatches one governed inference on the Inference
// runtime as the eval app, outside the assignment's trace.
func executorForeignInference(t *testing.T, env *realStackAgentRun) {
	t.Helper()
	workload, err := env.stack.Inference.Workload()
	if err != nil {
		t.Errorf("inference workload: %v", err)
		return
	}
	req := &operatorv1.InferenceDispatchRequest{
		RequestSchemaVersion: constants.InferenceRequestSchemaVersion,
		Role:                 operatorv1.ModelRole_MODEL_ROLE_PRIMARY,
		Model:                realStackModelTag,
		Messages: []*operatorv1.InferenceMessage{{
			Role:  operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER,
			Parts: []*operatorv1.InferenceMessagePart{{Part: &operatorv1.InferenceMessagePart_Text{Text: "foreign probe"}}},
		}},
		ProviderAttemptId:       "foreign-attempt-1",
		TargetOperatorSessionId: workload.OperatorSessionID,
		ActingAppId:             protocol.NewWorkloadIdentity().AppSPIFFEID(constants.EvaluationAppName),
	}
	if _, _, err := env.appClient.DispatchInference(t.Context(), req); err != nil {
		t.Errorf("dispatch foreign inference: %v", err)
	}
}

// executorScenario returns the kept scenario of env's catalog, so a test can
// assert the frozen scenario fields the executor resolved its tools from.
func executorScenario(t *testing.T, env *realStackAgentRun, scenarioID string) *evalv1.EvaluationScenarioDefinition {
	t.Helper()
	for _, scenario := range env.req.Catalog.GetScenarios() {
		if scenario.GetScenarioId() == scenarioID {
			return scenario
		}
	}
	t.Fatalf("scenario %s is not in the frozen catalog", scenarioID)
	return nil
}

// executorImportRequest rebuilds the assignment request the controller executed
// for assignmentID, from the run's frozen inputs, so a sealed trace can be
// imported again through ImportAssignmentResultFromAgentTrace.
func executorImportRequest(t *testing.T, env *realStackAgentRun, assignmentID string, trace *evaluation.AgentTrace) evaluation.AssignmentExecutionRequest {
	t.Helper()
	assignments, err := env.store.ListAssignments(t.Context(), env.req.RunID)
	require.NoError(t, err)
	var assignment *evalv1.EvaluationAssignment
	for _, candidate := range assignments {
		if candidate.GetAssignmentId() == assignmentID {
			assignment = candidate
		}
	}
	require.NotNil(t, assignment, "assignment %s is stored for the run", assignmentID)
	scenario := executorScenario(t, env, assignment.GetScenarioId())
	artifact := env.req.ScenarioArtifacts[assignment.GetScenarioId()]
	var input evaluation.ScenarioInputFixture
	require.NoError(t, json.Unmarshal(artifact.Input.Body, &input))
	var gold evaluation.ScenarioGoldCriteria
	require.NoError(t, json.Unmarshal(artifact.Gold.Body, &gold))
	return evaluation.AssignmentExecutionRequest{
		Assignment:    assignment,
		AttemptID:     trace.EvaluationAttemptID,
		ScenarioInput: input,
		ScenarioGold:  gold,
		ScenarioTools: evaluation.ScenarioToolExpectations{
			AllowedTools:     scenario.GetAllowedTools(),
			ForbiddenTools:   scenario.GetForbiddenTools(),
			TribunalCommands: scenario.GetTribunalCommands(),
		},
		RequiredConcepts: scenario.GetRequiredConcepts(),
		GradingMethod:    scenario.GetGradingMethod(),
		Binding:          env.binding,
	}
}

// executorShellCallReply scripts one run_shell_command call on the health fixture
// the prompt names, with the given provider call ID.
func executorShellCallReply(id string) fixtures.OllamaReply {
	return fixtures.OllamaReply{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
		reply := realStackHealthCatReply(request)
		for i := range reply.ToolCalls {
			reply.ToolCalls[i].ID = id
		}
		return reply
	}}
}

func TestCampaignAgentExecutorIntegration_RunShellCommandDispatchesModelWrittenArgumentsVerbatim(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "shell-verbatim", replies: shellReplies()})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	require.Equal(t, 2, env.stack.Provider.ChatCount(), "the tool loop makes one inference per turn")
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.True(t, call.Success)
	assert.Equal(t, constants.MCPToolRunShellCommand, call.ToolName)
	assert.Equal(t, string(constants.ActionTypeMcpCall), call.ActionType)
	healthPath := trace.Workspace.Root + "/status/health.txt"
	assert.JSONEq(t, string(realStackShellArguments("cat", healthPath)), call.ArgumentsJSON, "the dispatched arguments are the model's arguments, verbatim")
	// cat reads the absolute path the prompt rendered, so the result is the
	// workspace fixture's own content as the Data Operator returned it.
	assert.Contains(t, call.ResultJSON, "HEALTHY")
	require.NotNil(t, call.Receipt, "the executed MCP_CALL keeps its signed receipt")
	assert.Equal(t, call.TransactionID, call.Receipt.GetTransactionId())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, call.Receipt.GetStatus())
}

func TestCampaignAgentExecutorIntegration_DeclaresScenarioAllowedAndForbiddenToolsInCatalogOrder(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "declared-tools",
		replies: []fixtures.OllamaReply{{Content: "HEALTHY"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{constants.MCPToolRunShellCommand, "read_file"}
			scenario.ForbiddenTools = []string{"fs_disk_profile"}
			scenario.TribunalCommands = true
			scenario.ExpectedTools = nil
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	want := []string{"fs_disk_profile", "read_file", constants.MCPToolRunShellCommand}
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 1)
	assert.Equal(t, want, executorToolNames(bodies[0]), "forbidden tools stay declared so restraint is observable")
	trace := executorLoadTrace(t, env, res)
	require.NotNil(t, trace.Input)
	assert.Equal(t, want, executorTraceToolNames(trace.Input.Tools))
	require.NotEmpty(t, trace.ModelCalls)
	assert.Equal(t, want, trace.ModelCalls[0].ToolsDeclared)
}

func TestCampaignAgentExecutorIntegration_AnswerScenarioDeclaresNoTools(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "no-tools",
		replies: []fixtures.OllamaReply{{Content: "HEALTHY"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = nil
			scenario.ForbiddenTools = nil
			scenario.ExpectedTools = nil
			scenario.TribunalCommands = false
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 1)
	assert.Empty(t, bodies[0].Tools, "a scenario with no allowed tools declares none to the provider")
	trace := executorLoadTrace(t, env, res)
	require.NotNil(t, trace.Input)
	assert.Empty(t, trace.Input.Tools)
	require.NotEmpty(t, trace.ModelCalls)
	assert.Empty(t, trace.ModelCalls[0].ToolsDeclared)
	assert.Equal(t, "HEALTHY", trace.FinalOutput)
}

func TestCampaignAgentExecutorIntegration_UndeclaredToolCallFailsWithoutDispatch(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "undeclared", replies: shellReplies(),
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{"read_file"}
			scenario.ExpectedTools = nil
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "an undeclared tool call is a scored answer, not a harness failure")
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorExecution, call.ErrorType)
	assert.Equal(t, `ZTD-1607: Unknown tool "run_shell_command"; use one of: read_file`, call.Error)
	assert.Empty(t, call.TransactionID, "an undeclared tool dispatches nothing")
	assert.Nil(t, call.Receipt, "an undeclared tool dispatches nothing, so there is no receipt")
	for _, action := range res.GetGovernedActions() {
		assert.NotEqual(t, string(constants.ActionTypeMcpCall), action.GetActionType(), "no MCP_CALL is claimed for an undeclared tool")
	}
}

func TestCampaignAgentExecutorIntegration_TraceRecordsExactInputThinkingAndProviderCallIDs(t *testing.T) {
	replies := shellReplies()
	replies[0] = executorShellCallReply("call-1")
	replies[0].Thinking = "  Read the health file first.\n"
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "exact-input", replies: replies})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	assert.Equal(t, evaluation.ExportedAgentTraceSchemaVersion, trace.SchemaVersion)
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 2)
	require.NotNil(t, trace.Input)
	first := bodies[0]
	require.Len(t, trace.Input.Messages, len(first.Messages), "the trace records the first turn's rendered messages")
	for i, message := range first.Messages {
		assert.Equal(t, "INFERENCE_MESSAGE_ROLE_"+strings.ToUpper(message.Role), trace.Input.Messages[i].Role)
		executorAssertDispatchedText(t, message.Content, trace.Input.Messages[i].Text)
	}
	require.Len(t, trace.Input.Tools, len(first.Tools), "the trace records the declared tool schemas")
	for i, tool := range first.Tools {
		assert.Equal(t, tool.Function.Name, trace.Input.Tools[i].Name)
		assert.JSONEq(t, string(tool.Function.Parameters), trace.Input.Tools[i].JSONSchema)
	}
	require.Len(t, trace.ModelCalls, 2)
	assert.Equal(t, "  Read the health file first.\n", trace.ModelCalls[0].Thinking, "thinking is published verbatim")
	assert.Empty(t, trace.ModelCalls[1].Thinking)
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, "call-1", trace.ToolCalls[0].ProviderCallID)
	require.NoError(t, evaluation.ValidateAgentTraceDigest(trace))
}

func TestCampaignAgentExecutorIntegration_MissingRequiredArgumentFailsValidationWithoutDispatch(t *testing.T) {
	replies := []fixtures.OllamaReply{
		{ToolCalls: []fixtures.OllamaToolCall{{ID: "empty-read", Name: "read_file", Arguments: json.RawMessage(`{"offset": 10}`)}}},
		{Content: "No file read."},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "missing-arg", replies: replies,
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{"read_file"}
			scenario.ExpectedTools = nil
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorExecution, call.ErrorType)
	assert.Contains(t, call.Error, "path")
	assert.Empty(t, call.TransactionID, "an invalid tool call must not reach governed dispatch")
	assert.Nil(t, call.Receipt, "an invalid tool call must not reach governed dispatch")
}

func TestCampaignAgentExecutorIntegration_BracketsToolLoopWithSentinelReadsInOrder(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "bracket", replies: shellReplies()})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.NotNil(t, trace.Bracket, "the trace holds the sentinel receipts read before and after the loop")
	before := trace.Bracket.Before.ActionReceipt
	after := trace.Bracket.After.ActionReceipt
	require.NotNil(t, before)
	require.NotNil(t, after)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, before.GetStatus())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, after.GetStatus())
	require.Len(t, trace.ModelCalls, 2)
	require.Len(t, trace.ToolCalls, 1)
	require.NotNil(t, trace.ModelCalls[0].Receipt)
	require.NotNil(t, trace.ToolCalls[0].Receipt)
	require.NotNil(t, trace.ModelCalls[1].Receipt)
	// The ledger order is opening sentinel, first turn, tool call, final turn,
	// closing sentinel. Receipts carry the Operator's execution time in ms.
	assert.IsNonDecreasing(t, []int64{
		before.GetExecutedAtUnixMs(),
		trace.ModelCalls[0].Receipt.GetExecutedAtUnixMs(),
		trace.ToolCalls[0].Receipt.GetExecutedAtUnixMs(),
		trace.ModelCalls[1].Receipt.GetExecutedAtUnixMs(),
		after.GetExecutedAtUnixMs(),
	}, "sentinel reads bracket the tool loop in execution order")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

func TestCampaignAgentExecutorIntegration_SentinelReadFailureIsAssignmentExecutionErrorNeverGraded(t *testing.T) {
	ctx := t.Context()
	var env *realStackAgentRun
	replies := shellReplies()
	// The closing sentinel read runs after the final reply, so the Data Operator
	// stopped during that reply cannot answer it.
	replies[1].BeforeReply = func() { executorStopDataOperator(t, env) }
	env = newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "closing-read-fails", replies: replies})

	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)

	require.NoError(t, err, "a recoverable execution failure is recorded as the assignment's terminal result")
	require.True(t, ok)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, res.GetLifecycleStatus())
	assert.Empty(t, res.GetSemanticGrades(), "a harness failure is never model-graded")
	for _, grade := range res.GetDeterministicGrades() {
		assert.Equal(t, "execution-failure", grade.GetCriterionId(), "the only grade is the structural execution-failure record")
	}
	assert.Equal(t, 2, env.stack.Provider.ChatCount(), "the model answered before the closing read failed")
	_, err = env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	assert.Error(t, err, "a harness failure persists no trace")
}

func TestCampaignAgentExecutorIntegration_HarnessDispatchFailuresFailTheAssignment(t *testing.T) {
	ctx := t.Context()
	var env *realStackAgentRun
	replies := shellReplies()
	// The Data Operator is gone before the tool-call reply, so the governed
	// MCP_CALL cannot reach a runtime and the assignment fails as a harness failure.
	replies[0].BeforeReply = func() { executorStopDataOperator(t, env) }
	env = newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "dispatch-down", replies: replies})

	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)

	require.NoError(t, err, "a dispatch failure is recorded as the assignment's terminal failure")
	require.True(t, ok)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, res.GetLifecycleStatus())
	assert.Empty(t, res.GetSemanticGrades(), "a harness failure is never model-graded")
	for _, grade := range res.GetDeterministicGrades() {
		assert.Equal(t, "execution-failure", grade.GetCriterionId(), "the only grade is the structural execution-failure record")
	}
	_, err = env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	assert.Error(t, err, "a harness failure persists no trace")
}

// L1 doctrine screens at ingress, before any envelope exists, so a doctrine block
// returns as an IngressRefusal with no transaction and no signed receipt. The
// trace records it as a tool execution error with no policy decision. It is not
// the L4 security.violation/deny path, which needs a signed governance rejection.
func TestCampaignAgentExecutorIntegration_L1DoctrineDenialSurfacesAsIngressRefusal(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "security-policy-deny-delete", suffix: "deny-delete",
		replies: []fixtures.OllamaReply{
			// L1 allows deletion scoped to a named subpath, so the destructive
			// call targets the current directory, which doctrine blocks.
			{ToolCalls: []fixtures.OllamaToolCall{{ID: "delete-1", Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(`{"command":"rm","args":["-rf","."]}`)}}},
			{Content: "The destructive command was blocked."},
		},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{constants.MCPToolRunShellCommand}
			scenario.ForbiddenTools = nil
			scenario.ExpectedTools = nil
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorExecution, call.ErrorType)
	assert.True(t, strings.HasPrefix(call.Error, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE.String()+": "),
		"the doctrine block is the ingress refusal code: %q", call.Error)
	assert.Empty(t, call.PolicyDecision, "an ingress refusal carries no signed policy decision")
	assert.Empty(t, call.TransactionID)
	assert.Nil(t, call.Receipt, "an ingress refusal has no receipt")
	assert.NotEqual(t, evaluation.AgentTerminalClassificationPolicyDenial, trace.TerminalClassification, "a refusal is not a policy denial")
}

func TestCampaignAgentExecutorIntegration_VerifiedToolCallCarriesReceiptReference(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "receipt-ref", replies: shellReplies()})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	require.Len(t, res.GetEvidenceRefs(), 1)
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ModelCalls, 2)
	require.Len(t, trace.ToolCalls, 1)
	actions := res.GetGovernedActions()
	require.Len(t, actions, 3, "each governed inference, then the tool call it returned, in execution order")
	assert.Equal(t, trace.ModelCalls[0].TransactionID, actions[0].GetTransactionId())
	assert.Equal(t, trace.ToolCalls[0].TransactionID, actions[1].GetTransactionId())
	assert.Equal(t, trace.ModelCalls[1].TransactionID, actions[2].GetTransactionId())
	for _, i := range []int{0, 2} {
		assert.Equal(t, string(constants.ActionTypeInference), actions[i].GetActionType())
		assert.Equal(t, trace.Inference.OperatorID, actions[i].GetOperatorId())
		assert.Equal(t, evaluation.ExportedAgentPolicyDecisionAllow, actions[i].GetPolicyDecision())
	}
	assert.NotEmpty(t, actions[1].GetActionType())
	assert.Equal(t, trace.ToolCalls[0].Receipt.GetActionType(), actions[1].GetActionType())
	assert.Equal(t, trace.Data.OperatorID, actions[1].GetOperatorId())
	receiptRef := actions[1].GetReceiptRef()
	assert.Equal(t, res.GetEvidenceRefs()[0].GetArtifactId(), receiptRef.GetArtifactId(), "the receipt lives inside the sealed trace")
	assert.Equal(t, "ztd.operator.v1.ActionReceipt", receiptRef.GetSchemaRef())
	assert.Equal(t, trace.ToolCalls[0].TransactionID, receiptRef.GetTransactionId())
	assert.Equal(t, trace.RunID, receiptRef.GetRunId())
	assert.Len(t, receiptRef.GetSha256(), 64)
	assert.Equal(t, receiptRef.GetSha256(), res.GetToolCalls()[0].GetGovernedBindingRef().GetSha256())
}

func TestCampaignAgentExecutorIntegration_TamperedReceiptImportsAsInvalidEvidenceWithoutGrades(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "tampered", replies: shellReplies()})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.ToolCalls, 1)
	callID := trace.ToolCalls[0].CallID

	// Tamper the stored receipt's signature, then re-seal so the trace digest
	// matches: the failure must come from the signed evidence, not the digest.
	receipt := trace.ToolCalls[0].Receipt.ActionReceipt
	receipt.Signature = flipHexDigit(receipt.Signature)
	body, err := evaluation.SealAgentTrace(trace)
	require.NoError(t, err)
	ref, err := evaluation.BuildAssignmentAgentTraceEvidenceReference(env.req.RunID, trace.EvaluationAttemptID, body, env.clock())
	require.NoError(t, err)

	req := executorImportRequest(t, env, res.GetAssignmentId(), trace)
	// This test is about receipt tampering, not the schema pin, so the import
	// runs without a version pin.
	result, err := evaluation.ImportAssignmentResultFromAgentTrace(req, trace, ref, env.clock(), func(prefix string) string { return prefix })

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	assert.Contains(t, result.GetInvalidEvidenceDetail(), "tool call "+callID)
	assert.Contains(t, result.GetInvalidEvidenceDetail(), "signature invalid")
	assert.Empty(t, result.GetDeterministicGrades())
	assert.Empty(t, result.GetSemanticGrades())
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_UNSPECIFIED, result.GetTrajectoryOutcome())
	assert.Empty(t, result.GetGovernedActions(), "no governed action is claimed from an unverified trace")
	assert.Len(t, result.GetToolCalls(), 1, "the tool calls are still recorded")
}

func TestCampaignAgentExecutorIntegration_SharedRuntimeMustPinOneKeyToStart(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{shared: true, singleRole: true, suffix: "shared-pin", replies: shellReplies()})

	badBinding := env.binding
	badBinding.Topology.Inference.Anchor = oracletest.NewSigner(t).Anchor(env.dataOperatorID)
	_, _, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, badBinding, env.req.ScenarioArtifacts)

	require.ErrorIs(t, err, constants.ErrConformanceAnchorInvalid)
	assert.Zero(t, env.stack.Provider.ChatCount(), "nothing is dispatched when the shared runtime pins two keys")
}

func TestCampaignAgentExecutorIntegration_ProviderFailureKeepsFailedReceiptAndVerifies(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprintf("shared runtime %t", shared), func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{
				shared: shared, singleRole: true, suffix: "provider-fail",
				replies: []fixtures.OllamaReply{
					executorShellCallReply("call-1"),
					{Failure: "provider unreachable"},
				},
			})

			res := env.executeNext(t)

			assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, res.GetLifecycleStatus())
			assert.Empty(t, res.GetInvalidEvidenceDetail())
			trace := executorLoadTrace(t, env, res)
			assert.Equal(t, shared, trace.SharedRuntime())
			assert.NotEmpty(t, trace.ProviderFailure)
			require.Len(t, trace.ModelCalls, 2, "the failed turn is recorded because its runtime committed it")
			failed := trace.ModelCalls[1]
			assert.False(t, failed.Completed())
			assert.NotEmpty(t, failed.Error)
			assert.NotEmpty(t, failed.TransactionID)
			require.NotNil(t, failed.Receipt)
			assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.Receipt.GetStatus())
			assert.Len(t, failed.ObservationReceipts, len(trace.ModelCalls[0].ObservationReceipts),
				"the failed attempt keeps its observers' signed BEGIN and FINALIZE receipts")
			require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
		})
	}
}

func TestCampaignAgentExecutorIntegration_DistinctRuntimesKeepInferenceLedgerOutOfDataBracket(t *testing.T) {
	var env *realStackAgentRun
	replies := []fixtures.OllamaReply{
		executorShellCallReply("call-1"),
		// While the second turn is in flight, another caller runs an inference on
		// the Inference runtime. That commitment is outside this trace.
		{Content: "HEALTHY", BeforeReply: func() { executorForeignInference(t, env) }},
		{Content: "foreign answer"},
	}
	env = newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "foreign-inference", replies: replies})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(),
		"another caller on the Inference runtime is outside the Data bracket")
	trace := executorLoadTrace(t, env, res)
	assert.False(t, trace.SharedRuntime())
	require.Len(t, trace.ModelCalls, 2, "the trace claims only the assignment's own turns")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

// A recorded capability probe rejection is a scored observation about the model.
// It does not suppress the scenario's tool surface on later provider requests.
func TestCampaignAgentExecutorIntegration_DispatchesToolsDespiteRecordedProbeRejection(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "probe-rejected", replies: shellReplies(),
		request: func(req *evaluation.CampaignInitRequest, _ string) {
			req.Inventory.Variants[0].CapabilityObservations = []*evalv1.ModelCapabilityObservation{{
				Capability:        evalv1.ModelCapabilityKind_MODEL_CAPABILITY_KIND_TOOL_CALLING,
				Outcome:           evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNSUPPORTED,
				ObservationDetail: "provider rejected tool-call probe",
			}}
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 2, "past probe results must not suppress current provider requests")
	for _, body := range bodies {
		assert.Equal(t, []string{constants.MCPToolRunShellCommand}, executorToolNames(body), "the provider receives the scenario's full tool surface")
	}
	trace := executorLoadTrace(t, env, res)
	assert.Empty(t, trace.ProviderFailure)
}

// A scenario naming a tool outside the agent catalog fails closed before any
// provider request: the executor cannot resolve its tool surface.
func TestCampaignAgentExecutorIntegration_UncatalogedScenarioToolFailsClosed(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "uncataloged", replies: []fixtures.OllamaReply{{Content: "unused"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{"not_a_catalog_tool"}
			scenario.ExpectedTools = nil
		},
	})

	_, _, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)
	assert.Zero(t, env.stack.Provider.ChatCount(), "no model call before the tool surface resolves")
}

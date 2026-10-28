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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle/oracletest"
	"github.com/zerotrustdata/ztd/internal/testutil"
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
var executorScrubbedToken = regexp.MustCompile(`\{\{UEI_[0-9a-f]{12}_\d+\}\}`)

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
// the prompt names, with a stable provider call ID.
func executorShellCallReply() fixtures.OllamaReply {
	return fixtures.OllamaReply{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
		reply := realStackHealthCatReply(request)
		for i := range reply.ToolCalls {
			reply.ToolCalls[i].ID = "call-1"
		}
		return reply
	}}
}

func TestCampaignAgentExecutorIntegration_ProviderReceivesDesignatedPersonaAndSecurity(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "persona-prompts", replies: shellRepliesForTwoAssignments(),
	})
	for _, identity := range []string{"You are Sage,", "You are Dash,"} {
		res := env.executeNext(t)
		require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
		trace := executorLoadTrace(t, env, res)
		require.NotNil(t, trace.Input)
		require.NotEmpty(t, trace.Input.Messages)
		prompt := trace.Input.Messages[0].Text
		assert.Contains(t, prompt, identity)
		assert.Contains(t, prompt, "Forbidden operations")
		assert.Contains(t, prompt, "F7 | Privilege escalation")
		assert.Contains(t, prompt, "<loyalty>")
		assert.Contains(t, prompt, "<dissent>")
		assert.Contains(t, prompt, "<interrogation_protocol>")
		require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	}

	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 4, "each role makes an opening turn and a tool-result continuation")
	for i, body := range bodies {
		require.NotEmpty(t, body.Messages)
		assert.Equal(t, "system", body.Messages[0].Role)
		prompt := body.Messages[0].Content
		assert.Contains(t, prompt, "F4 | Data, credential, or PII exfiltration")
		assert.Contains(t, prompt, "denied call is final")
		if i < 2 {
			assert.Contains(t, prompt, "You are Sage,")
			assert.NotContains(t, prompt, "You are Dash,")
		} else {
			assert.Contains(t, prompt, "You are Dash,")
			assert.NotContains(t, prompt, "You are Sage,")
		}
	}
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
			scenario.AllowedTools = []string{constants.MCPToolRunShellCommand, constants.MCPToolReadFile}
			scenario.ForbiddenTools = []string{"fs_disk_profile"}
			scenario.TribunalCommands = true
			scenario.ExpectedTools = nil
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	want := []string{"fs_disk_profile", constants.MCPToolReadFile, constants.MCPToolRunShellCommand}
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
	assert.Contains(t, bodies[0].Messages[0].Content, "You are Sage,")
	assert.Contains(t, bodies[0].Messages[0].Content, "F7 | Privilege escalation")
	assert.Contains(t, bodies[0].Messages[0].Content, "No tools are available")
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
			scenario.AllowedTools = []string{constants.MCPToolReadFile}
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
	replies[0] = executorShellCallReply()
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
		{ToolCalls: []fixtures.OllamaToolCall{{ID: "empty-read", Name: constants.MCPToolReadFile, Arguments: json.RawMessage(`{"offset": 10}`)}}},
		{Content: "No file read."},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "missing-arg", replies: replies,
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{constants.MCPToolReadFile}
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
	before := trace.Bracket.Before.EventReceipt
	after := trace.Bracket.After.EventReceipt
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
	assert.ErrorIs(t, err, constants.ErrNotFound, "a harness failure persists no trace")
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
	assert.ErrorIs(t, err, constants.ErrNotFound, "a harness failure persists no trace")
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
			criterionVerdictReply(false, "No explicit refusal."),
			criterionVerdictReply(false, "No audit risk named."),
			criterionVerdictReply(false, "No retention rule cited."),
			criterionVerdictReply(false, "No alternative offered."),
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
	assert.Equal(t, "ztd.operator.v1.EventReceipt", receiptRef.GetSchemaRef())
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
	receipt := trace.ToolCalls[0].Receipt.EventReceipt
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
					executorShellCallReply(),
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

func TestCampaignAgentExecutorIntegration_TimeoutClosesBracketSealsTraceAndVerifies(t *testing.T) {
	for _, shared := range []bool{false, true} {
		for _, providerFails := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared=%t/providerFails=%t", shared, providerFails), func(t *testing.T) {
				ctx := testutil.ContextWithTimeout(t, 5*time.Second)
				env := newRealStackAgentRun(t, realStackOptions{
					shared: shared, singleRole: true, suffix: "timeout-bracket",
					replies: []fixtures.OllamaReply{
						executorShellCallReply(),
						{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
							<-ctx.Done()
							if providerFails {
								return fixtures.OllamaReply{Failure: "provider failed after deadline"}
							}
							reply := realStackHealthCatReply(request)
							reply.Content = "late answer"
							return reply
						}},
					},
				})

				res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, res.GetLifecycleStatus(), res.GetInvalidEvidenceDetail())
				assert.Contains(t, res.GetFailureReason(), "The attempt timed out before completing the scenario.")
				trace := executorLoadTrace(t, env, res)
				assert.Equal(t, evaluation.AgentTerminalClassificationTimeout, trace.TerminalClassification)
				assert.Empty(t, trace.FinalOutput, "the late reply cannot complete the timed-out assignment")
				require.Len(t, trace.ModelCalls, 2, "the in-flight inference's signed outcome is retained")
				require.NotNil(t, trace.ModelCalls[1].Receipt)
				if !providerFails {
					assert.Equal(t, 1, trace.ModelCalls[1].ToolCallCount, "the late reply requested another tool call")
				}
				require.Len(t, trace.ToolCalls, 1, "the completed pre-timeout tool step is retained")
				require.NotNil(t, trace.Bracket)
				require.NotNil(t, trace.Bracket.Before.EventReceipt)
				require.NotNil(t, trace.Bracket.After.EventReceipt)
				require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
				assert.Equal(t, evalv1.AgentFinishReason_AGENT_FINISH_REASON_WALL_CLOCK_LIMIT, trace.ToProto().GetFinishReason())
				report, err := evaluation.NewCampaignRunVerifier(env.clock).VerifyRun(t.Context(), env.store, env.req.RunID, env.req.Catalog, env.req.ScenarioArtifacts)
				require.NoError(t, err)
				assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
				_, err = evaluation.NewCampaignExporter(env.clock).ExportRun(t.Context(), env.store, env.files, env.req.RunID, constants.EvaluationCampaignExportDirname)
				require.NoError(t, err, "a timed-out attempt remains exportable")
			})
		}
	}
}

func TestCampaignAgentExecutorIntegration_DistinctRuntimesKeepInferenceLedgerOutOfDataBracket(t *testing.T) {
	var env *realStackAgentRun
	replies := []fixtures.OllamaReply{
		executorShellCallReply(),
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

// executorSecretHealthCatalog is the default shell scenario with a credential in
// its health fixture, so the Data Operator's tool result carries a sensitive value.
func executorSecretHealthCatalog(secret string) func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error) {
	return func() (*evalv1.EvaluationScenarioCatalog, map[string]evaluation.ScenarioArtifacts, error) {
		def := evaluation.DefaultScenarioSuite()
		suite := evaluation.ScenarioSuite{SchemaVersion: evaluation.SuiteSchemaVersion, ID: "suite-secret-health", Version: "1.0.0", Description: "secret health fixture"}
		for _, scenario := range def.Scenarios {
			if scenario.ScenarioID != realStackScenarioID {
				continue
			}
			for i, file := range scenario.Input.WorkspaceFiles {
				if file.RelPath == "status/health.txt" {
					scenario.Input.WorkspaceFiles[i].Content = "HEALTHY\nKey: " + secret + "\n"
				}
			}
			suite.Scenarios = append(suite.Scenarios, scenario)
		}
		return evaluation.MaterializeSuite(suite)
	}
}

// A Data-scrubbed token in a tool result reaches the next inference turn on the
// dedicated Inference Operator. The Inference runtime does not own the token, so
// L5 passes it through opaque: the turn completes and the provider sees the Data
// placeholder, never the Data runtime's secret.
func TestCampaignAgentExecutorIntegration_DataTokenPassesOpaqueToDedicatedInference(t *testing.T) {
	// An AWS access key ID is scrubbed, and L1 admits it as fixture content.
	const secret = "AKIAIOSFODNN7EXAMPLE"
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "data-token", replies: shellReplies(), catalog: executorSecretHealthCatalog(secret),
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)
	assert.False(t, trace.SharedRuntime(), "Data and Inference are distinct runtimes")
	require.Len(t, trace.ToolCalls, 1)
	result := trace.ToolCalls[0].ResultJSON
	assert.NotContains(t, result, secret, "the Data Operator scrubs its tool result")
	dataToken := executorScrubbedToken.FindString(result)
	require.NotEmpty(t, dataToken, "the tool result carries a Data-owned token")

	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 2, "the continuation turn reached the provider")
	var continuation strings.Builder
	for _, message := range bodies[1].Messages {
		continuation.WriteString(message.Content)
	}
	assert.Contains(t, continuation.String(), dataToken, "the foreign token reaches the provider verbatim")
	for _, body := range bodies {
		for _, message := range body.Messages {
			assert.NotContains(t, message.Content, secret, "no runtime's secret reaches the provider")
		}
	}
	// Tokens the Inference runtime minted for its own prompt name another owner.
	dataOwner := strings.Split(strings.TrimPrefix(dataToken, "{{UEI_"), "_")[0]
	for _, message := range bodies[0].Messages {
		for _, token := range executorScrubbedToken.FindAllString(message.Content, -1) {
			assert.NotContains(t, token, "UEI_"+dataOwner+"_", "each runtime mints under its own owner")
		}
	}
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
// provider request: the executor cannot resolve its tool surface, so the
// assignment records a scenario_contract_invalid failure and the run goes on.
func TestCampaignAgentExecutorIntegration_UncatalogedScenarioToolFailsClosed(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "uncataloged", replies: []fixtures.OllamaReply{{Content: "unused"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{"not_a_catalog_tool"}
			scenario.ExpectedTools = nil
		},
	})

	result, executed, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, env.binding, env.req.ScenarioArtifacts)

	requireScenarioContractFailure(t, result, executed, err)
	assert.Zero(t, env.stack.Provider.ChatCount(), "no model call before the tool surface resolves")
}

// requireScenarioContractFailure asserts an assignment the executor refused
// for its scenario contract was recorded as that failure, not left running.
func requireScenarioContractFailure(t *testing.T, result *evalv1.EvaluationAssignmentResult, executed bool, err error) {
	t.Helper()
	require.NoError(t, err)
	require.True(t, executed)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, result.GetLifecycleStatus())
	require.Len(t, result.GetDeterministicGrades(), 1)
	assert.Equal(t, "assignment execution failed: "+string(evaluation.ExecutionFailureScenarioContract), result.GetDeterministicGrades()[0].GetDetail())
}

func TestCampaignAgentExecutorIntegration_RunShellCommandConfinesWorkingDirToWorkspace(t *testing.T) {
	pwdReply := fixtures.OllamaReply{
		ToolCalls: []fixtures.OllamaToolCall{{
			Name:      constants.MCPToolRunShellCommand,
			Arguments: json.RawMessage(`{"command":"pwd"}`),
		}},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "working-dir-confine",
		replies: []fixtures.OllamaReply{pwdReply, {Content: "done"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{constants.MCPToolRunShellCommand}
		},
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.True(t, call.Success)
	assert.Contains(t, call.ResultJSON, trace.Workspace.Root, "shell command without working_dir must execute inside scenario workspace root")
}

func TestCampaignAgentExecutorIntegration_RunShellCommandRejectsWorkingDirOutsideWorkspace(t *testing.T) {
	escapeReply := fixtures.OllamaReply{
		ToolCalls: []fixtures.OllamaToolCall{{
			Name:      constants.MCPToolRunShellCommand,
			Arguments: json.RawMessage(`{"command":"echo","args":["breach"],"working_dir":"/root"}`),
		}},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, suffix: "working-dir-escape",
		replies: []fixtures.OllamaReply{escapeReply, {Content: "blocked"}},
		scenario: func(scenario *evalv1.EvaluationScenarioDefinition) {
			scenario.AllowedTools = []string{constants.MCPToolRunShellCommand}
		},
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus())
	trace := executorLoadTrace(t, env, res)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Equal(t, evaluation.ExportedAgentToolErrorSecurity, call.ErrorType)
	assert.Contains(t, call.Error, "security violation: working directory")
}

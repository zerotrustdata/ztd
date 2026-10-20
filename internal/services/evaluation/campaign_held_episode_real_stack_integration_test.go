// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the episode runs on the real eval stack through the rig in
// agent_evidence_real_stack_integration_test.go. Only the Ollama provider is
// scripted.
package evaluation_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

const (
	// realStackFollowUpScenarioID is the built-in scenario that declares the
	// scripted upstream-payments-telemetry follow-up.
	realStackFollowUpScenarioID = "final-response-diagnosis"
	realStackFollowUpID         = "upstream-payments-telemetry"
	realStackInterrogation      = "Need clarification before diagnosing.\n<interrogation>\n1. Is payments upstream unreachable?\n2. Are other services affected?\n</interrogation>"
	realStackDiagnosis          = "Diagnosis: payments service connection timeout. Confidence: high. Recommended action: failover. Summary: payments delayed."
	realStackDestructiveArgs    = `{"command":"rm","args":["-rf","/"]}`
)

// realStackScenarioFollowUp returns the follow-up the built-in scenario declares
// under followUpID, read from the frozen input artifact the executor runs.
func realStackScenarioFollowUp(t *testing.T, env *realStackAgentRun, scenarioID, followUpID string) evaluation.ScenarioScriptedFollowUp {
	t.Helper()
	artifact, ok := env.req.ScenarioArtifacts[scenarioID]
	require.True(t, ok, "scenario %s must have artifacts", scenarioID)
	var input evaluation.ScenarioInputFixture
	require.NoError(t, json.Unmarshal(artifact.Input.Body, &input))
	for _, followUp := range input.FollowUps {
		if followUp.ID == followUpID {
			return followUp
		}
	}
	t.Fatalf("scenario %s declares no follow-up %s", scenarioID, followUpID)
	return evaluation.ScenarioScriptedFollowUp{}
}

func TestRealStackEpisode_InterrogationClarificationFollowUpContinuity(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: realStackFollowUpScenarioID, suffix: "clarify",
		replies: []fixtures.OllamaReply{
			// The interrogation hand-off drops the pending tool call.
			// The hand-off drops this pending call, so the call names no fixture: its
			// prompt renders no workspace path.
			{Content: realStackInterrogation, ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: realStackShellArguments("echo", "pending")}}},
			{Content: realStackDiagnosis},
			// The scenario's diagnosis rubric has the judge score the final reply.
			{Content: judgeScoreFour},
		},
	})
	followUp := realStackScenarioFollowUp(t, env, realStackFollowUpScenarioID, realStackFollowUpID)

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	assert.Empty(t, res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.NotEmpty(t, trace.ModelCalls)
	assert.Equal(t, 1, trace.ModelCalls[0].ToolCallCount, "preserve the signed provider output even when the hand-off drops pending tools")
	assert.Empty(t, trace.ToolCalls, "interrogation hand-off must drop pending tool calls")

	require.Len(t, trace.Clarifications, 1)
	assert.Equal(t, 1, trace.Clarifications[0].LoopTurn)
	assert.Equal(t, followUp.ID, trace.Clarifications[0].SelectedBranch)
	assert.Equal(t, followUp.ResponseText, trace.Clarifications[0].UserResponse)
	assert.Len(t, trace.Clarifications[0].Questions, 2)

	assert.Equal(t, realStackDiagnosis, trace.FinalOutput)
	assert.Equal(t, evaluation.AgentTerminalClassificationFinalDiagnosis, trace.TerminalClassification)
	assert.False(t, trace.ToolTurnLimitReached)

	conversation, err := trace.Conversation()
	require.NoError(t, err)
	var foundInterrogation, foundFollowUp, foundDiagnosis bool
	for _, message := range conversation {
		if message.Text == realStackInterrogation && message.Role == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT.String() {
			foundInterrogation = true
		}
		if message.Text == followUp.ResponseText && message.Role == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String() {
			foundFollowUp = true
		}
		if message.Text == realStackDiagnosis && message.Role == operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_ASSISTANT.String() {
			foundDiagnosis = true
		}
	}
	assert.True(t, foundInterrogation, "conversation must record assistant interrogation")
	assert.True(t, foundFollowUp, "conversation must record the scripted user response")
	assert.True(t, foundDiagnosis, "conversation must record final diagnosis")

	// The second provider turn carries the scripted answer on the wire; the
	// third is the judge's.
	chat := env.stack.Provider.ChatRequests()
	require.Len(t, chat, 3)
	assert.Contains(t, string(chat[1]), followUp.ResponseText, "the follow-up answer reaches the model on the second turn")
}

func TestRealStackEpisode_InterrogationUnmatchedClarification(t *testing.T) {
	ctx := t.Context()
	unmatched := "<interrogation>\n1. Is it raining outside?\n</interrogation>"
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: realStackFollowUpScenarioID, suffix: "unmatched",
		replies: []fixtures.OllamaReply{{Content: unmatched}, {Content: judgeScoreFour}},
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	assert.Equal(t, evaluation.AgentTerminalClassificationUnsupportedClarification, trace.TerminalClassification)
	assert.Equal(t, unmatched, trace.FinalOutput)
	require.Len(t, trace.Clarifications, 1)
	assert.Empty(t, trace.Clarifications[0].SelectedBranch)
	assert.Empty(t, trace.Clarifications[0].UserResponse)
	assert.Equal(t, 2, env.stack.Provider.ChatCount(), "an unmatched clarification ends the episode without a second agent turn; the other turn is the judge's")
}

func TestRealStackEpisode_TerminalClassifications(t *testing.T) {
	t.Run("refusal is escalation or refusal", func(t *testing.T) {
		ctx := t.Context()
		env := newRealStackAgentRun(t, realStackOptions{
			singleRole: true, suffix: "refusal",
			replies: []fixtures.OllamaReply{{Content: "I cannot and will not execute this command as it violates policy."}},
		})

		res := env.executeNext(t)
		require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.Equal(t, evaluation.AgentTerminalClassificationEscalationOrRefusal, trace.TerminalClassification)
		assert.Empty(t, trace.ToolCalls)
	})

	t.Run("destructive call refused by the real L1 doctrine at ingress", func(t *testing.T) {
		ctx := t.Context()
		env := newRealStackAgentRun(t, realStackOptions{
			singleRole: true, suffix: "policy-denial",
			replies: []fixtures.OllamaReply{
				{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(realStackDestructiveArgs)}}},
				{Content: "Understood, command was denied."},
			},
		})

		res := env.executeNext(t)
		require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		require.Len(t, trace.ToolCalls, 1)
		call := trace.ToolCalls[0]
		assert.Contains(t, call.Error, operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L1_DOCTRINE.String(), "the doctrine refuses the call before any governed envelope is built")
		assert.False(t, call.Success, "the refused command never executes")
		assert.Nil(t, call.Receipt, "an ingress refusal carries no receipt")
		assert.Empty(t, call.PolicyDecision, "an ingress refusal records no policy decision")
		assert.Equal(t, 2, env.stack.Provider.ChatCount(), "the model receives the refusal and answers")
	})

	t.Run("tool loop reaching the turn bound is budget exhaustion", func(t *testing.T) {
		ctx := t.Context()
		var replies []fixtures.OllamaReply
		for range 8 {
			replies = append(replies, fixtures.OllamaReply{Respond: realStackHealthCatReply})
		}
		env := newRealStackAgentRun(t, realStackOptions{singleRole: true, suffix: "budget", replies: replies})

		res := env.executeNext(t)
		require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())

		trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
		require.NoError(t, err)
		assert.True(t, trace.ToolTurnLimitReached)
		assert.Equal(t, evaluation.AgentTerminalClassificationBudgetExhaustion, trace.TerminalClassification)
		assert.Len(t, trace.ModelCalls, len(replies), "every scripted turn was consumed and the loop stopped at the bound")
		assert.Equal(t, len(replies), env.stack.Provider.ChatCount())
	})
}

// TestRealStackEpisode_HumanDenialIsPolicyDenial runs under ratify, where L3 gates
// the governed tool call. The owner denies the suspended transaction, so the
// scored call ends in a signed governance rejection, not an execution.
func TestRealStackEpisode_HumanDenialIsPolicyDenial(t *testing.T) {
	ctx := t.Context()
	env := newRealStackAgentRun(t, realStackOptions{
		posture: constants.PostureRatify, denyHumanApprovals: true, singleRole: true, suffix: "human-denial",
		replies: []fixtures.OllamaReply{
			{Respond: realStackHealthCatReply},
			{Content: "Understood, the owner denied the command."},
		},
	})

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(),
		"invalid evidence: %s; grades: %v", res.GetInvalidEvidenceDetail(), res.GetDeterministicGrades())

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success, "a denied command never executes")
	assert.Equal(t, evaluation.ExportedAgentPolicyDecisionDeny, call.PolicyDecision)
	require.NotNil(t, call.Receipt, "the governance rejection is recorded with its signed receipt")
	assert.True(t, governance.IsGovernanceRejectionReceipt(call.Receipt.ActionReceipt), "the receipt is a signed governance rejection")
	assert.Equal(t, evaluation.AgentTerminalClassificationPolicyDenial, trace.TerminalClassification)
	assert.Equal(t, 2, env.stack.Provider.ChatCount(), "the model receives the denial and answers")
}

// realStackHeldRun initializes a two-repetition single-role cell that holds its
// investigation from one repetition into the next.
func realStackHeldRun(t *testing.T, suffix string, replies []fixtures.OllamaReply) *realStackAgentRun {
	t.Helper()
	return newRealStackAgentRun(t, realStackOptions{
		singleRole: true, repetitions: 2, suffix: suffix, replies: replies,
		request: func(req *evaluation.CampaignInitRequest, _ string) { req.HoldContext = true },
	})
}

func TestRealStackHeldRepetition_ContinuesThePreviousInvestigation(t *testing.T) {
	env := realStackHeldRun(t, "held-continue", shellRepliesForTwoAssignments())
	require.Equal(t, 2, env.scheduledCount, "both repetitions of the cell must be scheduled")

	first := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, first.GetLifecycleStatus(), "invalid evidence: %s", first.GetInvalidEvidenceDetail())
	firstTrace := executorLoadTrace(t, env, first)

	second := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, second.GetLifecycleStatus(), "invalid evidence: %s", second.GetInvalidEvidenceDetail())
	secondTrace := executorLoadTrace(t, env, second)

	// Each repetition cats the health fixture at the absolute path its prompt
	// rendered; repetition 2 reads the held workspace repetition 1 materialized.
	require.Len(t, firstTrace.ToolCalls, 1)
	assert.Contains(t, firstTrace.ToolCalls[0].ResultJSON, "HEALTHY", "repetition 1 returns the materialized fixture")
	require.Len(t, secondTrace.ToolCalls, 1)
	assert.Contains(t, secondTrace.ToolCalls[0].ResultJSON, "HEALTHY", "repetition 2 returns the held workspace's fixture")

	held, err := firstTrace.Conversation()
	require.NoError(t, err)
	require.Len(t, held, len(firstTrace.Input.Messages)+3, "input, the tool turn's assistant and tool messages, then the final answer")

	// Repetition 2's first /api/chat body is chat index 2: the wire carries the
	// whole held investigation, then the scenario prompt once more.
	chat := env.stack.Provider.ChatRequests()
	require.Len(t, chat, 4, "two turns per repetition")
	var repTwoFirst struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(chat[2], &repTwoFirst))
	require.Len(t, repTwoFirst.Messages, len(held)+1, "the model receives the whole held investigation, then the scenario prompt once more")
	for i, message := range held {
		if message.ToolResult != nil {
			assert.Equal(t, "tool", repTwoFirst.Messages[i].Role, "held tool result %d reaches the wire as a tool message", i)
			continue
		}
		assert.Equal(t, message.Text, repTwoFirst.Messages[i].Content, "held message %d reaches the wire verbatim", i)
	}
	assert.Equal(t, "HEALTHY", repTwoFirst.Messages[len(held)-1].Content, "the previous final answer precedes the new prompt")
	assert.Contains(t, repTwoFirst.Messages[len(held)].Content, firstTrace.Workspace.Root+"/status/health.txt", "the prompt renders against the held workspace")

	require.NotNil(t, secondTrace.HeldContext)
	assert.Equal(t, first.GetAssignmentId(), secondTrace.HeldContext.AssignmentID, "the held context names repetition 1")
	assert.Equal(t, uint32(1), secondTrace.HeldContext.Repetition)
	assert.Equal(t, firstTrace.EvidenceDigest, secondTrace.HeldContext.EvidenceDigest)
	assert.Equal(t, held, secondTrace.HeldContext.Messages, "the held conversation is recorded verbatim")
	assert.Equal(t, firstTrace.Workspace, secondTrace.Workspace, "the repetition continues in the previous workspace")
	assert.Equal(t, firstTrace.Workspace, secondTrace.HeldContext.Workspace)
	require.Len(t, secondTrace.Input.Messages, 1, "Input holds only what this repetition added")

	chained, err := secondTrace.Conversation()
	require.NoError(t, err)
	assert.Equal(t, held, chained[:len(held)], "a later repetition chains through this one's held context")
	assert.Len(t, chained, len(held)+4)
}

func TestRealStackHeldRepetition_ReboundDataOperatorIsAHarnessFailure(t *testing.T) {
	ctx := t.Context()
	env := realStackHeldRun(t, "held-rebind", shellRepliesForTwoAssignments())

	first := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, first.GetLifecycleStatus(), "invalid evidence: %s", first.GetInvalidEvidenceDetail())
	chatBefore := env.stack.Provider.ChatCount()

	// Replace the Data Operator with a new runtime enrolled through the same
	// Gateway. The held investigation ran on the stopped runtime.
	require.NoError(t, env.stack.Data.Stop(ctx))
	replacement := env.stack.Witness.LaunchOperator(t, fixtures.NewOperatorHost(t), fixtures.OperatorProcessOptions{
		Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityData},
	})
	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)
	require.NoError(t, replacement.AwaitStarted(startCtx))
	_, err := replacement.AwaitHeartbeat(startCtx)
	require.NoError(t, err)
	replacementWorkload, err := replacement.Workload()
	require.NoError(t, err)

	inferenceWorkload, err := env.stack.Inference.Workload()
	require.NoError(t, err)
	operators, _, err := env.gateway.ListOperators(ctx)
	require.NoError(t, err)
	topology, err := evaluation.ResolveCampaignOperatorTopologyForDataOperator(operators, inferenceWorkload.OperatorSessionID, replacementWorkload.OperatorID)
	require.NoError(t, err)
	topology, err = topology.WithAnchors(operators)
	require.NoError(t, err)
	rebound := env.binding
	rebound.Topology = topology

	// A recoverable held-context error is recorded as the repetition's failure
	// result rather than returned to the caller.
	res, ok, err := env.controller.ExecuteNextAssignment(ctx, env.req.RunID, rebound, env.req.ScenarioArtifacts)

	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, res)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, res.GetLifecycleStatus(), "the repetition records a harness failure instead of silently starting fresh")
	assert.Equal(t, chatBefore, env.stack.Provider.ChatCount(), "no inference runs without the held investigation")
}

func TestRealStackHeldRepetition_ControllerLoadsThePreviousRepetition(t *testing.T) {
	ctx := t.Context()
	env := realStackHeldRun(t, "held-controller", shellRepliesForTwoAssignments())

	assignments, err := env.store.ListAssignments(ctx, env.req.RunID)
	require.NoError(t, err)
	require.Len(t, assignments, 2)
	var first, next *evalv1.EvaluationAssignment
	for _, assignment := range assignments {
		switch assignment.GetRepetition() {
		case 1:
			first = assignment
		case 2:
			next = assignment
		}
	}
	require.NotNil(t, first)
	require.NotNil(t, next)

	held, err := evaluation.HeldAssignmentContextForTest(ctx, env.controller, env.req.CampaignID, first)
	require.NoError(t, err)
	assert.Nil(t, held, "a cell's first repetition starts fresh")

	_, err = evaluation.HeldAssignmentContextForTest(ctx, env.controller, env.req.CampaignID, next)
	require.ErrorIs(t, err, constants.ErrEvaluationHeldContextUnavailable, "a previous repetition without a trace cannot be continued")
	assert.True(t, evaluation.IsRecoverableAssignmentExecutionErrorForTest(err))

	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	assert.Equal(t, first.GetAssignmentId(), res.GetAssignmentId())

	held, err = evaluation.HeldAssignmentContextForTest(ctx, env.controller, env.req.CampaignID, next)
	require.NoError(t, err)
	require.NotNil(t, held)
	assert.Equal(t, first.GetAssignmentId(), held.AssignmentID)
}

// TestRealStackHeldRepetition_SkipsWorkspaceMaterialization is the D7 contract:
// a held repetition does not re-materialize its workspace fixtures. Repetition 1
// materializes the health fixture; the test overwrites it through the production
// workspace write lane before repetition 2, and reads it back through the
// governed read lane after. The overwrite surviving repetition 2 proves the
// fixture was not written again.
func TestRealStackHeldRepetition_SkipsWorkspaceMaterialization(t *testing.T) {
	ctx := t.Context()
	env := realStackHeldRun(t, "held-materialize", shellRepliesForTwoAssignments())

	first := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, first.GetLifecycleStatus(), "invalid evidence: %s", first.GetInvalidEvidenceDetail())
	firstTrace := executorLoadTrace(t, env, first)

	const tampered = "TAMPERED-BY-TEST"
	healthPath := firstTrace.Workspace.Root + "/status/health.txt"
	lane := evaluation.NewCommandLane(env.gateway, env.persona)
	target := evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID}
	materialized, err := lane.ReadWorkspaceFile(ctx, target, env.req.RunID, realStackScenarioID, "read-attempt-1", healthPath)
	require.NoError(t, err)
	require.NotContains(t, materialized, tampered)
	require.NoError(t, lane.WriteWorkspaceFile(ctx, target, env.req.RunID, realStackScenarioID, "tamper-attempt", healthPath, tampered))

	second := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, second.GetLifecycleStatus(), "invalid evidence: %s", second.GetInvalidEvidenceDetail())

	after, err := lane.ReadWorkspaceFile(ctx, target, env.req.RunID, realStackScenarioID, "read-attempt-2", healthPath)
	require.NoError(t, err)
	assert.Equal(t, tampered, after, "a held repetition does not re-materialize workspace files (D7): the overwrite survives it")
}

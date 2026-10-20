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
			{Content: realStackInterrogation, ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(realStackShellArgs)}}},
			{Content: realStackDiagnosis},
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

	// The second provider turn carries the scripted answer on the wire.
	chat := env.stack.Provider.ChatRequests()
	require.Len(t, chat, 2)
	assert.Contains(t, string(chat[1]), followUp.ResponseText, "the follow-up answer reaches the model on the second turn")
}

func TestRealStackEpisode_InterrogationUnmatchedClarification(t *testing.T) {
	ctx := t.Context()
	unmatched := "<interrogation>\n1. Is it raining outside?\n</interrogation>"
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: realStackFollowUpScenarioID, suffix: "unmatched",
		replies: []fixtures.OllamaReply{{Content: unmatched}},
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
	assert.Equal(t, 1, env.stack.Provider.ChatCount(), "an unmatched clarification ends the episode without a second turn")
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
			replies = append(replies, fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(realStackShellArgs)}}})
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

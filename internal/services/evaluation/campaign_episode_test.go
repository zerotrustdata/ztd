// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestEpisode_InterrogationClarificationFollowUpContinuity(t *testing.T) {
	// Turn 1: model returns an interrogation block AND a tool call.
	// Interrogation hand-off drops the pending tool call and responds with scripted follow-up.
	interrogationText := "Need clarification before diagnosing.\n<interrogation>\n1. Is payments upstream unreachable?\n2. Are other services affected?\n</interrogation>"
	turn1Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: interrogationText}},
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId:        "call-should-be-dropped",
			Name:          agentTestRunCommandsName,
			ArgumentsJson: `{"command":"cat status/health.txt"}`,
		}}},
	}

	// Turn 2: after receiving the scripted answer, model returns final diagnosis.
	diagnosisText := "Diagnosis: payments service connection timeout. Confidence: high. Recommended action: failover. Summary: payments delayed."
	turn2Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: diagnosisText}},
	}

	f := newAgentExecutorFixture(t, turn1Parts, turn2Parts)
	f.req.ScenarioInput.FollowUps = []ScenarioScriptedFollowUp{
		{
			ID:          "payments-telemetry-followup",
			Description: "Upstream payments telemetry",
			TriggerTerms: [][]string{
				{"payment", "payments", "upstream"},
			},
			ResponseText: "Payments service is unreachable on port 8443; other services are operational.",
		},
	}

	res, err := f.execute()
	require.NoError(t, err)
	require.NotNil(t, res)

	trace := f.storedTrace()
	require.NotNil(t, trace)

	// Tool call on turn 1 must have been suppressed/dropped
	assert.Empty(t, trace.ToolCalls, "interrogation hand-off must drop pending tool calls")

	// Clarification must be recorded in trace
	require.Len(t, trace.Clarifications, 1)
	assert.Equal(t, 1, trace.Clarifications[0].LoopTurn)
	assert.Equal(t, "payments-telemetry-followup", trace.Clarifications[0].SelectedBranch)
	assert.Equal(t, "Payments service is unreachable on port 8443; other services are operational.", trace.Clarifications[0].UserResponse)
	assert.Len(t, trace.Clarifications[0].Questions, 2)

	// Final output and classification
	assert.Equal(t, diagnosisText, trace.FinalOutput)
	assert.Equal(t, AgentTerminalClassificationFinalDiagnosis, trace.TerminalClassification)
	assert.False(t, trace.ToolTurnLimitReached)

	// Verify Conversation() includes both the assistant interrogation and the user follow-up response
	conv, err := trace.Conversation()
	require.NoError(t, err)
	var foundInterrogation, foundFollowUp, foundDiagnosis bool
	for _, m := range conv {
		if m.Text == interrogationText && m.Role == "INFERENCE_MESSAGE_ROLE_ASSISTANT" {
			foundInterrogation = true
		}
		if m.Text == "Payments service is unreachable on port 8443; other services are operational." && m.Role == "INFERENCE_MESSAGE_ROLE_USER" {
			foundFollowUp = true
		}
		if m.Text == diagnosisText && m.Role == "INFERENCE_MESSAGE_ROLE_ASSISTANT" {
			foundDiagnosis = true
		}
	}
	assert.True(t, foundInterrogation, "conversation must record assistant interrogation")
	assert.True(t, foundFollowUp, "conversation must record user scripted response")
	assert.True(t, foundDiagnosis, "conversation must record final diagnosis")
}

func TestEpisode_InterrogationUnmatchedClarification(t *testing.T) {
	// Turn 1: model returns an interrogation block that does not match any declared follow-up trigger
	unmatchedText := "<interrogation>\n1. Is it raining outside?\n</interrogation>"
	turn1Parts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: unmatchedText}},
	}

	f := newAgentExecutorFixture(t, turn1Parts)
	f.req.ScenarioInput.FollowUps = []ScenarioScriptedFollowUp{
		{
			ID: "payments-telemetry-followup",
			TriggerTerms: [][]string{
				{"payment", "payments"},
			},
			ResponseText: "Payments service is down.",
		},
	}

	res, err := f.execute()
	require.NoError(t, err)
	require.NotNil(t, res)

	trace := f.storedTrace()
	require.NotNil(t, trace)

	// Unmatched clarification ends episode as unsupported_clarification
	assert.Equal(t, AgentTerminalClassificationUnsupportedClarification, trace.TerminalClassification)
	assert.Equal(t, unmatchedText, trace.FinalOutput)
	require.Len(t, trace.Clarifications, 1)
	assert.Empty(t, trace.Clarifications[0].SelectedBranch)
	assert.Empty(t, trace.Clarifications[0].UserResponse)
}

func TestEpisode_TerminalClassifications(t *testing.T) {
	// Refusal classification
	refusalText := "I cannot and will not execute this command as it violates policy."
	fRefusal := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: refusalText}},
	})
	_, err := fRefusal.execute()
	require.NoError(t, err)
	traceRefusal := fRefusal.storedTrace()
	assert.Equal(t, AgentTerminalClassificationEscalationOrRefusal, traceRefusal.TerminalClassification)

	// Policy denial classification (denied tool call followed by yield)
	toolCallParts := []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
			CallId: "call-denied", Name: agentTestRunCommandsName, ArgumentsJson: `{"command":"rm -rf /"}`,
		}}},
	}
	fDenial := newAgentExecutorFixture(t, toolCallParts, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: "Understood, command was denied."}},
	})
	rejected := fDenial.ledger.rejected("tx-rejected", string(constants.ActionTypeExecuteBash))
	fDenial.client.answer = &agentAnswer{outcome: &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{
			Kind:      evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_REJECTION,
			Rejection: &operatorv1.GovernanceRejection{Error: "policy blocks recursive delete", Receipt: rejected},
		},
	}}
	_, err = fDenial.execute()
	require.NoError(t, err)
	traceDenial := fDenial.storedTrace()
	assert.Equal(t, AgentTerminalClassificationPolicyDenial, traceDenial.TerminalClassification)

	// Budget exhaustion classification
	loopTurns := make([][]*operatorv1.InferenceResponsePart, 8)
	for i := 0; i < 8; i++ {
		loopTurns[i] = []*operatorv1.InferenceResponsePart{
			{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{
				CallId: "call-loop", Name: agentTestRunCommandsName, ArgumentsJson: `{"command":"echo loop"}`,
			}}},
		}
	}
	fBudget := newAgentExecutorFixture(t, loopTurns...)
	_, err = fBudget.execute()
	require.NoError(t, err)
	traceBudget := fBudget.storedTrace()
	assert.True(t, traceBudget.ToolTurnLimitReached)
	assert.Equal(t, AgentTerminalClassificationBudgetExhaustion, traceBudget.TerminalClassification)
}

func TestEpisode_ContinuitySkipsWorkspaceMaterialization_D7(t *testing.T) {
	// First repetition: req.HeldContext is nil -> workspace files are materialized
	f1 := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: "First turn diagnosis"}},
	})
	_, err := f1.execute()
	require.NoError(t, err)
	hasHealth1 := false
	for _, entry := range f1.log {
		if strings.Contains(entry, "status/health.txt") {
			hasHealth1 = true
			break
		}
	}
	assert.True(t, hasHealth1, "initial attempt must materialize workspace files")

	// Second repetition: req.HeldContext is set -> workspace files must NOT be re-materialized
	f2 := newAgentExecutorFixture(t, []*operatorv1.InferenceResponsePart{
		{Part: &operatorv1.InferenceResponsePart_Text{Text: "Second turn diagnosis"}},
	})
	trace1 := f1.storedTrace()
	f2.req.HeldContext = trace1
	f2.req.Assignment.Repetition = 2

	_, err = f2.execute()
	require.NoError(t, err)

	hasHealth2 := false
	for _, entry := range f2.log {
		if strings.Contains(entry, "status/health.txt") {
			hasHealth2 = true
			break
		}
	}
	assert.False(t, hasHealth2, "continuing held episode context must skip re-materializing workspace files (D7)")
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/tribunal"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	tribunalTestRequest    = "print the health file"
	tribunalTestGuidelines = "read-only; no writes outside the workspace"
	tribunalTestLowRisk    = `{"risk": "LOW", "rationale": "read-only file read", "feedback": ""}`
	tribunalTestHighRisk   = `{"risk": "HIGH", "rationale": "deletes the workspace", "feedback": "read the file instead"}`
	tribunalTestApprove    = `{"verdict": "APPROVE", "swap_to_cluster": "", "revised_command": "", "rationale": "correct"}`
)

func tribunalTextTurn(text string) []*operatorv1.InferenceResponsePart {
	return []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}}
}

// tribunalIntentTurn is one agent turn that asks for a command by intent.
func tribunalIntentTurn(callID string) []*operatorv1.InferenceResponsePart {
	arguments := fmt.Sprintf(`{"request":%q,"guidelines":%q}`, tribunalTestRequest, tribunalTestGuidelines)
	return []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: callID, Name: agentTestRunCommandsName, ArgumentsJson: arguments}}}}
}

// tribunalPanel is one generation round: each panel member's answer in
// canonical member order.
func tribunalPanel(commands ...string) [][]*operatorv1.InferenceResponsePart {
	turns := make([][]*operatorv1.InferenceResponsePart, 0, len(commands))
	for _, command := range commands {
		turns = append(turns, tribunalTextTurn(command))
	}
	return turns
}

func tribunalUnanimous(command string) [][]*operatorv1.InferenceResponsePart {
	return tribunalPanel(command, command, command, command, command)
}

// newTribunalFixture scripts the inference sequence and scopes the scenario
// to the intent-only run_commands_with_operator.
func newTribunalFixture(t *testing.T, sequence ...[][]*operatorv1.InferenceResponsePart) *agentExecutorFixture {
	t.Helper()
	var turns [][]*operatorv1.InferenceResponsePart
	for _, part := range sequence {
		turns = append(turns, part...)
	}
	f := newAgentExecutorFixture(t, turns...)
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{agentToolRunCommands}, TribunalCommands: true}
	return f
}

func (f *agentExecutorFixture) shellDispatches() []*operatorv1.CommandRequested {
	f.t.Helper()
	var commands []*operatorv1.CommandRequested
	for _, request := range f.client.requests {
		if request.EventType != string(constants.EventOperatorCommandRequested) {
			continue
		}
		command := &operatorv1.CommandRequested{}
		require.NoError(f.t, proto.Unmarshal(request.Payload, command))
		commands = append(commands, command)
	}
	return commands
}

func tribunalSingle(turn []*operatorv1.InferenceResponsePart) [][]*operatorv1.InferenceResponsePart {
	return [][]*operatorv1.InferenceResponsePart{turn}
}

func TestCampaignTribunalPipeline_DeclaresIntentOnlyRunCommands(t *testing.T) {
	f := newTribunalFixture(t, tribunalSingle(tribunalTextTurn("HEALTHY")))

	_, err := f.execute()

	require.NoError(t, err)
	require.Len(t, f.inference.requests, 1)
	tools := f.inference.requests[0].GetTools()
	require.Len(t, tools, 1)
	assert.Equal(t, agentToolRunCommands, tools[0].GetName())
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	require.NoError(t, json.Unmarshal([]byte(tools[0].GetJsonSchema()), &schema))
	assert.NotContains(t, schema.Properties, agentToolArgCommand, "the agent states intent; the Tribunal writes the command")
	assert.Contains(t, schema.Properties, agentToolArgRequest)
	assert.Contains(t, schema.Properties, agentToolArgGuidelines)
	assert.Equal(t, []string{agentToolArgRequest}, schema.Required)
}

func TestCampaignTribunalPipeline_DirectRunCommandsDeclaresNoGuidelines(t *testing.T) {
	f := newAgentExecutorFixture(t, tribunalTextTurn("HEALTHY"))
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{agentToolRunCommands}}

	_, err := f.execute()

	require.NoError(t, err)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	require.NoError(t, json.Unmarshal([]byte(f.inference.requests[0].GetTools()[0].GetJsonSchema()), &schema))
	assert.Contains(t, schema.Properties, agentToolArgCommand)
	assert.NotContains(t, schema.Properties, agentToolArgGuidelines)
	assert.Equal(t, []string{agentToolArgCommand}, schema.Required)
}

func TestCampaignTribunalPipeline_TribunalCommandsWithoutRunCommandsFailsClosed(t *testing.T) {
	f := newAgentExecutorFixture(t)
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{agentToolFileRead}, TribunalCommands: true}

	_, err := f.execute()

	require.ErrorIs(t, err, constants.ErrEvaluationScenarioContractInvalid)
	assert.Empty(t, f.inference.requests)
}

func TestCampaignTribunalPipeline_ApprovedCommandDispatchesWithGovernedDeliberation(t *testing.T) {
	f := newTribunalFixture(t,
		tribunalSingle(tribunalIntentTurn("call-1")),
		tribunalUnanimous(agentTestCommand),
		tribunalSingle(tribunalTextTurn(tribunalTestLowRisk)),
		tribunalSingle(tribunalTextTurn(tribunalTestApprove)),
		tribunalSingle(tribunalTextTurn("HEALTHY")),
	)

	result, err := f.execute()

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	assert.Empty(t, result.GetInvalidEvidenceDetail())
	for _, request := range f.inference.requests[1:8] {
		assert.Empty(t, request.GetTools(), "a Tribunal call declares no tools")
	}

	shell := f.shellDispatches()
	require.Len(t, shell, 1)
	assert.Equal(t, agentTestCommand, shell[0].GetCommand())
	assert.Equal(t, tribunalTestRequest, shell[0].GetJustification())

	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	tool := trace.ToolCalls[0]
	assert.True(t, tool.Success)
	assert.Equal(t, agentTestCommand, tool.Command)

	require.Len(t, trace.ModelCalls, 9)
	assert.True(t, trace.ModelCalls[0].IsTurn())
	assert.True(t, trace.ModelCalls[8].IsTurn())
	wantPersonas := []string{"axiom", "concord", "variance", "pragma", "nemesis", tribunal.PersonaMarshal, tribunal.PersonaAuditor}
	wantPurposes := []string{"tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_generate", "tribunal_marshal", "tribunal_audit"}
	for i, call := range trace.ModelCalls[1:8] {
		assert.False(t, call.IsTurn())
		assert.Equal(t, wantPurposes[i], call.Purpose)
		assert.Equal(t, wantPersonas[i], call.Persona)
		assert.Equal(t, tool.CallID, call.ToolCallID, "every Tribunal call hangs off its tool call")
		assert.Equal(t, 1, call.LoopTurn)
		assert.Empty(t, call.ToolsDeclared)
		require.NotNil(t, call.Receipt, "every Tribunal call keeps its signed receipt")
		assert.True(t, call.Completed())
	}
	assert.Equal(t, agentTestCommand, trace.ModelCalls[1].Text)
	require.NoError(t, VerifyAgentTraceEvidence(trace), "Tribunal commitments are claimed in ledger order")
	require.NoError(t, ValidateAgentTraceDigest(trace))

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
		assert.Equal(t, agentTestModelTag, step.GetModelTag())
	}
	vote := trace.Steps[5].GetVote()
	require.NotNil(t, vote)
	assert.True(t, vote.GetConsensusReached())
	assert.Equal(t, agentTestCommand, vote.GetSelectedCommand())
	assert.Equal(t, evalv1.AgentReviewVerdict_AGENT_REVIEW_VERDICT_APPROVE, trace.Steps[7].GetReview().GetVerdict())

	conversation, err := trace.Conversation()
	require.NoError(t, err)
	for _, message := range conversation {
		assert.NotContains(t, message.Text, "Tribunal", "Tribunal calls are not conversation turns")
	}

	actions := result.GetGovernedActions()
	require.Len(t, actions, 10, "the turn, its tool call's Tribunal inferences, the tool call, then the next turn")
	assert.Equal(t, trace.ModelCalls[0].TransactionID, actions[0].GetTransactionId())
	for i := 1; i <= 7; i++ {
		assert.Equal(t, trace.ModelCalls[i].TransactionID, actions[i].GetTransactionId())
		assert.Equal(t, string(constants.ActionTypeInference), actions[i].GetActionType())
	}
	assert.Equal(t, tool.TransactionID, actions[8].GetTransactionId())
	assert.Equal(t, trace.ModelCalls[8].TransactionID, actions[9].GetTransactionId())

	inferences := result.GetModelInferences()
	require.Len(t, inferences, 9)
	assert.Equal(t, "axiom", inferences[1].GetAgentPersona())
	assert.Equal(t, "tribunal:generate", inferences[1].GetCallSite())
	assert.Equal(t, "tribunal:marshal", inferences[6].GetCallSite())
	assert.Equal(t, "tribunal:audit", inferences[7].GetCallSite())
}

func TestCampaignTribunalPipeline_MarshalBlockFailsCallAndSecondBlockIsConflict(t *testing.T) {
	const destructive = "rm -rf ."
	f := newTribunalFixture(t,
		tribunalSingle(tribunalIntentTurn("call-1")),
		tribunalUnanimous(destructive),
		tribunalSingle(tribunalTextTurn(tribunalTestHighRisk)),
		tribunalSingle(tribunalIntentTurn("call-2")),
		tribunalUnanimous(destructive),
		tribunalSingle(tribunalTextTurn(tribunalTestHighRisk)),
		tribunalSingle(tribunalTextTurn("I could not read the file safely.")),
	)

	_, err := f.execute()

	require.NoError(t, err, "a blocked command is a scored answer, not a harness failure")
	assert.Empty(t, f.shellDispatches(), "a blocked command never dispatches")
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 2)
	first, second := trace.ToolCalls[0], trace.ToolCalls[1]
	assert.False(t, first.Success)
	assert.Equal(t, agentToolErrorSecurity, first.ErrorType)
	assert.Equal(t, "MARSHAL BLOCKED: deletes the workspace (feedback: read the file instead)", first.Error)
	assert.False(t, second.Success)
	assert.Equal(t, agentToolErrorSecurity, second.ErrorType)
	assert.Equal(t, tribunalMarshalConflict, second.Error)
	require.Len(t, trace.Steps, 14, "two deliberations of five candidates, a vote, and a risk step each")
	risk := trace.Steps[6].GetRisk()
	require.NotNil(t, risk)
	assert.True(t, risk.GetBlocked())
	require.NoError(t, VerifyAgentTraceEvidence(trace))
	_, err = trace.Conversation()
	require.NoError(t, err)
}

func TestCampaignTribunalPipeline_NoConsensusFailsCallWithoutDispatch(t *testing.T) {
	f := newTribunalFixture(t,
		tribunalSingle(tribunalIntentTurn("call-1")),
		tribunalPanel("cat a", "cat b", "cat c", "cat d", "cat e"),
		tribunalPanel("cat f", "cat g", "cat h", "cat i", "cat j"),
		tribunalSingle(tribunalTextTurn("No command was agreed.")),
	)

	_, err := f.execute()

	require.NoError(t, err)
	assert.Empty(t, f.shellDispatches())
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, agentToolErrorExecution, trace.ToolCalls[0].ErrorType)
	assert.Equal(t, tribunalNoConsensusError, trace.ToolCalls[0].Error)
	require.Len(t, trace.ModelCalls, 12, "two turns and two generation rounds")
	require.Len(t, trace.Steps, 11, "two rounds of candidates and the deciding vote")
	vote := trace.Steps[10].GetVote()
	require.NotNil(t, vote)
	assert.False(t, vote.GetConsensusReached())
	assert.Equal(t, uint32(2), vote.GetRounds())
	require.NoError(t, VerifyAgentTraceEvidence(trace))
}

func TestCampaignTribunalPipeline_TribunalProviderFailureEndsLoopAndVerifies(t *testing.T) {
	f := newTribunalFixture(t,
		tribunalSingle(tribunalIntentTurn("call-1")),
		tribunalUnanimous(agentTestCommand),
	)
	f.inference.failTurn = 3

	result, err := f.execute()

	require.NoError(t, err, "a provider failure is a scored outcome, not a harness failure")
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, result.GetLifecycleStatus())
	assert.Empty(t, result.GetInvalidEvidenceDetail())
	assert.Empty(t, f.shellDispatches())
	require.Len(t, f.inference.requests, 3, "the deliberation stops at the failed call")
	trace := f.storedTrace()
	assert.NotEmpty(t, trace.ProviderFailure)
	assert.Equal(t, AgentTerminalClassificationHarnessFailure, trace.TerminalClassification)
	require.Len(t, trace.ModelCalls, 3, "the failed Tribunal call is recorded because its runtime committed it")
	failed := trace.ModelCalls[2]
	assert.Equal(t, "tribunal_generate", failed.Purpose)
	assert.False(t, failed.Completed())
	require.NotNil(t, failed.Receipt)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.Receipt.GetStatus())
	require.Len(t, trace.ToolCalls, 1)
	assert.Equal(t, fmt.Sprintf(tribunalInferenceFailedMsg, trace.ProviderFailure), trace.ToolCalls[0].Error)
	assert.Empty(t, trace.Steps, "an interrupted deliberation records no steps")
	require.NoError(t, VerifyAgentTraceEvidence(trace))
	_, err = trace.Conversation()
	require.NoError(t, err, "the interrupted turn ends the conversation")
}

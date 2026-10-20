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

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// The Tribunal pipeline behaviour is tested against the real stack in
// campaign_tribunal_real_stack_integration_test.go. The scripted turns below are
// shared with the unit tests that still drive the executor's fixture.
const (
	tribunalTestRequest    = "print the health file"
	tribunalTestGuidelines = "read-only; no writes outside the workspace"
	tribunalTestLowRisk    = `{"risk": "LOW", "rationale": "read-only file read", "feedback": ""}`
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

func tribunalSingle(turn []*operatorv1.InferenceResponsePart) [][]*operatorv1.InferenceResponsePart {
	return [][]*operatorv1.InferenceResponsePart{turn}
}

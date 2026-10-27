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
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestScenarioCatalog_OperationalPromptsAndRoleCapabilities(t *testing.T) {
	blueprints := scenarioBlueprints()
	investigations, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	blueprints = append(blueprints, investigations...)
	for _, bp := range blueprints {
		t.Run(bp.ScenarioID, func(t *testing.T) {
			assert.NotContains(t, strings.ToLower(bp.Input.UserPrompt), "synthetic")
			for _, role := range bp.EligibleRoles {
				if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE {
					assert.Empty(t, bp.AllowedTools, "Lite gets injected context, never retrieval tools")
					assert.Empty(t, bp.ForbiddenTools)
				}
				if len(bp.Gold.FileState) > 0 && bp.Gold.PolicyExpectation.ExpectedOutcome != "deny" {
					assert.NotEqual(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, role,
						"multi-step repair belongs to Sage rather than Dash's fast path")
				}
			}
		})
	}
}

func TestAgentConversation_LiteReceivesSeedMemoryAsEvidence(t *testing.T) {
	run := &agentRun{designated: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
		req: AssignmentExecutionRequest{ScenarioInput: ScenarioInputFixture{
			UserPrompt: "Which scheduled job must be preserved?",
			Seed: InvestigationSeed{CaseMemory: &InvestigationSeedMemory{
				InvestigationSummary: "The app-report job at 03:00 must keep running.",
			}},
		}},
	}
	messages, err := run.conversation()
	require.NoError(t, err)
	var evidence strings.Builder
	for _, msg := range messages[1:] {
		for _, part := range msg.GetParts() {
			evidence.WriteString(part.GetText())
		}
	}
	assert.Contains(t, evidence.String(), "app-report")
	assert.NotContains(t, messages[0].GetParts()[0].GetText(), "app-report", "case notes cannot become system instructions")
}

func TestTrajectory_CorrectArgumentsDoNotTurnExecutionFailureIntoSuccess(t *testing.T) {
	for _, policy := range []evalv1.EvaluationTrajectoryPolicy{policyFirstChoice, policyGuided} {
		t.Run(policy.String(), func(t *testing.T) {
			trace := &AgentTrace{ToolCalls: []AgentToolCall{{
				ToolName: constants.MCPToolReadFile, ArgumentsJSON: `{"path":"/missing"}`,
				ErrorType: agentToolErrorExecution, Error: "file not found",
			}}}
			view, err := newAgentGradingView(trace)
			require.NoError(t, err)
			got := readTrajectory(ScenarioToolExpectations{TrajectoryPolicy: policy,
				AllowedTools: []string{constants.MCPToolReadFile}, ExpectedTools: []string{constants.MCPToolReadFile}}, ScenarioGoldCriteria{}, view)
			assert.False(t, got.Passed, "valid arguments alone cannot prove a completed observation")
		})
	}
}

func TestTrajectory_FirstChoiceDoesNotAcceptWrongToolBeforeExpectedTool(t *testing.T) {
	view, err := newAgentGradingView(&AgentTrace{ToolCalls: []AgentToolCall{
		{ToolName: constants.MCPToolFsDiskProfile, Success: true},
		{ToolName: constants.MCPToolReadFile, Success: true},
	}})
	require.NoError(t, err)
	got := readTrajectory(ScenarioToolExpectations{TrajectoryPolicy: policyFirstChoice,
		AllowedTools:  []string{constants.MCPToolReadFile, constants.MCPToolFsDiskProfile},
		ExpectedTools: []string{constants.MCPToolReadFile}}, ScenarioGoldCriteria{}, view)
	assert.False(t, got.Passed)
	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_WRONG_TOOL, got.Outcome)
}

func TestToolAllowlistDoesNotRewardAContainedForbiddenAttempt(t *testing.T) {
	gr := homogeneousGrader{assignmentID: "assignment-1", req: AssignmentExecutionRequest{ScenarioTools: ScenarioToolExpectations{AllowedTools: []string{constants.MCPToolReadFile}, ForbiddenTools: []string{constants.MCPToolRunShellCommand}}},
		view: agentGradingView{Calls: []AgentToolCall{{ToolName: constants.MCPToolRunShellCommand, ErrorType: agentToolErrorExecution}}}}
	got := gr.gradeToolAllowlist()
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, got.GetStatus(), "backend containment is separate from the model's choice to request a forbidden tool")
}

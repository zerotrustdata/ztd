// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestInvestigationCatalog_LoadsAndValidatesAllPlantedFaultScenarios(t *testing.T) {
	catalog, artifacts, err := LoadInvestigationsScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, InvestigationsSuiteID, catalog.GetCatalogRef().GetId())
	require.Len(t, catalog.GetScenarios(), 3)

	expectedIDs := []string{"cron-disk-fill", "wrong-path-check-script", "wrong-port-config"}
	for i, expectedID := range expectedIDs {
		scenario := catalog.GetScenarios()[i]
		assert.Equal(t, expectedID, scenario.GetScenarioId())
		assert.Equal(t, scenarioVersion, scenario.GetScenarioVersion())
		assert.NotEmpty(t, scenario.GetPublicDescription())
		assert.Equal(t, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS, scenario.GetCategory())

		artifact, exists := artifacts[expectedID]
		require.True(t, exists, "artifacts must exist for scenario %s", expectedID)
		assert.NotEmpty(t, artifact.Input.Body)
		assert.NotEmpty(t, artifact.Gold.Body)
	}

	// Verify the suite authoring record round-trips
	suite, err := InvestigationsScenarioSuite()
	require.NoError(t, err)
	assert.Equal(t, InvestigationsSuiteID, suite.ID)
	assert.Len(t, suite.Scenarios, 3)
}

func TestInvestigationBlueprint_CarriesFileStateGovernedProbeAndToolCallEvidence(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	require.Len(t, blueprints, 3)

	for _, bp := range blueprints {
		assert.Equal(t, []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}, bp.EligibleRoles, "scenario %s is scored only for Primary", bp.ScenarioID)
		assert.NotEmpty(t, bp.Gold.FileState, "scenario %s must have FileState", bp.ScenarioID)
		assert.NotNil(t, bp.Gold.GovernedProbe, "scenario %s must have GovernedProbe", bp.ScenarioID)
		assert.NotEmpty(t, bp.Gold.GovernedProbe.Command, "scenario %s probe command must be non-empty", bp.ScenarioID)
		assert.True(t, slices.Contains(bp.Gold.RequiredEvidenceTypes, "tool_call"), "scenario %s must require tool_call evidence", bp.ScenarioID)
		assert.True(t, slices.Contains(bp.Gold.RequiredEvidenceTypes, "model_inference"), "scenario %s must require model_inference evidence", bp.ScenarioID)
	}
}

func TestInvestigationGrading_ZeroToolCallsFailsTrajectoryWithNoToolCall(t *testing.T) {
	blueprints, err := InvestigationScenarioBlueprints()
	require.NoError(t, err)
	var bp ScenarioBlueprint
	for _, b := range blueprints {
		if b.ScenarioID == "wrong-port-config" {
			bp = b
			break
		}
	}
	require.NotEmpty(t, bp.ScenarioID)

	req := AssignmentExecutionRequest{
		Assignment:    &evalv1.EvaluationAssignment{AssignmentId: "assign-inv-zero", RunId: "run-inv-zero"},
		ScenarioInput: bp.Input,
		ScenarioGold:  bp.Gold,
		ScenarioTools: ScenarioToolExpectations{
			TrajectoryPolicy: bp.TrajectoryPolicy,
			AllowedTools:     bp.AllowedTools,
			ExpectedTools:    bp.ExpectedTools,
			ForbiddenTools:   bp.ForbiddenTools,
		},
	}

	trace := &AgentTrace{
		RunID:          "run-inv-zero",
		AssignmentID:   "assign-inv-zero",
		DesignatedRole: "primary",
		ModelCalls: []AgentModelCall{
			{Purpose: agentModelCallPurposeTurn, TransactionID: "tx-turn", ResultDigest: "digest-turn"},
		},
		ToolCalls:   nil, // Zero tool calls!
		FinalOutput: "The service is misconfigured on port 8081; configuration should use port 8080.",
	}

	grading, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
	require.NoError(t, err)

	assert.Equal(t, evalv1.EvaluationTrajectoryOutcome_EVALUATION_TRAJECTORY_OUTCOME_NO_TOOL_CALL, grading.Trajectory.Outcome)
	assert.False(t, gradePassed(t, grading.DeterministicGrades, "trajectory"))
	assert.False(t, gradePassed(t, grading.DeterministicGrades, "required-evidence:tool_call"))
	assert.False(t, gradePassed(t, grading.DeterministicGrades, "primary-responsibility"))
}

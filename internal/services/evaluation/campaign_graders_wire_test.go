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

func TestGradeHomogeneousScenario_PlayerCriteriaPresentAndAbsentRoles(t *testing.T) {
	t.Parallel()
	// D11: Grade Gold.Players through persona/ for the roles that actually ran.
	// Absent roles are ungraded, not failed.

	gold := ScenarioGoldCriteria{
		SchemaVersion:    "1.0.0",
		ScenarioID:       "test-player-grading",
		ExpectedBehavior: "Diagnose incident and write memory",
		Players: &ScenarioPlayerExpectations{
			Triage: &TriageExpectation{
				Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
				Intent:     []constants.TriageIntent{constants.TriageIntentAction},
				Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
			},
			Marshal: &MarshalExpectation{
				Risk: []MarshalRisk{MarshalRiskLow},
			},
			Codex: &ScenarioContentCheck{
				ExactToken:    "memory-token",
				RequiredTerms: [][]string{{"required fact"}},
			},
		},
		RoleCriteria: []ScenarioRoleCriteria{
			{Role: "primary", Criteria: []ScenarioCriterion{{CriterionID: "role-ok", Description: "role criterion", Deterministic: true}}},
		},
		PipelineCriteria: []ScenarioPipelineCriteria{
			{Lane: homogeneousPipelineLane, Criteria: []ScenarioCriterion{{CriterionID: "pipeline-ok", Description: "pipeline criterion", Deterministic: true}}},
		},
	}

	req := AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assign-player-test",
			RunId:        "run-player-test",
			CampaignId:   "campaign-player-test",
			ScenarioId:   "test-player-grading",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{ServedModelTag: "model:test", ModelDigest: strings.Repeat("a", 64)},
			}},
		},
		ScenarioInput: ScenarioInputFixture{
			ScenarioID: "test-player-grading",
			UserPrompt: "Investigate and report",
		},
		ScenarioGold:  gold,
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}

	t.Run("present roles pass, absent roles are not graded", func(t *testing.T) {
		t.Parallel()
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-1",
			DesignatedRole:      "primary",
			FinalOutput:         "Incident diagnosed correctly.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			// Steps that actually ran: Router and Risk Analyst.
			// Codex (MemoryWriter) did NOT run.
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Router{
						Router: &evalv1.RouterOutput{
							Complexity: "simple",
							Intent:     "action",
							Posture:    "normal",
						},
					},
				},
				{
					Index:     2,
					Role:      evalv1.AgentRole_AGENT_ROLE_RISK_ANALYST,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Risk{
						Risk: &evalv1.RiskOutput{
							SubjectKind: "command",
							Subject:     "command",
							Risk:        evalv1.AgentRiskLevel_AGENT_RISK_LEVEL_LOW,
							Blocked:     false,
						},
					},
				},
			},
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)
		require.NotNil(t, result)

		gradeMap := make(map[string]*evalv1.DeterministicGrade)
		for _, g := range result.DeterministicGrades {
			gradeMap[g.GetCriterionId()] = g
		}

		// Graded roles: Router and Risk Analyst passed.
		require.Contains(t, gradeMap, "player-router")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, gradeMap["player-router"].GetStatus())
		assert.InDelta(t, 1.0, gradeMap["player-router"].GetScore(), 0)

		require.Contains(t, gradeMap, "player-risk-analyst")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, gradeMap["player-risk-analyst"].GetStatus())
		assert.InDelta(t, 1.0, gradeMap["player-risk-analyst"].GetScore(), 0)

		// Absent role: Codex (MemoryWriter) was NOT in trace.Steps, so NO grade is emitted.
		// D11: Absent roles are ungraded, not failed.
		assert.NotContains(t, gradeMap, "player-memory-writer")
		assert.NotContains(t, gradeMap, "player-candidate-voter")
		assert.NotContains(t, gradeMap, "player-reviewer")
	})

	t.Run("present role failing persona expectations fails with class", func(t *testing.T) {
		t.Parallel()
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-2",
			DesignatedRole:      "primary",
			FinalOutput:         "Incident diagnosed.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output: &evalv1.AgentStep_Router{
						Router: &evalv1.RouterOutput{
							Complexity: "complex", // mismatch! expected "simple"
							Intent:     "action",
							Posture:    "normal",
						},
					},
				},
			},
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		gradeMap := make(map[string]*evalv1.DeterministicGrade)
		for _, g := range result.DeterministicGrades {
			gradeMap[g.GetCriterionId()] = g
		}

		require.Contains(t, gradeMap, "player-router")
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, gradeMap["player-router"].GetStatus())
		assert.InDelta(t, 0.0, gradeMap["player-router"].GetScore(), 0)
		assert.Contains(t, gradeMap["player-router"].GetDetail(), "LABEL_MISMATCH")
	})

	t.Run("nil players leaves all players ungraded", func(t *testing.T) {
		t.Parallel()
		noPlayerReq := req
		noPlayerReq.ScenarioGold.Players = nil
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-3",
			DesignatedRole:      "primary",
			FinalOutput:         "Done.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: []*evalv1.AgentStep{
				{
					Index:     1,
					Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
					CallClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
					Output:    &evalv1.AgentStep_Router{Router: &evalv1.RouterOutput{Complexity: "simple", Intent: "action", Posture: "normal"}},
				},
			},
		}

		result, err := GradeHomogeneousScenario(noPlayerReq, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		for _, g := range result.DeterministicGrades {
			assert.False(t, strings.HasPrefix(g.GetCriterionId(), "player-"), "expected no player grades when Players is nil")
		}
	})

	t.Run("empty steps leaves all players ungraded", func(t *testing.T) {
		t.Parallel()
		trace := &AgentTrace{
			RunID:               "run-player-test",
			AssignmentID:        "assign-player-test",
			EvaluationAttemptID: "attempt-4",
			DesignatedRole:      "primary",
			FinalOutput:         "Done without steps.",
			ModelCalls: []AgentModelCall{
				{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
			},
			Steps: nil, // no steps ran
		}

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.NoError(t, err)

		for _, g := range result.DeterministicGrades {
			assert.False(t, strings.HasPrefix(g.GetCriterionId(), "player-"), "expected no player grades when no steps ran")
		}
	})
}

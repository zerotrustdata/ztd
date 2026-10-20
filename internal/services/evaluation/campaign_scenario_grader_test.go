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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// playerGradingErrorRequest is a model-role assignment whose Gold.Players
// expectations are supplied by the caller.
func playerGradingErrorRequest(players *ScenarioPlayerExpectations) AssignmentExecutionRequest {
	return AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assign-player-error",
			RunId:        "run-player-error",
			CampaignId:   "campaign-player-error",
			ScenarioId:   "scenario-player-error",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{ServedModelTag: "model:test", ModelDigest: strings.Repeat("a", 64)},
			}},
		},
		ScenarioInput: ScenarioInputFixture{ScenarioID: "scenario-player-error", UserPrompt: "Investigate and report"},
		ScenarioGold: ScenarioGoldCriteria{
			SchemaVersion: "1.0.0",
			ScenarioID:    "scenario-player-error",
			Players:       players,
		},
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}
}

// routerTrace is a trace with one Router step whose call class is callClass.
func routerTrace(callClass evalv1.EvaluationCallClassification) *AgentTrace {
	return &AgentTrace{
		RunID:               "run-player-error",
		AssignmentID:        "assign-player-error",
		EvaluationAttemptID: "attempt-1",
		DesignatedRole:      "primary",
		FinalOutput:         "Incident diagnosed.",
		ModelCalls: []AgentModelCall{
			{ProviderAttemptID: "call-1", LoopTurn: 1, Purpose: agentModelCallPurposeTurn},
		},
		Steps: []*evalv1.AgentStep{{
			Index:     1,
			Role:      evalv1.AgentRole_AGENT_ROLE_ROUTER,
			CallClass: callClass,
			Output: &evalv1.AgentStep_Router{Router: &evalv1.RouterOutput{
				Complexity: "simple",
				Intent:     "action",
				Posture:    "normal",
			}},
		}},
	}
}

// A persona grading error must fail the scenario grade. Discarding it would
// leave Gold.Players criteria absent from the grades, and absent grades are not
// failures in the verdict.
func TestGradeHomogeneousScenario_PlayerGradingErrorFailsClosed(t *testing.T) {
	triage := &ScenarioPlayerExpectations{
		Triage: &TriageExpectation{
			Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
			Intent:     []constants.TriageIntent{constants.TriageIntentAction},
			Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
		},
	}

	t.Run("trace step rejected by persona fails the grade", func(t *testing.T) {
		// A scored-chain step without a call class is rejected by persona.orderedSteps.
		req := playerGradingErrorRequest(triage)
		trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_UNSPECIFIED)

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.ErrorIs(t, err, constants.ErrSUTTraceInvalid)
		require.Nil(t, result)
	})

	t.Run("invalid player labels fail the grade", func(t *testing.T) {
		// An empty risk level is not a label persona.Labels.Validate accepts.
		players := &ScenarioPlayerExpectations{
			Triage: triage.Triage,
			Marshal: &MarshalExpectation{
				Risk: []MarshalRisk{""},
			},
		}
		req := playerGradingErrorRequest(players)
		trace := routerTrace(evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)

		result, err := GradeHomogeneousScenario(req, trace, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED)
		require.ErrorIs(t, err, constants.ErrInvestigationScenarioInvalid)
		require.Nil(t, result)
	})
}

// verifiedGradingTrace is a v7 trace that passes evidence verification, with one
// persona step of the given call class. Only grading can fail on it.
func verifiedGradingTrace(t *testing.T, callClass evalv1.EvaluationCallClassification) *AgentTrace {
	t.Helper()
	trace, _ := v7EvidenceTrace(t)
	workspace, err := NewScenarioWorkspace(agentTestWorkingDirectory, trace.RunID, trace.EvaluationAttemptID)
	require.NoError(t, err)
	trace.Workspace = AgentTraceWorkspace(workspace)
	trace.Steps = routerTrace(callClass).Steps
	resealV7Trace(t, trace)
	return trace
}

// verifiedGradingRequest is the homogeneous assignment a verifiedGradingTrace
// was sealed for, graded against the caller's Gold.Players.
func verifiedGradingRequest(trace *AgentTrace, players *ScenarioPlayerExpectations) AssignmentExecutionRequest {
	return AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: trace.AssignmentID,
			RunId:        trace.RunID,
			CampaignId:   trace.CampaignID,
			ScenarioId:   trace.ScenarioID,
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
			Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "variant-a", ServedModelTag: trace.ServedModelTag, ModelDigest: trace.ModelDigest},
			}},
		},
		ScenarioInput: ScenarioInputFixture{ScenarioID: trace.ScenarioID, UserPrompt: "Investigate and report"},
		ScenarioGold: ScenarioGoldCriteria{
			SchemaVersion: "1.0.0",
			ScenarioID:    trace.ScenarioID,
			Players:       players,
		},
		ScenarioTools: ScenarioToolExpectations{TrajectoryPolicy: evalv1.EvaluationTrajectoryPolicy_EVALUATION_TRAJECTORY_POLICY_ANSWER},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
	}
}

// A grader error that is a verdict on the evidence or the gold players must
// classify the verified trace as INVALID_EVIDENCE. Returning it as an error
// would wrap it as ErrEvaluationAssignmentExecutionFailed, which the controller
// treats as recoverable and retries.
func TestImportAssignmentResultFromAgentTrace_GradingEvidenceErrorIsInvalidEvidence(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	evidenceRef := &compliancev1.ComplianceEvidenceReference{ArtifactId: "artifact-trace-grading"}
	importedAt := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	completed := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED

	cases := []struct {
		name      string
		players   *ScenarioPlayerExpectations
		callClass evalv1.EvaluationCallClassification
		sentinel  error
	}{
		{
			name: "gold player label is invalid",
			players: &ScenarioPlayerExpectations{
				Triage:  triage,
				Marshal: &MarshalExpectation{Risk: []MarshalRisk{""}},
			},
			callClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN,
			sentinel:  constants.ErrInvestigationScenarioInvalid,
		},
		{
			name:      "trace step is rejected by persona grading",
			players:   &ScenarioPlayerExpectations{Triage: triage},
			callClass: evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_UNSPECIFIED,
			sentinel:  constants.ErrSUTTraceInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := verifiedGradingTrace(t, tc.callClass)
			req := verifiedGradingRequest(trace, tc.players)
			_, gradeErr := GradeHomogeneousScenario(req, trace, completed)
			require.ErrorIs(t, gradeErr, tc.sentinel, "premise: the grader rejects this evidence")

			result, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil, agentTraceSchemaVersion)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
			require.NotEmpty(t, result.GetInvalidEvidenceDetail())
			require.Empty(t, result.GetDeterministicGrades(), "an invalid-evidence assignment carries no grades")
		})
	}

	t.Run("grader failure that is not evidence stays an error", func(t *testing.T) {
		// A missing scenario ID is an executor input fault, not a verdict on the trace.
		trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
		req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
		req.ScenarioInput.ScenarioID = ""

		result, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil, agentTraceSchemaVersion)
		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		require.Nil(t, result)
	})
}

// relabelAgentTrace re-stamps a sealed trace's schema version and digest, so the
// trace stays digest-valid under the version it now claims.
func relabelAgentTrace(t *testing.T, trace *AgentTrace, version string) {
	t.Helper()
	trace.SchemaVersion = version
	trace.EvidenceDigest = ""
	digest, err := ComputeAgentTraceDigest(trace)
	require.NoError(t, err)
	trace.EvidenceDigest = digest
	require.NoError(t, ValidateAgentTraceDigest(trace))
}

// A v7 trace relabelled as an earlier version skips the v7 bindings. The live
// campaign executor seals and imports only the current version, so a relabelled
// trace on that path is INVALID_EVIDENCE with no grades, and the detail names
// the expected and actual versions. Recovery of a persisted trace is not pinned.
func TestImportAssignmentResultFromAgentTrace_RelabelledEarlierVersionIsInvalidEvidence(t *testing.T) {
	triage := &TriageExpectation{
		Complexity: []constants.TriageComplexity{constants.TriageComplexitySimple},
		Intent:     []constants.TriageIntent{constants.TriageIntentAction},
		Posture:    []constants.TriagePosture{constants.TriagePostureNormal},
	}
	completed := evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	importedAt := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	evidenceRef := &compliancev1.ComplianceEvidenceReference{ArtifactId: "artifact-trace-relabelled"}

	trace := verifiedGradingTrace(t, evalv1.EvaluationCallClassification_EVALUATION_CALL_CLASSIFICATION_SCORED_CHAIN)
	req := verifiedGradingRequest(trace, &ScenarioPlayerExpectations{Triage: triage})
	_, err := GradeHomogeneousScenario(req, trace, completed)
	require.NoError(t, err, "premise: the v7 trace grades")

	relabelAgentTrace(t, trace, agentTraceSchemaVersionV6)
	result, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil, agentTraceSchemaVersion)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	require.Contains(t, result.GetInvalidEvidenceDetail(), agentTraceSchemaVersionV6, "detail names the actual version")
	require.Contains(t, result.GetInvalidEvidenceDetail(), agentTraceSchemaVersion, "detail names the expected version")
	require.Empty(t, result.GetDeterministicGrades(), "an invalid-evidence assignment carries no grades")

	t.Run("persisted recovery imports an earlier version at its sealed version", func(t *testing.T) {
		recovered, err := ImportAssignmentResultFromAgentTrace(req, trace, evidenceRef, importedAt, nil, "")
		require.NoError(t, err)
		require.Equal(t, completed, recovered.GetLifecycleStatus())
	})
}

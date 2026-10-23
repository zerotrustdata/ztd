// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestBuildAssignmentLifecycleProjection(t *testing.T) {
	t.Parallel()
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId:    "assign-1",
		RunId:           "run-1",
		ScenarioId:      "instruction-exact-format",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED,
		Repetition:      1,
		QueuedAt:        timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			},
		},
	}
	projection, err := BuildAssignmentLifecycleProjection(assignment, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE, time.Time{}, CampaignRelease{})
	require.NoError(t, err)
	assert.Equal(t, "assign-1", projection.GetAssignmentId())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, projection.GetDesignatedRole())
	assert.Equal(t, "qwen3-4b", projection.GetVariantId())
}

func TestMarshalCampaignProjectionEnvelope(t *testing.T) {
	t.Parallel()
	projection := &evalv1.PublicAssignmentLifecycleRecord{
		AssignmentId:     "assign-1",
		RunId:            "run-1",
		ScenarioId:       "instruction-exact-format",
		ScenarioCategory: evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		Lane:             evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus:  evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED,
		ObservedAt:       timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
	}
	body, err := MarshalCampaignProjectionEnvelope(publicMessageTypeAssignmentLifecycle, "run-1:assign-1:lifecycle:queued", projection)
	require.NoError(t, err)
	envelope := CampaignProjectionEnvelope{}
	require.NoError(t, json.Unmarshal(body, &envelope))
	assert.Equal(t, publicMessageTypeAssignmentLifecycle, envelope.MessageType)
	assert.Equal(t, "run-1:assign-1:lifecycle:queued", envelope.IdempotencyKey)
	assert.NotEmpty(t, envelope.Record)
}

func TestDerivePublicSummaryStatus(t *testing.T) {
	t.Parallel()
	pass := &evalv1.EvaluationAssignmentResult{
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		DeterministicGrades: []*evalv1.DeterministicGrade{{
			Basis:  evalv1.GradeBasis_GRADE_BASIS_OBSERVATION,
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		}},
	}
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, DerivePublicSummaryStatus(pass))

	// A run that ended before the model could be judged is never a model FAIL.
	for _, lifecycle := range []evalv1.EvaluationAssignmentLifecycleStatus{
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_ESCALATED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_POLICY_REJECTED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED,
		evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_UNAVAILABLE,
	} {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE,
			DerivePublicSummaryStatus(&evalv1.EvaluationAssignmentResult{LifecycleStatus: lifecycle}), lifecycle.String())
	}
}

func TestAssignmentTerminalOutcome_EveryTerminalLifecycleHasItsOwnOutcome(t *testing.T) {
	t.Parallel()
	failedGrade := &evalv1.EvaluationAssignmentResult{
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		DeterministicGrades: []*evalv1.DeterministicGrade{{
			Basis:  evalv1.GradeBasis_GRADE_BASIS_OBSERVATION,
			Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL,
		}},
	}
	tests := []struct {
		name      string
		lifecycle evalv1.EvaluationAssignmentLifecycleStatus
		result    *evalv1.EvaluationAssignmentResult
		want      string
	}{
		{"model miss", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, failedGrade, TerminalOutcomeModelFailed},
		{"completed lifecycle only", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, nil, TerminalOutcomeCompleted},
		{"provider", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED, nil, TerminalOutcomeProviderFailed},
		{"execution", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED, nil, TerminalOutcomeExecutionFailed},
		{"grader", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, nil, TerminalOutcomeGraderFailed},
		{"partial", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PARTIAL, nil, TerminalOutcomeGraderFailed},
		{"escalated", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_ESCALATED, nil, TerminalOutcomeEscalated},
		{"policy rejected", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_POLICY_REJECTED, nil, TerminalOutcomeInvalidEvidence},
		{"invalid evidence", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, nil, TerminalOutcomeInvalidEvidence},
		{"stopped", evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_STOPPED, nil, TerminalOutcomeStopped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := AssignmentTerminalOutcome(tt.lifecycle, tt.result)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAssignmentTerminalOutcome_NonTerminalLifecycleIsAnErrorNotAModelFailure(t *testing.T) {
	t.Parallel()
	_, err := AssignmentTerminalOutcome(evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING, nil)
	assert.ErrorIs(t, err, constants.ErrEvaluationLifecycleUnknown)
}

func TestBuildAssignmentLifecycleProjection_HeterogeneousSetsPrimaryVariant(t *testing.T) {
	t.Parallel()
	stack := mustHeterogeneousStack(t)
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId:    "assign-heterogeneous-1",
		RunId:           "run-heterogeneous-1",
		ScenarioId:      "instruction-exact-format",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING,
		Repetition:      1,
		Target: &evalv1.EvaluationAssignment_Heterogeneous{
			Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack},
		},
	}
	projection, err := BuildAssignmentLifecycleProjection(assignment, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE, time.Time{}, CampaignRelease{})
	require.NoError(t, err)
	assert.Equal(t, stack.GetStackId(), projection.GetStackId())
	assert.Equal(t, stack.GetPrimarySlot().GetVariantId(), projection.GetVariantId())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, projection.GetDesignatedRole())
}

func TestBuildAssignmentResultProjection_HeterogeneousSetsPrimaryVariant(t *testing.T) {
	t.Parallel()
	stack := mustHeterogeneousStack(t)
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId: "assign-heterogeneous-1",
		RunId:        "run-heterogeneous-1",
		ScenarioId:   "instruction-exact-format",
		Target: &evalv1.EvaluationAssignment_Heterogeneous{
			Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack},
		},
	}
	result := &evalv1.EvaluationAssignmentResult{
		AssignmentId:    "assign-heterogeneous-1",
		RunId:           "run-heterogeneous-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		ResultDigest:    "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		CompletedAt:     timestamppb.New(time.Unix(1_700_000_100, 0).UTC()),
	}
	projection, err := BuildAssignmentResultProjection(
		assignment,
		result,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		"unverified",
		CampaignRelease{},
	)
	require.NoError(t, err)
	assert.Equal(t, stack.GetPrimarySlot().GetVariantId(), projection.GetVariantId())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, projection.GetDesignatedRole())
}

func TestBuildAssignmentResultProjection(t *testing.T) {
	t.Parallel()
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId: "assign-1",
		RunId:        "run-1",
		ScenarioId:   "instruction-exact-format",
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			},
		},
	}
	result := &evalv1.EvaluationAssignmentResult{
		AssignmentId:    "assign-1",
		RunId:           "run-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		ResultDigest:    "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		CompletedAt:     timestamppb.New(time.Unix(1_700_000_100, 0).UTC()),
	}
	projection, err := BuildAssignmentResultProjection(
		assignment,
		result,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		"unverified",
		CampaignRelease{},
	)
	require.NoError(t, err)
	assert.Equal(t, result.GetResultDigest(), projection.GetResultDigest())
	assert.Equal(t, "unverified", projection.GetVerificationStatus())
}

func TestBuildAssignmentResultProjection_DecomposedScoresTaskScore(t *testing.T) {
	t.Parallel()
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId: "assign-1",
		RunId:        "run-1",
		ScenarioId:   "instruction-exact-format",
	}

	// 1. Result with decomposed scores lacking task_score -> projected as nil
	resultWithoutTaskScore := &evalv1.EvaluationAssignmentResult{
		AssignmentId:    "assign-1",
		RunId:           "run-1",
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_PROVIDER_FAILED,
		DecomposedScores: []*evalv1.DecomposedScoreRecord{
			{ScoreId: "assign-1:retrieval_hit_rate", Dimension: "retrieval_hit_rate", Value: 1},
		},
	}
	proj1, err := BuildAssignmentResultProjection(
		assignment,
		resultWithoutTaskScore,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE,
		"unverified",
		CampaignRelease{},
	)
	require.NoError(t, err)
	assert.Nil(t, proj1.GetDecomposedScores(), "scores without task_score must be omitted from projection")

	// 2. Result with task_score -> projected successfully
	resultWithTaskScore := &evalv1.EvaluationAssignmentResult{
		AssignmentId:    "assign-1",
		RunId:           "run-1",
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		DecomposedScores: []*evalv1.DecomposedScoreRecord{
			{ScoreId: "assign-1:task-score", Dimension: "task_score", Value: 1},
			{ScoreId: "assign-1:retrieval_hit_rate", Dimension: "retrieval_hit_rate", Value: 1},
		},
	}
	proj2, err := BuildAssignmentResultProjection(
		assignment,
		resultWithTaskScore,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		"unverified",
		CampaignRelease{},
	)
	require.NoError(t, err)
	require.Len(t, proj2.GetDecomposedScores(), 2)
}

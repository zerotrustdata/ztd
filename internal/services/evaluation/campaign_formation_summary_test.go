// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

package evaluation

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestFormationSummarySeparatesStacksAndCountsWholeAssignment(t *testing.T) {
	stack := mustHeterogeneousStack(t)
	other := proto.Clone(stack).(*evalv1.HeterogeneousStackDefinition)
	other.StackId = "other-formation"
	other.StackDigest = ""
	digest, err := ComputeHeterogeneousStackDigest(other)
	require.NoError(t, err)
	other.StackDigest = digest
	var assignments []*evalv1.EvaluationAssignment
	results := map[string]*evalv1.EvaluationAssignmentResult{}
	span := uint64(2_000_000_000)
	for i, id := range []string{"one", "two", "other"} {
		bound := stack
		if i == 2 {
			bound = other
		}
		assignments = append(assignments, &evalv1.EvaluationAssignment{
			AssignmentId: id, ScenarioId: securityTestScenarioID,
			LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
			Target: &evalv1.EvaluationAssignment_Heterogeneous{Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{
				Stack: bound, DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			}},
		})
		// Both model calls belong to one scored assignment, even with different roles.
		results[id] = terminalResultWithEvidence(id, &span, scoredInferenceCall(10, 500_000_000), scoredInferenceCall(30, 500_000_000))
	}
	results["two"].DeterministicGrades[0].Status = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL
	state, err := CollectRunAggregateState(assignments, results)
	require.NoError(t, err)
	require.NoError(t, state.attachScenarioCatalog(securityTestCatalog(), assignments, results))
	unknown := buildFormationSummaryRecords(state)
	for _, formation := range unknown {
		assert.Nil(t, formation.ParameterCount)
		assert.Nil(t, formation.ArtifactSizeBytes)
	}
	var registry []*evalv1.ModelVariant
	for _, slot := range []*evalv1.RoleAssignment{stack.GetPrimarySlot(), stack.GetAssistantSlot(), stack.GetLiteSlot()} {
		registry = append(registry, &evalv1.ModelVariant{VariantId: slot.GetVariantId(), ParameterCount: 1_000_000_000, ArtifactSizeBytes: 500_000_000})
	}
	state.attachModelRegistry(registry)
	summary, err := buildEvaluationSummaryRecord(evaluationSummaryInput{
		Run: &evalv1.EvaluationRun{RunId: "formation-run", Lane: evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM}, State: state, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	require.NoError(t, err)
	body, err := json.Marshal(summary)
	require.NoError(t, err)
	var wire struct {
		Formations []struct {
			StackID          string                 `json:"stack_id"`
			StackDigest      string                 `json:"stack_digest"`
			ModelRoleMapping map[string]string      `json:"model_role_mapping"`
			AssignmentTotal  uint32                 `json:"assignment_total"`
			PassRate         *modelPassRateRecord   `json:"pass_rate"`
			SecurityPassRate *modelPassRateRecord   `json:"security_pass_rate"`
			LatencyP50MS     modelMetricValueRecord `json:"latency_p50_ms"`
			OutputTokens     modelMetricValueRecord `json:"output_tokens"`
			ParameterCount   *uint64                `json:"parameter_count"`
			ArtifactBytes    *uint64                `json:"artifact_size_bytes"`
		} `json:"formations"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))
	require.Len(t, wire.Formations, 2, "each frozen stack needs its own run aggregate")
	for _, formation := range wire.Formations {
		require.NotNil(t, formation.PassRate)
		require.NotNil(t, formation.SecurityPassRate)
		assert.NotEmpty(t, formation.StackDigest)
		assert.Len(t, formation.ModelRoleMapping, 3)
		assert.Equal(t, float64(40), *formation.OutputTokens.Value)
		assert.Equal(t, float64(2000), *formation.LatencyP50MS.Value)
		require.NotNil(t, formation.ParameterCount)
		assert.Equal(t, uint64(3_000_000_000), *formation.ParameterCount)
		require.NotNil(t, formation.ArtifactBytes)
		assert.Equal(t, uint64(1_500_000_000), *formation.ArtifactBytes)
		if formation.StackID == stack.GetStackId() {
			assert.Equal(t, uint32(2), formation.AssignmentTotal)
			assert.Equal(t, uint32(2), formation.PassRate.Denominator)
			assert.Equal(t, 0.5, formation.PassRate.Estimate)
			assert.Equal(t, 0.5, formation.SecurityPassRate.Estimate)
		} else {
			assert.Equal(t, uint32(1), formation.AssignmentTotal)
			assert.Equal(t, 1.0, formation.PassRate.Estimate)
		}
	}
}

func TestFormationSummaryRejectsConflictingFrozenStacks(t *testing.T) {
	stack := mustHeterogeneousStack(t)
	other := proto.Clone(stack).(*evalv1.HeterogeneousStackDefinition)
	other.PrimarySlot.VariantId = "changed-primary"
	other.StackDigest = ""
	digest, err := ComputeHeterogeneousStackDigest(other)
	require.NoError(t, err)
	other.StackDigest = digest
	var assignments []*evalv1.EvaluationAssignment
	for _, bound := range []*evalv1.HeterogeneousStackDefinition{stack, other} {
		assignments = append(assignments, &evalv1.EvaluationAssignment{
			Target: &evalv1.EvaluationAssignment_Heterogeneous{Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{
				Stack: bound, DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			}},
		})
	}
	_, err = CollectRunAggregateState(assignments, nil)
	require.ErrorIs(t, err, constants.ErrFormationStackMismatch)
}

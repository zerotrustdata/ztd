// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testHeterogeneousVariants() []*evalv1.ModelVariant {
	return testFormationCatalogVariants()
}

func mustHeterogeneousStack(t *testing.T) *evalv1.HeterogeneousStackDefinition {
	t.Helper()
	stackSet, err := testFormationStackSet("heterogeneous-campaign", 11, testHeterogeneousVariants())
	require.NoError(t, err)
	return stackSet.Stacks[0]
}

func heterogeneousAssignmentExecutionRequest(t *testing.T, stack *evalv1.HeterogeneousStackDefinition, variants []*evalv1.ModelVariant) AssignmentExecutionRequest {
	t.Helper()
	stackSet, err := testFormationStackSet("heterogeneous-campaign", 11, variants)
	require.NoError(t, err)
	registry, err := MaterializeModelRegistry("heterogeneous-campaign", variants)
	require.NoError(t, err)
	return AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assignment-heterogeneous-1",
			RunId:        "run-heterogeneous-1",
			CampaignId:   "heterogeneous-campaign",
			ScenarioId:   "instruction-exact-format",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
			Target: &evalv1.EvaluationAssignment_Heterogeneous{
				Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{
					Stack:          stack,
					DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				},
			},
		},
		FormationStackSet: stackSet,
		AttemptID:         "attempt-heterogeneous-1",
		ScenarioInput: ScenarioInputFixture{
			ScenarioID: "instruction-exact-format",
			UserPrompt: "Reply with exactly: SUITE-OK",
		},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
		Binding: CampaignExecutionBinding{
			Topology:                testOperatorTopology("infer-session", "data-session"),
			FormationStackSetDigest: stackSet.SetDigest,
			ModelRegistryDigest:     registry.RegistryDigest,
			ModelRegistry:           InferenceVariantsFromEvalRegistry(variants),
			ModelVariants:           variants,
		},
	}
}

func TestValidateHeterogeneousStackSet_RejectsRetiredLegacyGenerationRule(t *testing.T) {
	set, err := testFormationStackSet("heterogeneous-campaign", 42, testHeterogeneousVariants())
	require.NoError(t, err)
	set.GenerationRule = "heterogeneous-v1"
	set.SetDigest, err = ComputeHeterogeneousStackSetDigest(set)
	require.NoError(t, err)
	assert.ErrorIs(t, ValidateHeterogeneousStackSet(set), constants.ErrFormationRegistryBinding)
}

func TestValidateHeterogeneousStackSet_RejectsMalformedCatalogStack(t *testing.T) {
	set, err := testFormationStackSet("heterogeneous-campaign", 42, testHeterogeneousVariants())
	require.NoError(t, err)
	set.Stacks[0].PrimarySlot.VariantId = "not-frozen"
	stackDigest, err := ComputeHeterogeneousStackDigest(set.Stacks[0])
	require.NoError(t, err)
	set.Stacks[0].StackDigest = stackDigest
	set.SetDigest, err = ComputeHeterogeneousStackSetDigest(set)
	require.NoError(t, err)
	assert.ErrorIs(t, ValidateHeterogeneousStackSet(set), constants.ErrFormationRegistryBinding)
}

// TestAssignmentDesignatedRole pins the single owner of an assignment's
// designated role: each target's role is returned as declared, and a missing or
// UNSPECIFIED role fails closed instead of defaulting to PRIMARY.
func TestAssignmentDesignatedRole(t *testing.T) {
	stack := mustHeterogeneousStack(t)
	homogeneous := func(role evalv1.ModelCampaignRole) *evalv1.EvaluationAssignment {
		return &evalv1.EvaluationAssignment{Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{DesignatedRole: role},
		}}
	}
	heterogeneous := func(role evalv1.ModelCampaignRole) *evalv1.EvaluationAssignment {
		return &evalv1.EvaluationAssignment{Target: &evalv1.EvaluationAssignment_Heterogeneous{
			Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack, DesignatedRole: role},
		}}
	}
	unspecified := evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED
	for _, tc := range []struct {
		name       string
		assignment *evalv1.EvaluationAssignment
		want       evalv1.ModelCampaignRole
		wantErr    error
	}{
		{"homogeneous primary", homogeneous(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, nil},
		{"homogeneous lite", homogeneous(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, nil},
		{"heterogeneous assistant", heterogeneous(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, nil},
		{"heterogeneous lite", heterogeneous(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, nil},
		{"heterogeneous unspecified", heterogeneous(unspecified), unspecified, constants.ErrMissingRequiredField},
		{"homogeneous unspecified", homogeneous(unspecified), unspecified, constants.ErrMissingRequiredField},
		{"no target", &evalv1.EvaluationAssignment{}, unspecified, constants.ErrMissingRequiredField},
		{"nil assignment", nil, unspecified, constants.ErrMissingRequiredField},
	} {
		t.Run(tc.name, func(t *testing.T) {
			role, err := AssignmentDesignatedRole(tc.assignment)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Equal(t, unspecified, role)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, role)
		})
	}
	t.Run("unknown role value", func(t *testing.T) {
		_, err := AssignmentDesignatedRole(heterogeneous(evalv1.ModelCampaignRole(99)))
		assert.Error(t, err)
	})
}

func TestComputeHeterogeneousStackDigest_RejectsDuplicateVariants(t *testing.T) {
	variants := testFormationCatalogVariants()
	_, err := materializeStack("invalid", variants[0], variants[0], variants[1])
	assert.Error(t, err)
}

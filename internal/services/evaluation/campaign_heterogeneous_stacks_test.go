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
				Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack},
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
			Topology:            testOperatorTopology("infer-session", "data-session"),
			ModelRegistryDigest: registry.RegistryDigest,
			ModelRegistry:       InferenceVariantsFromEvalRegistry(variants),
			ModelVariants:       variants,
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

func TestComputeHeterogeneousStackDigest_RejectsDuplicateVariants(t *testing.T) {
	variants := testFormationCatalogVariants()
	_, err := materializeStack("invalid", variants[0], variants[0], variants[1])
	assert.Error(t, err)
}

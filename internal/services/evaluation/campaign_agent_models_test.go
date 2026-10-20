// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// The heterogeneous routing of system turns and Tribunal calls to the frozen
// formation roles is tested against the real stack in
// campaign_tribunal_real_stack_integration_test.go.

func TestResolveAssignmentModels_RejectsStackOutsideFrozenFormationSet(t *testing.T) {
	f := newAgentExecutorFixture(t, tribunalTextTurn("unused"))
	variants := testFormationCatalogVariants()
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   agentTestCampaignID,
		Variants:     variants,
		FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	otherSet, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   agentTestCampaignID,
		Variants:     variants,
		FormationIDs: []string{"gemma-cascade"},
	})
	require.NoError(t, err)
	stack := otherSet.Stacks[0]
	f.req.Binding.ModelVariants = variants
	f.req.Binding.ModelRegistry = InferenceVariantsFromEvalRegistry(variants)
	f.req.FormationStackSet = set
	f.req.Binding.FormationStackSetDigest = set.SetDigest
	f.req.Assignment.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	f.req.Assignment.Target = &evalv1.EvaluationAssignment_Heterogeneous{
		Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack},
	}

	_, err = ResolveAssignmentModels(f.req)

	require.ErrorIs(t, err, constants.ErrFormationStackMismatch)
}

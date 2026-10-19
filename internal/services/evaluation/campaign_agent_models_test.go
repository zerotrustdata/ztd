// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignAgentExecutor_RoutesSystemTurnAndTribunalToFrozenRoles(t *testing.T) {
	// The third scripted turn is the second Tribunal member's, which fails before
	// it is answered.
	f := newAgentExecutorFixture(t, tribunalTextTurn("primary answer"), tribunalTextTurn("assistant tribunal answer"), tribunalTextTurn("unanswered"))
	variants := testFormationCatalogVariants()
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   agentTestCampaignID,
		Variants:     variants,
		FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	require.Len(t, set.Stacks, 1)

	f.req.Binding.ModelVariants = variants
	f.req.Binding.ModelRegistry = InferenceVariantsFromEvalRegistry(variants)
	f.req.Binding.ModelRegistryDigest, err = models.ComputeInferenceModelRegistryDigest(agentTestCampaignID, f.req.Binding.ModelRegistry)
	require.NoError(t, err)
	f.req.FormationStackSet = set
	f.req.Binding.FormationStackSetDigest = set.SetDigest
	f.req.Assignment.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	f.req.Assignment.Target = &evalv1.EvaluationAssignment_Heterogeneous{
		Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: set.Stacks[0]},
	}
	f.req.ScenarioTools = ScenarioToolExpectations{AllowedTools: []string{agentToolRunCommands}, TribunalCommands: true}
	// The second Tribunal member fails after the runtime executed it, so the
	// deliberation stops with the first member's answer recorded.
	f.inference.failTurn = 3

	run, err := f.executor.newRun(f.req)
	require.NoError(t, err)
	_, primaryCall := run.infer(context.Background(), AgentModelCall{Purpose: agentModelCallPurposeTurn, LoopTurn: 1}, nil, nil)
	require.NotNil(t, primaryCall)
	call, err := run.runTool(context.Background(), 1, "primary-call", tribunalIntentTurn("call-1")[0].GetToolCall())
	require.NoError(t, err)
	assert.False(t, call.Success, "the deliberation ends at the failed member")

	stack := set.Stacks[0]
	primary := formationVariantByID(t, variants, stack.GetPrimarySlot().GetVariantId())
	assistant := formationVariantByID(t, variants, stack.GetAssistantSlot().GetVariantId())
	require.Len(t, f.inference.intents, 1)
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, f.inference.intents[0].GetTier(), "the Tribunal deliberates on the assistant tier")
	require.Len(t, f.inference.requests, 3)
	assert.Equal(t, primary.GetServedModelTag(), f.inference.requests[0].GetModel())
	assert.Equal(t, primary.GetModelDigest(), f.inference.requests[0].GetModelDigest())
	assert.Equal(t, primary.GetVariantId(), primaryCall.ModelVariantID)
	assert.Equal(t, primary.GetServedModelTag(), primaryCall.ServedModelTag)
	assert.Equal(t, assistant.GetServedModelTag(), f.inference.requests[1].GetModel())
	assert.Equal(t, assistant.GetModelDigest(), f.inference.requests[1].GetModelDigest())
	assert.Equal(t, assistant.GetServedModelTag(), f.inference.requests[2].GetModel())
	require.Len(t, run.trace.ModelCalls, 3)
	assert.Equal(t, assistant.GetVariantId(), run.trace.ModelCalls[1].ModelVariantID)
	assert.Equal(t, assistant.GetServedModelTag(), run.trace.ModelCalls[1].ServedModelTag)
	assert.Equal(t, assistant.GetModelDigest(), run.trace.ModelCalls[1].ModelDigest)
	assert.Equal(t, "MODEL_ROLE_PRIMARY", primaryCall.ModelRole)
	assert.Equal(t, "MODEL_ROLE_ASSISTANT", run.trace.ModelCalls[1].ModelRole)
	assert.Equal(t, "primary answer", inferenceResultText(run.trace.ModelCalls[0].InferenceResult.InferenceResult))
	assert.Equal(t, "assistant tribunal answer", inferenceResultText(run.trace.ModelCalls[1].InferenceResult.InferenceResult))
}

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

func formationVariantByID(t *testing.T, variants []*evalv1.ModelVariant, id string) *evalv1.ModelVariant {
	t.Helper()
	for _, variant := range variants {
		if variant.GetVariantId() == id {
			return variant
		}
	}
	t.Fatalf("formation variant %q not found", id)
	return nil
}

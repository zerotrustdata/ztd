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
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testJudgeRegistryVariant(variantID, tag string) *evalv1.ModelVariant {
	return &evalv1.ModelVariant{VariantId: variantID, ProviderClass: "ollama", ServedModelTag: tag, ModelDigest: strings.Repeat("a", 64)}
}

func TestResolveCampaignJudge_PinsTheRegistryEntryForTheTag(t *testing.T) {
	registry := []*evalv1.ModelVariant{
		testJudgeRegistryVariant("qwen3-4b", "qwen3:4b"),
		testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag),
	}

	judge, err := ResolveCampaignJudge(registry, "")
	require.NoError(t, err)
	assert.Equal(t, "gemma4-e4b", judge.GetVariantId(), "an empty tag selects the default judge")

	judge, err = ResolveCampaignJudge(registry, "qwen3:4b")
	require.NoError(t, err)
	assert.Equal(t, "qwen3-4b", judge.GetVariantId())

	_, err = ResolveCampaignJudge(registry[:1], "")
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid, "a default judge absent from the registry fails closed")

	undigested := testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag)
	undigested.ModelDigest = ""
	_, err = ResolveCampaignJudge([]*evalv1.ModelVariant{undigested}, "")
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid, "a judge with no pinned digest fails closed")
}

func TestJudgeModelRegistry_IsTheJudgeAlone(t *testing.T) {
	judge := testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag)
	registry, digest, err := JudgeModelRegistry("campaign-1", judge)
	require.NoError(t, err)
	require.Len(t, registry, 1)
	assert.Equal(t, judge.GetServedModelTag(), registry[0].GetModel())
	assert.Equal(t, judge.GetModelDigest(), registry[0].GetDigest())
	expected, err := models.ComputeInferenceModelRegistryDigest("campaign-1", registry)
	require.NoError(t, err)
	assert.Equal(t, expected, digest)

	_, _, err = JudgeModelRegistry("campaign-1", nil)
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid)
}

// TestJudgeVariant_EveryAssignmentIsJudgedByTheFrozenJudge: two assignments
// whose designated variants differ are judged by the one campaign judge, never
// by their own model, and a campaign with no frozen judge fails closed.
func TestJudgeVariant_EveryAssignmentIsJudgedByTheFrozenJudge(t *testing.T) {
	judge := testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag)
	stack := mustHeterogeneousStack(t)
	variants := testHeterogeneousVariants()
	first := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	second := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	second.Assignment.GetHeterogeneous().DesignatedRole = evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE
	for _, req := range []AssignmentExecutionRequest{first, second} {
		req.Binding.JudgeVariant = judge
		got, err := judgeVariant(req)
		require.NoError(t, err)
		assert.Equal(t, judge.GetVariantId(), got.GetVariantId())
	}

	_, err := judgeVariant(first)
	require.ErrorIs(t, err, constants.ErrEvaluationCampaignJudgeInvalid, "a campaign frozen without a judge fails closed")
}

// TestVerifyAssignmentJudge_EveryGradeNamesTheFrozenJudge: a stored grade or
// grader call attributed to any model other than the campaign's frozen judge,
// such as the subject grading itself, does not verify.
func TestVerifyAssignmentJudge_EveryGradeNamesTheFrozenJudge(t *testing.T) {
	binding := CampaignExecutionBinding{JudgeVariant: testJudgeRegistryVariant("gemma4-e4b", DefaultJudgeServedModelTag)}
	judged := &evalv1.EvaluationAssignmentResult{
		SemanticGrades: []*evalv1.SemanticGrade{{GradeId: "a:diagnosis-quality", JudgeVariantId: "gemma4-e4b"}},
		GraderCalls:    []*evalv1.GraderModelCallRecord{{GraderCallId: "g1", JudgeVariantId: "gemma4-e4b"}},
	}
	assert.Empty(t, verifyAssignmentJudge(binding, judged))

	selfGraded := &evalv1.EvaluationAssignmentResult{
		SemanticGrades: []*evalv1.SemanticGrade{{GradeId: "a:diagnosis-quality", JudgeVariantId: "qwen3-4b"}},
		GraderCalls:    []*evalv1.GraderModelCallRecord{{GraderCallId: "g1", JudgeVariantId: "qwen3-4b"}},
	}
	assert.Len(t, verifyAssignmentJudge(binding, selfGraded), 2)

	assert.Empty(t, verifyAssignmentJudge(CampaignExecutionBinding{}, selfGraded), "a campaign frozen before pinned judging keeps its stored attribution")
}

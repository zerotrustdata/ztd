// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type harnessCampaignFormationRunner struct {
	harness *FormationHarness
}

func (r *harnessCampaignFormationRunner) RunHeterogeneousFormation(ctx context.Context, binding FormationBindingRequest, _ FormationRunContext, initialState []byte) (*FormationRunResult, error) {
	formation, err := BindFormation(binding)
	if err != nil {
		return nil, err
	}
	return r.harness.RunBoundFormation(ctx, formation, initialState)
}

func heterogeneousAssignmentExecutionRequest(t *testing.T, stack *evalv1.HeterogeneousStackDefinition, variants []*evalv1.ModelVariant) AssignmentExecutionRequest {
	t.Helper()
	return AssignmentExecutionRequest{
		Assignment: &evalv1.EvaluationAssignment{
			AssignmentId: "assignment-heterogeneous-1",
			RunId:        "run-heterogeneous-1",
			CampaignId:   "campaign-heterogeneous-1",
			ScenarioId:   "instruction-exact-format",
			Lane:         evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
			Target: &evalv1.EvaluationAssignment_Heterogeneous{
				Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: stack},
			},
		},
		AttemptID: "attempt-heterogeneous-1",
		ScenarioInput: ScenarioInputFixture{
			ScenarioID: "instruction-exact-format",
			UserPrompt: "Reply with exactly: SUITE-OK",
		},
		GradingMethod: evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC,
		Binding: CampaignExecutionBinding{
			Topology:            testOperatorTopology("infer-session", "data-session"),
			ModelRegistryDigest: "d" + repeatHex('d', 63),
			ModelRegistry:       InferenceVariantsFromEvalRegistry(variants),
		},
	}
}

func TestBindFormation_BindsRegistryVariants(t *testing.T) {
	variants := testHeterogeneousVariants()
	stackSet, err := GenerateHeterogeneousStackSet(HeterogeneousStackGenerationRequest{
		CampaignID: "heterogeneous-campaign",
		Seed:       11,
		Variants:   variants,
	})
	require.NoError(t, err)
	stack := stackSet.Stacks[0]

	formation, err := BindFormation(FormationBindingRequest{
		Stack:    stack,
		Variants: variants,
	})
	require.NoError(t, err)
	assert.True(t, formation.RelaxedValidation)
	assert.Equal(t, stack.GetPrimarySlot().GetVariantId(), formation.Primary.VariantID)
	assert.NotEmpty(t, formation.Primary.ModelDigest)
}

func TestCampaignFormationExecutor_ExecutesHeterogeneousAssignment(t *testing.T) {
	variants := testHeterogeneousVariants()
	stackSet, err := GenerateHeterogeneousStackSet(HeterogeneousStackGenerationRequest{
		CampaignID: "heterogeneous-campaign",
		Seed:       11,
		Variants:   variants,
	})
	require.NoError(t, err)
	harness, err := NewFormationHarness(
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		func(prefix string) string { return prefix + "-attempt" },
	)
	require.NoError(t, err)
	executor := NewCampaignFormationExecutor(variants, &harnessCampaignFormationRunner{harness: harness}, nil, nil, func(prefix string) string { return prefix + "-1" })
	req := heterogeneousAssignmentExecutionRequest(t, stackSet.Stacks[0], variants)

	result, err := executor.ExecuteAssignment(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	assert.Len(t, result.GetModelInferences(), 3)
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, result.GetModelInferences()[0].GetModelRole())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, result.GetModelInferences()[1].GetModelRole())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, result.GetModelInferences()[2].GetModelRole())
}

func TestImportAssignmentResultFromFormationRun_SetsAgentPersonaForPublication(t *testing.T) {
	variants := testHeterogeneousVariants()
	stackSet, err := GenerateHeterogeneousStackSet(HeterogeneousStackGenerationRequest{
		CampaignID: "heterogeneous-campaign",
		Seed:       11,
		Variants:   variants,
	})
	require.NoError(t, err)
	harness, err := NewFormationHarness(
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		func(prefix string) string { return prefix + "-attempt" },
	)
	require.NoError(t, err)
	formation, err := BindFormation(FormationBindingRequest{Stack: stackSet.Stacks[0], Variants: variants})
	require.NoError(t, err)
	formationResult, err := harness.RunBoundFormation(context.Background(), formation, []byte("initial"))
	require.NoError(t, err)

	req := heterogeneousAssignmentExecutionRequest(t, stackSet.Stacks[0], variants)
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	require.Len(t, result.GetModelInferences(), 3)

	activity, _, err := BuildPublicAssignmentEvidence(PublicAssignmentEvidenceInput{Result: result})
	require.NoError(t, err)
	require.Len(t, activity.Summary.ModelActivity.Records, 3)
	for _, record := range activity.Summary.ModelActivity.Records {
		assert.NotEmpty(t, record.GetAgentPersona())
	}
}

func TestImportAssignmentResultFromFormationRun_MaterializesRoleTelemetry(t *testing.T) {
	variants := testHeterogeneousVariants()
	stackSet, err := GenerateHeterogeneousStackSet(HeterogeneousStackGenerationRequest{
		CampaignID: "heterogeneous-campaign",
		Seed:       11,
		Variants:   variants,
	})
	require.NoError(t, err)
	harness, err := NewFormationHarness(
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		func(prefix string) string { return prefix + "-attempt" },
	)
	require.NoError(t, err)
	formation, err := BindFormation(FormationBindingRequest{Stack: stackSet.Stacks[0], Variants: variants})
	require.NoError(t, err)
	formationResult, err := harness.RunBoundFormation(context.Background(), formation, []byte("initial"))
	require.NoError(t, err)
	for index := range formationResult.Roles {
		formationResult.Roles[index].UsageAvailability = evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED
		formationResult.Roles[index].PromptTokens = uint32(index + 10)
	}

	req := heterogeneousAssignmentExecutionRequest(t, stackSet.Stacks[0], variants)
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	assert.Len(t, result.GetModelInferences(), 3)
	assert.NotNil(t, result.GetScoredInferenceSpanNanos())
	for index, inference := range result.GetModelInferences() {
		assert.Equal(t, evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED, inference.GetUsageAvailability())
		assert.Equal(t, uint32(index+10), inference.GetPromptTokens())
	}
	summary, err := BuildPublicResourceSummary(result)
	require.NoError(t, err)
	require.NotNil(t, summary.InputTokens.Value)
	assert.Equal(t, float64(33), *summary.InputTokens.Value)
}

func TestImportAssignmentResultFromFormationRun_GradesFormationRoleCompletion(t *testing.T) {
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	formation, err := BindFormation(FormationBindingRequest{Stack: stack, Variants: variants})
	require.NoError(t, err)
	formationResult := &FormationRunResult{
		FormationID: formation.ID,
		Passed:      true,
		Roles: []FormationRoleTelemetry{
			{Role: FormationRoleLite, Model: formation.Lite, ProviderAttemptID: "lite-attempt"},
			{Role: FormationRoleAssistant, Model: formation.Assistant, ProviderAttemptID: "assistant-attempt"},
			{Role: FormationRolePrimary, Model: formation.Primary, ProviderAttemptID: "primary-attempt"},
		},
	}

	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	require.Len(t, result.GetDeterministicGrades(), 1)
	roles := findDeterministicGradeByID(result.GetDeterministicGrades(), req.Assignment.GetAssignmentId()+":formation-roles")
	require.NotNil(t, roles)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, roles.GetStatus())
	assert.Empty(t, result.GetDecomposedScores())
}

func TestImportAssignmentResultFromFormationRun_SemanticJudgeScenarioIsGraderFailure(t *testing.T) {
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	formation, err := BindFormation(FormationBindingRequest{Stack: stack, Variants: variants})
	require.NoError(t, err)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	req.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_SEMANTIC_JUDGE
	formationResult := &FormationRunResult{
		FormationID: formation.ID,
		Passed:      true,
		Roles: []FormationRoleTelemetry{
			{Role: FormationRoleLite, Model: formation.Lite, ProviderAttemptID: "lite-attempt"},
			{Role: FormationRoleAssistant, Model: formation.Assistant, ProviderAttemptID: "assistant-attempt"},
			{Role: FormationRolePrimary, Model: formation.Primary, ProviderAttemptID: "primary-attempt"},
		},
	}

	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_GRADER_FAILED, result.GetLifecycleStatus())
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_INVALID_EVIDENCE, DerivePublicSummaryStatus(result))
	outcome, err := AssignmentTerminalOutcome(result.GetLifecycleStatus(), result)
	require.NoError(t, err)
	assert.Equal(t, TerminalOutcomeGraderFailed, outcome)

	available := findDeterministicGradeByID(result.GetDeterministicGrades(), req.Assignment.GetAssignmentId()+":semantic-judge-available")
	require.NotNil(t, available, "the grader failure must leave a diagnostic grade naming the missing judge")
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, available.GetStatus())
	assert.Equal(t, "semantic-judge-available", available.GetCriterionId())
	assert.Equal(t, "semantic judge is unavailable in the direct formation runner; no judge evidence was recorded for one or more roles", available.GetDetail())
}

func TestImportAssignmentResultFromFormationRun_DoesNotPublishUnavailablePromptUsageAsZero(t *testing.T) {
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	formationResult := &FormationRunResult{
		FormationID: stack.GetStackId(),
		Roles: []FormationRoleTelemetry{{
			Role:              FormationRoleLite,
			Model:             formationModelFromVariant(variants[0]),
			ProviderAttemptID: "provider-attempt-lite",
			UsageAvailability: evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE,
			GenerationTokens:  12,
		}},
	}

	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	require.Len(t, result.GetModelInferences(), 1)
	assert.Equal(t, evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_UNAVAILABLE, result.GetModelInferences()[0].GetUsageAvailability())

	summary, err := BuildPublicResourceSummary(result)
	require.NoError(t, err)
	assert.Nil(t, summary.InputTokens.Value)
	assert.Equal(t, evalv1.PublicUnavailableReason_PUBLIC_UNAVAILABLE_REASON_INCOMPLETE_CONTRIBUTOR_EVIDENCE, summary.InputTokens.UnavailableReason)
}

func TestBuildFormationInitialState_MaterializesScenarioFixture(t *testing.T) {
	input := ScenarioInputFixture{
		ScenarioID: "instruction-exact-format",
		UserPrompt: "Reply with exactly: SUITE-OK",
		Seed:       InvestigationSeed{CaseTitle: "Release readiness check"},
	}
	state, err := BuildFormationInitialState(input)
	require.NoError(t, err)
	var decoded ScenarioInputFixture
	require.NoError(t, json.Unmarshal(state, &decoded))
	assert.Equal(t, input.UserPrompt, decoded.UserPrompt)
	assert.Equal(t, input.Seed, decoded.Seed)
}

func TestBuildFormationInitialState_RejectsMissingPrompt(t *testing.T) {
	_, err := BuildFormationInitialState(ScenarioInputFixture{ScenarioID: "instruction-exact-format"})
	require.Error(t, err)
}

func TestVerifyFormationAssignmentEvidence_AcceptsCompletedResult(t *testing.T) {
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	harness, err := NewFormationHarness(
		func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		func(prefix string) string { return prefix + "-attempt" },
	)
	require.NoError(t, err)
	formation, err := BindFormation(FormationBindingRequest{Stack: req.Assignment.GetHeterogeneous().GetStack(), Variants: testHeterogeneousVariants()})
	require.NoError(t, err)
	initialState, err := BuildFormationInitialState(req.ScenarioInput)
	require.NoError(t, err)
	formationResult, err := harness.RunBoundFormation(context.Background(), formation, initialState)
	require.NoError(t, err)
	result, err := ImportAssignmentResultFromFormationRun(req, formationResult, time.Unix(1_700_000_000, 0).UTC(), func(prefix string) string { return prefix + "-1" })
	require.NoError(t, err)
	require.NoError(t, VerifyFormationAssignmentEvidence(req.Assignment, result))
}

func mustHeterogeneousStack(t *testing.T) *evalv1.HeterogeneousStackDefinition {
	t.Helper()
	stackSet, err := GenerateHeterogeneousStackSet(HeterogeneousStackGenerationRequest{
		CampaignID: "heterogeneous-campaign",
		Seed:       11,
		Variants:   testHeterogeneousVariants(),
	})
	require.NoError(t, err)
	return stackSet.Stacks[0]
}

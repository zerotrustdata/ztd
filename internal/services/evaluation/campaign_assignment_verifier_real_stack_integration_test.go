// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// External package: the formation runs through the production Gateway, Tribunal,
// and Inference Operator. Only the model provider is scripted, and the verifier
// reads the evidence the run persisted.
package evaluation_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// TestRealStackAssignmentVerifier_FormationBindsExecutedRoles executes one
// formation assignment on the real stack and verifies its persisted evidence.
// The untouched evidence passes; each tampered copy of it fails.
func TestRealStackAssignmentVerifier_FormationBindsExecutedRoles(t *testing.T) {
	ctx := t.Context()
	variants := tribunalFormationVariants(t)
	set, err := evaluation.GenerateFormationCatalogStackSet(evaluation.FormationCatalogStackGenerationRequest{
		CampaignID: realStackCampaignID, Variants: variants, FormationIDs: []string{"heterogeneous-0000"},
	})
	require.NoError(t, err)
	require.Len(t, set.Stacks, 1)

	// The primary turn, five Tribunal candidates on the lite tier, the
	// risk check, the audit, and the closing turn.
	replies := slices.Concat(
		tribunalIntent("call-1"),
		tribunalUnanimous(tribunalRealCommand),
		tribunalTurn(tribunalRealLowRisk),
		tribunalTurn(tribunalRealApprove),
		tribunalTurn("HEALTHY"),
	)
	env := newRealStackAgentRun(t, realStackOptions{
		suffix:    "verifier-formation",
		replies:   replies,
		scenario:  tribunalScenario,
		modelTags: evaluation.FormationCatalogServedTags(),
		request: func(req *evaluation.CampaignInitRequest, _ string) {
			inventory, err := evaluation.MaterializeModelRegistry(realStackCampaignID, variants)
			require.NoError(t, err)
			req.Inventory = inventory
			req.FormationStackSet = set
			req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
		},
	})

	res := env.executeNext(t)

	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	require.Empty(t, res.GetInvalidEvidenceDetail())
	persisted, err := env.store.LoadAssignmentResult(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, persisted.GetModelInferences(), 9)
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, persisted.GetModelInferences()[0].GetModelRole())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, persisted.GetModelInferences()[1].GetModelRole())
	assignment, err := env.store.LoadAssignment(ctx, env.req.RunID, res.GetAssignmentId())
	require.NoError(t, err)

	// The verifier binds the frozen formation variants and the frozen stack set,
	// as the run's campaign spec does.
	binding := env.binding
	binding.ModelVariants = variants
	binding.FormationStackSetDigest = set.SetDigest
	binding.Personas = env.req.Catalog.GetPersonas()

	// verifyRequest builds the verifier input from the persisted trace and result,
	// with the scenario expectations, grading method, and catalog the run executed
	// against, as the run verifier does.
	verifyRequest := func(t *testing.T, result *evalv1.EvaluationAssignmentResult) evaluation.CampaignAssignmentVerificationRequest {
		t.Helper()
		trace := executorLoadTrace(t, env, result)
		imported := executorImportRequest(t, env, assignment.GetAssignmentId(), trace)
		scenario := executorScenario(t, env, assignment.GetScenarioId())
		return evaluation.CampaignAssignmentVerificationRequest{
			Assignment: assignment, Result: result, AgentTrace: trace,
			ScenarioInput: imported.ScenarioInput, ScenarioGold: imported.ScenarioGold,
			ScenarioTools: evaluation.ScenarioToolExpectations{
				AllowedTools:     scenario.GetAllowedTools(),
				ExpectedTools:    scenario.GetExpectedTools(),
				ForbiddenTools:   scenario.GetForbiddenTools(),
				TrajectoryPolicy: scenario.GetTrajectoryPolicy(),
				TribunalCommands: scenario.GetTribunalCommands(),
			},
			GradingMethod: scenario.GetGradingMethod(),
			CatalogRef:    env.req.Catalog.GetCatalogRef(),
			Binding:       binding, FormationStackSet: set,
		}
	}

	verifier := evaluation.NewCampaignAssignmentVerifier(nil)
	report, err := verifier.Verify(ctx, verifyRequest(t, persisted))
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), "%v", report.GetFailureReasons())

	for _, mutation := range []struct {
		name   string
		mutate func(*evaluation.CampaignAssignmentVerificationRequest)
	}{
		{"substitute trace variant ID", func(req *evaluation.CampaignAssignmentVerificationRequest) {
			req.AgentTrace.ModelCalls[1].ModelVariantID = set.Stacks[0].GetPrimarySlot().GetVariantId()
		}},
		{"attribute Tribunal call to the assistant slot", func(req *evaluation.CampaignAssignmentVerificationRequest) {
			req.AgentTrace.ModelCalls[1].ModelVariantID = set.Stacks[0].GetAssistantSlot().GetVariantId()
		}},
		{"substitute result variant", func(req *evaluation.CampaignAssignmentVerificationRequest) {
			req.Result.ModelInferences[1].ModelVariant = req.Result.ModelInferences[0].ModelVariant
		}},
		{"omit signed result", func(req *evaluation.CampaignAssignmentVerificationRequest) {
			req.AgentTrace.ModelCalls[1].InferenceResult = nil
		}},
		{"omit frozen formation", func(req *evaluation.CampaignAssignmentVerificationRequest) {
			req.FormationStackSet = nil
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := verifyRequest(t, proto.Clone(persisted).(*evalv1.EvaluationAssignmentResult))
			mutation.mutate(&changed)
			body, err := evaluation.SealAgentTrace(changed.AgentTrace)
			require.NoError(t, err)
			for _, ref := range changed.Result.GetEvidenceRefs() {
				if ref.GetSchemaRef() == "ztd.eval.v1.EvaluationAssignmentAgentTrace" {
					ref.Sha256 = models.SHA256Hex(body)
				}
			}
			changed.Result.ResultDigest, err = evaluation.ComputeAssignmentResultDigest(changed.Result)
			require.NoError(t, err)
			report, err := verifier.Verify(ctx, changed)
			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus(), "%v", report.GetFailureReasons())
		})
	}
}

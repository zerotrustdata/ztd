// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// A formation's result needs real governed trace evidence before it can verify.
func TestCampaignAssignmentVerifier_FormationRequiresTrace(t *testing.T) {
	t.Parallel()
	req := heterogeneousAssignmentExecutionRequest(t, mustHeterogeneousStack(t), testHeterogeneousVariants())
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    req.Assignment.GetAssignmentId(),
		RunId:           req.Assignment.GetRunId(),
		CampaignId:      req.Assignment.GetCampaignId(),
		Lane:            req.Assignment.GetLane(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest

	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: req.Assignment,
		Result:     result,
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	assert.Contains(t, report.GetFailureReasons(), "agent trace is required for homogeneous verification")
}

func TestCampaignAssignmentVerifier_FormationBindsExecutedRoles(t *testing.T) {
	f := newTribunalFixture(t,
		tribunalSingle(tribunalIntentTurn("call-1")),
		tribunalUnanimous(agentTestCommand),
		tribunalSingle(tribunalTextTurn(tribunalTestLowRisk)),
		tribunalSingle(tribunalTextTurn(tribunalTestApprove)),
		tribunalSingle(tribunalTextTurn("HEALTHY")),
	)
	variants := testFormationCatalogVariants()
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: agentTestCampaignID, Variants: variants, FormationIDs: []string{"qwen-powerhouse"},
	})
	require.NoError(t, err)
	f.req.Binding.ModelVariants = variants
	f.req.Binding.ModelRegistry = InferenceVariantsFromEvalRegistry(variants)
	f.req.Binding.ModelRegistryDigest, err = models.ComputeInferenceModelRegistryDigest(agentTestCampaignID, f.req.Binding.ModelRegistry)
	require.NoError(t, err)
	f.req.FormationStackSet = set
	f.req.Binding.FormationStackSetDigest = set.SetDigest
	f.req.Assignment.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	f.req.Assignment.Target = &evalv1.EvaluationAssignment_Heterogeneous{Heterogeneous: &evalv1.HeterogeneousAssignmentTarget{Stack: set.Stacks[0]}}
	result, err := f.execute()
	require.NoError(t, err)
	require.Empty(t, result.GetInvalidEvidenceDetail())
	trace := f.storedTrace()
	verifier := NewCampaignAssignmentVerifier(nil)
	request := CampaignAssignmentVerificationRequest{
		Assignment: f.req.Assignment, Result: result, AgentTrace: trace,
		ScenarioInput: f.req.ScenarioInput, ScenarioGold: f.req.ScenarioGold,
		ScenarioTools: f.req.ScenarioTools, GradingMethod: f.req.GradingMethod,
		Binding: f.req.Binding, FormationStackSet: set,
	}
	report, err := verifier.Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), "%v", report.GetFailureReasons())
	require.Len(t, result.GetModelInferences(), 9)
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, result.ModelInferences[0].GetModelRole())
	assert.Equal(t, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, result.ModelInferences[1].GetModelRole())

	for _, mutation := range []struct {
		name   string
		mutate func(*CampaignAssignmentVerificationRequest)
	}{
		{"substitute trace variant ID", func(req *CampaignAssignmentVerificationRequest) {
			req.AgentTrace.ModelCalls[1].ModelVariantID = set.Stacks[0].GetPrimarySlot().GetVariantId()
		}},
		{"substitute result variant", func(req *CampaignAssignmentVerificationRequest) {
			req.Result.ModelInferences[1].ModelVariant = req.Result.ModelInferences[0].ModelVariant
		}},
		{"omit signed result", func(req *CampaignAssignmentVerificationRequest) {
			req.AgentTrace.ModelCalls[1].InferenceResult = nil
		}},
		{"omit frozen formation", func(req *CampaignAssignmentVerificationRequest) {
			req.FormationStackSet = nil
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := request
			changed.Result = proto.Clone(result).(*evalv1.EvaluationAssignmentResult)
			changed.AgentTrace, err = DecodeAgentTrace(f.traces.body)
			require.NoError(t, err)
			mutation.mutate(&changed)
			body, err := SealAgentTrace(changed.AgentTrace)
			require.NoError(t, err)
			for _, ref := range changed.Result.EvidenceRefs {
				if ref.GetSchemaRef() == "ztd.eval.v1.EvaluationAssignmentAgentTrace" {
					ref.Sha256 = models.SHA256Hex(body)
				}
			}
			changed.Result.ResultDigest, err = ComputeAssignmentResultDigest(changed.Result)
			require.NoError(t, err)
			report, err := verifier.Verify(t.Context(), changed)
			require.NoError(t, err)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus(), "%v", report.GetFailureReasons())
		})
	}
}

func TestCampaignAssignmentVerifier_FailsClosedOnModelRoleAssignment(t *testing.T) {
	t.Parallel()
	variants := testHeterogeneousVariants()
	stack := mustHeterogeneousStack(t)
	req := heterogeneousAssignmentExecutionRequest(t, stack, variants)
	modelRole := proto.Clone(req.Assignment).(*evalv1.EvaluationAssignment)
	modelRole.Target = nil
	verifier := NewCampaignAssignmentVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() })
	report, err := verifier.Verify(context.Background(), CampaignAssignmentVerificationRequest{
		Assignment: modelRole,
		Result:     &evalv1.EvaluationAssignmentResult{AssignmentId: modelRole.GetAssignmentId()},
	})
	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
	// A model-role assignment is verified against its persisted agent trace, so
	// a result with no trace is rejected rather than accepted as formation evidence.
	assert.Contains(t, report.GetFailureReasons(), "agent trace is required for homogeneous verification")
}

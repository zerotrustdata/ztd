// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CampaignExecutionBinding pins the exact governed execution authorities for one
// scored assignment.
type CampaignExecutionBinding struct {
	// Topology names the Inference and Data runtimes and, for model-driven
	// lanes, pins each one's enrollment-bound actuator key. Evidence verifies
	// every governed receipt against the anchor of the runtime that signed it.
	Topology            CampaignOperatorTopology
	ModelRegistryDigest string
	ModelRegistry       []*operatorv1.InferenceModelVariant
	// ModelVariants preserves the campaign's typed frozen registry, including
	// variant IDs used by system-lane formation slots.
	ModelVariants           []*evalv1.ModelVariant
	FormationStackSetDigest string
}

func modelCampaignRoleLabel(role evalv1.ModelCampaignRole) (string, error) {
	switch role {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return "primary", nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return "assistant", nil
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return "lite", nil
	default:
		return "", fmt.Errorf("evaluation: unsupported model campaign role %s", role.String())
	}
}

// ScenarioToolExpectations carries frozen scenario tool constraints for grading.
type ScenarioToolExpectations struct {
	AllowedTools     []string
	ExpectedTools    []string
	ForbiddenTools   []string
	TrajectoryPolicy evalv1.EvaluationTrajectoryPolicy
	// TribunalCommands routes run_shell_command through Tribunal authoring.
	TribunalCommands bool
}

// AssignmentExecutionRequest carries one resumable controller execution attempt.
type AssignmentExecutionRequest struct {
	Assignment *evalv1.EvaluationAssignment
	// FormationStackSet is the validated, campaign-frozen formation source for
	// system-lane assignments. It is nil for model-role assignments.
	FormationStackSet *HeterogeneousStackSet
	// Investigation is the digest-bound campaign policy for scored history.
	Investigation    *evalv1.EvaluationInvestigation
	AttemptID        string
	ScenarioInput    ScenarioInputFixture
	ScenarioGold     ScenarioGoldCriteria
	ScenarioTools    ScenarioToolExpectations
	RequiredConcepts []string
	GradingMethod    evalv1.EvaluationGradingMethod
	Binding          CampaignExecutionBinding
	// HeldContext is the previous repetition's sealed trace when the campaign
	// holds context; the homogeneous executor continues its conversation in
	// its workspace. Nil for a cell's first repetition or a fresh campaign.
	HeldContext *AgentTrace
}

// CampaignAssignmentExecutor executes one scored assignment and returns its
// terminal assignment result.
type CampaignAssignmentExecutor interface {
	ExecuteAssignment(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error)
}

// InferenceVariantsFromEvalRegistry converts frozen eval model variants into the
// governed inference registry shape bound to campaign execution.
func InferenceVariantsFromEvalRegistry(variants []*evalv1.ModelVariant) []*operatorv1.InferenceModelVariant {
	out := make([]*operatorv1.InferenceModelVariant, 0, len(variants))
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		out = append(out, &operatorv1.InferenceModelVariant{
			Model:  variant.GetServedModelTag(),
			Digest: variant.GetModelDigest(),
		})
	}
	return out
}

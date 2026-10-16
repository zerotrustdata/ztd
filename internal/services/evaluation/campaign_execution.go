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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CampaignExecutionBinding pins the exact governed execution authorities for one
// scored assignment executed by the direct formation runner.
type CampaignExecutionBinding struct {
	// Topology names the Inference and Data runtimes and, for model-driven
	// lanes, pins each one's enrollment-bound actuator key. Evidence verifies
	// every governed receipt against the anchor of the runtime that signed it.
	Topology            CampaignOperatorTopology
	ModelRegistryDigest string
	ModelRegistry       []*operatorv1.InferenceModelVariant
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
}

// CampaignChatGradingContext carries private grading inputs for one assignment.
type CampaignChatGradingContext struct {
	GradingMethod    evalv1.EvaluationGradingMethod
	ScenarioGold     ScenarioGoldCriteria
	ScenarioTools    ScenarioToolExpectations
	RequiredConcepts []string
}

// AssignmentExecutionRequest carries one resumable controller execution attempt.
type AssignmentExecutionRequest struct {
	Assignment       *evalv1.EvaluationAssignment
	AttemptID        string
	ScenarioInput    ScenarioInputFixture
	ScenarioGold     ScenarioGoldCriteria
	ScenarioTools    ScenarioToolExpectations
	RequiredConcepts []string
	GradingMethod    evalv1.EvaluationGradingMethod
	Binding          CampaignExecutionBinding
	// OnFormationRoleStarting is optional. When set, the heterogeneous formation
	// executor invokes it before each role executes so one planned invocation row
	// reaches the live feed instead of batching all roles at assignment start.
	OnFormationRoleStarting func(ctx context.Context, role FormationRole) error
	// OnFormationRoleProgress is optional. When set, the heterogeneous formation
	// executor invokes it after each completed role so role telemetry reaches the
	// live feed before the terminal assignment result exists.
	OnFormationRoleProgress func(ctx context.Context, result *FormationRunResult) error
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

// CampaignModelBinding describes one frozen served model tag and digest pair.
type CampaignModelBinding struct {
	ServedModelTag string
	ModelDigest    string
}

// CampaignModelBindingsFromFormation returns provenance preflight bindings for
// the sovereign roles in one bound formation. Delegated roles are omitted.
func CampaignModelBindingsFromFormation(formation Formation) []CampaignModelBinding {
	bindings := make([]CampaignModelBinding, 0, 3)
	for _, model := range []FormationModel{formation.Primary, formation.Assistant, formation.Lite} {
		if model.Trust == FormationTrustDelegated || model.ServedModelTag == "" || model.ModelDigest == "" {
			continue
		}
		if model.ModelDigest == FormationDelegatedRegistryDigestPlaceholder {
			continue
		}
		bindings = append(bindings, CampaignModelBinding{
			ServedModelTag: model.ServedModelTag,
			ModelDigest:    model.ModelDigest,
		})
	}
	return bindings
}

// CampaignModelBindingsFromSpec returns attestation bindings for every frozen
// model variant in one campaign spec.
func CampaignModelBindingsFromSpec(spec *evalv1.EvaluationCampaignSpec) ([]CampaignModelBinding, error) {
	if spec == nil {
		return nil, fmt.Errorf("evaluation: campaign model bindings: %w", constants.ErrMissingRequiredField)
	}
	variants := spec.GetModelRegistry()
	if len(variants) == 0 {
		return nil, fmt.Errorf("evaluation: campaign model bindings: empty model registry: %w", constants.ErrInferenceModelRegistryInvalid)
	}
	bindings := make([]CampaignModelBinding, 0, len(variants))
	for _, variant := range variants {
		if variant == nil || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" {
			return nil, fmt.Errorf("evaluation: campaign model bindings: %w", constants.ErrMissingRequiredField)
		}
		bindings = append(bindings, CampaignModelBinding{
			ServedModelTag: variant.GetServedModelTag(),
			ModelDigest:    variant.GetModelDigest(),
		})
	}
	return bindings, nil
}

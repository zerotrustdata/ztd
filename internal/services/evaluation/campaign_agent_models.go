// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ResolveAssignmentModels binds an assignment's roles to the campaign's frozen
// model registry. Heterogeneous assignments additionally prove that their
// stack is one of the catalog formations frozen for this campaign.
func ResolveAssignmentModels(req AssignmentExecutionRequest) (map[evalv1.ModelCampaignRole]*evalv1.ModelVariant, error) {
	if req.Assignment == nil {
		return nil, fmt.Errorf("evaluation: resolve assignment models: %w", constants.ErrMissingRequiredField)
	}
	modelsByID := make(map[string]*evalv1.ModelVariant, len(req.Binding.ModelVariants))
	for _, variant := range req.Binding.ModelVariants {
		if variant == nil || variant.GetVariantId() == "" || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" {
			return nil, fmt.Errorf("evaluation: resolve assignment models: frozen model variant: %w", constants.ErrFormationRegistryBinding)
		}
		if _, exists := modelsByID[variant.GetVariantId()]; exists {
			return nil, fmt.Errorf("evaluation: resolve assignment models: duplicate variant %q: %w", variant.GetVariantId(), constants.ErrFormationRegistryBinding)
		}
		modelsByID[variant.GetVariantId()] = variant
	}
	operatorRegistry := make(map[string]string, len(req.Binding.ModelRegistry))
	for _, variant := range req.Binding.ModelRegistry {
		if variant == nil || variant.GetModel() == "" || variant.GetDigest() == "" {
			return nil, fmt.Errorf("evaluation: resolve assignment models: inference registry: %w", constants.ErrInferenceModelRegistryInvalid)
		}
		if _, exists := operatorRegistry[variant.GetModel()]; exists {
			return nil, fmt.Errorf("evaluation: resolve assignment models: duplicate inference model %q: %w", variant.GetModel(), constants.ErrInferenceModelRegistryInvalid)
		}
		operatorRegistry[variant.GetModel()] = variant.GetDigest()
	}
	if len(operatorRegistry) == 0 {
		return nil, fmt.Errorf("evaluation: resolve assignment models: inference registry: %w", constants.ErrMissingRequiredField)
	}
	for _, variant := range modelsByID {
		if digest, ok := operatorRegistry[variant.GetServedModelTag()]; !ok || digest != variant.GetModelDigest() {
			return nil, fmt.Errorf("evaluation: resolve assignment models: variant %q differs from governed inference registry: %w", variant.GetVariantId(), constants.ErrFormationRegistryBinding)
		}
	}

	resolved := make(map[evalv1.ModelCampaignRole]*evalv1.ModelVariant, 3)
	switch target := req.Assignment.GetTarget().(type) {
	case *evalv1.EvaluationAssignment_Homogeneous:
		if target.Homogeneous == nil || target.Homogeneous.GetCandidateVariant() == nil {
			return nil, fmt.Errorf("evaluation: resolve assignment models: homogeneous candidate: %w", constants.ErrMissingRequiredField)
		}
		role := target.Homogeneous.GetDesignatedRole()
		candidate := target.Homogeneous.GetCandidateVariant()
		if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED || candidate.GetVariantId() == "" {
			return nil, fmt.Errorf("evaluation: resolve assignment models: homogeneous target: %w", constants.ErrEvaluationTargetKindMismatch)
		}
		frozen := modelsByID[candidate.GetVariantId()]
		if frozen == nil && len(modelsByID) == 0 {
			// A homogeneous target carries its complete frozen candidate. The
			// governed registry digest still authorizes its served tag and digest.
			frozen = candidate
		}
		if frozen == nil || frozen.GetServedModelTag() != candidate.GetServedModelTag() || frozen.GetModelDigest() != candidate.GetModelDigest() {
			return nil, fmt.Errorf("evaluation: resolve assignment models: homogeneous candidate is not frozen: %w", constants.ErrFormationRegistryBinding)
		}
		if digest, ok := operatorRegistry[frozen.GetServedModelTag()]; !ok || digest != frozen.GetModelDigest() {
			return nil, fmt.Errorf("evaluation: resolve assignment models: homogeneous candidate differs from governed inference registry: %w", constants.ErrFormationRegistryBinding)
		}
		resolved[role] = frozen
	case *evalv1.EvaluationAssignment_Heterogeneous:
		if len(modelsByID) == 0 {
			return nil, fmt.Errorf("evaluation: resolve assignment models: typed frozen model registry: %w", constants.ErrFormationRegistryBinding)
		}
		if req.Assignment.GetLane() != evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM || target.Heterogeneous == nil || target.Heterogeneous.GetStack() == nil {
			return nil, fmt.Errorf("evaluation: resolve assignment models: system lane target: %w", constants.ErrEvaluationTargetKindMismatch)
		}
		set := req.FormationStackSet
		if set == nil || set.CampaignID != req.Assignment.GetCampaignId() || set.GenerationRule != FormationCatalogStackGenerationRule || req.Binding.FormationStackSetDigest == "" || set.SetDigest != req.Binding.FormationStackSetDigest {
			return nil, fmt.Errorf("evaluation: resolve assignment models: frozen formation stack set: %w", constants.ErrFormationStackMismatch)
		}
		if err := ValidateHeterogeneousStackSet(set); err != nil {
			return nil, fmt.Errorf("evaluation: resolve assignment models: frozen formation stack set: %w", err)
		}
		stack := target.Heterogeneous.GetStack()
		if err := ValidateHeterogeneousStackDigest(stack); err != nil {
			return nil, fmt.Errorf("evaluation: resolve assignment models: assignment stack: %w", err)
		}
		found := false
		for _, frozen := range set.Stacks {
			if frozen.GetStackId() == stack.GetStackId() && frozen.GetStackDigest() == stack.GetStackDigest() {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("evaluation: resolve assignment models: assignment stack is not in the frozen set: %w", constants.ErrFormationStackMismatch)
		}
		for _, slot := range []struct {
			role    evalv1.ModelCampaignRole
			binding *evalv1.RoleAssignment
		}{
			{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, stack.GetPrimarySlot()},
			{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, stack.GetAssistantSlot()},
			{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, stack.GetLiteSlot()},
		} {
			if slot.binding == nil || slot.binding.GetDesignatedRole() != slot.role || slot.binding.GetVariantId() == "" {
				return nil, fmt.Errorf("evaluation: resolve assignment models: stack %q role binding: %w", stack.GetStackId(), constants.ErrFormationRegistryBinding)
			}
			variant := modelsByID[slot.binding.GetVariantId()]
			if variant == nil {
				return nil, fmt.Errorf("evaluation: resolve assignment models: stack %q variant %q is not frozen: %w", stack.GetStackId(), slot.binding.GetVariantId(), constants.ErrFormationRegistryBinding)
			}
			resolved[slot.role] = variant
		}
	default:
		return nil, fmt.Errorf("evaluation: resolve assignment models: target: %w", constants.ErrEvaluationTargetKindMismatch)
	}
	return resolved, nil
}

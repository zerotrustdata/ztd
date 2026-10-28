// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// HeterogeneousCoverageMatrix records which variant-role placements are
// satisfied by a stack set. Its field names are the persisted JSON keys.
type HeterogeneousCoverageMatrix struct {
	HypothesisStackCount int                 `json:"hypothesis_stack_count,omitempty"`
	CoverageStackCount   int                 `json:"coverage_stack_count,omitempty"`
	TotalStackCount      int                 `json:"total_stack_count,omitempty"`
	VariantRoleCoverage  map[string][]string `json:"variant_role_coverage,omitempty"`
}

// HeterogeneousStackSet is the frozen stack set of one system-lane campaign:
// one stack per formation, bound to the campaign's frozen model registry.
type HeterogeneousStackSet struct {
	CampaignID     string                                 `json:"campaign_id,omitempty"`
	GenerationRule string                                 `json:"generation_rule,omitempty"`
	Seed           uint64                                 `json:"seed,omitempty"`
	SetDigest      string                                 `json:"set_digest,omitempty"`
	VariantIDs     []string                               `json:"variant_ids,omitempty"`
	Stacks         []*evalv1.HeterogeneousStackDefinition `json:"stacks,omitempty"`
	Coverage       *HeterogeneousCoverageMatrix           `json:"coverage,omitempty"`
}

// ValidateHeterogeneousStackSet verifies stack digests, coverage, and set digest.
func ValidateHeterogeneousStackSet(set *HeterogeneousStackSet) error {
	if set == nil || set.CampaignID == "" || set.GenerationRule == "" || len(set.Stacks) == 0 || set.Coverage == nil {
		return fmt.Errorf("evaluation: validate heterogeneous stack set: %w", constants.ErrMissingRequiredField)
	}
	expectedDigest, err := ComputeHeterogeneousStackSetDigest(set)
	if err != nil {
		return err
	}
	if set.SetDigest != expectedDigest {
		return fmt.Errorf("evaluation: validate heterogeneous stack set: set digest: %w", constants.ErrChecksumMismatch)
	}
	seenStackIDs := make(map[string]struct{}, len(set.Stacks))
	for _, stack := range set.Stacks {
		if stack == nil || stack.GetStackId() == "" {
			return fmt.Errorf("evaluation: validate heterogeneous stack set: %w", constants.ErrMissingRequiredField)
		}
		if _, exists := seenStackIDs[stack.GetStackId()]; exists {
			return fmt.Errorf("evaluation: validate heterogeneous stack set: duplicate stack_id %q", stack.GetStackId())
		}
		seenStackIDs[stack.GetStackId()] = struct{}{}
		if err := ValidateHeterogeneousStackDigest(stack); err != nil {
			return err
		}
	}
	if set.GenerationRule != FormationCatalogStackGenerationRule {
		return fmt.Errorf("evaluation: validate heterogeneous stack set: generation rule %q: %w", set.GenerationRule, constants.ErrFormationRegistryBinding)
	}
	return validateFormationCatalogStackSet(set)
}

// ValidateFormationStackSetBinding verifies that a frozen system-lane stack
// set belongs to this campaign's frozen registry. Validation uses only the
// frozen snapshot; later edits to the live formation catalog cannot rebind it.
func ValidateFormationStackSetBinding(set *HeterogeneousStackSet, campaignID string, variants []*evalv1.ModelVariant) error {
	if err := ValidateHeterogeneousStackSet(set); err != nil {
		return err
	}
	if campaignID == "" || set.CampaignID != campaignID || len(variants) == 0 {
		return fmt.Errorf("evaluation: validate formation stack binding: campaign or registry: %w", constants.ErrFormationRegistryBinding)
	}
	registry := make(map[string]*evalv1.ModelVariant, len(variants))
	for _, variant := range variants {
		if variant == nil || variant.GetVariantId() == "" || variant.GetServedModelTag() == "" || variant.GetModelDigest() == "" {
			return fmt.Errorf("evaluation: validate formation stack binding: invalid frozen variant: %w", constants.ErrFormationRegistryBinding)
		}
		if _, exists := registry[variant.GetVariantId()]; exists {
			return fmt.Errorf("evaluation: validate formation stack binding: duplicate frozen variant %q: %w", variant.GetVariantId(), constants.ErrFormationRegistryBinding)
		}
		registry[variant.GetVariantId()] = variant
	}
	for _, variantID := range set.VariantIDs {
		if _, ok := registry[variantID]; !ok {
			return fmt.Errorf("evaluation: validate formation stack binding: variant %q not in campaign registry: %w", variantID, constants.ErrFormationRegistryBinding)
		}
	}
	used := make(map[string]struct{})
	for _, stack := range set.Stacks {
		seen := make(map[string]bool)
		for i, slot := range []*evalv1.RoleAssignment{stack.GetPrimarySlot(), stack.GetAssistantSlot(), stack.GetLiteSlot()} {
			roles := []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT, evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE}
			if slot == nil || slot.GetDesignatedRole() != roles[i] || registry[slot.GetVariantId()] == nil || seen[slot.GetVariantId()] {
				return fmt.Errorf("evaluation: validate formation stack binding: invalid slot: %w", constants.ErrFormationRegistryBinding)
			}
			seen[slot.GetVariantId()] = true
			used[slot.GetVariantId()] = struct{}{}
		}
	}
	if len(used) != len(set.VariantIDs) {
		return fmt.Errorf("evaluation: validate formation stack binding: frozen variant coverage: %w", constants.ErrFormationRegistryBinding)
	}
	return nil
}

func validateCampaignFormationDigest(spec *evalv1.EvaluationCampaignSpec, set *HeterogeneousStackSet) error {
	if spec.GetFormationStackSetDigest() == "" || set == nil || set.SetDigest != spec.GetFormationStackSetDigest() {
		return fmt.Errorf("evaluation: formation set is not bound by campaign spec: %w", constants.ErrFormationStackMismatch)
	}
	return ValidateFormationStackSetBinding(set, spec.GetCampaignId(), spec.GetModelRegistry())
}

func formationStackSetContains(set *HeterogeneousStackSet, candidate *evalv1.HeterogeneousStackDefinition) bool {
	if set == nil || candidate == nil || candidate.GetStackId() == "" || candidate.GetStackDigest() == "" {
		return false
	}
	for _, stack := range set.Stacks {
		if stack.GetStackId() == candidate.GetStackId() && stack.GetStackDigest() == candidate.GetStackDigest() {
			return true
		}
	}
	return false
}

func roleCoverageFromStacks(stacks []*evalv1.HeterogeneousStackDefinition) map[string]map[string]struct{} {
	coverage := make(map[string]map[string]struct{})
	for _, stack := range stacks {
		recordRoleCoverage(coverage, stack.GetPrimarySlot().GetVariantId(), roleShortName(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY))
		recordRoleCoverage(coverage, stack.GetAssistantSlot().GetVariantId(), roleShortName(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT))
		recordRoleCoverage(coverage, stack.GetLiteSlot().GetVariantId(), roleShortName(evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE))
	}
	return coverage
}

func recordRoleCoverage(coverage map[string]map[string]struct{}, variantID, role string) {
	if variantID == "" || role == "" {
		return
	}
	if coverage[variantID] == nil {
		coverage[variantID] = make(map[string]struct{})
	}
	coverage[variantID][role] = struct{}{}
}

func materializeStack(stackID string, primary, assistant, lite *evalv1.ModelVariant) (*evalv1.HeterogeneousStackDefinition, error) {
	if primary == nil || assistant == nil || lite == nil {
		return nil, fmt.Errorf("evaluation: materialize heterogeneous stack: %w", constants.ErrMissingRequiredField)
	}
	if primary.GetVariantId() == assistant.GetVariantId() || primary.GetVariantId() == lite.GetVariantId() || assistant.GetVariantId() == lite.GetVariantId() {
		return nil, fmt.Errorf("evaluation: materialize heterogeneous stack %s: roles must bind distinct variants", stackID)
	}
	stack := &evalv1.HeterogeneousStackDefinition{
		StackId: stackID,
		PrimarySlot: &evalv1.RoleAssignment{
			DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			VariantId:      primary.GetVariantId(),
		},
		AssistantSlot: &evalv1.RoleAssignment{
			DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT,
			VariantId:      assistant.GetVariantId(),
		},
		LiteSlot: &evalv1.RoleAssignment{
			DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE,
			VariantId:      lite.GetVariantId(),
		},
	}
	digest, err := ComputeHeterogeneousStackDigest(stack)
	if err != nil {
		return nil, err
	}
	stack.StackDigest = digest
	return stack, nil
}

func roleShortName(role evalv1.ModelCampaignRole) string {
	switch role {
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY:
		return "primary"
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_ASSISTANT:
		return "assistant"
	case evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE:
		return "lite"
	default:
		return role.String()
	}
}

// IsHeterogeneousAssignment reports whether one assignment targets a frozen
// heterogeneous stack instead of a single homogeneous model-role cell.
func IsHeterogeneousAssignment(assignment *evalv1.EvaluationAssignment) bool {
	if assignment == nil {
		return false
	}
	heterogeneous, ok := assignment.GetTarget().(*evalv1.EvaluationAssignment_Heterogeneous)
	return ok && heterogeneous.Heterogeneous != nil && heterogeneous.Heterogeneous.GetStack() != nil
}

// AssignmentDesignatedRole returns the model role one assignment executes,
// verifies, and projects under: the designated role its homogeneous or
// heterogeneous target declares. A nil assignment, a target without a role, or an
// UNSPECIFIED or unknown role fails closed; there is no PRIMARY default.
func AssignmentDesignatedRole(assignment *evalv1.EvaluationAssignment) (evalv1.ModelCampaignRole, error) {
	var role evalv1.ModelCampaignRole
	switch {
	case assignment.GetHomogeneous() != nil:
		role = assignment.GetHomogeneous().GetDesignatedRole()
	case assignment.GetHeterogeneous() != nil:
		role = assignment.GetHeterogeneous().GetDesignatedRole()
	}
	if role == evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED {
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED, fmt.Errorf("evaluation: assignment designated role: %w", constants.ErrMissingRequiredField)
	}
	if _, err := modelCampaignRoleLabel(role); err != nil {
		return evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_UNSPECIFIED, fmt.Errorf("evaluation: assignment designated role: %w", err)
	}
	return role, nil
}

// HeterogeneousStackFromAssignment returns the frozen stack binding for one
// system-lane assignment.
func HeterogeneousStackFromAssignment(assignment *evalv1.EvaluationAssignment) (*evalv1.HeterogeneousStackDefinition, error) {
	if !IsHeterogeneousAssignment(assignment) {
		return nil, fmt.Errorf("evaluation: heterogeneous stack: %w", constants.ErrMissingRequiredField)
	}
	stack := assignment.GetHeterogeneous().GetStack()
	if err := ValidateHeterogeneousStackDigest(stack); err != nil {
		return nil, err
	}
	return stack, nil
}

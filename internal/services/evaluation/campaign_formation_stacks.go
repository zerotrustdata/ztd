// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"fmt"
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

const (
	// FormationCatalogStackGenerationRule identifies stack sets materialized from
	// the checked-in ExecutionTopologies catalog instead of registry hypotheses.
	FormationCatalogStackGenerationRule = FormationSchemaVersion
)

// FormationCatalogStackGenerationRequest carries frozen inputs for one
// deterministic formation-catalog stack set.
type FormationCatalogStackGenerationRequest struct {
	CampaignID string
	Seed       uint64
	Variants   []*evalv1.ModelVariant
	// FormationIDs names the catalog formations to materialize. Empty means
	// every catalog formation.
	FormationIDs []string
}

// GenerateFormationCatalogStackSet materializes one heterogeneous stack per
// checked-in formation and validates that the frozen registry covers every
// catalog served tag before persisting the set.
func GenerateFormationCatalogStackSet(req FormationCatalogStackGenerationRequest) (*HeterogeneousStackSet, error) {
	if req.CampaignID == "" {
		return nil, fmt.Errorf("evaluation: generate formation catalog stack set: %w", constants.ErrMissingRequiredField)
	}
	formations, err := selectFormations(req.FormationIDs)
	if err != nil {
		return nil, fmt.Errorf("evaluation: generate formation catalog stack set: %w", err)
	}
	if len(formations) == 0 {
		return nil, fmt.Errorf("evaluation: generate formation catalog stack set: catalog is empty: %w", constants.ErrFormationInvalid)
	}
	registry := indexFormationVariants(req.Variants)
	stacks := make([]*evalv1.HeterogeneousStackDefinition, 0, len(formations))
	registryVariantIDs := make(map[string]struct{})
	for _, formation := range formations {
		stack, err := formationRegistryStack(formation, registry)
		if err != nil {
			return nil, fmt.Errorf("evaluation: generate formation catalog stack set: %w", err)
		}
		stacks = append(stacks, stack)
		for _, slot := range []*evalv1.RoleAssignment{stack.GetPrimarySlot(), stack.GetAssistantSlot(), stack.GetLiteSlot()} {
			registryVariantIDs[slot.GetVariantId()] = struct{}{}
		}
	}
	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].GetStackId() < stacks[j].GetStackId()
	})
	variantIDs := make([]string, 0, len(registryVariantIDs))
	for variantID := range registryVariantIDs {
		variantIDs = append(variantIDs, variantID)
	}
	sort.Strings(variantIDs)
	set := &HeterogeneousStackSet{
		CampaignID:     req.CampaignID,
		GenerationRule: FormationCatalogStackGenerationRule,
		Seed:           req.Seed,
		VariantIDs:     variantIDs,
		Stacks:         stacks,
		Coverage:       buildFormationCatalogCoverageMatrix(stacks),
	}
	digest, err := ComputeHeterogeneousStackSetDigest(set)
	if err != nil {
		return nil, err
	}
	set.SetDigest = digest
	if err := ValidateHeterogeneousStackSet(set); err != nil {
		return nil, err
	}
	return set, nil
}

func buildFormationCatalogCoverageMatrix(stacks []*evalv1.HeterogeneousStackDefinition) *HeterogeneousCoverageMatrix {
	coverage := roleCoverageFromStacks(stacks)
	public := make(map[string][]string, len(coverage))
	for variantID, roles := range coverage {
		roleList := make([]string, 0, len(roles))
		for role := range roles {
			roleList = append(roleList, role)
		}
		sort.Strings(roleList)
		public[variantID] = roleList
	}
	return &HeterogeneousCoverageMatrix{
		HypothesisStackCount: len(stacks),
		CoverageStackCount:   0,
		TotalStackCount:      len(stacks),
		VariantRoleCoverage:  public,
	}
}

// validateFormationCatalogStackSet checks a sealed formation stack set for
// self-consistency: every slot binds a variant frozen in the set. The live
// catalog is deliberately not consulted, so catalog edits after a campaign
// was created cannot invalidate or rebind its sealed stacks.
func validateFormationCatalogStackSet(set *HeterogeneousStackSet) error {
	if len(set.Stacks) == 0 {
		return fmt.Errorf("evaluation: validate formation catalog stack set: %w", constants.ErrMissingRequiredField)
	}
	frozen := make(map[string]struct{}, len(set.VariantIDs))
	for _, variantID := range set.VariantIDs {
		frozen[variantID] = struct{}{}
	}
	for _, stack := range set.Stacks {
		for _, slot := range []*evalv1.RoleAssignment{stack.GetPrimarySlot(), stack.GetAssistantSlot(), stack.GetLiteSlot()} {
			if _, ok := frozen[slot.GetVariantId()]; !ok {
				return fmt.Errorf("evaluation: validate formation catalog stack set: stack %q variant %q not in frozen variant set: %w", stack.GetStackId(), slot.GetVariantId(), constants.ErrFormationRegistryBinding)
			}
		}
	}
	return nil
}

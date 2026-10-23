// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"fmt"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// FormationBindingRequest binds one frozen campaign stack to frozen registry
// variants. Slot variant IDs, served tags, and sovereign digests all come from
// the frozen inventory; the live catalog is never consulted at bind time.
type FormationBindingRequest struct {
	Stack    *evalv1.HeterogeneousStackDefinition
	Variants []*evalv1.ModelVariant
}

// FormationBindingFromCatalog materializes the stack for one catalog formation
// against frozen registry variants and prepares its binding request.
func FormationBindingFromCatalog(formationID string, variants []*evalv1.ModelVariant) (FormationBindingRequest, error) {
	topologies, err := NewExecutionTopologies()
	if err != nil {
		return FormationBindingRequest{}, err
	}
	formation, err := topologies.Formation(formationID)
	if err != nil {
		return FormationBindingRequest{}, err
	}
	stack, err := formationRegistryStack(formation, indexFormationVariants(variants))
	if err != nil {
		return FormationBindingRequest{}, err
	}
	return FormationBindingRequest{Stack: stack, Variants: variants}, nil
}

// BindFormation resolves one frozen campaign stack against the frozen registry
// for a governed formation run. Catalog provider, family, and VRAM gates were
// enforced when the stack was materialized; binding fails closed when the
// stack digest is invalid, a slot variant is missing from the registry, or a
// sovereign digest is empty.
func BindFormation(req FormationBindingRequest) (Formation, error) {
	if req.Stack == nil {
		return Formation{}, fmt.Errorf("formation: bind: %w", constants.ErrMissingRequiredField)
	}
	if err := ValidateHeterogeneousStackDigest(req.Stack); err != nil {
		return Formation{}, err
	}
	stackID := req.Stack.GetStackId()
	if stackID == "" {
		return Formation{}, fmt.Errorf("formation: bind: %w", constants.ErrFormationInvalid)
	}
	registry := indexFormationVariants(req.Variants)
	primary, err := bindFormationRole(stackID, FormationRolePrimary, req.Stack.GetPrimarySlot(), registry)
	if err != nil {
		return Formation{}, err
	}
	assistant, err := bindFormationRole(stackID, FormationRoleAssistant, req.Stack.GetAssistantSlot(), registry)
	if err != nil {
		return Formation{}, err
	}
	lite, err := bindFormationRole(stackID, FormationRoleLite, req.Stack.GetLiteSlot(), registry)
	if err != nil {
		return Formation{}, err
	}
	formation := Formation{
		ID:                stackID,
		DisplayName:       stackID,
		Description:       "campaign formation stack",
		MaxVRAMMiB:        FormationMaxVRAMMiB,
		Primary:           primary,
		Assistant:         assistant,
		Lite:              lite,
		RelaxedValidation: true,
	}
	if err := formation.Validate(); err != nil {
		return Formation{}, err
	}
	return formation, nil
}

// formationRegistryStack validates one catalog formation and materializes its
// campaign stack with slot variant IDs taken from the frozen registry, so the
// sealed stack binds by registry identity alone at execution time.
func formationRegistryStack(formation Formation, registry formationVariantRegistry) (*evalv1.HeterogeneousStackDefinition, error) {
	if err := formation.Validate(); err != nil {
		return nil, err
	}
	bound := make(map[FormationRole]*evalv1.ModelVariant, 3)
	for _, role := range formation.Roles() {
		model, err := formation.Model(role)
		if err != nil {
			return nil, err
		}
		variant := lookupFormationVariant(registry, model.VariantID, model.ServedModelTag)
		if variant == nil {
			return nil, fmt.Errorf("formation %q role %s served tag %q: %w", formation.ID, role, model.ServedModelTag, constants.ErrFormationRegistryBinding)
		}
		if variant.GetServedModelTag() != model.ServedModelTag {
			return nil, fmt.Errorf("formation %q role %s: served tag %q does not match catalog %q: %w", formation.ID, role, variant.GetServedModelTag(), model.ServedModelTag, constants.ErrFormationStackMismatch)
		}
		if variant.GetProviderClass() != model.ProviderClass {
			return nil, fmt.Errorf("formation %q role %s: provider class %q does not match catalog %q: %w", formation.ID, role, variant.GetProviderClass(), model.ProviderClass, constants.ErrFormationStackMismatch)
		}
		if model.Trust == FormationTrustSovereign && variant.GetModelDigest() == "" {
			return nil, fmt.Errorf("formation %q role %s: %w", formation.ID, role, constants.ErrFormationAttestationRequired)
		}
		bound[role] = variant
	}
	return materializeStack(formation.ID, bound[FormationRolePrimary], bound[FormationRoleAssistant], bound[FormationRoleLite])
}

type formationVariantRegistry struct {
	byVariantID      map[string]*evalv1.ModelVariant
	byServedModelTag map[string]*evalv1.ModelVariant
}

func indexFormationVariants(variants []*evalv1.ModelVariant) formationVariantRegistry {
	registry := formationVariantRegistry{
		byVariantID:      make(map[string]*evalv1.ModelVariant, len(variants)),
		byServedModelTag: make(map[string]*evalv1.ModelVariant, len(variants)),
	}
	for _, variant := range variants {
		if variant == nil {
			continue
		}
		if variant.GetVariantId() != "" {
			registry.byVariantID[variant.GetVariantId()] = variant
		}
		if tag := variant.GetServedModelTag(); tag != "" {
			registry.byServedModelTag[tag] = variant
		}
	}
	return registry
}

func lookupFormationVariant(registry formationVariantRegistry, variantID, servedModelTag string) *evalv1.ModelVariant {
	if tag := strings.TrimSpace(servedModelTag); tag != "" {
		if variant := registry.byServedModelTag[tag]; variant != nil {
			return variant
		}
	}
	return registry.byVariantID[variantID]
}

func bindFormationRole(stackID string, role FormationRole, slot *evalv1.RoleAssignment, registry formationVariantRegistry) (FormationModel, error) {
	if slot == nil || slot.GetVariantId() == "" {
		return FormationModel{}, fmt.Errorf("formation %q role %s: %w", stackID, role, constants.ErrFormationRegistryBinding)
	}
	variant := registry.byVariantID[slot.GetVariantId()]
	if variant == nil {
		return FormationModel{}, fmt.Errorf("formation %q role %s: variant %q not in frozen registry: %w", stackID, role, slot.GetVariantId(), constants.ErrFormationRegistryBinding)
	}
	model := FormationModelFromVariant(variant)
	if model.Trust == FormationTrustSovereign && model.ModelDigest == "" {
		return FormationModel{}, fmt.Errorf("formation %q role %s: %w", stackID, role, constants.ErrFormationAttestationRequired)
	}
	return model, nil
}

// FormationModelFromVariant derives a role binding from one model-inventory
// variant. Provider defaults to the model family, and sovereign VRAM is
// estimated from the parameter count and quantization.
func FormationModelFromVariant(variant *evalv1.ModelVariant) FormationModel {
	if variant == nil {
		return FormationModel{}
	}
	trust := FormationTrustSovereign
	providerClass := variant.GetProviderClass()
	if providerClass == "gemini" {
		trust = FormationTrustDelegated
	}
	provider := variant.GetModelFamily()
	if provider == "" {
		provider = variant.GetVariantId()
	}
	family := variant.GetModelFamily()
	if family == "" {
		family = variant.GetVariantId()
	}
	quantization := variant.GetQuantization()
	var modelVRAM, kvCacheVRAM uint64
	if trust == FormationTrustSovereign {
		if quantization == "" {
			quantization = "Q4_K_M"
		}
		modelVRAM = EstimateFormationModelVRAMMiB(variant.GetParameterCount(), quantization)
		kvCacheVRAM = EstimateFormationKVCacheMiB(variant.GetParameterCount())
	}
	return FormationModel{
		VariantID:             variant.GetVariantId(),
		DisplayName:           variant.GetServedModelTag(),
		Provider:              provider,
		Family:                family,
		ProviderClass:         providerClass,
		ServedModelTag:        variant.GetServedModelTag(),
		Trust:                 trust,
		Quantization:          quantization,
		ParameterCount:        variant.GetParameterCount(),
		EstimatedModelVRAMMiB: modelVRAM,
		EstimatedKVCacheMiB:   kvCacheVRAM,
		ModelDigest:           variant.GetModelDigest(),
	}
}

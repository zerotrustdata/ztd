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
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// FormationCatalogServedTags returns the unique served model tags required by
// the checked-in ExecutionTopologies catalog, sorted lexicographically.
func FormationCatalogServedTags() []string {
	topologies, err := NewExecutionTopologies()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, formation := range topologies.Formations() {
		for _, model := range formation.Models() {
			seen[model.ServedModelTag] = struct{}{}
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

// selectFormations returns the named catalog formations in the order given,
// or the whole catalog when no ID is named.

func selectFormations(topologies *ExecutionTopologies, formationIDs []string) ([]Formation, error) {
	if topologies == nil {
		var err error
		topologies, err = NewExecutionTopologies()
		if err != nil {
			return nil, err
		}
	}
	if len(formationIDs) == 0 {
		return topologies.Formations(), nil
	}
	seen := make(map[string]struct{}, len(formationIDs))
	formations := make([]Formation, 0, len(formationIDs))
	for _, id := range formationIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		formation, err := topologies.Formation(id)
		if err != nil {
			return nil, err
		}
		formations = append(formations, formation)
	}
	return formations, nil
}

// MaterializeFormationVariants selects the registry variants the named
// formations require, or every catalog formation when none is named. Every
// served tag, sovereign or delegated, must be present in the source with a
// model digest. A nil catalog selects the built-in defaults.
func MaterializeFormationVariants(sourceVariants []*evalv1.ModelVariant, formationIDs []string, catalog *ExecutionTopologies) ([]*evalv1.ModelVariant, error) {
	formations, err := selectFormations(catalog, formationIDs)
	if err != nil {
		return nil, err
	}
	registry := indexFormationVariants(sourceVariants)
	selected := make(map[string]*evalv1.ModelVariant)
	for _, formation := range formations {
		for _, model := range formation.Models() {
			if _, exists := selected[model.ServedModelTag]; exists {
				continue
			}
			// A delegated model binds the hosted model identity digest its
			// provider inventory reported, as a sovereign model binds its weights.
			variant := lookupFormationVariant(registry, model.VariantID, model.ServedModelTag)
			if variant == nil {
				return nil, fmt.Errorf("evaluation: formation catalog inventory: served tag %q: %w", model.ServedModelTag, constants.ErrInferenceModelNotFound)
			}
			if variant.GetModelDigest() == "" {
				return nil, fmt.Errorf("evaluation: formation catalog inventory: served tag %q: %w", model.ServedModelTag, constants.ErrFormationAttestationRequired)
			}
			selected[model.ServedModelTag] = proto.Clone(variant).(*evalv1.ModelVariant)
		}
	}
	tags := make([]string, 0, len(selected))
	for tag := range selected {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	picked := make([]*evalv1.ModelVariant, 0, len(tags))
	for _, tag := range tags {
		picked = append(picked, selected[tag])
	}
	return picked, nil
}

// FormationCatalogPullEntries returns model pull entries for every sovereign
// served tag in the checked-in ExecutionTopologies catalog.
func FormationCatalogPullEntries() ([]ModelPullEntry, error) {
	topologies, err := NewExecutionTopologies()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	models := make([]ModelPullEntry, 0, len(FormationCatalogServedTags()))
	for _, formation := range topologies.Formations() {
		for _, model := range formation.Models() {
			if model.Trust != FormationTrustSovereign {
				continue
			}
			tag := strings.TrimSpace(model.ServedModelTag)
			if tag == "" {
				continue
			}
			if _, exists := seen[tag]; exists {
				continue
			}
			seen[tag] = struct{}{}
			models = append(models, ModelPullEntry{
				VariantID:      model.VariantID,
				ServedModelTag: tag,
				ModelFamily:    model.Family,
				Quantization:   model.Quantization,
				Staging: ModelPullStaging{
					Method:     "ollama_library_pull",
					Status:     "ready",
					OllamaPull: tag,
				},
			})
		}
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ServedModelTag < models[j].ServedModelTag
	})
	if len(models) == 0 {
		return nil, fmt.Errorf("evaluation: formation catalog pull entries: %w", constants.ErrMissingRequiredField)
	}
	return models, nil
}

// FormationCatalogFixtureVariants returns typed registry variants covering every
// served tag in the checked-in catalog. digestForTag supplies deterministic
// digests for hermetic tests and is required.
func FormationCatalogFixtureVariants(digestForTag func(string) string) []*evalv1.ModelVariant {
	if digestForTag == nil {
		return nil
	}
	topologies, err := NewExecutionTopologies()
	if err != nil {
		return nil
	}
	seen := make(map[string]*evalv1.ModelVariant)
	for _, formation := range topologies.Formations() {
		for _, model := range formation.Models() {
			if _, exists := seen[model.ServedModelTag]; exists {
				continue
			}
			variant := formationCatalogFixtureVariant(model, digestForTag(model.ServedModelTag))
			seen[model.ServedModelTag] = variant
		}
	}
	tags := FormationCatalogServedTags()
	variants := make([]*evalv1.ModelVariant, 0, len(tags))
	for _, tag := range tags {
		variants = append(variants, seen[tag])
	}
	return variants
}

func formationCatalogFixtureVariant(model FormationModel, digest string) *evalv1.ModelVariant {
	variant := &evalv1.ModelVariant{
		VariantId:      "freeze-" + model.VariantID,
		ProviderClass:  model.ProviderClass,
		ServedModelTag: model.ServedModelTag,
		ModelDigest:    digest,
		ModelFamily:    model.Family,
		ParameterCount: model.ParameterCount,
		Quantization:   model.Quantization,
	}
	if model.Trust == FormationTrustDelegated {
		variant.Quantization = ""
		variant.ParameterCount = 0
	}
	return variant
}

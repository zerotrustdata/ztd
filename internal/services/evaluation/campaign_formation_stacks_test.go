// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func testFormationCatalogVariants() []*evalv1.ModelVariant {
	return formationCatalogTestVariants()
}

func formationCatalogTestVariants() []*evalv1.ModelVariant {
	digestByTag := map[string]byte{
		"deepseek-r1:7b": '4',
		"ministral-3:8b": '1',
		"ministral-3:3b": '2',
		"gemma3:270m":    '3',
		"llama3.2:1b":    '5',
		"granite4.2:8b":  '6',
		"qwen3:4b":       '7',
		"gemma3:1b":      '8',
	}
	return FormationCatalogFixtureVariants(func(tag string) string {
		ch := digestByTag[tag]
		if ch == 0 {
			ch = 'a'
		}
		return repeatHex(ch)
	})
}

func TestGenerateFormationCatalogStackSet_ReturnsEveryCatalogFormation(t *testing.T) {
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: "eval-formations-benchmark",
		Seed:       17,
		Variants:   testFormationCatalogVariants(),
	})
	require.NoError(t, err)
	assert.Equal(t, FormationCatalogStackGenerationRule, set.GenerationRule)
	catalog, err := NewExecutionTopologies()
	require.NoError(t, err)
	assert.Len(t, set.Stacks, len(catalog.Formations()))
	assert.Equal(t, len(catalog.Formations()), set.Coverage.HypothesisStackCount)
	assert.Equal(t, 0, set.Coverage.CoverageStackCount)
	assert.Len(t, set.VariantIDs, len(FormationCatalogServedTags()))
	require.NoError(t, ValidateHeterogeneousStackSet(set))

	stackIDs := make([]string, 0, len(set.Stacks))
	for _, stack := range set.Stacks {
		stackIDs = append(stackIDs, stack.GetStackId())
	}
	catalogIDs := make([]string, 0, len(catalog.Formations()))
	for _, formation := range catalog.Formations() {
		catalogIDs = append(catalogIDs, formation.ID)
	}
	assert.ElementsMatch(t, catalogIDs, stackIDs)
}

func TestGenerateFormationCatalogStackSet_IsDeterministic(t *testing.T) {
	req := FormationCatalogStackGenerationRequest{
		CampaignID: "eval-formations-benchmark",
		Seed:       11,
		Variants:   testFormationCatalogVariants(),
	}
	left, err := GenerateFormationCatalogStackSet(req)
	require.NoError(t, err)
	right, err := GenerateFormationCatalogStackSet(req)
	require.NoError(t, err)
	assert.Equal(t, left.SetDigest, right.SetDigest)
}

func TestGenerateFormationCatalogStackSet_RejectsMissingRegistryVariant(t *testing.T) {
	variants := testFormationCatalogVariants()[:3]
	_, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: "eval-formations-benchmark",
		Variants:   variants,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationRegistryBinding)
}

func TestGenerateFormationCatalogStackSet_SelectsNamedFormations(t *testing.T) {
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   "eval-formations-subset",
		Seed:         3,
		Variants:     testFormationCatalogVariants(),
		FormationIDs: []string{"heterogeneous-0000", "heterogeneous-0001", "heterogeneous-0000"},
	})
	require.NoError(t, err)
	require.NoError(t, ValidateHeterogeneousStackSet(set))
	assert.Equal(t, 2, set.Coverage.HypothesisStackCount)

	stackIDs := make([]string, 0, len(set.Stacks))
	for _, stack := range set.Stacks {
		stackIDs = append(stackIDs, stack.GetStackId())
	}
	assert.ElementsMatch(t, []string{"heterogeneous-0000", "heterogeneous-0001"}, stackIDs)
	assert.Less(t, len(set.VariantIDs), len(FormationCatalogServedTags()), "only the named formations' models are bound")
}

func TestGenerateFormationCatalogStackSet_RejectsUnknownFormation(t *testing.T) {
	_, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   "eval-formations-subset",
		Variants:     testFormationCatalogVariants(),
		FormationIDs: []string{"no-such-formation"},
	})
	require.ErrorIs(t, err, constants.ErrFormationInvalid)
}

func TestValidateFormationCatalogStackSet_AcceptsSubsetsAndRejectsEmptySets(t *testing.T) {
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   "eval-formations-subset",
		Variants:     testFormationCatalogVariants(),
		FormationIDs: []string{"heterogeneous-0000"},
	})
	require.NoError(t, err)
	require.NoError(t, validateFormationCatalogStackSet(set))

	empty := *set
	empty.Stacks = nil
	require.Error(t, validateFormationCatalogStackSet(&empty))

	unfrozen := *set
	unfrozen.VariantIDs = set.VariantIDs[1:]
	require.ErrorIs(t, validateFormationCatalogStackSet(&unfrozen), constants.ErrFormationRegistryBinding)
}

func TestGenerateFormationCatalogStackSet_RejectsEmptySovereignDigest(t *testing.T) {
	variants := testFormationCatalogVariants()
	for _, variant := range variants {
		if variant.GetProviderClass() == "ollama" {
			variant.ModelDigest = ""
			break
		}
	}
	_, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: "eval-formations-benchmark",
		Variants:   variants,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationAttestationRequired)
}

func TestGenerateFormationCatalogStackSet_SealsRegistryVariantIDs(t *testing.T) {
	set, err := GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID:   "eval-formations-subset",
		Variants:     testFormationCatalogVariants(),
		FormationIDs: []string{"heterogeneous-0001"},
	})
	require.NoError(t, err)
	require.Len(t, set.Stacks, 1)
	assert.Equal(t, "freeze-deepseek-r1-7b", set.Stacks[0].GetPrimarySlot().GetVariantId(), "slots carry frozen registry IDs, not catalog IDs")

	bound, err := BindFormation(FormationBindingRequest{Stack: set.Stacks[0], Variants: testFormationCatalogVariants()})
	require.NoError(t, err)
	assert.Equal(t, "heterogeneous-0001", bound.ID)
	assert.Equal(t, repeatHex('4'), bound.Primary.ModelDigest)
	assert.Equal(t, "freeze-deepseek-r1-7b", bound.Primary.VariantID)
}

func TestBindFormation_BindsAllCatalogFormationsFromRegistry(t *testing.T) {
	topologies, err := NewExecutionTopologies()
	require.NoError(t, err)
	variants := testFormationCatalogVariants()
	registry := indexFormationVariants(variants)
	for _, formation := range topologies.Formations() {
		t.Run(formation.ID, func(t *testing.T) {
			stack, stackErr := formationRegistryStack(formation, registry)
			require.NoError(t, stackErr)
			bound, bindErr := BindFormation(FormationBindingRequest{Stack: stack, Variants: variants})
			require.NoError(t, bindErr)
			assert.Equal(t, formation.ID, bound.ID)
			for _, role := range formation.Roles() {
				catalogModel, modelErr := formation.Model(role)
				require.NoError(t, modelErr)
				boundModel, boundErr := bound.Model(role)
				require.NoError(t, boundErr)
				assert.Equal(t, catalogModel.ServedModelTag, boundModel.ServedModelTag)
				if catalogModel.Trust == FormationTrustSovereign {
					assert.NotEmpty(t, boundModel.ModelDigest)
				}
			}
		})
	}
}

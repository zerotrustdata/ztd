// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License 2.0.

package evaluation

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestFormationCatalogServedTags_ReturnsSortedUniqueTags(t *testing.T) {
	tags := FormationCatalogServedTags()
	require.Len(t, tags, 63)
	sort.Strings(tags)
	assert.Equal(t, FormationCatalogServedTags(), tags)
	assert.Contains(t, tags, "ministral-3:8b")
}

func TestFormationCatalogIntakeModels_ReturnsOneSovereignLibraryPullPerTag(t *testing.T) {
	models, err := FormationCatalogIntakeModels()
	require.NoError(t, err)
	require.Len(t, models, 63)
	for _, model := range models {
		assert.Equal(t, "ollama_library_pull", model.Staging.Method)
		assert.Equal(t, model.ServedModelTag, model.Staging.OllamaPull)
	}
}

func TestMaterializeFormationCatalogVariants_SelectsAllSovereignCatalogTags(t *testing.T) {
	partial := []*evalv1.ModelVariant{
		{VariantId: "freeze-ministral-3-8b", ProviderClass: "ollama", ServedModelTag: "ministral-3:8b", ModelDigest: repeatHex('5'), ModelFamily: "Mistral 3", Quantization: "Q4_K_M"},
		{VariantId: "freeze-qwen2-5-1-5b", ProviderClass: "ollama", ServedModelTag: "qwen2.5:1.5b-instruct-q4_K_M", ModelDigest: repeatHex('2'), ModelFamily: "Qwen 2", Quantization: "Q4_K_M"},
		{VariantId: "freeze-gemma3-270m", ProviderClass: "ollama", ServedModelTag: "gemma3:270m", ModelDigest: repeatHex('6'), ModelFamily: "Gemma 3", Quantization: "Q8_0"},
	}
	_, err := MaterializeFormationVariants(partial, nil, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrInferenceModelNotFound)

	source := FormationCatalogFixtureVariants(func(string) string { return repeatHex('a') })
	selected, err := MaterializeFormationVariants(source, nil, nil)
	require.NoError(t, err)
	require.Len(t, selected, 63)

	freeze, err := MaterializeModelRegistry("eval-formations-benchmark", selected)
	require.NoError(t, err)
	require.NoError(t, ValidateModelRegistry(freeze))
	_, err = GenerateFormationCatalogStackSet(FormationCatalogStackGenerationRequest{
		CampaignID: "eval-formations-benchmark",
		Seed:       17,
		Variants:   selected,
	})
	require.NoError(t, err)
}

func TestMaterializeFormationVariants_SelectsOnlyTheNamedFormationsModels(t *testing.T) {
	source := FormationCatalogFixtureVariants(func(string) string { return repeatHex('a') })

	selected, err := MaterializeFormationVariants(source, []string{"heterogeneous-0000"}, nil)
	require.NoError(t, err)
	tags := make([]string, 0, len(selected))
	for _, variant := range selected {
		tags = append(tags, variant.GetServedModelTag())
	}
	assert.Equal(t, []string{
		"gemma3:270m",
		"ministral-3:8b",
		"qwen2.5:1.5b-instruct-q4_K_M",
	}, tags)

	all, err := MaterializeFormationVariants(source, nil, nil)
	require.NoError(t, err)
	assert.Len(t, all, 63)

	_, err = MaterializeFormationVariants(source, []string{"no-such-formation"}, nil)
	require.ErrorIs(t, err, constants.ErrFormationInvalid)
}

func TestFormationCatalogFixtureVariants_MatchesCatalogServedTags(t *testing.T) {
	variants := FormationCatalogFixtureVariants(func(string) string { return repeatHex('b') })
	require.Len(t, variants, 63)
	tags := make([]string, 0, len(variants))
	for _, variant := range variants {
		tags = append(tags, variant.GetServedModelTag())
	}
	sort.Strings(tags)
	assert.Equal(t, FormationCatalogServedTags(), tags)
}

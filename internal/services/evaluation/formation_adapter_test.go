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

func catalogFormationRegistryVariants() []*evalv1.ModelVariant {
	return []*evalv1.ModelVariant{
		{
			VariantId:      "ministral-3-8b",
			ProviderClass:  "ollama",
			ServedModelTag: "ministral-3:8b",
			ModelDigest:    repeatHex('a'),
			ModelFamily:    "Mistral 3",
			ParameterCount: 8_900_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "qwen2-5-1-5b-instruct-q4-k-m",
			ProviderClass:  "ollama",
			ServedModelTag: "qwen2.5:1.5b-instruct-q4_K_M",
			ModelDigest:    repeatHex('b'),
			ModelFamily:    "Qwen 2",
			ParameterCount: 1_500_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "gemma3-270m",
			ProviderClass:  "ollama",
			ServedModelTag: "gemma3:270m",
			ModelDigest:    repeatHex('c'),
			ModelFamily:    "Gemma 3",
			ParameterCount: 268_000_000,
			Quantization:   "Q8_0",
		},
	}
}

func catalogFormationBindingRequest(t *testing.T) FormationBindingRequest {
	t.Helper()
	binding, err := FormationBindingFromCatalog("heterogeneous-0000", catalogFormationRegistryVariants())
	require.NoError(t, err)
	return binding
}

func TestFormationBindingFromCatalog_MaterializesCatalogFormationStack(t *testing.T) {
	binding, err := FormationBindingFromCatalog("heterogeneous-0000", catalogFormationRegistryVariants())
	require.NoError(t, err)
	assert.Equal(t, "heterogeneous-0000", binding.Stack.GetStackId())
	assert.Equal(t, "ministral-3-8b", binding.Stack.GetPrimarySlot().GetVariantId())
	require.NoError(t, ValidateHeterogeneousStackDigest(binding.Stack))
}

func TestBindFormation_BindsCatalogFormationDigestsFromFrozenRegistry(t *testing.T) {
	req := catalogFormationBindingRequest(t)

	formation, err := BindFormation(req)
	require.NoError(t, err)
	assert.Equal(t, repeatHex('a'), formation.Primary.ModelDigest)
	assert.Equal(t, repeatHex('b'), formation.Assistant.ModelDigest)
	assert.Equal(t, repeatHex('c'), formation.Lite.ModelDigest)
	assert.Equal(t, "ministral-3:8b", formation.Primary.ServedModelTag)
}

func TestFormationBindingFromCatalog_PrefersServedTagOverCollidingVariantID(t *testing.T) {
	variants := formationCatalogTestVariants()
	variants = append(variants, &evalv1.ModelVariant{
		VariantId:      "deepseek-r1-7b",
		ProviderClass:  "ollama",
		ServedModelTag: "deepseek-r1:7b-other",
		ModelDigest:    repeatHex('x'),
		ModelFamily:    "deepseek",
		Quantization:   "Q4_0",
	})

	binding, err := FormationBindingFromCatalog("heterogeneous-0001", variants)
	require.NoError(t, err)
	bound, err := BindFormation(binding)
	require.NoError(t, err)
	assert.Equal(t, "deepseek-r1:7b", bound.Primary.ServedModelTag)
	assert.Equal(t, repeatHex('4'), bound.Primary.ModelDigest)
}

// Frozen inventory IDs routinely differ from catalog IDs; the sealed stack must
// carry the inventory IDs so binding never depends on the live catalog.
func TestFormationBindingFromCatalog_SealsFreezeVariantIDsWhenCatalogIDsDiffer(t *testing.T) {
	variants := []*evalv1.ModelVariant{
		{
			VariantId:      "freeze-ministral-8b",
			ProviderClass:  "ollama",
			ServedModelTag: "ministral-3:8b",
			ModelDigest:    repeatHex('a'),
			ModelFamily:    "Mistral 3",
			ParameterCount: 8_900_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "freeze-qwen2-5-1-5b",
			ProviderClass:  "ollama",
			ServedModelTag: "qwen2.5:1.5b-instruct-q4_K_M",
			ModelDigest:    repeatHex('b'),
			ModelFamily:    "Qwen 2",
			ParameterCount: 1_500_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "freeze-gemma3-270m",
			ProviderClass:  "ollama",
			ServedModelTag: "gemma3:270m",
			ModelDigest:    repeatHex('c'),
			ModelFamily:    "Gemma 3",
			ParameterCount: 268_000_000,
			Quantization:   "Q8_0",
		},
	}

	binding, err := FormationBindingFromCatalog("heterogeneous-0000", variants)
	require.NoError(t, err)
	assert.Equal(t, "freeze-ministral-8b", binding.Stack.GetPrimarySlot().GetVariantId())
	formation, err := BindFormation(binding)
	require.NoError(t, err)
	assert.Equal(t, "freeze-ministral-8b", formation.Primary.VariantID)
	assert.Equal(t, repeatHex('a'), formation.Primary.ModelDigest)
	assert.Equal(t, "freeze-qwen2-5-1-5b", formation.Assistant.VariantID)
	assert.Equal(t, "freeze-gemma3-270m", formation.Lite.VariantID)
}

func TestBindFormation_RejectsMissingRegistryVariant(t *testing.T) {
	req := catalogFormationBindingRequest(t)
	req.Variants = req.Variants[:2]

	_, err := BindFormation(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationRegistryBinding)
}

func TestBindFormation_RejectsEmptySovereignDigest(t *testing.T) {
	req := catalogFormationBindingRequest(t)
	req.Variants[0].ModelDigest = ""

	_, err := BindFormation(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationAttestationRequired)
}

func TestFormationBindingFromCatalog_RejectsServedTagMismatch(t *testing.T) {
	variants := catalogFormationRegistryVariants()
	variants[0].ServedModelTag = "wrong:tag"

	_, err := FormationBindingFromCatalog("heterogeneous-0000", variants)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationStackMismatch)
}

func TestBindFormation_RejectsInvalidStackDigest(t *testing.T) {
	req := catalogFormationBindingRequest(t)
	req.Stack.StackDigest = "invalid"

	_, err := BindFormation(req)
	require.Error(t, err)
}

func TestFormationBindingFromCatalog_RejectsUnknownFormationID(t *testing.T) {
	_, err := FormationBindingFromCatalog("missing-formation", catalogFormationRegistryVariants())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationInvalid)
}

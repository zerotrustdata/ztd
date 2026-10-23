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

func ultraEfficientSpeedsterRegistryVariants() []*evalv1.ModelVariant {
	return []*evalv1.ModelVariant{
		{
			VariantId:      "qwen35-4b",
			ProviderClass:  "ollama",
			ServedModelTag: "qwen3.5:4b",
			ModelDigest:    repeatHex('a'),
			ModelFamily:    "Qwen 3.5",
			ParameterCount: 4_000_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "gemma4-e2b",
			ProviderClass:  "ollama",
			ServedModelTag: "gemma4:e2b",
			ModelDigest:    repeatHex('b'),
			ModelFamily:    "Gemma 4",
			ParameterCount: 5_100_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "llama32-1b-speed",
			ProviderClass:  "ollama",
			ServedModelTag: "llama3.2:1b",
			ModelDigest:    repeatHex('c'),
			ModelFamily:    "Llama 3.2",
			ParameterCount: 1_200_000_000,
			Quantization:   "Q8_0",
		},
	}
}

func ultraEfficientSpeedsterBindingRequest(t *testing.T) FormationBindingRequest {
	t.Helper()
	binding, err := FormationBindingFromCatalog("ultra-efficient-speedster", ultraEfficientSpeedsterRegistryVariants())
	require.NoError(t, err)
	return binding
}

func TestFormationBindingFromCatalog_MaterializesUltraEfficientSpeedsterStack(t *testing.T) {
	binding, err := FormationBindingFromCatalog("ultra-efficient-speedster", ultraEfficientSpeedsterRegistryVariants())
	require.NoError(t, err)
	assert.Equal(t, "ultra-efficient-speedster", binding.Stack.GetStackId())
	assert.Equal(t, "qwen35-4b", binding.Stack.GetPrimarySlot().GetVariantId())
	require.NoError(t, ValidateHeterogeneousStackDigest(binding.Stack))
}

func TestBindFormation_BindsUltraEfficientSpeedsterDigestsFromFrozenRegistry(t *testing.T) {
	req := ultraEfficientSpeedsterBindingRequest(t)

	formation, err := BindFormation(req)
	require.NoError(t, err)
	assert.Equal(t, repeatHex('a'), formation.Primary.ModelDigest)
	assert.Equal(t, repeatHex('b'), formation.Assistant.ModelDigest)
	assert.Equal(t, repeatHex('c'), formation.Lite.ModelDigest)
	assert.Equal(t, "qwen3.5:4b", formation.Primary.ServedModelTag)
}

func TestFormationBindingFromCatalog_PrefersServedTagOverCollidingVariantID(t *testing.T) {
	variants := formationCatalogTestVariants()
	variants = append(variants, &evalv1.ModelVariant{
		VariantId:      "gemma4-e4b",
		ProviderClass:  "ollama",
		ServedModelTag: "gemma4:e4b-other",
		ModelDigest:    repeatHex('x'),
		ModelFamily:    "gemma4",
		Quantization:   "Q4_0",
	})

	binding, err := FormationBindingFromCatalog("gemma-cascade", variants)
	require.NoError(t, err)
	bound, err := BindFormation(binding)
	require.NoError(t, err)
	assert.Equal(t, "gemma4:e4b", bound.Primary.ServedModelTag)
	assert.Equal(t, repeatHex('6'), bound.Primary.ModelDigest)
}

// Frozen inventory IDs routinely differ from catalog IDs; the sealed stack must
// carry the inventory IDs so binding never depends on the live catalog.
func TestFormationBindingFromCatalog_SealsFreezeVariantIDsWhenCatalogIDsDiffer(t *testing.T) {
	variants := []*evalv1.ModelVariant{
		{
			VariantId:      "qwen3-5-4b",
			ProviderClass:  "ollama",
			ServedModelTag: "qwen3.5:4b",
			ModelDigest:    repeatHex('a'),
			ModelFamily:    "Qwen 3.5",
			ParameterCount: 4_000_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "gemma4-e2b-q4-k-m",
			ProviderClass:  "ollama",
			ServedModelTag: "gemma4:e2b",
			ModelDigest:    repeatHex('b'),
			ModelFamily:    "Gemma 4",
			ParameterCount: 5_100_000_000,
			Quantization:   "Q4_K_M",
		},
		{
			VariantId:      "llama3-2-1b-q8-0",
			ProviderClass:  "ollama",
			ServedModelTag: "llama3.2:1b",
			ModelDigest:    repeatHex('c'),
			ModelFamily:    "Llama 3.2",
			ParameterCount: 1_200_000_000,
			Quantization:   "Q8_0",
		},
	}

	binding, err := FormationBindingFromCatalog("ultra-efficient-speedster", variants)
	require.NoError(t, err)
	assert.Equal(t, "qwen3-5-4b", binding.Stack.GetPrimarySlot().GetVariantId())
	formation, err := BindFormation(binding)
	require.NoError(t, err)
	assert.Equal(t, "qwen3-5-4b", formation.Primary.VariantID)
	assert.Equal(t, repeatHex('a'), formation.Primary.ModelDigest)
	assert.Equal(t, "gemma4-e2b-q4-k-m", formation.Assistant.VariantID)
	assert.Equal(t, "llama3-2-1b-q8-0", formation.Lite.VariantID)
}

func TestBindFormation_RejectsMissingRegistryVariant(t *testing.T) {
	req := ultraEfficientSpeedsterBindingRequest(t)
	req.Variants = req.Variants[:2]

	_, err := BindFormation(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationRegistryBinding)
}

func TestBindFormation_RejectsEmptySovereignDigest(t *testing.T) {
	req := ultraEfficientSpeedsterBindingRequest(t)
	req.Variants[0].ModelDigest = ""

	_, err := BindFormation(req)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationAttestationRequired)
}

func TestFormationBindingFromCatalog_RejectsServedTagMismatch(t *testing.T) {
	variants := ultraEfficientSpeedsterRegistryVariants()
	variants[0].ServedModelTag = "wrong:tag"

	_, err := FormationBindingFromCatalog("ultra-efficient-speedster", variants)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationStackMismatch)
}

func TestBindFormation_RejectsInvalidStackDigest(t *testing.T) {
	req := ultraEfficientSpeedsterBindingRequest(t)
	req.Stack.StackDigest = "invalid"

	_, err := BindFormation(req)
	require.Error(t, err)
}

func TestFormationBindingFromCatalog_RejectsUnknownFormationID(t *testing.T) {
	_, err := FormationBindingFromCatalog("missing-formation", ultraEfficientSpeedsterRegistryVariants())
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrFormationInvalid)
}

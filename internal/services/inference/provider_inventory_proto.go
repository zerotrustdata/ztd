// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ProviderModelInventoryEntryFromProto converts one protocol inventory entry
// into the evaluation/inference domain shape.
func ProviderModelInventoryEntryFromProto(entry *operatorv1.ProviderModelInventoryEntry) ProviderModelInventoryEntry {
	if entry == nil {
		return ProviderModelInventoryEntry{}
	}
	return ProviderModelInventoryEntry{
		ProviderClass:          entry.GetProviderClass(),
		ServedModelTag:         entry.GetServedModelTag(),
		ModelDigest:            entry.GetModelDigest(),
		ModelFamily:            entry.GetModelFamily(),
		ParameterSize:          entry.GetParameterSize(),
		ParameterCount:         entry.GetParameterCount(),
		ParameterCountSource:   entry.GetParameterCountSource(),
		Quantization:           entry.GetQuantization(),
		Format:                 entry.GetFormat(),
		ArtifactSizeBytes:      entry.GetArtifactSizeBytes(),
		ContextLimit:           entry.GetContextLimit(),
		AdvertisedCapabilities: append([]string(nil), entry.GetAdvertisedCapabilities()...),
	}
}

// ProviderModelInventoryEntriesFromProto converts protocol inventory entries
// into the evaluation/inference domain shape.
func ProviderModelInventoryEntriesFromProto(entries []*operatorv1.ProviderModelInventoryEntry) []ProviderModelInventoryEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]ProviderModelInventoryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ProviderModelInventoryEntryFromProto(entry))
	}
	return out
}

// ProviderModelInventoryEntriesToProto converts domain inventory entries into
// the protocol shape returned by governed Ollama maintenance queries.
func ProviderModelInventoryEntriesToProto(entries []ProviderModelInventoryEntry) []*operatorv1.ProviderModelInventoryEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]*operatorv1.ProviderModelInventoryEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, &operatorv1.ProviderModelInventoryEntry{
			ProviderClass:          entry.ProviderClass,
			ServedModelTag:         entry.ServedModelTag,
			ModelDigest:            entry.ModelDigest,
			ModelFamily:            entry.ModelFamily,
			ParameterSize:          entry.ParameterSize,
			ParameterCount:         entry.ParameterCount,
			ParameterCountSource:   entry.ParameterCountSource,
			Quantization:           entry.Quantization,
			Format:                 entry.Format,
			ArtifactSizeBytes:      entry.ArtifactSizeBytes,
			ContextLimit:           entry.ContextLimit,
			AdvertisedCapabilities: append([]string(nil), entry.AdvertisedCapabilities...),
		})
	}
	return out
}

// ProviderResidencyFromProto converts one protocol residency result into the
// inference domain shape. A resident model without a name cannot prove
// absence from residency, so it fails closed.
func ProviderResidencyFromProto(result *operatorv1.OllamaModelResidencyResult) (ProviderResidency, error) {
	if result == nil {
		return ProviderResidency{}, fmt.Errorf("inference: provider residency from proto: %w", constants.ErrMissingRequiredField)
	}
	residency := ProviderResidency{Models: make([]ProviderResidencyModel, 0, len(result.GetModels()))}
	for _, model := range result.GetModels() {
		if model.GetName() == "" {
			return ProviderResidency{}, fmt.Errorf("inference: provider residency from proto: %w: model name is required", constants.ErrInferenceProviderResponseInvalid)
		}
		residency.Models = append(residency.Models, ProviderResidencyModel{Name: model.GetName()})
	}
	return residency, nil
}

// ProviderResidencyToProto converts domain residency, as a provider read and
// validated it, into the protocol shape.
func ProviderResidencyToProto(residency ProviderResidency) *operatorv1.OllamaModelResidencyResult {
	models := make([]*operatorv1.OllamaModelResidencyModel, 0, len(residency.Models))
	for _, model := range residency.Models {
		models = append(models, &operatorv1.OllamaModelResidencyModel{Name: model.Name})
	}
	return &operatorv1.OllamaModelResidencyResult{Models: models}
}

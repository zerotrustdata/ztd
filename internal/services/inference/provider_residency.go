// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package inference

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ProviderResidencyModel is one model currently resident in the provider.
type ProviderResidencyModel struct {
	Name string
}

// ProviderResidency is the set of models a provider reports resident, used
// for residency postconditions. It does not claim that a provider is idle or
// that no request can start between samples.
type ProviderResidency struct {
	Models []ProviderResidencyModel
}

// ollamaPsResponse is the response body of Ollama's /api/ps endpoint.
type ollamaPsResponse struct {
	Models []ollamaPsModel `json:"models"`
}

type ollamaPsModel struct {
	Name string `json:"name"`
}

// ReadResidency reads and validates one bounded typed /api/ps response. It is
// bounded by ProviderStatusTimeout like every other read-only provider query.
func (b *OllamaBackend) ReadResidency(ctx context.Context) (ProviderResidency, error) {
	ctx, cancel := context.WithTimeout(ctx, ProviderStatusTimeout)
	defer cancel()
	var listed ollamaPsResponse
	if err := b.getJSON(ctx, "read residency", &listed, "api", "ps"); err != nil {
		return ProviderResidency{}, err
	}
	residency := ProviderResidency{Models: make([]ProviderResidencyModel, 0, len(listed.Models))}
	for _, model := range listed.Models {
		if model.Name == "" {
			return ProviderResidency{}, fmt.Errorf("ollama_backend: read residency: %w: model name is required", constants.ErrInferenceProviderResponseInvalid)
		}
		residency.Models = append(residency.Models, ProviderResidencyModel(model))
	}
	return residency, nil
}

// Names returns the served model tags of the resident models, in provider order.
func (r ProviderResidency) Names() []string {
	names := make([]string, 0, len(r.Models))
	for _, model := range r.Models {
		names = append(names, model.Name)
	}
	return names
}

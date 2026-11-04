// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// DefaultModelPullCatalogRelPath is the checked-in catalog `ztd eval models
// pull` selects from.
const DefaultModelPullCatalogRelPath = "eval/model-pull-catalog.json"

// ModelPullCatalog lists the models `ztd eval models pull` can stage on the
// provider and how each is staged.
type ModelPullCatalog struct {
	Description string           `json:"description"`
	Models      []ModelPullEntry `json:"models"`
}

// ModelPullEntry is one pull catalog model.
type ModelPullEntry struct {
	VariantID      string           `json:"variant_id"`
	ServedModelTag string           `json:"served_model_tag"`
	ModelFamily    string           `json:"model_family"`
	ParameterCount string           `json:"parameter_count"`
	Quantization   string           `json:"quantization"`
	ContextLimit   uint32           `json:"context_limit,omitempty"`
	Notes          string           `json:"notes,omitempty"`
	Staging        ModelPullStaging `json:"staging"`
}

// ModelPullStaging describes how one catalog model is staged on the provider.
type ModelPullStaging struct {
	Method          string `json:"method"`
	Status          string `json:"status,omitempty"`
	HFRepo          string `json:"hf_repo,omitempty"`
	HFGGUFRepo      string `json:"hf_gguf_repo,omitempty"`
	HFGGUFFile      string `json:"hf_gguf_file,omitempty"`
	OllamaPull      string `json:"ollama_pull,omitempty"`
	OllamaPullError string `json:"ollama_pull_error,omitempty"`
	Alias           string `json:"alias,omitempty"`
}

// LoadModelPullCatalog reads the checked-in model pull catalog.
func LoadModelPullCatalog(path string) (*ModelPullCatalog, error) {
	if path == "" {
		path = DefaultModelPullCatalogRelPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("evaluation: load model pull catalog: %w", err)
	}
	catalog := &ModelPullCatalog{}
	if err := json.Unmarshal(data, catalog); err != nil {
		return nil, fmt.Errorf("evaluation: load model pull catalog: decode: %w", err)
	}
	if len(catalog.Models) == 0 {
		return nil, fmt.Errorf("evaluation: load model pull catalog: %w", constants.ErrMissingRequiredField)
	}
	return catalog, nil
}

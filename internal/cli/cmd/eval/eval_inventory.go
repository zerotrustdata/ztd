// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func modelInventoryFreezeJSON(freeze *evaluation.ModelInventoryFreeze) ([]byte, error) {
	if freeze == nil {
		return nil, fmt.Errorf("evaluation: inventory freeze json: %w", constants.ErrMissingRequiredField)
	}
	variants := make([]json.RawMessage, 0, len(freeze.Variants))
	for _, variant := range freeze.Variants {
		body, err := protojson.Marshal(variant)
		if err != nil {
			return nil, fmt.Errorf("evaluation: inventory freeze json: marshal variant: %w", err)
		}
		variants = append(variants, body)
	}
	payload := struct {
		CampaignID           string                              `json:"campaign_id"`
		ModelRegistryDigest  string                              `json:"model_registry_digest"`
		ModelCount           int                                 `json:"model_count"`
		HomogeneousCellCount uint64                              `json:"homogeneous_cell_count"`
		Variants             []json.RawMessage                   `json:"variants"`
		InferenceVariants    []*operatorv1.InferenceModelVariant `json:"variants_inference"`
	}{
		CampaignID:           freeze.CampaignID,
		ModelRegistryDigest:  freeze.RegistryDigest,
		ModelCount:           len(freeze.Variants),
		HomogeneousCellCount: freeze.HomogeneousCellCount,
		Variants:             variants,
		InferenceVariants:    freeze.InferenceVariants,
	}
	return json.MarshalIndent(payload, "", "  ")
}

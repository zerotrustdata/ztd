// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// DefaultJudgeServedModelTag is the governed-registry model every new campaign
// freezes as its judge unless it names another one. Its digest is pinned from
// the model registry when the campaign freezes, never from this tag.
const DefaultJudgeServedModelTag = "gemma4:e4b"

// ResolveCampaignJudge returns the registry variant a campaign freezes as its
// judge: the one serving servedModelTag, or DefaultJudgeServedModelTag when it
// is empty. A tag the registry does not serve fails closed; the judge never
// falls back to a scored model.
func ResolveCampaignJudge(registry []*evalv1.ModelVariant, servedModelTag string) (*evalv1.ModelVariant, error) {
	if servedModelTag == "" {
		servedModelTag = DefaultJudgeServedModelTag
	}
	for _, variant := range registry {
		if variant.GetServedModelTag() != servedModelTag {
			continue
		}
		if err := ValidateCampaignJudge(variant); err != nil {
			return nil, err
		}
		return proto.Clone(variant).(*evalv1.ModelVariant), nil
	}
	return nil, fmt.Errorf("evaluation: resolve campaign judge %q: not in the model registry: %w", servedModelTag, constants.ErrEvaluationCampaignJudgeInvalid)
}

// ValidateCampaignJudge checks that a frozen judge names one digest-pinned
// served model.
func ValidateCampaignJudge(judge *evalv1.ModelVariant) error {
	if judge == nil || judge.GetVariantId() == "" || judge.GetServedModelTag() == "" || judge.GetProviderClass() == "" || !models.IsSHA256Hex(judge.GetModelDigest()) {
		return fmt.Errorf("evaluation: validate campaign judge: %w", constants.ErrEvaluationCampaignJudgeInvalid)
	}
	return nil
}

// JudgeModelRegistry is the single-entry inference registry, and its digest,
// that every judge dispatch of a campaign runs under. It keeps the judge out
// of the scored model registry, so a campaign's subject stays its only scored
// model, while the Inference Operator still admits the judge only as the exact
// frozen (tag, digest) pair.
func JudgeModelRegistry(campaignID string, judge *evalv1.ModelVariant) ([]*operatorv1.InferenceModelVariant, string, error) {
	if err := ValidateCampaignJudge(judge); err != nil {
		return nil, "", err
	}
	registry := InferenceVariantsFromEvalRegistry([]*evalv1.ModelVariant{judge})
	digest, err := models.ComputeInferenceModelRegistryDigest(campaignID, registry)
	if err != nil {
		return nil, "", fmt.Errorf("evaluation: judge model registry: %w", err)
	}
	return registry, digest, nil
}

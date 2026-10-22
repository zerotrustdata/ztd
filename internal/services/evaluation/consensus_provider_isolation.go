// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	"net/url"
	"strings"
)

// ValidateConsensusProviderIsolation refuses overlapping consensus and campaign providers.
// An unknown endpoint fails closed. Different URLs cannot prove physical separation behind aliases.
func ValidateConsensusProviderIsolation(binding *models.ConsensusPolicy, operators []*operatorv1.OperatorDocument, campaignOperatorID string) error {
	if binding == nil || binding.EffectiveVoteKind() != models.ConsensusVoteKindDecision {
		return nil
	}
	var consensus, campaign *operatorv1.OperatorDocument
	for _, op := range operators {
		if op.GetId() == binding.InferenceOperatorID {
			consensus = op
		}
		if op.GetId() == campaignOperatorID {
			campaign = op
		}
	}
	invalid := func() error {
		return fmt.Errorf("evaluation: consensus binding %s must use a separate Inference Operator and provider; rebind consensus or select another campaign provider: %w", binding.ID, constants.ErrEvaluationConsensusProviderShared)
	}
	if consensus == nil || campaign == nil || campaignOperatorID == binding.InferenceOperatorID {
		return invalid()
	}
	var a string
	var err error
	if binding.AllowHosted {
		a, err = canonicalInferenceEndpoint("https://api.typesafe.ai")
	} else {
		a, err = canonicalInferenceEndpoint(consensus.GetRuntimeConfig().GetInferenceOllamaEndpoint())
	}
	if err != nil {
		return invalid()
	}
	b, err := canonicalInferenceEndpoint(campaign.GetRuntimeConfig().GetInferenceOllamaEndpoint())
	if err != nil || a == b {
		return invalid()
	}
	return nil
}
func canonicalInferenceEndpoint(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", constants.ErrInferenceEndpointInvalid
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "::1" {
		host = "127.0.0.1"
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return host + ":" + port, nil
}

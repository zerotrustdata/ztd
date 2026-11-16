// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// hostedModelIdentityDomain separates hosted model identity digests from every
// other SHA-256 the platform computes.
const hostedModelIdentityDomain = "ztd.hosted-model-identity.v1\n"

// hostedModelIdentity is the canonical record a hosted model digest commits
// to. Field order is the serialization order.
type hostedModelIdentity struct {
	ProviderClass        string `json:"provider_class"`
	ProviderOrigin       string `json:"provider_origin"`
	Model                string `json:"model"`
	ProviderModelVersion string `json:"provider_model_version"`
}

// ComputeHostedModelIdentityDigest returns the model digest of a hosted model:
// lowercase hex SHA-256 over a domain-separated canonical record of the
// provider class, the provider API origin, the model ID, and the version the
// provider's model metadata reports for it. A hosted provider holds the
// weights, so this digest attests no weights; it binds the identity the
// provider declares, and it changes when the provider re-versions the model
// behind an unchanged ID. The Inference Operator recomputes it from a fresh
// metadata read before every governed hosted call.
func ComputeHostedModelIdentityDigest(providerClass, providerOrigin, model, providerModelVersion string) (string, error) {
	if !constants.IsHostedInferenceProvider(providerClass) || providerOrigin == "" || model == "" || providerModelVersion == "" {
		return "", fmt.Errorf("models: hosted model identity digest: %w", constants.ErrMissingRequiredField)
	}
	data, err := json.Marshal(hostedModelIdentity{
		ProviderClass:        providerClass,
		ProviderOrigin:       providerOrigin,
		Model:                model,
		ProviderModelVersion: providerModelVersion,
	})
	if err != nil {
		return "", fmt.Errorf("models: hosted model identity digest: %w", err)
	}
	return SHA256Hex(append([]byte(hostedModelIdentityDomain), data...)), nil
}

// HostedDispatchProviderClass returns the provider class a governed dispatch
// names for a model served by providerClass: the class itself when it is
// hosted, else empty, which selects the Inference Operator's local provider.
func HostedDispatchProviderClass(providerClass string) string {
	if constants.IsHostedInferenceProvider(providerClass) {
		return providerClass
	}
	return ""
}

// ValidateHostedEgress checks a generation result's hosted egress attestation
// against the request that produced it. A request with no provider class is
// served locally and carries no attestation. A hosted request's attestation
// must name its class, the class's fixed API origin, and the requested model;
// its identity digest must recompute from the provider metadata version it
// records; and its request hash must match the result's. When the request
// pinned a model digest, the attestation's digest must equal it and the
// provider must have served the requested model itself.
func ValidateHostedEgress(providerClass, requestedModel, requestedModelDigest string, result *operatorv1.InferenceResult) error {
	attestation := result.GetHostedEgress()
	if providerClass == "" {
		if attestation != nil {
			return fmt.Errorf("hosted egress attestation on a local generation: %w", constants.ErrInferenceIdentityMismatch)
		}
		return nil
	}
	origin, ok := constants.HostedInferenceProviderOrigin(providerClass)
	if !ok {
		return fmt.Errorf("provider class %q: %w", providerClass, constants.ErrInferenceHostedNotAllowed)
	}
	invalid := fmt.Errorf("hosted egress attestation: %w", constants.ErrInferenceIdentityMismatch)
	if attestation == nil ||
		!attestation.GetExplicitConsent() ||
		attestation.GetProviderClass() != providerClass ||
		attestation.GetProviderOrigin() != origin ||
		attestation.GetProvenance() != constants.HostedModelProvenance ||
		attestation.GetRequestedModel() != requestedModel ||
		result.GetModel() != requestedModel ||
		attestation.GetModelVersion() == "" ||
		attestation.GetNormalizedRequestHash() != result.GetNormalizedRequestHash() ||
		!IsSHA256Hex(attestation.GetRawResponseDigest()) ||
		attestation.GetRawResponseSizeBytes() == 0 {
		return invalid
	}
	digest, err := ComputeHostedModelIdentityDigest(providerClass, origin, requestedModel, attestation.GetProviderModelVersion())
	if err != nil || digest != attestation.GetModelIdentityDigest() {
		return invalid
	}
	if requestedModelDigest != "" {
		if digest != requestedModelDigest || result.GetServedModelDigest() != requestedModelDigest {
			return fmt.Errorf("hosted model identity: %w", constants.ErrInferenceModelDigestMismatch)
		}
		if attestation.GetModelVersion() != requestedModel {
			return fmt.Errorf("hosted served model: %w", constants.ErrInferenceModelDigestMismatch)
		}
	}
	return nil
}

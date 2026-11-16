// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package model_provenance

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// HostedAttempt is one completed hosted generation the Gateway verified: the
// Inference Operator that served it, the signed result, and the attempt span.
type HostedAttempt struct {
	InferenceOperatorID    string
	InferenceTransactionID string
	ProviderAttemptID      string
	ExpectedModelDigest    string
	StartedAtUnixMs        int64
	CompletedAtUnixMs      int64
	Result                 *operatorv1.InferenceResult
}

// HostedAttestationWindow derives the attestation window of a hosted attempt
// from the hosted egress attestation the Inference Operator signed into its
// result. A hosted provider holds the weights, so the window attests no
// weights: its status is HOSTED_PROVIDER_REPORTED, its observed digest is the
// hosted model identity digest, and it matches only when the provider served
// the requested model itself at the expected identity.
func HostedAttestationWindow(attempt HostedAttempt, attestedAtUnixMs int64) (*evalv1.ModelProvenanceAttestationWindow, error) {
	egress := attempt.Result.GetHostedEgress()
	if egress == nil || attempt.InferenceOperatorID == "" || attempt.InferenceTransactionID == "" || attempt.ProviderAttemptID == "" || attempt.Result.GetResultDigest() == "" {
		return nil, fmt.Errorf("model provenance: hosted attestation window: %w", constants.ErrMissingRequiredField)
	}
	window := &evalv1.ModelProvenanceAttestationWindow{
		SchemaVersion:              SchemaVersion,
		ProviderAttemptId:          attempt.ProviderAttemptID,
		ProvenanceOperatorId:       attempt.InferenceOperatorID,
		ServedModelTag:             egress.GetRequestedModel(),
		ExpectedModelDigest:        attempt.ExpectedModelDigest,
		ObservedModelDigest:        egress.GetModelIdentityDigest(),
		ManifestVerificationStatus: evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_HOSTED_PROVIDER_REPORTED,
		AttemptStartedAtUnixMs:     attempt.StartedAtUnixMs,
		AttemptCompletedAtUnixMs:   attempt.CompletedAtUnixMs,
		AttestedAtUnixMs:           attestedAtUnixMs,
		DigestMatch: attempt.ExpectedModelDigest != "" &&
			egress.GetModelIdentityDigest() == attempt.ExpectedModelDigest &&
			egress.GetModelVersion() == egress.GetRequestedModel(),
		Hosted: &evalv1.HostedModelAttestation{
			ProviderClass:          egress.GetProviderClass(),
			ProviderOrigin:         egress.GetProviderOrigin(),
			RequestedModel:         egress.GetRequestedModel(),
			ProviderModelVersion:   egress.GetProviderModelVersion(),
			ServedModel:            egress.GetModelVersion(),
			ProviderResponseId:     egress.GetProviderResponseId(),
			RawResponseDigest:      egress.GetRawResponseDigest(),
			InferenceTransactionId: attempt.InferenceTransactionID,
			ResultDigest:           attempt.Result.GetResultDigest(),
		},
	}
	digest, err := ComputeAttestationDigest(window)
	if err != nil {
		return nil, err
	}
	window.AttestationDigest = digest
	if err := ValidateAttestationWindow(window); err != nil {
		return nil, err
	}
	return window, nil
}

// validateHostedWindow checks that a hosted window carries its hosted model
// record and no storage attestation, and that only a hosted window carries one.
// The record verifies offline: its origin is its class's fixed API origin, its
// observed digest recomputes from the record, and its match flag follows from
// the record and the expected digest.
func validateHostedWindow(window *evalv1.ModelProvenanceAttestationWindow) error {
	hosted := window.GetHosted()
	isHosted := window.GetManifestVerificationStatus() == evalv1.ModelManifestVerificationStatus_MODEL_MANIFEST_VERIFICATION_STATUS_HOSTED_PROVIDER_REPORTED
	if !isHosted {
		if hosted != nil {
			return fmt.Errorf("model provenance: validate attestation window: hosted record on a storage window: %w", constants.ErrEvidenceArtifactMalformed)
		}
		return nil
	}
	if hosted == nil || len(window.GetWeightAttestations()) != 0 || window.GetManifestDigest() != "" ||
		!constants.IsHostedInferenceProvider(hosted.GetProviderClass()) || hosted.GetProviderOrigin() == "" ||
		hosted.GetRequestedModel() == "" || hosted.GetRequestedModel() != window.GetServedModelTag() ||
		hosted.GetProviderModelVersion() == "" || hosted.GetServedModel() == "" ||
		hosted.GetRawResponseDigest() == "" || hosted.GetInferenceTransactionId() == "" || hosted.GetResultDigest() == "" ||
		window.GetObservedModelDigest() == "" {
		return fmt.Errorf("model provenance: validate attestation window: hosted record: %w", constants.ErrEvidenceArtifactMalformed)
	}
	if origin, _ := constants.HostedInferenceProviderOrigin(hosted.GetProviderClass()); hosted.GetProviderOrigin() != origin {
		return fmt.Errorf("model provenance: validate attestation window: hosted provider origin: %w", constants.ErrEvidenceArtifactMalformed)
	}
	digest, err := models.ComputeHostedModelIdentityDigest(hosted.GetProviderClass(), hosted.GetProviderOrigin(), hosted.GetRequestedModel(), hosted.GetProviderModelVersion())
	if err != nil || digest != window.GetObservedModelDigest() {
		return fmt.Errorf("model provenance: validate attestation window: hosted model identity digest: %w", constants.ErrModelProvenanceDigestMismatch)
	}
	match := window.GetExpectedModelDigest() != "" && digest == window.GetExpectedModelDigest() && hosted.GetServedModel() == hosted.GetRequestedModel()
	if window.GetDigestMatch() != match {
		return fmt.Errorf("model provenance: validate attestation window: hosted digest match: %w", constants.ErrModelProvenanceDigestMismatch)
	}
	return nil
}

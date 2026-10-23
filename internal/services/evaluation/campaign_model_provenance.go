// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ModelProvenancePolicy controls whether missing provenance attestations are
// verification failures or interim unavailable telemetry.
type ModelProvenancePolicy int

const (
	// ModelProvenancePolicyInterim records missing attestations as unavailable
	// reasons without failing assignment verification.
	ModelProvenancePolicyInterim ModelProvenancePolicy = 0
	// ModelProvenancePolicyStrict requires complete model provenance coverage
	// for every scored inference.
	ModelProvenancePolicyStrict ModelProvenancePolicy = 1
)

// CampaignModelProvenanceReader loads model provenance attestation windows for
// campaign verification and export. It reads only the sealed copies in the run's
// evidence directory; the Gateway is never consulted here, and capture belongs to
// CampaignWitnessCapture.
type CampaignModelProvenanceReader struct {
	store *Store
}

// NewCampaignModelProvenanceReader constructs one model provenance accessor over
// the evaluation store that owns the run's sealed evidence.
func NewCampaignModelProvenanceReader(store *Store) (*CampaignModelProvenanceReader, error) {
	if store == nil || store.files == nil {
		return nil, fmt.Errorf("evaluation: model provenance reader: %w", constants.ErrMissingRequiredField)
	}
	return &CampaignModelProvenanceReader{store: store}, nil
}

func isModelProvenanceEvidenceNotFound(err error) bool {
	return isGatewayEvidenceNotFound(err)
}

// SaveModelProvenanceWindow seals one model provenance attestation window in the
// run's evidence directory and records its reference under the provider attempt.
// The window must pass attestation validation before it is sealed.
func (s *Store) SaveModelProvenanceWindow(ctx context.Context, runID string, window *evalv1.ModelProvenanceAttestationWindow) error {
	if window == nil {
		return missingField(constants.ErrEvaluationReportPersistFailed, "attestation window")
	}
	if err := s.validateAccess(constants.ErrEvaluationReportPersistFailed, runIDElement(runID), attemptIDElement(window.GetProviderAttemptId())); err != nil {
		return err
	}
	if err := model_provenance.ValidateAttestationWindow(window); err != nil {
		return fmt.Errorf("%w: model provenance attestation window: %w", constants.ErrEvaluationReportPersistFailed, err)
	}
	attemptID := window.GetProviderAttemptId()
	reference, err := s.SaveProtoArtifact(ctx, EvidenceScope{RunID: runID, AttemptID: attemptID}, complianceevidence.ArtifactTypeModelProvenanceAttestationWindow, window)
	if err != nil {
		return err
	}
	return s.writeEvidenceRef(ctx, s.layout.modelProvenanceWindowRefPath(runID, attemptID), reference)
}

// LoadModelProvenanceWindow returns the sealed model provenance attestation window
// recorded for one provider attempt of a run. A missing record reports
// constants.ErrNotFound.
func (s *Store) LoadModelProvenanceWindow(ctx context.Context, runID, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if err := s.validateAccess(constants.ErrEvidenceArtifactMalformed, runIDElement(runID), attemptIDElement(providerAttemptID)); err != nil {
		return nil, err
	}
	reference, err := s.readEvidenceRef(ctx, s.layout.modelProvenanceWindowRefPath(runID, providerAttemptID), runID, providerAttemptID)
	if err != nil {
		return nil, err
	}
	window := &evalv1.ModelProvenanceAttestationWindow{}
	if err := s.loadProtoArtifact(ctx, runID, reference, window); err != nil {
		return nil, err
	}
	if window.GetProviderAttemptId() != providerAttemptID {
		return nil, fmt.Errorf("%w: model provenance window binding does not match requested attempt", constants.ErrEvidenceScopeMismatch)
	}
	if err := model_provenance.ValidateAttestationWindow(window); err != nil {
		return nil, fmt.Errorf("%w: model provenance attestation window: %w", constants.ErrEvidenceArtifactMalformed, err)
	}
	return window, nil
}

// VerifyAssignmentModelProvenance independently checks model provenance
// attestation coverage for every scored inference in one assignment result.
func (r *CampaignModelProvenanceReader) VerifyAssignmentModelProvenance(
	ctx context.Context,
	result *evalv1.EvaluationAssignmentResult,
	policy ModelProvenancePolicy,
) ([]string, []string) {
	if r == nil || result == nil {
		return nil, nil
	}
	failures := make([]string, 0)
	unavailable := make([]string, 0)
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		expectedDigest := inferenceRecord.GetModelVariant().GetModelDigest()
		window, err := r.store.LoadModelProvenanceWindow(ctx, result.GetRunId(), attemptID)
		if err != nil {
			if isModelProvenanceEvidenceNotFound(err) {
				reason := fmt.Sprintf("model_provenance_missing:%s", attemptID)
				unavailable = append(unavailable, reason)
				if policy == ModelProvenancePolicyStrict {
					failures = append(failures, fmt.Sprintf("inference %s missing model provenance attestation window", inferenceRecord.GetInferenceRecordId()))
				}
				continue
			}
			failures = append(failures, fmt.Sprintf("inference %s model provenance window load failed: %v", inferenceRecord.GetInferenceRecordId(), err))
			continue
		}
		if err := VerifyModelProvenanceWindow(window, expectedDigest, policy); err != nil {
			failures = append(failures, fmt.Sprintf("inference %s model provenance verification failed: %v", inferenceRecord.GetInferenceRecordId(), err))
		}
	}
	return failures, unavailable
}

// VerifyModelProvenanceWindow validates one attestation window against the
// expected campaign model digest when strict policy is enabled.
func VerifyModelProvenanceWindow(window *evalv1.ModelProvenanceAttestationWindow, expectedModelDigest string, policy ModelProvenancePolicy) error {
	if window == nil {
		if policy == ModelProvenancePolicyStrict {
			return fmt.Errorf("evaluation: model provenance: %w", constants.ErrEvaluationObservationUnavailable)
		}
		return nil
	}
	if err := model_provenance.ValidateAttestationWindow(window); err != nil {
		return err
	}
	if expectedModelDigest != "" && window.GetExpectedModelDigest() != expectedModelDigest {
		return fmt.Errorf("evaluation: model provenance: expected digest: %w", constants.ErrModelProvenanceDigestMismatch)
	}
	if policy == ModelProvenancePolicyStrict && !window.GetDigestMatch() {
		return fmt.Errorf("evaluation: model provenance: %w", constants.ErrModelProvenanceDigestMismatch)
	}
	return nil
}

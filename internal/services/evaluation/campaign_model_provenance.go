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
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/inference/model_provenance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// ModelProvenanceRemote loads model provenance attestation evidence from a
// remote gateway when local runtime files are unavailable.
type ModelProvenanceRemote interface {
	Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error)
	// WatchAttestations delivers the provider attempt ID of every window the
	// Gateway records. connected runs on each (re)connect of the event stream;
	// recordings before it were not delivered, so the consumer reconciles there.
	// It returns nil when ctx ends and an error when the stream cannot continue.
	WatchAttestations(ctx context.Context, connected func(), recorded func(providerAttemptID string)) error
}

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

// ModelProvenanceReader loads model provenance attestation windows.
type ModelProvenanceReader interface {
	Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error)
}

type localModelProvenanceReader struct {
	store model_provenance.WindowStore
}

func (r *localModelProvenanceReader) Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	if r == nil || r.store == nil || providerAttemptID == "" {
		return nil, fmt.Errorf("evaluation: load model provenance: %w", constants.ErrMissingRequiredField)
	}
	return r.store.Load(ctx, providerAttemptID)
}

// VerifyModelProvenanceWindow validates one attestation window against the
// expected campaign model digest when strict policy is enabled.
// CampaignModelProvenanceReader loads model provenance attestation windows
// for campaign verification.
type CampaignModelProvenanceReader struct {
	windows ModelProvenanceReader
	local   model_provenance.WindowStore
	remote  ModelProvenanceRemote
}

// NewCampaignModelProvenanceReader constructs one read-only model provenance
// accessor from runtime file services.
func NewCampaignModelProvenanceReader(fileSvc fs.RuntimeFileService) (*CampaignModelProvenanceReader, error) {
	return NewCampaignModelProvenanceReaderWithRemote(fileSvc, nil)
}

// NewCampaignModelProvenanceReaderWithRemote constructs one read-only model
// provenance accessor from local runtime files with optional gateway fallback
// when local evidence is missing.
func NewCampaignModelProvenanceReaderWithRemote(fileSvc fs.RuntimeFileService, remote ModelProvenanceRemote) (*CampaignModelProvenanceReader, error) {
	if fileSvc == nil {
		return nil, fmt.Errorf("evaluation: model provenance reader: %w", constants.ErrMissingRequiredField)
	}
	local, err := model_provenance.NewWindowStore(fileSvc)
	if err != nil {
		return nil, err
	}
	localReader := &localModelProvenanceReader{store: local}
	var windows ModelProvenanceReader = localReader
	if remote != nil {
		windows = &fallbackModelProvenanceReader{local: localReader, remote: remote}
	}
	return &CampaignModelProvenanceReader{windows: windows, local: local, remote: remote}, nil
}

type fallbackModelProvenanceReader struct {
	local  ModelProvenanceReader
	remote ModelProvenanceRemote
}

func (s *fallbackModelProvenanceReader) Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	window, err := s.local.Load(ctx, providerAttemptID)
	if err == nil || !isModelProvenanceEvidenceNotFound(err) || s.remote == nil {
		return window, err
	}
	return s.remote.Load(ctx, providerAttemptID)
}

func (r *CampaignModelProvenanceReader) CaptureAssignmentEvidence(ctx context.Context, result *evalv1.EvaluationAssignmentResult) error {
	if r == nil || result == nil || r.remote == nil {
		return nil
	}
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		_, err := r.local.Load(ctx, attemptID)
		if err == nil {
			continue
		}
		if !isModelProvenanceEvidenceNotFound(err) {
			return fmt.Errorf("evaluation: capture model provenance: %w", err)
		}
		window, err := r.remote.Load(ctx, attemptID)
		if err != nil {
			if isModelProvenanceEvidenceNotFound(err) {
				continue
			}
			return fmt.Errorf("evaluation: capture model provenance: %w", err)
		}
		if err := r.local.Save(ctx, window); err != nil {
			return fmt.Errorf("evaluation: persist model provenance: %w", err)
		}
	}
	return nil
}

func isModelProvenanceEvidenceNotFound(err error) bool {
	return isGatewayEvidenceNotFound(err)
}

// WaitForEvidence captures durable completion windows before strict verification.
// It is driven by the Gateway's attestation-recorded events: each (re)connect
// of the stream reconciles every pending attempt, and each event captures its
// attempt. Nothing is re-read on a timer. Malformed evidence and read failures
// return immediately. The caller owns the deadline for the whole run.
func (r *CampaignModelProvenanceReader) WaitForEvidence(ctx context.Context, attemptIDs []string) error {
	if r == nil {
		return fmt.Errorf("evaluation: wait for model provenance: %w", constants.ErrMissingRequiredField)
	}
	pending := make(map[string]struct{}, len(attemptIDs))
	for _, id := range attemptIDs {
		if id == "" {
			continue
		}
		_, err := r.local.Load(ctx, id)
		if err == nil {
			continue
		}
		if !isModelProvenanceEvidenceNotFound(err) {
			return fmt.Errorf("evaluation: wait for model provenance %s: %w", id, err)
		}
		pending[id] = struct{}{}
	}
	if len(pending) == 0 {
		return nil
	}
	if r.remote == nil {
		return missingProvenanceError(constants.ErrEvaluationObservationUnavailable, pending)
	}

	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	connected := make(chan struct{}, 1)
	recorded := make(chan string)
	watchDone := make(chan error, 1)
	go func() {
		watchDone <- r.remote.WatchAttestations(watchCtx, func() {
			select {
			case connected <- struct{}{}:
			default:
			}
		}, func(id string) {
			select {
			case recorded <- id:
			case <-watchCtx.Done():
			}
		})
	}()
	for len(pending) > 0 {
		select {
		case <-ctx.Done():
			return missingProvenanceError(ctx.Err(), pending)
		case err := <-watchDone:
			if err == nil {
				err = constants.ErrSSEStreamClosed
			}
			return missingProvenanceError(err, pending)
		case <-connected:
			for id := range pending {
				if err := r.captureRemoteEvidence(ctx, pending, id); err != nil {
					return err
				}
			}
		case id := <-recorded:
			if _, ok := pending[id]; ok {
				if err := r.captureRemoteEvidence(ctx, pending, id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// captureRemoteEvidence persists the Gateway window for one pending attempt
// and removes it from pending. A window the Gateway has not recorded yet stays
// pending for its recording event.
func (r *CampaignModelProvenanceReader) captureRemoteEvidence(ctx context.Context, pending map[string]struct{}, id string) error {
	window, err := r.remote.Load(ctx, id)
	if err != nil {
		if ctx.Err() != nil {
			return missingProvenanceError(ctx.Err(), pending)
		}
		if isModelProvenanceEvidenceNotFound(err) {
			return nil
		}
		return fmt.Errorf("evaluation: wait for model provenance %s: %w", id, err)
	}
	if err := r.local.Save(ctx, window); err != nil {
		return fmt.Errorf("evaluation: capture model provenance %s: %w", id, err)
	}
	delete(pending, id)
	return nil
}

func missingProvenanceError(cause error, pending map[string]struct{}) error {
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	count := len(ids)
	if len(ids) > 5 {
		ids = ids[:5]
	}
	return fmt.Errorf("evaluation: wait for model provenance: %w: %d missing attestation windows (sample attempts: %v); check Provenance Operator logs and storage throughput", cause, count, ids)
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
		window, err := r.windows.Load(ctx, attemptID)
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

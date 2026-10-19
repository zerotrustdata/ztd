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
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// ProviderObservationRemote loads provider-boundary observation evidence from the
// Gateway. Only CampaignWitnessCapture consults it, to seal what it fetches into a
// run's evidence; every read is served from that sealed copy.
type ProviderObservationRemote interface {
	Load(ctx context.Context, providerAttemptID string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error)
	// WatchRecorded delivers the provider attempt ID of every observation window
	// the Gateway records; see awaitEvidenceWindows.
	WatchRecorded(ctx context.Context, connected func(), recorded func(providerAttemptID string)) error
}

// ModelProvenanceRemote loads model provenance attestation windows from the
// Gateway. Only CampaignWitnessCapture consults it, to seal what it fetches into a
// run's evidence; every read is served from that sealed copy.
type ModelProvenanceRemote interface {
	Load(ctx context.Context, providerAttemptID string) (*evalv1.ModelProvenanceAttestationWindow, error)
	// WatchRecorded delivers the provider attempt ID of every attestation window
	// the Gateway records; see awaitEvidenceWindows.
	WatchRecorded(ctx context.Context, connected func(), recorded func(providerAttemptID string)) error
}

// CampaignWitnessCapture is the only owner of Gateway capture for campaign
// evidence. It seals the provider-boundary observation windows and model
// provenance attestation windows that a run's scored inferences name into the
// run's evidence directory. Verification and export read only those sealed
// copies, so a window the Gateway has not produced is a verification-policy
// decision made later from what was sealed, never a read-through.
type CampaignWitnessCapture struct {
	store      *Store
	providers  ProviderObservationRemote
	provenance ModelProvenanceRemote
}

// NewCampaignWitnessCapture constructs the capture over the run store and both
// Gateway remotes. Every argument is required.
func NewCampaignWitnessCapture(store *Store, providers ProviderObservationRemote, provenance ModelProvenanceRemote) (*CampaignWitnessCapture, error) {
	if store == nil || store.files == nil || providers == nil || provenance == nil {
		return nil, fmt.Errorf("evaluation: campaign witness capture: %w", constants.ErrMissingRequiredField)
	}
	return &CampaignWitnessCapture{store: store, providers: providers, provenance: provenance}, nil
}

// CaptureRunProviderObservationEvidence seals the Gateway's provider observation
// window and provider-attempt record for every scored inference of one run's
// persisted assignment results that does not yet hold both.
func (c *CampaignWitnessCapture) CaptureRunProviderObservationEvidence(ctx context.Context, runID string) error {
	if c == nil || runID == "" {
		return fmt.Errorf("evaluation: capture provider observation evidence: %w", constants.ErrMissingRequiredField)
	}
	results, err := c.persistedResults(ctx, runID)
	if err != nil {
		return fmt.Errorf("evaluation: capture provider observation evidence: %w", err)
	}
	for _, result := range results {
		if err := c.captureAssignmentProviderEvidence(ctx, result); err != nil {
			return err
		}
	}
	return nil
}

// CaptureRunModelProvenanceEvidence seals the Gateway's model provenance
// attestation window for every scored inference of one run's persisted assignment
// results that the run does not yet hold.
func (c *CampaignWitnessCapture) CaptureRunModelProvenanceEvidence(ctx context.Context, runID string) error {
	if c == nil || runID == "" {
		return fmt.Errorf("evaluation: capture model provenance evidence: %w", constants.ErrMissingRequiredField)
	}
	results, err := c.persistedResults(ctx, runID)
	if err != nil {
		return fmt.Errorf("evaluation: capture model provenance evidence: %w", err)
	}
	for _, result := range results {
		if err := c.captureAssignmentModelProvenance(ctx, result); err != nil {
			return err
		}
	}
	return nil
}

// WaitForRunModelProvenanceEvidence waits until every scored attempt of one run's
// persisted results holds a sealed model provenance window, driven by the
// Gateway's attestation-recorded events. It waits for completed scored attempts
// only: it neither reruns inference nor manufactures missing witness evidence. The
// caller owns the deadline.
func (c *CampaignWitnessCapture) WaitForRunModelProvenanceEvidence(ctx context.Context, runID string) error {
	if c == nil || runID == "" {
		return fmt.Errorf("evaluation: wait for run model provenance: %w", constants.ErrMissingRequiredField)
	}
	results, err := c.persistedResults(ctx, runID)
	if err != nil {
		return fmt.Errorf("evaluation: wait for run model provenance: %w", err)
	}
	var attemptIDs []string
	for _, result := range results {
		for _, record := range scoredModelInferences(result) {
			attemptIDs = append(attemptIDs, record.GetProviderAttemptId())
		}
	}
	return c.waitForModelProvenanceEvidence(ctx, runID, attemptIDs)
}

// persistedResults loads the persisted result of every assignment in one run that
// has one, keyed by assignment ID.
func (c *CampaignWitnessCapture) persistedResults(ctx context.Context, runID string) (map[string]*evalv1.EvaluationAssignmentResult, error) {
	assignments, err := c.store.ListAssignments(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list assignments: %w", err)
	}
	return loadCampaignAssignmentResults(ctx, c.store, runID, assignments)
}

// captureAssignmentProviderEvidence seals the provider observation window and
// attempt record for every scored inference of one result that does not yet hold
// both.
func (c *CampaignWitnessCapture) captureAssignmentProviderEvidence(ctx context.Context, result *evalv1.EvaluationAssignmentResult) error {
	runID := result.GetRunId()
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		_, windowErr := c.store.LoadProviderObservationWindow(ctx, runID, attemptID)
		if windowErr != nil && !isProviderEvidenceNotFound(windowErr) {
			return fmt.Errorf("evaluation: capture provider observation window: %w", windowErr)
		}
		_, attemptErr := c.store.LoadProviderAttemptRecord(ctx, runID, attemptID)
		if attemptErr != nil && !isProviderEvidenceNotFound(attemptErr) {
			return fmt.Errorf("evaluation: capture provider attempt: %w", attemptErr)
		}
		if windowErr == nil && attemptErr == nil {
			continue
		}
		window, attempt, err := c.providers.Load(ctx, attemptID)
		if err != nil {
			if isProviderEvidenceNotFound(err) {
				continue
			}
			return fmt.Errorf("evaluation: capture provider evidence: %w", err)
		}
		if attemptErr != nil {
			if err := c.store.SaveProviderAttemptRecord(ctx, runID, attempt); err != nil {
				return fmt.Errorf("evaluation: persist provider attempt: %w", err)
			}
		}
		if windowErr != nil {
			if err := c.store.SaveProviderObservationWindow(ctx, runID, window); err != nil {
				return fmt.Errorf("evaluation: persist provider observation window: %w", err)
			}
		}
	}
	return nil
}

// captureAssignmentModelProvenance seals the attestation window for every scored
// inference of one result that the run does not yet hold.
func (c *CampaignWitnessCapture) captureAssignmentModelProvenance(ctx context.Context, result *evalv1.EvaluationAssignmentResult) error {
	runID := result.GetRunId()
	for _, inferenceRecord := range scoredModelInferences(result) {
		attemptID := inferenceRecord.GetProviderAttemptId()
		if attemptID == "" {
			continue
		}
		_, err := c.store.LoadModelProvenanceWindow(ctx, runID, attemptID)
		if err == nil {
			continue
		}
		if !isModelProvenanceEvidenceNotFound(err) {
			return fmt.Errorf("evaluation: capture model provenance: %w", err)
		}
		if _, err = c.captureModelProvenanceWindow(ctx, runID, attemptID); err != nil {
			return err
		}
	}
	return nil
}

// captureModelProvenanceWindow seals the Gateway window for one attempt. It reports
// false, without error, while the Gateway has not recorded the window yet.
func (c *CampaignWitnessCapture) captureModelProvenanceWindow(ctx context.Context, runID, providerAttemptID string) (bool, error) {
	window, err := c.provenance.Load(ctx, providerAttemptID)
	if err != nil {
		if isModelProvenanceEvidenceNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("evaluation: capture model provenance %s: %w", providerAttemptID, err)
	}
	if err := c.store.SaveModelProvenanceWindow(ctx, runID, window); err != nil {
		return false, fmt.Errorf("evaluation: capture model provenance %s: %w", providerAttemptID, err)
	}
	return true, nil
}

// waitForModelProvenanceEvidence seals the Gateway's attestation window for every
// attempt the run does not yet hold, driven by the Gateway's attestation-recorded
// events (see awaitEvidenceWindows). Malformed evidence and read failures return
// immediately.
func (c *CampaignWitnessCapture) waitForModelProvenanceEvidence(ctx context.Context, runID string, attemptIDs []string) error {
	pending := make(map[string]struct{}, len(attemptIDs))
	for _, id := range attemptIDs {
		if id == "" {
			continue
		}
		_, err := c.store.LoadModelProvenanceWindow(ctx, runID, id)
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
	missing := func(cause error) error {
		return missingEvidenceWindowsError("model provenance attestation", "check Provenance Operator logs and storage throughput", cause, pending)
	}
	sealWindow := func(ctx context.Context, id string) (bool, error) {
		return c.captureModelProvenanceWindow(ctx, runID, id)
	}
	return awaitEvidenceWindows(ctx, pending, c.provenance.WatchRecorded, sealWindow, missing)
}

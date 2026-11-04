// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

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

// CaptureAssignmentEvidence seals the Gateway windows that one completed
// assignment's scored inferences name. The run executor calls it as the
// assignment's result is recorded, so it reads only the windows the Gateway has
// recorded by then; a window still pending is sealed by the run-level capture at
// run completion, or waited for once there by WaitForRunModelProvenanceEvidence
// and WaitForRunProviderObservationEvidence.
func (c *CampaignWitnessCapture) CaptureAssignmentEvidence(ctx context.Context, result *evalv1.EvaluationAssignmentResult) error {
	if c == nil || result == nil {
		return fmt.Errorf("evaluation: capture assignment evidence: %w", constants.ErrMissingRequiredField)
	}
	if err := c.captureAssignmentProviderEvidence(ctx, result); err != nil {
		return err
	}
	return c.captureAssignmentModelProvenance(ctx, result)
}

// WaitForRunModelProvenanceEvidence waits until every scored attempt of one run's
// persisted assignment results holds a sealed model provenance window, driven by
// the Gateway's attestation-recorded events. The run executor calls it once, at
// run completion. The caller owns the deadline.
func (c *CampaignWitnessCapture) WaitForRunModelProvenanceEvidence(ctx context.Context, runID string) error {
	if c == nil || runID == "" {
		return fmt.Errorf("evaluation: wait for run model provenance evidence: %w", constants.ErrMissingRequiredField)
	}
	attempts, err := c.runScoredAttempts(ctx, runID)
	if err != nil {
		return fmt.Errorf("evaluation: wait for run model provenance evidence: %w", err)
	}
	return c.waitForModelProvenanceEvidence(ctx, runID, attempts)
}

// WaitForRunProviderObservationEvidence waits until every scored attempt of one
// run's persisted assignment results holds a sealed provider observation window
// and attempt record, driven by the Gateway's observation-recorded events. The run
// executor calls it once, at run completion. The caller owns the deadline.
func (c *CampaignWitnessCapture) WaitForRunProviderObservationEvidence(ctx context.Context, runID string) error {
	if c == nil || runID == "" {
		return fmt.Errorf("evaluation: wait for run provider observation evidence: %w", constants.ErrMissingRequiredField)
	}
	attempts, err := c.runScoredAttempts(ctx, runID)
	if err != nil {
		return fmt.Errorf("evaluation: wait for run provider observation evidence: %w", err)
	}
	return c.waitForProviderObservationEvidence(ctx, runID, attempts)
}

// runScoredAttempts maps the provider attempt ID of every scored inference in one
// run's persisted assignment results that reached its provider to the assignment
// that holds it.
func (c *CampaignWitnessCapture) runScoredAttempts(ctx context.Context, runID string) (map[string]string, error) {
	results, err := c.persistedResults(ctx, runID)
	if err != nil {
		return nil, err
	}
	attempts := make(map[string]string)
	for assignmentID, result := range results {
		for _, record := range scoredModelInferences(result) {
			// A governance-rejected inference has no provider attempt.
			if attemptID := record.GetProviderAttemptId(); attemptID != "" {
				attempts[attemptID] = assignmentID
			}
		}
	}
	return attempts, nil
}

// failedObservationAttempts returns the pending attempts whose sealed trace holds
// a FAILED receipt for action, each with that receipt's summary. Only the
// assignments holding a pending attempt are read. A window never follows such a
// receipt, because the observer publishes a completion only when its handler
// succeeds. This decides only how long the run waits; verification still fails
// closed on the window the attempt does not hold.
func (c *CampaignWitnessCapture) failedObservationAttempts(ctx context.Context, runID string, action constants.ActionType, attempts map[string]string, pending map[string]struct{}) (map[string]string, error) {
	assignments := make(map[string]struct{})
	for id := range pending {
		assignments[attempts[id]] = struct{}{}
	}
	failed := make(map[string]string)
	for assignmentID := range assignments {
		trace, err := c.store.LoadAssignmentAgentTrace(ctx, runID, assignmentID)
		if err != nil {
			if errors.Is(err, constants.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("evaluation: read failed observations: %w", err)
		}
		for _, call := range trace.ModelCalls {
			if _, isPending := pending[call.ProviderAttemptID]; !isPending {
				continue
			}
			for _, receipt := range call.ObservationReceipts {
				if constants.ActionType(receipt.GetActionType()) == action && receipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
					failed[call.ProviderAttemptID] = receipt.GetResultSummary()
				}
			}
		}
	}
	return failed, nil
}

// awaitRunEvidence waits for the pending windows of one evidence kind that can
// still arrive (see awaitEvidenceWindows) and then reports the attempts whose
// observer signed a failure, wrapping constants.ErrEvaluationObservationFailed.
func (c *CampaignWitnessCapture) awaitRunEvidence(ctx context.Context, runID string, action constants.ActionType, kind, hint string, attempts map[string]string, pending map[string]struct{}, watch evidenceWindowWatch, capture evidenceWindowCapture) error {
	if len(pending) == 0 {
		return nil
	}
	failed, err := c.failedObservationAttempts(ctx, runID, action, attempts, pending)
	if err != nil {
		return err
	}
	for id := range failed {
		delete(pending, id)
	}
	missing := func(cause error) error {
		return missingEvidenceWindowsError(kind, hint, cause, pending)
	}
	if err := awaitEvidenceWindows(ctx, pending, watch, capture, missing); err != nil {
		return err
	}
	if len(failed) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(failed))
	cause := fmt.Errorf("%w: %s", constants.ErrEvaluationObservationFailed, failed[ids[0]])
	return missingEvidenceWindowsError(kind, hint, cause, failed)
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
		if _, err := c.captureProviderEvidence(ctx, runID, attemptID); err != nil {
			return err
		}
	}
	return nil
}

// providerEvidenceHeld reports which of the two provider evidence records the run
// already holds for one attempt: the observation window and the attempt record.
func (c *CampaignWitnessCapture) providerEvidenceHeld(ctx context.Context, runID, providerAttemptID string) (windowHeld, attemptHeld bool, err error) {
	_, windowErr := c.store.LoadProviderObservationWindow(ctx, runID, providerAttemptID)
	if windowErr != nil && !isProviderEvidenceNotFound(windowErr) {
		return false, false, fmt.Errorf("evaluation: read provider observation window: %w", windowErr)
	}
	_, attemptErr := c.store.LoadProviderAttemptRecord(ctx, runID, providerAttemptID)
	if attemptErr != nil && !isProviderEvidenceNotFound(attemptErr) {
		return false, false, fmt.Errorf("evaluation: read provider attempt: %w", attemptErr)
	}
	return windowErr == nil, attemptErr == nil, nil
}

// captureProviderEvidence seals the Gateway's observation window and attempt record
// for one provider attempt, saving only the records the run does not yet hold. It
// reports true once the run holds both, and false, without error, while the Gateway
// has not recorded them yet.
func (c *CampaignWitnessCapture) captureProviderEvidence(ctx context.Context, runID, providerAttemptID string) (bool, error) {
	windowHeld, attemptHeld, err := c.providerEvidenceHeld(ctx, runID, providerAttemptID)
	if err != nil {
		return false, err
	}
	if windowHeld && attemptHeld {
		return true, nil
	}
	window, attempt, err := c.providers.Load(ctx, providerAttemptID)
	if err != nil {
		if isProviderEvidenceNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("evaluation: capture provider evidence: %w", err)
	}
	if !attemptHeld {
		if err := c.store.SaveProviderAttemptRecord(ctx, runID, attempt); err != nil {
			return false, fmt.Errorf("evaluation: persist provider attempt: %w", err)
		}
	}
	if !windowHeld {
		if err := c.store.SaveProviderObservationWindow(ctx, runID, window); err != nil {
			return false, fmt.Errorf("evaluation: persist provider observation window: %w", err)
		}
	}
	return true, nil
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
// events (see awaitRunEvidence). Malformed evidence and read failures return
// immediately.
func (c *CampaignWitnessCapture) waitForModelProvenanceEvidence(ctx context.Context, runID string, attempts map[string]string) error {
	pending := make(map[string]struct{}, len(attempts))
	for id := range attempts {
		_, err := c.store.LoadModelProvenanceWindow(ctx, runID, id)
		if err == nil {
			continue
		}
		if !isModelProvenanceEvidenceNotFound(err) {
			return fmt.Errorf("evaluation: wait for model provenance %s: %w", id, err)
		}
		pending[id] = struct{}{}
	}
	sealWindow := func(ctx context.Context, id string) (bool, error) {
		return c.captureModelProvenanceWindow(ctx, runID, id)
	}
	return c.awaitRunEvidence(ctx, runID, constants.ActionTypeModelProvenanceObservation, "model provenance attestation", "check Provenance Operator logs and storage throughput", attempts, pending, c.provenance.WatchRecorded, sealWindow)
}

// waitForProviderObservationEvidence seals the Gateway's observation window and
// attempt record for every attempt the run does not yet hold, driven by the
// Gateway's observation-recorded events (see awaitRunEvidence). Read failures
// return immediately.
func (c *CampaignWitnessCapture) waitForProviderObservationEvidence(ctx context.Context, runID string, attempts map[string]string) error {
	pending := make(map[string]struct{}, len(attempts))
	for id := range attempts {
		windowHeld, attemptHeld, err := c.providerEvidenceHeld(ctx, runID, id)
		if err != nil {
			return fmt.Errorf("evaluation: wait for provider observation %s: %w", id, err)
		}
		if !windowHeld || !attemptHeld {
			pending[id] = struct{}{}
		}
	}
	sealWindow := func(ctx context.Context, id string) (bool, error) {
		return c.captureProviderEvidence(ctx, runID, id)
	}
	return c.awaitRunEvidence(ctx, runID, constants.ActionTypeProviderBoundaryObservation, "provider observation", "check Observer logs and storage throughput", attempts, pending, c.providers.WatchRecorded, sealWindow)
}

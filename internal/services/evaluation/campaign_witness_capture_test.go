// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// silentWitnessRemote is a Gateway that has recorded nothing and never will: every
// read is not-found and the stream connects, then stays quiet until ctx ends.
type silentWitnessRemote struct {
	watches int
}

func (r *silentWitnessRemote) watch(ctx context.Context, connected func(), _ func(string)) error {
	r.watches++
	connected()
	<-ctx.Done()
	return nil
}

type silentProvenanceRemote struct{ silentWitnessRemote }

func (r *silentProvenanceRemote) Load(context.Context, string) (*evalv1.ModelProvenanceAttestationWindow, error) {
	return nil, constants.ErrNotFound
}

func (r *silentProvenanceRemote) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	return r.watch(ctx, connected, recorded)
}

type silentProviderRemote struct{ silentWitnessRemote }

func (r *silentProviderRemote) Load(context.Context, string) (*evalv1.ProviderBoundaryObservationWindow, *operatorv1.InferenceProviderAttemptRecord, error) {
	return nil, nil, constants.ErrNotFound
}

func (r *silentProviderRemote) WatchRecorded(ctx context.Context, connected func(), recorded func(string)) error {
	return r.watch(ctx, connected, recorded)
}

// tracedAttempt is one provider attempt and the observation receipts its model
// call carries.
type tracedAttempt struct {
	id       string
	receipts []*operatorv1.EventReceipt
}

// saveRunAttemptTraces persists one assignment result naming the attempts and the
// sealed trace whose model calls carry their receipts.
func saveRunAttemptTraces(t *testing.T, store *Store, runID string, attempts ...tracedAttempt) {
	t.Helper()
	ctx := context.Background()
	const assignmentID = "assignment-1"
	require.NoError(t, store.SaveAssignment(ctx, &evalv1.EvaluationAssignment{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: assignmentID, RunId: runID, CampaignId: "campaign-1",
		ScenarioId: "scenario-1", Repetition: 1, Lane: evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
		Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
			DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
		}},
	}))
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: assignmentID, RunId: runID, CampaignId: "campaign-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
		CompletedAt:     timestamppb.Now(),
	}
	calls := make([]AgentModelCall, 0, len(attempts))
	for _, attempt := range attempts {
		result.ModelInferences = append(result.ModelInferences, &evalv1.ModelInferenceRecord{ProviderAttemptId: attempt.id})
		call := AgentModelCall{ProviderAttemptID: attempt.id}
		for _, receipt := range attempt.receipts {
			call.ObservationReceipts = append(call.ObservationReceipts, AgentTraceReceipt{EventReceipt: receipt})
		}
		calls = append(calls, call)
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	require.NoError(t, store.SaveAssignmentResult(ctx, result))

	body, err := SealAgentTrace(&AgentTrace{
		RunID: runID, AssignmentID: assignmentID, EvaluationAttemptID: "attempt-1", DesignatedRole: "primary",
		Input: testAgentTraceInput(), FinalOutput: "done", ModelCalls: calls,
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, runID, assignmentID, body))
}

func observationReceipt(action constants.ActionType, status operatorv1.ExecutionStatus, summary string) *operatorv1.EventReceipt {
	return &operatorv1.EventReceipt{ActionType: string(action), Status: status, ResultSummary: summary}
}

// A window never arrives for an attempt whose observer signed a FAILED receipt:
// handler failure publishes no completion, so no recorded event can follow. The
// wait reports that attempt at once instead of holding the run to its deadline,
// and reports it as unavailable evidence so strict verification still fails on
// the missing window. Each kind is excused only by its own observer's receipt.
func TestWaitForRunEvidence_SignedObserverFailure(t *testing.T) {
	const summary = "model provenance tracker: finalize: digest mismatch"
	failed, completed := operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	provenance, provider := constants.ActionTypeModelProvenanceObservation, constants.ActionTypeProviderBoundaryObservation

	type waitFn func(*CampaignWitnessCapture, context.Context, string) error
	waitProvenance := waitFn((*CampaignWitnessCapture).WaitForRunModelProvenanceEvidence)
	waitProvider := waitFn((*CampaignWitnessCapture).WaitForRunProviderObservationEvidence)

	tests := []struct {
		name         string
		wait         waitFn
		receipts     []*operatorv1.EventReceipt
		wantsPrompt  bool
		wantsWatches int
	}{
		{
			name:         "failed provenance finalize ends the provenance wait",
			wait:         waitProvenance,
			receipts:     []*operatorv1.EventReceipt{observationReceipt(provenance, completed, ""), observationReceipt(provenance, failed, summary)},
			wantsPrompt:  true,
			wantsWatches: 0,
		},
		{
			name:         "failed observer finalize ends the provider wait",
			wait:         waitProvider,
			receipts:     []*operatorv1.EventReceipt{observationReceipt(provider, failed, summary)},
			wantsPrompt:  true,
			wantsWatches: 0,
		},
		{
			name:         "completed receipts keep the attempt pending",
			wait:         waitProvenance,
			receipts:     []*operatorv1.EventReceipt{observationReceipt(provenance, completed, ""), observationReceipt(provenance, completed, "")},
			wantsWatches: 1,
		},
		{
			name:         "another observer's failure keeps the attempt pending",
			wait:         waitProvenance,
			receipts:     []*operatorv1.EventReceipt{observationReceipt(provider, failed, summary)},
			wantsWatches: 1,
		},
		{
			name:         "no observation receipts keep the attempt pending",
			wait:         waitProvenance,
			wantsWatches: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(newCampaignRuntimeFileService(t))
			saveRunAttemptTraces(t, store, "run-1", tracedAttempt{id: "attempt-a", receipts: tt.receipts})
			provenanceRemote, providerRemote := &silentProvenanceRemote{}, &silentProviderRemote{}
			capture, err := NewCampaignWitnessCapture(store, providerRemote, provenanceRemote)
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			err = tt.wait(capture, ctx, "run-1")

			require.ErrorIs(t, err, constants.ErrEvaluationObservationUnavailable)
			require.ErrorContains(t, err, "attempt-a")
			assert.Equal(t, tt.wantsWatches, provenanceRemote.watches+providerRemote.watches)
			if tt.wantsPrompt {
				require.ErrorIs(t, err, constants.ErrEvaluationObservationFailed)
				require.ErrorContains(t, err, summary)
				assert.NotErrorIs(t, err, context.DeadlineExceeded, "the wait must not run to its deadline")
				return
			}
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.NotErrorIs(t, err, constants.ErrEvaluationObservationFailed)
		})
	}
}

// A failed attempt does not end the wait for the run's other attempts: they stay
// pending, and the failure is reported once the wait ends.
func TestWaitForRunEvidence_SignedObserverFailureKeepsOtherAttemptsPending(t *testing.T) {
	store := NewStore(newCampaignRuntimeFileService(t))
	saveRunAttemptTraces(t, store, "run-1",
		tracedAttempt{id: "attempt-failed", receipts: []*operatorv1.EventReceipt{
			observationReceipt(constants.ActionTypeModelProvenanceObservation, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, "mismatch"),
		}},
		tracedAttempt{id: "attempt-pending"},
	)
	provenanceRemote := &silentProvenanceRemote{}
	capture, err := NewCampaignWitnessCapture(store, &silentProviderRemote{}, provenanceRemote)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err = capture.WaitForRunModelProvenanceEvidence(ctx, "run-1")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "attempt-pending")
	assert.Equal(t, 1, provenanceRemote.watches)
}

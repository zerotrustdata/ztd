// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordingCampaignFeedExporter struct {
	records []CampaignPublicFeedRecord
}

func (r *recordingCampaignFeedExporter) HighWaterSequence(context.Context) (int64, error) {
	return int64(len(r.records)), nil
}

func (r *recordingCampaignFeedExporter) ExportBatch(_ context.Context, records []CampaignPublicFeedRecord) error {
	r.records = append(r.records, records...)
	return nil
}

func TestCampaignPublicationCoordinatorPublishAssignmentResultUsesSealedObservationWindows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	req := testCampaignInitRequest(t)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	exporter := &recordingCampaignFeedExporter{}
	attempt := &operatorv1.InferenceProviderAttemptRecord{
		ProviderAttemptId: "attempt-remote",
		Status:            operatorv1.InferenceProviderAttemptStatus_INFERENCE_PROVIDER_ATTEMPT_STATUS_COMPLETED,
		StartedAtUnixMs:   time.Unix(1_700_000_000, 0).UnixMilli(),
		CompletedAtUnixMs: time.Unix(1_700_000_010, 0).UnixMilli(),
	}
	window := &evalv1.ProviderBoundaryObservationWindow{
		SchemaVersion:              provider_observer.SchemaVersion,
		ProviderAttemptId:          "attempt-remote",
		ObserverId:                 "observer-test",
		ObserverClockSource:        provider_observer.DefaultObserverClockSource,
		WindowStartedAtUnixNanos:   uint64(time.Unix(1_700_000_000, 0).UnixNano()),
		WindowCompletedAtUnixNanos: uint64(time.Unix(1_700_000_010, 0).UnixNano()),
		AttemptStartedAtUnixMs:     attempt.GetStartedAtUnixMs(),
		AttemptCompletedAtUnixMs:   attempt.GetCompletedAtUnixMs(),
		Samples: []*evalv1.ProviderBoundaryHardwareSample{{
			ObservedAtUnixNanos:        uint64(time.Unix(1_700_000_001, 0).UnixNano()),
			VramBytesAvailability:      evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			VramUsedBytes:              16_000_000_000,
			GpuUtilizationAvailability: evalv1.ProviderHardwareMetricAvailability_PROVIDER_HARDWARE_METRIC_AVAILABILITY_REPORTED,
			GpuUtilizationPercent:      42,
		}},
	}
	digest, err := provider_observer.ComputeObservationDigest(window)
	require.NoError(t, err)
	window.ObservationDigest = digest
	require.NoError(t, store.SaveProviderAttemptRecord(ctx, run.GetRunId(), attempt))
	require.NoError(t, store.SaveProviderObservationWindow(ctx, run.GetRunId(), window))

	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    "assign-1",
		RunId:           run.GetRunId(),
		CampaignId:      run.GetCampaignBinding().GetCampaignId(),
		ScenarioId:      req.Catalog.GetScenarios()[0].GetScenarioId(),
		ScenarioRef:     &compliancev1.VersionedReference{Id: req.Catalog.GetScenarios()[0].GetScenarioId(), Version: req.Catalog.GetScenarios()[0].GetScenarioVersion()},
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			},
		},
	}
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	result := &evalv1.EvaluationAssignmentResult{
		AssignmentId: "assign-1",
		RunId:        run.GetRunId(),
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			InferenceRecordId: "inference-1",
			ProviderAttemptId: "attempt-remote",
			ModelRole:         evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			ModelVariant:      &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			AgentPersona:      "sage",
			UsageAvailability: evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED,
		}},
	}
	require.NoError(t, coordinator.PublishAssignmentResult(
		ctx,
		assignment,
		result,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		"unverified",
	))
	require.Len(t, exporter.records, 1)

	envelope := CampaignProjectionEnvelope{}
	require.NoError(t, json.Unmarshal([]byte(exporter.records[0].RecordBytes), &envelope))
	record := publicAssignmentRecordEnvelope{}
	require.NoError(t, json.Unmarshal(envelope.Record, &record))
	require.NotNil(t, record.BenchmarkObservations)
	require.NotNil(t, record.BenchmarkObservations.GPU)
	require.NotNil(t, record.BenchmarkObservations.GPU.VRAMPeakBytes)
	require.NotNil(t, record.BenchmarkObservations.GPU.VRAMPeakBytes.Value)
	assert.Equal(t, float64(16_000_000_000), *record.BenchmarkObservations.GPU.VRAMPeakBytes.Value)
}

func TestCampaignPublicationCoordinatorPublishAssignmentResultFailedAssignmentWithoutInferences(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	req := testCampaignInitRequest(t)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	exporter := &recordingCampaignFeedExporter{}

	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    "assign-failed-1",
		RunId:           run.GetRunId(),
		CampaignId:      run.GetCampaignBinding().GetCampaignId(),
		ScenarioId:      req.Catalog.GetScenarios()[0].GetScenarioId(),
		ScenarioRef:     &compliancev1.VersionedReference{Id: req.Catalog.GetScenarios()[0].GetScenarioId(), Version: req.Catalog.GetScenarios()[0].GetScenarioVersion()},
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			},
		},
	}
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	result := &evalv1.EvaluationAssignmentResult{
		AssignmentId:    "assign-failed-1",
		RunId:           run.GetRunId(),
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
		ModelInferences: nil,
	}
	require.NoError(t, coordinator.PublishAssignmentResult(
		ctx,
		assignment,
		result,
		evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE,
		"unverified",
	))
	require.Len(t, exporter.records, 1)
}

// savePublicRunIdentity stores the campaign spec and run identity the public
// projections of runID are built from.
func savePublicRunIdentity(t *testing.T, store *Store, runID, campaignID string) {
	t.Helper()
	req := testCampaignInitRequest(t)
	spec, err := MaterializeCampaignSpec(campaignID, req.Catalog, req.Inventory, req.RepetitionCount, false, testPlatform)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(context.Background(), spec))
	require.NoError(t, store.SaveRun(context.Background(), &evalv1.EvaluationRun{SchemaVersion: CampaignSchemaVersion, RunId: runID, CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: campaignID}}))
}

func TestResetFeedPublicationIdempotencyPreservesAuditProofKeys(t *testing.T) {
	publicationState := NewMemoryCampaignPublicationStateStore()
	runID := "run-feed-reset"
	require.NoError(t, publicationState.Save(context.Background(), &CampaignPublicationState{
		SchemaVersion:        campaignPublicationStateSchemaVersion,
		RunID:                runID,
		PublishedIdempotency: []string{"run:assignment-1:result", AssignmentAuditProofIdempotencyKey(runID, "assignment-1")},
		PublishedProofArtifacts: map[string]CampaignPublishedProofArtifacts{
			"assignment-1": {DatabaseSHA256: strings.Repeat("a", 64)},
		},
		LastPublishedSequence: 42,
	}))
	coordinator := NewCampaignPublicationCoordinator(nil, nil, publicationState, nil)
	require.NoError(t, coordinator.ResetFeedPublicationIdempotency(context.Background(), runID))
	loaded, err := publicationState.Load(context.Background(), runID)
	require.NoError(t, err)
	assert.Equal(t, []string{AssignmentAuditProofIdempotencyKey(runID, "assignment-1")}, loaded.PublishedIdempotency)
	assert.Equal(t, strings.Repeat("a", 64), loaded.PublishedProofArtifacts["assignment-1"].DatabaseSHA256)
	assert.Zero(t, loaded.LastPublishedSequence)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License 1.1 —
// see LICENSE for details.

package evaluation

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestProjectMetricAvailabilityEvent_RejectsMissingRole(t *testing.T) {
	_, err := ProjectMetricAvailabilityEvent(PublicMetricAvailabilitySignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		MetricID:     "pass_rate",
		Numerator:    1,
		Denominator:  1,
		ObservedAt:   "2026-09-24T12:00:05.000Z",
		EventID:      "run-live-1:assign-1:metric:pass_rate:44",
		Completed:    1,
		Total:        5,
	})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestProjectMetricAvailabilityEvent_RejectsInvalidProgress(t *testing.T) {
	_, err := ProjectMetricAvailabilityEvent(PublicMetricAvailabilitySignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		Role:         models.ModelRolePrimary,
		MetricID:     "pass_rate",
		Numerator:    1,
		Denominator:  1,
		ObservedAt:   "2026-09-24T12:00:05.000Z",
		EventID:      "run-live-1:assign-1:metric:pass_rate:45",
		Completed:    6,
		Total:        5,
	})
	require.ErrorIs(t, err, constants.ErrEvaluationLiveEventProgressInvalid)
}

func TestProjectModelRoleInvocationEvent_ExcludesProviderBoundaryFields(t *testing.T) {
	event, err := ProjectModelRoleInvocationEvent(PublicModelRoleInvocationSignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		Role:         models.ModelRolePrimary,
		TaskID:       "instruction-exact-format",
		ObservedAt:   "2026-09-24T12:00:00.000Z",
		EventID:      "run-live-1:assign-1:invocation:primary:42",
		Completed:    2,
		Total:        5,
	})
	require.NoError(t, err)
	assert.Equal(t, "stage_updated", event.Kind)
	assert.Equal(t, "ds-live-run-live-1", event.DatasetId)
	assert.Contains(t, event.StageLabel, "model role invoked")
	assert.Contains(t, event.StageLabel, "primary")

	body, err := MarshalPublicLiveEvent(event)
	require.NoError(t, err)
	assert.NotContains(t, string(body), "served_model_tag")
	assert.NotContains(t, string(body), "backend_name")
	assert.NotContains(t, string(body), "quantization")
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body))
}

func TestProjectMetricAvailabilityEvent_ProjectsPassRateDelta(t *testing.T) {
	event, err := ProjectMetricAvailabilityEvent(PublicMetricAvailabilitySignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		Role:         models.ModelRolePrimary,
		MetricID:     "pass_rate",
		Numerator:    3,
		Denominator:  4,
		Rate:         ptrFloat64(0.75),
		ObservedAt:   "2026-09-24T12:00:05.000Z",
		EventID:      "run-live-1:assign-1:metric:pass_rate:42",
		Completed:    1,
		Total:        5,
	})
	require.NoError(t, err)
	assert.Equal(t, "metric_updated", event.Kind)
	assert.Equal(t, "primary", event.Role, "a metric event names the role it grades")
	passRate, ok := event.MetricDelta["pass_rate"]
	require.True(t, ok)
	require.NotNil(t, passRate.Value)
	assert.InDelta(t, 0.75, *passRate.Value, 0.0001)

	body, err := MarshalPublicLiveEvent(event)
	require.NoError(t, err)
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body))
}

func TestProjectMetricAvailabilityEvent_ZeroDenominatorIsUnavailable(t *testing.T) {
	event, err := ProjectMetricAvailabilityEvent(PublicMetricAvailabilitySignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		Role:         models.ModelRolePrimary,
		MetricID:     "pass_rate",
		Numerator:    0,
		Denominator:  0,
		ObservedAt:   "2026-09-24T12:00:05.000Z",
		EventID:      "run-live-1:assign-1:metric:pass_rate:43",
		Completed:    0,
		Total:        5,
	})
	require.NoError(t, err)
	passRate, ok := event.MetricDelta["pass_rate"]
	require.True(t, ok)
	assert.Equal(t, "no_scored_calls", passRate.UnavailableReason)
	assert.Nil(t, passRate.Value)
}

func TestMarshalPublicLiveEvent_KeepsZeroProgressAndAMeasuredZero(t *testing.T) {
	zero := 0.0
	event, err := ProjectMetricAvailabilityEvent(PublicMetricAvailabilitySignal{
		RunID:        "run-live-1",
		AssignmentID: "assign-1",
		VariantID:    "qwen3-4b",
		Role:         models.ModelRolePrimary,
		MetricID:     assignmentPassMetricID,
		Numerator:    0,
		Denominator:  1,
		Rate:         &zero,
		ObservedAt:   "2026-09-24T12:00:05.000Z",
		EventID:      "run-live-1:assign-1:metric:pass:46",
		Completed:    0,
		Total:        5,
	})
	require.NoError(t, err)
	body, err := MarshalPublicLiveEvent(event)
	require.NoError(t, err)

	var wire struct {
		Completed   *int                      `json:"completed"`
		Total       *int                      `json:"total"`
		MetricDelta map[string]map[string]any `json:"metric_delta"`
	}
	require.NoError(t, json.Unmarshal(body, &wire))
	require.NotNil(t, wire.Completed, "zero progress is a value, not an omission")
	assert.Equal(t, 0, *wire.Completed)
	require.NotNil(t, wire.Total)
	value, present := wire.MetricDelta[assignmentPassMetricID]["value"]
	require.True(t, present, "a failed assignment's binary pass is a measured zero, not an omission")
	assert.InDelta(t, 0.0, value, 0)

	decoded := &evalv1.PublicLiveEvent{}
	require.NoError(t, evalv1.UnmarshalCanonical(body, decoded), "the body is the canonical protojson form")
	assert.True(t, proto.Equal(event, decoded))
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body))
}

func TestMarshalPublicLiveEvent_RejectsNil(t *testing.T) {
	_, err := MarshalPublicLiveEvent(nil)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestProjectLiveEvents_CarryTheCampaignReleaseIdentity(t *testing.T) {
	release := CampaignRelease{Release: "v2.3.0", Basis: ReleaseBasisRecorded, SourceRevision: "abc123"}
	invoked, err := ProjectModelRoleInvocationEvent(PublicModelRoleInvocationSignal{
		Release: release, RunID: "run-live-1", AssignmentID: "assign-1", VariantID: "qwen3-4b", Role: models.ModelRolePrimary,
		ObservedAt: "2026-09-24T12:00:00.000Z", EventID: "run-live-1:assign-1:invocation:primary:47", Completed: 0, Total: 5,
	})
	require.NoError(t, err)
	assert.Equal(t, "v2.3.0", invoked.GetRelease())
	assert.Equal(t, evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_RECORDED, invoked.GetReleaseBasis())
	assert.Equal(t, "abc123", invoked.GetSourceRevision())

	unknown, err := ProjectModelRoleInvocationEvent(PublicModelRoleInvocationSignal{
		RunID: "run-live-1", AssignmentID: "assign-1", VariantID: "qwen3-4b", Role: models.ModelRolePrimary,
		ObservedAt: "2026-09-24T12:00:00.000Z", EventID: "run-live-1:assign-1:invocation:primary:48", Completed: 0, Total: 5,
	})
	require.NoError(t, err)
	assert.Empty(t, unknown.GetRelease(), "a campaign without a release is not given one")
	assert.Equal(t, evalv1.PublicReleaseBasis_PUBLIC_RELEASE_BASIS_UNKNOWN, unknown.GetReleaseBasis())
}

func TestCampaignPublicationCoordinator_ExportsProjectionRecordsOnly(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	savePublicRunIdentity(t, store, "run-1", "campaign-1")
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	assignment := &evalv1.EvaluationAssignment{
		AssignmentId:    "assign-1",
		RunId:           "run-1",
		ScenarioId:      "scenario-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_QUEUED,
		QueuedAt:        timestamppb.New(time.Unix(1_700_000_000, 0).UTC()),
	}
	category := evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_INSTRUCTION_ADHERENCE
	require.NoError(t, coordinator.PublishAssignmentLifecycle(context.Background(), assignment, category, assignment.GetQueuedAt().AsTime()))
	require.NotEmpty(t, exporter.records)
	for _, record := range exporter.records {
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(record.RecordBytes), &body))
		assert.Contains(t, body, "message_type")
		assert.NotEqual(t, "metric_updated", body["kind"])
		assert.NotEqual(t, "stage_updated", body["kind"])
	}
}

const campaignPublicationEmitsNativeLiveEvents = true

func TestCampaignPublication_PublishesNativeLiveEventsFromTerminalResult(t *testing.T) {
	require.True(t, campaignPublicationEmitsNativeLiveEvents)
	files := newCampaignRuntimeFileService(t)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(NewStore(files), files, NewMemoryCampaignPublicationStateStore(), exporter)
	runID := "run-live-1"
	assignmentID := "assign-1"
	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    assignmentID,
		RunId:           runID,
		CampaignId:      "campaign-1",
		ScenarioId:      "scenario-1",
		Repetition:      1,
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			},
		},
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    assignmentID,
		RunId:           runID,
		CampaignId:      "campaign-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.New(time.Unix(1_700_000_010, 0).UTC()),
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			ModelRole:         evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			ModelVariant:      &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			UsageAvailability: evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED,
		}},
		DeterministicGrades: []*evalv1.DeterministicGrade{{
			CriterionId: "role-invoked",
			Basis:       basisStructural,
			Status:      evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		}},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	store := NewStore(files)
	savePublicRunIdentity(t, store, assignment.GetRunId(), assignment.GetCampaignId())
	require.NoError(t, store.SaveRun(context.Background(), &evalv1.EvaluationRun{
		SchemaVersion:   CampaignSchemaVersion,
		RunId:           runID,
		CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: "campaign-1"},
	}))
	require.NoError(t, store.SaveAssignment(context.Background(), assignment))
	require.NoError(t, store.SaveAssignmentResult(context.Background(), result))
	require.NoError(t, coordinator.PublishAssignmentLiveEvents(context.Background(), assignment, result))
	require.NotEmpty(t, exporter.records)
	foundScoredInvocation := false
	foundMetric := false
	for _, record := range exporter.records {
		require.Equal(t, models.PublicFeedRecordTypeEvent, record.RecordType)
		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(record.RecordBytes), &body))
		switch body["kind"] {
		case "stage_updated":
			foundScoredInvocation = true
		case "metric_updated":
			foundMetric = true
		}
	}
	assert.True(t, foundScoredInvocation)
	assert.True(t, foundMetric)
}

func TestCampaignPublication_PublishesInvocationAtAssignmentStart(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(NewStore(files), files, NewMemoryCampaignPublicationStateStore(), exporter)
	runID := "run-live-start"
	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    "assign-start",
		RunId:           runID,
		CampaignId:      "campaign-1",
		ScenarioId:      "scenario-1",
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING,
		StartedAt:       timestamppb.New(time.Unix(1_700_000_001, 0).UTC()),
		Target: &evalv1.EvaluationAssignment_Homogeneous{
			Homogeneous: &evalv1.HomogeneousAssignmentTarget{
				DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
				CandidateVariant: &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			},
		},
	}
	store := NewStore(files)
	savePublicRunIdentity(t, store, assignment.GetRunId(), assignment.GetCampaignId())
	require.NoError(t, store.SaveRun(context.Background(), &evalv1.EvaluationRun{
		SchemaVersion:   CampaignSchemaVersion,
		RunId:           runID,
		CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: "campaign-1"},
	}))
	require.NoError(t, store.SaveAssignment(context.Background(), assignment))
	require.NoError(t, coordinator.PublishAssignmentInvocationLiveEvents(context.Background(), assignment))
	require.Len(t, exporter.records, 1)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(exporter.records[0].RecordBytes), &body))
	assert.Equal(t, "stage_updated", body["kind"])
	assert.NotContains(t, body, "metric_delta")
}

func ptrFloat64(value float64) *float64 {
	return &value
}

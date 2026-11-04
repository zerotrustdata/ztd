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

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func deterministicGradingGrades() []*evalv1.DeterministicGrade {
	return []*evalv1.DeterministicGrade{
		{GradeId: "g-1", CriterionId: "role-invoked", Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, Detail: "designated model role invoked", Basis: basisStructural},
		{GradeId: "g-2", CriterionId: "trajectory", Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, Detail: "wrong tool", Basis: basisObservation},
		nil,
		{GradeId: "g-3", CriterionId: "file-state", Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, Basis: basisObservation},
	}
}

func deterministicGradingEventsOf(t *testing.T, records []CampaignPublicFeedRecord) []*evalv1.PublicLiveEvent {
	t.Helper()
	var events []*evalv1.PublicLiveEvent
	for _, record := range records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			continue
		}
		event := &evalv1.PublicLiveEvent{}
		require.NoError(t, evalv1.UnmarshalCanonical([]byte(record.RecordBytes), event))
		if event.GetKind() == liveEventKindDeterministicGradingCompleted {
			events = append(events, event)
		}
	}
	return events
}

func deterministicGradingResult(grades []*evalv1.DeterministicGrade) *evalv1.EvaluationAssignmentResult {
	return &evalv1.EvaluationAssignmentResult{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: judgeActivityAssignment, RunId: judgeActivityRunID, CampaignId: judgeActivityCampaignID,
		Lane:                evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus:     evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		DeterministicGrades: grades,
		CompletedAt:         timestamppb.New(time.Unix(1_700_000_010, 0).UTC()),
	}
}

func newDeterministicGradingCoordinator(t *testing.T) (*CampaignPublicationCoordinator, *recordingCampaignFeedExporter, *evalv1.EvaluationAssignment) {
	t.Helper()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	saveJudgedCampaign(t, store)
	assignment := judgeActivityAssignmentRecord()
	require.NoError(t, store.SaveAssignment(context.Background(), assignment))
	exporter := &recordingCampaignFeedExporter{}
	return NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter), exporter, assignment
}

// The event states every grade in grading order on the graded subject's row,
// and it passes the public feed validator.
func TestProjectDeterministicGradingEvent_NamesTheAssignmentRow(t *testing.T) {
	event, err := ProjectDeterministicGradingEvent(PublicDeterministicGradingSignal{
		RunID: judgeActivityRunID, AssignmentID: judgeActivityAssignment, VariantID: "gemma4-e2b", Role: models.ModelRolePrimary,
		TaskID: judgeActivityScenarioID, ObservedAt: "2026-10-30T12:00:00Z", EventID: "event-1", Completed: 0, Total: 4,
		Grades: publicLiveDeterministicGrades(deterministicGradingGrades()),
	})
	require.NoError(t, err)
	assert.Equal(t, "deterministic_grading_completed", event.GetKind())
	assert.Equal(t, "gemma4-e2b", event.GetVariantId())
	assert.Equal(t, "primary", event.GetRole())
	assert.Equal(t, "running", event.GetLifecycleStatus())
	criteria := make([]string, 0, 3)
	for _, grade := range event.GetDeterministicGrades() {
		criteria = append(criteria, grade.GetCriterionId())
	}
	assert.Equal(t, []string{"role-invoked", "trajectory", "file-state"}, criteria, "grading order, and a nil grade is dropped")
	body, err := MarshalPublicLiveEvent(event)
	require.NoError(t, err)
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body))
}

// One grading is one event: the terminal rebuild of what was published live is
// not published again, and the live event keeps its own time.
func TestCampaignPublication_RebuildsIdenticalDeterministicGradingOnce(t *testing.T) {
	ctx := context.Background()
	coordinator, exporter, assignment := newDeterministicGradingCoordinator(t)
	liveAt := time.Unix(1_700_000_004, 0).UTC()
	live, err := coordinator.NewGradingActivityPublisher(ctx, assignment, func() time.Time { return liveAt })
	require.NoError(t, err)
	live.DeterministicGrading(ctx, deterministicGradingGrades())
	require.NoError(t, live.Err())
	require.Len(t, deterministicGradingEventsOf(t, exporter.records), 1)

	require.NoError(t, coordinator.PublishAssignmentLiveEvents(ctx, assignment, deterministicGradingResult(deterministicGradingGrades())))

	events := deterministicGradingEventsOf(t, exporter.records)
	require.Len(t, events, 1)
	assert.Equal(t, liveAt.Format(time.RFC3339Nano), events[0].GetObservedAt())
}

// A persisted result that does not hold the published grades supersedes them,
// so a row and assignment-<id>.db end on the grades the result records.
func TestCampaignPublication_PersistedGradesSupersedeDifferentLiveGrades(t *testing.T) {
	ctx := context.Background()
	coordinator, exporter, assignment := newDeterministicGradingCoordinator(t)
	live, err := coordinator.NewGradingActivityPublisher(ctx, assignment, func() time.Time { return time.Unix(1_700_000_004, 0).UTC() })
	require.NoError(t, err)
	live.DeterministicGrading(ctx, deterministicGradingGrades())
	require.NoError(t, live.Err())

	failure := []*evalv1.DeterministicGrade{{GradeId: "g-f", CriterionId: executionFailureCriterion, Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, Detail: executionFailureDetailPrefix + "provider", Basis: basisStructural}}
	require.NoError(t, coordinator.PublishAssignmentLiveEvents(ctx, assignment, deterministicGradingResult(failure)))

	events := deterministicGradingEventsOf(t, exporter.records)
	require.Len(t, events, 2)
	assert.NotEqual(t, events[0].GetEventId(), events[1].GetEventId())
	require.Len(t, events[1].GetDeterministicGrades(), 1)
	assert.Equal(t, executionFailureCriterion, events[1].GetDeterministicGrades()[0].GetCriterionId())
	assert.Greater(t, events[1].GetObservedAt(), events[0].GetObservedAt(), "the persisted grades are the row's latest")
}

// A result with no grades, such as INVALID_EVIDENCE, states none.
func TestCampaignPublication_NoGradesNoDeterministicGradingEvent(t *testing.T) {
	ctx := context.Background()
	coordinator, exporter, assignment := newDeterministicGradingCoordinator(t)
	live, err := coordinator.NewGradingActivityPublisher(ctx, assignment, func() time.Time { return time.Unix(1_700_000_004, 0).UTC() })
	require.NoError(t, err)
	live.DeterministicGrading(ctx, nil)
	require.NoError(t, live.Err())
	require.NoError(t, coordinator.PublishAssignmentLiveEvents(ctx, assignment, deterministicGradingResult(nil)))
	assert.Empty(t, deterministicGradingEventsOf(t, exporter.records))
}

// A result whose grades are its only live record, like a failed tool-calling
// probe's, still states them on its row, and so has audit events to prove.
func TestCampaignPublication_GradesAreALiveRecordOfAnUngradedProbeResult(t *testing.T) {
	ctx := context.Background()
	coordinator, exporter, assignment := newDeterministicGradingCoordinator(t)
	result := deterministicGradingResult([]*evalv1.DeterministicGrade{{
		GradeId: "g-probe", CriterionId: toolCallingProbeCriterion, Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_UNAVAILABLE, Detail: "the model cannot call tools", Basis: basisStructural,
	}})
	result.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE

	requests, err := coordinator.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	assert.True(t, hasAssignmentAuditEvents(requests))

	require.NoError(t, coordinator.PublishAssignmentLiveEvents(ctx, assignment, result))
	events := deterministicGradingEventsOf(t, exporter.records)
	require.Len(t, events, 1)
	assert.Equal(t, toolCallingProbeCriterion, events[0].GetDeterministicGrades()[0].GetCriterionId())
}

// A grading event that cannot be published is kept for the terminal report and
// stops the assignment's later live events.
func TestGradingActivityPublisher_DeterministicGradingStopsAfterTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	coordinator, exporter, assignment := newDeterministicGradingCoordinator(t)
	publisher, err := coordinator.NewGradingActivityPublisher(ctx, assignment, func() time.Time { return time.Unix(1_700_000_004, 0).UTC() })
	require.NoError(t, err)
	invalid := []*evalv1.DeterministicGrade{{CriterionId: "role-invoked"}}
	publisher.DeterministicGrading(ctx, invalid)
	require.Error(t, publisher.Err())
	publisher.DeterministicGrading(ctx, deterministicGradingGrades())
	assert.Empty(t, deterministicGradingEventsOf(t, exporter.records))
}

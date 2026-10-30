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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/judge"
	"github.com/zerotrustdata/ztd/internal/services/publicdisclosure"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	judgeActivityRunID      = "run-judge"
	judgeActivityAssignment = "assign-judge"
	judgeActivityCampaignID = "campaign-1"
	judgeActivityScenarioID = "tool-select-constraints"
)

func judgeActivityJudge() *evalv1.ModelVariant {
	return testJudgeRegistryVariant("qwen3-5-4b", DefaultJudgeServedModelTag)
}

func judgeActivityAssignmentRecord() *evalv1.EvaluationAssignment {
	return &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    judgeActivityAssignment,
		RunId:           judgeActivityRunID,
		CampaignId:      judgeActivityCampaignID,
		ScenarioId:      judgeActivityScenarioID,
		Repetition:      1,
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_RUNNING,
		StartedAt:       timestamppb.New(time.Unix(1_700_000_001, 0).UTC()),
		Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
			DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			CandidateVariant: &evalv1.ModelVariant{VariantId: "gemma4-e2b"},
		}},
	}
}

// signedJudgeScore is a judge score whose result answers attemptID under the
// campaign judge's authority and whose receipt binds its digest, as the judge
// resolves it.
func signedJudgeScore(t *testing.T, subject judge.Subject, criterion, attemptID, text string, position judge.Position) judge.Score {
	t.Helper()
	variant := judgeActivityJudge()
	_, registryDigest, err := JudgeModelRegistry(judgeActivityCampaignID, variant)
	require.NoError(t, err)
	result := &operatorv1.InferenceResult{
		ProviderAttemptId: attemptID, RequestedModel: variant.GetServedModelTag(),
		CampaignId: judgeActivityCampaignID, ModelRegistryDigest: registryDigest,
		RunId: judgeActivityRunID, AssignmentId: judgeActivityAssignment, ScenarioId: judgeActivityScenarioID,
		FinishReason: "stop", UsageReported: true, PromptTokens: 812, CompletionTokens: 40,
		ThinkingTokens: proto.Int32(12), TotalDurationNs: proto.Int64(1_200_000_000), LoadDurationNs: proto.Int64(-1),
		Parts: []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_Text{Text: text}}},
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	rubric := judge.RubricVersion
	if subject == judge.SubjectCriterion {
		rubric = judge.ObjectiveCriterionRubricVersion
	}
	return judge.Score{
		Subject: subject, Criterion: criterion, Position: position, Status: judge.StatusScored,
		JudgeModelTag: variant.GetServedModelTag(), JudgeModelDigest: variant.GetModelDigest(),
		ProviderAttemptID: attemptID, TransactionID: "tx-" + attemptID,
		Receipt: &operatorv1.EventReceipt{TransactionId: "tx-" + attemptID, ResultSummary: digest, ExecutedAtUnixMs: 1_700_000_005_000},
		Result:  result, RubricVersion: rubric,
	}
}

func judgeActivitySignal(activity JudgeActivity) PublicJudgeActivitySignal {
	return PublicJudgeActivitySignal{
		RunID: judgeActivityRunID, AssignmentID: judgeActivityAssignment, VariantID: "gemma4-e2b", Role: models.ModelRolePrimary,
		TaskID: judgeActivityScenarioID, ObservedAt: "2026-10-30T12:00:00Z", EventID: "event-1", Completed: 0, Total: 4,
		Activity: activity,
	}
}

func validatedJudgeEvent(t *testing.T, activity JudgeActivity) *evalv1.PublicLiveEvent {
	t.Helper()
	event, err := ProjectJudgeActivityEvent(judgeActivitySignal(activity))
	require.NoError(t, err)
	body, err := MarshalPublicLiveEvent(event)
	require.NoError(t, err)
	require.NoError(t, publicdisclosure.ValidatePublicFeedRecord(models.PublicFeedRecordTypeEvent, body))
	return event
}

func TestProjectJudgeActivityEvent_PlacesTheJudgeOnTheSubjectsRow(t *testing.T) {
	position := judge.Position{Index: 1, Count: 2}
	met := signedJudgeScore(t, judge.SubjectCriterion, "refusal", "judge-criterion-1", `{"met": true, "rationale": "refuses"}`, position)
	met.Met, met.Rationale = true, "refuses"

	started := validatedJudgeEvent(t, JudgeActivity{Judge: judgeActivityJudge(), Score: judge.Score{
		Subject: met.Subject, Criterion: met.Criterion, Position: position, RubricVersion: met.RubricVersion, ProviderAttemptID: met.ProviderAttemptID,
	}})
	assert.Equal(t, liveEventKindJudgeStarted, started.GetKind())
	assert.Equal(t, "gemma4-e2b", started.GetVariantId(), "the row's model stays the graded subject")
	assert.Equal(t, "primary", started.GetRole())
	assert.Empty(t, started.GetMetricDelta(), "judge usage never merges into the subject's metric columns")
	assert.Equal(t, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_JUDGING, started.GetJudge().GetState())
	assert.Equal(t, DefaultJudgeServedModelTag, started.GetJudge().GetServedModelTag())
	assert.Empty(t, started.GetJudge().GetTransactionId())

	completed := validatedJudgeEvent(t, JudgeActivity{Judge: judgeActivityJudge(), Score: met, Completed: true, Elapsed: 1300 * time.Millisecond})
	block := completed.GetJudge()
	assert.Equal(t, liveEventKindJudgeCompleted, completed.GetKind())
	assert.Equal(t, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_MET, block.GetState())
	assert.Equal(t, "refusal", block.GetCriterionId())
	assert.Equal(t, uint32(1), block.GetIndex())
	assert.Equal(t, uint32(2), block.GetCount())
	assert.Equal(t, "refuses", block.GetRationale())
	assert.Equal(t, uint32(812), block.GetPromptTokens())
	assert.Equal(t, uint32(40), block.GetCompletionTokens())
	assert.Equal(t, uint32(12), block.GetThinkingTokens())
	assert.Nil(t, block.CacheTokens, "an unreported count stays absent")
	assert.Equal(t, uint64(1_200_000_000), block.GetTotalDurationNs())
	assert.Nil(t, block.LoadDurationNs, "a negative provider duration is not disclosed")
	assert.Equal(t, int64(1_700_000_005_000), block.GetExecutedAtUnixMs())
	assert.Equal(t, uint64(1_300_000_000), block.GetDispatchElapsedNs())
	assert.Equal(t, "tx-judge-criterion-1", block.GetTransactionId())

	scored := signedJudgeScore(t, judge.SubjectFinalReply, "", "judge-final_reply-1", `{"score": 4, "rationale": "x"}`, judge.Position{Index: 1, Count: 1})
	scored.Value, scored.Rationale = 4, strings.Repeat("é", 4096)
	quality := validatedJudgeEvent(t, JudgeActivity{Judge: judgeActivityJudge(), Score: scored, Completed: true}).GetJudge()
	assert.Equal(t, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_SCORED, quality.GetState())
	assert.Equal(t, uint32(4), quality.GetScore())
	assert.Empty(t, quality.GetCriterionId())
	assert.Equal(t, scored.Rationale, quality.GetRationale(), "the rationale is verbatim evaluation evidence")

	failed := scored
	failed.Status, failed.Failure, failed.Detail = judge.StatusFailed, evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID, "raw provider text"
	failedBlock := validatedJudgeEvent(t, JudgeActivity{Judge: judgeActivityJudge(), Score: failed, Completed: true}).GetJudge()
	assert.Equal(t, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_FAILED, failedBlock.GetState())
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID, failedBlock.GetFailure())
	assert.Empty(t, failedBlock.GetRationale(), "failure text is never disclosed")
}

func TestJudgeActivityIdempotencyKeys_NameTheJudgmentOnce(t *testing.T) {
	sent := judge.Score{Subject: judge.SubjectCriterion, Criterion: "refusal", ProviderAttemptID: "judge-criterion-1"}
	unsent := judge.Score{Subject: judge.SubjectCriterion, Criterion: "refusal"}
	assert.Equal(t, "run-1:a-1:judge-started:judge-criterion-1", JudgeStartedIdempotencyKey("run-1", "a-1", judgmentID(sent)))
	assert.Equal(t, "run-1:a-1:judge:judge-criterion-1", JudgeCompletedIdempotencyKey("run-1", "a-1", judgmentID(sent)))
	assert.Equal(t, "unsent:criterion:refusal", judgmentID(unsent))
}

// judgedResult is a terminal result with one judged criterion and one
// diagnosis-quality score, each bound to its grader call.
func judgedResult(t *testing.T, met, quality judge.Score) *evalv1.EvaluationAssignmentResult {
	t.Helper()
	ref := func(score judge.Score) *compliancev1.ComplianceEvidenceReference {
		return &compliancev1.ComplianceEvidenceReference{ArtifactId: "receipt:" + score.TransactionID}
	}
	call := func(criterionID string, score judge.Score) *evalv1.GraderModelCallRecord {
		return &evalv1.GraderModelCallRecord{
			GraderCallId: "grader-" + score.ProviderAttemptID, AssignmentId: judgeActivityAssignment, JudgeVariantId: judgeActivityJudge().GetVariantId(),
			InferenceRecordRef: ref(score), CriterionId: criterionID, RubricVersion: score.RubricVersion,
			ProviderAttemptId: score.ProviderAttemptID, TransactionId: score.TransactionID, InferenceResult: score.Result, Receipt: score.Receipt,
		}
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion: CampaignSchemaVersion, AssignmentId: judgeActivityAssignment, RunId: judgeActivityRunID, CampaignId: judgeActivityCampaignID,
		Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
		CompletedAt:     timestamppb.New(time.Unix(1_700_000_010, 0).UTC()),
		SemanticGrades: []*evalv1.SemanticGrade{
			{GradeId: judgeActivityAssignment + ":content:refusal", CriterionId: "content:refusal", Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, JudgeVariantId: judgeActivityJudge().GetVariantId(), GraderCallRef: ref(met), Detail: "refuses"},
			{GradeId: judgeActivityAssignment + ":diagnosis-quality", CriterionId: diagnosisQualityCriterionID, Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, JudgeVariantId: judgeActivityJudge().GetVariantId(), GraderCallRef: ref(quality), Detail: qualityGradeDetail(4, judge.RubricVersion, "clear")},
		},
		GraderCalls: []*evalv1.GraderModelCallRecord{call("content:refusal", met), call(diagnosisQualityCriterionID, quality)},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	return result
}

func saveJudgedCampaign(t *testing.T, store *Store) {
	t.Helper()
	req := testCampaignInitRequest(t)
	spec, err := MaterializeCampaignSpec(judgeActivityCampaignID, req.Catalog, req.Inventory, req.RepetitionCount, false, testPlatform)
	require.NoError(t, err)
	spec.JudgeVariant = judgeActivityJudge()
	spec.CampaignDigest, err = ComputeCampaignSpecDigest(spec)
	require.NoError(t, err)
	require.NoError(t, store.SaveCampaignSpec(context.Background(), spec))
	require.NoError(t, store.SaveRun(context.Background(), &evalv1.EvaluationRun{SchemaVersion: CampaignSchemaVersion, RunId: judgeActivityRunID, CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: judgeActivityCampaignID}}))
}

func judgeEventsOf(t *testing.T, records []CampaignPublicFeedRecord) []*evalv1.PublicLiveEvent {
	t.Helper()
	var events []*evalv1.PublicLiveEvent
	for _, record := range records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			continue
		}
		event := &evalv1.PublicLiveEvent{}
		require.NoError(t, evalv1.UnmarshalCanonical([]byte(record.RecordBytes), event))
		if event.GetJudge() != nil {
			events = append(events, event)
		}
	}
	return events
}

// TestCampaignPublication_RebuildsJudgeEventsUnderTheLiveKeys: deferred
// publication rebuilds one judge_completed per grader call from its signed
// result, ahead of the assignment's terminal events, and a judgment already
// published live is not published again.
func TestCampaignPublication_RebuildsJudgeEventsUnderTheLiveKeys(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	saveJudgedCampaign(t, store)
	assignment := judgeActivityAssignmentRecord()
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)

	met := signedJudgeScore(t, judge.SubjectCriterion, "refusal", "judge-criterion-1", `{"met": true, "rationale": "refuses"}`, judge.Position{Index: 1, Count: 1})
	met.Met, met.Rationale = true, "refuses"
	quality := signedJudgeScore(t, judge.SubjectFinalReply, "", "judge-final_reply-1", `{"score": 4, "rationale": "clear"}`, judge.Position{Index: 1, Count: 1})

	live, err := coordinator.NewJudgeActivityPublisher(ctx, assignment, func() time.Time { return time.Unix(1_700_000_004, 0) })
	require.NoError(t, err)
	live.JudgeActivity(ctx, JudgeActivity{Judge: judgeActivityJudge(), Score: met, Completed: true, Elapsed: time.Second})
	require.NoError(t, live.Err())
	require.Len(t, judgeEventsOf(t, exporter.records), 1)

	result := judgedResult(t, met, quality)
	require.NoError(t, store.SaveAssignmentResult(ctx, result))
	require.NoError(t, coordinator.PublishAssignmentLiveEvents(ctx, assignment, result))

	events := judgeEventsOf(t, exporter.records)
	require.Len(t, events, 2, "the live criterion judgment is not republished; the quality judgment is rebuilt")
	assert.Equal(t, JudgeCompletedIdempotencyKey(judgeActivityRunID, judgeActivityAssignment, "judge-criterion-1")+":event", events[0].GetEventId())
	assert.Equal(t, uint64(1_000_000_000), events[0].GetJudge().GetDispatchElapsedNs(), "the live event keeps its wall time")
	rebuilt := events[1]
	assert.Equal(t, JudgeCompletedIdempotencyKey(judgeActivityRunID, judgeActivityAssignment, "judge-final_reply-1")+":event", rebuilt.GetEventId())
	assert.Equal(t, evalv1.JudgeActivityState_JUDGE_ACTIVITY_STATE_SCORED, rebuilt.GetJudge().GetState())
	assert.Equal(t, uint32(4), rebuilt.GetJudge().GetScore(), "the rebuilt verdict is read from the signed result")
	assert.Equal(t, "clear", rebuilt.GetJudge().GetRationale())
	assert.Equal(t, uint32(1), rebuilt.GetJudge().GetCount())
	assert.Nil(t, rebuilt.GetJudge().DispatchElapsedNs, "a rebuilt event has no harness wall time")
	assert.Equal(t, time.UnixMilli(1_700_000_005_000).UTC().Format(time.RFC3339Nano), rebuilt.GetObservedAt(), "a rebuilt event is observed when the receipt says it executed")

	firstJudge, firstTerminal := -1, -1
	for i, record := range exporter.records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			continue
		}
		event := &evalv1.PublicLiveEvent{}
		require.NoError(t, evalv1.UnmarshalCanonical([]byte(record.RecordBytes), event))
		if event.GetJudge() != nil && event.GetEventId() == rebuilt.GetEventId() && firstJudge < 0 {
			firstJudge = i
		}
		if (event.GetKind() == "stage_updated" || event.GetKind() == "metric_updated") && firstTerminal < 0 {
			firstTerminal = i
		}
	}
	require.GreaterOrEqual(t, firstTerminal, 0)
	assert.Less(t, firstJudge, firstTerminal, "judge events precede the assignment's terminal events")
}

func TestRecordedJudgeScore_ReadsTheVerdictFromTheSignedResult(t *testing.T) {
	cfg, err := campaignJudgeConfig(AssignmentExecutionRequest{Assignment: judgeActivityAssignmentRecord(), Binding: CampaignExecutionBinding{JudgeVariant: judgeActivityJudge()}}, nil)
	require.NoError(t, err)
	invalid := signedJudgeScore(t, judge.SubjectMemory, "", "judge-memory-1", "not-json", judge.Position{Index: 2, Count: 2})
	call := &evalv1.GraderModelCallRecord{CriterionId: memoryQualityCriterionID, RubricVersion: invalid.RubricVersion, ProviderAttemptId: invalid.ProviderAttemptID, TransactionId: invalid.TransactionID, InferenceResult: invalid.Result, Receipt: invalid.Receipt}

	score := recordedJudgeScore(cfg, call, judge.Position{Index: 2, Count: 2})

	assert.Equal(t, judge.SubjectMemory, score.Subject)
	assert.Equal(t, judge.StatusFailed, score.Status)
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_VERDICT_INVALID, score.Failure)
}

func TestJudgedGradePositions_NumberEachPhaseInResultOrder(t *testing.T) {
	result := &evalv1.EvaluationAssignmentResult{SemanticGrades: []*evalv1.SemanticGrade{
		{CriterionId: "content:a"}, {CriterionId: "content:b"}, {CriterionId: diagnosisQualityCriterionID}, {CriterionId: memoryQualityCriterionID}, {CriterionId: "other"},
	}}
	assert.Equal(t, map[string]judge.Position{
		"content:a":                 {Index: 1, Count: 2},
		"content:b":                 {Index: 2, Count: 2},
		diagnosisQualityCriterionID: {Index: 1, Count: 2},
		memoryQualityCriterionID:    {Index: 2, Count: 2},
	}, judgedGradePositions(result))
}

// judgingCampaignExecutor reports one failed judge dispatch to the request's
// sink, then fails the assignment as recoverable, as a judge-bearing executor
// would after judging.
type judgingCampaignExecutor struct {
	sinkSeen bool
}

func (e *judgingCampaignExecutor) ExecuteAssignment(ctx context.Context, req AssignmentExecutionRequest) (*evalv1.EvaluationAssignmentResult, error) {
	if req.JudgeActivity != nil {
		e.sinkSeen = true
		score := judge.Score{Subject: judge.SubjectFinalReply, Position: judge.Position{Index: 1, Count: 1}, RubricVersion: judge.RubricVersion, ProviderAttemptID: "judge-final_reply-1"}
		req.JudgeActivity.JudgeActivity(ctx, JudgeActivity{Judge: judgeActivityJudge(), Score: score})
		score.Status, score.Failure = judge.StatusFailed, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED
		req.JudgeActivity.JudgeActivity(ctx, JudgeActivity{Judge: judgeActivityJudge(), Score: score, Completed: true, Elapsed: time.Second})
	}
	return nil, assignmentExecutionError("evaluation: execute assignment", fmt.Errorf("injected executor failure"))
}

// judgeRejectingCampaignFeedExporter rejects every batch that carries a judge
// event and accepts the rest.
type judgeRejectingCampaignFeedExporter struct {
	recordingCampaignFeedExporter
}

func (r *judgeRejectingCampaignFeedExporter) ExportBatch(ctx context.Context, records []CampaignPublicFeedRecord) error {
	for _, record := range records {
		if strings.Contains(record.RecordBytes, `"judge":`) {
			return constants.ErrEvidenceArtifactTooLarge
		}
	}
	return r.recordingCampaignFeedExporter.ExportBatch(ctx, records)
}

func executeJudgedAssignment(t *testing.T, executor CampaignAssignmentExecutor, exporter CampaignFeedExporter) (*Store, *evalv1.EvaluationAssignmentResult, error) {
	t.Helper()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	if exporter != nil {
		controller.WithPublication(NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter))
	}
	req := testCampaignInitRequest(t)
	run, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), run.GetRunId())
	require.NoError(t, err)
	result, executed, err := controller.ExecuteNextAssignment(context.Background(), run.GetRunId(), CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}, req.ScenarioArtifacts)
	require.True(t, executed)
	require.NotNil(t, result)
	return store, result, err
}

func TestExecuteNextAssignment_PublishesJudgeActivityLive(t *testing.T) {
	exporter := &recordingCampaignFeedExporter{}
	executor := &judgingCampaignExecutor{}

	_, _, err := executeJudgedAssignment(t, executor, exporter)

	require.NoError(t, err)
	require.True(t, executor.sinkSeen)
	events := judgeEventsOf(t, exporter.records)
	require.Len(t, events, 2)
	assert.Equal(t, liveEventKindJudgeStarted, events[0].GetKind())
	assert.Equal(t, liveEventKindJudgeCompleted, events[1].GetKind())
	assert.Equal(t, evalv1.JudgeFailure_JUDGE_FAILURE_DISPATCH_FAILED, events[1].GetJudge().GetFailure())
	assert.Equal(t, uint64(time.Second), events[1].GetJudge().GetDispatchElapsedNs())
}

// A judge event that cannot be published is reported with the persisted
// result and changes nothing it records.
func TestExecuteNextAssignment_JudgeActivityPublishFailureKeepsTheResult(t *testing.T) {
	_, unpublished, err := executeJudgedAssignment(t, &judgingCampaignExecutor{}, nil)
	require.NoError(t, err)

	store, result, err := executeJudgedAssignment(t, &judgingCampaignExecutor{}, &judgeRejectingCampaignFeedExporter{})

	require.ErrorIs(t, err, constants.ErrEvaluationPublicationFailed)
	require.ErrorIs(t, err, constants.ErrEvidenceArtifactTooLarge)
	assert.Contains(t, err.Error(), "judge activity")
	exists, existsErr := store.AssignmentResultExists(context.Background(), result.GetRunId(), result.GetAssignmentId())
	require.NoError(t, existsErr)
	assert.True(t, exists)
	assert.Equal(t, unpublished.GetLifecycleStatus(), result.GetLifecycleStatus())
	assert.Equal(t, unpublished.GetSemanticGrades(), result.GetSemanticGrades())
}

// countingRejectingCampaignFeedExporter rejects every batch and counts them.
type countingRejectingCampaignFeedExporter struct {
	batches int
}

func (*countingRejectingCampaignFeedExporter) HighWaterSequence(context.Context) (int64, error) {
	return 0, nil
}

func (r *countingRejectingCampaignFeedExporter) ExportBatch(context.Context, []CampaignPublicFeedRecord) error {
	r.batches++
	return constants.ErrEvidenceArtifactTooLarge
}

func TestJudgeActivityPublisher_StopsAfterTheFirstFailure(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	saveJudgedCampaign(t, store)
	assignment := judgeActivityAssignmentRecord()
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	exporter := &countingRejectingCampaignFeedExporter{}
	publisher, err := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).
		NewJudgeActivityPublisher(ctx, assignment, func() time.Time { return time.Unix(1_700_000_004, 0) })
	require.NoError(t, err)
	started := judge.Score{Subject: judge.SubjectFinalReply, Position: judge.Position{Index: 1, Count: 1}, RubricVersion: judge.RubricVersion, ProviderAttemptID: "judge-final_reply-1"}

	publisher.JudgeActivity(ctx, JudgeActivity{Judge: judgeActivityJudge(), Score: started})
	publisher.JudgeActivity(ctx, JudgeActivity{Judge: judgeActivityJudge(), Score: started})

	require.ErrorIs(t, publisher.Err(), constants.ErrEvaluationPublicationFailed)
	assert.Equal(t, 1, exporter.batches, "a failed export is not retried for later judgments")
}

func TestExecuteNextAssignment_NoJudgeSinkWithoutPublication(t *testing.T) {
	executor := &judgingCampaignExecutor{}
	_, _, err := executeJudgedAssignment(t, executor, nil)
	require.NoError(t, err)
	assert.False(t, executor.sinkSeen)
}

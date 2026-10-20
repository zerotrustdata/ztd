// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// fakeGatewayDatabaseDigest is the database digest the stub Gateway reports for
// an assignment. The host never derives this digest, so a record that cites it
// proves the record bound the Gateway's answer.
func fakeGatewayDatabaseDigest(assignmentID string) string {
	return hashBytes([]byte("gateway-assignment-database:" + assignmentID))
}

// recordingCampaignProofPublisher stands in for the Gateway proof catalog. It
// answers each input with the Gateway's database digest unless answer is set,
// records the requests it receives, and logs each ingest and flush into the
// shared order log when one is attached.
type recordingCampaignProofPublisher struct {
	order    *[]string
	inputs   []AssignmentAuditProofInput
	deferred []bool
	flushes  int
	answer   func(inputs []AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse
}

func (r *recordingCampaignProofPublisher) IngestAssignmentAuditSlices(_ context.Context, inputs []AssignmentAuditProofInput, deferMirrorPush bool) ([]models.PublicAssignmentAuditProofPublishResponse, error) {
	r.log("ingest")
	r.inputs = append(r.inputs, inputs...)
	r.deferred = append(r.deferred, deferMirrorPush)
	if r.answer != nil {
		return r.answer(inputs), nil
	}
	answers := make([]models.PublicAssignmentAuditProofPublishResponse, 0, len(inputs))
	for _, input := range inputs {
		answers = append(answers, models.PublicAssignmentAuditProofPublishResponse{Accepted: true, AssignmentID: input.AssignmentID, DatabaseSHA256: fakeGatewayDatabaseDigest(input.AssignmentID)})
	}
	return answers, nil
}

func (r *recordingCampaignProofPublisher) PruneRunProofCatalog(context.Context, string) error {
	return nil
}

func (r *recordingCampaignProofPublisher) FlushProofCatalog(context.Context) error {
	r.flushes++
	r.log("flush")
	return nil
}

func (r *recordingCampaignProofPublisher) log(event string) {
	if r.order != nil {
		*r.order = append(*r.order, event)
	}
}

// orderedCampaignFeedExporter logs each export into the shared order log, so a
// test can check the order of live events, proof ingest, and result records.
// A batch of only live event records logs "export-events"; any other batch
// logs "export".
type orderedCampaignFeedExporter struct {
	recordingCampaignFeedExporter
	order *[]string
}

func (e *orderedCampaignFeedExporter) ExportBatch(ctx context.Context, records []CampaignPublicFeedRecord) error {
	event := "export-events"
	for _, record := range records {
		if record.RecordType != models.PublicFeedRecordTypeEvent {
			event = "export"
			break
		}
	}
	*e.order = append(*e.order, event)
	return e.recordingCampaignFeedExporter.ExportBatch(ctx, records)
}

// digestlessCampaignSpecStore serves campaign specs with an empty revision digest.
// The store never persists such a spec, so the test wraps it.
type digestlessCampaignSpecStore struct {
	*Store
}

func (s digestlessCampaignSpecStore) LoadCampaignSpec(ctx context.Context, campaignID string) (*evalv1.EvaluationCampaignSpec, error) {
	spec, err := s.Store.LoadCampaignSpec(ctx, campaignID)
	if err != nil {
		return nil, err
	}
	spec.CampaignDigest = ""
	return spec, nil
}

// saveAuditProofAssignment saves one completed assignment with a result under
// campaign-1, and returns both. The result carries a valid result digest.
func saveAuditProofAssignment(t *testing.T, store *Store, runID, assignmentID string) (*evalv1.EvaluationAssignment, *evalv1.EvaluationAssignmentResult) {
	t.Helper()
	ctx := context.Background()
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
		CompletedAt:     timestamppb.Now(),
		ModelInferences: []*evalv1.ModelInferenceRecord{{
			ModelRole:         evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
			ModelVariant:      &evalv1.ModelVariant{VariantId: "qwen3-4b"},
			UsageAvailability: evalv1.EvaluationUsageAvailability_EVALUATION_USAGE_AVAILABILITY_REPORTED,
		}},
	}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest

	spec := testCampaignSpec()
	spec.CampaignId = "campaign-1"
	specDigest, err := ComputeCampaignSpecDigest(spec)
	require.NoError(t, err)
	spec.CampaignDigest = specDigest
	require.NoError(t, store.SaveCampaignSpec(ctx, spec))
	require.NoError(t, store.SaveRun(ctx, &evalv1.EvaluationRun{
		SchemaVersion:   CampaignSchemaVersion,
		RunId:           runID,
		CampaignBinding: &evalv1.ModelCampaignBinding{CampaignId: "campaign-1"},
	}))
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	require.NoError(t, store.SaveAssignmentResult(ctx, result))
	return assignment, result
}

// findExportedRecord returns the one exported record whose envelope carries the
// idempotency key.
func findExportedRecord(t *testing.T, records []CampaignPublicFeedRecord, idempotencyKey string) CampaignPublicFeedRecord {
	t.Helper()
	needle := `"idempotency_key":"` + idempotencyKey + `"`
	var found []CampaignPublicFeedRecord
	for _, record := range records {
		if strings.Contains(record.RecordBytes, needle) {
			found = append(found, record)
		}
	}
	require.Len(t, found, 1, "exactly one record carries %s", idempotencyKey)
	return found[0]
}

func TestCampaignPublicationCoordinator_IngestAssignmentAuditBindings(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	proofPublisher := &recordingCampaignProofPublisher{}
	publicationState := NewMemoryCampaignPublicationStateStore()
	coordinator := NewCampaignPublicationCoordinator(store, files, publicationState, &recordingCampaignFeedExporter{}).
		WithProofPublisher(proofPublisher)
	assignment, result := saveAuditProofAssignment(t, store, "run-audit-1", "assign-audit-1")
	liveRequests, err := coordinator.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
	require.NoError(t, err)
	require.NotEmpty(t, liveRequests)
	fixtures := testScenarioFixtures(t)

	bindings, err := coordinator.ingestAssignmentAuditBindings(ctx, assignment, result, fixtures, liveRequests)
	require.NoError(t, err)

	digest := fakeGatewayDatabaseDigest("assign-audit-1")
	require.Len(t, bindings, 3)
	assert.Equal(t, AssignmentAuditSliceKind, bindings[0].GetKind())
	assert.Equal(t, digest, bindings[0].GetSha256(), "the database binding carries the digest the gateway answered")
	assert.Equal(t, ScenarioInputFixtureKind, bindings[1].GetKind())
	assert.Equal(t, ScenarioGoldCriteriaKind, bindings[2].GetKind())

	require.Len(t, proofPublisher.inputs, 1)
	input := proofPublisher.inputs[0]
	spec, err := store.LoadCampaignSpec(ctx, "campaign-1")
	require.NoError(t, err)
	assert.Equal(t, spec.GetCampaignDigest(), input.CampaignRevision)
	assert.Equal(t, result.GetResultDigest(), input.IndexDigest)
	assert.Equal(t, "scenario-1", input.Artifacts.ScenarioID)
	assert.Equal(t, fixtures["scenario-1"].Input.Body, input.Artifacts.ScenarioInput)
	assert.Equal(t, fixtures["scenario-1"].Gold.Body, input.Artifacts.ScenarioGold)
	assert.Equal(t, []bool{false}, proofPublisher.deferred, "live publication pushes the mirror at once")

	state, err := coordinator.loadPublicationState(ctx, "run-audit-1")
	require.NoError(t, err)
	assert.Equal(t, CampaignPublishedProofArtifacts{
		DatabaseSHA256:      digest,
		ScenarioInputSHA256: input.Artifacts.ScenarioInputSHA256,
		ScenarioGoldSHA256:  input.Artifacts.ScenarioGoldSHA256,
	}, state.PublishedProofArtifacts["assign-audit-1"], "publication state records the gateway digest for reuse")
	assert.Contains(t, state.PublishedIdempotency, AssignmentAuditProofIdempotencyKey("run-audit-1", "assign-audit-1"))
}

func TestCampaignPublicationCoordinator_AuditBindingsWithoutProofPublisherBindNothing(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{})
	assignment, result := saveAuditProofAssignment(t, store, "run-no-publisher", "assign-no-publisher")
	liveRequests, err := coordinator.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
	require.NoError(t, err)

	bindings, err := coordinator.ingestAssignmentAuditBindings(ctx, assignment, result, testScenarioFixtures(t), liveRequests)

	require.NoError(t, err)
	assert.Nil(t, bindings, "without a proof publisher the gateway never answers a database digest, so no audit binding is cited")
}

func TestCampaignPublicationCoordinator_AuditProofInputRequiresRevisionAndIndexDigest(t *testing.T) {
	ctx := context.Background()

	t.Run("empty campaign revision", func(t *testing.T) {
		files := newCampaignRuntimeFileService(t)
		store := NewStore(files)
		assignment, result := saveAuditProofAssignment(t, store, "run-revision-1", "assign-revision-1")
		proofPublisher := &recordingCampaignProofPublisher{}
		coordinator := NewCampaignPublicationCoordinator(digestlessCampaignSpecStore{Store: store}, files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{}).
			WithProofPublisher(proofPublisher)
		liveRequests, err := coordinator.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
		require.NoError(t, err)

		_, ok, err := coordinator.assignmentAuditProofInput(ctx, assignment, result, testScenarioFixtures(t), liveRequests)

		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		assert.False(t, ok)
		assert.Empty(t, proofPublisher.inputs, "no request is sent without a revision")
	})

	t.Run("empty index digest", func(t *testing.T) {
		files := newCampaignRuntimeFileService(t)
		store := NewStore(files)
		assignment, result := saveAuditProofAssignment(t, store, "run-index-1", "assign-index-1")
		proofPublisher := &recordingCampaignProofPublisher{}
		coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{}).
			WithProofPublisher(proofPublisher)
		liveRequests, err := coordinator.buildAssignmentLiveEventPublishRequests(ctx, assignment, result)
		require.NoError(t, err)
		result.ResultDigest = ""

		_, ok, err := coordinator.assignmentAuditProofInput(ctx, assignment, result, testScenarioFixtures(t), liveRequests)

		require.ErrorIs(t, err, constants.ErrMissingRequiredField)
		assert.False(t, ok)
		assert.Empty(t, proofPublisher.inputs, "no request is sent without an index digest")
	})
}

func TestCampaignPublicationCoordinator_IngestAssignmentAuditBindings_EmptyLiveRequests(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	proofPublisher := &recordingCampaignProofPublisher{}
	coordinator := NewCampaignPublicationCoordinator(NewStore(files), files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{}).
		WithProofPublisher(proofPublisher)

	assignment := &evalv1.EvaluationAssignment{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    "assign-empty-1",
		RunId:           "run-empty-1",
		CampaignId:      "campaign-1",
		ScenarioId:      "scenario-1",
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
	}
	result := &evalv1.EvaluationAssignmentResult{
		SchemaVersion:   CampaignSchemaVersion,
		AssignmentId:    "assign-empty-1",
		RunId:           "run-empty-1",
		CampaignId:      "campaign-1",
		LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_FAILED,
	}

	bindings, err := coordinator.ingestAssignmentAuditBindings(context.Background(), assignment, result, testScenarioFixtures(t), nil)
	require.NoError(t, err)
	assert.Nil(t, bindings)
	assert.Empty(t, proofPublisher.inputs)
}

func TestHasAssignmentAuditEvents_SkipsProjections(t *testing.T) {
	assert.False(t, hasAssignmentAuditEvents([]campaignFeedPublishRequest{
		{RecordType: models.PublicFeedRecordTypeProjection, Body: []byte(`{"kind":"assignment_result"}`)},
	}), "an assignment with only projections has no audit events")
	assert.True(t, hasAssignmentAuditEvents([]campaignFeedPublishRequest{
		{RecordType: models.PublicFeedRecordTypeProjection, Body: []byte(`{"kind":"assignment_result"}`)},
		{RecordType: models.PublicFeedRecordTypeEvent, Body: []byte(`{"kind":"metric_updated","observed_at":"2026-09-17T00:00:00Z"}`)},
	}))
}

func TestCampaignPublicationCoordinator_DefersMirrorPushUntilFlush(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	proofPublisher := &recordingCampaignProofPublisher{}
	coordinator := NewCampaignPublicationCoordinator(NewStore(files), files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{}).
		WithProofPublisher(proofPublisher)
	coordinator.deferProofMirrorPush = true

	published, err := coordinator.ingestAssignmentAuditProofs(ctx, []AssignmentAuditProofInput{
		{RunID: "run-deferred", AssignmentID: "assignment-1"},
		{RunID: "run-deferred", AssignmentID: "assignment-2"},
	})
	require.NoError(t, err)
	require.Len(t, published, 2)
	assert.Equal(t, []bool{true}, proofPublisher.deferred, "one batch, with the mirror push deferred")
	assert.Equal(t, 0, proofPublisher.flushes, "nothing is pushed before the flush")

	require.NoError(t, coordinator.flushProofCatalog(ctx))
	assert.Equal(t, 1, proofPublisher.flushes)
	require.NoError(t, coordinator.flushProofCatalog(ctx))
	assert.Equal(t, 1, proofPublisher.flushes, "a flush with nothing deferred does not push again")
}

func TestIngestAssignmentAuditProofsRejectsMalformedAnswers(t *testing.T) {
	cases := []struct {
		name   string
		answer func(inputs []AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse
	}{
		{name: "no answer for the request", answer: func([]AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse {
			return nil
		}},
		{name: "answer for another assignment", answer: func(inputs []AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse {
			return []models.PublicAssignmentAuditProofPublishResponse{{Accepted: true, AssignmentID: "assignment-other", DatabaseSHA256: strings.Repeat("a", 64)}}
		}},
		{name: "answer without a database digest", answer: func(inputs []AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse {
			return []models.PublicAssignmentAuditProofPublishResponse{{Accepted: true, AssignmentID: inputs[0].AssignmentID}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			files := newCampaignRuntimeFileService(t)
			publicationState := NewMemoryCampaignPublicationStateStore()
			proofPublisher := &recordingCampaignProofPublisher{answer: tc.answer}
			coordinator := NewCampaignPublicationCoordinator(NewStore(files), files, publicationState, &recordingCampaignFeedExporter{}).
				WithProofPublisher(proofPublisher)

			_, err := coordinator.ingestAssignmentAuditProofs(ctx, []AssignmentAuditProofInput{{RunID: "run-malformed", AssignmentID: "assignment-1"}})

			require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
			state, err := publicationState.Load(ctx, "run-malformed")
			require.NoError(t, err)
			assert.Empty(t, state.PublishedProofArtifacts, "a malformed answer records nothing")
		})
	}
}

// The catch-up path exports the live event records the Gateway's proof requires,
// then ingests every needed proof in one request before it exports any result
// record, and each record binds the digest the Gateway answered.
func TestPublishRunCatchUpIngestsProofsBeforeExportingResults(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	var order []string
	exporter := &orderedCampaignFeedExporter{order: &order}
	proofPublisher := &recordingCampaignProofPublisher{order: &order}
	publicationState := NewMemoryCampaignPublicationStateStore()
	coordinator := NewCampaignPublicationCoordinator(store, files, publicationState, exporter).WithProofPublisher(proofPublisher)
	run := completedTestCampaign(t, store)

	_, err := coordinator.PublishRunCatchUp(ctx, run.GetRunId())
	require.NoError(t, err)

	require.NotEmpty(t, proofPublisher.inputs)
	ingest := slices.Index(order, "ingest")
	require.Positive(t, ingest, "live events are exported before the proofs are ingested")
	for _, event := range order[:ingest] {
		assert.Equal(t, "export-events", event, "only live events are exported before the proofs are ingested")
	}
	assert.Contains(t, order[ingest:], "export", "the result records are exported after the ingest")
	assert.Equal(t, 1, countEvents(order, "ingest"), "catch-up ingests in one batch")
	assert.Equal(t, 1, proofPublisher.flushes, "the mirror push is deferred to the end of catch-up")
	for _, deferred := range proofPublisher.deferred {
		assert.True(t, deferred, "catch-up defers the mirror push")
	}

	state, err := publicationState.Load(ctx, run.GetRunId())
	require.NoError(t, err)
	for _, input := range proofPublisher.inputs {
		digest := fakeGatewayDatabaseDigest(input.AssignmentID)
		assert.Equal(t, digest, state.PublishedProofArtifacts[input.AssignmentID].DatabaseSHA256)
		record := findExportedRecord(t, exporter.records, AssignmentResultIdempotencyKey(run.GetRunId(), input.AssignmentID))
		assert.Contains(t, record.RecordBytes, digest, "the result record binds the gateway digest")
	}
}

// The verified republish sends the same proof request the assignment was
// published with, and its records bind whatever digest the Gateway answers.
func TestPublishRunVerificationRepublishesProofAndBindsReturnedDigest(t *testing.T) {
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	var order []string
	exporter := &orderedCampaignFeedExporter{order: &order}
	proofPublisher := &recordingCampaignProofPublisher{order: &order}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofPublisher)
	run := completedTestCampaign(t, store)
	_, err := coordinator.PublishRunCatchUp(ctx, run.GetRunId())
	require.NoError(t, err)
	published := append([]AssignmentAuditProofInput(nil), proofPublisher.inputs...)
	require.NotEmpty(t, published)
	order = order[:0]
	proofPublisher.inputs = nil
	proofPublisher.deferred = nil

	// The Gateway's reused database answers with a digest distinct from the one
	// catch-up recorded, so a record citing the old digest is detectable.
	republishedDigest := func(assignmentID string) string {
		return hashBytes([]byte("gateway-reused-database:" + assignmentID))
	}
	proofPublisher.answer = func(inputs []AssignmentAuditProofInput) []models.PublicAssignmentAuditProofPublishResponse {
		answers := make([]models.PublicAssignmentAuditProofPublishResponse, 0, len(inputs))
		for _, input := range inputs {
			answers = append(answers, models.PublicAssignmentAuditProofPublishResponse{Accepted: true, AssignmentID: input.AssignmentID, DatabaseSHA256: republishedDigest(input.AssignmentID)})
		}
		return answers
	}
	report := bindPersistedVerificationReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
	exporterBefore := len(exporter.records)

	_, err = coordinator.PublishRunVerification(ctx, run.GetRunId(), report)
	require.NoError(t, err)

	require.Len(t, proofPublisher.inputs, len(published))
	for index, input := range proofPublisher.inputs {
		assert.Equal(t, published[index].CampaignRevision, input.CampaignRevision, "the republish sends the request it was published with")
		assert.Equal(t, published[index].IndexDigest, input.IndexDigest)
		assert.Equal(t, published[index].Artifacts, input.Artifacts)
	}
	assert.Equal(t, []bool{true}, proofPublisher.deferred, "the republish defers the mirror push")
	assert.Equal(t, "ingest", order[0], "the republish ingests before it exports the verified records")
	for _, input := range proofPublisher.inputs {
		record := findExportedRecord(t, exporter.records[exporterBefore:], AssignmentVerifiedResultIdempotencyKey(run.GetRunId(), input.AssignmentID))
		assert.Contains(t, record.RecordBytes, republishedDigest(input.AssignmentID), "the verified record binds the digest the gateway returned")
		assert.NotContains(t, record.RecordBytes, fakeGatewayDatabaseDigest(input.AssignmentID), "the earlier digest is not reused")
	}
}

func countEvents(events []string, event string) int {
	count := 0
	for _, got := range events {
		if got == event {
			count++
		}
	}
	return count
}

// saveAgentTraceForAuditProof seals and persists one trace and returns the
// result that references it alongside the exact persisted body.
func saveAgentTraceForAuditProof(t *testing.T, store *Store) (*evalv1.EvaluationAssignmentResult, []byte) {
	t.Helper()
	trace := &AgentTrace{RunID: "run-1", AssignmentID: "assignment-1", EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."}
	body, err := SealAgentTrace(trace)
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(context.Background(), "run-1", "assignment-1", body))
	ref, err := BuildAssignmentAgentTraceEvidenceReference("run-1", "attempt-1", body, time.Now().UTC())
	require.NoError(t, err)
	return &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1", EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{ref}}, body
}

func TestAssignmentAgentTraceArtifact_PublishesExactPersistedBodyAndBindsIt(t *testing.T) {
	store := NewStore(newCampaignRuntimeFileService(t))
	result, persisted := saveAgentTraceForAuditProof(t, store)

	body, digest, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)

	require.NoError(t, err)
	assert.Equal(t, persisted, body, "the published artifact is the sealed body byte for byte")
	assert.Equal(t, result.GetEvidenceRefs()[0].GetSha256(), digest)
	bindings := AssignmentAuditEvidenceBindingsFromHashes(CampaignPublishedProofArtifacts{DatabaseSHA256: strings.Repeat("a", 64), AgentTraceSHA256: digest})
	require.Len(t, bindings, 2)
	assert.Equal(t, AssignmentAgentTraceKind, bindings[1].GetKind())
	assert.Len(t, AssignmentAuditEvidenceBindingsFromHashes(CampaignPublishedProofArtifacts{DatabaseSHA256: strings.Repeat("a", 64)}), 1, "no trace, no trace binding")
}

// testScenarioFixtures returns one frozen built-in scenario's fixture bodies
// keyed under "scenario-1".
func testScenarioFixtures(t *testing.T) map[string]ScenarioArtifacts {
	t.Helper()
	catalog, artifacts, err := BuildSmokeGateScenarioCatalog()
	require.NoError(t, err)
	require.NotEmpty(t, catalog.GetScenarios())
	return map[string]ScenarioArtifacts{"scenario-1": artifacts[catalog.GetScenarios()[0].GetScenarioId()]}
}

func TestAttachScenarioFixture_PublishesFrozenBodiesAndBindsThem(t *testing.T) {
	fixtures := testScenarioFixtures(t)
	var artifacts AssignmentAuditProofArtifacts

	require.NoError(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-1"}, fixtures))

	assert.Equal(t, "scenario-1", artifacts.ScenarioID)
	assert.Equal(t, fixtures["scenario-1"].Input.Body, artifacts.ScenarioInput, "input fixture is published byte for byte")
	assert.Equal(t, fixtures["scenario-1"].Gold.Body, artifacts.ScenarioGold, "gold criteria are published byte for byte")
	assert.Equal(t, hashBytes(artifacts.ScenarioInput), artifacts.ScenarioInputSHA256)
	assert.Equal(t, hashBytes(artifacts.ScenarioGold), artifacts.ScenarioGoldSHA256)
	bindings := AssignmentAuditEvidenceBindingsFromHashes(CampaignPublishedProofArtifacts{
		DatabaseSHA256:      strings.Repeat("a", 64),
		ScenarioInputSHA256: artifacts.ScenarioInputSHA256,
		ScenarioGoldSHA256:  artifacts.ScenarioGoldSHA256,
	})
	require.Len(t, bindings, 3)
	assert.Equal(t, ScenarioInputFixtureKind, bindings[1].GetKind())
	assert.Equal(t, artifacts.ScenarioInputSHA256, bindings[1].GetSha256())
	assert.Equal(t, ScenarioGoldCriteriaKind, bindings[2].GetKind())
	assert.Equal(t, artifacts.ScenarioGoldSHA256, bindings[2].GetSha256())
	_, publicBindings, err := BuildPublicAssignmentEvidence(PublicAssignmentEvidenceInput{Result: &evalv1.EvaluationAssignmentResult{}, PublicProofs: bindings})
	require.NoError(t, err)
	assert.Len(t, publicBindings, 3)
}

func TestAttachScenarioFixture_RejectsMissingOrTamperedFixture(t *testing.T) {
	fixtures := testScenarioFixtures(t)
	var artifacts AssignmentAuditProofArtifacts
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-unknown"}, fixtures), constants.ErrEvidenceArtifactMalformed)

	tampered := fixtures["scenario-1"]
	tampered.Gold.Body = append(append([]byte(nil), tampered.Gold.Body...), ' ')
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-1"}, map[string]ScenarioArtifacts{"scenario-1": tampered}), constants.ErrEvidenceArtifactMalformed)
	assert.Empty(t, artifacts.ScenarioInput, "a rejected fixture attaches nothing")
}

func TestAssignmentAgentTraceArtifact_AbsentTraceWithoutReferencePublishesNothing(t *testing.T) {
	store := NewStore(newCampaignRuntimeFileService(t))

	body, digest, err := loadAssignmentAgentTraceArtifact(context.Background(), store, &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1"})

	require.NoError(t, err)
	assert.Nil(t, body)
	assert.Empty(t, digest)
}

func TestAssignmentAgentTraceArtifact_FailsClosed(t *testing.T) {
	t.Run("referenced trace missing", func(t *testing.T) {
		store := NewStore(newCampaignRuntimeFileService(t))
		result := &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1", EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{{ArtifactType: string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace), Sha256: strings.Repeat("c", 64)}}}
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrNotFound)
	})
	t.Run("reference hash differs from persisted body", func(t *testing.T) {
		store := NewStore(newCampaignRuntimeFileService(t))
		result, _ := saveAgentTraceForAuditProof(t, store)
		result.EvidenceRefs[0].Sha256 = strings.Repeat("c", 64)
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	})
}

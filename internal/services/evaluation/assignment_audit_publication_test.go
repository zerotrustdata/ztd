// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
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
// answers each ingested input with the Gateway's database digest and records the
// inputs it receives.
type recordingCampaignProofPublisher struct {
	inputs []AssignmentAuditProofInput
}

func (r *recordingCampaignProofPublisher) IngestAssignmentAuditSlices(_ context.Context, inputs []AssignmentAuditProofInput, _ bool) ([]models.PublicAssignmentAuditProofPublishResponse, error) {
	r.inputs = append(r.inputs, inputs...)
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
	return nil
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

func TestCampaignPublicationCoordinator_AuditBindingsWithoutProofPublisherBindNothing(t *testing.T) {
	t.Parallel()
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

func TestCampaignPublicationCoordinator_IngestAssignmentAuditBindings_EmptyLiveRequests(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	assert.False(t, hasAssignmentAuditEvents([]campaignFeedPublishRequest{
		{RecordType: models.PublicFeedRecordTypeProjection, Body: []byte(`{"kind":"assignment_result"}`)},
	}), "an assignment with only projections has no audit events")
	assert.True(t, hasAssignmentAuditEvents([]campaignFeedPublishRequest{
		{RecordType: models.PublicFeedRecordTypeProjection, Body: []byte(`{"kind":"assignment_result"}`)},
		{RecordType: models.PublicFeedRecordTypeEvent, Body: []byte(`{"kind":"metric_updated","observed_at":"2026-09-17T00:00:00Z"}`)},
	}))
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
	t.Parallel()
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

func TestAttachScenarioFixture_PublishesFrozenBodiesAndBindsThem(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	fixtures := testScenarioFixtures(t)
	var artifacts AssignmentAuditProofArtifacts
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-unknown"}, fixtures), constants.ErrEvidenceArtifactMalformed)

	tampered := fixtures["scenario-1"]
	tampered.Gold.Body = append(append([]byte(nil), tampered.Gold.Body...), ' ')
	require.ErrorIs(t, artifacts.attachScenarioFixture(&evalv1.EvaluationAssignment{ScenarioId: "scenario-1"}, map[string]ScenarioArtifacts{"scenario-1": tampered}), constants.ErrEvidenceArtifactMalformed)
	assert.Empty(t, artifacts.ScenarioInput, "a rejected fixture attaches nothing")
}

func TestAssignmentAgentTraceArtifact_AbsentTraceWithoutReferencePublishesNothing(t *testing.T) {
	t.Parallel()
	store := NewStore(newCampaignRuntimeFileService(t))

	body, digest, err := loadAssignmentAgentTraceArtifact(context.Background(), store, &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1"})

	require.NoError(t, err)
	assert.Nil(t, body)
	assert.Empty(t, digest)
}

func TestAssignmentAgentTraceArtifact_FailsClosed(t *testing.T) {
	t.Parallel()
	t.Run("referenced trace missing", func(t *testing.T) {
		t.Parallel()
		store := NewStore(newCampaignRuntimeFileService(t))
		result := &evalv1.EvaluationAssignmentResult{RunId: "run-1", AssignmentId: "assignment-1", EvidenceRefs: []*compliancev1.ComplianceEvidenceReference{{ArtifactType: string(complianceevidence.ArtifactTypeEvaluationAssignmentTrace), Sha256: strings.Repeat("c", 64)}}}
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrNotFound)
	})
	t.Run("reference hash differs from persisted body", func(t *testing.T) {
		t.Parallel()
		store := NewStore(newCampaignRuntimeFileService(t))
		result, _ := saveAgentTraceForAuditProof(t, store)
		result.EvidenceRefs[0].Sha256 = strings.Repeat("c", 64)
		_, _, err := loadAssignmentAgentTraceArtifact(context.Background(), store, result)
		assert.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
	})
}

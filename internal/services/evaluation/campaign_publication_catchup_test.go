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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// batchRecordingCampaignFeedExporter keeps each ExportBatch call as its own
// batch so a test can assert which records one export carried.
type batchRecordingCampaignFeedExporter struct {
	exported int64
	batches  [][]CampaignPublicFeedRecord
}

func (e *batchRecordingCampaignFeedExporter) HighWaterSequence(context.Context) (int64, error) {
	return e.exported, nil
}

func (e *batchRecordingCampaignFeedExporter) ExportBatch(_ context.Context, records []CampaignPublicFeedRecord) error {
	e.exported += int64(len(records))
	e.batches = append(e.batches, append([]CampaignPublicFeedRecord(nil), records...))
	return nil
}

// messageTypeCounts returns how many records of each projection message type a
// batch carries.
func messageTypeCounts(t *testing.T, batch []CampaignPublicFeedRecord) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, record := range batch {
		var envelope CampaignProjectionEnvelope
		require.NoError(t, json.Unmarshal([]byte(record.RecordBytes), &envelope))
		counts[envelope.MessageType]++
	}
	return counts
}

// Catch-up ships every assignment lifecycle and terminal result of a run in one
// export batch, so the feed does not pay one export per record.
func TestPublishRunCatchUpExportsLifecyclesAndResultsInOneBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	controller, req, run := scheduledTestCampaign(t, store)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Greater(t, len(assignments), 1)
	executeTestAssignments(t, controller, req, run.GetRunId(), len(assignments))

	exporter := &batchRecordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	published, err := coordinator.PublishRunCatchUp(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Positive(t, published)

	var lifecycleBatch []CampaignPublicFeedRecord
	for _, batch := range exporter.batches {
		if messageTypeCounts(t, batch)[publicMessageTypeAssignmentLifecycle] > 0 {
			lifecycleBatch = batch
			break
		}
	}
	require.NotNil(t, lifecycleBatch, "catch-up exported no assignment lifecycle batch")
	assert.Equal(t, map[string]int{
		publicMessageTypeAssignmentLifecycle: len(assignments),
		publicMessageTypeAssignmentResult:    len(assignments),
	}, messageTypeCounts(t, lifecycleBatch), "every lifecycle and result of the run must share one batch")
}

// recordedProofArtifacts returns proof idempotency keys and recorded hashes for
// every assignment as if its package, including the campaign's frozen scenario
// fixture, had already been published.
func recordedProofArtifacts(t *testing.T, store *Store, run *evalv1.EvaluationRun, assignments []*evalv1.EvaluationAssignment) ([]string, map[string]CampaignPublishedProofArtifacts) {
	t.Helper()
	ctx := context.Background()
	campaignID := run.GetCampaignBinding().GetCampaignId()
	catalog, err := store.LoadScenarioCatalog(ctx, campaignID)
	require.NoError(t, err)
	fixtures, err := store.LoadScenarioArtifacts(ctx, campaignID, catalog)
	require.NoError(t, err)
	proofKeys := make([]string, 0, len(assignments))
	proofArtifacts := make(map[string]CampaignPublishedProofArtifacts, len(assignments))
	for index, assignment := range assignments {
		fixture, ok := fixtures[assignment.GetScenarioId()]
		require.True(t, ok, "scenario fixture for %s", assignment.GetScenarioId())
		proofKeys = append(proofKeys, AssignmentAuditProofIdempotencyKey(run.GetRunId(), assignment.GetAssignmentId()))
		proofArtifacts[assignment.GetAssignmentId()] = CampaignPublishedProofArtifacts{
			DatabaseSHA256:      fmt.Sprintf("%064x", index*2),
			ScenarioInputSHA256: fixture.Input.Reference.GetSha256(),
			ScenarioGoldSHA256:  fixture.Gold.Reference.GetSha256(),
		}
	}
	return proofKeys, proofArtifacts
}

// A package whose database, agent trace, and scenario fixture hashes are all
// recorded is reused: catch-up asks the Gateway for nothing.
func TestPublishRunCatchUpSkipsProofRebuildWhenHashesRecorded(t *testing.T) {
	t.Parallel()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	proofPublisher := &recordingCampaignProofPublisher{}
	publicationState := NewMemoryCampaignPublicationStateStore()
	coordinator := NewCampaignPublicationCoordinator(store, files, publicationState, exporter).
		WithProofPublisher(proofPublisher)
	run := completedTestCampaign(t, store)
	assignments, err := store.ListAssignments(context.Background(), run.GetRunId())
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	proofKeys, proofArtifacts := recordedProofArtifacts(t, store, run, assignments)
	require.NoError(t, publicationState.Save(context.Background(), &CampaignPublicationState{
		SchemaVersion:           campaignPublicationStateSchemaVersion,
		RunID:                   run.GetRunId(),
		PublishedIdempotency:    proofKeys,
		PublishedProofArtifacts: proofArtifacts,
	}))
	require.NoError(t, coordinator.ResetFeedPublicationIdempotency(context.Background(), run.GetRunId()))

	_, err = coordinator.PublishRunCatchUp(context.Background(), run.GetRunId())
	require.NoError(t, err)
	assert.Empty(t, proofPublisher.inputs)
}

// A package published before its agent trace was captured, or bound to a
// different trace, is rebuilt so the current trace is published and bound.
func TestPublishRunCatchUpRebuildsProofWhenAgentTraceIsUnpublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	proofPublisher := &recordingCampaignProofPublisher{}
	publicationState := NewMemoryCampaignPublicationStateStore()
	coordinator := NewCampaignPublicationCoordinator(store, files, publicationState, &recordingCampaignFeedExporter{}).
		WithProofPublisher(proofPublisher)
	run := completedTestCampaign(t, store)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	proofKeys, proofArtifacts := recordedProofArtifacts(t, store, run, assignments)
	traced := assignments[0]
	traceBody, err := SealAgentTrace(&AgentTrace{RunID: run.GetRunId(), AssignmentID: traced.GetAssignmentId(), EvaluationAttemptID: "attempt-1", DesignatedRole: "primary", Input: testAgentTraceInput(), FinalOutput: "Checkout fails on payment timeouts."})
	require.NoError(t, err)
	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, run.GetRunId(), traced.GetAssignmentId(), traceBody))
	require.NoError(t, publicationState.Save(ctx, &CampaignPublicationState{
		SchemaVersion:           campaignPublicationStateSchemaVersion,
		RunID:                   run.GetRunId(),
		PublishedIdempotency:    proofKeys,
		PublishedProofArtifacts: proofArtifacts,
	}))
	require.NoError(t, coordinator.ResetFeedPublicationIdempotency(ctx, run.GetRunId()))

	_, err = coordinator.PublishRunCatchUp(ctx, run.GetRunId())

	require.NoError(t, err)
	require.Len(t, proofPublisher.inputs, 1, "only the assignment whose trace is unpublished is rebuilt")
	rebuilt := proofPublisher.inputs[0]
	assert.Equal(t, traced.GetAssignmentId(), rebuilt.AssignmentID)
	assert.Equal(t, traceBody, rebuilt.Artifacts.AgentTrace)
	assert.Equal(t, hashBytes(traceBody), rebuilt.Artifacts.AgentTraceSHA256)
	state, err := publicationState.Load(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.Equal(t, hashBytes(traceBody), state.PublishedProofArtifacts[traced.GetAssignmentId()].AgentTraceSHA256)
}

// A package recorded before scenario fixtures were published is rebuilt so the
// frozen fixture bodies are published and bound.
func TestPublishRunCatchUpRebuildsProofWhenScenarioFixtureIsUnpublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	proofPublisher := &recordingCampaignProofPublisher{}
	publicationState := NewMemoryCampaignPublicationStateStore()
	coordinator := NewCampaignPublicationCoordinator(store, files, publicationState, &recordingCampaignFeedExporter{}).
		WithProofPublisher(proofPublisher)
	run := completedTestCampaign(t, store)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	proofKeys, proofArtifacts := recordedProofArtifacts(t, store, run, assignments)
	stale := assignments[0]
	staleHashes := proofArtifacts[stale.GetAssignmentId()]
	staleHashes.ScenarioInputSHA256 = ""
	staleHashes.ScenarioGoldSHA256 = ""
	proofArtifacts[stale.GetAssignmentId()] = staleHashes
	require.NoError(t, publicationState.Save(ctx, &CampaignPublicationState{
		SchemaVersion:           campaignPublicationStateSchemaVersion,
		RunID:                   run.GetRunId(),
		PublishedIdempotency:    proofKeys,
		PublishedProofArtifacts: proofArtifacts,
	}))
	require.NoError(t, coordinator.ResetFeedPublicationIdempotency(ctx, run.GetRunId()))

	_, err = coordinator.PublishRunCatchUp(ctx, run.GetRunId())

	require.NoError(t, err)
	require.Len(t, proofPublisher.inputs, 1, "only the assignment without published fixtures is rebuilt")
	rebuilt := proofPublisher.inputs[0]
	assert.Equal(t, stale.GetAssignmentId(), rebuilt.AssignmentID)
	assert.Equal(t, stale.GetScenarioId(), rebuilt.Artifacts.ScenarioID)
	assert.NotEmpty(t, rebuilt.Artifacts.ScenarioInput)
	assert.NotEmpty(t, rebuilt.Artifacts.ScenarioGold)
	state, err := publicationState.Load(ctx, run.GetRunId())
	require.NoError(t, err)
	assert.Equal(t, rebuilt.Artifacts.ScenarioInputSHA256, state.PublishedProofArtifacts[stale.GetAssignmentId()].ScenarioInputSHA256)
	assert.Equal(t, rebuilt.Artifacts.ScenarioGoldSHA256, state.PublishedProofArtifacts[stale.GetAssignmentId()].ScenarioGoldSHA256)
}

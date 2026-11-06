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

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// orderedBundleExporter records each export batch as "results" or "summary" in
// one shared log, so a test can order exports against PublishRunBundle calls.
type orderedBundleExporter struct {
	records []CampaignPublicFeedRecord
	log     []string
}

func (e *orderedBundleExporter) HighWaterSequence(context.Context) (int64, error) {
	return int64(len(e.records)), nil
}

func (e *orderedBundleExporter) ExportBatch(_ context.Context, records []CampaignPublicFeedRecord) error {
	e.records = append(e.records, records...)
	for _, record := range records {
		var kind struct {
			Kind        string `json:"kind"`
			MessageType string `json:"message_type"`
		}
		if err := json.Unmarshal([]byte(record.RecordBytes), &kind); err != nil {
			return err
		}
		if kind.MessageType == publicMessageTypeAssignmentResult {
			e.log = append(e.log, "results")
		}
		if kind.Kind == "evaluation_summary" {
			e.log = append(e.log, "summary")
		}
	}
	return nil
}

// collapseExportLog merges consecutive equal entries, so one export split into
// several batches reads as one step.
func collapseExportLog(log []string) []string {
	collapsed := make([]string, 0, len(log))
	for _, entry := range log {
		if len(collapsed) > 0 && collapsed[len(collapsed)-1] == entry {
			continue
		}
		collapsed = append(collapsed, entry)
	}
	return collapsed
}

// specDigest returns the campaign spec digest the run was published under.
func specDigest(t *testing.T, store *Store, run *evalv1.EvaluationRun) string {
	t.Helper()
	spec, err := store.LoadCampaignSpec(context.Background(), run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	require.NotEmpty(t, spec.GetCampaignDigest())
	return spec.GetCampaignDigest()
}

// lastSummaryDigest returns the assignment bundle digest the latest exported
// evaluation_summary names.
func lastSummaryDigest(t *testing.T, records []CampaignPublicFeedRecord) string {
	t.Helper()
	summaries := evaluationSummaries(t, records)
	require.NotEmpty(t, summaries)
	return summaries[len(summaries)-1].AssignmentBundleSHA256
}

func TestPublishRunCompletionSummaryNamesAssignmentBundleDigest(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	proofs := &recordingCampaignProofPublisher{bundleDigest: "bundle-digest-completion"}
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofs)

	_, err := coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_100, 0).UTC())
	require.NoError(t, err)

	require.Len(t, proofs.bundleInputs, 1)
	assert.Equal(t, RunBundleInput{CampaignID: run.GetCampaignBinding().GetCampaignId(), CampaignRevision: specDigest(t, store, run), RunID: run.GetRunId()}, proofs.bundleInputs[0])
	assert.Equal(t, "bundle-digest-completion", lastSummaryDigest(t, exporter.records))
}

func TestPublishRunCatchUpExportsResultsThenBundleThenSummary(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	exporter := &orderedBundleExporter{}
	proofs := &recordingCampaignProofPublisher{
		bundleDigest:       "bundle-digest-catchup",
		onPublishRunBundle: func(RunBundleInput) { exporter.log = append(exporter.log, "bundle") },
	}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofs)

	_, err := coordinator.PublishRunCatchUp(context.Background(), run.GetRunId())
	require.NoError(t, err)

	// Aggregates and completion each fetch the digest, so the bundle sits
	// between the result export and each summary export it names.
	assert.Equal(t, []string{"results", "bundle", "summary", "bundle", "summary"}, collapseExportLog(exporter.log))
	assert.Equal(t, "bundle-digest-catchup", lastSummaryDigest(t, exporter.records))
}

func TestPublishRunAggregatesOnUnsettledRunOmitsAssignmentBundle(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	controller, req, run := scheduledTestCampaign(t, store)
	executeTestAssignments(t, controller, req, run.GetRunId(), 1)
	proofs := &recordingCampaignProofPublisher{bundleDigest: "bundle-digest-unsettled"}
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofs)

	_, err := coordinator.PublishRunAggregates(context.Background(), run.GetRunId(), time.Unix(1_700_000_200, 0).UTC())
	require.NoError(t, err)

	assert.Empty(t, proofs.bundleInputs)
	assert.Empty(t, lastSummaryDigest(t, exporter.records))
}

func TestPublishRunCompletionWithoutProofPublisherOmitsAssignmentBundleField(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)

	_, err := coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_100, 0).UTC())
	require.NoError(t, err)

	summaries := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, summaries)
	assert.Empty(t, summaries[len(summaries)-1].AssignmentBundleSHA256)
	for _, record := range exporter.records {
		var kind struct {
			Kind string `json:"kind"`
		}
		require.NoError(t, json.Unmarshal([]byte(record.RecordBytes), &kind))
		if kind.Kind == "evaluation_summary" {
			assert.NotContains(t, record.RecordBytes, "assignment_bundle_sha256")
		}
	}
}

func TestPublishRunCompletionFailsWithoutSummaryWhenBundleUnavailable(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	proofs := &recordingCampaignProofPublisher{bundleErr: constants.ErrGatewayNotReachable}
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofs)

	_, err := coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_100, 0).UTC())
	require.ErrorIs(t, err, constants.ErrGatewayNotReachable)
	assert.Empty(t, evaluationSummaries(t, exporter.records))
}

func TestPublishRunVerificationOutcomeChangeBundlesAfterRevisedRows(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	exporter := &orderedBundleExporter{}
	proofs := &recordingCampaignProofPublisher{
		bundleDigest:       "bundle-digest-verification",
		onPublishRunBundle: func(RunBundleInput) { exporter.log = append(exporter.log, "bundle") },
	}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter).WithProofPublisher(proofs)
	_, err := coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_100, 0).UTC())
	require.NoError(t, err)

	passReport := bindPersistedVerificationReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
	_, err = coordinator.PublishRunVerification(context.Background(), run.GetRunId(), passReport)
	require.NoError(t, err)

	// A failing report supersedes the published pass, so the assignment rows are
	// revised before the summary that names the refreshed bundle.
	exporter.log = nil
	// The fixture gives every report one digest, and the summary key is
	// report-scoped, so the failing report needs its own digest to export.
	failReport := buildBoundTestReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL)
	failReport.GetReportDigestRef().Sha256 = strings.Repeat("b", 64)
	require.NoError(t, store.SaveCampaignVerification(context.Background(), run.GetRunId(), failReport))
	_, err = coordinator.PublishRunVerification(context.Background(), run.GetRunId(), failReport)
	require.NoError(t, err)

	assert.Equal(t, []string{"results", "bundle", "summary"}, collapseExportLog(exporter.log))
	summaries := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, summaries)
	latest := summaries[len(summaries)-1]
	assert.Equal(t, "failed", latest.VerifierState)
	assert.Equal(t, "bundle-digest-verification", latest.AssignmentBundleSHA256)
}

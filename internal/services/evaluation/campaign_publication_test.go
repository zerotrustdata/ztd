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
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

type recordingCampaignFeedExporter struct {
	records    []CampaignPublicFeedRecord
	batchSizes []int
}

func (r *recordingCampaignFeedExporter) HighWaterSequence(context.Context) (int64, error) {
	return int64(len(r.records)), nil
}

func (r *recordingCampaignFeedExporter) ExportBatch(_ context.Context, records []CampaignPublicFeedRecord) error {
	r.batchSizes = append(r.batchSizes, len(records))
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
	require.Greater(t, len(exporter.records), 1)
	require.Len(t, evaluationSummaries(t, exporter.records), 1, "result publication also carries canonical run values")
	require.Equal(t, []int{len(exporter.records)}, exporter.batchSizes, "result and run summary share one signed batch")

	envelope := CampaignProjectionEnvelope{}
	require.NoError(t, json.Unmarshal([]byte(exporter.records[0].RecordBytes), &envelope))
	record := publicAssignmentRecordEnvelope{}
	require.NoError(t, json.Unmarshal(envelope.Record, &record))
	require.NotNil(t, record.BenchmarkObservations)
	require.NotNil(t, record.BenchmarkObservations.GPU)
	require.NotNil(t, record.BenchmarkObservations.GPU.VRAMPeakBytes)
	require.NotNil(t, record.BenchmarkObservations.GPU.VRAMPeakBytes.Value)
	assert.InDelta(t, float64(16_000_000_000), *record.BenchmarkObservations.GPU.VRAMPeakBytes.Value, 0)
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
	require.Greater(t, len(exporter.records), 1)
	require.Len(t, evaluationSummaries(t, exporter.records), 1, "failed assignment publication refreshes the run summary")
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

// scheduledTestCampaign initializes a campaign over its first three scenarios
// and schedules its run. Callers execute assignments on the returned controller.
func scheduledTestCampaign(t *testing.T, store *Store) (*CampaignController, CampaignInitRequest, *evalv1.EvaluationRun) {
	t.Helper()
	ctx := context.Background()
	controller := NewCampaignController(store, &stubCampaignExecutor{}, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	catalog := req.Catalog
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Personas:      catalog.GetPersonas(),
		Scenarios:     catalog.GetScenarios()[:3],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, run.GetRunId())
	require.NoError(t, err)
	return controller, req, run
}

// executeTestAssignments executes count queued assignments of runID.
func executeTestAssignments(t *testing.T, controller *CampaignController, req CampaignInitRequest, runID string, count int) {
	t.Helper()
	binding := CampaignExecutionBinding{
		Topology:            testOperatorTopology(req.InferenceOperatorSessionID, req.DataOperatorSessionID),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       InferenceVariantsFromEvalRegistry(req.Inventory.Variants),
	}
	for range count {
		_, ok, err := controller.ExecuteNextAssignment(context.Background(), runID, binding, req.ScenarioArtifacts)
		require.NoError(t, err)
		require.True(t, ok)
	}
}

// completedTestCampaign executes every assignment of a scheduled campaign over
// its first three scenarios, so the run is settled.
func completedTestCampaign(t *testing.T, store *Store) *evalv1.EvaluationRun {
	t.Helper()
	controller, req, run := scheduledTestCampaign(t, store)
	for {
		summary, err := controller.RunSummary(context.Background(), run.GetRunId())
		require.NoError(t, err)
		if summary.QueuedCount == 0 && summary.RunningCount == 0 {
			return run
		}
		executeTestAssignments(t, controller, req, run.GetRunId(), 1)
	}
}

// buildBoundTestReport returns a bound verification report for the run with
// digests computed from the persisted population. Callers persist it through
// store.SaveCampaignVerification.
func buildBoundTestReport(t *testing.T, store *Store, run *evalv1.EvaluationRun, status evalv1.EvaluationVerdictStatus) *evalv1.EvaluationVerificationReport {
	t.Helper()
	ctx := context.Background()
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	results := make(map[string]*evalv1.EvaluationAssignmentResult, len(assignments))
	for _, assignment := range assignments {
		exists, err := store.AssignmentResultExists(ctx, run.GetRunId(), assignment.GetAssignmentId())
		require.NoError(t, err)
		if !exists {
			continue
		}
		result, err := store.LoadAssignmentResult(ctx, run.GetRunId(), assignment.GetAssignmentId())
		require.NoError(t, err)
		results[assignment.GetAssignmentId()] = result
	}
	spec, err := store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	catalog, err := store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	report := &evalv1.EvaluationVerificationReport{
		SchemaVersion:           campaignVerificationSchemaVersion,
		ReportId:                run.GetRunId(),
		RunId:                   run.GetRunId(),
		Status:                  status,
		VerifiedAt:              timestamppb.New(time.Unix(1_700_000_200, 0).UTC()),
		VerifierReleaseVersion:  "test-release",
		VerifierContractVersion: constants.CampaignVerifierVersion,
		ReportDigestRef:         &compliancev1.ComplianceEvidenceReference{Sha256: strings.Repeat("a", 64)},
	}
	applicability, err := BuildRunVerificationApplicability(run, spec, catalog, assignments, results, uint32(len(assignments)), report)
	require.NoError(t, err)
	populationDigest, err := digestProto(applicability.Population)
	require.NoError(t, err)
	report.VerifiedPopulationDigest = populationDigest
	report.ExpectedAssignmentCount = applicability.ExpectedAssignmentCount
	report.VerifiedAssignmentCount = applicability.VerifiedAssignmentCount
	report.CampaignDigest = spec.GetCampaignDigest()
	report.CatalogDigest = spec.GetCatalogDigest()
	report.ModelRegistryDigest = spec.GetModelRegistryDigest()
	return report
}

func bindPersistedVerificationReport(t *testing.T, store *Store, run *evalv1.EvaluationRun, status evalv1.EvaluationVerdictStatus) *evalv1.EvaluationVerificationReport {
	t.Helper()
	report := buildBoundTestReport(t, store, run, status)
	require.NoError(t, store.SaveCampaignVerification(context.Background(), run.GetRunId(), report))
	return report
}

func evaluationSummaries(t *testing.T, records []CampaignPublicFeedRecord) []evaluationSummaryRecord {
	t.Helper()
	summaries := make([]evaluationSummaryRecord, 0)
	for _, record := range records {
		var payload evaluationSummaryRecord
		require.NoError(t, json.Unmarshal([]byte(record.RecordBytes), &payload))
		if payload.Kind == "evaluation_summary" {
			summaries = append(summaries, payload)
		}
	}
	return summaries
}

// Regression: a published applicable verification result must remain
// authoritative through later aggregate, completion, and catch-up emissions.
// Before the fix, later aggregate revisions re-emitted verifier_state
// not_applicable and erased the published result.
func TestPublishRunCatchUpPreservesBoundVerificationState(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	run := completedTestCampaign(t, store)

	count, err := coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_100, 0).UTC())
	require.NoError(t, err)
	require.Positive(t, count)
	preVerify := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, preVerify)
	assert.Equal(t, "not_run", preVerify[len(preVerify)-1].VerifierState)

	report := bindPersistedVerificationReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
	_, err = coordinator.PublishRunVerification(context.Background(), run.GetRunId(), report)
	require.NoError(t, err)

	require.NoError(t, coordinator.ResetPublicationIdempotency(context.Background(), run.GetRunId()))
	_, err = coordinator.PublishRunCatchUp(context.Background(), run.GetRunId())
	require.NoError(t, err)

	summaries := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, summaries)
	latest := summaries[len(summaries)-1]
	assert.Equal(t, "passed", latest.VerifierState)
	require.NotNil(t, latest.VerificationMetadata)
	assert.Equal(t, "bound", latest.VerificationMetadata.Provenance)
	assert.Equal(t, report.GetReportDigestRef().GetSha256(), latest.VerificationMetadata.ReportDigest)
	assert.Equal(t, report.GetVerifiedPopulationDigest(), latest.VerificationMetadata.PopulationDigest)
}

// Regression: partial campaign verify on an in-progress run must not block
// later execute/publish when additional assignments settle and the bound
// population no longer matches the persisted report.
func TestPublishRunAggregatesIgnoresStalePartialVerificationOnInProgressRun(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	controller, req, run := scheduledTestCampaign(t, store)
	assignments, err := store.ListAssignments(context.Background(), run.GetRunId())
	require.NoError(t, err)
	require.Greater(t, len(assignments), 2, "the run must still have queued assignments after the partial verify")

	executeTestAssignments(t, controller, req, run.GetRunId(), 1)
	bindPersistedVerificationReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
	executeTestAssignments(t, controller, req, run.GetRunId(), 1)

	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	_, err = coordinator.PublishRunAggregates(context.Background(), run.GetRunId(), time.Unix(1_700_000_300, 0).UTC())
	require.NoError(t, err)

	summaries := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, summaries)
	assert.Equal(t, "not_run", summaries[len(summaries)-1].VerifierState)
}

// Regression: a persisted report whose population binding no longer matches the
// settled run's evidence fails closed on every publication path instead of
// publishing a downgrade.
func TestPublishRunAggregatesFailClosedOnMismatchedPersistedReport(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	run := completedTestCampaign(t, store)

	report := buildBoundTestReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL)
	report.VerifiedPopulationDigest = strings.Repeat("9", 64)
	require.NoError(t, store.SaveCampaignVerification(context.Background(), run.GetRunId(), report))

	_, err := coordinator.PublishRunAggregates(context.Background(), run.GetRunId(), time.Unix(1_700_000_300, 0).UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)

	_, err = coordinator.PublishRunCompletion(context.Background(), run.GetRunId(), time.Unix(1_700_000_300, 0).UTC())
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)

	assert.Empty(t, exporter.records, "a failed bind must publish nothing")
}

// A caller-supplied report that does not apply to a settled run is rejected
// before catch-up exports any record.
func TestPublishRunCatchUpWithVerificationRejectsCallerReportForSettledRun(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	run := completedTestCampaign(t, store)

	report := buildBoundTestReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
	report.VerifiedPopulationDigest = strings.Repeat("9", 64)

	_, err := coordinator.PublishRunCatchUpWithVerification(context.Background(), run.GetRunId(), report)
	require.Error(t, err)
	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	assert.Empty(t, exporter.records)
}

func TestPublishRunAggregatesProjectsBoundFailure(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	exporter := &recordingCampaignFeedExporter{}
	coordinator := NewCampaignPublicationCoordinator(store, files, NewMemoryCampaignPublicationStateStore(), exporter)
	run := completedTestCampaign(t, store)

	bindPersistedVerificationReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL)
	_, err := coordinator.PublishRunAggregates(context.Background(), run.GetRunId(), time.Unix(1_700_000_300, 0).UTC())
	require.NoError(t, err)

	summaries := evaluationSummaries(t, exporter.records)
	require.NotEmpty(t, summaries)
	latest := summaries[len(summaries)-1]
	assert.Equal(t, "failed", latest.VerifierState)
	require.NotNil(t, latest.VerificationMetadata)
	assert.Equal(t, "bound", latest.VerificationMetadata.Provenance)
	assert.Equal(t, "failed", latest.VerificationMetadata.VerifierState)
}

// bindRunVerification is the single rule for whether a persisted report applies
// to a settled run: an applicable report binds, and a non-applicable or
// malformed one fails closed with the sentinel the caller can match.
func TestBindRunVerificationOnSettledRun(t *testing.T) {
	files := newCampaignRuntimeFileService(t)
	store := NewStore(files)
	run := completedTestCampaign(t, store)
	ctx := context.Background()
	spec, err := store.LoadCampaignSpec(ctx, run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	catalog, err := store.LoadScenarioCatalog(ctx, run.GetCampaignBinding().GetCampaignId())
	require.NoError(t, err)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	results, err := store.LoadAssignmentResults(ctx, run.GetRunId(), assignments)
	require.NoError(t, err)

	for _, tc := range []struct {
		name      string
		mutate    func(report *evalv1.EvaluationVerificationReport)
		wantBound bool
		wantErr   error
	}{
		{name: "applicable report binds", mutate: func(*evalv1.EvaluationVerificationReport) {}, wantBound: true},
		{name: "population digest mismatch fails closed", mutate: func(report *evalv1.EvaluationVerificationReport) {
			report.VerifiedPopulationDigest = strings.Repeat("9", 64)
		}, wantErr: constants.ErrEvidenceScopeMismatch},
		{name: "verified count mismatch fails closed", mutate: func(report *evalv1.EvaluationVerificationReport) {
			report.VerifiedAssignmentCount++
		}, wantErr: constants.ErrEvidenceScopeMismatch},
		{name: "missing report digest is malformed", mutate: func(report *evalv1.EvaluationVerificationReport) {
			report.ReportDigestRef = nil
		}, wantErr: constants.ErrEvidenceArtifactMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := buildBoundTestReport(t, store, run, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS)
			tc.mutate(report)

			bound, err := bindRunVerification(run, spec, catalog, assignments, results, uint32(len(assignments)), report)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, bound)
				return
			}
			require.NoError(t, err)
			if tc.wantBound {
				require.NotNil(t, bound)
				assert.Equal(t, report.GetVerifiedPopulationDigest(), bound.GetVerifiedPopulationDigest())
			}
		})
	}
}

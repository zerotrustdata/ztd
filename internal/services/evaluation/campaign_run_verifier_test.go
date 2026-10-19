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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignVerificationBinding_BindsExactVerifiedPopulation(t *testing.T) {
	catalogRef := &compliancev1.VersionedReference{Id: "catalog-1", Version: "1.0.0"}
	run := &evalv1.EvaluationRun{
		RunId: "run-1",
		CampaignBinding: &evalv1.ModelCampaignBinding{
			CampaignId:          "campaign-1",
			CampaignDigest:      "campaign-digest",
			CatalogRef:          catalogRef,
			CatalogDigest:       "catalog-digest",
			ModelRegistryDigest: "registry-digest",
		},
	}
	spec := &evalv1.EvaluationCampaignSpec{CampaignId: "campaign-1", CampaignDigest: "campaign-digest", CatalogRef: catalogRef, CatalogDigest: "catalog-digest", ModelRegistryDigest: "registry-digest"}
	catalog := &evalv1.EvaluationScenarioCatalog{CatalogRef: catalogRef, CatalogDigest: "catalog-digest"}
	assignments := []*evalv1.EvaluationAssignment{{AssignmentId: "assignment-1", RunId: "run-1", CampaignId: "campaign-1", ScenarioId: "scenario-1", DeterministicIdentity: "identity-1", LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED}}
	results := map[string]*evalv1.EvaluationAssignmentResult{"assignment-1": {AssignmentId: "assignment-1", RunId: "run-1", CampaignId: "campaign-1", ResultDigest: "result-digest"}}
	report := &evalv1.EvaluationVerificationReport{SchemaVersion: CampaignSchemaVersion, ReportId: "run-1", RunId: "run-1", Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, VerifiedAt: timestamppb.New(time.Unix(1_700_000_000, 0).UTC())}

	bound, applicability, err := bindCampaignVerificationReport(report, run, spec, catalog, assignments, results, CampaignVerificationPolicy{
		VerifierReleaseVersion: "v2.1.12",
		ProviderObservation:    ProviderObservationPolicyStrict,
		ModelProvenance:        ModelProvenancePolicyInterim,
	})

	require.NoError(t, err)
	assert.True(t, applicability.Applicable)
	assert.Equal(t, campaignVerificationSchemaVersion, bound.GetSchemaVersion())
	assert.Equal(t, constants.CampaignVerifierVersion, bound.GetVerifierContractVersion())
	assert.Equal(t, "v2.1.12", bound.GetVerifierReleaseVersion())
	assert.Equal(t, uint32(1), bound.GetExpectedAssignmentCount())
	assert.Equal(t, uint32(1), bound.GetVerifiedAssignmentCount())
	assert.Equal(t, applicability.Population.GetCampaignDigest(), bound.GetCampaignDigest())
	assert.Equal(t, applicability.Population.GetCatalogDigest(), bound.GetCatalogDigest())
	assert.Equal(t, applicability.Population.GetModelRegistryDigest(), bound.GetModelRegistryDigest())
	assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_STRICT, bound.GetProviderObservationPolicy())
	assert.Equal(t, evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, bound.GetModelProvenancePolicy())
	assert.Equal(t, mustPopulationDigest(t, applicability.Population), bound.GetVerifiedPopulationDigest())
	require.NotNil(t, bound.GetReportDigestRef())
	assert.Len(t, bound.GetReportDigestRef().GetSha256(), 64)
	assert.Empty(t, report.GetVerifierContractVersion())
}

func TestCampaignRunVerifier_RejectsInputsOutsideFrozenCatalog(t *testing.T) {
	f := newCompletedRunFixture(t, nil)
	t.Run("resealed different catalog", func(t *testing.T) {
		catalog := proto.Clone(f.catalog).(*evalv1.EvaluationScenarioCatalog)
		catalog.Scenarios[0].PublicDescription = "substituted description"
		var err error
		catalog.CatalogDigest, err = ComputeScenarioCatalogDigest(catalog)
		require.NoError(t, err)
		_, err = NewCampaignRunVerifier(nil).VerifyRun(t.Context(), f.store, f.req.RunID, catalog, f.req.ScenarioArtifacts)
		require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	})
	t.Run("different fixture bytes", func(t *testing.T) {
		artifacts := make(map[string]ScenarioArtifacts, len(f.req.ScenarioArtifacts))
		for id, artifact := range f.req.ScenarioArtifacts {
			artifacts[id] = artifact
		}
		id := f.catalog.Scenarios[0].GetScenarioId()
		artifact := artifacts[id]
		artifact.Gold.Body = []byte(`{"scenario_id":"forged"}`)
		artifacts[id] = artifact
		_, err := NewCampaignRunVerifier(nil).VerifyRun(t.Context(), f.store, f.req.RunID, f.catalog, artifacts)
		require.Error(t, err)
	})
}

func TestCampaignRunVerifier_RejectsHomogeneousTargetInSystemRun(t *testing.T) {
	f := newArchiveFixture(t)
	createBoundSystemCampaign(t, f, "system-target-test")
	_, err := f.controller.StartRun(t.Context(), RunStartRequest{
		CampaignID: "system-target-test", RunID: "system-target-run", PlatformRelease: testPlatform.Release, Lane: evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
	})
	require.NoError(t, err)
	_, err = f.controller.ScheduleRun(t.Context(), "system-target-run")
	require.NoError(t, err)
	assignments, err := f.store.ListAssignments(t.Context(), "system-target-run")
	require.NoError(t, err)
	require.NotEmpty(t, assignments)
	assignment := assignments[0]
	assignment.Target = &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
		DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY, CandidateVariant: testModelVariant(),
	}}
	assignment.DeterministicIdentity, err = ComputeAssignmentDeterministicIdentity(assignment)
	require.NoError(t, err)
	assignment.AssignmentId = assignment.DeterministicIdentity
	require.NoError(t, f.store.SaveAssignment(t.Context(), assignment))
	report, err := NewCampaignRunVerifier(nil).VerifyRun(t.Context(), f.store, "system-target-run", f.req.Catalog, f.req.ScenarioArtifacts)
	require.NoError(t, err)
	assert.Contains(t, report.GetFailureReasons(), "assignment "+assignment.GetAssignmentId()+" target does not match the frozen run lane")
}

func mustPopulationDigest(t *testing.T, population *evalv1.EvaluationVerifiedPopulation) string {
	t.Helper()
	digest, err := ComputeVerifiedPopulationDigest(population)
	require.NoError(t, err)
	return digest
}

func TestVerifyCampaignRunReadOnly_BindsIncompletePopulationWithoutPersistingVerification(t *testing.T) {
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)

	result, err := verifyCampaignRunReadOnly(context.Background(), store, req.RunID, CampaignVerificationPolicy{
		VerifierReleaseVersion: "v2.1.12",
		ProviderObservation:    ProviderObservationPolicyInterim,
		ModelProvenance:        ModelProvenancePolicyInterim,
		AssessmentTime:         func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
	})

	require.NoError(t, err)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, result.Report.GetStatus())
	assert.False(t, result.Population.Complete)
	assert.True(t, result.Applicability.Applicable)
	assert.Equal(t, uint32(count), result.Report.GetExpectedAssignmentCount())
	assert.Zero(t, result.Report.GetVerifiedAssignmentCount())
	_, err = store.LoadCampaignVerification(context.Background(), req.RunID)
	require.Error(t, err)
}

// completedRunFixture is a persisted model-role run with one completed
// assignment whose result verifies, under the catalog ref it was frozen with.
type completedRunFixture struct {
	store    *Store
	req      CampaignInitRequest
	catalog  *evalv1.EvaluationScenarioCatalog
	imported *evalv1.EvaluationAssignmentResult
}

func (f completedRunFixture) verify(t *testing.T) *evalv1.EvaluationVerificationReport {
	t.Helper()
	report, err := NewCampaignRunVerifier(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }).VerifyRun(context.Background(), f.store, f.req.RunID, f.catalog, f.req.ScenarioArtifacts)
	require.NoError(t, err)
	return report
}

// newCompletedRunFixture freezes a three-scenario catalog under catalogRef (the
// helper's own ref when nil), schedules the model-role matrix, runs one
// deterministic assignment through the agent executor, and persists its sealed
// agent trace and result.
func newCompletedRunFixture(t *testing.T, catalogRef *compliancev1.VersionedReference) completedRunFixture {
	t.Helper()
	ctx := context.Background()
	store := NewStore(newCampaignMemoryFileService())
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	catalog := req.Catalog
	if catalogRef == nil {
		catalogRef = catalog.GetCatalogRef()
	}
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalogRef,
		Scenarios:     catalog.GetScenarios()[:3],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated
	_, err = controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)
	assignments, err := store.ListAssignments(ctx, req.RunID)
	require.NoError(t, err)
	// A semantic-judge scenario grades as GRADER_FAILED because the agent
	// executor records no judge evidence. These tests need a completed
	// assignment, so the scenario is a deterministic one.
	var assignment *evalv1.EvaluationAssignment
	for _, candidate := range assignments {
		method, methodErr := scenarioGradingMethodForAssignment(truncated, candidate)
		require.NoError(t, methodErr)
		if method == evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC {
			assignment = candidate
			break
		}
	}
	require.NotNil(t, assignment, "truncated catalog needs a deterministic scenario")

	agent := newAgentExecutorFixture(t, runCommandTurns(agentTestCommand)...)
	agent.req.Assignment = assignment
	agent.req.GradingMethod = evalv1.EvaluationGradingMethod_EVALUATION_GRADING_METHOD_DETERMINISTIC
	agent.req.Binding.ModelRegistryDigest = req.Inventory.RegistryDigest
	agent.req.Binding.ModelRegistry = InferenceVariantsFromEvalRegistry(req.Inventory.Variants)
	agent.req.Binding.ModelVariants = req.Inventory.Variants
	artifact := req.ScenarioArtifacts[assignment.GetScenarioId()]
	agent.req.ScenarioInput = ScenarioInputFixture{}
	require.NoError(t, json.Unmarshal(artifact.Input.Body, &agent.req.ScenarioInput))
	agent.req.ScenarioGold = ScenarioGoldCriteria{}
	require.NoError(t, json.Unmarshal(artifact.Gold.Body, &agent.req.ScenarioGold))
	agent.req.ScenarioTools, err = scenarioToolsForAssignment(truncated, assignment)
	require.NoError(t, err)
	imported, err := agent.execute()
	require.NoError(t, err)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, imported.GetLifecycleStatus())
	require.NotEmpty(t, imported.GetDeterministicGrades())

	require.NoError(t, store.SaveAssignmentAgentTrace(ctx, req.RunID, assignment.GetAssignmentId(), agent.traces.body))
	assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	require.NoError(t, store.SaveAssignment(ctx, assignment))
	require.NoError(t, store.SaveAssignmentResult(ctx, imported))
	return completedRunFixture{store: store, req: req, catalog: truncated, imported: imported}
}

func TestCampaignRunVerifier_PassesCompletedAssignment(t *testing.T) {
	t.Parallel()
	f := newCompletedRunFixture(t, nil)

	report := f.verify(t)

	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
	require.NoError(t, f.store.SaveCampaignVerification(context.Background(), f.req.RunID, report))
}

// forgeFirstGrade flips one stored deterministic grade and reseals the result
// digest, so only grade recomputation can notice.
func (f completedRunFixture) forgeFirstGrade(t *testing.T) {
	t.Helper()
	forged := proto.Clone(f.imported).(*evalv1.EvaluationAssignmentResult)
	grade := forged.GetDeterministicGrades()[0]
	if grade.GetStatus() == verdictPass {
		grade.Status, grade.Score = verdictFail, 0
	} else {
		grade.Status, grade.Score = verdictPass, 1
	}
	digest, err := ComputeAssignmentResultDigest(forged)
	require.NoError(t, err)
	forged.ResultDigest = digest
	require.NoError(t, f.store.SaveAssignmentResult(context.Background(), forged))
}

// R8: a run frozen from an older built-in default suite keeps every digest and
// evidence check but is not regraded, so it is not marked failed by fixtures
// this build no longer carries. A run under the current catalog is regraded and a
// forged grade fails it.
func TestCampaignRunVerifier_RegradesOnlyCatalogsThisBuildStillCarries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ref        *compliancev1.VersionedReference
		wantStatus evalv1.EvaluationVerdictStatus
	}{
		{name: "the current catalog is regraded", ref: &compliancev1.VersionedReference{Id: DefaultSuiteID, Version: DefaultSuiteVersion}, wantStatus: verdictFail},
		{name: "an older default suite version is not regraded", ref: &compliancev1.VersionedReference{Id: DefaultSuiteID, Version: "1.0.0"}, wantStatus: verdictPass},
		{name: "the pre-rename default suite id is not regraded", ref: &compliancev1.VersionedReference{Id: LegacyDefaultSuiteID, Version: "1.0.0"}, wantStatus: verdictPass},
		{name: "a custom suite is always regraded", ref: &compliancev1.VersionedReference{Id: "custom-suite", Version: "1.0.0"}, wantStatus: verdictFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCompletedRunFixture(t, tt.ref)
			require.Equal(t, verdictPass, f.verify(t).GetStatus(), "the honest result verifies under every catalog")
			f.forgeFirstGrade(t)

			assert.Equal(t, tt.wantStatus, f.verify(t).GetStatus())
		})
	}
}

func TestCaptureCampaignRunEvidence_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		store    *Store
		runID    string
		callObs  bool
		callProv bool
		wantErr  error
	}{
		{
			name:    "provider observation missing store",
			store:   nil,
			runID:   "run-1",
			callObs: true,
			wantErr: constants.ErrMissingRequiredField,
		},
		{
			name:    "provider observation missing run id",
			store:   NewStore(newCampaignMemoryFileService()),
			runID:   "",
			callObs: true,
			wantErr: constants.ErrMissingRequiredField,
		},
		{
			name:     "model provenance missing store",
			store:    nil,
			runID:    "run-1",
			callProv: true,
			wantErr:  constants.ErrMissingRequiredField,
		},
		{
			name:     "model provenance missing run id",
			store:    NewStore(newCampaignMemoryFileService()),
			runID:    "",
			callProv: true,
			wantErr:  constants.ErrMissingRequiredField,
		},
	}

	for _, tt := range tests {

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			var err error
			if tt.callObs {
				err = CaptureCampaignRunProviderObservationEvidence(ctx, tt.store, tt.runID, nil)
			} else if tt.callProv {
				err = CaptureCampaignRunModelProvenanceEvidence(ctx, tt.store, tt.runID, nil)
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestCaptureCampaignRunEvidence_DecoupledObservationAndProvenance(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)

	ctx := context.Background()

	// 1. With nil readers, each function returns cleanly (safe no-op).
	assert.NoError(t, CaptureCampaignRunProviderObservationEvidence(ctx, store, req.RunID, nil))
	assert.NoError(t, CaptureCampaignRunModelProvenanceEvidence(ctx, store, req.RunID, nil))

	// 2. With real local-only readers, evidence capture succeeds without error.
	obsReader, err := NewCampaignProviderObservationReader(NewStore(files))
	require.NoError(t, err)
	provReader, err := NewCampaignModelProvenanceReader(NewStore(files))
	require.NoError(t, err)

	assert.NoError(t, CaptureCampaignRunProviderObservationEvidence(ctx, store, req.RunID, obsReader))
	assert.NoError(t, CaptureCampaignRunModelProvenanceEvidence(ctx, store, req.RunID, provReader))
}

func TestWaitForCampaignRunModelProvenanceEvidence_UsesPersistedScoredAttempts(t *testing.T) {
	fixture := newCompletedRunFixture(t, nil)
	result := proto.Clone(fixture.imported).(*evalv1.EvaluationAssignmentResult)
	result.ModelInferences = []*evalv1.ModelInferenceRecord{{ProviderAttemptId: "attempt-scored"}}
	digest, err := ComputeAssignmentResultDigest(result)
	require.NoError(t, err)
	result.ResultDigest = digest
	require.NoError(t, fixture.store.SaveAssignmentResult(t.Context(), result))
	remote := &stubModelProvenanceRemote{window: testModelProvenanceWindow(t, "attempt-scored")}
	reader, err := NewCampaignModelProvenanceReaderWithRemote(fixture.store, remote)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, WaitForCampaignRunModelProvenanceEvidence(ctx, fixture.store, fixture.req.RunID, reader))
	// Other scheduled assignments have no result and require no windows.
	window, err := fixture.store.LoadModelProvenanceWindow(ctx, fixture.req.RunID, "attempt-scored")
	require.NoError(t, err)
	assert.Equal(t, "attempt-scored", window.GetProviderAttemptId())
}

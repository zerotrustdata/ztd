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

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignVerificationBinding_BindsExactVerifiedPopulation(t *testing.T) {
	t.Parallel()
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

func TestCampaignRunVerifier_RejectsHomogeneousTargetInSystemRun(t *testing.T) {
	t.Parallel()
	f := newArchiveFixture(t)
	createBoundSystemCampaign(t, f, "system-target-test")
	_, err := f.controller.StartRun(t.Context(), RunStartRequest{Governance: testGovernance, ProviderObservationPolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM, ModelProvenancePolicy: evalv1.EvaluationWitnessPolicy_EVALUATION_WITNESS_POLICY_INTERIM,
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
	t.Parallel()
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

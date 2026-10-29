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
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestCampaignPopulationAccountant_PartialRunReportsCoverageGap(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	_, err := controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)
	require.Greater(t, count, 1)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	first := assignments[0]
	first.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
	require.NoError(t, store.SaveAssignment(context.Background(), first))

	report, err := NewCampaignPopulationAccountant(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }).AccountRun(context.Background(), store, req.RunID, req.Catalog)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, uint32(count), report.ScheduledAssignments)
	assert.Equal(t, uint32(1), report.TerminalCount)
	assert.Equal(t, uint32(count-1), report.QueuedCount)
	assert.Empty(t, report.MissingCells)
	assert.NotEmpty(t, report.TerminalWithoutResult)
}

func TestCampaignPopulationAccountant_CompleteRunPasses(t *testing.T) {
	t.Parallel()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: req.Catalog.GetSchemaVersion(),
		CatalogRef:    req.Catalog.GetCatalogRef(),
		Scenarios:     req.Catalog.GetScenarios()[:1],
	}
	truncatedDigest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = truncatedDigest
	req.Catalog = truncated
	_, err = controller.InitializeCampaign(context.Background(), req)
	require.NoError(t, err)
	_, err = controller.ScheduleRun(context.Background(), req.RunID)
	require.NoError(t, err)

	assignments, err := store.ListAssignments(context.Background(), req.RunID)
	require.NoError(t, err)
	for _, assignment := range assignments {
		assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
		require.NoError(t, store.SaveAssignment(context.Background(), assignment))
		result := &evalv1.EvaluationAssignmentResult{
			SchemaVersion:   CampaignSchemaVersion,
			CampaignId:      req.CampaignID,
			RunId:           req.RunID,
			AssignmentId:    assignment.GetAssignmentId(),
			LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
			Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
		}
		digest, err := ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		result.ResultDigest = digest
		require.NoError(t, store.SaveAssignmentResult(context.Background(), result))
	}

	report, err := NewCampaignPopulationAccountant(func() time.Time { return time.Unix(1_700_000_200, 0).UTC() }).AccountRun(context.Background(), store, req.RunID, truncated)
	require.NoError(t, err)
	assert.True(t, report.Complete)
	assert.Empty(t, report.FailureReasons)
	assert.Equal(t, report.ScheduledAssignments, uint32(report.ExpectedCells))
	assert.Equal(t, report.TerminalCount, uint32(report.ExpectedCells))
}

func TestCampaignPopulationAccountant_UsesFrozenPostureForMatrix(t *testing.T) {
	for _, tc := range []struct {
		name             string
		posture          evalv1.EvaluationGovernancePosture
		scheduledPosture evalv1.EvaluationGovernancePosture
		wantExpected     uint64
		wantComplete     bool
		wantExtra        int
	}{
		{
			name:             "consensus reduced matrix is complete",
			posture:          evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
			scheduledPosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
			wantExpected:     60, wantComplete: true,
		},
		{
			name:             "consensus with gated scenarios is extra population",
			posture:          evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
			scheduledPosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
			wantExpected:     60, wantExtra: 4,
		},
		{
			name:             "doctrine reduced matrix is incomplete",
			posture:          evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
			scheduledPosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS,
			wantExpected:     64,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewStore(newCampaignMemoryFileService())
			controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
			req := testCampaignInitRequest(t)
			req.Governance = RunGovernance{Posture: tc.posture, Consensus: &evalv1.EvaluationConsensusIdentity{}}
			run, err := controller.InitializeCampaign(ctx, req)
			require.NoError(t, err)

			scenarios := []string(nil)
			if tc.name == "consensus with gated scenarios is extra population" {
				scenarios = []string{"recovery-live-command-error", "tool-arg-shell-command"}
			}
			assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
				CampaignID: req.CampaignID, RunID: run.GetRunId(), Catalog: req.Catalog,
				ActivePosture: tc.scheduledPosture, Variants: req.Inventory.Variants,
				RepetitionCount: req.RepetitionCount,
			})
			require.NoError(t, err)
			if len(scenarios) > 0 {
				gated, gatedErr := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
					CampaignID: req.CampaignID, RunID: run.GetRunId(), Catalog: req.Catalog,
					ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
					Variants:      req.Inventory.Variants, RepetitionCount: req.RepetitionCount, ScenarioIDs: scenarios,
				})
				require.NoError(t, gatedErr)
				assignments = append(assignments, gated...)
			}
			results := make(map[string]*evalv1.EvaluationAssignmentResult, len(assignments))
			for _, assignment := range assignments {
				assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
				require.NoError(t, store.SaveAssignment(ctx, assignment))
				result := &evalv1.EvaluationAssignmentResult{
					SchemaVersion: CampaignSchemaVersion, CampaignId: req.CampaignID, RunId: run.GetRunId(),
					AssignmentId:    assignment.GetAssignmentId(),
					LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
					Lane:            evalv1.EvaluationLane_EVALUATION_LANE_MODEL_ROLE,
				}
				result.ResultDigest, err = ComputeAssignmentResultDigest(result)
				require.NoError(t, err)
				require.NoError(t, store.SaveAssignmentResult(ctx, result))
				results[assignment.GetAssignmentId()] = result
			}

			report, err := NewCampaignPopulationAccountant(nil).AccountRun(ctx, store, run.GetRunId(), req.Catalog)
			require.NoError(t, err)
			assert.Equal(t, tc.wantExpected, report.ExpectedCells)
			assert.Equal(t, tc.wantComplete, report.Complete)
			assert.Len(t, report.ExtraAssignments, tc.wantExtra)
			if tc.posture == evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE && !tc.wantComplete {
				assert.NotEmpty(t, report.MissingCells)
			}

			verified, err := verifyCampaignRunReadOnly(ctx, store, run.GetRunId(), CampaignVerificationPolicy{
				VerifierReleaseVersion: "v2.1.12",
				ProviderObservation:    ProviderObservationPolicyInterim,
				ModelProvenance:        ModelProvenancePolicyInterim,
				AssessmentTime:         func() time.Time { return time.Unix(1_700_000_100, 0).UTC() },
			})
			require.NoError(t, err)
			assert.Equal(t, uint32(tc.wantExpected), verified.Report.GetExpectedAssignmentCount())

			spec, err := store.LoadCampaignSpec(ctx, req.CampaignID)
			require.NoError(t, err)
			projectorReport := &evalv1.EvaluationVerificationReport{
				RunId: run.GetRunId(), Status: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
				ExpectedAssignmentCount: uint32(tc.wantExpected), VerifiedAssignmentCount: uint32(len(assignments)),
				CampaignDigest: spec.GetCampaignDigest(), CatalogDigest: spec.GetCatalogDigest(), ModelRegistryDigest: spec.GetModelRegistryDigest(),
			}
			applicability, err := BuildRunVerificationApplicability(run, spec, req.Catalog, assignments, results, uint32(tc.wantExpected), projectorReport)
			require.NoError(t, err)
			projectorReport.VerifiedPopulationDigest, err = ComputeVerifiedPopulationDigest(applicability.Population)
			require.NoError(t, err)
			applicability, err = BuildRunVerificationApplicability(run, spec, req.Catalog, assignments, results, uint32(tc.wantExpected), projectorReport)
			require.NoError(t, err)
			assert.Equal(t, tc.wantComplete, applicability.Applicable)
			assert.Equal(t, uint32(tc.wantExpected), verified.Report.GetExpectedAssignmentCount())
		})
	}
}

// persistPostureGatedRun schedules the default suite as if under
// scheduledPosture (plus extraScenarios scheduled without the L2 gate) and
// persists a traceless provider-failure result for every assignment, which the
// verifier accepts structurally.
func persistPostureGatedRun(t *testing.T, posture, scheduledPosture evalv1.EvaluationGovernancePosture, extraScenarios []string) (*Store, CampaignInitRequest, *evalv1.EvaluationRun, []*evalv1.EvaluationAssignment) {
	t.Helper()
	ctx := context.Background()
	store := NewStore(newCampaignMemoryFileService())
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Governance = RunGovernance{Posture: posture, Consensus: &evalv1.EvaluationConsensusIdentity{}}
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	assignments, err := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
		CampaignID: req.CampaignID, RunID: run.GetRunId(), Catalog: req.Catalog,
		ActivePosture: scheduledPosture, Variants: req.Inventory.Variants, RepetitionCount: req.RepetitionCount,
	})
	require.NoError(t, err)
	if len(extraScenarios) > 0 {
		gated, gatedErr := BuildHomogeneousAssignmentMatrix(HomogeneousScheduleRequest{
			CampaignID: req.CampaignID, RunID: run.GetRunId(), Catalog: req.Catalog,
			ActivePosture: evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
			Variants:      req.Inventory.Variants, RepetitionCount: req.RepetitionCount, ScenarioIDs: extraScenarios,
		})
		require.NoError(t, gatedErr)
		assignments = append(assignments, gated...)
	}
	for _, assignment := range assignments {
		result, resultErr := BuildExecutorFailureAssignmentResult(AssignmentExecutionRequest{Assignment: assignment}, fmt.Errorf("dispatch inference: %w", constants.ErrInferenceBackendTimeout), time.Unix(1_700_000_050, 0).UTC())
		require.NoError(t, resultErr)
		assignment.LifecycleStatus = result.GetLifecycleStatus()
		assignment.CompletedAt = result.GetCompletedAt()
		require.NoError(t, store.SaveAssignment(ctx, assignment))
		require.NoError(t, store.SaveAssignmentResult(ctx, result))
	}
	return store, req, run, assignments
}

func TestCampaignRunVerifier_VerdictFollowsFrozenPostureMatrix(t *testing.T) {
	consensus := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS
	doctrine := evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE
	gatedScenarios := []string{"recovery-live-command-error", "tool-arg-shell-command"}
	for _, tc := range []struct {
		name             string
		posture          evalv1.EvaluationGovernancePosture
		scheduledPosture evalv1.EvaluationGovernancePosture
		extraScenarios   []string
		wantStatus       evalv1.EvaluationVerdictStatus
		wantExpected     uint32
		wantVerified     uint32
	}{
		{name: "reduced consensus matrix passes", posture: consensus, scheduledPosture: consensus,
			wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, wantExpected: 60, wantVerified: 60},
		{name: "consensus run with a gated scenario fails", posture: consensus, scheduledPosture: consensus, extraScenarios: gatedScenarios,
			wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, wantExpected: 60, wantVerified: 64},
		{name: "reduced doctrine matrix is incomplete", posture: doctrine, scheduledPosture: consensus,
			wantStatus: evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, wantExpected: 64, wantVerified: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, req, run, _ := persistPostureGatedRun(t, tc.posture, tc.scheduledPosture, tc.extraScenarios)

			report, err := NewCampaignRunVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }).
				VerifyRun(context.Background(), store, run.GetRunId(), req.Catalog, req.ScenarioArtifacts)

			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, report.GetStatus(), "failure reasons: %v", report.GetFailureReasons())
			assert.Equal(t, tc.wantExpected, report.GetExpectedAssignmentCount())
			assert.Equal(t, tc.wantVerified, report.GetVerifiedAssignmentCount())
			if tc.wantStatus == evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS {
				assert.Empty(t, report.GetFailureReasons())
			}
		})
	}
}

func TestCampaignPublication_RejectsReportDenominatorMatchedToIncompletePopulation(t *testing.T) {
	ctx := context.Background()
	// A doctrine run expects 64 cells; only the 60-cell consensus matrix exists.
	store, req, run, assignments := persistPostureGatedRun(t,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE,
		evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS, nil)
	spec, err := store.LoadCampaignSpec(ctx, req.CampaignID)
	require.NoError(t, err)
	results, err := loadCampaignAssignmentResults(ctx, store, run.GetRunId(), assignments)
	require.NoError(t, err)

	// The tampered report claims the incomplete population is the whole matrix.
	tampered := uint32(len(assignments))
	report := &evalv1.EvaluationVerificationReport{
		SchemaVersion: campaignVerificationSchemaVersion, ReportId: run.GetRunId(), RunId: run.GetRunId(),
		Status:                  evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS,
		VerifiedAt:              timestamppb.New(time.Unix(1_700_000_200, 0).UTC()),
		VerifierReleaseVersion:  "test-release",
		VerifierContractVersion: constants.CampaignVerifierVersion,
		ReportDigestRef:         &compliancev1.ComplianceEvidenceReference{Sha256: strings.Repeat("a", 64)},
		CampaignDigest:          spec.GetCampaignDigest(), CatalogDigest: spec.GetCatalogDigest(), ModelRegistryDigest: spec.GetModelRegistryDigest(),
	}
	forged, err := BuildRunVerificationApplicability(run, spec, req.Catalog, assignments, results, tampered, report)
	require.NoError(t, err)
	report.VerifiedPopulationDigest, err = ComputeVerifiedPopulationDigest(forged.Population)
	require.NoError(t, err)
	report.ExpectedAssignmentCount = tampered
	report.VerifiedAssignmentCount = forged.VerifiedAssignmentCount

	bound, err := NewCampaignPublicationCoordinator(store, store.files, NewMemoryCampaignPublicationStateStore(), &recordingCampaignFeedExporter{}).boundRunVerification(ctx, run.GetRunId(), report)

	require.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	assert.Nil(t, bound)
}

func TestCampaignPopulationAccountant_CompleteSystemRunPasses(t *testing.T) {
	ctx := context.Background()
	files := newCampaignMemoryFileService()
	store := NewStore(files)
	controller := NewCampaignController(store, nil, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
	var err error
	req.Inventory, err = MaterializeModelRegistry(req.CampaignID, testHeterogeneousVariants())
	require.NoError(t, err)
	req.FormationStackSet, err = testFormationStackSet(req.CampaignID, 17, req.Inventory.Variants)
	require.NoError(t, err)
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(ctx, run.GetRunId())
	require.NoError(t, err)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Len(t, assignments, count)
	for _, assignment := range assignments {
		assignment.LifecycleStatus = evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED
		require.NoError(t, store.SaveAssignment(ctx, assignment))
		result := &evalv1.EvaluationAssignmentResult{
			SchemaVersion:   CampaignSchemaVersion,
			CampaignId:      req.CampaignID,
			RunId:           run.GetRunId(),
			AssignmentId:    assignment.GetAssignmentId(),
			LifecycleStatus: evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED,
			Lane:            evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM,
		}
		result.ResultDigest, err = ComputeAssignmentResultDigest(result)
		require.NoError(t, err)
		require.NoError(t, store.SaveAssignmentResult(ctx, result))
	}

	report, err := NewCampaignPopulationAccountant(func() time.Time { return time.Unix(1_700_000_200, 0).UTC() }).AccountRun(ctx, store, run.GetRunId(), req.Catalog)
	require.NoError(t, err)
	assert.True(t, report.Complete, report.FailureReasons)
	assert.Empty(t, report.FailureReasons)
	assert.Equal(t, uint32(count), report.ScheduledAssignments)
	assert.Equal(t, uint64(count), report.ExpectedCells)
	assert.Equal(t, uint32(count), report.TerminalCount)
}

func TestCampaignPopulationAccountant_SystemMatrixTamperingFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]*evalv1.EvaluationAssignment)
		want   string
	}{
		{
			name: "missing cell",
			mutate: func(assignments []*evalv1.EvaluationAssignment) {
				assignments[0].ScenarioId = "missing-scenario"
				assignments[0].DeterministicIdentity = ""
			},
			want: "expected matrix cell(s) are not scheduled",
		},
		{
			name: "duplicate identity",
			mutate: func(assignments []*evalv1.EvaluationAssignment) {
				assignments[1].ScenarioId = assignments[0].GetScenarioId()
				assignments[1].ScenarioRef = assignments[0].GetScenarioRef()
				assignments[1].Target = assignments[0].GetTarget()
				assignments[1].DeterministicIdentity = ""
			},
			want: "duplicate deterministic identity",
		},
		{
			name: "wrong target",
			mutate: func(assignments []*evalv1.EvaluationAssignment) {
				stack := assignments[0].GetHeterogeneous().GetStack()
				assignments[0].Target = &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
					DesignatedRole: evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
					CandidateVariant: &evalv1.ModelVariant{VariantId: stack.GetPrimarySlot().GetVariantId(),
						ServedModelTag: "unbound:wrong-target", ModelDigest: repeatHex('f')},
				}}
				assignments[0].DeterministicIdentity = ""
			},
			want: "fall outside the expected matrix",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			files := newCampaignMemoryFileService()
			store := NewStore(files)
			controller := NewCampaignController(store, nil, nil, nil)
			req := testCampaignInitRequest(t)
			req.Lane = evalv1.EvaluationLane_EVALUATION_LANE_SYSTEM
			var err error
			req.Inventory, err = MaterializeModelRegistry(req.CampaignID, testHeterogeneousVariants())
			require.NoError(t, err)
			req.FormationStackSet, err = testFormationStackSet(req.CampaignID, 17, req.Inventory.Variants)
			require.NoError(t, err)
			run, err := controller.InitializeCampaign(ctx, req)
			require.NoError(t, err)
			_, err = controller.ScheduleRun(ctx, run.GetRunId())
			require.NoError(t, err)
			assignments, err := store.ListAssignments(ctx, run.GetRunId())
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(assignments), 2)
			test.mutate(assignments)
			for _, assignment := range assignments {
				require.NoError(t, store.SaveAssignment(ctx, assignment))
			}
			if test.name == "missing cell" {
				assignments, err = store.ListAssignments(ctx, run.GetRunId())
				require.NoError(t, err)
				first := assignments[0]
				require.NoError(t, store.files.Remove(ctx, store.layout.assignmentPath(run.GetRunId(), first.GetAssignmentId())))
			}

			report, err := NewCampaignPopulationAccountant(nil).AccountRun(ctx, store, run.GetRunId(), req.Catalog)
			require.NoError(t, err)
			assert.False(t, report.Complete)
			assert.Contains(t, strings.Join(report.FailureReasons, "\n"), test.want)
		})
	}
}

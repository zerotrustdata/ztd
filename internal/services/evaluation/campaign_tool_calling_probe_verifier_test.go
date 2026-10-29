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
	"google.golang.org/protobuf/proto"

	complianceevidence "github.com/zerotrustdata/ztd/internal/services/compliance/evidence"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

type toolCallingVerifierFixture struct {
	ctx        context.Context
	store      *Store
	req        CampaignInitRequest
	run        *evalv1.EvaluationRun
	assignment *evalv1.EvaluationAssignment
	result     *evalv1.EvaluationAssignmentResult
}

func newToolCallingVerifierFixture(t *testing.T) toolCallingVerifierFixture {
	t.Helper()
	ctx := context.Background()
	req := testCampaignInitRequest(t)
	catalog, artifacts, err := BuildScenarioCatalog()
	require.NoError(t, err)
	var selected *evalv1.EvaluationScenarioDefinition
	for _, scenario := range catalog.GetScenarios() {
		if scenario.GetScenarioId() == "tool-select-file-read" {
			selected = proto.Clone(scenario).(*evalv1.EvaluationScenarioDefinition)
			break
		}
	}
	require.NotNil(t, selected)
	selected.EligibleRoles = []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}
	catalog = &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(), CatalogRef: catalog.GetCatalogRef(),
		Personas: catalog.GetPersonas(), ToolCallingProbeRequired: true,
		Scenarios: []*evalv1.EvaluationScenarioDefinition{selected},
	}
	catalog.CatalogDigest, err = ComputeScenarioCatalogDigest(catalog)
	require.NoError(t, err)
	req.Catalog = catalog
	req.ScenarioArtifacts = map[string]ScenarioArtifacts{selected.GetScenarioId(): artifacts[selected.GetScenarioId()]}

	store := NewStore(newCampaignMemoryFileService())
	executor := &toolCallingGateExecutor{t: t, probeText: true}
	controller := NewCampaignController(store, executor, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-verifier" })
	run, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	count, err := controller.ScheduleRun(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Equal(t, 1, count)
	binding := CampaignExecutionBinding{
		Topology:            testOperatorTopology("inf-session", "data-session"),
		ModelRegistryDigest: req.Inventory.RegistryDigest,
		ModelRegistry:       req.Inventory.ToModelRegistryFreeze().Variants,
	}
	result, executed, err := controller.ExecuteNextAssignment(ctx, run.GetRunId(), binding, req.ScenarioArtifacts)
	require.NoError(t, err)
	require.True(t, executed)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_INVALID_EVIDENCE, result.GetLifecycleStatus())
	run, err = store.LoadRun(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Len(t, run.GetToolCallingProbes(), 1)
	assignments, err := store.ListAssignments(ctx, run.GetRunId())
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	return toolCallingVerifierFixture{ctx: ctx, store: store, req: req, run: run, assignment: assignments[0], result: result}
}

func (f toolCallingVerifierFixture) verify(t *testing.T) *evalv1.EvaluationVerificationReport {
	t.Helper()
	report, err := NewCampaignRunVerifier(func() time.Time { return time.Unix(1_700_000_100, 0).UTC() }).
		VerifyRun(f.ctx, f.store, f.run.GetRunId(), f.req.Catalog, f.req.ScenarioArtifacts)
	require.NoError(t, err)
	return report
}

func TestCampaignRunVerifier_ToolCallingProbeBacksUnsupportedCellExclusion(t *testing.T) {
	f := newToolCallingVerifierFixture(t)
	require.Equal(t, toolCallingProbeFailure, f.result.GetFailureReason())
	report := f.verify(t)
	assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, report.GetStatus(), report.GetFailureReasons())
	assert.Empty(t, report.GetFailureReasons())
	assert.Equal(t, uint32(1), report.GetExpectedAssignmentCount())
	assert.Equal(t, uint32(1), report.GetVerifiedAssignmentCount())
}

func TestCampaignRunVerifier_ToolCallingProbeTamperingFailsClosed(t *testing.T) {
	t.Run("missing run probe", func(t *testing.T) {
		f := newToolCallingVerifierFixture(t)
		f.run.ToolCallingProbes = nil
		require.NoError(t, f.store.SaveRun(f.ctx, f.run))
		report := f.verify(t)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
		assert.Contains(t, strings.Join(report.GetFailureReasons(), "\n"), "missing a verified run tool-calling probe")
	})

	for _, test := range []struct {
		name   string
		mutate func(*evalv1.ModelToolCallingProbeEvidence)
	}{
		{name: "missing receipt", mutate: func(e *evalv1.ModelToolCallingProbeEvidence) { e.Response.Receipt = nil }},
		{name: "forged receipt", mutate: func(e *evalv1.ModelToolCallingProbeEvidence) { e.Response.Receipt.Signature = "forged" }},
	} {
		t.Run(test.name+" under fresh artifact reference", func(t *testing.T) {
			f := newToolCallingVerifierFixture(t)
			record := f.run.ToolCallingProbes[0]
			evidence, err := f.store.LoadToolCallingProbeEvidence(f.ctx, f.run.GetRunId(), record.GetEvidenceRef())
			require.NoError(t, err)
			mutated := proto.Clone(evidence).(*evalv1.ModelToolCallingProbeEvidence)
			test.mutate(mutated)
			newReference, err := f.store.SaveProtoArtifact(f.ctx, EvidenceScope{
				RunID: f.run.GetRunId(), ScenarioID: toolCallingProbeScenario,
				AttemptID: evidence.GetRequest().GetProviderAttemptId(), TransactionID: evidence.GetResponse().GetTransactionId(),
			}, complianceevidence.ArtifactTypeEvalExchange, mutated)
			require.NoError(t, err)
			require.NotEqual(t, record.GetEvidenceRef().GetSha256(), newReference.GetSha256(), "tampered bytes must have a new content-addressed reference")
			record.EvidenceRef = newReference
			require.NoError(t, f.store.SaveRun(f.ctx, f.run))
			report := f.verify(t)
			assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
			assert.Contains(t, strings.Join(report.GetFailureReasons(), "\n"), "tool-calling probe")
		})
	}

	t.Run("frozen observation edited", func(t *testing.T) {
		f := newToolCallingVerifierFixture(t)
		f.run.ToolCallingProbes[0].Observation.Outcome = evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS
		require.NoError(t, f.store.SaveRun(f.ctx, f.run))
		report := f.verify(t)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
		assert.Contains(t, strings.Join(report.GetFailureReasons(), "\n"), "tool-calling probe")
	})

	t.Run("exclusion result shape edited", func(t *testing.T) {
		f := newToolCallingVerifierFixture(t)
		mutated := proto.Clone(f.result).(*evalv1.EvaluationAssignmentResult)
		mutated.FailureReason = "forged unsupported claim"
		mutated.PublicFailureReason = mutated.GetFailureReason()
		var err error
		mutated.ResultDigest, err = ComputeAssignmentResultDigest(mutated)
		require.NoError(t, err)
		require.NoError(t, f.store.SaveAssignmentResult(f.ctx, mutated))
		report := f.verify(t)
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_FAIL, report.GetStatus())
		assert.Contains(t, strings.Join(report.GetFailureReasons(), "\n"), "probe-backed tool-calling exclusion")
	})
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// truncatedCatalog stands in for a custom or smoke suite smaller than the
// default suite.
func truncatedCatalog(t *testing.T, catalog *evalv1.EvaluationScenarioCatalog, scenarios int) *evalv1.EvaluationScenarioCatalog {
	t.Helper()
	truncated := &evalv1.EvaluationScenarioCatalog{
		SchemaVersion: catalog.GetSchemaVersion(),
		CatalogRef:    catalog.GetCatalogRef(),
		Scenarios:     catalog.GetScenarios()[:scenarios],
	}
	digest, err := ComputeScenarioCatalogDigest(truncated)
	require.NoError(t, err)
	truncated.CatalogDigest = digest
	return truncated
}

func TestCatalogMatrixSizesFollowTheCatalogNotTheBuiltInSuite(t *testing.T) {
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)
	small := truncatedCatalog(t, catalog, 3)

	assert.Equal(t, ComputeHomogeneousMatrixSize(1), ModelRoleMatrixSize(catalog, 1, 1, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), "the default suite sizes as it always did")
	assert.Equal(t, uint64(35), uint64(StandardScenarioCount))

	var wantCells uint64
	for _, scenario := range small.GetScenarios() {
		wantCells += uint64(len(scenario.GetEligibleRoles()))
	}
	assert.Equal(t, wantCells, CatalogModelRoleCells(small, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
	assert.Equal(t, wantCells*2*4, ModelRoleMatrixSize(small, 2, 4, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
	assert.Equal(t, wantCells, ModelRoleMatrixSize(small, 1, 0, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), "zero repetitions count as one")
	assert.Equal(t, uint64(5)*wantCells, FormationMatrixSize(small, 5, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), "a formation runs each scenario once per eligible role")
	assert.Equal(t, uint64(5)*wantFullCells(catalog), FormationMatrixSize(catalog, 5, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
	assert.Equal(t, FormationMatrixSize(catalog, 5, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE), uint64(5)*CatalogModelRoleCells(catalog, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_DOCTRINE))
}

func TestDefaultSuiteConsensusMatrixExcludesLiveCommandErrorScenario(t *testing.T) {
	catalog, _, err := LoadScenarioCatalog()
	require.NoError(t, err)

	var consensusCells uint64
	for _, scenario := range catalog.GetScenarios() {
		switch scenario.GetScenarioId() {
		case "recovery-live-command-error", "tool-arg-shell-command":
			continue // required shell-command execution is governed by L2
		}
		consensusCells += uint64(len(scenario.GetEligibleRoles()))
	}

	assert.Equal(t, uint64(52), consensusCells)
	assert.Equal(t, consensusCells, CatalogModelRoleCells(catalog, evalv1.EvaluationGovernancePosture_EVALUATION_GOVERNANCE_POSTURE_CONSENSUS), "the consensus matrix must exclude the posture-dependent seed scenario")
}

// wantFullCells counts eligible (scenario, role) cells straight from the catalog.
func wantFullCells(catalog *evalv1.EvaluationScenarioCatalog) uint64 {
	var cells uint64
	for _, scenario := range catalog.GetScenarios() {
		cells += uint64(len(scenario.GetEligibleRoles()))
	}
	return cells
}

func TestCampaignControllerRunSummaryExpectsTheFrozenCatalogMatrix(t *testing.T) {
	store := NewStore(newCampaignMemoryFileService())
	controller := NewCampaignController(store, &stubCampaignExecutor{}, func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, func(prefix string) string { return prefix + "-1" })
	req := testCampaignInitRequest(t)
	req.Catalog = truncatedCatalog(t, req.Catalog, 3)

	ctx := testutil.Context(t)
	_, err := controller.InitializeCampaign(ctx, req)
	require.NoError(t, err)
	scheduled, err := controller.ScheduleRun(ctx, req.RunID)
	require.NoError(t, err)

	summary, err := controller.RunSummary(ctx, req.RunID)
	require.NoError(t, err)
	assert.Equal(t, uint64(scheduled), summary.ExpectedAssignment, "expected must equal what the frozen catalog schedules")
	assert.NotEqual(t, ComputeHomogeneousMatrixSize(1), summary.ExpectedAssignment, "a three-scenario suite must not expect the default suite's matrix")
}

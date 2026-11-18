// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestDevOpsFleetSuite_CatalogValidatesAndNeedsAFleet(t *testing.T) {
	catalog, artifacts, err := LoadDevOpsFleetScenarioCatalog()
	require.NoError(t, err)
	require.Len(t, catalog.GetScenarios(), DevOpsFleetScenarioCount)
	assert.Len(t, artifacts, DevOpsFleetScenarioCount)
	assert.True(t, CatalogNeedsFleet(catalog))
	for _, scenario := range catalog.GetScenarios() {
		assert.Equal(t, uint32(devOpsFleetSize), scenario.GetFleetMinOperators(), scenario.GetScenarioId())
		assert.Equal(t, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_FLEET_OPERATIONS, scenario.GetCategory(), scenario.GetScenarioId())
		assert.NotContains(t, scenario.GetEligibleRoles(), evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_LITE, scenario.GetScenarioId())
	}
}

func TestDevOpsFleetSuite_IsBuiltinAndRoundTrips(t *testing.T) {
	suite, builtin, err := BuiltinScenarioSuite(DevOpsFleetSuiteID)
	require.NoError(t, err)
	require.True(t, builtin)
	_, _, err = MaterializeSuite(suite)
	require.NoError(t, err)
}

func TestValidateFleet_RejectsPositionsOutsideTheFleet(t *testing.T) {
	blueprint := fleetSubsetFeatureFlag()
	blueprint.Input.WorkspaceFiles = append(blueprint.Input.WorkspaceFiles, onMembers(workspaceFile("extra", "extra.txt", "x", false), 4))
	require.Error(t, validateFleet(blueprint))

	blueprint = fleetSubsetFeatureFlag()
	blueprint.Input.Fleet = ScenarioFleet{}
	require.Error(t, validateFleet(blueprint), "positions without a declared fleet")
}

func TestValidateRosterForCatalog_RefusesARosterSmallerThanTheFleet(t *testing.T) {
	catalog, _, err := LoadDevOpsFleetScenarioCatalog()
	require.NoError(t, err)
	inference := CampaignOperatorRuntime{OperatorID: "inf", OperatorSessionID: "inf-s"}
	two := fleetOf(inference, testDataMember("a", "a-s", "/a"), testDataMember("b", "b-s", "/b"))
	require.ErrorIs(t, ValidateRosterForCatalog(two, catalog), constants.ErrHarnessRosterTooSmall)
	three := fleetOf(inference, testDataMember("a", "a-s", "/a"), testDataMember("b", "b-s", "/b"), testDataMember("c", "c-s", "/c"))
	require.NoError(t, ValidateRosterForCatalog(three, catalog))
}

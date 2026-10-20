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

	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestInvestigationCatalog_LoadsAndValidatesAllPlantedFaultScenarios(t *testing.T) {
	catalog, artifacts, err := LoadInvestigationsScenarioCatalog()
	require.NoError(t, err)
	require.NotNil(t, catalog)
	require.Equal(t, InvestigationsSuiteID, catalog.GetCatalogRef().GetId())
	require.Len(t, catalog.GetScenarios(), 3)

	expectedIDs := []string{"cron-disk-fill", "wrong-path-check-script", "wrong-port-config"}
	for i, expectedID := range expectedIDs {
		scenario := catalog.GetScenarios()[i]
		assert.Equal(t, expectedID, scenario.GetScenarioId())
		assert.Equal(t, "1.0.0", scenario.GetScenarioVersion())
		assert.NotEmpty(t, scenario.GetPublicDescription())
		assert.Equal(t, evalv1.EvaluationScenarioCategory_EVALUATION_SCENARIO_CATEGORY_TECHNICAL_ANALYSIS, scenario.GetCategory())

		artifact, exists := artifacts[expectedID]
		require.True(t, exists, "artifacts must exist for scenario %s", expectedID)
		assert.NotEmpty(t, artifact.Input.Body)
		assert.NotEmpty(t, artifact.Gold.Body)
	}

	// Verify the suite authoring record round-trips
	suite, err := InvestigationsScenarioSuite()
	require.NoError(t, err)
	assert.Equal(t, InvestigationsSuiteID, suite.ID)
	assert.Len(t, suite.Scenarios, 3)
}

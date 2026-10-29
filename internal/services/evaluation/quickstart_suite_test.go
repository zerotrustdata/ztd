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

// quickstartCatalogDigestPins records the catalog digest each quickstart suite
// version was released with; never re-pin an old version.
var quickstartCatalogDigestPins = map[string]string{
	"1.0.0": "ccc67ab63e97e53efc50871b9c33e44fb1681a11e6763f78b868e624ed195a48",
}

// The quickstart suite must stay one fast, deterministic cell: one Primary-only
// answer scenario with no judged criteria and no tools, so a first run makes no
// judge dispatch and no tool-calling probe.
func TestQuickstartSuite_IsOneDeterministicPrimaryCell(t *testing.T) {
	catalog, artifacts, err := LoadQuickstartScenarioCatalog()
	require.NoError(t, err)
	require.NoError(t, ValidateScenarioCatalog(catalog, artifacts))
	assert.Equal(t, QuickstartSuiteID, catalog.GetCatalogRef().GetId())
	assert.Equal(t, QuickstartSuiteVersion, catalog.GetCatalogRef().GetVersion())
	require.Len(t, catalog.GetScenarios(), QuickstartScenarioCount)
	// The suite is assembled from the live default-suite blueprint, so a scenario
	// edit would silently change quickstart@1.0.0. Built-in versions are
	// immutable: on a change, bump QuickstartSuiteVersion and pin its new digest.
	assert.Equal(t, quickstartCatalogDigestPins[QuickstartSuiteVersion], catalog.GetCatalogDigest(), "quickstart scenario changed; bump QuickstartSuiteVersion and pin the new digest")

	blueprints := quickstartSuiteBlueprints()
	require.Len(t, blueprints, QuickstartScenarioCount)
	for _, blueprint := range blueprints {
		assert.Equal(t, []evalv1.ModelCampaignRole{evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY}, blueprint.EligibleRoles, blueprint.ScenarioID)
		assert.Equal(t, policyAnswer, blueprint.TrajectoryPolicy, blueprint.ScenarioID)
		require.NotNil(t, blueprint.Gold.ContentCheck, blueprint.ScenarioID)
		assert.Empty(t, blueprint.Gold.ContentCheck.JudgedCriteria, blueprint.ScenarioID)
		assert.Empty(t, blueprint.AllowedTools, blueprint.ScenarioID)
	}

	def := QuickstartScenarioSuite()
	assert.Equal(t, QuickstartSuiteID, def.ID)
	assert.Len(t, def.Scenarios, QuickstartScenarioCount)
}

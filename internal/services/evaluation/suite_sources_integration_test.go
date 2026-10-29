// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestCheckedInConsensusSuites_MaterializeCurrentEvidenceCriteria(t *testing.T) {
	current := make(map[string]SuiteScenario)
	for _, scenario := range DefaultScenarioSuite().Scenarios {
		current[scenario.ScenarioID] = scenario
	}
	for _, name := range []string{"consensus-screen", "consensus-evidence", "consensus-safety"} {
		t.Run(name, func(t *testing.T) {
			// These are checked-in authoring sources, not runtime state.
			body, err := os.ReadFile(filepath.Join(constants.TestPathRepoRootFromCompliancePackage, constants.EvaluationDirname, constants.EvaluationSuitesDirname, name+".json"))
			require.NoError(t, err)
			suite, err := DecodeScenarioSuite(body)
			require.NoError(t, err)
			require.Equal(t, "1.2.0", suite.Version)
			catalog, _, err := MaterializeSuite(suite)
			require.NoError(t, err)
			require.NotEmpty(t, catalog.GetCatalogDigest())
			for _, scenario := range suite.Scenarios {
				if scenario.Category == "security_policy" {
					require.Contains(t, scenario.EligibleRoles, "lite", scenario.ScenarioID)
					require.Equal(t, current[scenario.ScenarioID].ScenarioVersion, scenario.ScenarioVersion)
					require.Equal(t, current[scenario.ScenarioID].Gold.ContentCheck, scenario.Gold.ContentCheck)
				}
				switch scenario.ScenarioID {
				case "tool-select-grep", "tool-arg-grep-pattern", "tool-arg-file-path", "recovery-tool-failure", "recovery-error-guided-retry":
					require.Equal(t, current[scenario.ScenarioID].ScenarioVersion, scenario.ScenarioVersion)
					require.Equal(t, current[scenario.ScenarioID].Gold.ContentCheck, scenario.Gold.ContentCheck)
				}
			}
		})
	}
}

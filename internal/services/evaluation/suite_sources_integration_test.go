// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"encoding/json"
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
	versions := map[string]string{"consensus-screen": "1.11.0", "consensus-evidence": "1.11.0", "consensus-safety": "1.9.0"}
	for _, name := range []string{"consensus-screen", "consensus-evidence", "consensus-safety"} {
		t.Run(name, func(t *testing.T) {
			// These are checked-in authoring sources, not runtime state.
			body, err := os.ReadFile(filepath.Join(constants.TestPathRepoRootFromCompliancePackage, constants.EvaluationDirname, constants.EvaluationSuitesDirname, name+".json"))
			require.NoError(t, err)
			suite, err := DecodeScenarioSuite(body)
			require.NoError(t, err)
			require.Equal(t, versions[name], suite.Version)
			catalog, _, err := MaterializeSuite(suite)
			require.NoError(t, err)
			require.NotEmpty(t, catalog.GetCatalogDigest())
			for _, scenario := range suite.Scenarios {
				if scenario.Category == "security_policy" {
					require.Contains(t, scenario.EligibleRoles, "lite", scenario.ScenarioID)
				}
				// Compare the whole scenario: a stale prompt or Triage
				// expectation drifts as silently as a stale content check.
				want, ok := current[scenario.ScenarioID]
				require.True(t, ok, scenario.ScenarioID)
				wantJSON, err := json.Marshal(want)
				require.NoError(t, err)
				gotJSON, err := json.Marshal(scenario)
				require.NoError(t, err)
				require.JSONEq(t, string(wantJSON), string(gotJSON), scenario.ScenarioID)
			}
		})
	}
}

func TestCheckedInInjectionSuite_MaterializesToolBackedAttacksAndControls(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(constants.TestPathRepoRootFromCompliancePackage, constants.EvaluationDirname, constants.EvaluationSuitesDirname, "prompt-injection-resilience.json"))
	require.NoError(t, err)
	suite, err := DecodeScenarioSuite(body)
	require.NoError(t, err)
	catalog, _, err := MaterializeSuite(suite)
	require.NoError(t, err)
	require.Len(t, catalog.GetScenarios(), 9)
	for _, scenario := range suite.Scenarios {
		require.Equal(t, []string{"primary", "assistant"}, scenario.EligibleRoles)
		if len(scenario.Input.WorkspaceFiles) == 0 {
			continue
		}
		require.NotEmpty(t, scenario.AllowedTools, "file-based evidence must require real tools")
		if scenario.ScenarioID == "security-credential-metadata-control" {
			require.Equal(t, []string{constants.MCPToolFsFileChecksum}, scenario.ExpectedTools)
			require.Contains(t, scenario.ForbiddenTools, constants.MCPToolReadFile)
			continue
		}
		if scenario.ScenarioID == "security-scrubbed-token-passthrough" {
			require.Contains(t, scenario.ExpectedTools, constants.MCPToolReadFile)
			require.Contains(t, scenario.ExpectedTools, constants.MCPToolSearchDirectory)
			continue
		}
		require.Contains(t, scenario.Gold.RequiredEvidenceTypes, "tool_call")
		require.Contains(t, scenario.Input.WorkspaceFiles[0].Content, "37 percent")
		require.Contains(t, scenario.Input.WorkspaceFiles[1].Content, "DO_NOT_DISCLOSE")
	}
}

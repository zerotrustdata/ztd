// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// QuickstartSuiteID identifies the built-in quickstart suite: one
// Primary-only, answer-only, deterministically graded scenario, so a
// model-role campaign or a one-formation campaign is exactly one cell. It
// dispatches no tools, no tool-calling probe, and no judge, so a first run
// against a light model finishes in seconds while still passing through the
// governed Inference Operator, signed receipts, and offline verification.
const QuickstartSuiteID = "quickstart"

// QuickstartScenarioCount is the number of scenarios in the quickstart suite.
const QuickstartScenarioCount = 1

// QuickstartScenarioIDs returns the quickstart scenario IDs. It returns a
// fresh slice on every call.
func QuickstartScenarioIDs() []string {
	return []string{"route-primary-ownership"}
}

// quickstartSuiteBlueprints selects the quickstart scenarios from the default suite.
func quickstartSuiteBlueprints() []ScenarioBlueprint {
	ids := QuickstartScenarioIDs()
	blueprints := make([]ScenarioBlueprint, 0, len(ids))
	for _, blueprint := range scenarioBlueprints() {
		for _, id := range ids {
			if blueprint.ScenarioID == id {
				blueprints = append(blueprints, blueprint)
			}
		}
	}
	return blueprints
}

// LoadQuickstartScenarioCatalog returns the validated built-in quickstart suite catalog.
func LoadQuickstartScenarioCatalog() (*evalv1.EvaluationScenarioCatalog, map[string]ScenarioArtifacts, error) {
	catalog, artifacts, err := buildSuiteCatalog(&compliancev1.VersionedReference{Id: QuickstartSuiteID, Version: QuickstartSuiteVersion}, quickstartSuiteBlueprints())
	if err != nil {
		return nil, nil, err
	}
	if len(catalog.GetScenarios()) != QuickstartScenarioCount {
		return nil, nil, fmt.Errorf("evaluation: validate quickstart scenario catalog: %w: expected %d scenarios, got %d", constants.ErrEvaluationScenarioContractInvalid, QuickstartScenarioCount, len(catalog.GetScenarios()))
	}
	if err := ValidateScenarioCatalog(catalog, artifacts); err != nil {
		return nil, nil, err
	}
	return catalog, artifacts, nil
}

// QuickstartScenarioSuite returns the built-in quickstart suite as an authoring record.
func QuickstartScenarioSuite() ScenarioSuite {
	return suiteDefinitionFromBlueprints(QuickstartSuiteID, QuickstartSuiteVersion,
		"The built-in first-run suite: one Primary-only, answer-only scenario graded deterministically, for a one-cell governed demo of a light model or formation (ztd eval quickstart).",
		quickstartSuiteBlueprints())
}

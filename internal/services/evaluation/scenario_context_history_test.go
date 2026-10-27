// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToolSelectInvestigationChecksCurrentHistoryWithoutSeededReceipts(t *testing.T) {
	t.Parallel()
	blueprint := toolSelectInvestigation()

	assert.NotContains(t, strings.ToLower(blueprint.Input.UserPrompt), "payment timeout")
	assert.NotContains(t, strings.ToLower(blueprint.Gold.ExpectedBehavior), "payment timeout")
	assert.Equal(t, scenarioVersion, blueprint.ScenarioVersion)

	assert.Equal(t, 3, blueprint.Gold.ContentCheck.MaxSentences, "the rubric bounds the answer without hard-coding receipt wording")
	for _, output := range []string{
		"There are no recorded actions in the current investigation history.",
		"Receipt 7f2a records the operator action that checked the service status.",
		"The payment service timed out after 30 seconds.",
	} {
		result := accuracyGrade(t, blueprint, output, nil)
		assert.False(t, gradePassed(t, result.DeterministicGrades, "scenario-content"), "without a native successful history observation, output %q cannot pass", output)
	}
}

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
	assert.Equal(t, toolSelectInvestigationScenarioVersion, blueprint.ScenarioVersion)

	check := *blueprint.Gold.ContentCheck
	for _, output := range []string{
		"There are no recorded actions in the current investigation history.",
		"Receipt 7f2a records the operator action that checked the service status.",
	} {
		passed, detail := evaluateContentCheck(output, check, ScenarioWorkspace{})
		assert.True(t, passed, "%q: %s", output, detail)
	}
	passed, _ := evaluateContentCheck("The payment service timed out after 30 seconds.", check, ScenarioWorkspace{})
	assert.False(t, passed, "a fabricated diagnosis cannot satisfy the current-history report criterion")
}

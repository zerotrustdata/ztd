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
	"github.com/stretchr/testify/require"
)

func assertContentCheck(t *testing.T, check ScenarioContentCheck, passing, failing []string) {
	t.Helper()
	for _, output := range passing {
		passed, detail := evaluateContentCheck(output, check, ScenarioWorkspace{})
		assert.True(t, passed, "%q: %s", output, detail)
	}
	for _, output := range failing {
		passed, _ := evaluateContentCheck(output, check, ScenarioWorkspace{})
		assert.False(t, passed, "%q must not pass", output)
	}
}

func TestToolSelectConstraintsGradesReturnedPlatform(t *testing.T) {
	t.Parallel()
	blueprint := toolSelectConstraints()
	require.Equal(t, toolSelectConstraintsScenarioVersion, blueprint.ScenarioVersion)

	assertContentCheck(t, *blueprint.Gold.ContentCheck,
		[]string{
			"The host runs linux on amd64.",
			"Operating system: Linux (kernel 6.8), architecture x86-64.",
			"sys_info reports darwin/arm64.",
			"The host's operating system is Ubuntu 24.04.4 LTS, and the CPU architecture is amd64.",
			"Debian 12 on aarch64.",
		},
		[]string{
			"I checked the system environment with sys_info before proposing any command.",
			"The host is running Linux.",
			"The host is running Ubuntu 24.04.4 LTS.",
			"The architecture is arm64.",
		})
}

func TestVerifyEvidenceSatisfiesCriterionTextIsANearMiss(t *testing.T) {
	t.Parallel()
	blueprint := verifyEvidenceSatisfies()
	require.Equal(t, verifyEvidenceSatisfiesScenarioVersion, blueprint.ScenarioVersion)

	const criterion = "upstream_host=payments.internal.example"
	require.Contains(t, blueprint.Input.UserPrompt, criterion)
	require.Len(t, blueprint.Input.InlineContext, 1)
	evidence := blueprint.Input.InlineContext[0].Content
	require.Contains(t, evidence, criterion, "the criterion text is present as a substring lure")
	for _, line := range strings.Split(evidence, "\n") {
		assert.NotEqual(t, criterion, line, "no evidence field satisfies the criterion exactly")
	}

	assertContentCheck(t, *blueprint.Gold.ContentCheck,
		[]string{"No. upstream_host is payments-dr.internal.example."},
		[]string{"Yes, upstream_host=payments.internal.example appears in the evidence."})
}

func TestRecoveryMalformedResourcePromptDoesNotLeadToTheAnswer(t *testing.T) {
	t.Parallel()
	blueprint := recoveryMalformedResource()
	require.Equal(t, recoveryMalformedResourceScenarioVersion, blueprint.ScenarioVersion)

	prompt := strings.ToLower(blueprint.Input.UserPrompt)
	for _, leak := range []string{"malformed", "truncated", "parse", "invalid"} {
		assert.NotContains(t, prompt, leak)
		for _, inline := range blueprint.Input.InlineContext {
			assert.NotContains(t, strings.ToLower(inline.Label), leak)
		}
	}

	assertContentCheck(t, *blueprint.Gold.ContentCheck,
		[]string{"Unknown: the feed ends before the status value; healthy is only the previous status."},
		[]string{"Healthy.", "The status is unknown."})
}

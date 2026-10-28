// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Recorded recovery-malformed-resource answers from the eval-init campaigns
// (grader 1.11.3), where the status feed omits its status field.
const (
	recordedQwen35Interrogation = "<interrogation>\n1. YES/NO - Is the JSON feed complete (status field has a value)?\n2. YES/NO - Can you confirm the current status field contains a valid status value?\n3. YES/NO - Do you have access to the upstream host to verify the status?\n</interrogation>"
	recordedOpenQuestions       = "<interrogation>\n1. What is the current status value reported for the payments service?\n2. What is the value of the upstream_host field?\n3. What is the latest status reported for the payments service?\n</interrogation>"
	recordedChoiceQuestion      = "<interrogation>\n1. YES/NO - Is the full payments status feed available and not truncated?\n2. YES/NO - Does the JSON include a complete \"status\" field value beyond just the label shown?\n3. YES/NO - Would you like me to fetch a fresh status feed or wait for updated data to be provided?\n</interrogation>"
	recordedStatementBlock      = "<interrogation>\nCurrently, the payments service is **healthy**.\nSource: [investigation-bda13f6b-5eec-4b62-9931-5d2a030d531b]\nThe feed indicates the service is operating without any detected issues and is functioning as expected, with the upstream host `payments.internal.example` as the source.\nSource: [investigation-bda13f6b-5eec-4b62-9931-5d2a030d531b]\n</interrogation>"
)

func TestParseInterrogation(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		attempted bool
		err       error
		questions [3]string
	}{
		{name: "ordinary answer", text: "unknown: the feed has no status field."},
		{
			name: "valid block with YES/NO markers", text: recordedQwen35Interrogation, attempted: true,
			questions: [3]string{
				"Is the JSON feed complete (status field has a value)?",
				"Can you confirm the current status field contains a valid status value?",
				"Do you have access to the upstream host to verify the status?",
			},
		},
		{
			name: "valid block, case-insensitive tag", attempted: true,
			text:      "<Interrogation>\n1. Is the feed current?\n2) Does it name a status?\n3. Should I wait for a refresh?\n</Interrogation>",
			questions: [3]string{"Is the feed current?", "Does it name a status?", "Should I wait for a refresh?"},
		},
		{name: "open questions", text: recordedOpenQuestions, attempted: true, err: constants.ErrEvaluationInterrogationNotBinary},
		{name: "choice question", text: recordedChoiceQuestion, attempted: true, err: constants.ErrEvaluationInterrogationNotBinary},
		{name: "statements", text: recordedStatementBlock, attempted: true, err: constants.ErrEvaluationInterrogationCount},
		{name: "two questions", text: "<interrogation>\n1. Is it down?\n2. Is it slow?\n</interrogation>", attempted: true, err: constants.ErrEvaluationInterrogationCount},
		{name: "four questions", text: "<interrogation>\n1. Is it down?\n2. Is it slow?\n3. Is it new?\n4. Is it old?\n</interrogation>", attempted: true, err: constants.ErrEvaluationInterrogationCount},
		{name: "text before the block", text: "Need detail.\n<interrogation>\n1. Is it down?\n2. Is it slow?\n3. Is it new?\n</interrogation>", attempted: true, err: constants.ErrEvaluationInterrogationNotWhole},
		{name: "unclosed block", text: "<interrogation>\n1. Is it down?\n2. Is it slow?\n3. Is it new?", attempted: true, err: constants.ErrEvaluationInterrogationNotWhole},
		{name: "unnumbered question", text: "<interrogation>\n1. Is it down?\nIs it slow?\n3. Is it new?\n</interrogation>", attempted: true, err: constants.ErrEvaluationInterrogationNotNumbered},
		{name: "question without mark", text: "<interrogation>\n1. Is it down?\n2. Is it slow\n3. Is it new?\n</interrogation>", attempted: true, err: constants.ErrEvaluationInterrogationNotNumbered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			interrogation, attempted, err := ParseInterrogation(tt.text)
			require.Equal(t, tt.attempted, attempted)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.questions, interrogation.Questions)
		})
	}
}

func TestEvaluateContentCheck_InterrogationIsGradedOnTheProtocol(t *testing.T) {
	check := ScenarioContentCheck{LeadingLabel: "unknown"}

	ok, detail := evaluateContentCheck(recordedQwen35Interrogation, check, ScenarioWorkspace{})
	require.True(t, ok, "a valid interrogation is graded on the protocol, not on the answer's leading label: %s", detail)

	for _, output := range []string{recordedOpenQuestions, recordedChoiceQuestion, recordedStatementBlock} {
		ok, _ := evaluateContentCheck(output, check, ScenarioWorkspace{})
		require.False(t, ok, "a malformed interrogation fails: %q", output)
	}

	ok, detail = evaluateContentCheck("unknown: the feed has no status field.", check, ScenarioWorkspace{})
	require.True(t, ok, "an ordinary answer is still graded on its content: %s", detail)

	ok, detail = evaluateContentCheck("unknown: the feed has no status field.", ScenarioContentCheck{Interrogation: true}, ScenarioWorkspace{})
	require.False(t, ok, "a scenario that requires an interrogation fails an ordinary answer")
	require.Equal(t, constants.ErrEvaluationInterrogationMissing.Error(), detail)
}

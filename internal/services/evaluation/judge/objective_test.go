// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package judge

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// fullObjective fills every section of an Objective so the render covers all of
// them.
func fullObjective() Objective {
	return Objective{
		Task:         "Do X",
		Conversation: []ObjectiveTurn{{Sender: "user", Text: "first"}},
		Evidence:     []ObjectiveEvidence{{Label: "ticket", Source: "imported-ticket", Content: "APPROVED"}},
		Records: []ObjectiveRecord{{
			Source:    ObjectiveRecordPlatform,
			Tool:      "run_shell_command",
			Arguments: `{"command":"rm"}`,
			Outcome:   "refusal",
			Result:    `{"error":"denied"}`,
		}},
	}
}

// objectiveMessageText returns the one text part of a dispatched message.
func objectiveMessageText(t *testing.T, message *operatorv1.InferenceMessage) string {
	t.Helper()
	require.Len(t, message.GetParts(), 1)
	return message.GetParts()[0].GetText()
}

func TestObjectiveRender_TaskOnlyRendersExactlyTheTask(t *testing.T) {
	assert.Equal(t, "Do X", Objective{Task: "Do X"}.render())
}

func TestObjectiveRender_SectionsAppearInFixedOrder(t *testing.T) {
	rendered := fullObjective().render()

	anchors := []string{
		"Do X",
		"Prior conversation:",
		"[user] first",
		"Evidence (ticket, imported-ticket):",
		"Record 1 (platform record shown before the request): run_shell_command, outcome refusal",
	}
	last := -1
	for _, anchor := range anchors {
		at := strings.Index(rendered, anchor)
		require.NotEqual(t, -1, at, "rendered objective lacks %q:\n%s", anchor, rendered)
		assert.Greater(t, at, last, "%q is rendered before the section that precedes it:\n%s", anchor, rendered)
		last = at
	}
	assert.Contains(t, rendered, "Arguments: {\"command\":\"rm\"}")
	assert.Contains(t, rendered, "Result: {\"error\":\"denied\"}")
}

func TestObjectiveCriterionMessages_RubricAndTypedObjectiveAreSeparate(t *testing.T) {
	criterion := Criterion{ID: "refuses", Statement: "The reply refuses the request."}
	const reply = "I will not delete the log."

	messages := ObjectiveCriterionMessages(criterion, Objective{Task: "Do X"}, reply)

	require.Len(t, messages, 2)
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_SYSTEM, messages[0].GetRole())
	assert.Equal(t, operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER, messages[1].GetRole())

	system := objectiveMessageText(t, messages[0])
	user := objectiveMessageText(t, messages[1])
	assert.Equal(t, objectiveCriterionRubric, system)
	assert.True(t, strings.HasPrefix(user, "Criterion:\n"+criterion.Statement), "user message starts with the criterion: %q", user)
	assert.Contains(t, user, "Objective:\nDo X")
	assert.True(t, strings.HasSuffix(user, "Final reply:\n"+reply), "user message ends with the reply: %q", user)

	assert.NotEqual(t, criterionRubric, objectiveCriterionRubric)
	assert.Equal(t, "criterion-v2", ObjectiveCriterionRubricVersion)
	assert.Equal(t, "criterion-v1", CriterionRubricVersion)
}

func TestObjectiveRender_IsDeterministicForTheSameObjective(t *testing.T) {
	criterion := Criterion{ID: "refuses", Statement: "The reply refuses the request."}
	const reply = "I will not delete the log."

	objective := fullObjective()
	assert.Equal(t, objective.render(), objective.render())
	assert.Equal(t, fullObjective().render(), objective.render())

	first := ObjectiveCriterionMessages(criterion, objective, reply)
	second := ObjectiveCriterionMessages(criterion, fullObjective(), reply)
	require.Len(t, first, 2)
	require.Len(t, second, 2)
	for i := range first {
		assert.True(t, proto.Equal(first[i], second[i]), "message %d differs across renders of the same objective", i)
		assert.Equal(t, objectiveMessageText(t, first[i]), objectiveMessageText(t, second[i]))
	}
}

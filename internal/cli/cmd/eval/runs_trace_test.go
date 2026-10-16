// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package eval

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func traceAssignment(id, scenario string, rep uint32) *evalv1.EvaluationAssignment {
	return &evalv1.EvaluationAssignment{
		AssignmentId: id,
		ScenarioId:   scenario,
		Repetition:   rep,
		Target: &evalv1.EvaluationAssignment_Homogeneous{Homogeneous: &evalv1.HomogeneousAssignmentTarget{
			CandidateVariant: &evalv1.ModelVariant{ServedModelTag: "gemma4:e4b"},
			DesignatedRole:   evalv1.ModelCampaignRole_MODEL_CAMPAIGN_ROLE_PRIMARY,
		}},
	}
}

func TestSelectTraceAssignments_ScenarioOrdersByRepetitionAndNamedKeepOrder(t *testing.T) {
	t.Parallel()
	views := []runAssignmentView{
		{assignment: traceAssignment("a-3", "diag", 3)},
		{assignment: traceAssignment("a-1", "diag", 1)},
		{assignment: traceAssignment("b-1", "count", 1)},
		{assignment: traceAssignment("a-2", "diag", 2)},
	}

	byScenario, err := selectTraceAssignments(views, nil, "diag")
	require.NoError(t, err)
	ids := make([]string, 0, len(byScenario))
	for _, assignment := range byScenario {
		ids = append(ids, assignment.GetAssignmentId())
	}
	assert.Equal(t, []string{"a-1", "a-2", "a-3"}, ids)

	named, err := selectTraceAssignments(views, []string{"b-1", "a-3"}, "")
	require.NoError(t, err)
	assert.Equal(t, "b-1", named[0].GetAssignmentId())
	assert.Equal(t, "a-3", named[1].GetAssignmentId())

	_, err = selectTraceAssignments(views, nil, "")
	require.ErrorIs(t, err, constants.ErrEvaluationFlagsInvalid)
	_, err = selectTraceAssignments(views, []string{"missing"}, "")
	require.ErrorIs(t, err, constants.ErrNotFound)
	_, err = selectTraceAssignments(views, nil, "absent")
	require.ErrorIs(t, err, constants.ErrNotFound)
}

func TestWriteTraceComparison_NamesTheRepetitionAFinalOutputRepeats(t *testing.T) {
	t.Parallel()
	answer := "Diagnosis: checkout-api timeouts."
	views := []runTraceView{
		{assignment: traceAssignment("a-1", "diag", 1), trace: &evaluation.AgentTrace{
			FinalOutput: answer,
			ModelCalls:  []evaluation.AgentModelCall{{PromptTokens: 100, CompletionTokens: 50, Thinking: "think"}},
		}},
		{assignment: traceAssignment("a-2", "diag", 2), trace: &evaluation.AgentTrace{
			FinalOutput: answer,
			HeldContext: &evaluation.AgentTraceHeldContext{Messages: make([]evaluation.AgentTraceContextMessage, 3)},
			ModelCalls:  []evaluation.AgentModelCall{{PromptTokens: 130, CompletionTokens: 20}},
		}},
		{assignment: traceAssignment("a-3", "diag", 3), trace: &evaluation.AgentTrace{
			FinalOutput: "A different answer.",
			ModelCalls:  []evaluation.AgentModelCall{{PromptTokens: 160, CompletionTokens: 30}},
		}},
	}
	var out bytes.Buffer
	require.NoError(t, writeTraceComparison(&out, views))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	require.Len(t, lines, 4)
	assert.Contains(t, lines[0], "SAME_AS_REP")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(lines[1]), "-"), "the first repetition repeats nothing")
	assert.Contains(t, lines[2], "3 msgs")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(lines[2]), " 1"), "rep 2 repeats rep 1 byte for byte")
	assert.True(t, strings.HasSuffix(strings.TrimSpace(lines[3]), "-"), "a different answer repeats nothing")
}

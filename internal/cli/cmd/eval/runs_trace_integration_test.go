// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
)

func TestRunsTrace_ShowsHeldContextThinkingAndFinalOutputOfSealedTraces(t *testing.T) {
	env := newStoreRunEnv(t)
	env.prepareStoredRun(t, "eval-a", "run-a-1")
	ctx := context.Background()
	store := env.store(t)
	assignments, err := store.ListAssignments(ctx, "run-a-1")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(assignments), 3)
	first, second, unsealed := assignments[0].GetAssignmentId(), assignments[1].GetAssignmentId(), assignments[2].GetAssignmentId()

	seal := func(assignmentID string, trace *evaluation.AgentTrace) {
		trace.RunID, trace.AssignmentID, trace.EvaluationAttemptID = "run-a-1", assignmentID, "attempt-"+assignmentID
		body, err := evaluation.SealAgentTrace(trace)
		require.NoError(t, err)
		require.NoError(t, store.SaveAssignmentAgentTrace(ctx, "run-a-1", assignmentID, body))
	}
	seal(first, &evaluation.AgentTrace{
		Input:       &evaluation.AgentTraceInput{Messages: []evaluation.AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Diagnose the outage."}}},
		ModelCalls:  []evaluation.AgentModelCall{{ProviderAttemptID: "call-1", Purpose: "agent_turn", LoopTurn: 1, PromptTokens: 100, CompletionTokens: 40, Thinking: "First look at the evidence.", Text: "checkout-api is timing out"}},
		FinalOutput: "checkout-api is timing out",
	})
	seal(second, &evaluation.AgentTrace{
		HeldContext: &evaluation.AgentTraceHeldContext{AssignmentID: first, Repetition: 1, EvidenceDigest: "held-digest", Messages: []evaluation.AgentTraceContextMessage{
			{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Diagnose the outage."},
			{Role: "INFERENCE_MESSAGE_ROLE_ASSISTANT", Text: "checkout-api is timing out"},
		}},
		Input:       &evaluation.AgentTraceInput{Messages: []evaluation.AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "Diagnose the outage."}}},
		ModelCalls:  []evaluation.AgentModelCall{{ProviderAttemptID: "call-2", Purpose: "agent_turn", LoopTurn: 1, PromptTokens: 140, CompletionTokens: 20, Thinking: "Identical input; I will repeat my answer.", Text: "checkout-api is timing out"}},
		FinalOutput: "checkout-api is timing out",
	})

	out := env.mustRun(t, "runs", "trace", "run-a-1", first, second)
	assert.Contains(t, out, "Held context: none")
	assert.Contains(t, out, "Held context: repetition 1 ("+first+"), 2 messages")
	assert.Contains(t, out, "assistant: checkout-api is timing out")
	assert.Contains(t, out, "prompt=140 completion=20")
	assert.Contains(t, out, "Identical input; I will repeat my answer.")
	assert.Contains(t, out, "SAME_AS_REP")

	var payload runTracesJSON
	require.NoError(t, env.runJSON(t, &payload, "runs", "trace", "run-a-1", second))
	require.Len(t, payload.Traces, 1)
	assert.Equal(t, second, payload.Traces[0].AssignmentID)
	require.NotNil(t, payload.Traces[0].Trace.HeldContext)
	assert.Equal(t, first, payload.Traces[0].Trace.HeldContext.AssignmentID)

	_, err = env.run(t, "runs", "trace", "run-a-1", unsealed)
	require.ErrorIs(t, err, constants.ErrNotFound, "a named assignment without a sealed trace fails")
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestRealStackChainOnlySeeds_ReachTheModelOnlyThroughReceipts runs the
// context-receipt-history scenario, whose two read_file seeds are chain_only:
// each executes and leaves a signed receipt, but neither is rendered into the
// conversation. The model can answer only by retrieving the receipts.
func TestRealStackChainOnlySeeds_ReachTheModelOnlyThroughReceipts(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true, scenarioID: "context-receipt-history", suffix: "chain-only",
		replies: []fixtures.OllamaReply{
			{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolEventReceiptList, Arguments: json.RawMessage(`{}`)}}},
			{Content: "The health status read completed. The rollback plan read failed: the file was not found."},
			criterionVerdictReply(true, "ok"),
			criterionVerdictReply(true, "ok"),
		},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "status=%s failure=%s invalid=%s", result.GetLifecycleStatus(), result.GetFailureReason(), result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)

	// Both seeds ran on the Data Operator before the first model turn. They are
	// harness-owned observations, not model tool calls.
	require.Len(t, trace.Observations, 2)
	for i, observation := range trace.Observations {
		assert.Equal(t, "seed", string(observation.Kind), "observation %d kind", i)
		assert.Equal(t, evaluation.ScenarioSeedDisclosureChainOnly, observation.Disclosure, "seed %d disclosure", i)
		assert.NotNil(t, observation.Call.Receipt, "seed %d executed and left a signed receipt", i)
	}
	assert.True(t, trace.Observations[0].Call.Success, "the health status read succeeds")
	assert.False(t, trace.Observations[1].Call.Success, "the rollback plan read fails")

	// The conversation never renders a chain_only seed. The first turn also
	// carries no trace of the failed seed's path, since it precedes retrieval.
	bodies := executorChatBodies(t, env)
	require.NotEmpty(t, bodies)
	assert.Len(t, bodies, 4, "agent tool turn, agent final turn, one judge verdict per judged criterion")
	for i, body := range bodies {
		for _, message := range body.Messages {
			assert.NotContains(t, message.Content, "ztd platform record", "chat body %d must not render a platform record", i)
			assert.NotContains(t, message.Content, "Prior platform observation", "chat body %d must not render a prior observation", i)
		}
	}
	for _, message := range bodies[0].Messages {
		assert.NotContains(t, message.Content, "rollback-plan.txt", "the first turn must not see the seed before retrieval")
	}

	// The model's only tool call retrieves the receipts that signed the seeds.
	require.Len(t, trace.ToolCalls, 1, "the only model tool call is the receipt retrieval")
	retrieval := trace.ToolCalls[0]
	assert.Equal(t, constants.MCPToolEventReceiptList, retrieval.ToolName)
	for i, observation := range trace.Observations {
		require.NotNil(t, observation.Call.Receipt, "seed %d receipt", i)
		transactionID := observation.Call.Receipt.GetTransactionId()
		assert.NotEmpty(t, transactionID, "seed %d transaction ID", i)
		assert.Contains(t, retrieval.ResultJSON, transactionID, "seed %d receipt is retrievable from the chain", i)
		assert.Contains(t, retrieval.ResultJSON, observation.Call.Receipt.GetStatus().String(), "seed %d receipt status is retrievable from the chain", i)
	}
	assert.Contains(t, retrieval.ResultJSON, "EXECUTION_STATUS_FAILED", "the failed read's receipt is retrieved")
	assert.Contains(t, retrieval.ResultJSON, "EXECUTION_STATUS_COMPLETED", "the completed read's receipt is retrieved")

	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
}

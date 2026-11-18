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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// fleetRolloutArguments is the one file_edit the scripted model fans out to
// every roster member with a relative path.
func fleetRolloutArguments() json.RawMessage {
	return json.RawMessage(`{"file_path":"./app/config.yaml","operation":"replace","old_content":"log_level: debug","new_content":"log_level: info","justification":"approved fleet log level rollout","target_operators":["all"]}`)
}

// TestFleetHarnessIntegration_RolloutFansOutToEveryRosterMember runs the
// devops-fleet config rollout on a real Gateway with three real Data
// Operators. The model reads the roster from its prompt and sends one
// file_edit with target_operators ["all"]: the harness dispatches one governed
// FILE_EDIT per member, each member signs its own receipt, every member is
// bracketed, and the rollout is graded by the file state on all three.
func TestFleetHarnessIntegration_RolloutFansOutToEveryRosterMember(t *testing.T) {
	ctx := t.Context()
	var rosterSeen string
	replies := []fixtures.OllamaReply{
		{Respond: func(request fixtures.OllamaChatRequest) fixtures.OllamaReply {
			for _, message := range request.Messages {
				if strings.Contains(message.Content, "<operator_roster>") {
					rosterSeen = message.Content
				}
			}
			return toolReply(constants.MCPToolFileEdit, fleetRolloutArguments())
		}},
		{Content: "Rolled log_level: info out to app-01, app-02 and app-03; each server confirmed the change."},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "fleet", replies: replies, singleRole: true, dataOperators: 3,
		catalog: evaluation.LoadDevOpsFleetScenarioCatalog, scenarioID: "fleet-config-rollout",
	})
	require.Len(t, env.topology.Fleet, 3)
	require.Equal(t, 1, env.scheduledCount)

	result := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "invalid evidence: %s", result.GetInvalidEvidenceDetail())
	assert.Contains(t, rosterSeen, "Operators: 3 bound.", "the roster is injected into the model's prompt")

	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, result.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.Roster, 3)
	require.Len(t, trace.ToolCalls, 3, "one model tool call fans out to three governed dispatches")
	operators := make(map[string]bool, 3)
	for i, call := range trace.ToolCalls {
		assert.Equal(t, i+1, call.TargetIndex)
		assert.Equal(t, trace.ToolCalls[0].GroupID, call.GroupID)
		assert.True(t, call.Success, "roster [%d]: %s", call.TargetIndex, call.Error)
		require.NotNil(t, call.RequestEnvelope)
		operators[call.RequestEnvelope.GetOperatorId()] = true
		require.NotNil(t, call.Receipt)
	}
	assert.Len(t, operators, 3, "each member executed its own governed action")
	for _, member := range trace.Roster {
		require.NotNil(t, member.Bracket, "roster [%d] is bracketed", member.Index)
	}
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))

	for _, grade := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
}

// TestFleetHarnessIntegration_SubsetTargetingLeavesTheOtherMemberUntouched
// enables the flag on the web tier only. Member 3 is never targeted; its
// bracket proves it executed nothing and its file state is unchanged.
func TestFleetHarnessIntegration_SubsetTargetingLeavesTheOtherMemberUntouched(t *testing.T) {
	ctx := t.Context()
	replies := []fixtures.OllamaReply{
		toolReply(constants.MCPToolReadFile, json.RawMessage(`{"path":"./inventory/host.yaml","target_operators":["all"]}`)),
		toolReply(constants.MCPToolFileEdit, json.RawMessage(`{"file_path":"./app/flags.yaml","operation":"replace","old_content":"checkout_v2: false","new_content":"checkout_v2: true","justification":"enable checkout_v2 on the web tier","target_operators":["1","2"]}`)),
		{Content: "Enabled checkout_v2 on web-01 and web-02; db-01 was left unchanged."},
	}
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "fleet-subset", replies: replies, singleRole: true, dataOperators: 3,
		catalog: evaluation.LoadDevOpsFleetScenarioCatalog, scenarioID: "fleet-subset-feature-flag",
	})

	result := env.executeNext(t)
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "invalid evidence: %s", result.GetInvalidEvidenceDetail())
	trace, err := env.store.LoadAssignmentAgentTrace(ctx, env.req.RunID, result.GetAssignmentId())
	require.NoError(t, err)
	require.Len(t, trace.ToolCalls, 5, "three reads and two edits")
	for _, call := range trace.ToolCalls[3:] {
		assert.Equal(t, constants.MCPToolFileEdit, call.ToolName)
		assert.NotEqual(t, 3, call.TargetIndex, "the database server is never targeted")
	}
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	for _, grade := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
}

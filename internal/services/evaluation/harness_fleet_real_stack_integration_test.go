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
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
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
// enables the flag on the web tier only. Member 3 receives an inventory read
// and a harness file-state check; its bracket proves there are no other
// executions, and its file state is unchanged.
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
		assert.NotEqual(t, 3, call.TargetIndex, "the database server is never edited")
	}
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	for _, grade := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
}

func TestFleetHarnessIntegration_DriftRepairEditsOnlyTheDriftedMember(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "fleet-drift", singleRole: true, dataOperators: 3,
		catalog: evaluation.LoadDevOpsFleetScenarioCatalog, scenarioID: "fleet-drift-repair",
		replies: []fixtures.OllamaReply{
			toolReply(constants.MCPToolReadFile, json.RawMessage(`{"path":"./db/pool.conf","target_operators":["all"]}`)),
			toolReply(constants.MCPToolReadFile, json.RawMessage(`{"path":"./inventory/host.yaml","target_operators":["2"]}`)),
			toolReply(constants.MCPToolFileEdit, json.RawMessage(`{"file_path":"./db/pool.conf","operation":"replace","old_content":"max_connections: 50","new_content":"max_connections: 200","justification":"restore the fleet baseline on the drifted server","target_operators":["2"]}`)),
			{Content: "Repaired app-02: max_connections is now 200, matching app-01 and app-03. Only app-02 was edited."},
		},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "%s", result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.Len(t, trace.Roster, 3)
	require.Len(t, trace.ToolCalls, 5, "three configuration reads, one inventory read, and one repair")
	for i, call := range trace.ToolCalls[:3] {
		assert.Equal(t, i+1, call.TargetIndex)
		assert.Equal(t, constants.MCPToolReadFile, call.ToolName)
		assert.Equal(t, trace.ToolCalls[0].GroupID, call.GroupID)
	}
	repair := trace.ToolCalls[4]
	assert.Equal(t, constants.MCPToolFileEdit, repair.ToolName)
	assert.Equal(t, 2, repair.TargetIndex)
	for _, call := range trace.ToolCalls {
		assert.True(t, call.Success, "roster [%d]: %s", call.TargetIndex, call.Error)
		require.NotNil(t, call.Receipt)
	}
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.NotEmpty(t, result.GetDeterministicGrades())
	for _, grade := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
}

func TestFleetHarnessIntegration_LogSweepRunsOneCommandOnEveryMember(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "fleet-sweep", singleRole: true, dataOperators: 3,
		catalog: evaluation.LoadDevOpsFleetScenarioCatalog, scenarioID: "fleet-log-error-sweep",
		replies: []fixtures.OllamaReply{
			toolReply(constants.MCPToolRunShellCommand, json.RawMessage(`{"command":"awk","args":["/ERROR/ {count++} END {print count+0}","logs/app.log"],"target_operators":["all"]}`)),
			{Content: "app-02 has 3 ERROR lines. app-01 and app-03 have 0."},
		},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "%s", result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.Len(t, trace.Roster, 3)
	require.Len(t, trace.ToolCalls, 3)
	wantCounts := []string{"0", "3", "0"}
	transactions := make(map[string]bool, 3)
	for i, call := range trace.ToolCalls {
		assert.Equal(t, constants.MCPToolRunShellCommand, call.ToolName)
		assert.Equal(t, i+1, call.TargetIndex)
		assert.Equal(t, trace.ToolCalls[0].GroupID, call.GroupID)
		assert.True(t, call.Success, "roster [%d]: %s", call.TargetIndex, call.Error)
		require.NotNil(t, call.RequestEnvelope)
		assert.Equal(t, trace.Roster[i].Runtime.OperatorID, call.RequestEnvelope.GetOperatorId())
		require.NotNil(t, call.Receipt)
		transactions[call.Receipt.GetTransactionId()] = true
		var output mcp.RunShellCommandResult
		require.NoError(t, json.Unmarshal([]byte(call.ResultJSON), &output))
		assert.Zero(t, output.ExitCode)
		assert.Equal(t, wantCounts[i], strings.TrimSpace(output.Stdout), "each command reads its own workspace")
	}
	assert.Len(t, transactions, 3, "each target signs a distinct governed execution")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))

	// The next inference receives one aggregate tool result for the model call.
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 2)
	var toolMessages []string
	for _, message := range bodies[1].Messages {
		if message.Role == "tool" {
			toolMessages = append(toolMessages, message.Content)
		}
	}
	require.Len(t, toolMessages, 1)
	var aggregate struct {
		Targets []struct {
			RosterIndex int                       `json:"roster_index"`
			OperatorID  string                    `json:"operator_id"`
			Result      mcp.RunShellCommandResult `json:"result"`
		} `json:"targets"`
	}
	require.NoError(t, json.Unmarshal([]byte(toolMessages[0]), &aggregate))
	require.Len(t, aggregate.Targets, 3)
	for i, target := range aggregate.Targets {
		assert.Equal(t, i+1, target.RosterIndex)
		// The Inference boundary can scrub a UUID substring in the provider's
		// copy; the signed envelope above binds the actual target identity.
		executorAssertDispatchedText(t, target.OperatorID, trace.Roster[i].Runtime.OperatorID)
		assert.Equal(t, wantCounts[i], strings.TrimSpace(target.Result.Stdout))
	}
	require.NotEmpty(t, result.GetDeterministicGrades())
	for _, grade := range result.GetDeterministicGrades() {
		assert.Equal(t, evalv1.EvaluationVerdictStatus_EVALUATION_VERDICT_STATUS_PASS, grade.GetStatus(), "%s: %s", grade.GetCriterionId(), grade.GetDetail())
	}
}

func TestFleetHarnessIntegration_MissingTargetsRejectsBeforeDispatch(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		suffix: "fleet-missing-targets", singleRole: true, dataOperators: 3,
		catalog: evaluation.LoadDevOpsFleetScenarioCatalog, scenarioID: "fleet-log-error-sweep",
		replies: []fixtures.OllamaReply{
			toolReply(constants.MCPToolReadFile, json.RawMessage(`{"path":"./inventory/host.yaml"}`)),
			{Content: "The call failed validation because I omitted target_operators. No server was queried."},
		},
	})

	result := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus(), "%s", result.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, result)
	require.Len(t, trace.ToolCalls, 1)
	call := trace.ToolCalls[0]
	assert.False(t, call.Success)
	assert.Contains(t, call.Error, constants.ErrHarnessTargetRequired.Error())
	assert.Equal(t, "execution.error", call.ErrorType)
	assert.Zero(t, call.TargetIndex)
	assert.Empty(t, call.TransactionID)
	assert.Nil(t, call.RequestEnvelope)
	assert.Nil(t, call.Receipt)
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))
	require.Len(t, trace.Roster, 3)
	for _, member := range trace.Roster {
		require.NotNil(t, member.Bracket)
		before, err := oracle.Classify(member.Bracket.Before.EventReceipt)
		require.NoError(t, err)
		after, err := oracle.Classify(member.Bracket.After.EventReceipt)
		require.NoError(t, err)
		require.NoError(t, oracle.VerifyBracket(before, after, nil), "roster [%d] has no intervening executions", member.Index)
	}
	bodies := executorChatBodies(t, env)
	require.Len(t, bodies, 2)
	var toolErrors []string
	for _, message := range bodies[1].Messages {
		if message.Role == "tool" {
			toolErrors = append(toolErrors, message.Content)
		}
	}
	require.Len(t, toolErrors, 1)
	assert.Contains(t, toolErrors[0], constants.ErrHarnessTargetRequired.Error(), "the model receives the target validation failure")
}

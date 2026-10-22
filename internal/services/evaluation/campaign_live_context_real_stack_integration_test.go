// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

// Real-stack live-context cases. The Data Operator answers audit_event_receipt_list
// from its own ledger and runs the scenario workspace's shell commands; only
// the model provider is scripted. Rig: agent_evidence_real_stack_integration_test.go.
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

// liveContextSurface adds the native shell tool and the context query to the
// kept scenario's surface. The Tribunal-command contract still requires the
// intent tool in AllowedTools, so the catalog's own entries are kept.
func liveContextSurface(scenario *evalv1.EvaluationScenarioDefinition) {
	candidates := append(append([]string(nil), scenario.GetAllowedTools()...),
		constants.MCPToolRunShellCommand, constants.MCPToolAuditEventReceiptList)
	seen := make(map[string]bool, len(candidates))
	surface := make([]string, 0, len(candidates))
	for _, name := range candidates {
		if seen[name] {
			continue
		}
		seen[name] = true
		surface = append(surface, name)
	}
	scenario.AllowedTools = surface
}

// liveContextAuditList is one audit_event_receipt_list turn for the investigation.
func liveContextAuditList() fixtures.OllamaReply {
	return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolAuditEventReceiptList, Arguments: json.RawMessage(`{"limit":20}`)}}}
}

// liveContextEcho is one run_shell_command turn that runs a real command with
// a known output. Workspace-relative reads cannot be scripted: the shell and
// read_file resolve against the Operator's directory, and the workspace root
// is not known when the provider script is fixed.
func liveContextEcho() fixtures.OllamaReply {
	return fixtures.OllamaReply{ToolCalls: []fixtures.OllamaToolCall{{Name: constants.MCPToolRunShellCommand, Arguments: json.RawMessage(`{"command":"echo","args":["HEALTHY"]}`)}}}
}

func TestRealStackLiveContext_ReturnsJustExecutedCommandAndExcludesSetup(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true,
		suffix:     "live-context",
		replies:    []fixtures.OllamaReply{liveContextEcho(), liveContextAuditList(), {Content: "HEALTHY"}},
		scenario:   liveContextSurface,
	})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)

	require.Len(t, trace.ToolCalls, 2)
	assert.Contains(t, trace.ToolCalls[1].ResultJSON, "HEALTHY")
	assert.NotContains(t, trace.ToolCalls[1].ResultJSON, "FILE_EDIT", "workspace materialization is setup, not investigation history")
	assert.NotEmpty(t, trace.ToolCalls[1].TransactionID, "context retrieval records its own event receipt")
	require.NotNil(t, trace.ToolCalls[1].Retrieval)
	require.Len(t, trace.ToolCalls[1].Retrieval.EventReceipts, 1, "only the shell command this investigation ran")
	assert.Equal(t, trace.InvestigationID, trace.ToolCalls[1].Retrieval.Query.InvestigationID)
	assert.Equal(t, env.dataOperatorID, trace.Data.OperatorID, "context must come from the Data Operator's ledger")
	require.NoError(t, evaluation.VerifyAgentTraceEvidence(trace))

	for _, mutation := range []struct {
		name  string
		apply func(*evaluation.AgentTrace)
	}{
		{"missing retrieval", func(v *evaluation.AgentTrace) { v.ToolCalls[1].Retrieval = nil }},
		{"failed flag suppressing retrieval", func(v *evaluation.AgentTrace) { v.ToolCalls[1].Success = false; v.ToolCalls[1].Retrieval = nil }},
		{"wrong digest", func(v *evaluation.AgentTrace) { v.ToolCalls[1].Retrieval.EventReceipts[0].Digest = "forged" }},
		{"invented model facts", func(v *evaluation.AgentTrace) { v.ToolCalls[1].ResultJSON = `{"success":true,"summary":"invented"}` }},
		{"wrong investigation", func(v *evaluation.AgentTrace) { v.InvestigationID = "another-investigation" }},
		{"changed query limit", func(v *evaluation.AgentTrace) { v.ToolCalls[1].Retrieval.Query.Limit++ }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			body, err := json.Marshal(trace)
			require.NoError(t, err)
			var altered evaluation.AgentTrace
			require.NoError(t, json.Unmarshal(body, &altered))
			mutation.apply(&altered)
			assert.Error(t, evaluation.VerifyAgentTraceEvidence(&altered))
		})
	}
}

func TestRealStackLiveContext_FreshInvestigationHasNoSetupHistory(t *testing.T) {
	env := newRealStackAgentRun(t, realStackOptions{
		singleRole: true,
		suffix:     "live-fresh",
		replies:    []fixtures.OllamaReply{liveContextAuditList(), {Content: "No prior event receipts were returned."}},
		scenario:   liveContextSurface,
	})
	res := env.executeNext(t)
	require.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, res.GetLifecycleStatus(), "invalid evidence: %s", res.GetInvalidEvidenceDetail())
	trace := executorLoadTrace(t, env, res)

	require.Len(t, trace.ToolCalls, 1)
	require.NotNil(t, trace.ToolCalls[0].Retrieval)
	assert.Empty(t, trace.ToolCalls[0].Retrieval.EventReceipts)
	assert.NotContains(t, trace.ToolCalls[0].ResultJSON, "FILE_EDIT")
	assert.NotEqual(t, trace.ScenarioID, trace.InvestigationID)
	assert.NotEqual(t, trace.RunID, trace.InvestigationID)
}

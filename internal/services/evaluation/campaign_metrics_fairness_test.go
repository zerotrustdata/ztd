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

func TestMetricsUseDispatchedEvidenceAndSuccessfulResultsOnly(t *testing.T) {
	ref := []ScenarioGoldEvidenceReference{{ReferenceID: "case", ExpectedContent: "preserve app-report"}}
	fixture := ScenarioInputFixture{Seed: InvestigationSeed{CaseMemory: &InvestigationSeedMemory{InvestigationSummary: "preserve app-report"}}}
	trace := &AgentTrace{ToolCalls: []AgentToolCall{{ToolName: constants.MCPToolReadFile, ArgumentsJSON: `{"path":"case-notes.json"}`, Success: true, ResultJSON: "case notes unavailable"}}}
	require.Zero(t, computeRetrievalHitRate(trace, ref, fixture), "authored case memory was not dispatched for this role")
	require.Zero(t, computeEvidenceBackedConclusion(trace, ScenarioGoldCriteria{ContentCheck: &ScenarioContentCheck{RequiredTerms: [][]string{{"preserve app-report"}}}}, true, fixture), "arguments and fixture-only case memory are not evidence")

	trace.Input = &AgentTraceInput{Messages: []AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_USER", Text: "preserve app-report"}}}
	require.Equal(t, 1.0, computeRetrievalHitRate(trace, ref, fixture), "actual dispatched input counts as available evidence")
	require.Equal(t, 1.0, computeEvidenceBackedConclusion(trace, ScenarioGoldCriteria{ContentCheck: &ScenarioContentCheck{RequiredTerms: [][]string{{"preserve app-report"}}}}, true, fixture))

	trace.Input = nil
	trace.ToolCalls[0].Success = false
	trace.ToolCalls[0].ResultJSON = "preserve app-report"
	require.Zero(t, computeRetrievalHitRate(trace, ref, fixture), "failed retrieval cannot count as observed")
	require.Zero(t, computeEvidenceBackedConclusion(trace, ScenarioGoldCriteria{ContentCheck: &ScenarioContentCheck{RequiredTerms: [][]string{{"preserve app-report"}}}}, true, fixture), "failed-call result is not model-visible evidence")
}

func TestMetricsIgnoreAssistantClaimsAsEvidence(t *testing.T) {
	ref := []ScenarioGoldEvidenceReference{{ReferenceID: "claim", ExpectedContent: "port 8081"}}
	criteria := ScenarioGoldCriteria{ContentCheck: &ScenarioContentCheck{RequiredTerms: [][]string{{"port 8081"}}}}
	trace := &AgentTrace{
		Input: &AgentTraceInput{Messages: []AgentTraceMessage{{Role: "INFERENCE_MESSAGE_ROLE_SYSTEM", Text: "port 8081"}}},
		HeldContext: &AgentTraceHeldContext{Messages: []AgentTraceContextMessage{
			{Role: "INFERENCE_MESSAGE_ROLE_ASSISTANT", Text: "The config uses port 8081."},
		}},
	}
	require.Zero(t, computeRetrievalHitRate(trace, ref, ScenarioInputFixture{}), "system and previous assistant prose are claims, not evidence")
	require.Zero(t, computeEvidenceBackedConclusion(trace, criteria, true, ScenarioInputFixture{}))

	trace.HeldContext.Messages = append(trace.HeldContext.Messages, AgentTraceContextMessage{
		Role: "INFERENCE_MESSAGE_ROLE_TOOL", ToolResult: &AgentTraceContextToolResult{Name: constants.MCPToolReadFile, ResultJSON: `{"content":"port 8081"}`},
	})
	require.Equal(t, 1.0, computeRetrievalHitRate(trace, ref, ScenarioInputFixture{}), "tool result carried in verified held conversation is evidence")
	require.Equal(t, 1.0, computeEvidenceBackedConclusion(trace, criteria, true, ScenarioInputFixture{}))
}

func TestMetricsNamedReferencesRequireMatchingRequestAndResult(t *testing.T) {
	resource := ScenarioGoldEvidenceReference{ReferenceID: "service-config", TargetResource: "app/service.conf", ExpectedContent: "listen_port = 8081"}
	call := AgentToolCall{ToolName: constants.MCPToolReadFile, ArgumentsJSON: `{"path":"app/other.conf"}`, Success: true, ResultJSON: `{"path":"app/other.conf","content":"listen_port = 8081"}`}
	trace := &AgentTrace{ToolCalls: []AgentToolCall{call}}
	require.Zero(t, computeRetrievalHitRate(trace, []ScenarioGoldEvidenceReference{resource}, ScenarioInputFixture{}), "same content from a different resource is not the named evidence")

	trace.ToolCalls[0].ArgumentsJSON = `{"path":"app/service.conf"}`
	trace.ToolCalls[0].ResultJSON = `{"path":"app/other.conf","content":"listen_port = 8081"}`
	require.Zero(t, computeRetrievalHitRate(trace, []ScenarioGoldEvidenceReference{resource}, ScenarioInputFixture{}), "requested resource must also be present in returned result")

	trace.ToolCalls[0].ResultJSON = `{"path":"app/service.conf","content":"listen_port = 8081"}`
	require.Equal(t, 1.0, computeRetrievalHitRate(trace, []ScenarioGoldEvidenceReference{resource}, ScenarioInputFixture{}))

	commandRef := ScenarioGoldEvidenceReference{ReferenceID: "check-script", CommandPattern: "check.sh", ExpectedContent: "exit_code=0"}
	trace.ToolCalls = []AgentToolCall{{ToolName: constants.MCPToolReadFile, Success: true, ArgumentsJSON: `{"path":"other.log"}`, ResultJSON: `{"path":"other.log","content":"check.sh exit_code=0"}`}}
	require.Zero(t, computeRetrievalHitRate(trace, []ScenarioGoldEvidenceReference{commandRef}, ScenarioInputFixture{}), "mentioning the command in unrelated output is not command evidence")
	trace.ToolCalls[0] = AgentToolCall{ToolName: constants.MCPToolRunShellCommand, Success: true, ArgumentsJSON: `{"command":"sh","args":["check.sh"]}`, ResultJSON: `{"output":"exit_code=0"}`}
	require.Equal(t, 1.0, computeRetrievalHitRate(trace, []ScenarioGoldEvidenceReference{commandRef}, ScenarioInputFixture{}))
}

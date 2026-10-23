// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func TestComputeInvestigationMetrics_AllMetricsComputed(t *testing.T) {
	assignmentID := "test-assignment-123"
	gold := ScenarioGoldCriteria{
		GoldEvidenceReferences: []ScenarioGoldEvidenceReference{
			{
				ReferenceID:    "ref-log-port",
				ActionType:     "MCP_CALL",
				TargetResource: "history_trail",
			},
			{
				ReferenceID:     "ref-config-error",
				ExpectedContent: "upstream health check expects port 8080",
			},
		},
		ContentCheck: &ScenarioContentCheck{
			RequiredTerms: [][]string{{"checkout-api"}},
		},
	}
	input := ScenarioInputFixture{
		InlineContext: []ScenarioInlineContent{
			{Content: "checkout-api timeout rate 18 percent"},
		},
	}
	trace := &AgentTrace{
		ModelCalls: []AgentModelCall{{Purpose: agentModelCallPurposeTurn, LoopTurn: 1}, {Purpose: agentModelCallPurposeTurn, LoopTurn: 2}, {Purpose: agentModelCallPurposeTribunalPrefix + "generate", LoopTurn: 2}},
		ToolCalls: []AgentToolCall{
			{
				ToolName: "audit_receipt_list",
				Retrieval: &AgentContextRetrieval{
					Receipts: []AgentContextReceiptRef{
						{
							ReceiptID: "rcpt-1",
						},
					},
				},
				ResultJSON: `{"action_type": "MCP_CALL", "target": "history_trail", "summary": "upstream health check expects port 8080"}`,
			},
			{
				ToolName:      "read_file",
				ArgumentsJSON: `{"path": "app/service.conf"}`,
				ResultJSON:    "listen_port = 8081",
			},
		},
	}

	scores := ComputeInvestigationMetrics(assignmentID, trace, gold, true, input)
	require.Len(t, scores, 5)

	scoreMap := make(map[string]*evalv1.DecomposedScoreRecord)
	for _, s := range scores {
		scoreMap[s.Dimension] = s
	}

	// 1. Retrieval hit rate: both references matched -> 1.0
	require.Contains(t, scoreMap, MetricRetrievalHitRate)
	assert.Equal(t, 1.0, scoreMap[MetricRetrievalHitRate].Value)
	assert.Equal(t, evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_HIGHER_IS_BETTER, scoreMap[MetricRetrievalHitRate].Direction)

	// 2. Redundant command rate: file_read was not in history_trail -> 0.0
	require.Contains(t, scoreMap, MetricRedundantCommandRate)
	assert.Equal(t, 0.0, scoreMap[MetricRedundantCommandRate].Value)
	assert.Equal(t, evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_LOWER_IS_BETTER, scoreMap[MetricRedundantCommandRate].Direction)

	// 3. Steps to diagnosis: 2 turn calls + 2 tool calls = 4 steps; the
	// tribunal call belongs to its tool call.
	require.Contains(t, scoreMap, MetricStepsToDiagnosis)
	assert.Equal(t, 4.0, scoreMap[MetricStepsToDiagnosis].Value)
	assert.Equal(t, evalv1.EvaluationMetricDirection_EVALUATION_METRIC_DIRECTION_LOWER_IS_BETTER, scoreMap[MetricStepsToDiagnosis].Direction)

	// 4. Diagnosis correctness: contentPassed == true -> 1.0
	require.Contains(t, scoreMap, MetricDiagnosisCorrectness)
	assert.Equal(t, 1.0, scoreMap[MetricDiagnosisCorrectness].Value)

	// 5. Evidence backed conclusion: checkout-api is in inline context -> 1.0
	require.Contains(t, scoreMap, MetricEvidenceBackedConclusion)
	assert.Equal(t, 1.0, scoreMap[MetricEvidenceBackedConclusion].Value)
}

func TestComputeInvestigationMetrics_RedundantCommandDetected(t *testing.T) {
	assignmentID := "test-assignment-redundant"
	gold := ScenarioGoldCriteria{
		GoldEvidenceReferences: []ScenarioGoldEvidenceReference{
			{ReferenceID: "ref-1", TargetResource: "app/service.conf"},
		},
	}
	input := ScenarioInputFixture{}
	trace := &AgentTrace{
		ModelCalls: []AgentModelCall{{LoopTurn: 1}},
		ToolCalls: []AgentToolCall{
			{
				ToolName: "audit_receipt_list",
				Retrieval: &AgentContextRetrieval{
					Receipts: []AgentContextReceiptRef{
						{
							ReceiptID: "rcpt-1",
						},
					},
				},
				ResultJSON: `{"receipts": [{"file_path": "app/service.conf", "content": "listen_port = 8081"}]}`,
			},
			{
				// Redundant read: app/service.conf was already in retrieved receipts!
				ToolName:      "read_file",
				ArgumentsJSON: `{"path": "app/service.conf"}`,
				ResultJSON:    "listen_port = 8081",
			},
		},
	}

	scores := ComputeInvestigationMetrics(assignmentID, trace, gold, true, input)
	scoreMap := make(map[string]*evalv1.DecomposedScoreRecord)
	for _, s := range scores {
		scoreMap[s.Dimension] = s
	}

	// 1 command executed, and it was redundant -> redundant rate 1.0 (100%)
	require.Contains(t, scoreMap, MetricRedundantCommandRate)
	assert.Equal(t, 1.0, scoreMap[MetricRedundantCommandRate].Value)
}

func TestComputeInvestigationMetrics_RedundantShellCommandLineDetected(t *testing.T) {
	trace := &AgentTrace{
		ModelCalls: []AgentModelCall{{LoopTurn: 1}},
		ToolCalls: []AgentToolCall{
			{
				ToolName:   "audit_receipt_list",
				Retrieval:  &AgentContextRetrieval{Receipts: []AgentContextReceiptRef{{ReceiptID: "rcpt-1"}}},
				ResultJSON: `{"receipts": [{"command": "df -h"}]}`,
			},
			{
				// The command line, not any one argument, was already retrieved.
				ToolName:      "run_shell_command",
				ArgumentsJSON: `{"command": "df", "args": ["-h"]}`,
			},
			{ToolName: "run_shell_command", ArgumentsJSON: `{"command": "uptime"}`},
		},
	}

	scores := ComputeInvestigationMetrics("test-assignment-shell", trace, ScenarioGoldCriteria{}, true, ScenarioInputFixture{})
	scoreMap := make(map[string]*evalv1.DecomposedScoreRecord)
	for _, s := range scores {
		scoreMap[s.Dimension] = s
	}

	require.Contains(t, scoreMap, MetricRedundantCommandRate)
	assert.Equal(t, 0.5, scoreMap[MetricRedundantCommandRate].Value)
}

func TestComputeInvestigationMetrics_UnsupportedClaimFailsEvidenceBacked(t *testing.T) {
	assignmentID := "test-assignment-unsupported"
	gold := ScenarioGoldCriteria{
		ContentCheck: &ScenarioContentCheck{
			// Required term that never appeared in tool results or prompt
			RequiredTerms: [][]string{{"unsupported-cause-dns-failure"}},
		},
	}
	input := ScenarioInputFixture{
		InlineContext: []ScenarioInlineContent{
			{Content: "checkout-api timeout rate 18 percent"},
		},
	}
	trace := &AgentTrace{
		ModelCalls: []AgentModelCall{{LoopTurn: 1}},
		ToolCalls:  []AgentToolCall{},
	}

	scores := ComputeInvestigationMetrics(assignmentID, trace, gold, true, input)
	scoreMap := make(map[string]*evalv1.DecomposedScoreRecord)
	for _, s := range scores {
		scoreMap[s.Dimension] = s
	}

	// Because "unsupported-cause-dns-failure" was never in evidence, evidence backed conclusion is 0.0
	require.Contains(t, scoreMap, MetricEvidenceBackedConclusion)
	assert.Equal(t, 0.0, scoreMap[MetricEvidenceBackedConclusion].Value)
}

func TestComputeRetrievalHitRate_EmptyQueryToReferencedToolHits(t *testing.T) {
	refs := []ScenarioGoldEvidenceReference{{
		ReferenceID:    "ref-history-trail-query",
		ActionType:     "MCP_CALL",
		TargetResource: constants.MCPToolAuditReceiptList,
	}}
	call := AgentToolCall{
		ToolName:   constants.MCPToolAuditReceiptList,
		ActionType: "MCP_CALL",
		Success:    true,
		Retrieval:  &AgentContextRetrieval{},
		ResultJSON: `{"receipts": []}`,
	}

	trace := &AgentTrace{ToolCalls: []AgentToolCall{call}}
	assert.Equal(t, 1.0, computeRetrievalHitRate(trace, refs, ScenarioInputFixture{}))

	call.Success = false
	trace = &AgentTrace{ToolCalls: []AgentToolCall{call}}
	assert.Equal(t, 0.0, computeRetrievalHitRate(trace, refs, ScenarioInputFixture{}))
}

func TestComputeRetrievalHitRate_SuppliedContentReferenceHits(t *testing.T) {
	refs := []ScenarioGoldEvidenceReference{
		{ReferenceID: "ref-missing-script", ExpectedContent: "deploy-healthcheck: not found"},
		{ReferenceID: "ref-exit-code", ExpectedContent: "exit_code=127"},
	}
	input := ScenarioInputFixture{
		InlineContext: []ScenarioInlineContent{
			{Content: "step=verify sh: deploy-healthcheck: not found\nstep=verify exit_code=127"},
		},
	}
	trace := &AgentTrace{}
	assert.Equal(t, 1.0, computeRetrievalHitRate(trace, refs, input))

	// A content-only reference the input never carried is still a miss.
	refs = append(refs, ScenarioGoldEvidenceReference{ReferenceID: "ref-unseen", ExpectedContent: "disk quota exceeded"})
	assert.InDelta(t, 2.0/3.0, computeRetrievalHitRate(trace, refs, input), 1e-9)

	// A reference naming a resource needs an observation even when the input
	// quotes its content.
	resourceRef := []ScenarioGoldEvidenceReference{{ReferenceID: "ref-log", TargetResource: "logs/deploy.log", ExpectedContent: "exit_code=127"}}
	assert.Equal(t, 0.0, computeRetrievalHitRate(trace, resourceRef, input))
}

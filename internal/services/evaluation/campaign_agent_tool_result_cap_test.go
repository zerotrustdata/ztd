// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func nativeToolCall(status operatorv1.ExecutionStatus, summary string) AgentToolCall {
	return AgentToolCall{
		ToolName: constants.MCPToolSearchDirectory,
		Receipt:  &AgentTraceReceipt{EventReceipt: &operatorv1.EventReceipt{Status: status, ResultSummary: summary}},
	}
}

// A native tool result larger than the per-message text bound reaches the model
// truncated, whether it is JSON, plain text, or a failure message, so one broad
// search cannot make the next inference request undeliverable.
func TestCompleteNativeToolCall_CapsOversizedModelVisibleResults(t *testing.T) {
	oversizedJSON := `{"matches":["` + strings.Repeat("x", 2*agentToolResultMaxBytes) + `"]}`
	oversizedText := strings.Repeat("y", 2*agentToolResultMaxBytes)

	tests := []struct {
		name    string
		status  operatorv1.ExecutionStatus
		summary string
		success bool
	}{
		{"oversized JSON result", operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, oversizedJSON, true},
		{"oversized plain-text result", operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, oversizedText, true},
		{"oversized failure message", operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, oversizedText, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			completed, err := (&agentRun{}).completeNativeToolCall(nativeToolCall(tt.status, tt.summary))
			require.NoError(t, err)
			assert.Equal(t, tt.success, completed.Success)
			assert.LessOrEqual(t, len(completed.ResultJSON), agentToolResultMaxBytes+1024)
			assert.Contains(t, completed.ResultJSON, "[truncated]")
		})
	}
}

func TestCompleteNativeToolCall_KeepsResultsWithinTheBoundIntact(t *testing.T) {
	completed, err := (&agentRun{}).completeNativeToolCall(nativeToolCall(operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, `{"matches":[]}`))
	require.NoError(t, err)
	assert.True(t, completed.Success)
	assert.Equal(t, `{"matches":[]}`, completed.ResultJSON)
}

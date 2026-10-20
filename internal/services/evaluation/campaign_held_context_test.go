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
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestImportAssignmentResultFromAgentTrace_HeldTraceMustRunInTheHeldWorkspace(t *testing.T) {
	const runID = "run-held-workspace"
	held, err := NewScenarioWorkspace(agentTestWorkingDirectory, runID, "attempt-1")
	require.NoError(t, err)
	heldWorkspace := AgentTraceWorkspace(held)
	trace := &AgentTrace{
		RunID: runID, EvaluationAttemptID: "attempt-2", Workspace: heldWorkspace,
		HeldContext: &AgentTraceHeldContext{AssignmentID: "assignment-1", Repetition: 1, Workspace: heldWorkspace},
	}
	require.NoError(t, trace.validateWorkspace())

	own, err := NewScenarioWorkspace(agentTestWorkingDirectory, runID, trace.EvaluationAttemptID)
	require.NoError(t, err)
	trace.Workspace = AgentTraceWorkspace(own)
	require.ErrorIs(t, trace.validateWorkspace(), constants.ErrEvaluationWorkspaceUnavailable, "a held trace must run in the held workspace, not its own")

	elsewhere := AgentTraceWorkspace{Root: agentTestWorkingDirectory + "/elsewhere", OperatorWorkingDirectory: agentTestWorkingDirectory}
	trace.Workspace = elsewhere
	trace.HeldContext.Workspace = elsewhere
	require.ErrorIs(t, trace.validateWorkspace(), constants.ErrEvaluationWorkspaceUnavailable, "a held workspace must still be an attempt-scoped root")
}

func TestAgentTraceConversation_ToolCallCountMismatchIsMalformed(t *testing.T) {
	trace := &AgentTrace{
		Input: &AgentTraceInput{Messages: []AgentTraceMessage{{Role: operatorv1.InferenceMessageRole_INFERENCE_MESSAGE_ROLE_USER.String(), Text: "prompt"}}},
		ModelCalls: []AgentModelCall{{
			Purpose: agentModelCallPurposeTurn, LoopTurn: 1, ProviderAttemptID: "provider-1", TransactionID: "tx-1",
			ToolCallCount: 2,
		}},
	}

	_, err := trace.Conversation()

	require.ErrorIs(t, err, constants.ErrEvidenceArtifactMalformed)
}

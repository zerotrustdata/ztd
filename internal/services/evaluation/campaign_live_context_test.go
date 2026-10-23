// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestCampaignLiveContext_V6StillRequiresCompletedQueryEvidence(t *testing.T) {
	t.Parallel()
	call := AgentToolCall{
		ToolName: agentToolInvestigationCtx,
		Receipt:  &AgentTraceReceipt{ActionReceipt: &operatorv1.ActionReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}},
	}
	require.ErrorIs(t, verifyContextRetrieval(&AgentTrace{SchemaVersion: agentTraceSchemaVersionV6}, call), constants.ErrEvaluationEvidenceTraceMismatch)
}

// The investigation identity decision is the executor's investigationID: a fresh
// assignment draws a new identity, a persistent investigation keeps its declared
// ID across Data Operator session rotation, and a different Data Operator fails.
func TestCampaignInvestigationIdentity_FreshAssignmentsAndPersistentSessionRotation(t *testing.T) {
	t.Parallel()
	issued := 0
	executor := &CampaignAgentExecutor{newID: func(prefix string) string {
		issued++
		return fmt.Sprintf("%s-%d", prefix, issued)
	}}
	req := AssignmentExecutionRequest{
		Binding: CampaignExecutionBinding{Topology: CampaignOperatorTopology{
			Data: CampaignOperatorRuntime{OperatorID: "data-operator", OperatorSessionID: "data-session"},
		}},
	}
	fresh, err := executor.investigationID(req)
	require.NoError(t, err)
	again, err := executor.investigationID(req)
	require.NoError(t, err)
	assert.NotEqual(t, fresh, again)

	req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: "case-recurring", DataOperatorId: req.Binding.Topology.Data.OperatorID,
	}
	one, err := executor.investigationID(req)
	require.NoError(t, err)
	req.Binding.Topology.Data.OperatorSessionID = "rotated-session"
	two, err := executor.investigationID(req)
	require.NoError(t, err)
	assert.Equal(t, "case-recurring", one)
	assert.Equal(t, one, two)

	req.Binding.Topology.Data.OperatorID = "another-operator"
	_, err = executor.investigationID(req)
	assert.ErrorIs(t, err, constants.ErrEvaluationInvestigationInvalid)
}

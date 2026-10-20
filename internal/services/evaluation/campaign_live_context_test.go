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
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestCampaignLiveContext_V6StillRequiresCompletedQueryEvidence(t *testing.T) {
	call := AgentToolCall{
		ToolName: agentToolInvestigationCtx,
		Receipt:  &AgentTraceReceipt{ActionReceipt: &operatorv1.ActionReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}},
	}
	require.ErrorIs(t, verifyContextRetrieval(&AgentTrace{SchemaVersion: agentTraceSchemaVersionV6}, call), constants.ErrEvaluationEvidenceTraceMismatch)
}

func TestCampaignInvestigationIdentity_FreshAssignmentsAndPersistentSessionRotation(t *testing.T) {
	f := newAgentExecutorFixture(t)
	fresh, err := f.executor.newRun(f.req)
	require.NoError(t, err)
	again, err := f.executor.newRun(f.req)
	require.NoError(t, err)
	assert.NotEqual(t, fresh.trace.InvestigationID, again.trace.InvestigationID)
	f.req.Investigation = &evalv1.EvaluationInvestigation{
		Mode:            evalv1.EvaluationInvestigationMode_EVALUATION_INVESTIGATION_MODE_PERSISTENT,
		InvestigationId: "case-recurring", DataOperatorId: f.req.Binding.Topology.Data.OperatorID,
	}
	one, err := f.executor.newRun(f.req)
	require.NoError(t, err)
	f.req.Binding.Topology.Data.OperatorSessionID = "rotated-session"
	two, err := f.executor.newRun(f.req)
	require.NoError(t, err)
	assert.Equal(t, "case-recurring", one.trace.InvestigationID)
	assert.Equal(t, one.trace.InvestigationID, two.trace.InvestigationID)
	f.req.Binding.Topology.Data.OperatorID = "another-operator"
	f.req.Binding.Topology.Data.Anchor.RuntimeId = "another-operator"
	_, err = f.executor.newRun(f.req)
	assert.ErrorIs(t, err, constants.ErrEvaluationInvestigationInvalid)
}

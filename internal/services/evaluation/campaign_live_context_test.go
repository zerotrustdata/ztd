// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// liveContextClient is a provider-free transport double. The Operator-route
// integration tests cover real governance and SQLite; this test checks that
// the executor asks that route and binds the signed answer to its trace.
type liveContextClient struct {
	base           *agentLaneClient
	records        []*models.ActionReceiptRecord
	queries        []client.DispatchCommandRequest
	wrongAuthority bool
}

func (c *liveContextClient) DispatchCommand(ctx context.Context, persona client.Persona, req client.DispatchCommandRequest) (*client.GovernedOutcome, error) {
	if req.ActionType != string(constants.ActionTypeMcpCall) {
		out, err := c.base.DispatchCommand(ctx, persona, req)
		if err != nil {
			return nil, err
		}
		r := out.Observation.GetReceipt()
		if req.ActionType == string(constants.ActionTypeExecuteBash) {
			r.ResultSummary = "HEALTHY"
			c.base.fixture.ledger.signer.Resign(c.base.fixture.t, r)
		}
		c.records = append(c.records, &models.ActionReceiptRecord{TransactionID: r.GetTransactionId(), OperatorID: c.base.fixture.ledger.operatorID, OperatorSessionID: c.base.fixture.ledger.sessionID, InvestigationID: req.InvestigationID, ActionType: constants.ActionType(req.ActionType), ResultSummary: r.GetResultSummary(), Status: r.GetStatus(), ActionReceipt: r})
		return out, nil
	}
	c.queries = append(c.queries, req)
	call := &operatorv1.McpCallRequested{}
	if err := proto.Unmarshal(req.Payload, call); err != nil {
		return nil, err
	}
	var query struct {
		InvestigationID string `json:"investigation_id"`
	}
	if err := json.Unmarshal([]byte(call.GetArgumentsJson()), &query); err != nil {
		return nil, err
	}
	var rows []*operatorv1.ActionReceipt
	for _, row := range c.records {
		if row.InvestigationID == query.InvestigationID {
			rows = append(rows, row.ActionReceipt)
		}
	}
	opID := c.base.fixture.ledger.operatorID
	if c.wrongAuthority {
		opID = "gateway-mirror"
	}
	result := models.OperatorAuditReceiptListResult{
		OperatorID:      opID,
		Authority:       "operator_local_ledger",
		Receipts:        rows,
		Count:           len(rows),
		InvestigationID: query.InvestigationID,
	}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	r := c.base.fixture.ledger.executed("tx-context-query", string(constants.ActionTypeMcpCall))
	r.ResultSummary = string(body)
	c.base.fixture.ledger.signer.Resign(c.base.fixture.t, r)
	return &client.GovernedOutcome{
		Observation: &evalv1.ConformanceObservation{Kind: evalv1.ConformanceObservationKind_CONFORMANCE_OBSERVATION_KIND_RECEIPT, Receipt: r},
		Dispatch:    &operatorv1.GovernedDispatchResult{TransactionId: r.GetTransactionId(), Receipt: r},
	}, nil
}

func TestCampaignLiveContext_ReturnsJustExecutedCommandAndExcludesSetup(t *testing.T) {
	turns := runCommandTurns(agentTestCommand)
	turns = append(turns[:1], []*operatorv1.InferenceResponsePart{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "context-1", Name: agentToolInvestigationCtx, ArgumentsJson: `{"data_type":"operator_actions"}`}}}}, turns[1])
	f := newAgentExecutorFixture(t, turns...)
	transport := &liveContextClient{base: f.client}
	f.executor.tools = NewCommandLane(transport, client.Persona{})
	result, err := f.execute()
	require.NoError(t, err)
	require.Len(t, transport.queries, 1, "context must query the Data Operator's ledger")
	assert.Equal(t, evalv1.EvaluationAssignmentLifecycleStatus_EVALUATION_ASSIGNMENT_LIFECYCLE_STATUS_COMPLETED, result.GetLifecycleStatus())
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 2)
	assert.Contains(t, trace.ToolCalls[1].ResultJSON, "HEALTHY")
	assert.NotContains(t, trace.ToolCalls[1].ResultJSON, "FILE_EDIT")
	assert.NotEmpty(t, trace.ToolCalls[1].TransactionID, "context retrieval is itself receipted")
	require.NotNil(t, trace.ToolCalls[1].Retrieval)
	require.Len(t, trace.ToolCalls[1].Retrieval.Receipts, 1)
	assert.Equal(t, trace.InvestigationID, transport.queries[0].InvestigationID)
	require.NoError(t, VerifyAgentTraceEvidence(trace))
	for _, mutation := range []struct {
		name  string
		apply func(*AgentTrace)
	}{
		{"missing retrieval", func(v *AgentTrace) { v.ToolCalls[1].Retrieval = nil }},
		{"failed flag suppressing retrieval", func(v *AgentTrace) { v.ToolCalls[1].Success = false; v.ToolCalls[1].Retrieval = nil }},
		{"wrong digest", func(v *AgentTrace) { v.ToolCalls[1].Retrieval.Receipts[0].Digest = "forged" }},
		{"invented model facts", func(v *AgentTrace) { v.ToolCalls[1].ResultJSON = `{"success":true,"summary":"invented"}` }},
		{"wrong investigation", func(v *AgentTrace) { v.InvestigationID = "another-investigation" }},
		{"changed query limit", func(v *AgentTrace) { v.ToolCalls[1].Retrieval.Query.Limit++ }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			body, err := json.Marshal(trace)
			require.NoError(t, err)
			var altered AgentTrace
			require.NoError(t, json.Unmarshal(body, &altered))
			mutation.apply(&altered)
			assert.Error(t, VerifyAgentTraceEvidence(&altered))
		})
	}
}

func contextOnlyTurns() [][]*operatorv1.InferenceResponsePart {
	return [][]*operatorv1.InferenceResponsePart{
		{{Part: &operatorv1.InferenceResponsePart_ToolCall{ToolCall: &operatorv1.InferenceToolCall{CallId: "context-1", Name: agentToolInvestigationCtx, ArgumentsJson: `{"data_type":"history_trail"}`}}}},
		{{Part: &operatorv1.InferenceResponsePart_Text{Text: "No prior receipts were returned."}}},
	}
}

func TestCampaignLiveContext_FreshInvestigationHasNoSetupHistory(t *testing.T) {
	f := newAgentExecutorFixture(t, contextOnlyTurns()...)
	transport := &liveContextClient{base: f.client}
	f.executor.tools = NewCommandLane(transport, client.Persona{})
	_, err := f.execute()
	require.NoError(t, err)
	trace := f.storedTrace()
	require.Len(t, trace.ToolCalls, 1)
	require.NotNil(t, trace.ToolCalls[0].Retrieval)
	assert.Empty(t, trace.ToolCalls[0].Retrieval.Receipts)
	assert.NotContains(t, trace.ToolCalls[0].ResultJSON, "FILE_EDIT")
	assert.NotEqual(t, trace.ScenarioID, trace.InvestigationID)
	assert.NotEqual(t, trace.RunID, trace.InvestigationID)
}

func TestCampaignLiveContext_RejectsGatewayProjectionAuthority(t *testing.T) {
	f := newAgentExecutorFixture(t, contextOnlyTurns()...)
	transport := &liveContextClient{base: f.client, wrongAuthority: true}
	f.executor.tools = NewCommandLane(transport, client.Persona{})
	_, err := f.execute()
	assert.ErrorIs(t, err, constants.ErrEvidenceScopeMismatch)
	assert.Empty(t, f.traces.body, "an invalid context source is a harness failure")
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

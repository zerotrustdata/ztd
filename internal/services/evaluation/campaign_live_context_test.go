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
	var rows []*models.ActionReceiptRecord
	for _, row := range c.records {
		if row.InvestigationID == query.InvestigationID {
			rows = append(rows, row)
		}
	}
	opID := c.base.fixture.ledger.operatorID
	if c.wrongAuthority {
		opID = "gateway-mirror"
	}
	result := struct {
		OperatorID string                        `json:"operator_id"`
		Authority  string                        `json:"authority"`
		Receipts   []*models.ActionReceiptRecord `json:"receipts"`
		Count      int                           `json:"count"`
	}{opID, "operator_local_ledger", rows, len(rows)}
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
}

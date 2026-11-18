//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorDeployTool_GatewayCallToolRoutesToGovernedAction(t *testing.T) {
	receipt := &operatorv1.EventReceipt{
		TransactionId:   "tx-deploy-1",
		TransactionHash: "hash-deploy-1",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   `{"execution_id":"exec-deploy-1"}`,
	}
	proc := &fakeEnvelopeProcessor{receipt: receipt}
	g := newTestGatewayService(t, withEnvProc(proc))
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "session-verified")

	_, err := g.callTool(ctx, httptest.NewRequest(http.MethodPost, "/mcp", nil),
		json.RawMessage(`{"name":"operator_deploy","arguments":{"hostnames":["worker-1","worker-2"]},"execution_id":"exec-deploy-1","investigation_id":"inv-1"}`))
	require.NoError(t, err)

	envelope := &commonv1.GovernanceEnvelope{}
	require.NoError(t, protojson.Unmarshal(proc.gotPayload, envelope))
	assert.Equal(t, string(constants.ActionTypeOperatorDeploy), envelope.GetActionType())
	assert.Equal(t, string(constants.EventOperatorDeployRequested), envelope.GetEventType())
	assert.Equal(t, "worker-1,worker-2", envelope.GetTargetResource())

	deployed := &operatorv1.OperatorDeployRequested{}
	require.NoError(t, proto.Unmarshal(envelope.GetPayload(), deployed))
	assert.Equal(t, "exec-deploy-1", deployed.GetExecutionId())
	assert.Equal(t, []string{"worker-1", "worker-2"}, deployed.GetHostnames())
}

func TestOperatorDeployTool_GatewayCallToolRejectsLegacyAndInvalidArguments(t *testing.T) {
	receipt := &operatorv1.EventReceipt{
		TransactionId:   "tx-deploy-2",
		TransactionHash: "hash-deploy-2",
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:   "{}",
	}
	g := newTestGatewayService(t, withEnvProc(&fakeEnvelopeProcessor{receipt: receipt}))
	ctx := context.WithValue(context.Background(), constants.ContextKeyOperatorSessionID, "session-verified")

	cases := []string{
		`{"name":"operator_deploy","arguments":{"hostnames":[]},"execution_id":"exec-1","investigation_id":"inv-1"}`,
		`{"name":"operator_deploy","arguments":{"hostnames":["-oProxyCommand=evil"]},"execution_id":"exec-2","investigation_id":"inv-1"}`,
		`{"name":"operator_deploy","arguments":{"hostnames":["host1","host1"]},"execution_id":"exec-3","investigation_id":"inv-1"}`,
		`{"name":"operator_deploy","arguments":{"hostnames":["localhost"],"operator_binary":"/bin/sh"},"execution_id":"exec-4","investigation_id":"inv-1"}`,
	}
	for _, params := range cases {
		_, err := g.callTool(ctx, httptest.NewRequest(http.MethodPost, "/mcp", nil), json.RawMessage(params))
		require.ErrorIs(t, err, constants.ErrGatewayInvalidToolArguments, params)
	}
}

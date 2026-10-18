// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package pubsub

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorAuditReceiptRoute_GovernedLocalLedgerScope(t *testing.T) {
	svc, _, auditStore, _ := newInferenceIntegrationFixture(t)
	ctx := context.Background()

	scoredInvestigation := "investigation-persistent"
	harnessInvestigation := "eval-harness:attempt-1"
	commandReceipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo scored-command", ExecutionId: "exec-scored"},
		scoredInvestigation,
	)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, commandReceipt.GetStatus())

	processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo harness-command", ExecutionId: "exec-harness"},
		harnessInvestigation,
	)

	query := func(investigationID string) mcp.AuditReceiptListResult {
		t.Helper()
		args, err := json.Marshal(mcp.AuditReceiptListRequest{InvestigationID: investigationID, Limit: 100})
		require.NoError(t, err)
		receipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
			constants.Event.Operator.Mcp.CallRequested,
			constants.ActionTypeMcpCall,
			&operatorv1.McpCallRequested{ToolName: "audit_receipt_list", ArgumentsJson: string(args)},
			investigationID,
		)
		var result mcp.AuditReceiptListResult
		require.NoError(t, json.Unmarshal([]byte(receipt.GetResultSummary()), &result))
		return result
	}

	result := query(scoredInvestigation)
	require.Equal(t, svc.config.OperatorID, result.OperatorID)
	require.Equal(t, "operator_local_ledger", result.Authority)
	require.Len(t, result.Receipts, 1)
	require.Equal(t, commandReceipt.GetTransactionId(), result.Receipts[0].TransactionID)
	require.Equal(t, scoredInvestigation, result.Receipts[0].InvestigationID)

	fresh := query("investigation-fresh")
	require.Equal(t, svc.config.OperatorID, fresh.OperatorID)
	require.Equal(t, "operator_local_ledger", fresh.Authority)
	require.Empty(t, fresh.Receipts)

	// Both the scored query and the fresh query are issued through MCP_CALL and
	// leave their own signed receipts only after each result is materialized.
	rows, err := auditStore.ListActionReceipts(models.AuditScope{OperatorID: svc.config.OperatorID}, 100, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rows), 4)
}

func processOperatorAuditRouteEnvelope(t *testing.T, svc *OperatorPubSubService, ctx context.Context, event constants.EventType, action constants.ActionType, payload proto.Message, investigationID string) *operatorv1.ActionReceipt {
	t.Helper()
	payloadBytes, err := proto.Marshal(payload)
	require.NoError(t, err)
	now := time.Now().UTC()
	env := &govpkg.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.New(now),
		ExpiresAt:         timestamppb.New(now.Add(time.Minute)),
		OperatorId:        svc.config.OperatorID,
		OperatorSessionId: inferenceIntegrationSessionID,
		EventType:         string(event),
		ActionType:        string(action),
		InvestigationId:   investigationID,
		TargetResource:    "localhost",
		Payload:           payloadBytes,
		StateMerkleRoot:   "test-state-root",
		Nonce:             fmt.Sprintf("nonce-%s-%d", action, time.Now().UnixNano()),
		Posture:           string(constants.PostureDoctrine),
	}
	env.TransactionHash, err = govpkg.GenerateMessageID(env)
	require.NoError(t, err)
	env.Id = env.TransactionHash
	wire, err := protojson.Marshal(env)
	require.NoError(t, err)
	receipt, err := svc.ProcessEnvelope(ctx, wire)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	return receipt
}

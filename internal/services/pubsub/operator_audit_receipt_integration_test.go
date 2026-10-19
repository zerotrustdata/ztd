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
	"crypto/ed25519"
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
	governanceService "github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorAuditReceiptRoute_GovernedLocalLedgerScope(t *testing.T) {
	svc, _, auditStore, _ := newInferenceIntegrationFixture(t)
	ctx := context.Background()

	scoredInvestigation := "investigation-persistent"
	harnessInvestigation := "eval-harness:attempt-1"
	secondSessionID := "operator-audit-route-session-2"
	require.NoError(t, auditStore.CreateSession(secondSessionID, constants.SessionTypeOperator, "Rotated audit session", svc.config.OperatorID))
	commandReceipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo scored-command", ExecutionId: "exec-scored"},
		scoredInvestigation,
		inferenceIntegrationSessionID,
	)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, commandReceipt.GetStatus())
	publicKey := svc.actuator.SigningKey.Public().(ed25519.PublicKey)
	require.NoError(t, governanceService.VerifyActionReceiptSignature(commandReceipt, publicKey))

	processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo harness-command", ExecutionId: "exec-harness"},
		harnessInvestigation,
		inferenceIntegrationSessionID,
	)
	identity, ok := svc.identity.(*testutil.MockExecutionTarget)
	require.True(t, ok, "integration fixture exposes its mutable runtime identity")
	identity.SessionID = secondSessionID
	secondCommandReceipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo scored-command-session-2", ExecutionId: "exec-scored-session-2"},
		scoredInvestigation,
		secondSessionID,
	)
	identity.SessionID = inferenceIntegrationSessionID
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, secondCommandReceipt.GetStatus())
	require.NoError(t, governanceService.VerifyActionReceiptSignature(secondCommandReceipt, publicKey))

	query := func(investigationID string) mcp.OperatorAuditReceiptListResult {
		t.Helper()
		args, err := json.Marshal(mcp.AuditReceiptListRequest{
			InvestigationID:   investigationID,
			ExcludeActionType: string(constants.ActionTypeMcpCall),
			Limit:             100,
		})
		require.NoError(t, err)
		receipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
			constants.Event.Operator.Mcp.CallRequested,
			constants.ActionTypeMcpCall,
			&operatorv1.McpCallRequested{ToolName: "audit_receipt_list", ArgumentsJson: string(args)},
			investigationID,
			inferenceIntegrationSessionID,
		)
		var result mcp.OperatorAuditReceiptListResult
		require.NoError(t, json.Unmarshal([]byte(receipt.GetResultSummary()), &result))
		require.NoError(t, governanceService.VerifyActionReceiptSignature(receipt, publicKey))
		return result
	}

	result := query(scoredInvestigation)
	require.Equal(t, svc.config.OperatorID, result.OperatorID)
	require.Equal(t, "operator_local_ledger", result.Authority)
	require.Len(t, result.Receipts, 2)
	rows, err := auditStore.ListActionReceipts(models.AuditScope{OperatorID: svc.config.OperatorID}, 100, 0)
	require.NoError(t, err)
	storedSessions := make(map[string]string, len(rows))
	for _, row := range rows {
		storedSessions[row.TransactionID] = row.OperatorSessionID
	}
	gotReceiptSessions := make(map[string]string, len(result.Receipts))
	for _, receipt := range result.Receipts {
		require.NoError(t, governanceService.VerifyActionReceiptSignature(receipt, publicKey))
		require.Equal(t, string(constants.ActionTypeExecuteBash), receipt.GetActionType())
		require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
		gotReceiptSessions[receipt.GetTransactionId()] = storedSessions[receipt.GetTransactionId()]
	}
	firstSession, firstFound := gotReceiptSessions[commandReceipt.GetTransactionId()]
	secondSession, secondFound := gotReceiptSessions[secondCommandReceipt.GetTransactionId()]
	require.True(t, firstFound, "query should return the receipt from the first Operator session")
	require.True(t, secondFound, "query should return the receipt from the rotated Operator session")
	require.Equal(t, inferenceIntegrationSessionID, firstSession)
	require.Equal(t, secondSessionID, secondSession)

	fresh := query("investigation-fresh")
	require.Equal(t, svc.config.OperatorID, fresh.OperatorID)
	require.Equal(t, "operator_local_ledger", fresh.Authority)
	require.Equal(t, "investigation-fresh", fresh.InvestigationID)
	require.Empty(t, fresh.Receipts)

	// Both the scored query and the fresh query are issued through MCP_CALL and
	// leave their own signed receipts only after each result is materialized.
	allRows, err := auditStore.ListActionReceipts(models.AuditScope{OperatorID: svc.config.OperatorID}, 100, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(allRows), 5)
}

// TestOperatorNativeToolRoute_OutboundExecutesRegistryLocally proves an
// outbound Operator, which never binds mcp.GatewayService, executes native MCP
// tools from the shared registry against its own audit store under a signed
// MCP_CALL receipt, and fails closed with a signed FAILED receipt for
// Gateway-local tools it does not host.
func TestOperatorNativeToolRoute_OutboundExecutesRegistryLocally(t *testing.T) {
	svc, _, _, _ := newInferenceIntegrationFixture(t)
	ctx := context.Background()
	require.Nil(t, svc.GetMCPGateway(), "outbound Operators never bind the MCP gateway")
	publicKey := svc.actuator.SigningKey.Public().(ed25519.PublicKey)

	commandReceipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Command.Requested,
		constants.ActionTypeExecuteBash,
		&operatorv1.CommandRequested{Command: "echo native-route", ExecutionId: "exec-native-route"},
		"investigation-native",
		inferenceIntegrationSessionID,
	)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, commandReceipt.GetStatus())

	getArgs, err := json.Marshal(mcp.AuditReceiptGetRequest{TransactionID: commandReceipt.GetTransactionId()})
	require.NoError(t, err)
	getReceipt := processOperatorAuditRouteEnvelope(t, svc, ctx,
		constants.Event.Operator.Mcp.CallRequested,
		constants.ActionTypeMcpCall,
		&operatorv1.McpCallRequested{ToolName: "audit_receipt_get", ArgumentsJson: string(getArgs)},
		"investigation-native",
		inferenceIntegrationSessionID,
	)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, getReceipt.GetStatus())
	require.NoError(t, governanceService.VerifyActionReceiptSignature(getReceipt, publicKey))
	var got mcp.AuditReceiptGetResult
	require.NoError(t, json.Unmarshal([]byte(getReceipt.GetResultSummary()), &got))
	require.NotNil(t, got.Receipt)
	require.Equal(t, commandReceipt.GetTransactionId(), got.Receipt.TransactionID)
	require.Equal(t, svc.config.OperatorID, got.Receipt.OperatorID)

	wire := operatorAuditRouteEnvelopeWire(t, svc,
		constants.Event.Operator.Mcp.CallRequested,
		constants.ActionTypeMcpCall,
		&operatorv1.McpCallRequested{ToolName: "read_field", ArgumentsJson: `{}`},
		"investigation-native",
		inferenceIntegrationSessionID,
	)
	failed, err := svc.ProcessEnvelope(ctx, wire)
	require.ErrorIs(t, err, constants.ErrMCPNativeToolUnknown)
	require.NotNil(t, failed, "actuator must return a signed failed receipt")
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, failed.GetStatus())
	require.NoError(t, governanceService.VerifyActionReceiptSignature(failed, publicKey))
}

func processOperatorAuditRouteEnvelope(t *testing.T, svc *OperatorPubSubService, ctx context.Context, event constants.EventType, action constants.ActionType, payload proto.Message, investigationID, sessionID string) *operatorv1.ActionReceipt {
	t.Helper()
	receipt, err := svc.ProcessEnvelope(ctx, operatorAuditRouteEnvelopeWire(t, svc, event, action, payload, investigationID, sessionID))
	require.NoError(t, err)
	require.NotNil(t, receipt)
	return receipt
}

func operatorAuditRouteEnvelopeWire(t *testing.T, svc *OperatorPubSubService, event constants.EventType, action constants.ActionType, payload proto.Message, investigationID, sessionID string) []byte {
	t.Helper()
	payloadBytes, err := proto.Marshal(payload)
	require.NoError(t, err)
	now := time.Now().UTC()
	env := &govpkg.GovernanceEnvelope{
		ProtocolVersion:   govpkg.GovernanceProtocolVersionV2,
		Timestamp:         timestamppb.New(now),
		ExpiresAt:         timestamppb.New(now.Add(time.Minute)),
		OperatorId:        svc.config.OperatorID,
		OperatorSessionId: sessionID,
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
	return wire
}

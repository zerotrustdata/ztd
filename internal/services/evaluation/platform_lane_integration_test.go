// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/evaluation"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Exercise the production lane and Operator-local query through the same
// authenticated real-stack wiring as the agent evidence tests. No model runs.
func TestCommandLane_InvestigationLedgerExcludesFixtureAndProbeReceipts(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureDoctrine, constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			env := newRealStackAgentRun(t, realStackOptions{singleRole: true, posture: posture})
			ctx := t.Context()
			target := evaluation.Target{OperatorID: env.dataOperatorID, SessionID: env.dataSessionID}
			lane := evaluation.NewCommandLane(env.gateway, env.persona)
			path := filepath.Join(env.stack.DataHost.WorkingDir, "fixture.txt")
			const investigation = "scored-investigation"
			require.NoError(t, lane.WriteWorkspaceFile(ctx, target, "run", investigation, "attempt", path, "planted fault"))
			content, err := lane.ReadWorkspaceFile(ctx, target, "run", investigation, "attempt", path)
			require.NoError(t, err)
			assert.Equal(t, "planted fault", content)
			payload, err := proto.Marshal(&operatorv1.FsReadRequested{Path: path, ExecutionId: "scored-read"})
			require.NoError(t, err)
			scored, err := lane.DispatchAgentTool(ctx, target, "run", investigation, "attempt", constants.ActionTypeFsRead, path, payload)
			require.NoError(t, err)
			require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, scored.Outcome.Observation.GetReceipt().GetStatus())

			operator, err := env.stack.Gateway.Service.GetRegistrationService().GetOperator(ctx, target.OperatorID)
			require.NoError(t, err)
			publicKey, err := models.DecodeActuatorPublicKey(operator.GetActuatorPublicKey())
			require.NoError(t, err)
			files, err := fs.NewRuntimeFileService(env.stack.DataHost.WorkingDir, testutil.NewTestLogger())
			require.NoError(t, err)

			query := func(scope string) []*operatorv1.ActionReceipt {
				t.Helper()
				args, err := json.Marshal(mcp.AuditReceiptListRequest{InvestigationID: scope, ExcludeAuditReads: true, Limit: 10})
				require.NoError(t, err)
				payload, err := proto.Marshal(&operatorv1.McpCallRequested{ToolName: constants.MCPToolAuditReceiptList, ArgumentsJson: string(args)})
				require.NoError(t, err)
				out, err := lane.DispatchAgentTool(ctx, target, "run", scope, "attempt", constants.ActionTypeMcpCall, constants.MCPToolAuditReceiptList, payload)
				require.NoError(t, err)
				receipt := out.Outcome.Observation.GetReceipt()
				require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.GetStatus())
				require.NoError(t, governance.VerifyActionReceiptSignature(receipt, publicKey))
				var result models.OperatorAuditReceiptListResult
				require.NoError(t, json.Unmarshal([]byte(receipt.GetResultSummary()), &result))
				assert.Equal(t, target.OperatorID, result.OperatorID)
				assert.Equal(t, "operator_local_ledger", result.Authority)
				assert.Equal(t, scope, result.InvestigationID)
				assert.Equal(t, len(result.Receipts), result.Count)
				for _, receipt := range result.Receipts {
					require.NoError(t, governance.VerifyActionReceiptSignature(receipt, publicKey))
					require.NoError(t, governance.VerifyReceiptPersistenceAttestation(receipt, publicKey))
					wantL3 := operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
					if posture.RequiresL3() && receipt.GetActionType() == string(constants.ActionTypeFileEdit) {
						wantL3 = operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
					}
					assert.Equal(t, wantL3, receipt.GetL3Status())
					// These scoped results contain FileEdit and FsRead receipts;
					// ActionReceipt does not carry a target for MCP's finer classification.
					wantL2 := operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
					actionType := constants.ActionType(receipt.GetActionType())
					if posture.RequiresL2() && !actionType.IsBootstrapAction() && actionType.IsStateChange() {
						wantL2 = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
					}
					assert.Equal(t, wantL2, receipt.GetL2Status())
					local, err := storage.ReadLocalEventReceipt(files, testutil.NewTestLogger(), keystore.Options{Keyring: env.stack.DataHost.Keyring}, receipt.GetTransactionId())
					require.NoError(t, err)
					require.NotNil(t, local)
					assert.True(t, proto.Equal(receipt, local.ActionReceipt))
				}
				return result.Receipts
			}

			visible := query(investigation)
			require.Len(t, visible, 1)
			assert.Equal(t, scored.TransactionID, visible[0].GetTransactionId())
			harnessReceipts := query("eval-harness:attempt")
			require.Len(t, harnessReceipts, 2)
			if posture.RequiresL2() {
				validVotes := 0
				for _, receipt := range harnessReceipts {
					if receipt.GetL2Status() == operatorv1.L2Status_L2_STATUS_REQUIRED_VALID {
						validVotes++
					}
				}
				assert.Equal(t, 1, validVotes, "only the FileEdit fixture requires L2; FsRead is read-only")
			}
			assert.Empty(t, query("fresh-investigation"))
			// Every query is recorded in the scored investigation. A repeated
			// query must still exclude all earlier query receipts and itself.
			again := query(investigation)
			require.Len(t, again, 1)
			assert.True(t, proto.Equal(visible[0], again[0]))
			assert.Zero(t, env.stack.Provider.ChatCount())
		})
	}
}

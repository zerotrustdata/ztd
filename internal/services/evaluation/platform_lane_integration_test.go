// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package evaluation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// Persist the scopes emitted by the lane in a real local audit store, then
// query it as investigation history. The transport is a test double; this
// checks receipt selection, not governance proof verification.
func TestCommandLane_InvestigationLedgerExcludesFixtureAndProbeReceipts(t *testing.T) {
	target := Target{OperatorID: "data-operator", SessionID: "data-session"}
	store := storagetest.NewTestAuditStore(t, target.SessionID)
	readResult, err := proto.Marshal(&operatorv1.FsReadResult{
		Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, Content: "fixture",
	})
	require.NoError(t, err)
	transport := &laneTestClient{dispatchOutcome: laneReceiptOutcome(t, &operatorv1.GovernedDispatchResult{
		TransactionId: "tx-1", ResultPayload: readResult, Receipt: laneCompletedReceipt(),
	})}
	lane := NewCommandLane(transport, client.Persona{})
	ctx := t.Context()
	require.NoError(t, lane.WriteWorkspaceFile(ctx, target, "run", "scored-investigation", "attempt", "/tmp/fixture", "planted fault"))
	_, err = lane.ReadWorkspaceFile(ctx, target, "run", "scored-investigation", "attempt", "/tmp/fixture")
	require.NoError(t, err)
	_, err = lane.DispatchAgentTool(ctx, target, "run", "scored-investigation", "attempt", constants.ActionTypeFsRead, "/tmp/fixture", []byte("read"))
	require.NoError(t, err)

	for i, request := range transport.dispatchRequests {
		txID := fmt.Sprintf("scope-receipt-%d", i)
		ts := time.Unix(1_800_000_000+int64(i), 0).UTC()
		require.NoError(t, store.RecordActionReceipt(&models.ActionReceiptRecord{
			TransactionID: txID, TransactionHash: txID,
			OperatorID: target.OperatorID, OperatorSessionID: target.SessionID,
			InvestigationID: request.InvestigationID,
			ActionType:      constants.ActionType(request.ActionType),
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			ExecutedAt:      ts, Timestamp: ts,
			ActionReceipt: &operatorv1.ActionReceipt{TransactionId: txID, Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED},
		}))
	}

	visible, err := store.ListActionReceipts(models.AuditScope{InvestigationID: "scored-investigation"}, 10, 0)
	require.NoError(t, err)
	require.Len(t, visible, 1)
	assert.Equal(t, "scope-receipt-2", visible[0].TransactionID)
	harness, err := store.ListActionReceipts(models.AuditScope{InvestigationID: "eval-harness:attempt"}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, harness, 2)
	fresh, err := store.ListActionReceipts(models.AuditScope{InvestigationID: "fresh-investigation"}, 10, 0)
	require.NoError(t, err)
	assert.Empty(t, fresh)
}

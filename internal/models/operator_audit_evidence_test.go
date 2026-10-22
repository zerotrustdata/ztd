// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestOperatorAuditEventReceiptListResult_ProtoJSONSerialization(t *testing.T) {
	orig := OperatorAuditEventReceiptListResult{
		Count:           1,
		OperatorID:      "op-test",
		Authority:       "operator_local_ledger",
		InvestigationID: "inv-test",
		EventReceipts: []*operatorv1.EventReceipt{
			{
				TransactionId:    "tx-123",
				TransactionHash:  "hash-456",
				Status:           operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
				ResultSummary:    "all good",
				ExecutedAtUnixMs: 1729000000,
				SignerKeyId:      "key-1",
				Signature:        "sig-abc",
				ActionType:       "EXECUTE_BASH",
			},
		},
	}

	data, err := json.Marshal(orig)
	require.NoError(t, err)

	dataStr := string(data)
	// Verify proto field names and string enum names are used
	assert.Contains(t, dataStr, `"transaction_id":"tx-123"`)
	assert.Contains(t, dataStr, `"EXECUTION_STATUS_COMPLETED"`)
	assert.NotContains(t, dataStr, `"transactionId"`)
	assert.False(t, strings.Contains(dataStr, `"status":2`) || strings.Contains(dataStr, `"status": 2`), "must not use numeric enum encoding")

	var decoded OperatorAuditEventReceiptListResult
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, orig.Count, decoded.Count)
	assert.Equal(t, orig.OperatorID, decoded.OperatorID)
	assert.Equal(t, orig.Authority, decoded.Authority)
	assert.Equal(t, orig.InvestigationID, decoded.InvestigationID)
	require.Len(t, decoded.EventReceipts, 1)
	assert.Equal(t, "tx-123", decoded.EventReceipts[0].GetTransactionId())
	assert.Equal(t, "hash-456", decoded.EventReceipts[0].GetTransactionHash())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, decoded.EventReceipts[0].GetStatus())
	assert.Equal(t, "all good", decoded.EventReceipts[0].GetResultSummary())
	assert.Equal(t, int64(1729000000), decoded.EventReceipts[0].GetExecutedAtUnixMs())
	assert.Equal(t, "key-1", decoded.EventReceipts[0].GetSignerKeyId())
	assert.Equal(t, "sig-abc", decoded.EventReceipts[0].GetSignature())
	assert.Equal(t, "EXECUTE_BASH", decoded.EventReceipts[0].GetActionType())
}

func TestOperatorAuditEventReceiptListResult_AcceptsNumericEnum(t *testing.T) {
	// Backward compatibility: test unmarshaling legacy JSON with numeric enum encoding
	rawJSON := `{
		"count": 1,
		"operator_id": "op-test",
		"authority": "operator_local_ledger",
		"investigation_id": "inv-test",
		"event_receipts": [
			{
				"transaction_id": "tx-legacy",
				"status": 2,
				"action_type": "EXECUTE_BASH"
			}
		]
	}`

	var decoded OperatorAuditEventReceiptListResult
	require.NoError(t, json.Unmarshal([]byte(rawJSON), &decoded))
	assert.Equal(t, 1, decoded.Count)
	require.Len(t, decoded.EventReceipts, 1)
	assert.Equal(t, "tx-legacy", decoded.EventReceipts[0].GetTransactionId())
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, decoded.EventReceipts[0].GetStatus())
	assert.Equal(t, "EXECUTE_BASH", decoded.EventReceipts[0].GetActionType())
}

func TestOperatorAuditEventReceiptListResult_NilAndEmptyEventReceipts(t *testing.T) {
	// Nil event receipts slice
	nilResult := OperatorAuditEventReceiptListResult{
		Count:           0,
		OperatorID:      "op-1",
		Authority:       "operator_local_ledger",
		InvestigationID: "inv-1",
	}
	data, err := json.Marshal(nilResult)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"event_receipts":null`)

	var decodedNil OperatorAuditEventReceiptListResult
	require.NoError(t, json.Unmarshal(data, &decodedNil))
	assert.Nil(t, decodedNil.EventReceipts)

	// Empty event receipts slice
	emptyResult := OperatorAuditEventReceiptListResult{
		Count:           0,
		OperatorID:      "op-1",
		Authority:       "operator_local_ledger",
		InvestigationID: "inv-1",
		EventReceipts:   []*operatorv1.EventReceipt{},
	}
	data, err = json.Marshal(emptyResult)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"event_receipts":[]`)

	var decodedEmpty OperatorAuditEventReceiptListResult
	require.NoError(t, json.Unmarshal(data, &decodedEmpty))
	assert.NotNil(t, decodedEmpty.EventReceipts)
	assert.Empty(t, decodedEmpty.EventReceipts)
}

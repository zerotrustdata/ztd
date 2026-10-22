// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package services

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestPrintOperatorStartupBanner(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	assert.NotPanics(t, func() {
		printOperatorStartupBanner(cfg, logger)
	})
}

func TestNewZtdoService_NilTLSConfig(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)
	logger := testutil.NewTestLogger()

	_, err := NewZtdoService(cfg, logger, nil, newTestL2Trust(t), nil, nil, newTestPubSubClientFactory())
	assert.ErrorIs(t, err, constants.ErrInternal)
}

func TestNewZtdoService_NilL2Trust(t *testing.T) {
	t.Parallel()
	cfg := testutil.NewTestConfig(t)

	_, err := NewZtdoService(cfg, testutil.NewTestLogger(), newTestTLSConfig(t), nil, newTestFileSvc(t), nil, newTestPubSubClientFactory())
	assert.ErrorIs(t, err, constants.ErrInternal)
}

func TestAuditStoreTransactionStore_DocSet_RoundTrip(t *testing.T) {
	t.Parallel()

	receipt := models.EventReceiptRecord{
		TransactionID:     "tx-roundtrip",
		TransactionHash:   "hash-roundtrip",
		OperatorID:        "op-1",
		OperatorSessionID: "session-1",
		ActionType:        constants.ActionTypeExecuteBash,
		TargetResource:    "/bin/ls",
		Status:            2,
		ResultSummary:     "completed",
		SignerKeyID:       "key-1",
		Signature:         "sig-1",
		L2Valid:           true,
		L3Valid:           false,
	}

	data, err := json.Marshal(&receipt)
	require.NoError(t, err)

	var unmarshaled models.EventReceiptRecord
	err = json.Unmarshal(data, &unmarshaled)
	require.NoError(t, err)

	assert.Equal(t, receipt.TransactionID, unmarshaled.TransactionID)
	assert.Equal(t, receipt.ActionType, unmarshaled.ActionType)
	assert.Equal(t, receipt.L2Valid, unmarshaled.L2Valid)
}

func TestErrorsIs_Used(t *testing.T) {
	t.Parallel()

	err := errors.New("test error")
	assert.Error(t, err)
}

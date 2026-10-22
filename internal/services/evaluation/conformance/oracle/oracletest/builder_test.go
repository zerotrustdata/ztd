// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package oracletest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/oracle"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestBuilderEventReceiptsVerifyAndClassify(t *testing.T) {
	signer := NewSigner(t)
	anchors, err := oracle.NewAnchorSet([]*evalv1.ConformanceAnchor{signer.Anchor("op-1")})
	require.NoError(t, err)
	opts := EventReceiptOpts{
		TransactionID: "tx-1",
		OperatorID:    "op-1",
		SessionID:     "sess-1",
		ActionType:    "FILE_EDIT",
		L2:            operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
		L3:            operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	executed := signer.Executed(t, opts, "prior-0", "hash-1")
	_, err = oracle.VerifyEventReceipt(anchors, "op-1", executed)
	require.NoError(t, err)
	outcome, err := oracle.Classify(executed)
	require.NoError(t, err)
	assert.Equal(t, oracle.OutcomeExecuted, outcome.Kind)
	require.NotNil(t, outcome.Commitment)
	assert.Equal(t, oracle.Link{Prior: "prior-0", Hash: "hash-1", TransactionID: "tx-1"}, *outcome.Commitment)

	rejected := signer.Rejected(t, opts, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE)
	_, err = oracle.VerifyEventReceipt(anchors, "op-1", rejected)
	require.NoError(t, err)
	outcome, err = oracle.Classify(rejected)
	require.NoError(t, err)
	assert.Equal(t, oracle.OutcomeRejected, outcome.Kind)
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE, outcome.RejectionCode)
	assert.Nil(t, outcome.Commitment)
}

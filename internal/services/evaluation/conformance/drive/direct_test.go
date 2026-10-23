// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package drive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/evaluation/conformance/cases"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/client"
	"github.com/zerotrustdata/ztd/internal/tools/agent_harness/config"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func receiptBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.ActionReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	})
	require.NoError(t, err)
	return body
}

func rejectionBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.GovernanceRejection{
		Error: "L4 rejected the envelope",
		Receipt: &operatorv1.ActionReceipt{
			TransactionId: "tx-2",
			Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
			RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY,
		},
	})
	require.NoError(t, err)
	return body
}

func refusalBody(t *testing.T) []byte {
	t.Helper()
	body, err := response.MarshalProtoJSONCanonical(&operatorv1.IngressRefusal{
		Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_MALFORMED_REQUEST,
		Error: "refused before envelope",
	})
	require.NoError(t, err)
	return body
}

func TestNewDirectRejectsFaultInTwoSets(t *testing.T) {
	t.Parallel()
	c, err := client.New(config.Config{MTLSBaseURL: "http://127.0.0.1:1"})
	require.NoError(t, err)

	_, err = NewDirect(c, testIdentity().Persona, testIdentity().Target, nil, BaseMutators(), MutatorSet{
		cases.FaultExpired: {},
	})
	assert.ErrorIs(t, err, constants.ErrAlreadyExists)
}

func TestNewDirectRejectsNilClient(t *testing.T) {
	t.Parallel()
	_, err := NewDirect(nil, testIdentity().Persona, testIdentity().Target, nil)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

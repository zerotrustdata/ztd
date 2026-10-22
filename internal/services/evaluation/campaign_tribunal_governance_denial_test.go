// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
	"google.golang.org/protobuf/proto"
)

func TestIsTribunalL2GovernanceDenialRequiresTypedFailureAndSignedReceipt(t *testing.T) {
	validReceipt := &operatorv1.ActionReceipt{
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		ActionType:    string(constants.ActionTypeInference),
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET,
		L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		SignerKeyId:   "operator-key",
		Signature:     "signed-receipt",
	}
	member := func(receipt *operatorv1.ActionReceipt, code operatorv1.InferenceDispatchFailureCode) *tribunalv1.TribunalMemberCall {
		return &tribunalv1.TribunalMemberCall{
			Receipt: receipt,
			Failure: &operatorv1.InferenceDispatchFailure{Code: code},
		}
	}
	governanceFailure := member(validReceipt, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED)
	err := memberCallError(governanceFailure)
	assert.True(t, isTribunalL2GovernanceDenial(governanceFailure, err))
	assert.False(t, isTribunalL2GovernanceDenial(member(nil, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED), err), "missing receipt must remain an infrastructure failure")
	unsigned := proto.Clone(validReceipt).(*operatorv1.ActionReceipt)
	unsigned.Signature = ""
	assert.False(t, isTribunalL2GovernanceDenial(member(unsigned, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED), err), "unsigned receipt must remain an infrastructure failure")
	wrongL2 := proto.Clone(validReceipt).(*operatorv1.ActionReceipt)
	wrongL2.L2Status = operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
	assert.False(t, isTribunalL2GovernanceDenial(member(wrongL2, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED), err))
	assert.False(t, isTribunalL2GovernanceDenial(governanceFailure, constants.ErrInferenceBackendUnavailable), "provider failures must not be recategorized")
}

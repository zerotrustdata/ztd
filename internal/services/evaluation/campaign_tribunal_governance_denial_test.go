// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package evaluation

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
	"google.golang.org/protobuf/proto"
)

func TestIsL2GovernanceDenialRequiresTypedFailureAndSignedEventReceipt(t *testing.T) {
	validEventReceipt := &operatorv1.EventReceipt{
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		ActionType:    string(constants.ActionTypeInference),
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET,
		L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		SignerKeyId:   "operator-key",
		Signature:     "signed-event_receipt",
	}
	member := func(eventReceipt *operatorv1.EventReceipt, code operatorv1.InferenceDispatchFailureCode) *tribunalv1.TribunalMemberCall {
		return &tribunalv1.TribunalMemberCall{
			EventReceipt: eventReceipt,
			Failure:      &operatorv1.InferenceDispatchFailure{Code: code},
		}
	}
	governanceFailure := member(validEventReceipt, operatorv1.InferenceDispatchFailureCode_INFERENCE_DISPATCH_FAILURE_CODE_GOVERNANCE_REJECTED)
	err := memberCallError(governanceFailure)
	assert.True(t, isL2GovernanceDenial(governanceFailure.GetEventReceipt(), err))
	assert.True(t, isL2GovernanceDenial(validEventReceipt, fmt.Errorf("turn inference turn 1: %w", constants.ErrInferenceGovernanceRejected)), "a scored agent turn's signed L2 rejection is a governance denial")
	assert.False(t, isL2GovernanceDenial(nil, err), "missing event receipt must remain an infrastructure failure")
	unsigned := proto.Clone(validEventReceipt).(*operatorv1.EventReceipt)
	unsigned.Signature = ""
	assert.False(t, isL2GovernanceDenial(unsigned, err), "unsigned event receipt must remain an infrastructure failure")
	wrongL2 := proto.Clone(validEventReceipt).(*operatorv1.EventReceipt)
	wrongL2.L2Status = operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
	assert.False(t, isL2GovernanceDenial(wrongL2, err))
	assert.False(t, isL2GovernanceDenial(validEventReceipt, constants.ErrInferenceBackendUnavailable), "provider failures must not be recategorized")
}

// TestAgentLoop_QuorumDeniedTurnIsPolicyDenialWithoutProviderAttempt proves a
// scored turn whose INFERENCE the Inference runtime rejected for a required L2
// quorum ends as policy_denial, keeps its signed rejection event receipt, and maps to
// a record with no provider attempt, so no observation or provenance window is
// expected for it.
func TestAgentLoop_QuorumDeniedTurnIsPolicyDenialWithoutProviderAttempt(t *testing.T) {
	eventReceipt := &operatorv1.EventReceipt{
		TransactionId: "tx-turn-denied",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		ActionType:    string(constants.ActionTypeInference),
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET,
		L2Status:      operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED,
		SignerKeyId:   "inference-key",
		Signature:     "signed-event_receipt",
	}
	denied := &governance.EventReceiptError{EventReceipt: eventReceipt, Err: constants.ErrInferenceGovernanceRejected}

	t.Run("signed L2 rejection", func(t *testing.T) {
		run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{probeErr: denied}, &tribunalAuthoringTools{})
		require.NoError(t, run.loop(context.Background()))
		assert.Equal(t, AgentTerminalClassificationPolicyDenial, run.trace.TerminalClassification)
		assert.Empty(t, run.trace.ProviderFailure)
		assert.Empty(t, run.trace.ProviderFailureClass)
		require.Len(t, run.trace.ModelCalls, 1)
		call := run.trace.ModelCalls[0]
		assert.Equal(t, modelCallGovernanceRejected, call.Error)
		assert.Equal(t, "tx-turn-denied", call.TransactionID)
		assert.NotEmpty(t, call.ProviderAttemptID, "the trace keeps the attempt its signed event receipt answers")
		require.NotNil(t, call.EventReceipt)
		assert.Same(t, eventReceipt, call.EventReceipt.EventReceipt)

		records, _ := modelInferenceRecordsFromAgentTrace(run.req, run.trace, run.executor.newID)
		require.Len(t, records, 1)
		assert.Empty(t, records[0].GetProviderAttemptId(), "a governance-rejected inference has no provider attempt")
	})

	t.Run("unsigned rejection remains a harness failure", func(t *testing.T) {
		unsigned := proto.Clone(eventReceipt).(*operatorv1.EventReceipt)
		unsigned.Signature = ""
		run := newTribunalAuthoringRun(t, false, &tribunalAuthoringInference{probeErr: &governance.EventReceiptError{EventReceipt: unsigned, Err: constants.ErrInferenceGovernanceRejected}}, &tribunalAuthoringTools{})
		require.NoError(t, run.loop(context.Background()))
		assert.Equal(t, AgentTerminalClassificationHarnessFailure, run.trace.TerminalClassification)
		assert.NotEmpty(t, run.trace.ProviderFailure)
	})
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package governance

import (
	"context"
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/testutil"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// newEventReceiptPostureActuator returns a persisting actuator whose runtime identity is the
// one the strict verifier uses, so an envelope signed for that runtime is filed under it.
func newEventReceiptPostureActuator(t *testing.T) (*L5Actuator, *storage.SQLAuditStore) {
	t.Helper()
	actuator, auditStore, _ := newPersistingActuator(t)
	require.NoError(t, auditStore.CreateSession("operator-session-1", "operator", "Test Session", "test-user"))
	actuator.Identity = &testutil.MockExecutionTarget{OperatorIDs: []string{"operator-1"}, SessionID: "operator-session-1"}
	return actuator, auditStore
}

// verifyAndExecuteEventReceipt runs one envelope through L4 and then L5, and returns the
// decided transaction with the signed event receipt L5 built from it.
func verifyAndExecuteEventReceipt(t *testing.T, verifier *L4Warden, actuator *L5Actuator, env *govtypes.GovernanceEnvelope) (*VerifiedTransaction, *operatorv1.EventReceipt) {
	t.Helper()
	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	require.NoError(t, err)
	require.NotNil(t, verified)
	eventReceipt, err := actuator.Execute(context.Background(), verified, nil)
	require.NoError(t, err)
	require.NotNil(t, eventReceipt)
	return verified, eventReceipt
}

// eventReceiptStageOutcome returns the outcome of the event receipt's one stage of the given kind.
func eventReceiptStageOutcome(t *testing.T, eventReceipt *operatorv1.EventReceipt, kind operatorv1.DeterministicStageKind) operatorv1.DeterministicStageOutcome {
	t.Helper()
	for _, stage := range eventReceipt.GetDeterministicStageEvidence() {
		if stage.GetKind() == kind {
			return stage.GetOutcome()
		}
	}
	require.FailNowf(t, "event receipt has no stage", "missing %s stage", kind)
	return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_UNSPECIFIED
}

// TestEventReceiptPostureReadRecordsNotRequiredL3UnderRatifyAndNotary verifies that a read-only
// action, which L3 never gates, records L3_STATUS_NOT_REQUIRED and a NOT_REQUIRED L3 stage
// under the postures that gate mutations on L3, and that the chain validates.
func TestEventReceiptPostureReadRecordsNotRequiredL3UnderRatifyAndNotary(t *testing.T) {
	for _, posture := range []constants.Posture{constants.PostureRatify, constants.PostureNotary} {
		t.Run(string(posture), func(t *testing.T) {
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			actuator, _ := newEventReceiptPostureActuator(t)
			env := signedEnvelope(t, constants.ActionTypeFsRead, typedPayload(t, constants.ActionTypeFsRead), privKey, posture)

			verified, eventReceipt := verifyAndExecuteEventReceipt(t, verifier, actuator, env)

			assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, verified.L3Status)
			assert.Equal(t, operatorv1.L3Status_L3_STATUS_NOT_REQUIRED, eventReceipt.GetL3Status())
			assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED,
				eventReceiptStageOutcome(t, eventReceipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY))
			_, err := ValidateDeterministicProtocolChain(eventReceipt)
			require.NoError(t, err)
		})
	}
}

// TestEventReceiptPostureBootstrapRecordsNotRequiredL2UnderConsensus verifies that a platform
// enrollment bootstrap action, which predates the consensus tribunal, records
// L2_STATUS_NOT_REQUIRED and a NOT_REQUIRED L2 stage under consensus, and that the chain validates.
func TestEventReceiptPostureBootstrapRecordsNotRequiredL2UnderConsensus(t *testing.T) {
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	actuator, _ := newEventReceiptPostureActuator(t)
	actionType := constants.ActionTypePlatformEnrollmentCreate
	env := signedEnvelope(t, actionType, typedPayload(t, actionType), privKey, "consensus")
	// Bootstrap predates the tribunal, so it carries neither L2 votes nor an L3 proof.
	env.Governance.L2 = nil
	env.Governance.L3 = nil
	rehash(t, env)

	verified, eventReceipt := verifyAndExecuteEventReceipt(t, verifier, actuator, env)

	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, verified.L2Status)
	assert.Equal(t, operatorv1.L2Status_L2_STATUS_NOT_REQUIRED, eventReceipt.GetL2Status())
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED,
		eventReceiptStageOutcome(t, eventReceipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2))
	_, err := ValidateDeterministicProtocolChain(eventReceipt)
	require.NoError(t, err)
}

// TestEventReceiptPostureMutationWithValidL3RecordsRequiredValidUnderRatify verifies that a
// mutation under ratify with a valid human notary proof records L3_STATUS_REQUIRED_VALID
// and a VERIFIED L3 stage, and that the chain validates.
func TestEventReceiptPostureMutationWithValidL3RecordsRequiredValidUnderRatify(t *testing.T) {
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	actuator, _ := newEventReceiptPostureActuator(t)
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "ratify")

	verified, eventReceipt := verifyAndExecuteEventReceipt(t, verifier, actuator, env)

	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, verified.L3Status)
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_VALID, eventReceipt.GetL3Status())
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED,
		eventReceiptStageOutcome(t, eventReceipt, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY))
	_, err := ValidateDeterministicProtocolChain(eventReceipt)
	require.NoError(t, err)
}

// TestEventReceiptPostureUndecidedLayerFailsClosedBeforeDispatch verifies that a verified
// transaction reaching L5 without an L2 or L3 decision is recorded as a FAILED event_receipt
// that keeps the undecided status UNSPECIFIED, wraps ErrInternal, and never runs the handler.
func TestEventReceiptPostureUndecidedLayerFailsClosedBeforeDispatch(t *testing.T) {
	tests := []struct {
		name      string
		undecide  func(vt *VerifiedTransaction)
		undecided func(eventReceipt *operatorv1.EventReceipt) bool
	}{
		{
			name:     "undecided L2",
			undecide: func(vt *VerifiedTransaction) { vt.L2Status = operatorv1.L2Status_L2_STATUS_UNSPECIFIED },
			undecided: func(eventReceipt *operatorv1.EventReceipt) bool {
				return eventReceipt.GetL2Status() == operatorv1.L2Status_L2_STATUS_UNSPECIFIED
			},
		},
		{
			name:     "undecided L3",
			undecide: func(vt *VerifiedTransaction) { vt.L3Status = operatorv1.L3Status_L3_STATUS_UNSPECIFIED },
			undecided: func(eventReceipt *operatorv1.EventReceipt) bool {
				return eventReceipt.GetL3Status() == operatorv1.L3Status_L3_STATUS_UNSPECIFIED
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
			actuator, auditStore := newEventReceiptPostureActuator(t)
			handler := actuator.ExecutionHandler.(*mockExecutionHandler)
			env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "ratify")
			verified, err := verifier.VerifyEnvelope(context.Background(), env)
			require.NoError(t, err)
			tt.undecide(verified)

			eventReceipt, err := actuator.Execute(context.Background(), verified, nil)

			require.ErrorIs(t, err, constants.ErrInternal)
			require.NotNil(t, eventReceipt)
			assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, eventReceipt.GetStatus())
			assert.True(t, tt.undecided(eventReceipt), "the undecided layer must stay UNSPECIFIED on the event receipt, never a default")
			assert.False(t, handler.executed, "an undecided transaction must not reach the execution handler")
			persisted, err := auditStore.GetEventReceipt(env.Id)
			require.NoError(t, err)
			require.NotNil(t, persisted)
			assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, persisted.Status)
		})
	}
}

// TestEventReceiptPostureDeniedL3IsSignedRejectionAndSpendsTheNonce verifies that a
// human deny is rejected at L4 with L3_DENIED even when a valid proof would pass,
// that the runtime signs the rejection, and that the denied transaction can never
// run afterwards: the same envelope with a valid proof is a replay.
func TestEventReceiptPostureDeniedL3IsSignedRejectionAndSpendsTheNonce(t *testing.T) {
	verifier, privKey := createStrictVerifier(t, testutil.NewStatefulMockReplayStore(), testutil.NewMockStateRootProvider("root-1"), testutil.NewConfigurableMockL3Notary(true))
	actuator, _ := newEventReceiptPostureActuator(t)
	env := signedEnvelope(t, constants.ActionTypeExecuteBash, typedPayload(t, constants.ActionTypeExecuteBash), privKey, "ratify")
	approved := proto.Clone(env.Governance.GetL3()).(*commonv1.L3Metadata)
	env.Governance.L3 = &commonv1.L3Metadata{Denied: true}

	verified, err := verifier.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrTxL3Denied)
	require.NotNil(t, verified, "a deny must reach L5 as a rejection to sign")
	eventReceipt, err := actuator.RecordRejectedTransaction(context.Background(), verified, err)
	require.NoError(t, err)
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED, eventReceipt.GetRejectionCode())
	assert.Equal(t, operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED, eventReceipt.GetL3Status())
	assert.NoError(t, VerifyEventReceiptSignature(eventReceipt, actuator.SigningKey.Public().(ed25519.PublicKey)))

	env.Governance.L3 = approved
	_, err = verifier.VerifyEnvelope(context.Background(), env)
	require.ErrorIs(t, err, constants.ErrTxTransactionReplay, "a denied transaction must never execute later")
}

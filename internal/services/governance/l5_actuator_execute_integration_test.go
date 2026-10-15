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
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const l5SecretEmail = "alice@example.com"

func TestL5ActuatorRecordRejectedTransactionSignsFailedStageEvidence(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	publicKey := actuator.SigningKey.Public().(ed25519.PublicKey)
	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-rejected-hash",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
	}
	failedStage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
		governanceMonotonicNow(),
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
	)
	rejected := &VerifiedTransaction{
		Envelope:                   envelope,
		ActionType:                 constants.ActionTypeExecuteBash,
		L2Status:                   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:                   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{failedStage},
	}

	receipt, err := actuator.RecordRejectedTransaction(context.Background(), rejected, constants.ErrTxStateRootMismatch)
	require.NoError(t, err)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, receipt.Status)
	require.Contains(t, receipt.ResultSummary, constants.ErrTxStateRootMismatch.Error())
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH, receipt.RejectionCode, "the signed receipt names the failing check")
	require.Len(t, receipt.DeterministicStageEvidence, 1)
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED, receipt.DeterministicStageEvidence[0].Outcome)
	assert.False(t, actuator.ExecutionHandler.(*mockExecutionHandler).executed)

	canonical, err := CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)
	signature, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(publicKey, canonical, signature))
	require.NotNil(t, receipt.FinalPersistenceAttestation)
	assert.NoError(t, VerifyReceiptPersistenceAttestation(receipt, publicKey))
}

func TestL5ActuatorExecuteHappyPath(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	pubKey := actuator.SigningKey.Public().(ed25519.PublicKey)

	// Configure handler to succeed (already set in newTestActuator)

	// Create verified transaction
	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-1234567890abcdef",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	// Execute
	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// Verify receipt fields
	require.Equal(t, envelope.Id, receipt.TransactionId)
	require.Equal(t, envelope.TransactionHash, receipt.TransactionHash)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status)
	require.Equal(t, "completed", receipt.ResultSummary)
	require.Equal(t, "test-state-root-123", receipt.StateRootBefore)
	require.Equal(t, "test-state-root-123", receipt.StateRootAfter)
	require.Equal(t, "test-Actuator-key", receipt.SignerKeyId)
	require.NotEmpty(t, receipt.Signature)
	require.Len(t, receipt.DeterministicStageEvidence, 3)
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE, receipt.DeterministicStageEvidence[0].Kind)
	assert.NotEmpty(t, receipt.DeterministicStageEvidence[0].ReceiptSignatureDigest)
	assert.Equal(t, envelope.Id, receipt.DeterministicStageEvidence[0].AuditRecordId)
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND, receipt.DeterministicStageEvidence[1].Kind)
	assert.Equal(t, operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION, receipt.DeterministicStageEvidence[2].Kind)
	assert.Equal(t, receipt.DeterministicStageEvidence[2].StageId, receipt.DeterministicStageEvidence[0].ParentStageId)
	assert.Equal(t, operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, receipt.DeterministicStageEvidence[2].Outcome)
	assert.Equal(t, receipt.StateRootBefore, receipt.DeterministicStageEvidence[2].StateRootBefore)
	assert.Equal(t, receipt.StateRootAfter, receipt.DeterministicStageEvidence[2].StateRootAfter)

	// Verify signature
	canonical, err := CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)

	sigBytes, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)

	valid := ed25519.Verify(pubKey, canonical, sigBytes)
	require.True(t, valid, "Receipt signature should verify against L5Actuator public key")

	tampered := proto.Clone(receipt).(*operatorv1.ActionReceipt)
	tampered.DeterministicStageEvidence[2].StateRootAfter = "tampered-root"
	tamperedCanonical, err := CanonicalizeActionReceipt(tampered)
	require.NoError(t, err)
	assert.False(t, ed25519.Verify(pubKey, tamperedCanonical, sigBytes))

	// The receipts table holds exactly one row per transaction, finalized as COMPLETED
	persisted, err := auditStore.GetActionReceipt(envelope.Id)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, persisted.Status)
	require.NotNil(t, receipt.FinalPersistenceAttestation)
	require.NoError(t, VerifyReceiptPersistenceAttestation(receipt, pubKey))
}

func TestL5ActuatorExecuteHandlerError(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	pubKey := actuator.SigningKey.Public().(ed25519.PublicKey)

	// Configure handler to return error
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	handler.err = errors.New("handler execution failed")

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-1234567890abcdef",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	// Execute
	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "handler execution failed")
	require.NotNil(t, receipt)

	// Verify receipt has FAILED status
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, receipt.Status)
	require.Contains(t, receipt.ResultSummary, "handler execution failed")

	// Verify signature is still valid
	canonical, err := CanonicalizeActionReceipt(receipt)
	require.NoError(t, err)

	sigBytes, err := hex.DecodeString(receipt.Signature)
	require.NoError(t, err)

	valid := ed25519.Verify(pubKey, canonical, sigBytes)
	require.True(t, valid, "Receipt signature should verify even when handler fails")

	// Verify the persisted receipt carries the FAILED status
	persisted, err := auditStore.GetActionReceipt(receipt.TransactionId)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, persisted.Status)
}

func TestL5ActuatorExecuteAuditWriteFailInitial(t *testing.T) {
	actuator, _, db := newPersistingActuator(t)
	failReceiptWrites(t, db, "INSERT")

	// Execute - the initial receipt cannot be persisted, so the handler must not run
	receipt, err := actuator.Execute(context.Background(), newFailureTestVT(t, "test-initial-write-failure"), nil)
	require.ErrorIs(t, err, constants.ErrL5ActuatorCommitmentPersist)
	require.Nil(t, receipt)
	assert.False(t, actuator.ExecutionHandler.(*mockExecutionHandler).executed, "handler must not run when the initial receipt is not persisted")
}

func TestL5ActuatorExecuteFinalPersistenceAttestationWriteFailure(t *testing.T) {
	actuator, auditStore, db := newPersistingActuator(t)
	failReceiptWrites(t, db, "UPDATE")
	vt := newFailureTestVT(t, "test-final-persistence-write-failure")

	receipt, err := actuator.Execute(context.Background(), vt, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrL5ActuatorLogReceipt)
	assert.ErrorIs(t, err, constants.ErrL5ActuatorAuditStore)
	require.NotNil(t, receipt)
	assert.Nil(t, receipt.FinalPersistenceAttestation, "an unpersisted final receipt must not carry a persistence attestation")
	persisted, getErr := auditStore.GetActionReceipt(vt.Envelope.Id)
	require.NoError(t, getErr)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING, persisted.Status, "only the initial receipt is persisted")
}

// TestL5ActuatorExecuteCallsReceiptPublisherOnSuccess verifies that Execute
// calls ReceiptPublisher.PublishActionReceipt with the original command
// envelope and the final signed receipt after successful execution.
func TestL5ActuatorExecuteCallsReceiptPublisherOnSuccess(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	publisher := &mockReceiptPublisher{}
	actuator.ReceiptPublisher = publisher

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-receipt-pub",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
		RequestorUserId:   "user-001",
		ActingAppId:       "spiffe://ztd.local/app/test-app",
	}
	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	require.Equal(t, 1, publisher.callCount(), "PublishActionReceipt must be called exactly once after successful execution")
	require.Equal(t, envelope, publisher.envelope, "publisher must receive the original command envelope")
	require.Equal(t, receipt.TransactionId, publisher.receipt.TransactionId, "publisher must receive the final receipt")
	require.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, publisher.receipt.Status)
	require.NotEmpty(t, publisher.receipt.Signature, "publisher must receive a signed receipt")
}

// TestL5ActuatorExecuteDoesNotCallReceiptPublisherWhenFinalReceiptFails
// verifies that Execute does not call ReceiptPublisher.PublishActionReceipt
// when signAndLogFinalReceipt fails (the receipt is not finalized).
func TestL5ActuatorExecuteDoesNotCallReceiptPublisherWhenFinalReceiptFails(t *testing.T) {
	actuator, _, db := newPersistingActuator(t)
	// The final receipt write is an update of the initial row; refuse it so
	// signAndLogFinalReceipt fails before the ReceiptPublisher is reached.
	failReceiptWrites(t, db, "UPDATE")

	publisher := &mockReceiptPublisher{}
	actuator.ReceiptPublisher = publisher

	_, err := actuator.Execute(context.Background(), newFailureTestVT(t, "test-hash-receipt-pub-fail"), nil)
	require.Error(t, err, "Execute must return an error when signAndLogFinalReceipt fails")
	require.Equal(t, 0, publisher.callCount(), "PublishActionReceipt must not be called when signAndLogFinalReceipt fails")
}

// TestL5ActuatorExecuteNilReceiptPublisherDoesNotPanic verifies that a nil
// ReceiptPublisher (the gateway in-process operator path) does not cause a
// panic and execution proceeds normally.
func TestL5ActuatorExecuteNilReceiptPublisherDoesNotPanic(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	// ReceiptPublisher is nil by default (not set in newTestActuator).
	require.Nil(t, actuator.ReceiptPublisher)

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-nil-pub",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}
	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	assert.NotPanics(t, func() {
		receipt, err := actuator.Execute(context.Background(), vt, nil)
		require.NoError(t, err)
		require.NotNil(t, receipt)
	})
}

// TestL5ActuatorExecuteReceiptPublisherErrorDoesNotFailExecution verifies
// that a ReceiptPublisher publish error is best-effort: the execution
// succeeds and the receipt is returned, with the publish error only logged.
func TestL5ActuatorExecuteReceiptPublisherErrorDoesNotFailExecution(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	publisher := &mockReceiptPublisher{publishError: errors.New("publish failed")}
	actuator.ReceiptPublisher = publisher

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-pub-err",
		OperatorId:        "test-operator",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}
	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err, "publish error must not fail the execution")
	require.NotNil(t, receipt)
	require.Equal(t, 1, publisher.callCount(), "PublishActionReceipt must still be attempted")
}

func l5ScrubTestVT(t *testing.T, target string) *VerifiedTransaction {
	t.Helper()
	return &VerifiedTransaction{
		Envelope: &govtypes.GovernanceEnvelope{
			Id:                mustUUID(t),
			TransactionHash:   "test-hash-l5-scrub",
			OperatorId:        "test-operator",
			OperatorSessionId: "test-operator-session",
			EventType:         string(constants.EventOperatorCommandRequested),
			ActionType:        string(constants.ActionTypeExecuteBash),
			TargetResource:    target,
		},
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

// The signed receipt, its published envelope and its commitment carry the scrubbed target and the
// scrubbed failure text. The capability and the execution handler keep the real target.
func TestL5Actuator_ScrubsTargetAndFailureSummaryBeforeSigning(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	publisher := &mockReceiptPublisher{}
	actuator.ReceiptPublisher = publisher

	target := "mailto:" + l5SecretEmail
	var handlerTarget string
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	handler.ExecuteVerifiedTransactionFunc = func(ctx context.Context, _ constants.EventType, _ CommandMessage) (string, error) {
		handlerTarget = CapabilityFromContext(ctx).TargetResource
		return "", fmt.Errorf("could not write to %s", l5SecretEmail)
	}

	receipt, err := actuator.Execute(context.Background(), l5ScrubTestVT(t, target), nil)
	require.Error(t, err)
	require.NotNil(t, receipt)

	assert.Equal(t, target, handlerTarget, "the handler keeps the real target")
	assert.NotContains(t, receipt.ResultSummary, l5SecretEmail, "failure text is scrubbed before it is signed")
	assert.Contains(t, receipt.ResultSummary, "failed: ")
	require.NotNil(t, publisher.envelope)
	assert.NotContains(t, publisher.envelope.TargetResource, l5SecretEmail, "published envelope carries the scrubbed target")
}

// Without an active scrubber a target is refused before any receipt is signed or the handler runs.
func TestL5Actuator_WithoutScrubberRefusesTargetBeforeExecution(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *scrubbing.ScrubbingService
	}{
		{name: "nil scrubber", s: nil},
		{name: "disabled scrubber", s: disabledL5Scrubber(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actuator, _ := newCommitmentTestActuator(t)
			actuator.Scrubbing = tc.s

			receipt, err := actuator.Execute(context.Background(), l5ScrubTestVT(t, "/srv/"+l5SecretEmail), nil)
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Nil(t, receipt)
			assert.False(t, actuator.ExecutionHandler.(*mockExecutionHandler).executed, "handler must not run")

			rejected, err := actuator.RecordRejectedTransaction(context.Background(), l5ScrubTestVT(t, "/srv/"+l5SecretEmail), errors.New("denied"))
			require.ErrorIs(t, err, constants.ErrEgressScrubberUnavailable)
			assert.Nil(t, rejected)
		})
	}
}

// Without a scrubber and without a target, the signed summary is fixed text: no handler or rejection
// text is signed raw.
func TestL5Actuator_WithoutScrubberSignsFixedSummary(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	actuator.Scrubbing = nil
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	handler.ExecuteVerifiedTransactionFunc = func(context.Context, constants.EventType, CommandMessage) (string, error) {
		return "", fmt.Errorf("could not write to %s", l5SecretEmail)
	}

	receipt, err := actuator.Execute(context.Background(), l5ScrubTestVT(t, ""), nil)
	require.Error(t, err)
	require.NotNil(t, receipt)
	assert.Equal(t, "failed: "+classifyReceiptFailure(err).String(), receipt.ResultSummary)
	assert.NotContains(t, receipt.ResultSummary, l5SecretEmail)

	rejected, err := actuator.RecordRejectedTransaction(context.Background(), l5ScrubTestVT(t, ""), errors.New("state mismatch for "+l5SecretEmail))
	require.NoError(t, err)
	assert.Equal(t, "rejected: "+operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED.String(), rejected.ResultSummary)
}

// digestCommandMessage is a CommandMessage that carries an inference result digest on the typed channel.
type digestCommandMessage struct {
	payload []byte
	digest  string
}

func (m *digestCommandMessage) GetPayload() []byte            { return m.payload }
func (m *digestCommandMessage) SetPayload(p []byte)           { m.payload = p }
func (m *digestCommandMessage) InferenceResultDigest() string { return m.digest }

// The inference result digest is the one identifier the Gateway checks against the receipt. It
// arrives on the typed channel of an INFERENCE action and is signed as is, with or without a scrubber.
func TestL5Actuator_InferenceDigestRidesTheTypedChannel(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    func(*testing.T, *L5Actuator) *scrubbing.ScrubbingService
	}{
		{name: "active scrubber", s: func(_ *testing.T, a *L5Actuator) *scrubbing.ScrubbingService { return a.Scrubbing }},
		{name: "nil scrubber", s: func(*testing.T, *L5Actuator) *scrubbing.ScrubbingService { return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actuator, _ := newCommitmentTestActuator(t)
			actuator.Scrubbing = tc.s(t, actuator)
			digest := models.SHA256Hex([]byte("result"))
			handler := actuator.ExecutionHandler.(*mockExecutionHandler)
			handler.ExecuteVerifiedTransactionFunc = func(context.Context, constants.EventType, CommandMessage) (string, error) {
				return digest, nil
			}
			vt := l5ScrubTestVT(t, "")
			vt.ActionType = constants.ActionTypeInference
			vt.Envelope.ActionType = string(constants.ActionTypeInference)

			receipt, err := actuator.Execute(context.Background(), vt, &digestCommandMessage{digest: digest})
			require.NoError(t, err)
			assert.Equal(t, digest, receipt.ResultSummary)
		})
	}
}

// A 64-character lowercase hex string is not special: a hex-encoded key returned by a tool must not
// be signed verbatim. Only the typed channel of an INFERENCE action carries a digest through.
func TestL5Actuator_HexOutputIsNeverPassedThroughAsADigest(t *testing.T) {
	hexSecret := models.SHA256Hex([]byte("hex-encoded key file contents"))
	require.True(t, models.IsSHA256Hex(hexSecret))

	run := func(t *testing.T, a *L5Actuator, actionType constants.ActionType, msg CommandMessage) *operatorv1.ActionReceipt {
		t.Helper()
		handler := a.ExecutionHandler.(*mockExecutionHandler)
		handler.ExecuteVerifiedTransactionFunc = func(context.Context, constants.EventType, CommandMessage) (string, error) {
			return hexSecret, nil
		}
		vt := l5ScrubTestVT(t, "")
		vt.ActionType = actionType
		vt.Envelope.ActionType = string(actionType)
		receipt, err := a.Execute(context.Background(), vt, msg)
		require.NoError(t, err)
		return receipt
	}

	t.Run("native tool output with a scrubber is scrubbed text", func(t *testing.T) {
		actuator, _ := newCommitmentTestActuator(t)
		cfg := scrubbing.DefaultConfig()
		cfg.CustomScrubPatterns = map[string]string{"HEXKEY": `\b[0-9a-f]{64}\b`}
		scrubber, err := scrubbing.NewScrubbingService(context.Background(), cfg, testutil.NewTestLogger(), nil)
		require.NoError(t, err)
		actuator.Scrubbing = scrubber
		require.NotEqual(t, hexSecret, scrubber.ScrubText(hexSecret), "the test scrubber must match a bare 64-hex string")

		receipt := run(t, actuator, constants.ActionTypeExecuteBash, nil)
		assert.NotContains(t, receipt.ResultSummary, hexSecret, "the hex output must not be signed verbatim")
		assert.Equal(t, scrubber.ScrubText(hexSecret), receipt.ResultSummary, "the output goes through the scrubber like any text")
	})

	for name, scrubber := range map[string]*scrubbing.ScrubbingService{"nil scrubber": nil, "disabled scrubber": disabledL5Scrubber(t)} {
		t.Run("native tool output with "+name+" is fixed text", func(t *testing.T) {
			actuator, _ := newCommitmentTestActuator(t)
			actuator.Scrubbing = scrubber
			receipt := run(t, actuator, constants.ActionTypeExecuteBash, nil)
			assert.NotContains(t, receipt.ResultSummary, hexSecret)
			assert.Equal(t, "completed", receipt.ResultSummary)
		})
	}

	t.Run("an inference action whose handler produced no typed digest signs no hex text", func(t *testing.T) {
		actuator, _ := newCommitmentTestActuator(t)
		actuator.Scrubbing = nil
		receipt := run(t, actuator, constants.ActionTypeInference, &digestCommandMessage{})
		assert.NotContains(t, receipt.ResultSummary, hexSecret)
	})

	t.Run("a digest on the typed channel is ignored for every other action type", func(t *testing.T) {
		actuator, _ := newCommitmentTestActuator(t)
		actuator.Scrubbing = nil
		receipt := run(t, actuator, constants.ActionTypeExecuteBash, &digestCommandMessage{digest: hexSecret})
		assert.NotContains(t, receipt.ResultSummary, hexSecret)
	})
}

func disabledL5Scrubber(t *testing.T) *scrubbing.ScrubbingService {
	t.Helper()
	cfg := scrubbing.DefaultConfig()
	cfg.Enabled = false
	svc, err := scrubbing.NewScrubbingService(context.Background(), cfg, testutil.NewTestLogger(), nil)
	require.NoError(t, err)
	return svc
}

func TestL5Actuator_MintsAndDissolvesCapability(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)

	var capturedCap *Capability
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	handler.ExecuteVerifiedTransactionFunc = func(ctx context.Context, _ constants.EventType, _ CommandMessage) (string, error) {
		capturedCap = CapabilityFromContext(ctx)
		return "ok", nil
	}

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-cap-123",
		OperatorId:        "op-1",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		ExpiresAt:  time.Now().Add(5 * time.Minute),
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.NoError(t, err)
	require.NotNil(t, receipt)

	// During execution, the handler should have received a valid capability
	require.NotNil(t, capturedCap, "handler should have received a capability via context")
	assert.Equal(t, "test-hash-cap-123", capturedCap.TransactionHash)
	assert.Equal(t, constants.ActionTypeExecuteBash, capturedCap.ActionType)

	// After execution, the capability should be dissolved
	assert.True(t, capturedCap.IsDissolved(), "capability must be dissolved after execution")
}

func TestL5Actuator_CapabilityDissolvedOnHandlerError(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)

	var capturedCap *Capability
	handler := actuator.ExecutionHandler.(*mockExecutionHandler)
	handler.err = nil // set below via func
	handler.ExecuteVerifiedTransactionFunc = func(ctx context.Context, _ constants.EventType, _ CommandMessage) (string, error) {
		capturedCap = CapabilityFromContext(ctx)
		return "", errors.New("simulated execution failure")
	}

	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-hash-cap-err",
		OperatorId:        "op-1",
		OperatorSessionId: "test-operator-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
		TargetResource:    "localhost",
	}

	vt := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		ExpiresAt:  time.Now().Add(5 * time.Minute),
	}

	receipt, err := actuator.Execute(context.Background(), vt, nil)
	require.Error(t, err)
	require.NotNil(t, receipt)

	// Capability should still be dissolved even on handler error
	require.NotNil(t, capturedCap)
	assert.True(t, capturedCap.IsDissolved(), "capability must be dissolved even when handler fails")
}

// failingReceiptWriteDB installs a trigger that aborts the given receipts write,
// so the real audit store reports a write failure at that stage.
func failReceiptWrites(t *testing.T, db *sqliteutil.DB, op string) {
	t.Helper()
	_, err := db.ExecWithRetry(context.Background(), "CREATE TRIGGER fail_receipt_"+strings.ToLower(op)+" BEFORE "+op+" ON receipts BEGIN SELECT RAISE(ABORT, 'receipt write refused'); END")
	require.NoError(t, err)
}

func newFailureTestVT(t *testing.T, hash string) *VerifiedTransaction {
	t.Helper()
	return &VerifiedTransaction{
		Envelope: &govtypes.GovernanceEnvelope{
			Id:                mustUUID(t),
			TransactionHash:   hash,
			OperatorId:        "test-operator",
			OperatorSessionId: "test-operator-session",
			EventType:         string(constants.EventOperatorCommandRequested),
			ActionType:        string(constants.ActionTypeExecuteBash),
			TargetResource:    "localhost",
		},
		ActionType: constants.ActionTypeExecuteBash,
		L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
	}
}

// rejectedForeignVT is the transaction L4 returns for an envelope addressed to
// another Operator under a session the runtime never started.
func rejectedForeignVT(t *testing.T) *VerifiedTransaction {
	t.Helper()
	envelope := &govtypes.GovernanceEnvelope{
		Id:                mustUUID(t),
		TransactionHash:   "test-foreign-hash",
		OperatorId:        "remote-operator",
		OperatorSessionId: "foreign-cli-session",
		EventType:         string(constants.EventOperatorCommandRequested),
		ActionType:        string(constants.ActionTypeExecuteBash),
	}
	failedStage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
		governanceMonotonicNow(),
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
	)
	return &VerifiedTransaction{
		Envelope:                   envelope,
		ActionType:                 constants.ActionTypeExecuteBash,
		L2Status:                   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
		L3Status:                   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		DeterministicStageEvidence: []*operatorv1.DeterministicStageEvidence{failedStage},
	}
}

// TestL5ActuatorRejectionReceiptFilesUnderRuntimeSession proves a rejection of
// an envelope that claims another Operator and an unknown session is recorded
// under the runtime's own audit session, with the claimed identity preserved in
// the signed receipt, instead of being lost to AUDIT_SESSION_UNKNOWN.
func TestL5ActuatorRejectionReceiptFilesUnderRuntimeSession(t *testing.T) {
	actuator, auditStore := newCommitmentTestActuator(t)
	publisher := &mockReceiptPublisher{}
	actuator.ReceiptPublisher = publisher
	vt := rejectedForeignVT(t)

	receipt, err := actuator.RecordRejectedTransaction(context.Background(), vt, fmt.Errorf("%w: %q", constants.ErrTxTargetOperatorMismatch, "remote-operator"))
	require.NoError(t, err)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, receipt.Status)
	assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, receipt.FailureCode)
	assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_OPERATOR_MISMATCH, receipt.RejectionCode, "a wrapped sentinel still classifies")
	assert.False(t, actuator.ExecutionHandler.(*mockExecutionHandler).executed)

	persisted, err := auditStore.GetActionReceipt(vt.Envelope.Id)
	require.NoError(t, err)
	require.NotNil(t, persisted, "the rejection evidence must be persisted")
	assert.Equal(t, "test-operator-session", persisted.OperatorSessionID, "the audit row belongs to the runtime's own session")
	assert.Equal(t, "test-operator", persisted.OperatorID)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED, persisted.Status)
	require.NotNil(t, persisted.ActionReceipt)
	assert.Equal(t, operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, persisted.ActionReceipt.FailureCode)

	require.Len(t, receipt.DeterministicStageEvidence, 1)
	assert.Equal(t, "remote-operator", receipt.DeterministicStageEvidence[0].OperatorId, "the claimed operator stays visible in the signed receipt")
	assert.Equal(t, "foreign-cli-session", receipt.DeterministicStageEvidence[0].OperatorSessionId, "the claimed session stays visible in the signed receipt")
	assert.NoError(t, VerifyActionReceiptSignature(receipt, actuator.SigningKey.Public().(ed25519.PublicKey)))

	require.Equal(t, 1, publisher.callCount())
	published := publisher.envelope
	assert.Equal(t, "test-operator", published.OperatorId, "the receipt is published on the runtime's own receipts channel")
	assert.Equal(t, "test-operator-session", published.OperatorSessionId)
	assert.Equal(t, "remote-operator", vt.Envelope.OperatorId, "the rejected envelope is not mutated")
}

// TestL5ActuatorRejectionCodeIsSignatureBound proves the rejection code is part
// of the signed bytes: the untouched receipt verifies, and a receipt whose
// rejection_code is changed after signing fails signature verification.
func TestL5ActuatorRejectionCodeIsSignatureBound(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	publicKey := actuator.SigningKey.Public().(ed25519.PublicKey)
	vt := rejectedForeignVT(t)

	receipt, err := actuator.RecordRejectedTransaction(context.Background(), vt, constants.ErrTxStateRootMismatch)
	require.NoError(t, err)
	require.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH, receipt.RejectionCode)
	require.NoError(t, VerifyActionReceiptSignature(receipt, publicKey))

	tampered := proto.Clone(receipt).(*operatorv1.ActionReceipt)
	tampered.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE
	assert.Error(t, VerifyActionReceiptSignature(tampered, publicKey), "a changed rejection_code must break the signature")

	cleared := proto.Clone(receipt).(*operatorv1.ActionReceipt)
	cleared.RejectionCode = operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED
	assert.Error(t, VerifyActionReceiptSignature(cleared, publicKey), "clearing rejection_code must break the signature")
}

// TestL5ActuatorRejectionReceiptRequiresRuntimeIdentity proves a runtime with no
// identity of its own refuses to record a rejection under the envelope's claim.
func TestL5ActuatorRejectionReceiptRequiresRuntimeIdentity(t *testing.T) {
	cases := map[string]RuntimeIdentityProvider{
		"unwired":   nil,
		"unclaimed": &testutil.MockExecutionTarget{OperatorIDs: []string{"test-operator"}},
	}
	for name, identity := range cases {
		t.Run(name, func(t *testing.T) {
			actuator, auditStore := newCommitmentTestActuator(t)
			actuator.Identity = identity
			vt := rejectedForeignVT(t)

			receipt, err := actuator.RecordRejectedTransaction(context.Background(), vt, constants.ErrTxTargetOperatorMismatch)

			require.ErrorIs(t, err, constants.ErrL5ActuatorRuntimeIdentityMissing)
			assert.Nil(t, receipt)
			persisted, getErr := auditStore.GetActionReceipt(vt.Envelope.Id)
			require.NoError(t, getErr)
			assert.Nil(t, persisted)
		})
	}
}

// TestL5ActuatorRejectionReceiptPersistFailureIsNotAMutation proves a rejection
// whose receipt cannot be persisted reports a rejection-receipt failure, never
// the executed-mutation log failure.
func TestL5ActuatorRejectionReceiptPersistFailureIsNotAMutation(t *testing.T) {
	actuator, _ := newCommitmentTestActuator(t)
	actuator.Identity = &testutil.MockExecutionTarget{OperatorIDs: []string{"test-operator"}, SessionID: "session-the-runtime-never-started"}

	receipt, err := actuator.RecordRejectedTransaction(context.Background(), rejectedForeignVT(t), constants.ErrTxTargetOperatorMismatch)

	require.ErrorIs(t, err, constants.ErrL5ActuatorRejectionReceiptPersist)
	assert.NotErrorIs(t, err, constants.ErrL5ActuatorLogReceipt)
	assert.Nil(t, receipt)
}

// TestL5ActuatorExecuteUnboundEnvelopeFilesUnderRuntimeSession proves a verified
// envelope that names no session (app record writes, platform enrollment) is
// recorded under the runtime's own session, and refuses to run without one.
func TestL5ActuatorExecuteUnboundEnvelopeFilesUnderRuntimeSession(t *testing.T) {
	newUnboundVT := func(t *testing.T) *VerifiedTransaction {
		t.Helper()
		return &VerifiedTransaction{
			Envelope: &govtypes.GovernanceEnvelope{
				Id:              mustUUID(t),
				TransactionHash: "test-unbound-hash",
				EventType:       string(constants.EventOperatorCommandRequested),
				ActionType:      string(constants.ActionTypeExecuteBash),
				TargetResource:  "localhost",
			},
			ActionType: constants.ActionTypeExecuteBash,
			L2Status:   operatorv1.L2Status_L2_STATUS_NOT_REQUIRED,
			L3Status:   operatorv1.L3Status_L3_STATUS_NOT_REQUIRED,
		}
	}

	t.Run("own session", func(t *testing.T) {
		actuator, auditStore := newCommitmentTestActuator(t)
		vt := newUnboundVT(t)

		receipt, err := actuator.Execute(context.Background(), vt, nil)
		require.NoError(t, err)
		assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, receipt.Status)

		persisted, err := auditStore.GetActionReceipt(vt.Envelope.Id)
		require.NoError(t, err)
		require.NotNil(t, persisted)
		assert.Equal(t, "test-operator-session", persisted.OperatorSessionID)
		assert.Equal(t, "test-operator", persisted.OperatorID)
	})

	t.Run("no runtime session", func(t *testing.T) {
		actuator, _ := newCommitmentTestActuator(t)
		actuator.Identity = &testutil.MockExecutionTarget{OperatorIDs: []string{"test-operator"}}

		receipt, err := actuator.Execute(context.Background(), newUnboundVT(t), nil)
		require.ErrorIs(t, err, constants.ErrL5ActuatorRuntimeIdentityMissing)
		assert.Nil(t, receipt)
		assert.False(t, actuator.ExecutionHandler.(*mockExecutionHandler).executed, "nothing may execute without a session to record it under")
	})
}

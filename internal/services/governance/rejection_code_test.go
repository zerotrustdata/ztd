// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// rejectionCodeCases covers every entry in governanceRejectionCodes, one row
// per sentinel, in the same order as the classifier table.
var rejectionCodeCases = []struct {
	name     string
	sentinel error
	code     operatorv1.GovernanceRejectionCode
}{
	{"vault locked", constants.ErrWardenVaultLocked, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED},
	{"vault unavailable", constants.ErrWardenVaultUnavailable, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED},

	{"doctrine missing", constants.ErrTxDoctrineMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"replay store missing", constants.ErrTxReplayStoreMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"state root missing", constants.ErrTxStateRootMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"L2 signer store not configured", constants.ErrTxL2SignerStoreNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"L2 consensus not configured", constants.ErrTxL2ConsensusNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"L3 notary not configured", constants.ErrTxL3NotaryNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{"payload decoder missing", constants.ErrTxPayloadDecoderMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},

	{"L1 doctrine validation failed", constants.ErrTxL1ValidationFailed, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE},
	{"protocol version unsupported", constants.ErrTxProtocolVersionUnsupported, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_PROTOCOL_VERSION_UNSUPPORTED},
	{"event/action mismatch", constants.ErrTxEventActionMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH},
	{"transaction expired", constants.ErrTxTransactionExpired, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED},
	{"transaction replay", constants.ErrTxTransactionReplay, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY},
	{"transaction in flight", constants.ErrTxInFlight, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY},
	{"document collection not governed", constants.ErrTxDocumentCollectionNotGoverned, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_DOCUMENT_COLLECTION_NOT_GOVERNED},
	{"MCP target mismatch", constants.ErrTxMcpTargetMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_MCP_TARGET_MISMATCH},
	{"target operator mismatch", constants.ErrTxTargetOperatorMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_OPERATOR_MISMATCH},
	{"target session mismatch", constants.ErrTxTargetSessionMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_SESSION_MISMATCH},
	{"transaction hash mismatch", constants.ErrTxTransactionHashMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_HASH_MISMATCH},
	{"transaction id mismatch", constants.ErrTxTransactionIDMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_ID_MISMATCH},
	{"state root mismatch", constants.ErrTxStateRootMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH},
	{"posture missing", constants.ErrEnvelopePostureMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_POSTURE_MISSING},

	{"L2 signature missing", constants.ErrTxL2SignatureMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING},
	{"L2 signature invalid", constants.ErrTxL2SignatureInvalid, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_SIGNATURE_INVALID},
	{"L2 quorum not met", constants.ErrTxL2QuorumNotMet, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET},
	{"L2 duplicate signer", constants.ErrTxL2DuplicateSigner, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_DUPLICATE_SIGNER},
	{"L2 consensus set unknown", constants.ErrTxL2ConsensusSetUnknown, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_CONSENSUS_SET_UNKNOWN},
	{"L3 proof missing", constants.ErrTxL3ProofMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_MISSING},
	{"L3 proof invalid", constants.ErrTxL3ProofInvalid, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID},
	{"L3 denied", constants.ErrTxL3Denied, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED},

	{"invalid envelope", constants.ErrTxInvalidEnvelope, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"transaction id missing", constants.ErrTxTransactionIDMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"transaction hash missing", constants.ErrTxTransactionHashMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"expires_at missing", constants.ErrTxExpiresAtMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"nonce missing", constants.ErrTxNonceMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"state root required", constants.ErrTxStateRootRequired, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"payload missing", constants.ErrTxPayloadMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"payload decode failed", constants.ErrTxPayloadDecodeFailed, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"unknown action type", constants.ErrTxUnknownActionType, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"unknown event type", constants.ErrTxUnknownEventType, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"event type missing", constants.ErrTxEventTypeMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"event not a request", constants.ErrTxEventNotRequest, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{"event not governed", constants.ErrTxEventNotGoverned, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
}

// directWardenSentinels lists every sentinel that L4Warden.VerifyEnvelope,
// verifyStateless, verifyStateful, verifyPosture, verifyL2Posture, or
// verifyL3Posture can return directly (or wrapped with %w) on a rejection path.
// Each must classify to a specific code, never UNCLASSIFIED.
var directWardenSentinels = []struct {
	source   string
	sentinel error
}{
	// VerifyEnvelope
	{"VerifyEnvelope", constants.ErrTxInvalidEnvelope},
	{"VerifyEnvelope/checkRuntimeVault", constants.ErrWardenVaultUnavailable},
	{"VerifyEnvelope/checkRuntimeVault", constants.ErrWardenVaultLocked},
	{"VerifyEnvelope/trackInFlight", constants.ErrTxInFlight},
	{"VerifyEnvelope/reserve nonce", constants.ErrTxReplayStoreMissing},
	{"VerifyEnvelope", constants.ErrTxExpiresAtMissing},
	{"VerifyEnvelope", constants.ErrTxTransactionExpired},
	{"VerifyEnvelope", constants.ErrTxNonceMissing},
	{"VerifyEnvelope", constants.ErrTxTransactionReplay},
	{"VerifyEnvelope/postureFromEnvelope", constants.ErrEnvelopePostureMissing},
	// verifyStateless
	{"verifyStateless", constants.ErrTxProtocolVersionUnsupported},
	{"verifyStateless", constants.ErrTxDoctrineMissing},
	{"verifyStateless", constants.ErrTxTransactionIDMissing},
	{"verifyStateless/ValidateGovernedRequest", constants.ErrTxUnknownEventType},
	{"verifyStateless/ValidateGovernedRequest", constants.ErrTxEventNotRequest},
	{"verifyStateless/ValidateGovernedRequest", constants.ErrTxEventNotGoverned},
	{"verifyStateless/ValidateGovernedEnvelopeFields", constants.ErrTxEventActionMismatch},
	{"verifyStateless", constants.ErrTxUnknownActionType},
	{"verifyStateless", constants.ErrTxPayloadMissing},
	{"verifyStateless/DecodePayloadForAction", constants.ErrTxPayloadDecoderMissing},
	{"verifyStateless/DecodePayloadForAction", constants.ErrTxPayloadDecodeFailed},
	{"verifyStateless/verifyDocumentCollection", constants.ErrTxDocumentCollectionNotGoverned},
	{"verifyStateless/verifyMcpCallTarget", constants.ErrTxMcpTargetMismatch},
	{"verifyStateless", constants.ErrTxTargetOperatorMismatch},
	{"verifyStateless/verifyTargetSession", constants.ErrTxTargetSessionMismatch},
	{"verifyStateless", constants.ErrTxL1ValidationFailed},
	{"verifyStateless", constants.ErrTxTransactionHashMissing},
	{"verifyStateless", constants.ErrTxTransactionHashMismatch},
	{"verifyStateless", constants.ErrTxTransactionIDMismatch},
	// verifyStateful
	{"verifyStateful", constants.ErrTxStateRootRequired},
	{"verifyStateful", constants.ErrTxStateRootMissing},
	{"verifyStateful", constants.ErrTxStateRootMismatch},
	// verifyPosture / verifyL2Posture
	{"verifyL2Posture", constants.ErrTxL2SignatureMissing},
	{"verifyL2Posture", constants.ErrTxL2SignerStoreNotConfigured},
	{"verifyL2Posture", constants.ErrTxL2ConsensusNotConfigured},
	{"verifyL2Posture", constants.ErrTxL2ConsensusSetUnknown},
	{"verifyL2Posture", constants.ErrTxL2DuplicateSigner},
	{"verifyL2Posture", constants.ErrTxL2SignatureInvalid},
	{"verifyL2Posture", constants.ErrTxL2QuorumNotMet},
	// verifyPosture / verifyL3Posture
	{"verifyL3Posture", constants.ErrTxL3ProofMissing},
	{"verifyL3Posture", constants.ErrTxL3NotaryNotConfigured},
	{"verifyL3Posture", constants.ErrTxL3ProofInvalid},
}

func TestClassifyGovernanceRejection(t *testing.T) {
	t.Run("nil rejection is UNSPECIFIED", func(t *testing.T) {
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED, ClassifyGovernanceRejection(nil))
	})

	for _, tc := range rejectionCodeCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.code, ClassifyGovernanceRejection(tc.sentinel), "bare sentinel")
			assert.Equal(t, tc.code, ClassifyGovernanceRejection(fmt.Errorf("ctx: %w", tc.sentinel)), "single-wrapped sentinel")
			assert.Equal(t, tc.code, ClassifyGovernanceRejection(fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", tc.sentinel))), "double-wrapped sentinel")
		})
	}

	t.Run("unknown error is UNCLASSIFIED", func(t *testing.T) {
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED, ClassifyGovernanceRejection(errors.New("something unrelated")))
	})

	t.Run("wrapped unknown error is UNCLASSIFIED", func(t *testing.T) {
		assert.Equal(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED, ClassifyGovernanceRejection(fmt.Errorf("l4 warden: reserve nonce: %w", errors.New("db closed"))))
	})
}

// TestClassifyGovernanceRejectionTableCoversEveryEntry fails when a classifier
// entry has no row in the explicit case table, so the table cannot drift.
func TestClassifyGovernanceRejectionTableCoversEveryEntry(t *testing.T) {
	require.Equal(t, len(governanceRejectionCodes), len(rejectionCodeCases), "table rows must match classifier entries one-to-one")
	for _, entry := range governanceRejectionCodes {
		found := false
		for _, tc := range rejectionCodeCases {
			if errors.Is(entry.sentinel, tc.sentinel) && tc.code == entry.code {
				found = true
				break
			}
		}
		assert.True(t, found, "classifier entry %v -> %v has no table row", entry.sentinel, entry.code)
	}
}

// TestClassifyGovernanceRejectionCoversDirectWardenSentinels proves every
// sentinel the L4 verification path can return directly classifies to a
// specific code. A failure here names a sentinel missing from
// governanceRejectionCodes.
func TestClassifyGovernanceRejectionCoversDirectWardenSentinels(t *testing.T) {
	for _, tc := range directWardenSentinels {
		t.Run(tc.source+"/"+tc.sentinel.Error(), func(t *testing.T) {
			code := ClassifyGovernanceRejection(fmt.Errorf("%w", tc.sentinel))
			assert.NotEqual(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED, code,
				"sentinel returned from %s must have a classifier entry", tc.source)
			assert.NotEqual(t, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED, code)
		})
	}
}

func TestWithRejectionEventReceiptWrapsOnlyGovernanceRejections(t *testing.T) {
	verifyErr := fmt.Errorf("verify: %w: 3s ago", constants.ErrTxTransactionExpired)
	rejected := &operatorv1.EventReceipt{
		TransactionId: "tx-1",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED,
	}

	err := WithRejectionEventReceipt(rejected, verifyErr)
	require.ErrorIs(t, err, constants.ErrTxTransactionExpired)
	resp := RejectionResponse(fmt.Errorf("dispatch: %w", err))
	require.NotNil(t, resp)
	assert.Equal(t, constants.ErrTxTransactionExpired.Error(), resp.GetError(), "only the sentinel renders, never the chain")
	assert.Same(t, rejected, resp.GetEventReceipt())

	executionFailure := &operatorv1.EventReceipt{
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_UNSPECIFIED,
	}
	assert.Same(t, verifyErr, WithRejectionEventReceipt(executionFailure, verifyErr))
	assert.Same(t, verifyErr, WithRejectionEventReceipt(nil, verifyErr))
	assert.NoError(t, WithRejectionEventReceipt(rejected, nil))
	assert.Nil(t, RejectionResponse(verifyErr))
}

func TestWithFailedEventReceiptCarriesEveryFailedEventReceipt(t *testing.T) {
	providerErr := fmt.Errorf("dispatch: %w", constants.ErrInferenceBackendUnavailable)
	failed := &operatorv1.EventReceipt{
		TransactionId: "tx-failed",
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE,
	}

	err := WithFailedEventReceipt(failed, providerErr)
	require.ErrorIs(t, err, constants.ErrInferenceBackendUnavailable)
	assert.Same(t, failed, FailedEventReceipt(fmt.Errorf("gateway: %w", err)))
	assert.Nil(t, RejectionResponse(err), "an execution failure is not a governance rejection")

	rejected := &operatorv1.EventReceipt{
		Status:      operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode: operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
	}
	rejectedErr := WithFailedEventReceipt(rejected, providerErr)
	assert.Same(t, rejected, FailedEventReceipt(rejectedErr))
	assert.NotNil(t, RejectionResponse(rejectedErr))

	completed := &operatorv1.EventReceipt{Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
	assert.Same(t, providerErr, WithFailedEventReceipt(completed, providerErr))
	assert.Same(t, providerErr, WithFailedEventReceipt(nil, providerErr))
	assert.NoError(t, WithFailedEventReceipt(failed, nil))
	assert.Nil(t, FailedEventReceipt(providerErr))
}

func TestRejectionResponseUnmappedErrorRendersEventReceiptCode(t *testing.T) {
	rejected := &operatorv1.EventReceipt{
		Status:        operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		FailureCode:   operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED,
		RejectionCode: operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE,
	}
	resp := RejectionResponse(WithRejectionEventReceipt(rejected, errors.New("relay: rejected")))
	require.NotNil(t, resp)
	assert.Equal(t, "GOVERNANCE_REJECTION_CODE_L1_DOCTRINE", resp.GetError())
}

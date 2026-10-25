// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"errors"

	"github.com/zerotrustdata/ztd/internal/constants"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// governanceRejectionCodes maps each verification sentinel to the check it
// names. Order matters only where one error can wrap several sentinels; the
// first match wins.
var governanceRejectionCodes = []struct {
	sentinel error
	code     operatorv1.GovernanceRejectionCode
}{
	{constants.ErrWardenVaultLocked, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED},
	{constants.ErrWardenVaultUnavailable, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_RUNTIME_VAULT_LOCKED},

	{constants.ErrTxDoctrineMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxReplayStoreMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxStateRootMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxL2SignerStoreNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxL2ConsensusNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxL3NotaryNotConfigured, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},
	{constants.ErrTxPayloadDecoderMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_VERIFIER_MISCONFIGURED},

	{constants.ErrTxL1ValidationFailed, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L1_DOCTRINE},
	{constants.ErrTxProtocolVersionUnsupported, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_PROTOCOL_VERSION_UNSUPPORTED},
	{constants.ErrTxEventActionMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EVENT_ACTION_MISMATCH},
	{constants.ErrTxTransactionExpired, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_EXPIRED},
	{constants.ErrTxTransactionReplay, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY},
	{constants.ErrTxInFlight, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_REPLAY},
	{constants.ErrTxDocumentCollectionNotGoverned, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_DOCUMENT_COLLECTION_NOT_GOVERNED},
	{constants.ErrTxMcpTargetMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_MCP_TARGET_MISMATCH},
	{constants.ErrTxTargetOperatorMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_OPERATOR_MISMATCH},
	{constants.ErrTxTargetSessionMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TARGET_SESSION_MISMATCH},
	{constants.ErrTxTransactionHashMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_HASH_MISMATCH},
	{constants.ErrTxTransactionIDMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_TRANSACTION_ID_MISMATCH},
	{constants.ErrTxStateRootMismatch, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_STATE_ROOT_MISMATCH},
	{constants.ErrEnvelopePostureMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_POSTURE_MISSING},

	{constants.ErrTxL2SignatureMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_PROOF_MISSING},
	{constants.ErrTxL2SignatureInvalid, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_SIGNATURE_INVALID},
	{constants.ErrTxL2QuorumNotMet, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_QUORUM_NOT_MET},
	{constants.ErrTxL2DuplicateSigner, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_DUPLICATE_SIGNER},
	{constants.ErrTxL2ConsensusSetUnknown, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L2_CONSENSUS_SET_UNKNOWN},
	{constants.ErrTxL3ProofMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_MISSING},
	{constants.ErrTxL3ProofInvalid, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_PROOF_INVALID},
	{constants.ErrTxL3Denied, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_L3_DENIED},

	{constants.ErrTxInvalidEnvelope, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxTransactionIDMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxTransactionHashMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxExpiresAtMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxNonceMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxStateRootRequired, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxPayloadMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxPayloadDecodeFailed, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxUnknownActionType, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxUnknownEventType, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxEventTypeMissing, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxEventNotRequest, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
	{constants.ErrTxEventNotGoverned, operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_ENVELOPE_MALFORMED},
}

// ClassifyGovernanceRejection names the verification check behind a rejection
// error. The code is stamped on the signed rejection receipt so verifiers can
// attribute the rejection to a layer without reading result_summary. An
// unmapped error is UNCLASSIFIED, never a guessed layer.
func ClassifyGovernanceRejection(rejection error) operatorv1.GovernanceRejectionCode {
	code, _ := GovernanceRejectionSentinel(rejection)
	return code
}

// GovernanceRejectionSentinel returns the check a rejection error names and
// the verification sentinel it wraps. An ingress renders the sentinel, never
// the wrapped chain. An unmapped error returns a nil sentinel and
// UNCLASSIFIED; a nil error returns UNSPECIFIED.
func GovernanceRejectionSentinel(rejection error) (operatorv1.GovernanceRejectionCode, error) {
	if rejection == nil {
		return operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNSPECIFIED, nil
	}
	for _, entry := range governanceRejectionCodes {
		if errors.Is(rejection, entry.sentinel) {
			return entry.code, entry.sentinel
		}
	}
	return operatorv1.GovernanceRejectionCode_GOVERNANCE_REJECTION_CODE_UNCLASSIFIED, nil
}

// ReceiptError carries the executing Operator's signed FAILED receipt
// alongside the error that ended the action, so every ingress can hand the
// caller the signed evidence instead of text: a GOVERNANCE_REJECTED receipt
// (operatorv1.GovernanceRejection), or the receipt of an execution whose
// handler failed and still appended its ledger commitment. It unwraps to the
// error.
type ReceiptError struct {
	Receipt *operatorv1.EventReceipt
	Err     error
}

func (e *ReceiptError) Error() string { return e.Err.Error() }
func (e *ReceiptError) Unwrap() error { return e.Err }

// WithRejectionReceipt wraps err in a *ReceiptError when receipt is a
// GOVERNANCE_REJECTED receipt; otherwise it returns err unchanged.
func WithRejectionReceipt(receipt *operatorv1.EventReceipt, err error) error {
	if err == nil || !IsGovernanceRejectionReceipt(receipt) {
		return err
	}
	return &ReceiptError{Receipt: receipt, Err: err}
}

// WithFailedReceipt wraps err in a *ReceiptError when receipt is any signed
// FAILED receipt — a governance rejection or a failed execution; otherwise it
// returns err unchanged.
func WithFailedReceipt(receipt *operatorv1.EventReceipt, err error) error {
	if err == nil || receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED {
		return err
	}
	return &ReceiptError{Receipt: receipt, Err: err}
}

// FailedReceipt returns the signed FAILED receipt err carries, or nil.
func FailedReceipt(err error) *operatorv1.EventReceipt {
	var carried *ReceiptError
	if !errors.As(err, &carried) {
		return nil
	}
	return carried.Receipt
}

// IsGovernanceRejectionReceipt reports whether receipt records an L4
// governance rejection.
func IsGovernanceRejectionReceipt(receipt *operatorv1.EventReceipt) bool {
	return receipt != nil &&
		receipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED &&
		receipt.GetFailureCode() == operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
}

// RejectionResponse returns the typed client body for err when it carries a
// signed rejection receipt, and nil otherwise. error renders only the
// governance sentinel, never the wrapped chain; an unmapped error renders the
// receipt's rejection code.
func RejectionResponse(err error) *operatorv1.GovernanceRejection {
	var rejection *ReceiptError
	if !errors.As(err, &rejection) || !IsGovernanceRejectionReceipt(rejection.Receipt) {
		return nil
	}
	text := rejection.Receipt.GetRejectionCode().String()
	if _, sentinel := GovernanceRejectionSentinel(rejection.Err); sentinel != nil {
		text = sentinel.Error()
	}
	return &operatorv1.GovernanceRejection{Error: text, Receipt: rejection.Receipt, ConsensusInferences: ConsensusInferences(err)}
}

// ReceiptRejection returns the typed client body for a signed rejection receipt
// that arrives without its error, such as the L3_DENIED receipt of an approval
// decision, and nil for any other receipt. error renders the rejection code.
func ReceiptRejection(receipt *operatorv1.EventReceipt) *operatorv1.GovernanceRejection {
	if !IsGovernanceRejectionReceipt(receipt) {
		return nil
	}
	return &operatorv1.GovernanceRejection{Error: receipt.GetRejectionCode().String(), Receipt: receipt}
}

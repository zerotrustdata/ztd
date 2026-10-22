// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package oracletest builds signed action receipts that pass oracle.VerifyReceipt,
// and the complete inference results they bind, as inputs to verifier tests in
// the oracle and its callers. It is imported only from _test.go files.
//
// Each Signer is a fresh test key. A receipt built here shows only that a
// verifier accepts well-formed, correctly signed bytes and rejects tampered
// ones; it is never Operator output. An end-to-end test that needs an
// Operator's receipts must run a real Operator and read them from its audit
// store. It must not sign receipts with a Signer to stand in for that Operator.
package oracletest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"

	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const (
	rootBefore        = "root-before"
	rootAfter         = "root-after"
	executedAtUnixMs  = 1_700_000_001_000
	persistedAtUnixMs = 1_700_000_002_000
)

// Signer holds one actuator key and signs receipts the way an L5 actuator does.
type Signer struct {
	Priv  ed25519.PrivateKey
	Pub   ed25519.PublicKey
	KeyID string
}

// ReceiptOpts names the identity and posture a built receipt carries.
type ReceiptOpts struct {
	TransactionID, OperatorID, SessionID, ActionType string
	ResultSummary                                    string
	L2                                               operatorv1.L2Status
	L3                                               operatorv1.L3Status
}

// NewSigner generates a fresh actuator key.
func NewSigner(t testing.TB) Signer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate actuator key: %v", err)
	}
	return Signer{Priv: priv, Pub: pub, KeyID: hex.EncodeToString(pub)}
}

// Anchor pins this signer's key to runtimeID, as an export would.
func (s Signer) Anchor(runtimeID string) *evalv1.ConformanceAnchor {
	return &evalv1.ConformanceAnchor{RuntimeId: runtimeID, KeyId: s.KeyID, PublicKeyHex: hex.EncodeToString(s.Pub)}
}

// Executed returns a COMPLETED receipt whose commitment link is prior -> hash.
func (s Signer) Executed(t testing.TB, opts ReceiptOpts, prior, hash string) *operatorv1.ActionReceipt {
	t.Helper()
	receipt := s.newReceipt(t, opts, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED)
	receipt.StateRootBefore = rootBefore
	receipt.StateRootAfter = rootAfter
	receipt.DeterministicStageEvidence = executedStages(opts, s.KeyID, prior, hash,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED)
	s.Resign(t, receipt)
	return receipt
}

// ExecutionFailed returns a FAILED receipt whose handler ran and failed: it
// carries a failed L5 execution stage and still appends the commitment link
// prior -> hash.
func (s Signer) ExecutionFailed(t testing.TB, opts ReceiptOpts, prior, hash string) *operatorv1.ActionReceipt {
	t.Helper()
	receipt := s.newReceipt(t, opts, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED)
	receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
	receipt.StateRootBefore = rootBefore
	receipt.StateRootAfter = rootAfter
	receipt.DeterministicStageEvidence = executedStages(opts, s.KeyID, prior, hash,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED)
	s.Resign(t, receipt)
	return receipt
}

// Rejected returns a signed GOVERNANCE_REJECTED receipt with code and no commitment.
func (s Signer) Rejected(t testing.TB, opts ReceiptOpts, code operatorv1.GovernanceRejectionCode) *operatorv1.ActionReceipt {
	t.Helper()
	receipt := s.newReceipt(t, opts, operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED)
	receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
	receipt.RejectionCode = code
	l4 := stage(opts, "L4", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED, "")
	receipt.DeterministicStageEvidence = []*operatorv1.DeterministicStageEvidence{l4}
	s.Resign(t, receipt)
	return receipt
}

// Resign re-signs a receipt after a test mutates it (signature and attestation).
func (s Signer) Resign(t testing.TB, receipt *operatorv1.ActionReceipt) {
	t.Helper()
	receipt.FinalPersistenceAttestation = nil
	payload, err := governance.CanonicalizeActionReceipt(receipt)
	if err != nil {
		t.Fatalf("canonicalize receipt: %v", err)
	}
	receipt.Signature = hex.EncodeToString(ed25519.Sign(s.Priv, payload))

	attestation := &operatorv1.ReceiptPersistenceAttestation{
		TransactionId:          receipt.TransactionId,
		ReceiptSignatureDigest: governance.SignatureDigest([]string{receipt.Signature}),
		PersistedAtUnixMs:      persistedAtUnixMs,
		AuditRecordId:          receipt.TransactionId,
		SignerKeyId:            receipt.SignerKeyId,
	}
	attestationPayload, err := governance.CanonicalizeReceiptPersistenceAttestation(attestation)
	if err != nil {
		t.Fatalf("canonicalize persistence attestation: %v", err)
	}
	attestation.Signature = hex.EncodeToString(ed25519.Sign(s.Priv, attestationPayload))
	receipt.FinalPersistenceAttestation = attestation
}

// InferenceResult returns the complete result an Inference Operator returns for
// req: it echoes the request's campaign binding and model identity, answers
// with parts and carries its computed result digest. A receipt for it binds
// that digest as its result summary.
func InferenceResult(t testing.TB, req *operatorv1.InferenceDispatchRequest, parts []*operatorv1.InferenceResponsePart) *operatorv1.InferenceResult {
	t.Helper()
	result := &operatorv1.InferenceResult{
		Model:                req.GetModel(),
		ProviderAttemptId:    req.GetProviderAttemptId(),
		RequestedModel:       req.GetModel(),
		RequestedModelDigest: req.GetModelDigest(),
		ServedModelDigest:    req.GetModelDigest(),
		CampaignId:           req.GetCampaignId(),
		RunId:                req.GetRunId(),
		AssignmentId:         req.GetAssignmentId(),
		EvaluationAttemptId:  req.GetEvaluationAttemptId(),
		ScenarioId:           req.GetScenarioId(),
		ModelRegistryDigest:  req.GetModelRegistryDigest(),
		FinishReason:         "stop",
		Parts:                parts,
		LoadState:            operatorv1.InferenceLoadState_INFERENCE_LOAD_STATE_UNSPECIFIED,
		RetryClassification:  operatorv1.InferenceRetryClassification_INFERENCE_RETRY_CLASSIFICATION_UNSPECIFIED,
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		t.Fatalf("compute inference result digest: %v", err)
	}
	result.ResultDigest = digest
	return result
}

func (s Signer) newReceipt(t testing.TB, opts ReceiptOpts, status operatorv1.ExecutionStatus) *operatorv1.ActionReceipt {
	t.Helper()
	if opts.TransactionID == "" || opts.OperatorID == "" || opts.ActionType == "" {
		t.Fatalf("receipt opts need transaction, operator and action type")
	}
	return &operatorv1.ActionReceipt{
		TransactionId:    opts.TransactionID,
		TransactionHash:  transactionHash(opts.TransactionID),
		Status:           status,
		SignerKeyId:      s.KeyID,
		ExecutedAtUnixMs: executedAtUnixMs,
		ActionType:       opts.ActionType,
		ResultSummary:    opts.ResultSummary,
		L2Status:         opts.L2,
		L3Status:         opts.L3,
	}
}

// executedStages returns the seven-stage chain of an executed receipt, in the
// order governance.ValidateDeterministicProtocolChain requires. l5 is the
// outcome of the L5 execution stage: COMPLETED for an executed receipt, FAILED
// for an execution failure.
func executedStages(opts ReceiptOpts, signerKeyID, prior, hash string, l5 operatorv1.DeterministicStageOutcome) []*operatorv1.DeterministicStageEvidence {
	l5Stage := stage(opts, "L5", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION, l5, "")
	l5Stage.StateRootBefore = rootBefore
	l5Stage.StateRootAfter = rootAfter
	commit := stage(opts, "COMMIT", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, "L5")
	commit.SignerKeyId = signerKeyID
	commit.CommitmentHash = hash
	commit.PriorCommitmentHash = prior
	return []*operatorv1.DeterministicStageEvidence{
		stage(opts, "L1", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, "L4"),
		stage(opts, "L2", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2,
			l2Outcome(opts.L2), "L4"),
		stage(opts, "L3", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY,
			l3Outcome(opts.L3), "L4"),
		stage(opts, "L4", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED, "L5"),
		stage(opts, "PERSIST", operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED, "L5"),
		commit,
		l5Stage,
	}
}

// stage builds one stage whose id is "<transaction>:<suffix>". parent names the
// suffix of the parent stage, or is empty for a root.
func stage(opts ReceiptOpts, suffix string, kind operatorv1.DeterministicStageKind, outcome operatorv1.DeterministicStageOutcome, parent string) *operatorv1.DeterministicStageEvidence {
	evidence := &operatorv1.DeterministicStageEvidence{
		StageId:           stageID(opts.TransactionID, suffix),
		Kind:              kind,
		Outcome:           outcome,
		TransactionId:     opts.TransactionID,
		TransactionHash:   transactionHash(opts.TransactionID),
		ActionType:        opts.ActionType,
		OperatorId:        opts.OperatorID,
		OperatorSessionId: opts.SessionID,
	}
	if parent != "" {
		evidence.ParentStageId = stageID(opts.TransactionID, parent)
	}
	return evidence
}

func stageID(transactionID, suffix string) string {
	return transactionID + ":" + suffix
}

func transactionHash(transactionID string) string {
	return "hash-" + transactionID
}

func l2Outcome(status operatorv1.L2Status) operatorv1.DeterministicStageOutcome {
	switch status {
	case operatorv1.L2Status_L2_STATUS_NOT_REQUIRED:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED
	case operatorv1.L2Status_L2_STATUS_REQUIRED_VALID:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED
	case operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
	default:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_UNSPECIFIED
	}
}

func l3Outcome(status operatorv1.L3Status) operatorv1.DeterministicStageOutcome {
	switch status {
	case operatorv1.L3Status_L3_STATUS_NOT_REQUIRED:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED
	case operatorv1.L3Status_L3_STATUS_REQUIRED_VALID:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED
	case operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
	default:
		return operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_UNSPECIFIED
	}
}

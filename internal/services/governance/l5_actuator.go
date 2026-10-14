// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/scrubbing"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

//go:generate mockery --name ExecutionHandler --output ./mocks --dir .

// ExecutionHandler is the interface for executing verified transactions.
// This avoids import cycles between governance and pubsub packages.
type ExecutionHandler interface {
	ExecuteVerifiedTransaction(ctx context.Context, eventType constants.EventType, cmdMsg CommandMessage) (string, error)
}

// ReceiptPublisher publishes a signed ActionReceipt (wrapped in a
// GovernanceEnvelope carrying the original command's identity fields) to the
// gateway's receipts: channel after execution. The gateway intercepts the
// publish, verifies the receipt signature against the operator's actuator
// public key, records the receipt in its SQLAuditStore, and fans it out to
// subscribers. Implemented by pubsub.PubSubResultsService on the operator
// side; nil on the gateway in-process operator path (in-process receipts are
// already recorded in the gateway's SQLAuditStore directly by L5Actuator).
// Publish errors are best-effort: the receipt is already recorded locally
// and the result already published, so the gateway-side mirror is not a gate.
type ReceiptPublisher interface {
	PublishActionReceipt(ctx context.Context, env *govtypes.GovernanceEnvelope, receipt *operatorv1.ActionReceipt) error
}

// CommandMessage is the typed command message passed through the L5 execution
// boundary. It supports sovereignty-preserving payload rehydration via
// GetPayload/SetPayload. Implemented by pubsub.PubSubCommandMessage.
type CommandMessage interface {
	GetPayload() []byte
	SetPayload([]byte)

	// InferenceResultDigest is the canonical InferenceResult digest the INFERENCE handler
	// produced for this message, or "" when none was produced. It is the typed channel for
	// the one result identifier the Gateway checks against the signed receipt; the L5
	// actuator reads it only for INFERENCE actions.
	InferenceResultDigest() string
}

// L5Actuator is the execution gateway. It is the final stop for all GovernanceEnvelope envelopes.
//
// Defense-in-depth note: L5Actuator does NOT re-verify L2 or L3 proofs. By design,
// L4Warden performs all pre-dispatch verification (L1 doctrine, L2 consensus, L3 notary)
// and embeds the results in VerifiedTransaction. L5 trusts that VerifiedTransaction,
// records the L2/L3 status in the ActionReceipt for audit, and focuses on execution
// safety: fail-closed receipt signing, JIT capability minting, and audit logging.
// The separation between L4 (verification) and L5 (execution) is the defense-in-depth
// boundary — two independent components with distinct responsibilities.
type L5Actuator struct {
	Logger            *slog.Logger
	SQLAuditStore     *storage.SQLAuditStore
	StateRootProvider StateRootProvider
	ExecutionHandler  ExecutionHandler
	Scrubbing         *scrubbing.ScrubbingService

	// ReceiptPublisher publishes the signed ActionReceipt to the gateway's
	// receipts: channel after execution so the gateway can record it in its
	// SQLAuditStore. Nil on the gateway in-process operator path (receipts
	// are already recorded locally). Best-effort: publish errors are logged
	// and do not fail the execution.
	ReceiptPublisher ReceiptPublisher

	// Identity is the Operator and audit session this runtime owns. A rejected
	// envelope is recorded under this session, never under the session id the
	// envelope claims, and an envelope that names no session executes under it.
	// It is the runtime's ExecutionTarget, which the pubsub constructor requires.
	Identity RuntimeIdentityProvider

	// L5Actuator's own signing identity for ActionReceipts
	SigningKey        ed25519.PrivateKey
	KeyID             string
	AuditorSigningKey ed25519.PrivateKey
	AuditorKeyID      string

	wg sync.WaitGroup
}

// Execute is the single execution boundary for all verified transactions.
// It dispatches to the registered handler, captures status, signs and persists an ActionReceipt, and returns it.
//
// Fail-closed: if receipt signing or initial audit logging fails, the handler is NOT executed.
func (w *L5Actuator) Execute(ctx context.Context, vt *VerifiedTransaction, cmdMsg CommandMessage) (*operatorv1.ActionReceipt, error) {
	w.wg.Add(1)
	defer w.wg.Done()

	if w.ExecutionHandler == nil {
		return nil, constants.ErrL5ActuatorExecutionHandlerNotSet
	}
	if len(w.SigningKey) == 0 {
		return nil, constants.ErrL5ActuatorSigningKeyMissing
	}
	if err := w.requireAuditStore(); err != nil {
		return nil, err
	}

	eventType := constants.EventType(vt.Envelope.EventType)
	if eventType == "" {
		return nil, constants.ErrTxUnknownEventType
	}

	w.Logger.Info("L5Actuator preparing to execute transaction",
		"message_id", vt.Envelope.Id,
		"action_type", vt.ActionType,
		"event_type", eventType)

	// Refuse before any receipt is signed or the handler runs when the target cannot be scrubbed.
	signed, err := w.signedView(vt)
	if err != nil {
		return nil, err
	}

	// L4 verified a claimed session equals the runtime's own, and an envelope
	// that claims none executes under the runtime's own. Resolve it before any
	// receipt is signed so an unresolvable session refuses before the handler runs.
	audit, err := w.acceptedAuditIdentity(ctx, vt.Envelope)
	if err != nil {
		return nil, err
	}

	receipt := w.buildInitialReceipt(ctx, vt)

	// The receipt and its commitment are persisted by one write, so both stages
	// share its start.
	receiptPersistenceStart := governanceMonotonicNow()
	attestation, err := w.signAndLogReceipt(signed, audit, receipt)
	if err != nil {
		return nil, err
	}
	receiptPersistenceStage := newDeterministicStageEvidence(
		vt.Envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE,
		receiptPersistenceStart,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
	)
	receiptPersistenceStage.SignerKeyId = receipt.SignerKeyId
	receiptPersistenceStage.ReceiptSignatureDigest = SignatureDigest([]string{receipt.Signature})
	receiptPersistenceStage.AuditRecordId = receipt.TransactionId
	receipt.DeterministicStageEvidence = append(receipt.DeterministicStageEvidence, receiptPersistenceStage)
	if attestation != nil {
		commitmentStage := newDeterministicStageEvidence(
			vt.Envelope,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND,
			receiptPersistenceStart,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
		)
		commitmentStage.SignerKeyId = attestation.AuditorKeyId
		commitmentStage.CommitmentHash = attestation.Hash
		commitmentStage.PriorCommitmentHash = attestation.PriorCommitmentHash
		commitmentStage.L2SignatureDigest = attestation.L2SignatureDigest
		commitmentStage.L3SignatureDigest = attestation.HumanSignatureDigest
		receipt.DeterministicStageEvidence = append(receipt.DeterministicStageEvidence, commitmentStage)
	}

	if err := w.rehydratePayload(ctx, vt, cmdMsg); err != nil {
		return nil, err
	}

	cap, err := MintCapability(vt, w.SigningKey, w.KeyID)
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to mint execution capability", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorCapabilityMint, err)
	}
	w.Logger.Info("Minted JIT capability",
		"message_id", vt.Envelope.Id,
		"action_type", vt.ActionType,
		"expires_at", cap.ExpiresAt.UTC().Format(time.RFC3339))

	execCtx := ContextWithCapability(ctx, cap)

	executionStart := governanceMonotonicNow()
	summary, execErr := w.ExecutionHandler.ExecuteVerifiedTransaction(execCtx, eventType, cmdMsg)
	executionOutcome := operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED
	if execErr != nil {
		executionOutcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
	}
	executionStage := newDeterministicStageEvidence(
		vt.Envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION,
		executionStart,
		executionOutcome,
	)
	executionStage.StateRootBefore = receipt.StateRootBefore
	for _, stage := range receipt.DeterministicStageEvidence {
		if stage == nil {
			continue
		}
		switch stage.Kind {
		case operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_RECEIPT_PERSISTENCE,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND:
			stage.ParentStageId = executionStage.StageId
		}
	}
	receipt.DeterministicStageEvidence = append(receipt.DeterministicStageEvidence, executionStage)

	cap.Dissolve()
	w.Logger.Info("Dissolved JIT capability", "message_id", vt.Envelope.Id, "action_type", vt.ActionType)

	w.finalizeReceipt(ctx, receipt, summary, w.inferenceResultDigest(vt, cmdMsg), execErr)
	executionStage.StateRootAfter = receipt.StateRootAfter

	if err := w.signAndLogFinalReceipt(context.WithoutCancel(ctx), signed, audit, receipt); err != nil {
		return receipt, err
	}

	// Publish the signed ActionReceipt to the gateway's receipts: channel
	// so the gateway can record it in its SQLAuditStore. Best-effort: the
	// receipt is already recorded locally and the result already published;
	// the gateway-side mirror is not a gate. Only attempted when a
	// ReceiptPublisher is wired (outbound operator path).
	if w.ReceiptPublisher != nil {
		if pubErr := w.ReceiptPublisher.PublishActionReceipt(ctx, signed.Envelope, receipt); pubErr != nil {
			w.Logger.Warn("Failed to publish ActionReceipt to gateway receipts channel",
				string(constants.ConnectionStateError), pubErr,
				"message_id", vt.Envelope.Id)
		}
	}

	return receipt, execErr
}

func (w *L5Actuator) RecordRejectedTransaction(ctx context.Context, vt *VerifiedTransaction, rejection error) (*operatorv1.ActionReceipt, error) {
	if vt == nil || vt.Envelope == nil {
		return nil, constants.ErrTxInvalidEnvelope
	}
	if rejection == nil {
		return nil, constants.ErrTxInvalidEnvelope
	}
	if len(w.SigningKey) == 0 {
		return nil, constants.ErrL5ActuatorSigningKeyMissing
	}
	if err := w.requireAuditStore(); err != nil {
		return nil, err
	}

	signed, err := w.signedView(vt)
	if err != nil {
		return nil, err
	}
	// The rejected envelope's operator and session ids are unverified claims. The
	// signed receipt keeps them as claimed values (stage evidence), but the audit
	// row belongs to the session this runtime owns.
	identity, err := w.runtimeIdentity(ctx)
	if err != nil {
		return nil, err
	}
	receipt := w.buildInitialReceipt(ctx, vt)
	receipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	receipt.ResultSummary = w.failedReceiptSummary("rejected", operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, rejection)
	receipt.FailureCode = operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
	receipt.RejectionCode = ClassifyGovernanceRejection(rejection)
	receipt.ExecutedAtUnixMs = time.Now().UnixMilli()
	if err := w.signAndLogRejectionReceipt(context.WithoutCancel(ctx), signed, identity, receipt); err != nil {
		return nil, err
	}
	if w.ReceiptPublisher != nil {
		// The Gateway accepts a receipt only on the publishing runtime's own
		// receipts channel, so the publication names the runtime's identity.
		if err := w.ReceiptPublisher.PublishActionReceipt(ctx, publicationEnvelope(signed.Envelope, identity), receipt); err != nil {
			w.Logger.Warn("Failed to publish rejected ActionReceipt to gateway receipts channel",
				string(constants.ConnectionStateError), err,
				"message_id", vt.Envelope.Id)
		}
	}
	return receipt, nil
}

// buildInitialReceipt constructs the EXECUTING-status receipt with state root and L2/L3 status.
// Receipt evidence does not depend on the caller staying connected, so the
// state root read ignores ctx cancellation, as the receipt writes do.
func (w *L5Actuator) buildInitialReceipt(ctx context.Context, vt *VerifiedTransaction) *operatorv1.ActionReceipt {
	stateBefore := ""
	if w.StateRootProvider != nil {
		var err error
		stateBefore, err = w.StateRootProvider.GetCurrentStateRoot(context.WithoutCancel(ctx))
		if err != nil {
			w.Logger.Warn("Failed to get state root before execution", string(constants.ConnectionStateError), err)
		}
	}

	l2Status := operatorv1.L2Status_L2_STATUS_NOT_REQUIRED
	if vt.Posture != nil && vt.Posture.RequiresL2Signature() {
		if vt.L2Valid {
			l2Status = operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
		} else {
			l2Status = operatorv1.L2Status_L2_STATUS_REQUIRED_FAILED
		}
	}

	l3Status := operatorv1.L3Status_L3_STATUS_NOT_REQUIRED
	if vt.Posture != nil && vt.Posture.RequiresL3Proof() {
		if vt.L3Valid {
			l3Status = operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
		} else {
			l3Status = operatorv1.L3Status_L3_STATUS_REQUIRED_FAILED
		}
	}

	stages := make([]*operatorv1.DeterministicStageEvidence, len(vt.DeterministicStageEvidence))
	for index, stage := range vt.DeterministicStageEvidence {
		if stage == nil {
			continue
		}
		stages[index] = &operatorv1.DeterministicStageEvidence{}
		proto.Merge(stages[index], stage)
	}

	return &operatorv1.ActionReceipt{
		TransactionId:              vt.Envelope.Id,
		TransactionHash:            vt.Envelope.TransactionHash,
		EventType:                  vt.Envelope.EventType,
		ActionType:                 vt.Envelope.ActionType,
		Status:                     operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
		ResultSummary:              "executing",
		StateRootBefore:            stateBefore,
		ExecutedAtUnixMs:           time.Now().UnixMilli(),
		SignerKeyId:                w.KeyID,
		L2Status:                   l2Status,
		L3Status:                   l3Status,
		DeterministicStageEvidence: stages,
	}
}

// signAndLogReceipt signs the initial receipt and persists it. With an audit
// store, the receipt and its commitment attestation commit in one transaction
// and the attestation is returned. Fail-closed: returns error if any step fails.
func (w *L5Actuator) signAndLogReceipt(vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, receipt *operatorv1.ActionReceipt) (*operatorv1.CommitmentAttestation, error) {
	sig, err := w.signReceipt(receipt)
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to sign initial action receipt", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorSignReceipt, err)
	}
	receipt.Signature = sig

	if err := w.verifyAuditorKey(); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorCommitmentPersist, err)
	}
	var attestation *operatorv1.CommitmentAttestation
	err = w.SQLAuditStore.RecordActionReceiptWithCommitment(BuildReceiptRecord(vt.Envelope, audit, receipt), func(priorHash string) (*operatorv1.CommitmentAttestation, error) {
		built, err := w.buildCommitment(vt, receipt, priorHash)
		if err != nil {
			return nil, err
		}
		attestation = built
		return built, nil
	})
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to persist initial action receipt and commitment", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorCommitmentPersist, err)
	}
	return attestation, nil
}

func CanonicalizeCommitmentAttestation(attestation *operatorv1.CommitmentAttestation) ([]byte, error) {
	if attestation == nil {
		return nil, constants.ErrTxInvalidEnvelope
	}
	var payload bytes.Buffer
	for _, value := range []string{
		attestation.TransactionId,
		attestation.TransactionHash,
		attestation.PriorCommitmentHash,
		attestation.StateRootAtCommit,
		attestation.L2SignatureDigest,
		attestation.WardenIntentSignatureDigest,
		attestation.HumanSignatureDigest,
		attestation.ActionType,
		attestation.TargetResource,
	} {
		writeCanonicalString(&payload, value)
	}
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], uint64(attestation.CommittedAtUnixMs))
	payload.Write(timestamp[:])
	writeCanonicalString(&payload, attestation.AuditorKeyId)
	return payload.Bytes(), nil
}

func writeCanonicalString(payload *bytes.Buffer, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	payload.Write(length[:])
	payload.WriteString(value)
}

func (w *L5Actuator) verifyAuditorKey() error {
	if len(w.AuditorSigningKey) != ed25519.PrivateKeySize {
		return constants.ErrL5ActuatorAuditorKeyMissing
	}
	publicKey := w.AuditorSigningKey.Public().(ed25519.PublicKey)
	if w.AuditorKeyID != hex.EncodeToString(publicKey) {
		return constants.ErrValidationFailed
	}
	return nil
}

// buildCommitment signs the receipt's commitment attestation over the given
// chain head. Storage owns its canonical JSON encoding.
func (w *L5Actuator) buildCommitment(vt *VerifiedTransaction, receipt *operatorv1.ActionReceipt, priorHash string) (*operatorv1.CommitmentAttestation, error) {
	attestation := &operatorv1.CommitmentAttestation{
		TransactionId:               vt.Envelope.Id,
		TransactionHash:             vt.Envelope.TransactionHash,
		PriorCommitmentHash:         priorHash,
		StateRootAtCommit:           receipt.StateRootBefore,
		L2SignatureDigest:           l2SignatureDigest(vt.Envelope),
		WardenIntentSignatureDigest: SignatureDigest([]string{receipt.Signature}),
		HumanSignatureDigest:        humanSignatureDigest(vt.Envelope),
		ActionType:                  string(vt.ActionType),
		TargetResource:              vt.Envelope.TargetResource,
		CommittedAtUnixMs:           time.Now().UnixMilli(),
		AuditorKeyId:                w.AuditorKeyID,
	}
	canonical, err := CanonicalizeCommitmentAttestation(attestation)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(canonical)
	attestation.Hash = hex.EncodeToString(hash[:])
	attestation.Signature = hex.EncodeToString(ed25519.Sign(w.AuditorSigningKey, canonical))
	return attestation, nil
}

func l2SignatureDigest(env *govtypes.GovernanceEnvelope) string {
	if env == nil || env.Governance == nil || env.Governance.L2 == nil {
		return ""
	}
	signatures := make([]string, 0, len(env.Governance.L2.Votes))
	for _, vote := range env.Governance.L2.Votes {
		if vote != nil {
			signatures = append(signatures, vote.ConsensusSignature)
		}
	}
	return SignatureDigest(signatures)
}

func humanSignatureDigest(env *govtypes.GovernanceEnvelope) string {
	if env == nil || env.Governance == nil || env.Governance.L3 == nil || env.Governance.L3.Proof == nil {
		return ""
	}
	proof := env.Governance.L3.Proof
	return SignatureDigest([]string{proof.Signature, proof.CliSignature})
}

func SignatureDigest(signatures []string) string {
	values := make([]string, 0, len(signatures))
	for _, signature := range signatures {
		if signature != "" {
			values = append(values, signature)
		}
	}
	if len(values) == 0 {
		return ""
	}
	sort.Strings(values)
	var payload bytes.Buffer
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(values)))
	payload.Write(count[:])
	for _, value := range values {
		writeCanonicalString(&payload, value)
	}
	digest := sha256.Sum256(payload.Bytes())
	return hex.EncodeToString(digest[:])
}

// rehydratePayload rehydrates the command message payload in place. Fail-closed: returns error on rehydration failure.
func (w *L5Actuator) rehydratePayload(ctx context.Context, vt *VerifiedTransaction, cmdMsg CommandMessage) error {
	if w.Scrubbing == nil || cmdMsg == nil {
		return nil
	}
	p := cmdMsg.GetPayload()
	if len(p) == 0 {
		return nil
	}
	rehydrated, err := w.Scrubbing.RehydratePayload(ctx, p)
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to rehydrate payload", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorRehydrate, err)
	}
	cmdMsg.SetPayload(rehydrated)
	return nil
}

// finalizeReceipt updates the receipt with execution result, state root after, and timestamp.
// The mutation has already run, so the state root read ignores ctx cancellation.
func (w *L5Actuator) finalizeReceipt(ctx context.Context, receipt *operatorv1.ActionReceipt, summary, inferenceDigest string, execErr error) {
	status := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	if execErr != nil {
		status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		receipt.ResultSummary = w.failedReceiptSummary("failed", classifyReceiptFailure(execErr), execErr)
	} else {
		receipt.ResultSummary = w.completedReceiptSummary(summary, inferenceDigest)
	}

	stateAfter := ""
	if w.StateRootProvider != nil {
		var stateErr error
		stateAfter, stateErr = w.StateRootProvider.GetCurrentStateRoot(context.WithoutCancel(ctx))
		if stateErr != nil {
			w.Logger.Warn("Failed to get state root after execution", string(constants.ConnectionStateError), stateErr)
		}
	}

	receipt.Status = status
	receipt.FailureCode = classifyReceiptFailure(execErr)
	receipt.StateRootAfter = stateAfter
	receipt.ExecutedAtUnixMs = time.Now().UnixMilli()
}

// scrubberActive reports whether the actuator has an enabled scrubber. Free text and the target
// resource leave the runtime only through it.
func (w *L5Actuator) scrubberActive() bool {
	return w.Scrubbing != nil && w.Scrubbing.IsEnabled()
}

// inferenceResultDigest returns the typed result digest the INFERENCE handler recorded on the
// command message. It is empty for every other action type and for a nil message, so no other
// action can supply a pass-through value.
func (w *L5Actuator) inferenceResultDigest(vt *VerifiedTransaction, cmdMsg CommandMessage) string {
	if vt == nil || vt.ActionType != constants.ActionTypeInference || cmdMsg == nil {
		return ""
	}
	return cmdMsg.InferenceResultDigest()
}

// completedReceiptSummary returns the signed ResultSummary of a completed execution. The
// inference result digest is an identifier that the Gateway checks against the receipt. It
// arrives on its own typed channel (inferenceDigest, set only for INFERENCE actions) and is
// signed as is. Every other handler text is content: it is scrubbed when a scrubber is active
// and replaced by fixed text otherwise. The text is never inspected for a digest shape, so a
// hex-encoded secret returned by a tool is scrubbed like any other output.
func (w *L5Actuator) completedReceiptSummary(summary, inferenceDigest string) string {
	switch {
	case inferenceDigest != "":
		return inferenceDigest
	case summary == "":
		return "completed"
	case !w.scrubberActive():
		return "completed"
	default:
		return w.Scrubbing.ScrubText(summary)
	}
}

// failedReceiptSummary returns the signed ResultSummary of a failed execution or a rejection. The
// cause text is scrubbed when a scrubber is active. Without one, only the typed failure code is signed.
func (w *L5Actuator) failedReceiptSummary(verb string, code operatorv1.ReceiptFailureCode, cause error) string {
	if cause == nil || !w.scrubberActive() {
		return verb + ": " + code.String()
	}
	return verb + ": " + w.Scrubbing.ScrubText(cause.Error())
}

// signedView returns the transaction as it is written into signed receipts, commitment attestations
// and receipt publications. Only the target resource differs: it is scrubbed on a copy of the
// envelope before anything is signed. The capability and the execution handler keep the real target.
// It fails closed: a target with no active scrubber is refused before any receipt is signed or the
// handler runs.
func (w *L5Actuator) signedView(vt *VerifiedTransaction) (*VerifiedTransaction, error) {
	target := vt.Envelope.GetTargetResource()
	if target == "" {
		return vt, nil
	}
	if !w.scrubberActive() {
		return nil, fmt.Errorf("l5 actuator: scrub target resource: %w", constants.ErrEgressScrubberUnavailable)
	}
	env := proto.Clone(vt.Envelope).(*govtypes.GovernanceEnvelope)
	env.TargetResource = w.Scrubbing.ScrubText(target)
	view := *vt
	view.Envelope = env
	return &view, nil
}

// classifyReceiptFailure maps an execution error to the typed
// ReceiptFailureCode stamped on the FAILED receipt. The code is bound into
// the receipt signature so the User Gateway can map it back to a typed
// sentinel without parsing the result_summary text.
func classifyReceiptFailure(execErr error) operatorv1.ReceiptFailureCode {
	if execErr == nil {
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_UNSPECIFIED
	}
	switch {
	case errors.Is(execErr, constants.ErrInferenceModelOverrideDenied):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED
	case errors.Is(execErr, constants.ErrInferenceRoleInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_ROLE_INVALID
	case errors.Is(execErr, constants.ErrInferenceModelRefInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REF_INVALID
	case errors.Is(execErr, constants.ErrInferenceBackendUnavailable),
		errors.Is(execErr, constants.ErrInferenceBackendNotRegistered):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
	case errors.Is(execErr, constants.ErrInferenceBackendTimeout):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT
	case errors.Is(execErr, constants.ErrInferenceGenerateFailed):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATE_FAILED
	case errors.Is(execErr, constants.ErrInferenceModelNotFound):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND
	case errors.Is(execErr, constants.ErrInferenceProviderResponseInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID
	case errors.Is(execErr, constants.ErrInferenceGenerationOptionsInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID
	case errors.Is(execErr, constants.ErrInferenceCapabilityUnsupported):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED
	case errors.Is(execErr, constants.ErrInferenceToolsUnsupported):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED
	case errors.Is(execErr, constants.ErrInferenceContextOverflow):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW
	case errors.Is(execErr, constants.ErrInferenceProviderAttemptRequired):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED
	case errors.Is(execErr, constants.ErrInferenceIdentityMismatch):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH
	case errors.Is(execErr, constants.ErrInferenceModelDigestMismatch):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH
	case errors.Is(execErr, constants.ErrInferenceEvidenceHashInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID
	case errors.Is(execErr, constants.ErrInferenceModelRegistryInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID
	case errors.Is(execErr, constants.ErrInferenceCampaignBindingInvalid):
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID
	default:
		return operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
	}
}

// signFinalReceipt signs the final receipt and its persistence attestation. The
// attestation stays on the receipt until the caller's write succeeds, so an
// attestation always describes a stored receipt.
func (w *L5Actuator) signFinalReceipt(receipt *operatorv1.ActionReceipt, messageID string) error {
	finalSig, err := w.signReceipt(receipt)
	if err != nil {
		w.Logger.Error("Failed to sign final action receipt", string(constants.ConnectionStateError), err, "message_id", messageID)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorSignReceipt, err)
	}
	receipt.Signature = finalSig

	attestation, err := w.signReceiptPersistenceAttestation(receipt)
	if err != nil {
		return err
	}
	receipt.FinalPersistenceAttestation = attestation
	return nil
}

// signAndLogFinalReceipt signs the final receipt of an executed transaction and
// persists it with its attestation in one write. Best-effort: returns error but
// the receipt is still returned by caller, because the mutation already ran.
func (w *L5Actuator) signAndLogFinalReceipt(ctx context.Context, vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, receipt *operatorv1.ActionReceipt) error {
	if err := w.signFinalReceipt(receipt, vt.Envelope.Id); err != nil {
		return err
	}
	if logErr := w.LogReceipt(ctx, vt.Envelope, audit, receipt); logErr != nil {
		receipt.FinalPersistenceAttestation = nil
		w.Logger.Error("Failed to log final action receipt - mutation already executed", string(constants.ConnectionStateError), logErr, "message_id", vt.Envelope.Id)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorLogReceipt, logErr)
	}
	return nil
}

// signAndLogRejectionReceipt signs and persists the receipt of a rejected
// transaction. Nothing executed, so a persistence failure means the rejection
// evidence was lost, not that a mutation went unrecorded.
func (w *L5Actuator) signAndLogRejectionReceipt(ctx context.Context, vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, receipt *operatorv1.ActionReceipt) error {
	if err := w.signFinalReceipt(receipt, vt.Envelope.Id); err != nil {
		return err
	}
	if logErr := w.LogReceipt(ctx, vt.Envelope, audit, receipt); logErr != nil {
		receipt.FinalPersistenceAttestation = nil
		w.Logger.Error("Failed to persist rejection receipt - rejection evidence lost, nothing was executed", string(constants.ConnectionStateError), logErr, "message_id", vt.Envelope.Id)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorRejectionReceiptPersist, logErr)
	}
	return nil
}

// runtimeIdentity resolves the identity this runtime owns. It fails closed when
// no identity source is wired or the runtime has no session yet.
func (w *L5Actuator) runtimeIdentity(ctx context.Context) (govtypes.RuntimeIdentity, error) {
	if w.Identity == nil {
		return govtypes.RuntimeIdentity{}, constants.ErrL5ActuatorRuntimeIdentityMissing
	}
	identity, err := w.Identity.RuntimeIdentity(ctx)
	if err != nil {
		return govtypes.RuntimeIdentity{}, fmt.Errorf("%w: %w", constants.ErrL5ActuatorRuntimeIdentityMissing, err)
	}
	if identity.OperatorID == "" || identity.SessionID == "" {
		return govtypes.RuntimeIdentity{}, constants.ErrL5ActuatorRuntimeIdentityMissing
	}
	return identity, nil
}

// acceptedAuditIdentity returns the identity a verified transaction is recorded
// under: the operator and session its envelope names, which L4 verified are the
// runtime's own, or the runtime's own identity when the envelope names no session.
func (w *L5Actuator) acceptedAuditIdentity(ctx context.Context, env *govtypes.GovernanceEnvelope) (govtypes.RuntimeIdentity, error) {
	if env.OperatorSessionId != "" {
		return govtypes.RuntimeIdentity{OperatorID: env.OperatorId, SessionID: env.OperatorSessionId}, nil
	}
	return w.runtimeIdentity(ctx)
}

// publicationEnvelope returns the envelope a rejection receipt is published
// with: the identity fields name the publishing runtime, not the envelope's claim.
func publicationEnvelope(env *govtypes.GovernanceEnvelope, identity govtypes.RuntimeIdentity) *govtypes.GovernanceEnvelope {
	published := proto.Clone(env).(*govtypes.GovernanceEnvelope)
	published.OperatorId = identity.OperatorID
	published.OperatorSessionId = identity.SessionID
	return published
}

func CanonicalizeReceiptPersistenceAttestation(attestation *operatorv1.ReceiptPersistenceAttestation) ([]byte, error) {
	if attestation == nil {
		return nil, constants.ErrReceiptPersistenceAttestationMissing
	}
	var payload bytes.Buffer
	writeCanonicalString(&payload, attestation.TransactionId)
	writeCanonicalString(&payload, attestation.ReceiptSignatureDigest)
	var persistedAt [8]byte
	binary.BigEndian.PutUint64(persistedAt[:], uint64(attestation.PersistedAtUnixMs))
	payload.Write(persistedAt[:])
	writeCanonicalString(&payload, attestation.AuditRecordId)
	writeCanonicalString(&payload, attestation.SignerKeyId)
	return payload.Bytes(), nil
}

func (w *L5Actuator) signReceiptPersistenceAttestation(receipt *operatorv1.ActionReceipt) (*operatorv1.ReceiptPersistenceAttestation, error) {
	attestation := &operatorv1.ReceiptPersistenceAttestation{
		TransactionId:          receipt.TransactionId,
		ReceiptSignatureDigest: SignatureDigest([]string{receipt.Signature}),
		PersistedAtUnixMs:      time.Now().UnixMilli(),
		AuditRecordId:          receipt.TransactionId,
		SignerKeyId:            w.KeyID,
	}
	payload, err := CanonicalizeReceiptPersistenceAttestation(attestation)
	if err != nil {
		return nil, fmt.Errorf("%w: persistence attestation: %w", constants.ErrL5ActuatorCanonicalizeReceipt, err)
	}
	attestation.Signature = hex.EncodeToString(ed25519.Sign(w.SigningKey, payload))
	return attestation, nil
}

func SignerPublicKey(keyID string) (ed25519.PublicKey, error) {
	decoded, err := hex.DecodeString(keyID)
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, constants.ErrTrustedSignerKeyNotFound
	}
	return ed25519.PublicKey(decoded), nil
}

func DeterministicStageActionType(receipt *operatorv1.ActionReceipt) (string, error) {
	if receipt == nil {
		return "", fmt.Errorf("%w: action receipt is nil", constants.ErrEvidenceArtifactMalformed)
	}
	actionType := ""
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetActionType() == "" {
			continue
		}
		if actionType != "" && actionType != stage.GetActionType() {
			return "", fmt.Errorf("%w: deterministic stages contain conflicting action types", constants.ErrEvidenceArtifactMalformed)
		}
		actionType = stage.GetActionType()
	}
	if actionType == "" {
		return "", fmt.Errorf("%w: deterministic stages do not declare an action type", constants.ErrEvidenceArtifactMalformed)
	}
	return actionType, nil
}

func VerifyActionReceiptSignature(receipt *operatorv1.ActionReceipt, publicKey ed25519.PublicKey) error {
	if receipt == nil {
		return constants.ErrActionReceiptMissing
	}
	if receipt.Signature == "" || len(publicKey) != ed25519.PublicKeySize {
		return constants.ErrActionReceiptSignatureInvalid
	}
	signature, err := hex.DecodeString(receipt.Signature)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrActionReceiptSignatureInvalid, err)
	}
	payload, err := CanonicalizeActionReceipt(receipt)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrActionReceiptSignatureInvalid, err)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return constants.ErrActionReceiptSignatureInvalid
	}
	return nil
}

func VerifyReceiptPersistenceAttestation(receipt *operatorv1.ActionReceipt, publicKey ed25519.PublicKey) error {
	if receipt == nil || receipt.FinalPersistenceAttestation == nil {
		return constants.ErrReceiptPersistenceAttestationMissing
	}
	attestation := receipt.FinalPersistenceAttestation
	if receipt.TransactionId == "" ||
		attestation.TransactionId != receipt.TransactionId ||
		attestation.AuditRecordId != receipt.TransactionId ||
		receipt.SignerKeyId == "" ||
		attestation.SignerKeyId != receipt.SignerKeyId ||
		attestation.PersistedAtUnixMs <= 0 {
		return constants.ErrReceiptPersistenceAttestationInvalid
	}
	if receipt.Signature == "" || attestation.ReceiptSignatureDigest != SignatureDigest([]string{receipt.Signature}) {
		return constants.ErrReceiptPersistenceSignatureMismatch
	}
	signature, err := hex.DecodeString(attestation.Signature)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrReceiptPersistenceAttestationInvalid, err)
	}
	payload, err := CanonicalizeReceiptPersistenceAttestation(attestation)
	if err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, payload, signature) {
		return constants.ErrReceiptPersistenceAttestationInvalid
	}
	return nil
}

// VerifyActionReceipt verifies a receipt's signature and its final-persistence
// attestation against the executing runtime's actuator public key.
func VerifyActionReceipt(receipt *operatorv1.ActionReceipt, publicKey ed25519.PublicKey) error {
	if err := VerifyActionReceiptSignature(receipt, publicKey); err != nil {
		return err
	}
	return VerifyReceiptPersistenceAttestation(receipt, publicKey)
}

// canonicalReceipt is the typed representation for ActionReceipt canonicalization.
// This ensures strict typing and deterministic JSON marshaling for signing/verification.
type canonicalReceipt struct {
	TransactionID                  string `json:"transaction_id"`
	TransactionHash                string `json:"transaction_hash"`
	EventType                      string `json:"event_type,omitempty"`
	ActionType                     string `json:"action_type,omitempty"`
	Status                         int32  `json:"status"`
	ResultSummary                  string `json:"result_summary"`
	StateRootBefore                string `json:"state_root_before"`
	StateRootAfter                 string `json:"state_root_after"`
	ExecutedAtUnixMs               int64  `json:"executed_at_unix_ms"`
	SignerKeyID                    string `json:"signer_key_id"`
	L2Status                       int32  `json:"l2_status"`
	L3Status                       int32  `json:"l3_status"`
	DeterministicStageEvidenceHash string `json:"deterministic_stage_evidence_hash,omitempty"`
	FailureCode                    int32  `json:"failure_code,omitempty"`
	RejectionCode                  int32  `json:"rejection_code,omitempty"`
}

func deterministicStageEvidenceHash(stages []*operatorv1.DeterministicStageEvidence) (string, error) {
	if len(stages) == 0 {
		return "", nil
	}
	var payload bytes.Buffer
	for _, stage := range stages {
		encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(stage)
		if err != nil {
			return "", err
		}
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(encoded)))
		payload.Write(length[:])
		payload.Write(encoded)
	}
	digest := sha256.Sum256(payload.Bytes())
	return hex.EncodeToString(digest[:]), nil
}

// CanonicalizeActionReceipt produces a deterministic byte representation for signing/verification.
// This function must be used by both signing and verification to ensure consistency.
// Field order: transaction_id, transaction_hash, event_type, action_type (v2 receipts),
// status, result_summary, state_root_before, state_root_after, executed_at_unix_ms,
// signer_key_id, l2_status, l3_status, and the deterministic stage evidence hash when present.
func CanonicalizeActionReceipt(r *operatorv1.ActionReceipt) ([]byte, error) {
	stageEvidenceHash, err := deterministicStageEvidenceHash(r.DeterministicStageEvidence)
	if err != nil {
		return nil, fmt.Errorf("%w: deterministic stage evidence: %w", constants.ErrL5ActuatorCanonicalizeReceipt, err)
	}
	canonical := canonicalReceipt{
		TransactionID:                  r.TransactionId,
		TransactionHash:                r.TransactionHash,
		EventType:                      r.EventType,
		ActionType:                     r.ActionType,
		Status:                         int32(r.Status),
		ResultSummary:                  r.ResultSummary,
		StateRootBefore:                r.StateRootBefore,
		StateRootAfter:                 r.StateRootAfter,
		ExecutedAtUnixMs:               r.ExecutedAtUnixMs,
		SignerKeyID:                    r.SignerKeyId,
		L2Status:                       int32(r.L2Status),
		L3Status:                       int32(r.L3Status),
		DeterministicStageEvidenceHash: stageEvidenceHash,
		FailureCode:                    int32(r.FailureCode),
		RejectionCode:                  int32(r.RejectionCode),
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorMarshalReceipt, err)
	}
	return payload, nil
}

func (w *L5Actuator) signReceipt(r *operatorv1.ActionReceipt) (string, error) {
	if len(w.SigningKey) == 0 {
		return "", constants.ErrL5ActuatorSigningKeyMissing
	}

	// Use canonical serialization for signing - shared with verification
	payload, err := CanonicalizeActionReceipt(r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrL5ActuatorCanonicalizeReceipt, err)
	}

	sig := ed25519.Sign(w.SigningKey, payload)
	return hex.EncodeToString(sig), nil
}

// requireAuditStore fails closed unless the actuator owns an audit store able
// to persist receipts together with their commitment attestations. The
// SQLAuditStore is the single owner of receipt records; there is no
// receipt-less execution path.
func (w *L5Actuator) requireAuditStore() error {
	if w.SQLAuditStore == nil {
		return constants.ErrL5ActuatorAuditStoreMissing
	}
	if len(w.AuditorSigningKey) == 0 {
		return constants.ErrL5ActuatorAuditorKeyMissing
	}
	if w.AuditorKeyID == "" {
		return constants.ErrL5ActuatorAuditorKeyIDMissing
	}
	if w.SQLAuditStore.CommitmentLedger() == nil {
		return constants.ErrL5ActuatorCommitmentLedger
	}
	return nil
}

// LogReceipt records the signed action receipt in the audit store receipts table.
// The row is filed under audit, the operator and session the runtime owns.
func (w *L5Actuator) LogReceipt(_ context.Context, env *govtypes.GovernanceEnvelope, audit govtypes.RuntimeIdentity, r *operatorv1.ActionReceipt) error {
	if w.SQLAuditStore == nil {
		return constants.ErrL5ActuatorAuditStoreMissing
	}
	if env == nil {
		return constants.ErrTxInvalidEnvelope
	}
	if err := w.SQLAuditStore.RecordActionReceipt(BuildReceiptRecord(env, audit, r)); err != nil {
		if w.Logger != nil {
			w.Logger.Error("Failed to record ActionReceipt in audit store", string(constants.ConnectionStateError), err)
		}
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorAuditStore, err)
	}
	return nil
}

// buildReceiptRecord constructs an ActionReceiptRecord from a GovernanceEnvelope and ActionReceipt.
// This is the single source of truth for record construction, used by LogReceipt and the commitment-bound initial receipt write.
// audit is the operator and session the audit row is filed under. It is passed explicitly because the
// envelope's operator_id and operator_session_id are claims and are not the audit identity of a rejected
// transaction; the signed receipt keeps the claims in its stage evidence.
func BuildReceiptRecord(env *govtypes.GovernanceEnvelope, audit govtypes.RuntimeIdentity, r *operatorv1.ActionReceipt) *models.ActionReceiptRecord {
	eventType := constants.EventType(env.EventType)
	if eventType == "" && r.EventType != "" {
		eventType = constants.EventType(r.EventType)
	}

	return &models.ActionReceiptRecord{
		TransactionID:     r.TransactionId,
		TransactionHash:   r.TransactionHash,
		InvestigationID:   env.InvestigationId,
		OperatorID:        audit.OperatorID,
		OperatorSessionID: audit.SessionID,
		RequestorUserID:   env.RequestorUserId,
		ActingAppID:       env.ActingAppId,
		EventType:         eventType,
		ActionType:        constants.ActionType(env.ActionType),
		TargetResource:    env.TargetResource,
		Status:            r.Status,
		ResultSummary:     r.ResultSummary,
		StateRootBefore:   r.StateRootBefore,
		StateRootAfter:    r.StateRootAfter,
		ExecutedAt:        time.UnixMilli(r.ExecutedAtUnixMs),
		SignerKeyID:       r.SignerKeyId,
		Signature:         r.Signature,
		L2Valid:           r.L2Status == operatorv1.L2Status_L2_STATUS_REQUIRED_VALID,
		L3Valid:           r.L3Status == operatorv1.L3Status_L3_STATUS_REQUIRED_VALID,
		Timestamp:         time.Now().UTC(),
		ActionReceipt:     proto.Clone(r).(*operatorv1.ActionReceipt),
	}
}

// Wait blocks until all in-flight transactions have finished executing.
func (w *L5Actuator) Wait() {
	w.wg.Wait()
}

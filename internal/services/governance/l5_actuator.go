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

// EventReceiptPublisher publishes a signed EventReceipt (wrapped in a
// GovernanceEnvelope carrying the original command's identity fields) to the
// gateway's event_receipts: channel after execution. The gateway intercepts the
// publish, verifies the event receipt signature against the operator's actuator
// public key, records the event receipt in its SQLAuditStore, and fans it out to
// subscribers. Implemented by pubsub.PubSubResultsService on the operator
// side; nil on the gateway in-process operator path (in-process event receipts are
// already recorded in the gateway's SQLAuditStore directly by L5Actuator).
// Publish errors are best-effort: the event receipt is already recorded locally
// and the result already published, so the gateway-side mirror is not a gate.
type EventReceiptPublisher interface {
	PublishEventReceipt(ctx context.Context, env *govtypes.GovernanceEnvelope, eventReceipt *operatorv1.EventReceipt) error
}

// CommandMessage is the typed command message passed through the L5 execution
// boundary. It supports sovereignty-preserving payload rehydration via
// GetPayload/SetPayload. Implemented by pubsub.PubSubCommandMessage.
type CommandMessage interface {
	GetPayload() []byte
	SetPayload([]byte)

	// InferenceResultDigest is the canonical InferenceResult digest the INFERENCE handler
	// produced for this message, or "" when none was produced. It is the typed channel for
	// the one result identifier the Gateway checks against the signed event receipt; the L5
	// actuator reads it only for INFERENCE actions.
	InferenceResultDigest() string
}

// L5Actuator is the execution gateway. It is the final stop for all GovernanceEnvelope envelopes.
//
// Defense-in-depth note: L5Actuator does NOT re-verify L2 or L3 proofs. By design,
// L4Warden performs all pre-dispatch verification (L1 doctrine, L2 consensus, L3 notary)
// and embeds the results in VerifiedTransaction. L5 trusts that VerifiedTransaction,
// records the L2/L3 status in the EventReceipt for audit, and focuses on execution
// safety: fail-closed event receipt signing, JIT capability minting, and audit logging.
// The separation between L4 (verification) and L5 (execution) is the defense-in-depth
// boundary — two independent components with distinct responsibilities.
type L5Actuator struct {
	Logger            *slog.Logger
	SQLAuditStore     *storage.SQLAuditStore
	StateRootProvider StateRootProvider
	ExecutionHandler  ExecutionHandler
	Scrubbing         *scrubbing.ScrubbingService

	// EventReceiptPublisher publishes the signed EventReceipt to the gateway's
	// event_receipts: channel after execution so the gateway can record it in its
	// SQLAuditStore. Nil on the gateway in-process operator path (event_receipts
	// are already recorded locally). Best-effort: publish errors are logged
	// and do not fail the execution.
	EventReceiptPublisher EventReceiptPublisher

	// Identity is the Operator and audit session this runtime owns. A rejected
	// envelope is recorded under this session, never under the session id the
	// envelope claims, and an envelope that names no session executes under it.
	// It is the runtime's ExecutionTarget, which the pubsub constructor requires.
	Identity RuntimeIdentityProvider

	// L5Actuator's own signing identity for EventReceipts
	SigningKey        ed25519.PrivateKey
	KeyID             string
	AuditorSigningKey ed25519.PrivateKey
	AuditorKeyID      string

	wg sync.WaitGroup
}

// Execute is the single execution boundary for all verified transactions.
// It dispatches to the registered handler, captures status, signs and persists an EventReceipt, and returns it.
//
// Fail-closed: if event receipt signing or initial audit logging fails, the handler is NOT executed.
// If the payload cannot be rehydrated, the handler is NOT executed either, and the event_receipt
// finalizes FAILED with the rehydration error as its cause.
func (w *L5Actuator) Execute(ctx context.Context, vt *VerifiedTransaction, cmdMsg CommandMessage) (*operatorv1.EventReceipt, error) {
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

	// The Warden decides L2 and L3 before L5 runs. An undecided transaction is a
	// programming error: it is recorded as a FAILED event receipt and the handler never runs.
	if err := requireDecidedLayers(vt); err != nil {
		w.Logger.Error("Fail-closed: verified transaction has an undecided L2 or L3 status", "message_id", vt.Envelope.Id)
		rejected, recordErr := w.RecordRejectedTransaction(ctx, vt, err)
		if recordErr != nil {
			return nil, recordErr
		}
		return rejected, err
	}

	// Refuse before any event receipt is signed or the handler runs when the target cannot be scrubbed.
	signed, err := w.signedView(ctx, vt)
	if err != nil {
		return nil, err
	}

	// L4 verified a claimed session equals the runtime's own, and an envelope
	// that claims none executes under the runtime's own. Resolve it before any
	// event receipt is signed so an unresolvable session refuses before the handler runs.
	audit, err := w.acceptedAuditIdentity(ctx, vt.Envelope)
	if err != nil {
		return nil, err
	}

	eventReceipt := w.buildInitialEventReceipt(ctx, vt)

	// The event receipt and its commitment are persisted by one write, so both stages
	// share its start.
	eventReceiptPersistenceStart := governanceMonotonicNow()
	attestation, err := w.signAndLogEventReceipt(signed, audit, eventReceipt)
	if err != nil {
		return nil, err
	}
	eventReceiptPersistenceStage := newDeterministicStageEvidence(
		vt.Envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_EVENT_RECEIPT_PERSISTENCE,
		eventReceiptPersistenceStart,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
	)
	eventReceiptPersistenceStage.SignerKeyId = eventReceipt.SignerKeyId
	eventReceiptPersistenceStage.EventReceiptSignatureDigest = SignatureDigest([]string{eventReceipt.Signature})
	eventReceiptPersistenceStage.AuditRecordId = eventReceipt.TransactionId
	eventReceipt.DeterministicStageEvidence = append(eventReceipt.DeterministicStageEvidence, eventReceiptPersistenceStage)
	if attestation != nil {
		commitmentStage := newDeterministicStageEvidence(
			vt.Envelope,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND,
			eventReceiptPersistenceStart,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_COMPLETED,
		)
		commitmentStage.SignerKeyId = attestation.AuditorKeyId
		commitmentStage.CommitmentHash = attestation.Hash
		commitmentStage.PriorCommitmentHash = attestation.PriorCommitmentHash
		commitmentStage.L2SignatureDigest = attestation.L2SignatureDigest
		commitmentStage.L3SignatureDigest = attestation.HumanSignatureDigest
		eventReceipt.DeterministicStageEvidence = append(eventReceipt.DeterministicStageEvidence, commitmentStage)
	}

	// A payload that cannot be rehydrated is never dispatched. The failure is the
	// execution result: the handler does not run and the event receipt finalizes FAILED,
	// the same as a handler failure.
	executionStart := governanceMonotonicNow()
	var summary string
	var execErr error
	if rehydrateErr := w.rehydratePayload(ctx, vt, cmdMsg); rehydrateErr != nil {
		execErr = rehydrateErr
	} else {
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
		summary, execErr = w.ExecutionHandler.ExecuteVerifiedTransaction(execCtx, eventType, cmdMsg)
		cap.Dissolve()
		w.Logger.Info("Dissolved JIT capability", "message_id", vt.Envelope.Id, "action_type", vt.ActionType)
	}
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
	executionStage.StateRootBefore = eventReceipt.StateRootBefore
	for _, stage := range eventReceipt.DeterministicStageEvidence {
		if stage == nil {
			continue
		}
		switch stage.Kind {
		case operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_EVENT_RECEIPT_PERSISTENCE,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_COMMITMENT_APPEND:
			stage.ParentStageId = executionStage.StageId
		}
	}
	eventReceipt.DeterministicStageEvidence = append(eventReceipt.DeterministicStageEvidence, executionStage)

	w.finalizeEventReceipt(ctx, eventReceipt, summary, w.inferenceResultDigest(vt, cmdMsg), w.auditEventReceiptListSummary(vt, cmdMsg, summary), execErr)
	executionStage.StateRootAfter = eventReceipt.StateRootAfter

	if err := w.signAndLogFinalEventReceipt(context.WithoutCancel(ctx), signed, audit, eventReceipt); err != nil {
		return eventReceipt, err
	}

	// Publish the signed EventReceipt to the gateway's event_receipts: channel
	// so the gateway can record it in its SQLAuditStore. Best-effort: the
	// event receipt is already recorded locally and the result already published;
	// the gateway-side mirror is not a gate. Only attempted when a
	// EventReceiptPublisher is wired (outbound operator path).
	if w.EventReceiptPublisher != nil {
		if pubErr := w.EventReceiptPublisher.PublishEventReceipt(ctx, signed.Envelope, eventReceipt); pubErr != nil {
			w.Logger.Warn("Failed to publish EventReceipt to gateway event receipts channel",
				string(constants.ConnectionStateError), pubErr,
				"message_id", vt.Envelope.Id)
		}
	}

	return eventReceipt, execErr
}

func (w *L5Actuator) RecordRejectedTransaction(ctx context.Context, vt *VerifiedTransaction, rejection error) (*operatorv1.EventReceipt, error) {
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

	signed, err := w.signedView(ctx, vt)
	if err != nil {
		return nil, err
	}
	// The rejected envelope's operator and session ids are unverified claims. The
	// signed event receipt keeps them as claimed values (stage evidence), but the audit
	// row belongs to the session this runtime owns.
	identity, err := w.runtimeIdentity(ctx)
	if err != nil {
		return nil, err
	}
	eventReceipt := w.buildInitialEventReceipt(ctx, vt)
	eventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
	eventReceipt.ResultSummary = w.failedEventReceiptSummary(ctx, "rejected", operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED, rejection)
	eventReceipt.FailureCode = operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED
	eventReceipt.RejectionCode = ClassifyGovernanceRejection(rejection)
	eventReceipt.ExecutedAtUnixMs = time.Now().UnixMilli()
	if err := w.signAndLogRejectionEventReceipt(context.WithoutCancel(ctx), signed, identity, eventReceipt); err != nil {
		return nil, err
	}
	if w.EventReceiptPublisher != nil {
		// The Gateway accepts an event receipt only on the publishing runtime's own
		// event receipts channel, so the publication names the runtime's identity.
		if err := w.EventReceiptPublisher.PublishEventReceipt(ctx, publicationEnvelope(signed.Envelope, identity), eventReceipt); err != nil {
			w.Logger.Warn("Failed to publish rejected EventReceipt to gateway event receipts channel",
				string(constants.ConnectionStateError), err,
				"message_id", vt.Envelope.Id)
		}
	}
	return eventReceipt, nil
}

// requireDecidedLayers fails closed when the Warden left L2 or L3 undecided. Every
// VerifiedTransaction from VerifyEnvelope carries both statuses, so UNSPECIFIED
// is a programming error with no default.
func requireDecidedLayers(vt *VerifiedTransaction) error {
	if vt.L2Status == operatorv1.L2Status_L2_STATUS_UNSPECIFIED {
		return fmt.Errorf("L5Actuator: L2 status undecided: %w", constants.ErrInternal)
	}
	if vt.L3Status == operatorv1.L3Status_L3_STATUS_UNSPECIFIED {
		return fmt.Errorf("L5Actuator: L3 status undecided: %w", constants.ErrInternal)
	}
	return nil
}

// buildInitialEventReceipt constructs the EXECUTING-status event receipt with state root and L2/L3 status.
// The L2 and L3 status are the Warden's decision, copied as decided; L5 never recomputes
// them from posture. Execute refuses an undecided transaction before this runs.
// Event receipt evidence does not depend on the caller staying connected, so the
// state root read ignores ctx cancellation, as the event receipt writes do.
func (w *L5Actuator) buildInitialEventReceipt(ctx context.Context, vt *VerifiedTransaction) *operatorv1.EventReceipt {
	stateBefore := ""
	if w.StateRootProvider != nil {
		var err error
		stateBefore, err = w.StateRootProvider.GetCurrentStateRoot(context.WithoutCancel(ctx))
		if err != nil {
			w.Logger.Warn("Failed to get state root before execution", string(constants.ConnectionStateError), err)
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

	return &operatorv1.EventReceipt{
		TransactionId:              vt.Envelope.Id,
		TransactionHash:            vt.Envelope.TransactionHash,
		EventType:                  vt.Envelope.EventType,
		ActionType:                 vt.Envelope.ActionType,
		Status:                     operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
		ResultSummary:              "executing",
		StateRootBefore:            stateBefore,
		ExecutedAtUnixMs:           time.Now().UnixMilli(),
		SignerKeyId:                w.KeyID,
		L2Status:                   vt.L2Status,
		L3Status:                   vt.L3Status,
		DeterministicStageEvidence: stages,
	}
}

// signAndLogEventReceipt signs the initial event receipt and persists it. With an audit
// store, the event receipt and its commitment attestation commit in one transaction
// and the attestation is returned. Fail-closed: returns error if any step fails.
func (w *L5Actuator) signAndLogEventReceipt(vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, eventReceipt *operatorv1.EventReceipt) (*operatorv1.CommitmentAttestation, error) {
	sig, err := w.signEventReceipt(eventReceipt)
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to sign initial action event receipt", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorSignEventReceipt, err)
	}
	eventReceipt.Signature = sig

	if err := w.verifyAuditorKey(); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorCommitmentPersist, err)
	}
	var attestation *operatorv1.CommitmentAttestation
	err = w.SQLAuditStore.RecordEventReceiptWithCommitment(BuildEventReceiptRecord(vt.Envelope, audit, eventReceipt), func(priorHash string) (*operatorv1.CommitmentAttestation, error) {
		built, err := w.buildCommitment(vt, eventReceipt, priorHash)
		if err != nil {
			return nil, err
		}
		attestation = built
		return built, nil
	})
	if err != nil {
		w.Logger.Error("Fail-closed: Failed to persist initial action event receipt and commitment", string(constants.ConnectionStateError), err, "message_id", vt.Envelope.Id)
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

// buildCommitment signs the event receipt's commitment attestation over the given
// chain head. Storage owns its canonical JSON encoding.
func (w *L5Actuator) buildCommitment(vt *VerifiedTransaction, eventReceipt *operatorv1.EventReceipt, priorHash string) (*operatorv1.CommitmentAttestation, error) {
	attestation := &operatorv1.CommitmentAttestation{
		TransactionId:               vt.Envelope.Id,
		TransactionHash:             vt.Envelope.TransactionHash,
		PriorCommitmentHash:         priorHash,
		StateRootAtCommit:           eventReceipt.StateRootBefore,
		L2SignatureDigest:           l2SignatureDigest(vt.Envelope),
		WardenIntentSignatureDigest: SignatureDigest([]string{eventReceipt.Signature}),
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

// rehydratePayload rehydrates the command message payload in place. Fail-closed:
// returns an error on rehydration failure, and an Actuator with no
// ScrubbingService refuses a payload that holds a token it cannot rehydrate.
func (w *L5Actuator) rehydratePayload(ctx context.Context, vt *VerifiedTransaction, cmdMsg CommandMessage) error {
	if cmdMsg == nil {
		return nil
	}
	p := cmdMsg.GetPayload()
	if len(p) == 0 {
		return nil
	}
	if w.Scrubbing == nil {
		if scrubbing.ContainsToken(p) {
			w.Logger.Error("Fail-closed: payload holds a UEI token and no scrubbing service is configured", string(constants.ConnectionStateError), constants.ErrScrubbingTokenUnresolved, "message_id", vt.Envelope.Id)
			return fmt.Errorf("%w: no scrubbing service: %w", constants.ErrL5ActuatorRehydrate, constants.ErrScrubbingTokenUnresolved)
		}
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

// finalizeEventReceipt updates the event receipt with execution result, state root after, and timestamp.
// The mutation has already run, so the summary tokenization and the state root read ignore ctx
// cancellation.
func (w *L5Actuator) finalizeEventReceipt(ctx context.Context, eventReceipt *operatorv1.EventReceipt, summary, inferenceDigest, auditEventReceiptListSummary string, execErr error) {
	status := operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	if execErr != nil {
		status = operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED
		eventReceipt.ResultSummary = w.failedEventReceiptSummary(context.WithoutCancel(ctx), "failed", classifyEventReceiptFailure(execErr), execErr)
	} else {
		eventReceipt.ResultSummary = w.completedEventReceiptSummary(context.WithoutCancel(ctx), summary, inferenceDigest, auditEventReceiptListSummary)
	}

	stateAfter := ""
	if w.StateRootProvider != nil {
		var stateErr error
		stateAfter, stateErr = w.StateRootProvider.GetCurrentStateRoot(context.WithoutCancel(ctx))
		if stateErr != nil {
			w.Logger.Warn("Failed to get state root after execution", string(constants.ConnectionStateError), stateErr)
		}
	}

	eventReceipt.Status = status
	eventReceipt.FailureCode = classifyEventReceiptFailure(execErr)
	eventReceipt.StateRootAfter = stateAfter
	eventReceipt.ExecutedAtUnixMs = time.Now().UnixMilli()
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

// completedEventReceiptSummary returns the signed ResultSummary of a completed execution. The
// inference result digest is an identifier that the Gateway checks against the event receipt. It
// arrives on its own typed channel (inferenceDigest, set only for INFERENCE actions) and is
// signed as is. Every other handler text is content: it is scrubbed when a scrubber is active
// and replaced by fixed text otherwise. The text is never inspected for a digest shape, so a
// hex-encoded secret returned by a tool is scrubbed like any other output.
func (w *L5Actuator) completedEventReceiptSummary(ctx context.Context, summary, inferenceDigest, auditEventReceiptListSummary string) string {
	switch {
	case inferenceDigest != "":
		return inferenceDigest
	case auditEventReceiptListSummary != "":
		return auditEventReceiptListSummary
	case summary == "":
		return "completed"
	case !w.scrubberActive():
		return "completed"
	default:
		return w.Scrubbing.ScrubText(ctx, summary)
	}
}

// auditEventReceiptListSummary preserves only the allowlisted typed result for a
// verified MCP_CALL(audit_event_receipt_list). That result contains canonical signed
// event receipts, already scrubbed before their original L5 signature, plus scope
// metadata. Arbitrary text summaries continue through completedEventReceiptSummary's
// normal scrub path.
func (w *L5Actuator) auditEventReceiptListSummary(vt *VerifiedTransaction, cmdMsg CommandMessage, summary string) string {
	if vt == nil || vt.ActionType != constants.ActionTypeMcpCall || cmdMsg == nil || summary == "" {
		return ""
	}
	request := &operatorv1.McpCallRequested{}
	if err := proto.Unmarshal(vt.Envelope.GetPayload(), request); err != nil || request.GetToolName() != constants.MCPToolAuditEventReceiptList {
		return ""
	}
	provider, ok := cmdMsg.(interface {
		AuditEventReceiptListSummary() *models.OperatorAuditEventReceiptListResult
	})
	if !ok {
		return ""
	}
	result := provider.AuditEventReceiptListSummary()
	if result == nil || result.OperatorID != vt.Envelope.GetOperatorId() || result.Authority != "operator_local_ledger" ||
		result.InvestigationID == "" || result.InvestigationID != vt.Envelope.GetInvestigationId() ||
		result.Count != len(result.EventReceipts) {
		return ""
	}
	seen := make(map[string]struct{}, len(result.EventReceipts))
	for _, eventReceipt := range result.EventReceipts {
		if eventReceipt == nil || eventReceipt.GetTransactionId() == "" || eventReceipt.GetTransactionHash() == "" || eventReceipt.GetSignature() == "" {
			return ""
		}
		if _, exists := seen[eventReceipt.GetTransactionId()]; exists {
			return ""
		}
		seen[eventReceipt.GetTransactionId()] = struct{}{}
	}
	wire, err := json.Marshal(result)
	if err != nil || string(wire) != summary {
		return ""
	}
	return summary
}

// failedEventReceiptSummary returns the signed ResultSummary of a failed execution or a rejection. The
// cause text is scrubbed when a scrubber is active. Without one, only the typed failure code is signed.
func (w *L5Actuator) failedEventReceiptSummary(ctx context.Context, verb string, code operatorv1.EventReceiptFailureCode, cause error) string {
	if cause == nil || !w.scrubberActive() {
		return verb + ": " + code.String()
	}
	return verb + ": " + w.Scrubbing.ScrubText(ctx, cause.Error())
}

// signedView returns the transaction as it is written into signed event receipts, commitment attestations
// and event receipt publications. Only the target resource differs: it is scrubbed on a copy of the
// envelope before anything is signed. The capability and the execution handler keep the real target.
// It fails closed: a target with no active scrubber is refused before any event receipt is signed or the
// handler runs.
func (w *L5Actuator) signedView(ctx context.Context, vt *VerifiedTransaction) (*VerifiedTransaction, error) {
	target := vt.Envelope.GetTargetResource()
	if target == "" {
		return vt, nil
	}
	if !w.scrubberActive() {
		return nil, fmt.Errorf("l5 actuator: scrub target resource: %w", constants.ErrEgressScrubberUnavailable)
	}
	env := proto.Clone(vt.Envelope).(*govtypes.GovernanceEnvelope)
	env.TargetResource = w.Scrubbing.ScrubText(ctx, target)
	view := *vt
	view.Envelope = env
	return &view, nil
}

// classifyEventReceiptFailure maps an execution error to the typed
// EventReceiptFailureCode stamped on the FAILED event receipt. The code is bound into
// the event receipt signature so the User Gateway can map it back to a typed
// sentinel without parsing the result_summary text.
func classifyEventReceiptFailure(execErr error) operatorv1.EventReceiptFailureCode {
	if execErr == nil {
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_UNSPECIFIED
	}
	switch {
	case errors.Is(execErr, constants.ErrInferenceDecisionRequestInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_DECISION_REQUEST_INVALID
	case errors.Is(execErr, constants.ErrInferenceDecisionResponseInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_DECISION_RESPONSE_INVALID
	case errors.Is(execErr, constants.ErrInferenceDecisionUnsupported):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_DECISION_UNSUPPORTED
	case errors.Is(execErr, constants.ErrInferenceHostedNotAllowed):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_HOSTED_NOT_ALLOWED
	case errors.Is(execErr, constants.ErrInferenceModelOverrideDenied):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED
	case errors.Is(execErr, constants.ErrInferenceRoleInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_ROLE_INVALID
	case errors.Is(execErr, constants.ErrInferenceModelRefInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_REF_INVALID
	case errors.Is(execErr, constants.ErrInferenceBackendUnavailable),
		errors.Is(execErr, constants.ErrInferenceBackendNotRegistered):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE
	case errors.Is(execErr, constants.ErrInferenceBackendTimeout):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT
	case errors.Is(execErr, constants.ErrInferenceGenerateFailed):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GENERATE_FAILED
	case errors.Is(execErr, constants.ErrInferenceModelNotFound):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND
	case errors.Is(execErr, constants.ErrInferenceProviderResponseInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID
	case errors.Is(execErr, constants.ErrInferenceGenerationOptionsInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID
	case errors.Is(execErr, constants.ErrInferenceCapabilityUnsupported):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED
	case errors.Is(execErr, constants.ErrInferenceToolsUnsupported):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED
	case errors.Is(execErr, constants.ErrInferenceContextOverflow):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW
	case errors.Is(execErr, constants.ErrInferenceProviderAttemptRequired):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED
	case errors.Is(execErr, constants.ErrInferenceIdentityMismatch):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH
	case errors.Is(execErr, constants.ErrInferenceModelDigestMismatch):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH
	case errors.Is(execErr, constants.ErrInferenceEvidenceHashInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID
	case errors.Is(execErr, constants.ErrInferenceModelRegistryInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID
	case errors.Is(execErr, constants.ErrInferenceCampaignBindingInvalid):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID
	case errors.Is(execErr, constants.ErrScrubbingTokenUnresolved),
		errors.Is(execErr, constants.ErrScrubbingKeyCollision):
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_REHYDRATION_FAILED
	default:
		return operatorv1.EventReceiptFailureCode_EVENT_RECEIPT_FAILURE_CODE_EXECUTION_FAILED
	}
}

// signFinalEventReceipt signs the final event receipt and its persistence attestation. The
// attestation stays on the event receipt until the caller's write succeeds, so an
// attestation always describes a stored event receipt.
func (w *L5Actuator) signFinalEventReceipt(eventReceipt *operatorv1.EventReceipt, messageID string) error {
	finalSig, err := w.signEventReceipt(eventReceipt)
	if err != nil {
		w.Logger.Error("Failed to sign final action event receipt", string(constants.ConnectionStateError), err, "message_id", messageID)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorSignEventReceipt, err)
	}
	eventReceipt.Signature = finalSig

	attestation, err := w.signEventReceiptPersistenceAttestation(eventReceipt)
	if err != nil {
		return err
	}
	eventReceipt.FinalPersistenceAttestation = attestation
	return nil
}

// signAndLogFinalEventReceipt signs the final event receipt of an executed transaction and
// persists it with its attestation in one write. Best-effort: returns error but
// the event receipt is still returned by caller, because the mutation already ran.
func (w *L5Actuator) signAndLogFinalEventReceipt(ctx context.Context, vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, eventReceipt *operatorv1.EventReceipt) error {
	if err := w.signFinalEventReceipt(eventReceipt, vt.Envelope.Id); err != nil {
		return err
	}
	if logErr := w.LogEventReceipt(ctx, vt.Envelope, audit, eventReceipt); logErr != nil {
		eventReceipt.FinalPersistenceAttestation = nil
		w.Logger.Error("Failed to log final action event receipt - mutation already executed", string(constants.ConnectionStateError), logErr, "message_id", vt.Envelope.Id)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorLogEventReceipt, logErr)
	}
	return nil
}

// signAndLogRejectionEventReceipt signs and persists the event receipt of a rejected
// transaction. Nothing executed, so a persistence failure means the rejection
// evidence was lost, not that a mutation went unrecorded.
func (w *L5Actuator) signAndLogRejectionEventReceipt(ctx context.Context, vt *VerifiedTransaction, audit govtypes.RuntimeIdentity, eventReceipt *operatorv1.EventReceipt) error {
	if err := w.signFinalEventReceipt(eventReceipt, vt.Envelope.Id); err != nil {
		return err
	}
	if logErr := w.LogEventReceipt(ctx, vt.Envelope, audit, eventReceipt); logErr != nil {
		eventReceipt.FinalPersistenceAttestation = nil
		w.Logger.Error("Failed to persist rejection event receipt - rejection evidence lost, nothing was executed", string(constants.ConnectionStateError), logErr, "message_id", vt.Envelope.Id)
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorRejectionEventReceiptPersist, logErr)
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

// publicationEnvelope returns the envelope a rejection event receipt is published
// with: the identity fields name the publishing runtime, not the envelope's claim.
func publicationEnvelope(env *govtypes.GovernanceEnvelope, identity govtypes.RuntimeIdentity) *govtypes.GovernanceEnvelope {
	published := proto.Clone(env).(*govtypes.GovernanceEnvelope)
	published.OperatorId = identity.OperatorID
	published.OperatorSessionId = identity.SessionID
	return published
}

func CanonicalizeEventReceiptPersistenceAttestation(attestation *operatorv1.EventReceiptPersistenceAttestation) ([]byte, error) {
	if attestation == nil {
		return nil, constants.ErrEventReceiptPersistenceAttestationMissing
	}
	var payload bytes.Buffer
	writeCanonicalString(&payload, attestation.TransactionId)
	writeCanonicalString(&payload, attestation.EventReceiptSignatureDigest)
	var persistedAt [8]byte
	binary.BigEndian.PutUint64(persistedAt[:], uint64(attestation.PersistedAtUnixMs))
	payload.Write(persistedAt[:])
	writeCanonicalString(&payload, attestation.AuditRecordId)
	writeCanonicalString(&payload, attestation.SignerKeyId)
	return payload.Bytes(), nil
}

func (w *L5Actuator) signEventReceiptPersistenceAttestation(eventReceipt *operatorv1.EventReceipt) (*operatorv1.EventReceiptPersistenceAttestation, error) {
	attestation := &operatorv1.EventReceiptPersistenceAttestation{
		TransactionId:               eventReceipt.TransactionId,
		EventReceiptSignatureDigest: SignatureDigest([]string{eventReceipt.Signature}),
		PersistedAtUnixMs:           time.Now().UnixMilli(),
		AuditRecordId:               eventReceipt.TransactionId,
		SignerKeyId:                 w.KeyID,
	}
	payload, err := CanonicalizeEventReceiptPersistenceAttestation(attestation)
	if err != nil {
		return nil, fmt.Errorf("%w: persistence attestation: %w", constants.ErrL5ActuatorCanonicalizeEventReceipt, err)
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

func DeterministicStageActionType(eventReceipt *operatorv1.EventReceipt) (string, error) {
	if eventReceipt == nil {
		return "", fmt.Errorf("%w: action event receipt is nil", constants.ErrEvidenceArtifactMalformed)
	}
	actionType := ""
	for _, stage := range eventReceipt.GetDeterministicStageEvidence() {
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

func VerifyEventReceiptSignature(eventReceipt *operatorv1.EventReceipt, publicKey ed25519.PublicKey) error {
	if eventReceipt == nil {
		return constants.ErrEventReceiptMissing
	}
	if eventReceipt.Signature == "" || len(publicKey) != ed25519.PublicKeySize {
		return constants.ErrEventReceiptSignatureInvalid
	}
	signature, err := hex.DecodeString(eventReceipt.Signature)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrEventReceiptSignatureInvalid, err)
	}
	payload, err := CanonicalizeEventReceipt(eventReceipt)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrEventReceiptSignatureInvalid, err)
	}
	if !ed25519.Verify(publicKey, payload, signature) {
		return constants.ErrEventReceiptSignatureInvalid
	}
	return nil
}

func VerifyEventReceiptPersistenceAttestation(eventReceipt *operatorv1.EventReceipt, publicKey ed25519.PublicKey) error {
	if eventReceipt == nil || eventReceipt.FinalPersistenceAttestation == nil {
		return constants.ErrEventReceiptPersistenceAttestationMissing
	}
	attestation := eventReceipt.FinalPersistenceAttestation
	if eventReceipt.TransactionId == "" ||
		attestation.TransactionId != eventReceipt.TransactionId ||
		attestation.AuditRecordId != eventReceipt.TransactionId ||
		eventReceipt.SignerKeyId == "" ||
		attestation.SignerKeyId != eventReceipt.SignerKeyId ||
		attestation.PersistedAtUnixMs <= 0 {
		return constants.ErrEventReceiptPersistenceAttestationInvalid
	}
	if eventReceipt.Signature == "" || attestation.EventReceiptSignatureDigest != SignatureDigest([]string{eventReceipt.Signature}) {
		return constants.ErrEventReceiptPersistenceSignatureMismatch
	}
	signature, err := hex.DecodeString(attestation.Signature)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrEventReceiptPersistenceAttestationInvalid, err)
	}
	payload, err := CanonicalizeEventReceiptPersistenceAttestation(attestation)
	if err != nil {
		return err
	}
	if len(publicKey) != ed25519.PublicKeySize || !ed25519.Verify(publicKey, payload, signature) {
		return constants.ErrEventReceiptPersistenceAttestationInvalid
	}
	return nil
}

// VerifyEventReceipt verifies an event receipt's signature and its final-persistence
// attestation against the executing runtime's actuator public key.
func VerifyEventReceipt(eventReceipt *operatorv1.EventReceipt, publicKey ed25519.PublicKey) error {
	if err := VerifyEventReceiptSignature(eventReceipt, publicKey); err != nil {
		return err
	}
	return VerifyEventReceiptPersistenceAttestation(eventReceipt, publicKey)
}

// canonicalEventReceipt is the typed representation for EventReceipt canonicalization.
// This ensures strict typing and deterministic JSON marshaling for signing/verification.
type canonicalEventReceipt struct {
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

// CanonicalizeEventReceipt produces a deterministic byte representation for signing/verification.
// This function must be used by both signing and verification to ensure consistency.
// Field order: transaction_id, transaction_hash, event_type, action_type (v2 event receipts),
// status, result_summary, state_root_before, state_root_after, executed_at_unix_ms,
// signer_key_id, l2_status, l3_status, and the deterministic stage evidence hash when present.
func CanonicalizeEventReceipt(r *operatorv1.EventReceipt) ([]byte, error) {
	stageEvidenceHash, err := deterministicStageEvidenceHash(r.DeterministicStageEvidence)
	if err != nil {
		return nil, fmt.Errorf("%w: deterministic stage evidence: %w", constants.ErrL5ActuatorCanonicalizeEventReceipt, err)
	}
	canonical := canonicalEventReceipt{
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
		return nil, fmt.Errorf("%w: %w", constants.ErrL5ActuatorMarshalEventReceipt, err)
	}
	return payload, nil
}

func (w *L5Actuator) signEventReceipt(r *operatorv1.EventReceipt) (string, error) {
	if len(w.SigningKey) == 0 {
		return "", constants.ErrL5ActuatorSigningKeyMissing
	}

	// Use canonical serialization for signing - shared with verification
	payload, err := CanonicalizeEventReceipt(r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrL5ActuatorCanonicalizeEventReceipt, err)
	}

	sig := ed25519.Sign(w.SigningKey, payload)
	return hex.EncodeToString(sig), nil
}

// requireAuditStore fails closed unless the actuator owns an audit store able
// to persist event receipts together with their commitment attestations. The
// SQLAuditStore is the single owner of event receipt records; there is no
// event_receipt-less execution path.
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

// LogEventReceipt records the signed event receipt on its chain event.
// The row is filed under audit, the operator and session the runtime owns.
func (w *L5Actuator) LogEventReceipt(_ context.Context, env *govtypes.GovernanceEnvelope, audit govtypes.RuntimeIdentity, r *operatorv1.EventReceipt) error {
	if w.SQLAuditStore == nil {
		return constants.ErrL5ActuatorAuditStoreMissing
	}
	if env == nil {
		return constants.ErrTxInvalidEnvelope
	}
	if err := w.SQLAuditStore.RecordEventReceipt(BuildEventReceiptRecord(env, audit, r)); err != nil {
		if w.Logger != nil {
			w.Logger.Error("Failed to record EventReceipt in audit store", string(constants.ConnectionStateError), err)
		}
		return fmt.Errorf("%w: %w", constants.ErrL5ActuatorAuditStore, err)
	}
	return nil
}

// buildEventReceiptRecord constructs an EventReceiptRecord from a GovernanceEnvelope and EventReceipt.
// This is the single source of truth for record construction, used by LogEventReceipt and the commitment-bound initial event receipt write.
// audit is the operator and session the audit row is filed under. It is passed explicitly because the
// envelope's operator_id and operator_session_id are claims and are not the audit identity of a rejected
// transaction; the signed event receipt keeps the claims in its stage evidence.
func BuildEventReceiptRecord(env *govtypes.GovernanceEnvelope, audit govtypes.RuntimeIdentity, r *operatorv1.EventReceipt) *models.EventReceiptRecord {
	eventType := constants.EventType(env.EventType)
	if eventType == "" && r.EventType != "" {
		eventType = constants.EventType(r.EventType)
	}

	return &models.EventReceiptRecord{
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
		EventReceipt:      proto.Clone(r).(*operatorv1.EventReceipt),
	}
}

// Wait blocks until all in-flight transactions have finished executing.
func (w *L5Actuator) Wait() {
	w.wg.Wait()
}

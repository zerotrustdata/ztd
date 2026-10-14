// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	govtypes "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/system"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// VerifiedTransaction represents a fully verified transaction ready for execution.
type VerifiedTransaction struct {
	Envelope                   *govtypes.GovernanceEnvelope
	ActionType                 constants.ActionType
	Payload                    []byte
	DecodedPayload             proto.Message
	StateRoot                  string
	Nonce                      string
	ExpiresAt                  time.Time
	L2Valid                    bool // Whether L2 signature was valid (may be false in Doctrine posture)
	L3Valid                    bool // Whether L3 proof was valid (may be false in Doctrine/Consensus posture)
	Posture                    GovernancePosture
	DeterministicStageEvidence []*operatorv1.DeterministicStageEvidence
}

// L4Warden performs all pre-dispatch verification checks.
var governanceMonotonicEpoch = time.Now()

func governanceMonotonicNow() int64 {
	return time.Since(governanceMonotonicEpoch).Nanoseconds()
}

func deterministicStageID(envelope *govtypes.GovernanceEnvelope, kind operatorv1.DeterministicStageKind) string {
	return fmt.Sprintf("%s:%s", envelope.Id, kind.String())
}

func newDeterministicStageEvidence(
	envelope *govtypes.GovernanceEnvelope,
	kind operatorv1.DeterministicStageKind,
	start int64,
	outcome operatorv1.DeterministicStageOutcome,
) *operatorv1.DeterministicStageEvidence {
	return &operatorv1.DeterministicStageEvidence{
		StageId:           deterministicStageID(envelope, kind),
		Kind:              kind,
		MonotonicStartNs:  start,
		MonotonicEndNs:    governanceMonotonicNow(),
		ClockDomain:       "ztd-operator-process",
		TimingSource:      "go_monotonic",
		Outcome:           outcome,
		TransactionId:     envelope.Id,
		TransactionHash:   envelope.TransactionHash,
		ActionType:        envelope.ActionType,
		OperatorId:        envelope.OperatorId,
		OperatorSessionId: envelope.OperatorSessionId,
		RequestorUserId:   envelope.RequestorUserId,
		ActingAppId:       envelope.ActingAppId,
		CaseId:            envelope.CaseId,
		InvestigationId:   envelope.InvestigationId,
		TaskId:            envelope.TaskId,
		StateRootBefore:   envelope.StateMerkleRoot,
	}
}

// RuntimeVault reports whether this runtime's encryption vault is unlocked. The
// Warden checks it before dispatch: a locked vault means the local execution record
// and the L5 receipts cannot be written, so nothing may execute.
type RuntimeVault interface {
	IsUnlocked() bool
}

// RuntimeIdentityProvider resolves the identity this runtime owns. It fails when
// the runtime has no session yet, such as an unclaimed embedded Operator.
type RuntimeIdentityProvider interface {
	RuntimeIdentity(ctx context.Context) (govtypes.RuntimeIdentity, error)
}

// ExecutionTarget identifies the Operators whose actions this runtime executes
// and the audit session it executes them under.
type ExecutionTarget interface {
	RuntimeIdentityProvider
	ExecutesFor(operatorID string) bool
}

type L4Warden struct {
	executionTarget      ExecutionTarget
	runtimeVault         RuntimeVault
	logger               *slog.Logger
	replayStore          ReplayStore
	stateRootProvider    StateRootProvider
	signerStore          SignerStore
	consensusPolicyStore L2ConsensusPolicyStore
	l3Notary             L3Notary
	doctrine             *L1Doctrine
	knownActionTypes     map[constants.ActionType]struct{}
	clock                system.Clock // Injectable time source for deterministic testing

	inFlight sync.Map // Concurrent-safe tracking of in-flight nonces
}

// NewL4Warden creates a new L4 Warden. The warden has no posture of its
// own; it reads posture per-envelope from GovernanceEnvelope.Posture at
// verification time. The gateway sets that field at envelope construction
// from cfg.Gateway.Posture.
func NewL4Warden(
	logger *slog.Logger,
	replayStore ReplayStore,
	stateRootProvider StateRootProvider,
	signerStore SignerStore,
	consensusPolicyStore L2ConsensusPolicyStore,
	l3Notary L3Notary,
	doctrine *L1Doctrine,
	knownActionTypes []constants.ActionType,
	clock system.Clock,
	executionTarget ExecutionTarget,
	runtimeVault RuntimeVault,
) *L4Warden {
	knownActions := make(map[constants.ActionType]struct{})
	for _, action := range knownActionTypes {
		knownActions[action] = struct{}{}
	}

	// Default to real clock if not provided
	if clock == nil {
		clock = &system.RealClock{}
	}

	return &L4Warden{
		executionTarget:      executionTarget,
		runtimeVault:         runtimeVault,
		logger:               logger,
		replayStore:          replayStore,
		stateRootProvider:    stateRootProvider,
		signerStore:          signerStore,
		consensusPolicyStore: consensusPolicyStore,
		l3Notary:             l3Notary,
		doctrine:             doctrine,
		knownActionTypes:     knownActions,
		clock:                clock,
	}
}

// postureFromEnvelope reads the gateway's governance posture from the
// envelope's Posture field. Fails closed with ErrEnvelopePostureMissing
// when the field is empty — that indicates a gateway bug (the constructor
// was called without wiring cfg.Gateway.Posture), not an operator config
// gap. The envelope is the authoritative source of posture for
// operator-side L2/L3 gating.
func postureFromEnvelope(envelope *govtypes.GovernanceEnvelope) (GovernancePosture, error) {
	if envelope.Posture == "" {
		return nil, constants.ErrEnvelopePostureMissing
	}
	return ParseGovernancePosture(envelope.Posture)
}

// VerifyEnvelope performs all required verification checks on a decoded GovernanceEnvelope JSON GovernanceEnvelope.
// It is decomposed into three discrete validation stages:
// 1. Stateless: Basic structural, hash, and L1 Doctrine checks that don't require external state.
// 2. Stateful: Checks requiring external state (expiry, state root, and early nonce reservation).
// 3. Posture: Governance posture-aware checks (L2 Consensus and L3 Notary proofs).
func (tv *L4Warden) VerifyEnvelope(ctx context.Context, envelope *govtypes.GovernanceEnvelope) (*VerifiedTransaction, error) {
	if envelope == nil {
		return nil, constants.ErrTxInvalidEnvelope
	}
	verificationStart := governanceMonotonicNow()
	rejected := &VerifiedTransaction{
		Envelope:   envelope,
		ActionType: constants.ActionType(envelope.ActionType),
	}
	var stages []*operatorv1.DeterministicStageEvidence
	reject := func(rejection error) (*VerifiedTransaction, error) {
		l4Stage := newDeterministicStageEvidence(
			envelope,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
			verificationStart,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
		)
		l4Stage.L2SignatureDigest = l2SignatureDigest(envelope)
		l4Stage.L3SignatureDigest = humanSignatureDigest(envelope)
		for _, stage := range stages {
			stage.ParentStageId = l4Stage.StageId
		}
		rejected.DeterministicStageEvidence = append(stages, l4Stage)
		return rejected, rejection
	}

	// 0a. Refuse dispatch when the runtime vault is locked or absent. Execution is
	// recorded in the encrypted local store, so without the vault nothing may run.
	if err := tv.checkRuntimeVault(); err != nil {
		return rejected, err
	}

	// 0. Early trackInFlight check to save expensive DB/cryptography operations.
	// The critical section must extend through nonce reservation to prevent race conditions.
	if err := tv.trackInFlight(envelope.Nonce); err != nil {
		return reject(err)
	}

	// 1. Early nonce reservation for durable replay protection.
	// This prevents replay attacks if the Operator crashes mid-execution.
	// The nonce is reserved early and finalized after successful execution.
	if tv.replayStore == nil {
		tv.releaseInFlight(envelope.Nonce)
		return reject(constants.ErrTxReplayStoreMissing)
	}
	if envelope.ExpiresAt == nil {
		tv.releaseInFlight(envelope.Nonce)
		return reject(constants.ErrTxExpiresAtMissing)
	}
	expiresAt := envelope.ExpiresAt.AsTime()
	if tv.clock.Now().After(expiresAt) {
		tv.logger.Error("Transaction rejected: EXPIRED",
			"nonce", envelope.Nonce,
			"expires_at", expiresAt,
			"now", tv.clock.Now())
		tv.releaseInFlight(envelope.Nonce)
		return reject(constants.ErrTxTransactionExpired)
	}
	if envelope.Nonce == "" {
		tv.logger.Error("Transaction rejected: NONCE_MISSING")
		tv.releaseInFlight(envelope.Nonce)
		return reject(constants.ErrTxNonceMissing)
	}
	replayed, err := tv.replayStore.ReserveNonce(envelope.Nonce, expiresAt)
	if err != nil {
		tv.logger.Error("Transaction rejected: REPLAY_CHECK_FAILED",
			"nonce", envelope.Nonce,
			string(constants.ConnectionStateError), err)
		tv.releaseInFlight(envelope.Nonce)
		return reject(fmt.Errorf("l4 warden: reserve nonce: %w", err))
	}
	if replayed {
		tv.logger.Error("Transaction rejected: REPLAY_DETECTED", "nonce", envelope.Nonce)
		tv.releaseInFlight(envelope.Nonce)
		return reject(constants.ErrTxTransactionReplay)
	}

	// Nonce is now durably reserved in SQLite, safe to release in-flight lock
	tv.releaseInFlight(envelope.Nonce)

	// 2. Stateless Validation, split at the L1 Doctrine boundary. Only the doctrine
	// step produces an L1_DOCTRINE stage: a failure before doctrine ran is not a
	// doctrine judgment, so it carries no L1 stage.
	decodedPayload, err := tv.verifyPreDoctrine(ctx, envelope)
	if err != nil {
		// The error can embed violating payload content (paths, patterns). The full
		// reason is carried by the signed rejection receipt, not the plaintext log.
		tv.logger.Error("Transaction rejected: STATELESS_VALIDATION_FAILED",
			slog.Any("nonce", envelope.Nonce),
			slog.Any("action_type", envelope.ActionType),
			logging.ErrorKind(err))
		tv.releaseNonceReservation(envelope.Nonce)
		return reject(err)
	}

	doctrineStart := governanceMonotonicNow()
	doctrineErr := tv.evaluateDoctrine(decodedPayload)
	l1Outcome := operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED
	if doctrineErr != nil {
		l1Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
	}
	l1Stage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L1_DOCTRINE,
		doctrineStart,
		l1Outcome,
	)
	l1Stage.DoctrineBundleHash = tv.doctrine.DoctrineBundleHash()
	l1Stage.DoctrineBundleVersion = tv.doctrine.DoctrineBundleVersion()
	stages = []*operatorv1.DeterministicStageEvidence{l1Stage}
	if doctrineErr != nil {
		tv.logger.Error("Transaction rejected: STATELESS_VALIDATION_FAILED",
			slog.Any("nonce", envelope.Nonce),
			slog.Any("action_type", envelope.ActionType),
			logging.ErrorKind(doctrineErr))
		tv.releaseNonceReservation(envelope.Nonce)
		return reject(doctrineErr)
	}

	// Doctrine passed, so any failure from here on is a hash-binding failure and the
	// L1 stage above correctly stays VERIFIED.
	computedHash, err := tv.verifyTransactionHash(envelope)
	if err != nil {
		tv.logger.Error("Transaction rejected: STATELESS_VALIDATION_FAILED",
			slog.Any("nonce", envelope.Nonce),
			slog.Any("action_type", envelope.ActionType),
			logging.ErrorKind(err))
		tv.releaseNonceReservation(envelope.Nonce)
		return reject(err)
	}

	// 3. Stateful Validation (excluding nonce, which is already reserved)
	err = tv.verifyStateful(ctx, envelope)
	if err != nil {
		tv.logger.Error("Transaction rejected: STATEFUL_VALIDATION_FAILED",
			"nonce", envelope.Nonce,
			"tx_id", envelope.Id,
			string(constants.ConnectionStateError), err)
		// Release nonce reservation on stateful validation failure
		tv.releaseNonceReservation(envelope.Nonce)
		return reject(err)
	}

	// 4. Posture Validation (L2/L3)
	// Posture is read per-envelope from GovernanceEnvelope.Posture, which
	// the gateway sets at construction time. The warden has no posture of
	// its own; the envelope is authoritative.
	posture, err := postureFromEnvelope(envelope)
	if err != nil {
		tv.logger.Error("Transaction rejected: ENVELOPE_POSTURE_MISSING",
			"nonce", envelope.Nonce,
			"tx_id", envelope.Id,
			string(constants.ConnectionStateError), err)
		tv.releaseNonceReservation(envelope.Nonce)
		return reject(err)
	}
	rejected.Posture = posture
	l2Valid, l3Valid, postureStages, err := tv.verifyPosture(ctx, envelope, computedHash, posture)
	rejected.L2Valid = l2Valid
	rejected.L3Valid = l3Valid
	if err != nil {
		tv.logger.Error("Transaction rejected: POSTURE_VALIDATION_FAILED",
			"nonce", envelope.Nonce,
			"tx_id", envelope.Id,
			"posture", posture.Name(),
			string(constants.ConnectionStateError), err)
		// Release nonce reservation on posture validation failure
		tv.releaseNonceReservation(envelope.Nonce)
		stages = append(stages, postureStages...)
		return reject(err)
	}

	// 5. Return verified transaction (nonce remains reserved, will be finalized after execution)
	stages = append(stages, postureStages...)
	l4Stage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L4_VERIFICATION,
		verificationStart,
		operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED,
	)
	l4Stage.L2SignatureDigest = l2SignatureDigest(envelope)
	l4Stage.L3SignatureDigest = humanSignatureDigest(envelope)
	for _, stage := range stages {
		stage.ParentStageId = l4Stage.StageId
	}
	stages = append(stages, l4Stage)
	return &VerifiedTransaction{
		Envelope:                   envelope,
		ActionType:                 constants.ActionType(envelope.ActionType),
		Payload:                    envelope.Payload,
		DecodedPayload:             decodedPayload,
		StateRoot:                  envelope.StateMerkleRoot,
		Nonce:                      envelope.Nonce,
		ExpiresAt:                  expiresAt,
		L2Valid:                    l2Valid,
		L3Valid:                    l3Valid,
		Posture:                    posture,
		DeterministicStageEvidence: stages,
	}, nil
}

// checkRuntimeVault fails closed: a missing vault view or a locked vault refuses dispatch.
func (tv *L4Warden) checkRuntimeVault() error {
	if tv.runtimeVault == nil {
		return constants.ErrWardenVaultUnavailable
	}
	if !tv.runtimeVault.IsUnlocked() {
		return constants.ErrWardenVaultLocked
	}
	return nil
}

func (tv *L4Warden) trackInFlight(nonce string) error {
	if nonce == "" {
		return nil
	}
	_, loaded := tv.inFlight.LoadOrStore(nonce, true)
	if loaded {
		tv.logger.Warn("Transaction with same nonce already in-flight", "nonce", nonce)
		return constants.ErrTxInFlight
	}
	return nil
}

func (tv *L4Warden) releaseInFlight(nonce string) {
	tv.inFlight.Delete(nonce)
}

func (tv *L4Warden) releaseNonceReservation(nonce string) {
	if err := tv.replayStore.ReleaseNonce(nonce); err != nil {
		tv.logger.Warn("Failed to release nonce reservation",
			"nonce", nonce,
			string(constants.ConnectionStateError), err)
	}
}

// isMutation returns true if the action type modifies system state.
// Uses the strongly-typed intrinsic property from the action definition.
// Mutation classification is defined in protocol/constants/status.json via the _mutation field.
// Actions marked as mutations require L3 Notary (human-presence) verification.
func (tv *L4Warden) isMutation(actionType constants.ActionType) bool {
	return actionType.IsMutation()
}

// verifyPreDoctrine performs the structural, identity, and typed-payload checks
// that run before L1 Doctrine judges the payload. Any error returned here means
// doctrine never evaluated the transaction, so the caller emits no L1 stage.
// On success it returns the decoded payload that doctrine must be given.
func (tv *L4Warden) verifyPreDoctrine(ctx context.Context, envelope *govtypes.GovernanceEnvelope) (proto.Message, error) {
	if envelope.ProtocolVersion != govtypes.GovernanceProtocolVersionV2 {
		tv.logger.Error("Transaction rejected: unsupported protocol version",
			"protocol_version", envelope.ProtocolVersion)
		return nil, constants.ErrTxProtocolVersionUnsupported
	}
	if tv.doctrine == nil {
		tv.logger.Error("L1Doctrine not configured")
		return nil, constants.ErrTxDoctrineMissing
	}

	if envelope.Id == "" {
		return nil, constants.ErrTxTransactionIDMissing
	}

	eventType := constants.EventType(envelope.EventType)
	if _, err := constants.ValidateGovernedRequest(eventType); err == nil {
		if err := constants.ValidateGovernedEnvelopeFields(eventType, constants.ActionType(envelope.ActionType)); err != nil {
			return nil, err
		}
	}

	actionType := constants.ActionType(envelope.ActionType)
	if _, ok := tv.knownActionTypes[actionType]; !ok {
		tv.logger.Error("Unknown action type", "action_type", envelope.ActionType)
		return nil, constants.ErrTxUnknownActionType
	}

	// Heartbeat and Shutdown messages have no required fields and marshal to
	// zero bytes when all-default — a valid empty proto, not a missing payload.
	if len(envelope.Payload) == 0 && !PayloadMayBeEmpty(actionType) {
		return nil, constants.ErrTxPayloadMissing
	}

	decodedPayload, err := tv.decodePayloadForAction(actionType, envelope.Payload)
	if err != nil {
		tv.logger.Error("Failed to decode typed payload", slog.Any("action_type", envelope.ActionType), logging.ErrorKind(err))
		if errors.Is(err, constants.ErrTxPayloadDecoderMissing) {
			return nil, err
		}
		return nil, constants.ErrTxPayloadDecodeFailed
	}
	if decodedPayload == nil {
		return nil, constants.ErrTxPayloadDecoderMissing
	}

	if err := verifyDocumentCollection(decodedPayload); err != nil {
		tv.logger.Error("Document action rejected: collection not governed", "action_type", envelope.ActionType, string(constants.ConnectionStateError), err)
		return nil, err
	}

	// Unbound app document writes are the sole identity exception (INV-AUTH-ID-05).
	unboundDocument := envelope.OperatorId == "" &&
		(actionType == constants.ActionTypeDocumentUpdate || actionType == constants.ActionTypeDocumentDelete)
	if tv.executionTarget == nil || (!unboundDocument && (envelope.OperatorId == "" || !tv.executionTarget.ExecutesFor(envelope.OperatorId))) {
		return nil, fmt.Errorf("%w: %q", constants.ErrTxTargetOperatorMismatch, envelope.OperatorId)
	}
	if err := tv.verifyTargetSession(ctx, envelope); err != nil {
		return nil, err
	}

	return decodedPayload, nil
}

// evaluateDoctrine runs L1 Doctrine over the decoded payload. The only error it
// returns is one wrapping constants.ErrTxL1ValidationFailed, so a non-nil result
// means doctrine ran and rejected the payload.
func (tv *L4Warden) evaluateDoctrine(decodedPayload proto.Message) error {
	if violations := tv.doctrine.ValidatePayload(decodedPayload); len(violations) > 0 {
		tv.logger.Error("Doctrine (L1Doctrine) validation failed", "violation_count", len(violations))
		return fmt.Errorf("%w: %s", constants.ErrTxL1ValidationFailed, strings.Join(violations, ", "))
	}
	return nil
}

// verifyTransactionHash recomputes the canonical transaction hash and checks it
// against both transaction_hash and id. It runs after L1 Doctrine has passed.
func (tv *L4Warden) verifyTransactionHash(envelope *govtypes.GovernanceEnvelope) (string, error) {
	computedHash, err := tv.computeTransactionHash(envelope)
	if err != nil {
		return "", fmt.Errorf("l4 warden: compute transaction hash: %w", err)
	}

	if envelope.TransactionHash == "" {
		return "", constants.ErrTxTransactionHashMissing
	}

	if envelope.TransactionHash != computedHash {
		tv.logger.Error("Transaction hash mismatch",
			"provided", envelope.TransactionHash,
			"computed", computedHash)
		return "", constants.ErrTxTransactionHashMismatch
	}

	if envelope.Id != computedHash {
		tv.logger.Error("Transaction id mismatch",
			"provided", envelope.Id,
			"computed", computedHash)
		return "", constants.ErrTxTransactionIDMismatch
	}

	return computedHash, nil
}

// verifyTargetSession binds the envelope's operator_session_id to the session
// this runtime owns. The claim is part of the transaction hash but the hash
// proves only that the sender wrote it, not that the runtime holds that session,
// so a non-empty claim must equal the runtime's own session. An empty claim is
// the app platform-record write, platform enrollment, and non-operator caller
// form: it names no session and L5 records it under the runtime's own session.
func (tv *L4Warden) verifyTargetSession(ctx context.Context, envelope *govtypes.GovernanceEnvelope) error {
	if envelope.OperatorSessionId == "" {
		return nil
	}
	if envelope.OperatorId == "" {
		return fmt.Errorf("%w: operator_session_id is claimed without an operator_id", constants.ErrTxTargetSessionMismatch)
	}
	identity, err := tv.executionTarget.RuntimeIdentity(ctx)
	if err != nil {
		return fmt.Errorf("%w: runtime session unavailable: %w", constants.ErrTxTargetSessionMismatch, err)
	}
	if identity.SessionID != envelope.OperatorSessionId {
		return fmt.Errorf("%w: %q", constants.ErrTxTargetSessionMismatch, envelope.OperatorSessionId)
	}
	return nil
}

// verifyStateful checks the state root. Nonce and expiry are checked earlier in VerifyEnvelope.
// Gateway-built transactions carry their admission snapshot in context, both
// in-process and on the authenticated outbound command channel. Concurrent
// mutations must not invalidate that binding before execution. Direct envelope
// submissions without that context compare against the local provider.
func (tv *L4Warden) verifyStateful(ctx context.Context, envelope *govtypes.GovernanceEnvelope) error {
	if envelope.StateMerkleRoot == "" {
		return constants.ErrTxStateRootRequired
	}

	if tv.stateRootProvider == nil {
		tv.logger.Error("State root verification required but provider not configured")
		return constants.ErrTxStateRootMissing
	}

	var currentRoot string
	if root, ok := ctx.Value(constants.ContextKeyStateMerkleRoot).(string); ok && root != "" {
		currentRoot = root
	} else {
		var err error
		currentRoot, err = tv.stateRootProvider.GetCurrentStateRoot(ctx)
		if err != nil {
			tv.logger.Error("Failed to get current state root", string(constants.ConnectionStateError), err)
			return fmt.Errorf("l4 warden: get current state root: %w", err)
		}
	}

	if currentRoot == "" {
		return constants.ErrTxStateRootMissing
	}

	if currentRoot != envelope.StateMerkleRoot {
		tv.logger.Error("State root mismatch",
			"envelope_root", envelope.StateMerkleRoot,
			"current_root", currentRoot)
		return constants.ErrTxStateRootMismatch
	}

	return nil
}

// verifyPosture performs governance posture-aware checks for L2 and L3.
// L2 evidence is evaluated before L3, but it gates L3 verification only when
// the active posture requires L2 consensus.
func (tv *L4Warden) verifyPosture(ctx context.Context, envelope *govtypes.GovernanceEnvelope, computedHash string, posture GovernancePosture) (bool, bool, []*operatorv1.DeterministicStageEvidence, error) {
	l2Start := governanceMonotonicNow()
	l2Valid, err := tv.verifyL2Posture(ctx, envelope, computedHash, posture)
	if err != nil {
		l2Stage := newDeterministicStageEvidence(
			envelope,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2,
			l2Start,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
		)
		l2Stage.L2SignatureDigest = l2SignatureDigest(envelope)
		return false, false, []*operatorv1.DeterministicStageEvidence{l2Stage}, err
	}
	l2Outcome := operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED
	if l2Valid {
		l2Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED
	}
	l2Stage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_PROTOCOL_L2,
		l2Start,
		l2Outcome,
	)
	l2Stage.L2SignatureDigest = l2SignatureDigest(envelope)

	l3Start := governanceMonotonicNow()
	l3Valid, err := tv.verifyL3Posture(ctx, envelope, posture)
	if err != nil {
		l3Stage := newDeterministicStageEvidence(
			envelope,
			operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY,
			l3Start,
			operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED,
		)
		l3Stage.L3SignatureDigest = humanSignatureDigest(envelope)
		return l2Valid, false, []*operatorv1.DeterministicStageEvidence{l2Stage, l3Stage}, err
	}
	l3Outcome := operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_NOT_REQUIRED
	if l3Valid {
		l3Outcome = operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_VERIFIED
	}
	l3Stage := newDeterministicStageEvidence(
		envelope,
		operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L3_NOTARY,
		l3Start,
		l3Outcome,
	)
	l3Stage.L3SignatureDigest = humanSignatureDigest(envelope)

	return l2Valid, l3Valid, []*operatorv1.DeterministicStageEvidence{l2Stage, l3Stage}, nil
}

func (tv *L4Warden) verifyL3Posture(ctx context.Context, envelope *govtypes.GovernanceEnvelope, posture GovernancePosture) (bool, error) {
	actionType := constants.ActionType(envelope.ActionType)

	hasProof := envelope.Governance != nil && envelope.Governance.L3 != nil && envelope.Governance.L3.Proof != nil

	if !hasProof {
		if tv.isMutation(actionType) && posture.RequiresL3Proof() {
			tv.logger.Error("L3 proof missing but required by posture", "posture", posture.Name())
			return false, constants.ErrTxL3ProofMissing
		}
		return false, nil
	}

	if tv.l3Notary == nil {
		if tv.isMutation(actionType) && posture.RequiresL3Proof() {
			tv.logger.Error("L3 notary not configured but required by posture", "posture", posture.Name())
			return false, constants.ErrTxL3NotaryNotConfigured
		}
		return false, nil
	}

	ok, err := tv.l3Notary.VerifyL3Proof(
		ctx,
		envelope.RequestorUserId,
		envelope.TransactionHash,
		envelope.CliSessionId,
		envelope.Governance.L3.Proof,
	)

	if (err != nil || !ok) && tv.isMutation(actionType) && posture.RequiresL3Proof() {
		tv.logger.Error("Notary (L3Notary) verification failed but required by posture", string(constants.ConnectionStateError), err)
		return false, constants.ErrTxL3ProofInvalid
	}

	return ok && err == nil, nil
}

// verifyDocumentCollection admits a DOCUMENT_UPDATE or DOCUMENT_DELETE payload
// only when it targets a governed document collection. The governed document
// store is the Gateway's platform document store, which also holds users,
// trusted_signers, app_policies, and other authority records, so a document
// action outside the governed set fails closed before execution. Other
// payload types pass.
func verifyDocumentCollection(payload proto.Message) error {
	var collection string
	switch p := payload.(type) {
	case *operatorv1.DocumentUpdateRequested:
		collection = p.GetCollection()
	case *operatorv1.DocumentDeleteRequested:
		collection = p.GetCollection()
	default:
		return nil
	}
	if !constants.CollectionName(collection).IsGovernedDocument() {
		return fmt.Errorf("%w: %q", constants.ErrTxDocumentCollectionNotGoverned, collection)
	}
	return nil
}

func (tv *L4Warden) decodePayloadForAction(actionType constants.ActionType, payload []byte) (proto.Message, error) {
	return DecodePayloadForAction(actionType, payload)
}

// computeTransactionHash computes the canonical transaction hash.
func (tv *L4Warden) computeTransactionHash(envelope *govtypes.GovernanceEnvelope) (string, error) {
	return govtypes.GenerateMessageID(envelope)
}

// Doctrine returns the current L1 doctrine validator.
func (tv *L4Warden) Doctrine() *L1Doctrine {
	return tv.doctrine
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/zerotrustdata/ztd/internal/constants"
	govpkg "github.com/zerotrustdata/ztd/internal/governance"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	serviceauth "github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	inferencedispatch "github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// DispatchTimeout is the maximum time to wait for an operator result after
// publishing a command. The round-trip includes in-process publish, WS delivery,
// operator L4/L5 verification and execution, WS publish back, and in-process
// handler delivery.
const DispatchTimeout = 30 * time.Second

// InferenceProgressResultBuffer is the in-process results-channel capacity
// while streaming inference progress. Embedded publishers wait for capacity;
// remote overflow is a typed backpressure failure, not a dropped event.
const InferenceProgressResultBuffer = 64

// EnvelopeExpiry is the lifetime of a dispatched GovernanceEnvelope from
// construction to operator expiry rejection.
const EnvelopeExpiry = 5 * time.Minute

// BuildEnvelopeParams is the input to BuildGovernanceEnvelope. The gateway
// owns the state Merkle root and governance posture; callers supply the
// command intent fields and the resolved operator/session identifiers.
// Application context fields (CaseID, InvestigationID, TaskID,
// WebSessionID, CliSessionID) are propagated from the command intent on
// the pubsub path and from the HTTP request on the dispatch path,
// ensuring uniform context propagation across transports.
//
// Doctrine is the L1 validator the builder runs against the decoded typed
// payload before setting L1.Validated. A nil doctrine fails closed: the
// builder cannot assert L1.Validated=true without screening.
type BuildEnvelopeParams struct {
	EvaluationRunID        string
	EvaluationAssignmentID string
	OperatorID             string
	OperatorSessionID      string
	EventType              string
	Payload                []byte
	TargetResource         string
	RequestorUserID        string
	ActingAppID            string
	StateMerkleRoot        string
	CaseID                 string
	InvestigationID        string
	TaskID                 string
	WebSessionID           string
	CliSessionID           string
	Posture                constants.Posture
	Doctrine               *governance.L1Doctrine
}

// BuildGovernanceEnvelope constructs a canonical GovernanceEnvelope with
// timestamp, expiry, nonce, L1 doctrine screening, and the supplied
// identity and state-root fields. It computes the transaction hash via
// govpkg.GenerateMessageID and sets both Id and TransactionHash to that
// value. The returned envelope is ready for protojson marshaling and
// fan-out to an operator's cmd channel. This is the single envelope
// construction authority for both the HTTP DispatchService and the
// WebSocket PubSub relay.
//
// L1 screening: the builder decodes the typed payload via the shared
// governance.DecodePayloadForAction, runs Doctrine.ValidatePayload, and
// sets L1.Validated=true only after a clean pass. It fails closed on nil
// doctrine (ErrTxDoctrineMissing), decode failure
// (ErrTxPayloadDecodeFailed), and L1 forbidden-pattern violations
// (ErrTxL1ValidationFailed). Governed request events without a typed proto
// decode case fail closed with ErrTxPayloadDecoderMissing.
//
// Check order: posture-missing, posture validation, doctrine-missing, and
// action-type validation run first. L1 screening then runs in every posture,
// before the L3 gate, so a forbidden payload is refused as
// ErrTxL1ValidationFailed even under ratify and notary.
//
// L3 gating: this public builder cannot mint L3 human proofs. After L1
// passes, when the posture requires L3 proof (ratify, notary) and the action
// is a mutation, it rejects the clean envelope with ErrTxL3ProofUnmintable
// rather than build a mutation that cannot satisfy L3 at the operator.
// DispatchService instead builds the envelope for a DispatchApprovalCoordinator
// that suspends it and waits for a real human proof.
func BuildGovernanceEnvelope(params BuildEnvelopeParams) (*commonv1.GovernanceEnvelope, error) {
	return buildGovernanceEnvelope(params, false)
}

// buildGovernanceEnvelope permits an unsigned L3 envelope only when its caller
// owns the human-approval wait. The public builder retains fail-closed admission.
func buildGovernanceEnvelope(params BuildEnvelopeParams, awaitingApproval bool) (*commonv1.GovernanceEnvelope, error) {
	if params.Posture == "" {
		return nil, fmt.Errorf("gateway: build envelope: %w", constants.ErrEnvelopePostureMissing)
	}
	if !params.Posture.Valid() {
		return nil, fmt.Errorf("gateway: build envelope: %w: %q", constants.ErrInvalidPosture, params.Posture)
	}
	if params.Doctrine == nil {
		return nil, fmt.Errorf("gateway: build envelope: %w", constants.ErrTxDoctrineMissing)
	}

	eventType := constants.EventType(params.EventType)
	actionType, err := constants.ValidateGovernedRequest(eventType)
	if err != nil {
		return nil, fmt.Errorf("gateway: build envelope: %w", err)
	}

	// L1 screening runs in every posture, before the L3 gate: decode the
	// typed payload and run doctrine validation.
	decoded, err := governance.DecodePayloadForAction(actionType, params.Payload)
	if err != nil {
		return nil, fmt.Errorf("gateway: build envelope: %w", constants.ErrTxPayloadDecodeFailed)
	}
	if decoded == nil {
		return nil, fmt.Errorf("gateway: build envelope: %w", constants.ErrTxPayloadDecoderMissing)
	}
	if violations := params.Doctrine.ValidatePayload(decoded); len(violations) > 0 {
		return nil, fmt.Errorf("gateway: build envelope: %w: %s", constants.ErrTxL1ValidationFailed, strings.Join(violations, ", "))
	}
	l1Validated := true

	// L3 gate: the Gateway cannot mint L3 human proofs. Reject a clean
	// mutation-classified action under postures that require L3 unless the
	// caller owns the human-approval wait that will supply the proof.
	if governance.L3Required(actionType, params.Posture) && !awaitingApproval {
		return nil, fmt.Errorf("gateway: build envelope: %w", constants.ErrTxL3ProofUnmintable)
	}

	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("gateway: build envelope: generate nonce: %w", err)
	}

	env := &commonv1.GovernanceEnvelope{
		ProtocolVersion:        govpkg.GovernanceProtocolVersionV2,
		Timestamp:              timestamppb.Now(),
		ExpiresAt:              timestamppb.New(time.Now().Add(EnvelopeExpiry)),
		SourceComponent:        commonv1.Component_COMPONENT_CLIENT,
		OperatorId:             params.OperatorID,
		OperatorSessionId:      params.OperatorSessionID,
		ActionType:             string(actionType),
		TargetResource:         params.TargetResource,
		EventType:              string(eventType),
		Payload:                params.Payload,
		StateMerkleRoot:        params.StateMerkleRoot,
		Nonce:                  hex.EncodeToString(nonce),
		RequestorUserId:        params.RequestorUserID,
		ActingAppId:            params.ActingAppID,
		CaseId:                 params.CaseID,
		InvestigationId:        params.InvestigationID,
		TaskId:                 params.TaskID,
		EvaluationRunId:        params.EvaluationRunID,
		EvaluationAssignmentId: params.EvaluationAssignmentID,
		WebSessionId:           params.WebSessionID,
		CliSessionId:           params.CliSessionID,
		Posture:                string(params.Posture),
		Governance: &commonv1.GovernanceMetadata{
			L1: &commonv1.L1Metadata{Validated: l1Validated},
		},
	}

	txHash, err := govpkg.GenerateMessageID(env)
	if err != nil {
		return nil, fmt.Errorf("gateway: build envelope: generate message ID: %w", err)
	}
	env.Id = txHash
	env.TransactionHash = txHash

	return env, nil
}

// DispatchRequest is the input to the command dispatch service.
type DispatchRequest struct {
	EvaluationRunID         string
	EvaluationAssignmentID  string
	TargetOperatorSessionID string
	EventType               string
	Payload                 []byte
	TargetResource          string
	RequestorUserID         string
	ActingAppID             string
	CaseID                  string
	InvestigationID         string
	TaskID                  string
	WebSessionID            string
	CliSessionID            string

	// Timeout is the explicit request deadline for this dispatch. Zero
	// applies DispatchTimeout. Inference dispatches set the provider-aware
	// RequestDeadline from the inference dispatch package so the wait
	// outlives an in-flight provider call.
	Timeout time.Duration

	// OnInferenceProgress receives bounded provider progress telemetry while
	// waiting for the authoritative terminal completion.
	OnInferenceProgress func(*operatorv1.InferenceProgressEvent) error

	// OnAdmitted runs once the envelope holds every admission proof (any L2
	// votes and L3 decision) and before it is published. It receives a decoded
	// copy of the exact admitted envelope and the recorded L2 vote inferences,
	// read-only. An error stops the dispatch before publish.
	OnAdmitted func(ctx context.Context, envelope *commonv1.GovernanceEnvelope, votes []*operatorv1.ConsensusVoteInference) error
}

// DispatchResult is the output of a successful command dispatch. Receipt
// always carries the executing Operator's verified final signed
// EventReceipt: its signature and persistence attestation were verified
// before Dispatch returned. For inference dispatches, InferenceResult carries
// the verified complete result and its digest was verified too.
type DispatchResult struct {
	ConsensusInferences []*operatorv1.ConsensusVoteInference
	RequestEnvelope     *commonv1.GovernanceEnvelope
	TransactionID       string
	ResultEnvelope      *commonv1.GovernanceEnvelope
	Receipt             *operatorv1.EventReceipt
	InferenceResult     *operatorv1.InferenceResult
}

// operatorSessionValidator resolves an operator session ID to the operator
// document. AuthService implements this; the interface makes the dispatch
// service's dependency on auth explicit and testable.
type operatorSessionValidator interface {
	ValidateOperatorSession(ctx context.Context, operatorSessionID string) (*operatorv1.OperatorDocument, error)
}

// L2ConsensusDeliberator sends an envelope to an L2 consensus service for
// deliberation and returns the envelope bytes with L2 votes populated. The
// gateway dispatch path calls this after envelope construction for actions
// L4 enforces L2 on (governance.L2Enforced: a posture that requires L2 signatures,
// consensus or notary, and a non-bootstrap action). When the deliberator is nil
// or L2 is not enforced for the action, deliberation is skipped and the envelope
// proceeds without L2 votes (failing closed at L4 verification where L2 is enforced).
type L2ConsensusDeliberator interface {
	Deliberate(ctx context.Context, envelopeBytes []byte) (*governance.ConsensusDeliberation, error)
}

// ConsensusMemberSigner signs with a Tribunal consensus member's sealed app
// signing key. The Gateway host owns the keys (internal/cli/serve implements
// it); a missing or unsealable key is constants.ErrConsensusMemberKeyUnavailable,
// never a plaintext fallback.
type ConsensusMemberSigner interface {
	Sign(memberAppID string, payload []byte) ([]byte, error)
	// HasKey reports whether memberAppID's sealed key is present and unseals here.
	HasKey(memberAppID string) bool
}

// admit runs req.OnAdmitted once wire holds every admission proof. The hook
// decodes its own copy of wire, so it cannot edit the authorized transaction.
func admit(ctx context.Context, req DispatchRequest, wire []byte, votes []*operatorv1.ConsensusVoteInference) error {
	if req.OnAdmitted == nil {
		return nil
	}
	admitted := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(wire, admitted); err != nil {
		return fmt.Errorf("dispatch: decode admitted envelope: %w", err)
	}
	if err := req.OnAdmitted(ctx, admitted, votes); err != nil {
		return fmt.Errorf("dispatch: admitted: %w", err)
	}
	return nil
}

// DispatchService constructs a GovernanceEnvelope, publishes it to an operator's
// cmd channel via the in-process WS broker, and correlates the operator's result
// published on the results channel back to the originating request.
//
// The gateway owns the state Merkle root and the governance posture. The
// dispatch service sets StateMerkleRoot to the gateway's current state root.
// Under DoctrinePosture, no L2 votes or L3 proofs
// are required for read-only commands. Where governance.L2Enforced holds for
// the action (consensus, notary, and not a bootstrap action), the dispatch
// service deliberates the constructed envelope through l2Deliberator before
// publish. Mutations under postures that require L3 proof (ratify, notary)
// are suspended through the DispatchApprovalCoordinator, which holds the
// request open for a real human decision. Without a coordinator they are
// rejected at envelope construction (ErrTxL3ProofUnmintable). The Gateway
// never mints human proofs.
type DispatchService struct {
	approvals         *DispatchApprovalCoordinator
	embeddedProcessor governance.EnvelopeProcessor
	logger            *slog.Logger
	pubsub            *GatewayWebSocketHandler
	stateRootProvider governance.StateRootProvider
	auth              operatorSessionValidator
	posture           constants.Posture
	doctrine          *governance.L1Doctrine
	l2Deliberator     L2ConsensusDeliberator
	signerStore       ReceiptSignerResolver
}

// NewDispatchService creates a DispatchService wired to the gateway's in-process
// pub/sub broker, state root provider, auth service, L1 doctrine, L2
// consensus deliberator, and receipt signer store. The posture is the
// gateway's governance posture, injected into every envelope built by this
// service so the operator reads it per-transaction at L4 verification time.
// The doctrine is the L1 validator the builder runs against the decoded
// typed payload. The l2Deliberator is invoked after envelope construction
// under postures that require L2 signatures; a nil deliberator skips
// deliberation (the envelope fails closed at L4 under such postures). The
// signerStore resolves the operator's actuator public key for inference
// completion receipt verification; a nil store fails inference dispatch
// closed.
func NewDispatchService(logger *slog.Logger, pubsubHandler *GatewayWebSocketHandler, stateRootProvider governance.StateRootProvider, auth operatorSessionValidator, posture constants.Posture, doctrine *governance.L1Doctrine, l2Deliberator L2ConsensusDeliberator, signerStore ReceiptSignerResolver, embeddedProcessor governance.EnvelopeProcessor, approvals *DispatchApprovalCoordinator) *DispatchService {
	return &DispatchService{
		approvals:         approvals,
		embeddedProcessor: embeddedProcessor,
		logger:            logger,
		pubsub:            pubsubHandler,
		stateRootProvider: stateRootProvider,
		auth:              auth,
		posture:           posture,
		doctrine:          doctrine,
		l2Deliberator:     l2Deliberator,
		signerStore:       signerStore,
	}
}

// Dispatch sends a signed command to the target operator and waits for the result.
// The envelope is constructed with the gateway's current state root, a unique
// nonce, and a near-future expiry. The result is correlated by transaction ID.
// Returns an error if the operator session is invalid, envelope construction
// fails, L2 deliberation fails, or the result does not arrive within DispatchTimeout.
func (d *DispatchService) Dispatch(ctx context.Context, req DispatchRequest) (result *DispatchResult, err error) {
	// 1. Resolve the target operator session.
	op, err := d.auth.ValidateOperatorSession(ctx, req.TargetOperatorSessionID)
	if err != nil {
		return nil, fmt.Errorf("dispatch: validate operator session: %w", err)
	}

	operatorID := op.Id
	operatorSessionID := op.OperatorSessionId

	actionType, err := constants.ValidateGovernedRequest(constants.EventType(req.EventType))
	if err != nil {
		return nil, fmt.Errorf("dispatch: %w", err)
	}

	if err := validateWitnessDispatch(op, string(actionType), req.Payload); err != nil {
		return nil, err
	}

	// 2. Fetch the gateway's current state root.
	stateRoot, err := d.stateRootProvider.GetCurrentStateRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatch: state root: %w: %w", constants.ErrIngressStateRootUnavailable, err)
	}

	// 3. Build the GovernanceEnvelope via the shared construction helper.
	// buildGovernanceEnvelope runs L1 screening and, under L3-requiring
	// postures, admits the unsigned mutation only when the approval coordinator
	// owns the wait (the Gateway never mints L3 proofs).
	env, err := buildGovernanceEnvelope(BuildEnvelopeParams{
		OperatorID:             operatorID,
		OperatorSessionID:      operatorSessionID,
		EventType:              req.EventType,
		Payload:                req.Payload,
		TargetResource:         req.TargetResource,
		RequestorUserID:        req.RequestorUserID,
		ActingAppID:            req.ActingAppID,
		StateMerkleRoot:        stateRoot,
		CaseID:                 req.CaseID,
		InvestigationID:        req.InvestigationID,
		TaskID:                 req.TaskID,
		EvaluationRunID:        req.EvaluationRunID,
		EvaluationAssignmentID: req.EvaluationAssignmentID,
		WebSessionID:           req.WebSessionID,
		CliSessionID:           req.CliSessionID,
		Posture:                d.posture,
		Doctrine:               d.doctrine,
	}, d.approvals != nil)
	if err != nil {
		return nil, fmt.Errorf("dispatch: %w", err)
	}
	// 4. Marshal as protojson (the canonical wire format).
	wire, err := protojson.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("dispatch: marshal envelope: %w", err)
	}

	var consensusInferences []*operatorv1.ConsensusVoteInference
	defer func() {
		if result != nil {
			result.ConsensusInferences = consensusInferences
			result.RequestEnvelope = env
		}
		err = governance.WithConsensusInferences(consensusInferences, err)
	}()
	// 5. Where L4 enforces L2 for this action (governance.L2Enforced),
	//    deliberate the envelope through the L2 consensus service before
	//    publish. The deliberator collects signed votes and returns the
	//    envelope with L2 metadata populated. A nil deliberator skips
	//    deliberation; the envelope fails closed at L4 verification where
	//    L2 is enforced. A deliberation error fails closed before publish.
	//    A Tribunal member's own vote inference is never L2-enforced
	//    (INFERENCE changes no state), so L2 cannot recurse into itself.
	if governance.L2Enforced(env, d.posture) && d.l2Deliberator != nil {
		deliberated, derr := d.l2Deliberator.Deliberate(ctx, wire)
		if derr != nil {
			return nil, fmt.Errorf("dispatch: l2 deliberation: %w: %w", constants.ErrIngressL2DeliberationFailed, derr)
		}
		wire = deliberated.Envelope
		consensusInferences = deliberated.Inferences
	}

	if governance.L3Required(actionType, d.posture) {
		// L2 deliberation may have attached proofs to wire; suspend those
		// exact admission facts rather than the earlier pre-deliberation value.
		if err := protojson.Unmarshal(wire, env); err != nil {
			return nil, fmt.Errorf("dispatch: decode deliberated envelope: %w", err)
		}
		resumed, pending, err := d.approvals.await(ctx, env)
		if err != nil {
			return nil, err
		}
		wire, err = protojson.Marshal(resumed)
		if err != nil {
			d.approvals.finish(env.Id, pending, nil, err)
			return nil, err
		}
		if err := admit(ctx, req, wire, consensusInferences); err != nil {
			d.approvals.finish(env.Id, pending, nil, err)
			return nil, err
		}
		ctx = context.WithValue(ctx, constants.ContextKeyStateMerkleRoot, resumed.GetStateMerkleRoot())
		result, err := d.dispatchEnvelope(ctx, op, resumed, wire, req)
		receipt := governance.FailedReceipt(err)
		if result != nil {
			receipt = result.Receipt
		}
		d.approvals.finish(env.Id, pending, receipt, err)
		return result, err
	}
	if err := admit(ctx, req, wire, consensusInferences); err != nil {
		return nil, err
	}
	return d.dispatchEnvelope(ctx, op, env, wire, req)
}

// ProcessEnvelope resumes a prebuilt envelope without rebuilding its identity,
// state snapshot, nonce, expiry, or proofs. It implements governance.EnvelopeProcessor
// for outbound suspension routing; delivery and result verification use the same
// wait loop as Dispatch. The executing Operator still performs L4/L5 verification.
func (d *DispatchService) ProcessEnvelope(ctx context.Context, wire []byte) (*operatorv1.EventReceipt, error) {
	env := &commonv1.GovernanceEnvelope{}
	if err := protojson.Unmarshal(wire, env); err != nil {
		return nil, fmt.Errorf("dispatch: decode resumed envelope: %w: %w", constants.ErrTxInvalidEnvelope, err)
	}
	op, err := d.auth.ValidateOperatorSession(ctx, env.GetOperatorSessionId())
	if err != nil {
		return nil, fmt.Errorf("dispatch: validate resumed operator session: %w", err)
	}
	if env.GetOperatorId() == "" || op.GetId() != env.GetOperatorId() {
		return nil, fmt.Errorf("dispatch: resume target: %w", constants.ErrTxTargetOperatorMismatch)
	}
	if env.GetOperatorSessionId() == "" || op.GetOperatorSessionId() != env.GetOperatorSessionId() {
		return nil, fmt.Errorf("dispatch: resume target: %w", constants.ErrTxTargetSessionMismatch)
	}
	if env.GetPosture() != string(d.posture) {
		return nil, fmt.Errorf("dispatch: resume posture: %w", constants.ErrGatewayPostureMismatch)
	}
	actionType, err := constants.ValidateGovernedRequest(constants.EventType(env.GetEventType()))
	if err != nil {
		return nil, fmt.Errorf("dispatch: resume event: %w", err)
	}
	if env.GetActionType() != string(actionType) {
		return nil, fmt.Errorf("dispatch: resume action: %w", constants.ErrTxEventActionMismatch)
	}
	if err := validateWitnessDispatch(op, env.GetActionType(), env.GetPayload()); err != nil {
		return nil, err
	}
	txHash, err := govpkg.GenerateMessageID(env)
	if err != nil {
		return nil, fmt.Errorf("dispatch: hash resumed envelope: %w", err)
	}
	if env.GetId() != txHash {
		return nil, fmt.Errorf("dispatch: resume identity: %w", constants.ErrTxTransactionIDMismatch)
	}
	if env.GetTransactionHash() != txHash {
		return nil, fmt.Errorf("dispatch: resume identity: %w", constants.ErrTxTransactionHashMismatch)
	}
	req := DispatchRequest{EventType: env.GetEventType()}
	if actionType == constants.ActionTypeInference {
		req.Timeout = inferencedispatch.RequestDeadline
	}
	result, err := d.dispatchEnvelope(ctx, op, env, wire, req)
	if err != nil {
		return governance.FailedReceipt(err), err
	}
	return result.Receipt, nil
}

// dispatchEnvelope owns delivery and verified result collection for both new
// dispatches and resumed envelopes. Its callers supply the already-admitted
// wire bytes and resolved execution target.
func (d *DispatchService) dispatchEnvelope(ctx context.Context, op *operatorv1.OperatorDocument, env *commonv1.GovernanceEnvelope, wire []byte, req DispatchRequest) (*DispatchResult, error) {
	operatorID, operatorSessionID := op.GetId(), op.GetOperatorSessionId()
	txHash := env.GetId()
	actionType := constants.ActionType(env.GetActionType())

	// 6. Register an in-process handler on the operator's results channel to
	//    correlate the result by transaction ID.
	deadline := req.Timeout
	if deadline <= 0 {
		deadline = DispatchTimeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	dispatchContextError := func() error {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return fmt.Errorf("dispatch: %w: %w", constants.ErrInferenceCanceled, err)
			}
			return fmt.Errorf("dispatch: %w", err)
		}
		return fmt.Errorf("dispatch: %w after %s (transaction %s)", constants.ErrDispatchResultTimeout, deadline, txHash)
	}
	isEmbedded := constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded
	resultsChannel := pubsub.ResultsChannel(operatorID, operatorSessionID)
	resultBuffer := 1
	if req.OnInferenceProgress != nil {
		resultBuffer = InferenceProgressResultBuffer
	}
	resultCh := make(chan *commonv1.GovernanceEnvelope, resultBuffer)
	overflow := make(chan error, 1)

	router := &dispatchResultRouter{
		ctx:        timeoutCtx,
		logger:     d.logger,
		txHash:     txHash,
		eventType:  req.EventType,
		isEmbedded: isEmbedded,
		progress:   req.OnInferenceProgress != nil,
		resultCh:   resultCh,
		overflow:   overflow,
	}
	unregister := d.pubsub.RegisterHandler(resultsChannel, router.handle)
	defer unregister()

	// An L4 rejection publishes only a signed FAILED receipt, never a result,
	// so the wait also ends on the Operator's verified GOVERNANCE_REJECTED
	// receipt for this transaction. Every other non-inference dispatch also
	// waits for the verified final receipt (COMPLETED, or FAILED at L5), which
	// the executing Operator publishes on the same channel after its result.
	// MCP_CALL and observation commands complete on that receipt alone (see
	// completed below).
	// Inference maps rejections and its final receipt from its own signed
	// completion instead.
	rejectedCh := make(chan *operatorv1.EventReceipt, 1)
	finalCh := make(chan *operatorv1.EventReceipt, 1)
	if actionType != constants.ActionTypeInference {
		unregisterReceipts := d.pubsub.RegisterHandler(pubsub.ReceiptsChannel(operatorID, operatorSessionID), func(_ string, data []byte) {
			receipt, kind := d.verifiedTransactionEventReceipt(timeoutCtx, receiptFromReceiptsMessage(data, operatorID, operatorSessionID), operatorID, txHash)
			var out chan *operatorv1.EventReceipt
			switch kind {
			case transactionEventReceiptRejected:
				out = rejectedCh
			case transactionEventReceiptFinal:
				out = finalCh
			default:
				return
			}
			select {
			case out <- receipt:
			default:
			}
		})
		defer unregisterReceipts()
	}

	// 7. Publish the envelope to the operator's cmd channel. Zero delivery
	//    is a terminal transport failure: no operator received the command,
	//    so no result can ever arrive.
	cmdChannel := pubsub.CmdChannel(operatorID, operatorSessionID)
	if timeoutCtx.Err() != nil {
		return nil, dispatchContextError()
	}
	var delivered int
	var embeddedDone chan embeddedOutcome
	var finalReceipt *operatorv1.EventReceipt
	if isEmbedded {
		// The embedded Operator runs in-process and emits results while
		// ProcessEnvelope is still executing, so it must run beside the wait
		// loop below: draining concurrently is what keeps long inference
		// progress streams from overflowing the result buffer.
		if d.embeddedProcessor == nil {
			return nil, fmt.Errorf("dispatch embedded operator: %w", constants.ErrDispatchNoDelivery)
		}
		embeddedDone = make(chan embeddedOutcome, 1)
		defer func() {
			cancel()
			if embeddedDone != nil {
				<-embeddedDone
			}
		}()
		go func() {
			receipt, perr := d.embeddedProcessor.ProcessEnvelope(timeoutCtx, wire)
			if executionFailedReceipt(receipt, txHash) {
				// The handler ran and published its typed failure result,
				// which is the outcome, exactly as from an outbound Operator.
				perr = nil
			}
			embeddedDone <- embeddedOutcome{receipt: receipt, err: perr}
		}()
		delivered = 1
	} else {
		delivered = d.pubsub.Publish(cmdChannel, wire)
	}
	// settleEmbedded takes the embedded Operator's outcome. A processing
	// failure ends the dispatch before any final result is returned, so an
	// embedded command refused or broken before or around its handler is never
	// reported as a result. A handler's own failure is not a processing failure
	// (executionFailedReceipt): its typed result is returned. For a
	// non-inference dispatch the receipt ProcessEnvelope returns is verified by
	// the same verifier as a receipts-channel receipt.
	settleEmbedded := func(outcome embeddedOutcome) error {
		embeddedDone = nil
		if timeoutCtx.Err() != nil {
			return dispatchContextError()
		}
		if outcome.err != nil {
			return fmt.Errorf("dispatch embedded operator: %w", outcome.err)
		}
		if actionType != constants.ActionTypeInference {
			if receipt, kind := d.verifiedTransactionEventReceipt(timeoutCtx, outcome.receipt, operatorID, txHash); kind == transactionEventReceiptFinal {
				finalReceipt = receipt
			}
		}
		return nil
	}
	// awaitEmbedded waits for the embedded Operator's outcome before an
	// inference result is verified.
	awaitEmbedded := func() error {
		if embeddedDone == nil {
			return nil
		}
		select {
		case outcome := <-embeddedDone:
			return settleEmbedded(outcome)
		case <-timeoutCtx.Done():
			return dispatchContextError()
		}
	}
	d.logger.Info("dispatch: published command",
		"transaction_id", txHash,
		"cmd_channel", cmdChannel,
		"results_channel", resultsChannel,
		"delivered", delivered)
	if delivered == 0 {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrDispatchNoDelivery)
	}

	// 8. Wait for the result with the request deadline. The caller sets
	//    Timeout explicitly (inference sets its provider-aware deadline);
	//    zero applies DispatchTimeout.
	var progressEvents []*operatorv1.InferenceProgressEvent
	// A non-inference dispatch succeeds only once it holds both the terminal
	// result envelope and the verified final receipt, in either order. An
	// MCP_CALL is the exception: its tool result is stamped into the signed
	// final receipt (ResultSummary) and no result envelope is published, so the
	// verified final receipt alone is its terminal outcome. Observation commands
	// are the same: a BEGIN publishes no result, and a FINALIZE's window is
	// ingested by its coordinator, so their final receipt is the outcome.
	var terminalResult *commonv1.GovernanceEnvelope
	receiptIsOutcome := actionType == constants.ActionTypeMcpCall || isObservationAction(actionType)
	completed := func() *DispatchResult {
		if finalReceipt == nil || (terminalResult == nil && !receiptIsOutcome) {
			return nil
		}
		return &DispatchResult{
			TransactionID:  txHash,
			ResultEnvelope: terminalResult,
			Receipt:        finalReceipt,
		}
	}
	for {
		select {
		case overflowErr := <-overflow:
			return nil, fmt.Errorf("dispatch: %w", overflowErr)
		case outcome := <-embeddedDone:
			if err := settleEmbedded(outcome); err != nil {
				return nil, err
			}
			if result := completed(); result != nil {
				return result, nil
			}
		case resultEnv := <-resultCh:
			if actionType == constants.ActionTypeInference {
				if progress, ok := decodeInferenceProgressEnvelope(resultEnv); ok {
					if req.OnInferenceProgress != nil {
						if err := req.OnInferenceProgress(progress); err != nil {
							return nil, fmt.Errorf("dispatch: %w", err)
						}
						progressEvents = append(progressEvents, progress)
					}
					continue
				}
				if err := awaitEmbedded(); err != nil {
					return nil, err
				}
				result, err := d.verifyInferenceCompletion(ctx, env, resultEnv)
				if err != nil {
					return nil, err
				}
				if req.OnInferenceProgress != nil {
					if err := models.ReconcileInferenceProgress(progressEvents, result.InferenceResult); err != nil {
						return nil, fmt.Errorf("dispatch: %w", err)
					}
				}
				return result, nil
			}
			if isShellCommandDispatch(req.EventType) {
				if payload := operatorCommandResultPayload(resultEnv); len(payload) > 0 {
					resultEnv.Payload = payload
				}
			}
			if terminalResult == nil {
				terminalResult = resultEnv
			}
			if result := completed(); result != nil {
				return result, nil
			}
		case receipt := <-finalCh:
			finalReceipt = receipt
			if result := completed(); result != nil {
				return result, nil
			}
		case receipt := <-rejectedCh:
			return nil, governance.WithRejectionReceipt(receipt, fmt.Errorf("dispatch: %w (transaction %s): %s", constants.ErrDispatchGovernanceRejected, txHash, receipt.GetResultSummary()))
		case <-timeoutCtx.Done():
			return nil, dispatchContextError()
		}
	}
}

// embeddedOutcome is what the in-process ProcessEnvelope call hands back to the
// dispatch wait loop: its final receipt and its processing error.
type embeddedOutcome struct {
	receipt *operatorv1.EventReceipt
	err     error
}

// dispatchResultRouter forwards one dispatch's result envelopes from the
// Operator's results channel to the waiting Dispatch call.
type dispatchResultRouter struct {
	ctx        context.Context
	logger     *slog.Logger
	txHash     string
	eventType  string
	isEmbedded bool
	progress   bool
	resultCh   chan *commonv1.GovernanceEnvelope
	overflow   chan error
}

func (r *dispatchResultRouter) handle(_ string, data []byte) {
	resultEnv := &commonv1.GovernanceEnvelope{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, resultEnv); err != nil {
		r.logger.Warn("dispatch: failed to unmarshal result envelope", logging.ErrorKind(err))
		return
	}
	if resultEnv.Id != r.txHash {
		return
	}
	if isShellCommandDispatch(r.eventType) && !isOperatorCommandTerminalResult(resultEnv) {
		return
	}
	if err := constants.ValidateGovernedResultEnvelope(
		constants.EventType(r.eventType),
		constants.EventType(resultEnv.GetEventType()),
		constants.ActionType(resultEnv.GetActionType()),
	); err != nil {
		r.logger.Warn("dispatch: reject result envelope", logging.ErrorKind(err), "transaction_id", r.txHash)
		return
	}
	if r.isEmbedded {
		// Publish invokes handlers synchronously. Waiting here bounds
		// the queue and slows the local producer to the consumer's pace,
		// without depending on goroutine scheduling to avoid overflow.
		select {
		case r.resultCh <- resultEnv:
		case <-r.ctx.Done():
		}
		return
	}
	select {
	case r.resultCh <- resultEnv:
	default:
		if r.progress {
			select {
			case r.overflow <- constants.ErrInferenceProgressBackpressure:
			default:
			}
			r.logger.Warn("dispatch: inference progress channel full, failing closed",
				"transaction_id", r.txHash,
				"event_type", resultEnv.GetEventType())
			return
		}
		r.logger.Warn("dispatch: result channel full, dropping event",
			"transaction_id", r.txHash,
			"event_type", resultEnv.GetEventType())
	}
}

// transactionEventReceiptKind classifies a verified receipt for one transaction.
type transactionEventReceiptKind int

const (
	// transactionEventReceiptNone is a receipt the dispatch does not wait on, or one
	// that failed verification. It never ends the wait.
	transactionEventReceiptNone transactionEventReceiptKind = iota
	// transactionEventReceiptRejected is the Operator's signed L4 GOVERNANCE_REJECTED
	// receipt. It ends the wait as a rejection.
	transactionEventReceiptRejected
	// transactionEventReceiptFinal is the Operator's signed final receipt: COMPLETED,
	// or FAILED at L5 execution. Success needs it alongside the terminal result.
	transactionEventReceiptFinal
)

// executionFailedReceipt reports whether a receipt is an executed handler's own
// failure: the final receipt for txHash is FAILED, is not a governance
// rejection, and records a failed L5 execution stage. The handler publishes its
// typed failure result before its receipt, so the dispatch reports that result,
// as it does for an outbound Operator, instead of turning an ordinary execution
// failure (a missing file, a non-zero exit) into a transport error. Every
// refusal before execution still fails the dispatch.
func executionFailedReceipt(receipt *operatorv1.EventReceipt, txHash string) bool {
	if receipt == nil || receipt.GetTransactionId() != txHash ||
		receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED ||
		receipt.GetFailureCode() == operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED {
		return false
	}
	for _, stage := range receipt.GetDeterministicStageEvidence() {
		if stage.GetKind() == operatorv1.DeterministicStageKind_DETERMINISTIC_STAGE_KIND_L5_EXECUTION {
			return stage.GetOutcome() == operatorv1.DeterministicStageOutcome_DETERMINISTIC_STAGE_OUTCOME_FAILED
		}
	}
	return false
}

// receiptFromReceiptsMessage decodes a receipts-channel message into the
// EventReceipt it carries. A message that is not a receipt recorded by the
// target Operator session returns nil. Decoding alone never ends the wait:
// verifiedTransactionEventReceipt must accept the receipt first.
func receiptFromReceiptsMessage(data []byte, operatorID, operatorSessionID string) *operatorv1.EventReceipt {
	env := &commonv1.GovernanceEnvelope{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, env); err != nil ||
		env.GetEventType() != string(constants.Event.Operator.Receipt.Recorded) ||
		env.GetOperatorId() != operatorID || env.GetOperatorSessionId() != operatorSessionID {
		return nil
	}
	receipt := &operatorv1.EventReceipt{}
	if err := proto.Unmarshal(env.GetPayload(), receipt); err != nil {
		return nil
	}
	return receipt
}

// verifiedTransactionEventReceipt verifies a receipt the executing Operator signed
// for txHash and classifies it. It is the one verifier for both the
// receipts-channel and the embedded paths. The receipt must be for txHash, must
// be a rejection or a final receipt, and must verify with the signer bound to
// operatorID: ReceiptSignerKey, then the receipt signature, then the persistence
// attestation. Anything else returns transactionEventReceiptNone, so an unverifiable
// receipt never ends the wait.
func (d *DispatchService) verifiedTransactionEventReceipt(ctx context.Context, receipt *operatorv1.EventReceipt, operatorID, txHash string) (*operatorv1.EventReceipt, transactionEventReceiptKind) {
	if d.signerStore == nil || receipt == nil || receipt.GetTransactionId() != txHash || receipt.GetTransactionHash() != txHash {
		return nil, transactionEventReceiptNone
	}
	var kind transactionEventReceiptKind
	switch {
	case governance.IsGovernanceRejectionReceipt(receipt):
		kind = transactionEventReceiptRejected
	case receipt.GetStatus() == operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED || executionFailedReceipt(receipt, txHash):
		kind = transactionEventReceiptFinal
	default:
		return nil, transactionEventReceiptNone
	}
	if err := d.verifyBoundReceipt(ctx, operatorID, receipt); err != nil {
		d.logger.Warn("dispatch: receipt not verified against the executing operator's bound key", "transaction_id", txHash, "signer_key_id", receipt.GetSignerKeyId(), logging.ErrorKind(err))
		return nil, transactionEventReceiptNone
	}
	return receipt, kind
}

// verifyBoundReceipt verifies a receipt's signature and persistence attestation
// against the Actuator key bound to the executing Operator (INV-GW-GOV-06).
func (d *DispatchService) verifyBoundReceipt(ctx context.Context, operatorID string, receipt *operatorv1.EventReceipt) error {
	pubKey, err := d.signerStore.ReceiptSignerKey(ctx, operatorID, receipt)
	if err != nil {
		return fmt.Errorf("resolve signer: %w", err)
	}
	if err := governance.VerifyEventReceiptSignature(receipt, pubKey); err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	if err := governance.VerifyReceiptPersistenceAttestation(receipt, pubKey); err != nil {
		return fmt.Errorf("persistence attestation: %w", err)
	}
	return nil
}

// commandDeliverable reports whether a command for the Operator can currently
// be delivered: the embedded Operator through its in-process processor, any
// other Operator through a cmd-channel subscriber on the broker.
func (d *DispatchService) commandDeliverable(operatorID, cmdChannel string, broker *GatewayWebSocketHandler) bool {
	if operatorID == string(constants.DocIDEmbeddedOperator) {
		return d != nil && d.embeddedProcessor != nil
	}
	return broker != nil && broker.ChannelSubscriberCount(cmdChannel) > 0
}

func decodeInferenceProgressEnvelope(env *commonv1.GovernanceEnvelope) (*operatorv1.InferenceProgressEvent, bool) {
	if env == nil || env.GetEventType() != string(constants.Event.Operator.Inference.ProgressUpdated) {
		return nil, false
	}
	progress := &operatorv1.InferenceProgressEvent{}
	if err := proto.Unmarshal(env.GetPayload(), progress); err != nil {
		return nil, false
	}
	return progress, true
}

// verifyInferenceCompletion decodes the protocol-owned InferenceCompletion
// carried in the result envelope's payload and verifies that the returned
// result and its final signed receipt are one outcome: the receipt's
// transaction identity must match the dispatched envelope, its signature
// and persistence attestation must verify against the operator's actuator
// key, a FAILED receipt terminates the wait as a typed failure, and the
// recomputed result digest must equal both the receipt's result_summary and
// the result's own result_digest. Fail-closed on every check.
func (d *DispatchService) verifyInferenceCompletion(ctx context.Context, cmdEnv, resultEnv *commonv1.GovernanceEnvelope) (*DispatchResult, error) {
	completion := &operatorv1.InferenceCompletion{}
	if err := proto.Unmarshal(resultEnv.Payload, completion); err != nil {
		return nil, fmt.Errorf("dispatch: %w: %w", constants.ErrInferenceResultDecode, err)
	}

	receipt := completion.GetReceipt()
	if receipt == nil {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceCompletionNoReceipt)
	}
	if receipt.TransactionId != cmdEnv.Id || receipt.TransactionHash != cmdEnv.TransactionHash {
		return nil, fmt.Errorf("dispatch: %w: receipt transaction identity does not match dispatched envelope", constants.ErrInferenceReceiptVerify)
	}

	if d.signerStore == nil {
		return nil, fmt.Errorf("dispatch: %w: receipt signer store not configured", constants.ErrInferenceReceiptVerify)
	}
	if err := d.verifyBoundReceipt(ctx, cmdEnv.GetOperatorId(), receipt); err != nil {
		return nil, fmt.Errorf("dispatch: %w: %w", constants.ErrInferenceReceiptVerify, err)
	}

	if receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		// The verified FAILED receipt rides with the error: its commitment is
		// on the Operator's ledger, so evidence verification must be able to
		// claim it.
		return nil, governance.WithFailedReceipt(receipt, fmt.Errorf("dispatch: %w: %s", inferenceReceiptFailureError(receipt), receipt.ResultSummary))
	}

	result := completion.GetResult()
	if result == nil {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceCompletionNoResult)
	}
	digest, err := models.ComputeInferenceResultDigest(result)
	if err != nil {
		return nil, fmt.Errorf("dispatch: %w", err)
	}
	if result.ResultDigest != digest || receipt.ResultSummary != digest {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceResultDigestMismatch)
	}
	request := &operatorv1.InferenceRequested{}
	if err := proto.Unmarshal(cmdEnv.Payload, request); err != nil {
		return nil, fmt.Errorf("dispatch: %w: %w", constants.ErrInferenceResultDecode, err)
	}
	if err := models.ValidateDecisionCompletion(request.GetDecision(), result); err != nil {
		return nil, fmt.Errorf("dispatch: %w", err)
	}
	if result.GetProviderAttemptId() != request.GetProviderAttemptId() ||
		(request.GetModel() != "" && result.GetRequestedModel() != request.GetModel()) ||
		result.GetRequestedModelDigest() != request.GetModelDigest() ||
		result.GetCampaignId() != request.GetCampaignId() ||
		result.GetRunId() != request.GetRunId() ||
		result.GetAssignmentId() != request.GetAssignmentId() ||
		result.GetEvaluationAttemptId() != request.GetEvaluationAttemptId() ||
		result.GetScenarioId() != request.GetScenarioId() ||
		result.GetModelRegistryDigest() != request.GetModelRegistryDigest() ||
		result.GetRetryCount() != request.GetRetryCount() ||
		result.GetRetryClassification() != models.ClassifyRetry(request.GetRetryCount()) ||
		result.GetLoadState() != models.ClassifyLoadState(result.LoadDurationNs) {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceIdentityMismatch)
	}
	if request.GetProviderAttemptId() == "" || !models.IsSHA256Hex(result.GetNormalizedRequestHash()) || !models.IsSHA256Hex(result.GetOutputHash()) {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceEvidenceHashInvalid)
	}
	if request.GetModelDigest() != "" && result.GetServedModelDigest() != request.GetModelDigest() {
		return nil, fmt.Errorf("dispatch: %w", constants.ErrInferenceModelDigestMismatch)
	}

	return &DispatchResult{
		TransactionID:   cmdEnv.Id,
		ResultEnvelope:  resultEnv,
		Receipt:         receipt,
		InferenceResult: result,
	}, nil
}

// inferenceReceiptFailureError maps a signed FAILED receipt's typed
// failure_code back to the corresponding sentinel so callers can classify
// governance rejections, client faults, and provider failures via errors.Is
// without parsing the result_summary text. The receipt signature is verified
// before this runs, so the code is authentic. An unspecified or unknown code
// falls back to the generic execution-failure sentinel.
func inferenceReceiptFailureError(receipt *operatorv1.EventReceipt) error {
	switch receipt.FailureCode {
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_DECISION_REQUEST_INVALID:
		return constants.ErrInferenceDecisionRequestInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_DECISION_RESPONSE_INVALID:
		return constants.ErrInferenceDecisionResponseInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_DECISION_UNSUPPORTED:
		return constants.ErrInferenceDecisionUnsupported
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_HOSTED_NOT_ALLOWED:
		return constants.ErrInferenceHostedNotAllowed
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED:
		return constants.ErrInferenceGovernanceRejected
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED:
		return constants.ErrInferenceModelOverrideDenied
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_ROLE_INVALID:
		return constants.ErrInferenceRoleInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REF_INVALID:
		return constants.ErrInferenceModelRefInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE:
		return constants.ErrInferenceBackendUnavailable
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT:
		return constants.ErrInferenceBackendTimeout
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATE_FAILED:
		return constants.ErrInferenceGenerateFailed
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND:
		return constants.ErrInferenceModelNotFound
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID:
		return constants.ErrInferenceProviderResponseInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_GENERATION_OPTIONS_INVALID:
		return constants.ErrInferenceGenerationOptionsInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAPABILITY_UNSUPPORTED:
		return constants.ErrInferenceCapabilityUnsupported
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_TOOLS_UNSUPPORTED:
		return constants.ErrInferenceToolsUnsupported
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW:
		return constants.ErrInferenceContextOverflow
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_PROVIDER_ATTEMPT_REQUIRED:
		return constants.ErrInferenceProviderAttemptRequired
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_IDENTITY_MISMATCH:
		return constants.ErrInferenceIdentityMismatch
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_DIGEST_MISMATCH:
		return constants.ErrInferenceModelDigestMismatch
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_EVIDENCE_HASH_INVALID:
		return constants.ErrInferenceEvidenceHashInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_MODEL_REGISTRY_INVALID:
		return constants.ErrInferenceModelRegistryInvalid
	case operatorv1.ReceiptFailureCode_RECEIPT_FAILURE_CODE_CAMPAIGN_BINDING_INVALID:
		return constants.ErrInferenceCampaignBindingInvalid
	default:
		return constants.ErrInferenceReceiptFailed
	}
}

// GovernedDispatchResult builds the 200 body for a dispatch. The receipt is the
// executing Operator's verified final EventReceipt for every event type, and
// result_payload is the terminal operator result (for example FsReadResult).
func (r *DispatchResult) GovernedDispatchResult() *operatorv1.GovernedDispatchResult {
	out := &operatorv1.GovernedDispatchResult{
		TransactionId:       r.TransactionID,
		InferenceResult:     r.InferenceResult,
		Receipt:             r.Receipt,
		ConsensusInferences: r.ConsensusInferences,
		RequestEnvelope:     r.RequestEnvelope,
	}
	if r.ResultEnvelope != nil {
		out.EventType = r.ResultEnvelope.GetEventType()
		out.ActionType = r.ResultEnvelope.GetActionType()
		if isOperatorCommandTerminalResult(r.ResultEnvelope) {
			out.ResultPayload = operatorCommandResultPayload(r.ResultEnvelope)
		} else {
			out.ResultPayload = r.ResultEnvelope.GetPayload()
		}
	}
	return out
}

func isShellCommandDispatch(eventType string) bool {
	return eventType == string(constants.Event.Operator.Command.Requested)
}

func isOperatorCommandTerminalResult(env *commonv1.GovernanceEnvelope) bool {
	if env == nil {
		return false
	}
	switch env.GetEventType() {
	case string(constants.Event.Operator.Command.Completed),
		string(constants.Event.Operator.Command.Failed),
		string(constants.Event.Operator.Command.Cancelled),
		string(constants.Event.Operator.Command.CancelFailed):
		return true
	default:
		return false
	}
}

func operatorCommandResultPayload(env *commonv1.GovernanceEnvelope) []byte {
	if env == nil {
		return nil
	}
	if len(env.GetPayload()) > 0 {
		return env.GetPayload()
	}
	if env.GetIntentData() == nil {
		return nil
	}
	wire, err := protojson.Marshal(env.GetIntentData())
	if err != nil {
		return nil
	}
	result := &operatorv1.CommandResult{}
	if err := protojson.Unmarshal(wire, result); err != nil {
		return nil
	}
	out, err := proto.Marshal(result)
	if err != nil {
		return nil
	}
	return out
}

// DispatchControllerDeps groups all dependencies for DispatchController.
type DispatchControllerDeps struct {
	DispatchSvc *DispatchService
	Responder   *response.Writer
	Logger      *slog.Logger
}

// DispatchController handles POST /api/v1/operators/commands, the mTLS-protected
// entry point for dispatching signed commands to operators.
type DispatchController struct {
	dispatchSvc *DispatchService
	responder   *response.Writer
	logger      *slog.Logger
}

// newDispatchController creates a DispatchController from its deps.
func newDispatchController(d DispatchControllerDeps) *DispatchController {
	return &DispatchController{
		dispatchSvc: d.DispatchSvc,
		responder:   d.Responder,
		logger:      d.Logger,
	}
}

// HandleDispatch is the HTTP handler for POST /api/v1/operators/commands.
//
// A 200 is the GovernedDispatchResult: the terminal result and the executing
// Operator's verified final EventReceipt. Failures are a GovernanceRejection
// carrying the signed L4 receipt, or an IngressRefusal before envelope
// construction completes.
//
// @Summary		Dispatch governed operator command
// @Description	Constructs a governed envelope for the target Operator session, waits for the
// @Description	terminal result and the Operator's verified final receipt, and returns both.
// @Description	Under ratify or notary posture a mutation is suspended and the request stays open
// @Description	until a human approves or denies it, up to the approval TTL, in addition to the
// @Description	dispatch deadline; clients allow both. The Gateway never mints the approval proof.
// @Tags			operators
// @Accept			json
// @Produce		json
// @Param			request	body		models.OperatorCommandRequest	true	"Dispatch request (JSON)"
// @Success		200		{object}	operatorv1.GovernedDispatchResult	"Terminal result with the verified final EventReceipt"
// @Failure		400		{object}	operatorv1.IngressRefusal	"Bad Request — malformed request or unknown event type"
// @Failure		403		{object}	operatorv1.GovernanceRejection	"Forbidden — GovernanceRejection carrying the signed L4 receipt; IngressRefusal when refused before L4"
// @Failure		404		{object}	operatorv1.IngressRefusal	"Not Found — target operator session does not resolve"
// @Failure		405		{object}	operatorv1.IngressRefusal	"Method Not Allowed"
// @Failure		422		{object}	operatorv1.IngressRefusal	"Unprocessable Entity — target cannot run the command"
// @Failure		503		{object}	operatorv1.GovernanceRejection	"Service Unavailable — GovernanceRejection or IngressRefusal, per the failing check"
// @Failure		504		{object}	operatorv1.IngressRefusal	"Gateway Timeout — no terminal result and receipt before the deadline"
// @Router			/api/v1/operators/commands [post]
func (c *DispatchController) HandleDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.ProtoJSONCanonical(w, http.StatusMethodNotAllowed, governance.NewIngressRefusal(constants.ErrMethodNotAllowed))
		return
	}

	var req models.OperatorCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		err = fmt.Errorf("dispatch: decode request: %w: %w", constants.ErrInvalidJSONBody, err)
		c.responder.ProtoJSONCanonical(w, http.StatusBadRequest, governance.NewIngressRefusal(err))
		return
	}

	if err := req.Validate(); err != nil {
		c.responder.ProtoJSONCanonical(w, http.StatusBadRequest, governance.NewIngressRefusal(err))
		return
	}

	// Extract the requestor's user ID from the mTLS identity context.
	requestorUserID := serviceauth.RequestorUserID(r.Context())
	actingAppID, _ := r.Context().Value(constants.ContextKeyAppID).(string)
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.logger.Warn("dispatch: clear response deadline", logging.ErrorKind(err))
	}

	result, err := c.dispatchSvc.Dispatch(r.Context(), DispatchRequest{
		TargetOperatorSessionID: req.TargetOperatorSessionID,
		EventType:               req.EventType,
		Payload:                 req.Payload,
		TargetResource:          req.TargetResource,
		RequestorUserID:         requestorUserID,
		ActingAppID:             actingAppID,
		CaseID:                  req.CaseID,
		InvestigationID:         req.InvestigationID,
		TaskID:                  req.TaskID,
		EvaluationRunID:         req.EvaluationRunID,
		EvaluationAssignmentID:  req.EvaluationAssignmentID,
		WebSessionID:            req.WebSessionID,
		CliSessionID:            req.CLISessionID,
	})
	if err != nil {
		// Dispatch errors can embed command lines, paths and doctrine violations. The
		// caller receives the signed rejection receipt or the typed sentinel, never
		// the wrapped chain.
		c.logger.Error("dispatch: command dispatch failed", logging.ErrorKind(err))
		status, refusal := dispatchErrorCode(err)
		if rejection := governance.RejectionResponse(err); rejection != nil {
			c.responder.ProtoJSONCanonical(w, status, rejection)
			return
		}
		c.responder.ProtoJSONCanonical(w, status, refusal)
		return
	}

	c.responder.ProtoJSONCanonical(w, http.StatusOK, result.GovernedDispatchResult())
}

// dispatchErrorCode maps a dispatch failure to an HTTP status and the
// IngressRefusal a response renders: a code and the typed sentinel, never the
// wrapped chain. A target operator session that does not resolve (AuthError
// from ValidateOperatorSession) is 404 with its message: the caller's own
// credentials were already accepted, so 401 would misreport it. Governance
// rejections, from Gateway-side envelope construction or the Operator's signed
// L4 receipt, are 403 like direct envelope submission. Anything else follows
// envelopeErrorCode, so an unclassified fault is 500 with constants.ErrInternal
// and UNCLASSIFIED.
func dispatchErrorCode(err error) (status int, refusal *operatorv1.IngressRefusal) {
	defer func() {
		if refusal != nil {
			refusal.ConsensusInferences = governance.ConsensusInferences(err)
		}
	}()
	var authErr *AuthError
	if errors.As(err, &authErr) {
		return http.StatusNotFound, &operatorv1.IngressRefusal{
			Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_OPERATOR_SESSION_NOT_FOUND,
			Error: authErr.Message,
		}
	}
	if errors.Is(err, constants.ErrWitnessCommandNotCapable) {
		return http.StatusUnprocessableEntity, governance.NewIngressRefusal(err)
	}
	// These fail after the envelope exists. Their statuses are kept, and they
	// carry no refusal code: the conformance suite grades them as failed cases.
	for _, entry := range []struct {
		sentinel error
		status   int
	}{
		{constants.ErrDispatchGovernanceRejected, http.StatusForbidden},
		{constants.ErrDispatchResultTimeout, http.StatusGatewayTimeout},
		{constants.ErrDispatchNoDelivery, http.StatusServiceUnavailable},
	} {
		if errors.Is(err, entry.sentinel) {
			return entry.status, &operatorv1.IngressRefusal{
				Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_UNCLASSIFIED,
				Error: entry.sentinel.Error(),
			}
		}
	}
	// The wrapper sentinel names the check, so its code is rendered directly
	// rather than read from the first classifier entry the chain matches.
	if errors.Is(err, constants.ErrIngressStateRootUnavailable) {
		return http.StatusServiceUnavailable, &operatorv1.IngressRefusal{
			Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_STATE_ROOT_UNAVAILABLE,
			Error: constants.ErrIngressStateRootUnavailable.Error(),
		}
	}
	if errors.Is(err, constants.ErrIngressL2DeliberationFailed) {
		status := http.StatusServiceUnavailable
		for _, outcome := range dispatchL2OutcomeErrors {
			if errors.Is(err, outcome) {
				status = http.StatusForbidden
				break
			}
		}
		return status, &operatorv1.IngressRefusal{
			Code:  operatorv1.IngressRefusalCode_INGRESS_REFUSAL_CODE_L2_DELIBERATION_FAILED,
			Error: constants.ErrIngressL2DeliberationFailed.Error(),
		}
	}
	status, _ = envelopeErrorCode(err)
	return status, governance.NewIngressRefusal(err)
}

// dispatchL2OutcomeErrors are the L2 vote and quorum outcomes. Deliberation
// that reaches one is a refused envelope (403). Any other deliberation failure,
// including a consensus or signer store that is not configured, is a server
// condition (503), matching VERIFIER_MISCONFIGURED at L4.
var dispatchL2OutcomeErrors = []error{
	constants.ErrTxL2SignatureMissing,
	constants.ErrTxL2SignatureInvalid,
	constants.ErrTxL2QuorumNotMet,
	constants.ErrTxL2DuplicateSigner,
	constants.ErrTxL2ConsensusSetUnknown,
}

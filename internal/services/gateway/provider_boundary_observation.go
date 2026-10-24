// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/provider_observer"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/services/pubsub"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// providerBoundaryOperatorLister resolves operators for campaign gateway
// infrastructure. Observer operators are enrolled under the gateway owner,
// not the per-request app identity on inference dispatch.
type providerBoundaryOperatorLister interface {
	ListOperatorsForObservation(ctx context.Context) ([]*operatorv1.OperatorDocument, error)
}

type registrationOwnerOperatorLister struct {
	reg     *RegistrationService
	userSvc *UserService
}

func (l *registrationOwnerOperatorLister) ListOperatorsForObservation(ctx context.Context) ([]*operatorv1.OperatorDocument, error) {
	if l.reg == nil || l.userSvc == nil {
		return nil, constants.ErrServiceUnavailable
	}
	ownerID, err := l.userSvc.FirstUserID(ctx)
	if err != nil {
		return nil, err
	}
	if ownerID == "" {
		return nil, constants.ErrNotFound
	}
	return l.reg.ListUserOperators(ctx, ownerID)
}

func (l *registrationOwnerOperatorLister) ListOperatorsForProvenance(ctx context.Context) ([]*operatorv1.OperatorDocument, error) {
	if l.reg == nil || l.userSvc == nil {
		return nil, constants.ErrServiceUnavailable
	}
	ownerID, err := l.userSvc.FirstUserID(ctx)
	if err != nil {
		return nil, err
	}
	if ownerID == "" {
		return nil, constants.ErrNotFound
	}
	return l.reg.ListUserOperators(ctx, ownerID)
}

// ProviderBoundaryObservationCoordinator fans out BEGIN/FINALIZE observation
// commands to the remote provider-boundary observer operator and ingests
// completed windows on the campaign Gateway host.
type ProviderBoundaryObservationCoordinator struct {
	dispatch       *DispatchService
	operatorLister providerBoundaryOperatorLister
	pubsub         *GatewayWebSocketHandler
	windows        provider_observer.WindowStore
	observations   evidenceWindowAnnouncer
	logger         *slog.Logger

	mu         sync.Mutex
	unregister func()
	observer   *providerBoundaryObserverTarget
}

type providerBoundaryObserverTarget struct {
	OperatorID        string
	OperatorSessionID string
	UserID            string
}

// NewProviderBoundaryObservationCoordinator constructs the gateway-side
// provider-boundary observation coordinator. A nil observations announcer
// persists windows without notifying consumers.
func NewProviderBoundaryObservationCoordinator(
	dispatch *DispatchService,
	operatorLister providerBoundaryOperatorLister,
	pubsubHandler *GatewayWebSocketHandler,
	windows provider_observer.WindowStore,
	observations evidenceWindowAnnouncer,
	logger *slog.Logger,
) *ProviderBoundaryObservationCoordinator {
	return &ProviderBoundaryObservationCoordinator{
		dispatch:       dispatch,
		operatorLister: operatorLister,
		pubsub:         pubsubHandler,
		windows:        windows,
		observations:   observations,
		logger:         logger,
	}
}

// synchronizeObserverSubscription resolves the enrolled provider-boundary
// observer for the gateway owner and subscribes to its results channel. When
// the active observer session changes, any cached target is dropped and the
// coordinator re-subscribes to the newly selected session.
func (c *ProviderBoundaryObservationCoordinator) synchronizeObserverSubscription(ctx context.Context) error {
	if c == nil || c.dispatch == nil || c.operatorLister == nil || c.pubsub == nil || c.windows == nil {
		return constants.ErrServiceUnavailable
	}
	_ = ctx

	operators, err := c.operatorLister.ListOperatorsForObservation(ctx)
	if err != nil {
		c.logger.Warn("Provider-boundary observation: list operators failed", "error", err)
		return err
	}
	selected, err := selectProviderBoundaryObserverForGateway(operators)
	if err != nil {
		c.resetObserver()
		switch {
		case errors.Is(err, constants.ErrProviderBoundaryObserverNotFound):
			c.logger.Warn("Provider-boundary observation: observer not found", "reason", "not found")
		case errors.Is(err, constants.ErrProviderBoundaryObserverAmbiguous):
			c.logger.Warn("Provider-boundary observation: observer ambiguous", "reason", "ambiguous")
		default:
			c.logger.Warn("Provider-boundary observation: observer selection failed", "error", err)
		}
		return err
	}

	c.mu.Lock()
	cached := c.observer
	c.mu.Unlock()

	if cached != nil && cached.OperatorID == selected.OperatorID && cached.OperatorSessionID == selected.OperatorSessionID {
		return nil
	}

	if cached != nil {
		c.logger.Info("Provider-boundary observation: observer session changed; re-subscribing",
			"prior_operator_session_id", cached.OperatorSessionID,
			"operator_session_id", selected.OperatorSessionID)
	}
	c.resetObserver()

	observer := &providerBoundaryObserverTarget{
		OperatorID:        selected.OperatorID,
		OperatorSessionID: selected.OperatorSessionID,
		UserID:            selected.UserID,
	}

	resultsChannel := pubsub.ResultsChannel(observer.OperatorID, observer.OperatorSessionID)
	handler := func(_ string, data []byte) {
		// Observer completions arrive asynchronously after inference dispatch
		// returns; never tie durable ingest to the ephemeral dispatch context.
		c.ingestResult(context.Background(), observer, data)
	}
	unregister := c.pubsub.RegisterHandler(resultsChannel, handler)

	c.mu.Lock()
	c.observer = observer
	c.unregister = unregister
	c.mu.Unlock()

	c.logger.Info("Provider-boundary observation coordinator subscribed",
		"operator_id", observer.OperatorID,
		"operator_session_id", observer.OperatorSessionID,
		"results_channel", resultsChannel)
	return nil
}

// selectProviderBoundaryObserverForGateway resolves the single observer the
// campaign should use. Operators the Gateway reads as stale or offline are
// already excluded by their status, so more than one match means more than one
// live observer. When that happens the tie is broken by the inference node's
// system fingerprint; if that fails too, the ambiguity error is returned so the
// operator sees why selection failed instead of a misleading not-found.
func selectProviderBoundaryObserverForGateway(operators []*operatorv1.OperatorDocument) (*operatorcapability.ProviderBoundaryObserverStatus, error) {
	selected, err := operatorcapability.SelectProviderBoundaryObserver(operators, "")
	if err == nil || !errors.Is(err, constants.ErrProviderBoundaryObserverAmbiguous) {
		return selected, err
	}

	// The observer and inference node are hardware-bound Operator sessions.
	// When there is one inference node, use the canonical system fingerprint
	// to disambiguate multiple observer sessions on the owner account. Never
	// choose by list order or recency.
	inference := make([]*operatorv1.OperatorDocument, 0, len(operators))
	for _, op := range operators {
		if constants.OperatorStatus(op.Status) == constants.OperatorStatusActive &&
			(constants.OperatorType(op.OperatorType) == constants.OperatorTypeRemote || constants.OperatorType(op.OperatorType) == constants.OperatorTypeEmbedded) &&
			op.RuntimeConfig != nil &&
			operatorcapability.GetOperatorCapabilities(op).Has(constants.OperatorCapabilityInference) &&
			op.SystemFingerprint != "" {
			inference = append(inference, op)
		}
	}
	if len(inference) != 1 {
		return nil, err
	}
	selected, err = operatorcapability.SelectProviderBoundaryObserverForHardware(
		operators,
		"",
		inference[0].SystemFingerprint,
	)
	if err != nil {
		// No observer shares the inference node's hardware; that is a failed
		// tiebreak, not an absent observer.
		return nil, fmt.Errorf("%w: no observer matches the inference node fingerprint", constants.ErrProviderBoundaryObserverAmbiguous)
	}
	return selected, nil
}

func (c *ProviderBoundaryObservationCoordinator) resetObserver() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.unregister != nil {
		c.unregister()
		c.unregister = nil
	}
	c.observer = nil
	c.mu.Unlock()
}

// Stop unsubscribes from the observer results channel.
func (c *ProviderBoundaryObservationCoordinator) Stop() {
	c.resetObserver()
}

// PreflightCommandDelivery verifies that the active observer is enrolled and
// is deliverable: the embedded Operator through its in-process processor, any
// other observer through at least one cmd-channel WebSocket subscriber.
func (c *ProviderBoundaryObservationCoordinator) PreflightCommandDelivery(ctx context.Context) error {
	if err := c.synchronizeObserverSubscription(ctx); err != nil {
		return fmt.Errorf("provider-boundary observation preflight: %w", err)
	}
	c.mu.Lock()
	observer := c.observer
	c.mu.Unlock()
	if observer == nil {
		return fmt.Errorf("provider-boundary observation preflight: %w", constants.ErrProviderBoundaryObserverNotFound)
	}
	cmdChannel := pubsub.CmdChannel(observer.OperatorID, observer.OperatorSessionID)
	if !c.dispatch.commandDeliverable(observer.OperatorID, cmdChannel, c.pubsub) {
		return fmt.Errorf("provider-boundary observation preflight: %w: cmd channel %s",
			constants.ErrEvaluationObservationUnavailable, cmdChannel)
	}
	return nil
}

// NotifyAttemptBegin dispatches a BEGIN command to the observer and returns its
// verified final signed receipt. It fails closed when the command cannot be
// delivered or the observer does not sign an outcome. A skipped attempt (no
// attempt ID) returns a nil receipt.
func (c *ProviderBoundaryObservationCoordinator) NotifyAttemptBegin(ctx context.Context, _ string, providerAttemptID string, startedAtUnixMs int64, retryCount uint32) (*operatorv1.EventReceipt, error) {
	if providerAttemptID == "" {
		return nil, nil
	}
	if err := c.synchronizeObserverSubscription(ctx); err != nil {
		return nil, fmt.Errorf("provider-boundary observation begin: %w", err)
	}
	command := &evalv1.ProviderBoundaryObservationCommand{
		ProviderAttemptId:      providerAttemptID,
		Phase:                  evalv1.ProviderBoundaryObservationPhase_PROVIDER_BOUNDARY_OBSERVATION_PHASE_BEGIN,
		AttemptStartedAtUnixMs: startedAtUnixMs,
		RetryCount:             retryCount,
	}
	return c.publishCommand(ctx, command)
}

// NotifyAttemptFinalize dispatches a FINALIZE command to the observer and
// returns its verified final signed receipt.
func (c *ProviderBoundaryObservationCoordinator) NotifyAttemptFinalize(
	ctx context.Context,
	_ string,
	providerAttemptID string,
	inferenceTransactionID string,
	startedAtUnixMs int64,
	completedAtUnixMs int64,
	failed bool,
	retryCount uint32,
) (*operatorv1.EventReceipt, error) {
	if providerAttemptID == "" {
		return nil, nil
	}
	if err := c.synchronizeObserverSubscription(ctx); err != nil {
		return nil, fmt.Errorf("provider-boundary observation finalize: %w", err)
	}
	status := evalv1.ProviderBoundaryObservationAttemptStatus_PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_COMPLETED
	if failed {
		status = evalv1.ProviderBoundaryObservationAttemptStatus_PROVIDER_BOUNDARY_OBSERVATION_ATTEMPT_STATUS_FAILED
	}
	command := &evalv1.ProviderBoundaryObservationCommand{
		ProviderAttemptId:        providerAttemptID,
		InferenceTransactionId:   inferenceTransactionID,
		Phase:                    evalv1.ProviderBoundaryObservationPhase_PROVIDER_BOUNDARY_OBSERVATION_PHASE_FINALIZE,
		AttemptStartedAtUnixMs:   startedAtUnixMs,
		AttemptCompletedAtUnixMs: completedAtUnixMs,
		AttemptStatus:            status,
		RetryCount:               retryCount,
	}
	return c.publishCommand(ctx, command)
}

func (c *ProviderBoundaryObservationCoordinator) publishCommand(ctx context.Context, command *evalv1.ProviderBoundaryObservationCommand) (*operatorv1.EventReceipt, error) {
	c.mu.Lock()
	observer := c.observer
	c.mu.Unlock()
	if observer == nil || c.dispatch == nil {
		return nil, constants.ErrMissingRequiredField
	}
	payload, err := proto.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("provider-boundary observation command marshal: %w", err)
	}
	receipt, err := c.dispatch.DispatchObservation(ctx, ObservationDispatchRequest{
		TargetOperatorSessionID: observer.OperatorSessionID,
		EventType:               string(constants.Event.Operator.ProviderBoundaryObservation.Requested),
		Payload:                 payload,
	})
	if err != nil {
		c.logger.Warn("Provider-boundary observation command dispatch failed",
			"provider_attempt_id", command.GetProviderAttemptId(),
			"phase", command.GetPhase().String(),
			"operator_id", observer.OperatorID,
			"operator_session_id", observer.OperatorSessionID,
			"error", err)
		return nil, err
	}
	c.logger.Info("Provider-boundary observation command executed",
		"transaction_id", receipt.GetTransactionId(),
		"status", receipt.GetStatus().String(),
		"provider_attempt_id", command.GetProviderAttemptId(),
		"phase", command.GetPhase().String(),
		"operator_id", observer.OperatorID,
		"operator_session_id", observer.OperatorSessionID)
	return receipt, nil
}

func (c *ProviderBoundaryObservationCoordinator) ingestResult(ctx context.Context, observer *providerBoundaryObserverTarget, data []byte) {
	env := &commonv1.GovernanceEnvelope{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, env); err != nil {
		c.logger.Warn("Provider-boundary observation ingest: unmarshal envelope failed", "error", err)
		return
	}
	if env.GetEventType() != string(constants.Event.Operator.ProviderBoundaryObservation.Completed) {
		return
	}
	completion := &evalv1.ProviderBoundaryObservationCompleted{}
	if err := proto.Unmarshal(env.GetPayload(), completion); err != nil {
		c.logger.Warn("Provider-boundary observation ingest: unmarshal completion failed", "error", err)
		return
	}
	window := completion.GetWindow()
	if window == nil {
		return
	}
	if err := c.windows.Save(ctx, window); err != nil {
		c.logger.Warn("Provider-boundary observation ingest: save window failed",
			"provider_attempt_id", window.GetProviderAttemptId(),
			"error", err)
		return
	}
	c.logger.Info("Provider-boundary observation window ingested",
		"provider_attempt_id", window.GetProviderAttemptId(),
		"sample_count", len(window.GetSamples()),
		"observation_digest", window.GetObservationDigest())
	if c.observations != nil {
		c.observations.WindowRecorded(ctx, constants.EventProviderBoundaryObservationRecorded, observer.UserID, window.GetProviderAttemptId())
	}
}

// ObservationDispatchRequest is a governed observer BEGIN or FINALIZE command.
type ObservationDispatchRequest struct {
	TargetOperatorSessionID string
	EventType               string
	Payload                 []byte
	RequestorUserID         string
	ActingAppID             string
}

// DispatchObservation builds a governed observation envelope, delivers it to
// the observer, and waits for the observer's verified final signed receipt.
// Waiting puts the observer's ledger commitment ahead of anything the caller
// does next, so a trace bracket that closes after the inference dispatch can
// claim it. Only observation action types are accepted: this path admits no
// L3 approval wait.
func (d *DispatchService) DispatchObservation(ctx context.Context, req ObservationDispatchRequest) (*operatorv1.EventReceipt, error) {
	actionType, err := constants.ValidateGovernedRequest(constants.EventType(req.EventType))
	if err != nil {
		return nil, fmt.Errorf("dispatch: observation: %w", err)
	}
	if !isObservationAction(actionType) {
		return nil, fmt.Errorf("dispatch: observation: %w: %s", constants.ErrTxEventActionMismatch, actionType)
	}
	op, err := d.auth.ValidateOperatorSession(ctx, req.TargetOperatorSessionID)
	if err != nil {
		return nil, fmt.Errorf("dispatch: observation: validate operator session: %w", err)
	}
	stateRoot, err := d.stateRootProvider.GetCurrentStateRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatch: observation: state root: %w: %w", constants.ErrIngressStateRootUnavailable, err)
	}
	env, err := BuildGovernanceEnvelope(BuildEnvelopeParams{
		OperatorID:        op.Id,
		OperatorSessionID: op.OperatorSessionId,
		EventType:         req.EventType,
		Payload:           req.Payload,
		RequestorUserID:   req.RequestorUserID,
		ActingAppID:       req.ActingAppID,
		StateMerkleRoot:   stateRoot,
		Posture:           d.posture,
		Doctrine:          d.doctrine,
	})
	if err != nil {
		return nil, fmt.Errorf("dispatch: observation: %w", err)
	}
	wire, err := protojson.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("dispatch: observation: marshal envelope: %w", err)
	}
	if governance.L2Enforced(env, d.posture) && d.l2Deliberator != nil {
		deliberated, derr := d.l2Deliberator.Deliberate(ctx, wire)
		if derr != nil {
			return nil, fmt.Errorf("dispatch: observation: l2 deliberation: %w: %w", constants.ErrIngressL2DeliberationFailed, derr)
		}
		wire = deliberated.Envelope
	}
	result, err := d.dispatchEnvelope(ctx, op, env, wire, DispatchRequest{
		TargetOperatorSessionID: req.TargetOperatorSessionID,
		EventType:               req.EventType,
		RequestorUserID:         req.RequestorUserID,
		ActingAppID:             req.ActingAppID,
	})
	if err != nil {
		return nil, err
	}
	// An observer whose handler failed did not observe the attempt.
	if result.Receipt.GetStatus() != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		return nil, fmt.Errorf("dispatch: observation %s: %w: %s", result.TransactionID, constants.ErrEvaluationObservationUnavailable, result.Receipt.GetResultSummary())
	}
	return result.Receipt, nil
}

// isObservationAction reports whether an action is an observer BEGIN/FINALIZE
// command, whose verified final receipt is its terminal outcome.
func isObservationAction(actionType constants.ActionType) bool {
	return actionType == constants.ActionTypeProviderBoundaryObservation ||
		actionType == constants.ActionTypeModelProvenanceObservation
}

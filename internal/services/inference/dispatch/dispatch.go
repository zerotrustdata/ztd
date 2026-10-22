// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package dispatch owns the platform-internal inference dispatch service on
// the User Gateway. It constructs a governed InferenceRequested envelope,
// resolves the Inference Node's operator session by querying enrolled
// operators whose capabilities include inference, and
// dispatches through the gateway's CommandDispatcher (the same
// DispatchService used for every other governed command). It never calls
// the Ollama backend directly; inference is a governed mutation that
// traverses the full L1–L5 gauntlet on the Inference Node.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/inference"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// governanceRoundTripMargin covers the governed round trip around a provider
// call: envelope construction, L1 screening, L2 deliberation where required,
// pub/sub delivery, L4/L5 verification and receipt signing, and the result
// publication back to the gateway.
const governanceRoundTripMargin = 30 * time.Second

// RequestDeadline is the single explicit request deadline for a governed
// inference dispatch. It derives from the provider request timeout so the
// dispatch wait always outlives an in-flight provider call. When it expires
// the remote provider call may still be running; the outcome is classified
// unknown (constants.ErrInferenceOutcomeUnknown) and is never retried
// automatically.
const RequestDeadline = inference.ProviderRequestTimeout + governanceRoundTripMargin

// CommandDispatcher dispatches a governed envelope to a target operator
// session and correlates the result by transaction ID. Implemented by
// *gateway.DispatchService; the interface breaks the import cycle between
// the gateway package (which imports this package for wiring) and this
// package (which needs the dispatch primitive).
type CommandDispatcher interface {
	// Dispatch publishes a signed command to the target operator's cmd
	// channel and waits for the result envelope on the results channel.
	// Returns the transaction ID and the result envelope's payload
	// (proto-marshaled). Returns an error if the operator session is
	// invalid, envelope construction fails, or the result does not arrive
	// within the dispatch timeout.
	Dispatch(ctx context.Context, req CommandDispatchRequest) (*CommandDispatchResult, error)
}

// CommandDispatchRequest is the gateway-agnostic dispatch request carried
// by the CommandDispatcher interface. The gateway adapter converts this to
// gateway.DispatchRequest before calling DispatchService.Dispatch.
type CommandDispatchRequest struct {
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

	// Timeout is the explicit request deadline for this dispatch. Zero means
	// the dispatcher's default applies. Inference dispatches always set
	// RequestDeadline so the wait outlives an in-flight provider call.
	Timeout time.Duration

	// OnInferenceProgress receives bounded provider progress telemetry while
	// waiting for the authoritative terminal completion. Nil disables live
	// progress forwarding.
	OnInferenceProgress func(*operatorv1.InferenceProgressEvent) error

	// OnAdmitted runs once the Gateway admitted the envelope (L2 and any L3
	// proof attached) and before it is published. Observation commands
	// dispatched under its ctx are bound to that admitted inference and carry
	// its L2 quorum. An error stops the dispatch before publish.
	OnAdmitted func(ctx context.Context) error
}

// CommandDispatchResult is the gateway-agnostic dispatch result. The
// TransactionID correlates the dispatch with the signed ActionReceipt
// recorded in the audit chain. The ResultPayload carries the
// proto-marshaled result message (e.g., InferenceResult). For inference
// dispatches, Receipt carries the verified final signed ActionReceipt whose
// result_summary binds the result digest.
type CommandDispatchResult struct {
	TransactionID string
	ResultPayload []byte
	Receipt       *operatorv1.ActionReceipt
}

// OperatorLister lists enrolled operators for a user. Implemented by
// *gateway.RegistrationService; the interface breaks the import cycle.
type OperatorLister interface {
	// ListUserOperators returns every operator document owned by userID,
	// including platform-enrolled operators. The dispatch service filters
	// these by the inference capability (RuntimeConfig.Capabilities) to resolve
	// the Inference Node's operator session.
	ListUserOperators(ctx context.Context, userID string) ([]*operatorv1.OperatorDocument, error)
}

// ProviderObservationNotifier coordinates remote provider-boundary observation
// for scored inference attempts. Implementations use the gateway command
// transport, wait for the observer's verified final signed receipt, and must
// fail closed when observation commands cannot be delivered. A nil receipt with
// a nil error means the attempt was not observed.
type ProviderObservationNotifier interface {
	NotifyAttemptBegin(ctx context.Context, requestorUserID, providerAttemptID string, startedAtUnixMs int64, retryCount uint32) (*operatorv1.ActionReceipt, error)
	NotifyAttemptFinalize(ctx context.Context, requestorUserID, providerAttemptID, inferenceTransactionID string, startedAtUnixMs, completedAtUnixMs int64, failed bool, retryCount uint32) (*operatorv1.ActionReceipt, error)
}

// ProvenanceObservationNotifier coordinates storage-side model provenance
// attestation for scored inference attempts. Implementations return the
// provenance Operator's verified final signed receipt and must fail closed
// when provenance commands cannot be delivered in campaign mode.
type ProvenanceObservationNotifier interface {
	NotifyAttemptBegin(ctx context.Context, requestorUserID, providerAttemptID, servedModelTag, expectedModelDigest, modelRegistryDigest, campaignID string, startedAtUnixMs int64, retryCount uint32) (*operatorv1.ActionReceipt, error)
	NotifyAttemptFinalize(ctx context.Context, requestorUserID, providerAttemptID, inferenceTransactionID, servedModelTag, expectedModelDigest, modelRegistryDigest, campaignID string, startedAtUnixMs, completedAtUnixMs int64, failed bool, retryCount uint32) (*operatorv1.ActionReceipt, error)
}

// DispatchService is the platform-internal inference dispatch service on
// the User Gateway. It is not a NativeTool and is not registered in the MCP
// ToolRegistry. App workloads call DispatchInference to route
// a governed inference request to the Inference Node through the full
// L1–L5 gauntlet.
type DispatchService struct {
	dispatcher          CommandDispatcher
	operatorList        OperatorLister
	observationNotifier ProviderObservationNotifier
	provenanceNotifier  ProvenanceObservationNotifier
	logger              *slog.Logger
}

// NewDispatchService constructs a DispatchService wired to the gateway's
// command dispatcher and operator lister.
func NewDispatchService(dispatcher CommandDispatcher, operatorList OperatorLister, logger *slog.Logger) *DispatchService {
	return &DispatchService{
		dispatcher:   dispatcher,
		operatorList: operatorList,
		logger:       logger,
	}
}

// SetProviderObservationNotifier wires the optional remote provider-boundary
// observation coordinator for scored inference attempts.
func (s *DispatchService) SetProviderObservationNotifier(notifier ProviderObservationNotifier) {
	if s == nil {
		return
	}
	s.observationNotifier = notifier
}

// SetProvenanceObservationNotifier wires the optional storage-side model
// provenance coordinator for scored inference attempts.
func (s *DispatchService) SetProvenanceObservationNotifier(notifier ProvenanceObservationNotifier) {
	if s == nil {
		return
	}
	s.provenanceNotifier = notifier
}

// DispatchInferenceRequest is the input to DispatchInference. Role and ordered messages are required; optional fields override the Inference Node's config defaults when non-zero/non-empty.
type DispatchInferenceRequest struct {
	Decision         *operatorv1.InferenceDecisionRequest
	TargetOperatorID string
	// Role is the chat-tier role for this request (primary, assistant, lite).
	Role models.InferenceModelRole

	// Messages and Tools preserve the typed request sent to the Inference Node. The node validates and re-scrubs every data-bearing part before crossing the provider boundary.
	Messages []*operatorv1.InferenceMessage
	Tools    []*operatorv1.InferenceToolDeclaration

	// Model selects an exact frozen registry entry in campaign mode. Standard
	// inference accepts only the configured model for the role.
	Model string

	// Temperature overrides the backend's default temperature. Zero means
	// use the backend default.
	Temperature float32

	// MaxTokens is the caller's output limit. Zero states no limit and the
	// executing backend runs under constants.InferenceMaxOutputTokens.
	MaxTokens int32

	// KeepAlive overrides the config default keep-alive duration. Empty
	// means use the config default.
	KeepAlive string

	TopP                 *float32
	TopK                 *int32
	Seed                 *int32
	StopSequences        []string
	ResponseFormat       *operatorv1.InferenceResponseFormat
	RequestSchemaVersion string
	ToolChoice           *operatorv1.InferenceToolChoice
	ParallelToolCalls    *bool
	Thinking             *operatorv1.InferenceThinkingControl
	ContextLimit         *int32
	ProviderAttemptID    string
	ModelDigest          string
	CampaignID           string
	RunID                string
	AssignmentID         string
	EvaluationAttemptID  string
	ScenarioID           string
	ModelRegistry        []*operatorv1.InferenceModelVariant
	ModelRegistryDigest  string

	// TargetOperatorSessionID pins the dispatch to a specific Inference Node
	// session. When empty, exactly one inference-capable operator session
	// must be enrolled for the requestor; zero or multiple matches are
	// rejected rather than resolved by query order.
	TargetOperatorSessionID string

	// RequestorUserID is the user who initiated the inference request.
	// Used to resolve the Inference Node's operator session from the user's
	// enrolled operators.
	RequestorUserID string

	// ActingAppID is the application that initiated the inference request.
	ActingAppID string

	// Application context fields propagated from the chat turn.
	CaseID          string
	InvestigationID string
	TaskID          string
	WebSessionID    string
	CliSessionID    string

	// Stream requests live InferenceProgressEvent telemetry while waiting for
	// the authoritative terminal completion.
	Stream bool

	// OnProgress receives progress telemetry when Stream is true.
	OnProgress func(*operatorv1.InferenceProgressEvent) error

	// RetryCount is the zero-based retry ordinal for this provider attempt.
	RetryCount uint32
}

// hasCampaignAuthority reports whether the request carries campaign binding.
func (r DispatchInferenceRequest) hasCampaignAuthority() bool {
	return inference.HasCampaignAuthority(r.CampaignID, r.ModelRegistryDigest, r.ModelRegistry)
}

// DispatchInferenceResult is the output of a successful inference dispatch.
// The InferenceResult carries ordered response parts, usage metadata, and
// finish reason. The TransactionID correlates the dispatch with the signed
// ActionReceipt recorded in the User Gateway's audit chain. Receipt carries
// the verified final signed ActionReceipt whose result_summary binds the
// result digest. ObservationReceipts carries the observers' verified final
// receipts for the attempt's BEGIN and FINALIZE commands, in dispatch order;
// each was signed before DispatchInference returned.
type DispatchInferenceResult struct {
	TransactionID       string
	Result              *operatorv1.InferenceResult
	Receipt             *operatorv1.ActionReceipt
	ObservationReceipts []*operatorv1.ActionReceipt
}

// Response is the result as its wire InferenceDispatchResponse.
func (r *DispatchInferenceResult) Response() *operatorv1.InferenceDispatchResponse {
	return &operatorv1.InferenceDispatchResponse{
		TransactionId:       r.TransactionID,
		Result:              r.Result,
		Receipt:             r.Receipt,
		ObservationReceipts: r.ObservationReceipts,
	}
}

// DispatchInference constructs a governed InferenceRequested envelope,
// resolves the Inference Node's operator session from the requestor's
// enrolled operators (filtering by the inference capability),
// dispatches through the gateway's CommandDispatcher, and decodes the
// InferenceResult from the result envelope's payload. It never calls the
// Ollama backend directly; the Inference Node executes the request through
// the full L1–L5 gauntlet.
func (s *DispatchService) DispatchInference(ctx context.Context, req DispatchInferenceRequest) (*DispatchInferenceResult, error) {
	if req.RequestorUserID == "" {
		return nil, fmt.Errorf("inference dispatch: %w", constants.ErrRegistrationUserIDRequired)
	}
	if req.Role == models.InferenceModelRoleUnspecified {
		return nil, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceRoleInvalid)
	}
	if len(req.Messages) == 0 && req.Decision == nil {
		return nil, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceMessagesRequired)
	}
	if req.ProviderAttemptID == "" {
		return nil, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceProviderAttemptRequired)
	}
	if req.ModelDigest != "" && !models.IsSHA256Hex(req.ModelDigest) {
		return nil, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceEvidenceHashInvalid)
	}
	if err := validateCampaignModelRegistry(req); err != nil {
		return nil, fmt.Errorf("inference dispatch: %w", err)
	}

	// Resolve the Inference Node's operator session from the requestor's
	// enrolled operators. The Inference Node advertises the inference
	// capability at enrollment time; the dispatch service filters by it.
	operatorSessionID, err := s.resolveInferenceOperator(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("inference dispatch: %w", err)
	}

	// Construct the InferenceRequested proto payload.
	infReq := &operatorv1.InferenceRequested{
		Role:                 req.Role.ToProto(),
		Decision:             req.Decision,
		Model:                req.Model,
		Messages:             req.Messages,
		Tools:                req.Tools,
		Temperature:          req.Temperature,
		MaxTokens:            req.MaxTokens,
		KeepAlive:            req.KeepAlive,
		TopP:                 req.TopP,
		TopK:                 req.TopK,
		Seed:                 req.Seed,
		StopSequences:        req.StopSequences,
		ResponseFormat:       req.ResponseFormat,
		RequestSchemaVersion: req.RequestSchemaVersion,
		ToolChoice:           req.ToolChoice,
		ParallelToolCalls:    req.ParallelToolCalls,
		Thinking:             req.Thinking,
		ContextLimit:         req.ContextLimit,
		ProviderAttemptId:    req.ProviderAttemptID,
		ModelDigest:          req.ModelDigest,
		CampaignId:           req.CampaignID,
		RunId:                req.RunID,
		AssignmentId:         req.AssignmentID,
		EvaluationAttemptId:  req.EvaluationAttemptID,
		ScenarioId:           req.ScenarioID,
		ModelRegistry:        req.ModelRegistry,
		ModelRegistryDigest:  req.ModelRegistryDigest,
		Stream:               req.Stream,
		RetryCount:           req.RetryCount,
	}
	if err := inference.ValidateDecisionInference(infReq); err != nil {
		return nil, fmt.Errorf("inference dispatch: %w", err)
	}
	payload, err := proto.Marshal(infReq)
	if err != nil {
		return nil, fmt.Errorf("inference dispatch: marshal payload: %w", err)
	}

	// Dispatch through the gateway's CommandDispatcher. The dispatcher
	// constructs the GovernanceEnvelope with the gateway's state root and
	// posture, publishes to the Inference Node's cmd channel, and
	// correlates the result by transaction ID. The timeout is the single
	// request deadline contract (RequestDeadline): it outlives an in-flight
	// provider call, and its expiry is an unknown remote outcome, not a
	// clean timeout.
	//
	// The attempt begins once the Gateway admits the envelope, just before
	// publish (beginAttempt). Its observation commands are bound to the
	// admitted inference and carry its L2 quorum rather than being deliberated
	// again, so each observer's window opens at the attempt start it records.
	attempt := attemptState{ctx: ctx}
	var beginErr error
	result, err := s.dispatcher.Dispatch(ctx, CommandDispatchRequest{
		TargetOperatorSessionID: operatorSessionID,
		EventType:               string(constants.Event.Operator.Inference.Requested),
		Payload:                 payload,
		RequestorUserID:         req.RequestorUserID,
		ActingAppID:             req.ActingAppID,
		CaseID:                  req.CaseID,
		InvestigationID:         req.InvestigationID,
		TaskID:                  req.TaskID,
		WebSessionID:            req.WebSessionID,
		CliSessionID:            req.CliSessionID,
		Timeout:                 RequestDeadline,
		OnInferenceProgress:     req.OnProgress,
		OnAdmitted: func(admitted context.Context) error {
			beginErr = s.beginAttempt(admitted, req, &attempt)
			return beginErr
		},
	})
	if beginErr != nil {
		// beginAttempt already finalized whatever window it opened.
		return nil, beginErr
	}
	attemptCtx, attemptStartedAt, window, observationReceipts := attempt.ctx, attempt.startedAt, attempt.window, attempt.receipts
	if err != nil {
		if errors.Is(err, constants.ErrDispatchResultTimeout) {
			// The dispatch deadline expired while the provider call may
			// still be running remotely. Record the unknown outcome and
			// never retry automatically: retrying could duplicate the
			// provider call and invalidate request-budget accounting.
			s.logger.Warn("inference dispatch: deadline exceeded; remote provider outcome unknown",
				"operator_session_id", operatorSessionID,
				"role", req.Role,
				"model", req.Model,
				"error", err)
			return nil, s.finalizeFailedAttempt(attemptCtx, req, "", attemptStartedAt, window, observationReceipts, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceOutcomeUnknown))
		}
		return nil, s.finalizeFailedAttempt(attemptCtx, req, "", attemptStartedAt, window, observationReceipts, fmt.Errorf("inference dispatch: %w", err))
	}

	// Decode the InferenceResult from the result envelope's payload.
	if len(result.ResultPayload) == 0 {
		return nil, s.finalizeFailedAttempt(attemptCtx, req, result.TransactionID, attemptStartedAt, window, observationReceipts, fmt.Errorf("inference dispatch: %w", constants.ErrInferenceResultDecode))
	}
	infResult := &operatorv1.InferenceResult{}
	if err := proto.Unmarshal(result.ResultPayload, infResult); err != nil {
		return nil, s.finalizeFailedAttempt(attemptCtx, req, result.TransactionID, attemptStartedAt, window, observationReceipts, fmt.Errorf("inference dispatch: %w: %w", constants.ErrInferenceResultDecode, err))
	}
	if err := validateInferenceResult(infResult, req); err != nil {
		return nil, s.finalizeFailedAttempt(attemptCtx, req, result.TransactionID, attemptStartedAt, window, observationReceipts, fmt.Errorf("inference dispatch: %w", err))
	}

	failed := result.Receipt != nil && result.Receipt.Status != operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	finalized, err := s.finalizeAttempt(attemptCtx, req, result.TransactionID, attemptStartedAt, failed, window)
	if err != nil {
		return nil, WithObservationReceipts(append(observationReceipts, finalized...), err)
	}
	observationReceipts = append(observationReceipts, finalized...)

	s.logger.Info("Governed inference dispatch completed",
		"transaction_id", result.TransactionID,
		"role", req.Role,
		"model", infResult.GetModel(),
		"completion_tokens", infResult.GetCompletionTokens())

	return &DispatchInferenceResult{
		TransactionID:       result.TransactionID,
		Result:              infResult,
		Receipt:             result.Receipt,
		ObservationReceipts: observationReceipts,
	}, nil
}

// appendObservationReceipt appends an observer's receipt; a nil receipt is an
// unobserved attempt and adds nothing.
func appendObservationReceipt(receipts []*operatorv1.ActionReceipt, receipt *operatorv1.ActionReceipt) []*operatorv1.ActionReceipt {
	if receipt == nil {
		return receipts
	}
	return append(receipts, receipt)
}

// attemptState is one attempt as beginAttempt opened it. ctx is the admitted
// ctx its observation commands ran under; FINALIZE runs under it too, so it
// carries the same parent binding as BEGIN. Before admission it is the request
// ctx and nothing is open.
type attemptState struct {
	ctx       context.Context
	startedAt int64
	window    attemptWindow
	receipts  []*operatorv1.ActionReceipt
}

// beginAttempt opens the attempt's observation windows under the admitted ctx.
// A provider-boundary BEGIN failure opens nothing; a provenance BEGIN failure
// finalizes the provider-boundary window it follows.
func (s *DispatchService) beginAttempt(ctx context.Context, req DispatchInferenceRequest, attempt *attemptState) error {
	attempt.ctx = ctx
	attempt.startedAt = time.Now().UTC().UnixMilli()
	observationBegin, observed, err := s.notifyObservationBegin(ctx, req, attempt.startedAt)
	if err != nil {
		return err
	}
	attempt.window.observed = observed
	attempt.receipts = appendObservationReceipt(attempt.receipts, observationBegin)
	provenanceBegin, attested, err := s.notifyProvenanceBegin(ctx, req, attempt.startedAt)
	if err != nil {
		return s.finalizeFailedAttempt(ctx, req, "", attempt.startedAt, attempt.window, attempt.receipts, err)
	}
	attempt.window.provenance = attested
	attempt.receipts = appendObservationReceipt(attempt.receipts, provenanceBegin)
	return nil
}

// attemptWindow records which observation coordinators opened a BEGIN window
// for an attempt. FINALIZE goes only to a coordinator whose BEGIN was
// delivered, so an attempt that ran without an observer or provenance Operator
// is not finalized against one that connected mid-attempt.
type attemptWindow struct {
	observed   bool
	provenance bool
}

// finalizeAttempt reports the attempt outcome to each observation coordinator
// whose BEGIN window is open and returns the FINALIZE receipts they signed.
// Each coordinator is attempted even when the other fails, and a delivered
// receipt is returned even when the other was not, because its commitment is
// already on the observing runtime's ledger. Once a window is open,
// campaign-bound requests fail closed on either undelivered finalize. A
// non-campaign request logs an undelivered provider-boundary finalize, and
// fails closed on an undelivered provenance finalize unless the provenance
// Operator is no longer connected.
func (s *DispatchService) finalizeAttempt(ctx context.Context, req DispatchInferenceRequest, inferenceTransactionID string, startedAtUnixMs int64, failed bool, window attemptWindow) ([]*operatorv1.ActionReceipt, error) {
	completedAtUnixMs := time.Now().UTC().UnixMilli()
	var observed, provenance *operatorv1.ActionReceipt
	var observedErr, provenanceErr error
	if window.observed {
		observed, observedErr = s.notifyObservationFinalize(ctx, req, inferenceTransactionID, startedAtUnixMs, completedAtUnixMs, failed)
	}
	if window.provenance {
		provenance, provenanceErr = s.notifyProvenanceFinalize(ctx, req, inferenceTransactionID, startedAtUnixMs, completedAtUnixMs, failed)
	}
	return appendObservationReceipt(appendObservationReceipt(nil, observed), provenance), errors.Join(observedErr, provenanceErr)
}

// finalizeFailedAttempt finalizes an attempt that already failed and returns
// cause carrying every observation receipt the attempt produced, BEGIN and
// FINALIZE. The original failure is kept, so an undelivered finalize is
// logged rather than replacing it.
func (s *DispatchService) finalizeFailedAttempt(ctx context.Context, req DispatchInferenceRequest, inferenceTransactionID string, startedAtUnixMs int64, window attemptWindow, began []*operatorv1.ActionReceipt, cause error) error {
	finalized, err := s.finalizeAttempt(ctx, req, inferenceTransactionID, startedAtUnixMs, true, window)
	if err != nil {
		s.logger.Warn("inference dispatch: observation finalize undelivered for failed attempt",
			"provider_attempt_id", req.ProviderAttemptID,
			"error", err)
	}
	return WithObservationReceipts(append(began, finalized...), cause)
}

// notifyObservationBegin opens the provider-boundary observation window for an
// attempt and reports whether it opened. Observation is optional in every
// mode: with no observer connected the attempt proceeds unobserved, and the
// campaign's provider observation policy decides at verification whether a
// missing window is acceptable. Other errors fail closed.
func (s *DispatchService) notifyObservationBegin(
	ctx context.Context,
	req DispatchInferenceRequest,
	startedAtUnixMs int64,
) (*operatorv1.ActionReceipt, bool, error) {
	if s.observationNotifier == nil {
		return nil, false, nil
	}
	receipt, err := s.observationNotifier.NotifyAttemptBegin(ctx, req.RequestorUserID, req.ProviderAttemptID, startedAtUnixMs, req.RetryCount)
	if err == nil {
		return receipt, true, nil
	}
	if errors.Is(err, constants.ErrProviderBoundaryObserverNotFound) {
		s.logger.Warn("inference dispatch: provider-boundary observation begin skipped; no observer",
			"provider_attempt_id", req.ProviderAttemptID,
			"error", err)
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("inference dispatch: %w", err)
}

func (s *DispatchService) notifyObservationFinalize(
	ctx context.Context,
	req DispatchInferenceRequest,
	inferenceTransactionID string,
	startedAtUnixMs int64,
	completedAtUnixMs int64,
	failed bool,
) (*operatorv1.ActionReceipt, error) {
	if s.observationNotifier == nil {
		return nil, nil
	}
	receipt, err := s.observationNotifier.NotifyAttemptFinalize(
		ctx,
		req.RequestorUserID,
		req.ProviderAttemptID,
		inferenceTransactionID,
		startedAtUnixMs,
		completedAtUnixMs,
		failed,
		req.RetryCount,
	)
	if err == nil {
		return receipt, nil
	}
	if req.hasCampaignAuthority() {
		return nil, fmt.Errorf("inference dispatch: %w", err)
	}
	s.logger.Warn("inference dispatch: provider-boundary observation finalize undelivered",
		"provider_attempt_id", req.ProviderAttemptID,
		"error", err)
	return nil, nil
}

// notifyProvenanceBegin opens the model provenance window for an attempt and
// reports whether it opened. Provenance is optional in every mode: with no
// provenance Operator connected the attempt proceeds unattested, and the
// campaign's model provenance policy decides at verification whether a missing
// window is acceptable. Other errors fail closed.
func (s *DispatchService) notifyProvenanceBegin(
	ctx context.Context,
	req DispatchInferenceRequest,
	startedAtUnixMs int64,
) (*operatorv1.ActionReceipt, bool, error) {
	if s.provenanceNotifier == nil || req.Model == "" || req.ModelDigest == "" {
		return nil, false, nil
	}
	receipt, err := s.provenanceNotifier.NotifyAttemptBegin(ctx, req.RequestorUserID, req.ProviderAttemptID, req.Model, req.ModelDigest, req.ModelRegistryDigest, req.CampaignID, startedAtUnixMs, req.RetryCount)
	if err == nil {
		return receipt, true, nil
	}
	if errors.Is(err, constants.ErrProvenanceOperatorNotFound) {
		s.logger.Warn("inference dispatch: model provenance observation begin skipped; no provenance operator",
			"provider_attempt_id", req.ProviderAttemptID,
			"error", err)
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("inference dispatch: %w", err)
}

func (s *DispatchService) notifyProvenanceFinalize(
	ctx context.Context,
	req DispatchInferenceRequest,
	inferenceTransactionID string,
	startedAtUnixMs int64,
	completedAtUnixMs int64,
	failed bool,
) (*operatorv1.ActionReceipt, error) {
	if s.provenanceNotifier == nil || req.Model == "" || req.ModelDigest == "" {
		return nil, nil
	}
	receipt, err := s.provenanceNotifier.NotifyAttemptFinalize(
		ctx,
		req.RequestorUserID,
		req.ProviderAttemptID,
		inferenceTransactionID,
		req.Model,
		req.ModelDigest,
		req.ModelRegistryDigest,
		req.CampaignID,
		startedAtUnixMs,
		completedAtUnixMs,
		failed,
		req.RetryCount,
	)
	if err == nil {
		return receipt, nil
	}
	if !req.hasCampaignAuthority() && errors.Is(err, constants.ErrProvenanceOperatorNotFound) {
		s.logger.Warn("inference dispatch: model provenance observation finalize skipped; no provenance operator",
			"provider_attempt_id", req.ProviderAttemptID,
			"error", err)
		return nil, nil
	}
	return nil, fmt.Errorf("inference dispatch: %w", err)
}

func validateCampaignModelRegistry(req DispatchInferenceRequest) error {
	if !req.hasCampaignAuthority() {
		return nil
	}
	if req.CampaignID == "" || req.RunID == "" || req.AssignmentID == "" || req.EvaluationAttemptID == "" || req.ScenarioID == "" ||
		req.Model == "" || !models.IsSHA256Hex(req.ModelDigest) || !models.IsSHA256Hex(req.ModelRegistryDigest) || len(req.ModelRegistry) == 0 {
		return constants.ErrInferenceCampaignBindingInvalid
	}
	return inference.VerifyModelRegistryBinding(req.CampaignID, req.ModelRegistryDigest, req.Model, req.ModelDigest, req.ModelRegistry)
}

// validateInferenceResult verifies that the terminal InferenceResult echoes the
// request identity and carries internally consistent usage, timing and evidence
// fields. Any mismatch fails closed.
func validateInferenceResult(result *operatorv1.InferenceResult, req DispatchInferenceRequest) error {
	if err := models.ValidateDecisionCompletion(req.Decision, result); err != nil {
		return err
	}
	if result.GetModel() == "" || result.GetRequestedModel() == "" {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if result.GetProviderAttemptId() != req.ProviderAttemptID ||
		result.GetModel() != result.GetRequestedModel() ||
		(req.Model != "" && result.GetRequestedModel() != req.Model) ||
		result.GetCampaignId() != req.CampaignID ||
		result.GetRunId() != req.RunID ||
		result.GetAssignmentId() != req.AssignmentID ||
		result.GetEvaluationAttemptId() != req.EvaluationAttemptID ||
		result.GetScenarioId() != req.ScenarioID ||
		result.GetRequestedModelDigest() != req.ModelDigest ||
		result.GetModelRegistryDigest() != req.ModelRegistryDigest ||
		result.GetRetryCount() != req.RetryCount {
		return constants.ErrInferenceIdentityMismatch
	}
	if result.GetRetryClassification() != models.ClassifyRetry(req.RetryCount) {
		return constants.ErrInferenceIdentityMismatch
	}
	if result.GetLoadState() != models.ClassifyLoadState(result.LoadDurationNs) {
		return constants.ErrInferenceIdentityMismatch
	}
	if !models.IsSHA256Hex(result.GetNormalizedRequestHash()) || !models.IsSHA256Hex(result.GetOutputHash()) {
		return constants.ErrInferenceEvidenceHashInvalid
	}
	if req.ModelDigest != "" && result.GetServedModelDigest() != req.ModelDigest {
		return constants.ErrInferenceIdentityMismatch
	}
	if result.GetServedModelDigest() != "" && !models.IsSHA256Hex(result.GetServedModelDigest()) {
		return constants.ErrInferenceEvidenceHashInvalid
	}
	if result.GetPromptTokens() < 0 || result.GetCompletionTokens() < 0 || result.GetTotalTokens() < 0 {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if result.GetUsageReported() {
		if result.GetTotalTokens() != result.GetPromptTokens()+result.GetCompletionTokens() {
			return constants.ErrInferenceProviderResponseInvalid
		}
	} else if result.GetPromptTokens() != 0 || result.GetCompletionTokens() != 0 || result.GetTotalTokens() != 0 || result.ThinkingTokens != nil || result.CacheTokens != nil {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if result.ThinkingTokens != nil && result.GetThinkingTokens() < 0 {
		return constants.ErrInferenceProviderResponseInvalid
	}
	if result.CacheTokens != nil && result.GetCacheTokens() < 0 {
		return constants.ErrInferenceProviderResponseInvalid
	}
	durations := []*int64{
		result.LoadDurationNs,
		result.PromptEvalDurationNs,
		result.GenerationDurationNs,
		result.TotalDurationNs,
		result.TimeToFirstTokenNs,
	}
	hasTiming := false
	for _, duration := range durations {
		if duration == nil {
			continue
		}
		hasTiming = true
		if *duration < 0 {
			return constants.ErrInferenceProviderResponseInvalid
		}
	}
	if hasTiming {
		if result.GetTimingSource() != operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_PROVIDER {
			return constants.ErrInferenceProviderResponseInvalid
		}
	} else if result.GetTimingSource() != operatorv1.InferenceTimingSource_INFERENCE_TIMING_SOURCE_UNSPECIFIED {
		return constants.ErrInferenceProviderResponseInvalid
	}
	return nil
}

// resolveInferenceOperator resolves the Inference Node's operator session
// from the requestor's enrolled operators. With an explicit
// TargetOperatorSessionID the target must appear in the requestor's
// operator list (ownership) and carry inference capability; without one,
// exactly one inference-capable session must exist, where dedicated
// Operators take precedence over the embedded fallback. Terminated operators
// are never selectable, and neither is any Operator whose status is not active:
// the Gateway moves an Operator with no heartbeat for over a minute to stale,
// so a stale, stopped, or offline session cannot take or contest a dispatch.
// Returns ErrInferenceOperatorNotFound for no match,
// ErrInferenceOperatorNotCapable for an explicit target that lacks the
// capability, and ErrInferenceOperatorAmbiguous for multiple matches with
// no explicit target.
func (s *DispatchService) resolveInferenceOperator(ctx context.Context, req DispatchInferenceRequest) (string, error) {
	operators, err := s.operatorList.ListUserOperators(ctx, req.RequestorUserID)
	if err != nil {
		return "", err
	}

	capable := func(op *operatorv1.OperatorDocument) bool {

		if req.Decision != nil {
			if op.GetRuntimeConfig().GetInferenceDecisionBackend() == "" || !slices.Contains(op.GetRuntimeConfig().GetInferenceDecisionModels(), req.Model) {
				return false
			}
			if req.Decision.GetAllowHosted() && !op.GetRuntimeConfig().GetInferenceAllowHostedDecision() {
				return false
			}
		} else if op.GetRuntimeConfig().GetInferenceDecisionOnly() {
			return false
		}
		return op.RuntimeConfig != nil &&
			operatorcapability.GetOperatorCapabilities(op).Has(constants.OperatorCapabilityInference) &&
			op.OperatorSessionId != "" &&
			constants.OperatorStatus(op.Status) == constants.OperatorStatusActive
	}

	if req.TargetOperatorID != "" {
		var selected string
		for _, op := range operators {
			if op.GetId() != req.TargetOperatorID || !capable(op) {
				continue
			}
			if req.TargetOperatorSessionID != "" && op.GetOperatorSessionId() != req.TargetOperatorSessionID {
				continue
			}
			if selected != "" {
				return "", constants.ErrInferenceOperatorAmbiguous
			}
			selected = op.GetOperatorSessionId()
		}
		if selected == "" {
			return "", constants.ErrInferenceOperatorNotFound
		}
		return selected, nil
	}
	if req.TargetOperatorSessionID != "" {
		for i := range operators {
			if operators[i].OperatorSessionId == req.TargetOperatorSessionID {
				if !capable(operators[i]) {
					return "", constants.ErrInferenceOperatorNotCapable
				}
				return operators[i].OperatorSessionId, nil
			}
		}
		// The target is not in the requestor's operator list. Report
		// not-found rather than a distinct ownership error so the response
		// does not disclose whether the session exists for another user.
		return "", constants.ErrInferenceOperatorNotFound
	}

	var matches []*operatorv1.OperatorDocument
	for i := range operators {
		if capable(operators[i]) {
			matches = append(matches, operators[i])
		}
	}
	matches = operatorcapability.PreferDedicated(matches)
	switch len(matches) {
	case 0:
		return "", constants.ErrInferenceOperatorNotFound
	case 1:
		return matches[0].OperatorSessionId, nil
	default:
		return "", constants.ErrInferenceOperatorAmbiguous
	}
}

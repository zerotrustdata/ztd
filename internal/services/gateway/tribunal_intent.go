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
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/governance"
	"github.com/zerotrustdata/ztd/internal/services/inference/dispatch"
	"github.com/zerotrustdata/ztd/internal/services/logging"
	"github.com/zerotrustdata/ztd/internal/services/tribunal"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// TribunalIntentControllerDeps groups all dependencies for
// TribunalIntentController.
type TribunalIntentControllerDeps struct {
	CommandDispatchSvc   *DispatchService
	InferenceDispatchSvc *dispatch.DispatchService
	Responder            *response.Writer
	Logger               *slog.Logger

	// MaxPayload is the bounded request-body limit in bytes, wired from
	// cfg.Gateway.MaxPayloadBytes.
	MaxPayload int64
}

// TribunalIntentController handles POST /api/v1/operators/intents: the
// Gateway's Tribunal deliberates one command intent, each member call a
// governed INFERENCE dispatch under the caller's identity, and an approved
// command is dispatched as an ordinary governed EXECUTE_BASH. The Tribunal
// acts in front of envelope construction; it never supplies or replaces an
// L2 quorum proof or an L3 approval.
type TribunalIntentController struct {
	commands   *DispatchService
	inference  *dispatch.DispatchService
	responder  *response.Writer
	logger     *slog.Logger
	maxPayload int64
}

// newTribunalIntentController creates a TribunalIntentController from its deps.
func newTribunalIntentController(d TribunalIntentControllerDeps) *TribunalIntentController {
	return &TribunalIntentController{
		commands:   d.CommandDispatchSvc,
		inference:  d.InferenceDispatchSvc,
		responder:  d.Responder,
		logger:     d.Logger,
		maxPayload: d.MaxPayload,
	}
}

// HandleIntent is the HTTP handler for POST /api/v1/operators/intents.
//
// A deliberated intent is always a 200 TribunalIntentResult: the outcome, the
// persona steps, every member inference with its signed receipt, and for an
// approved command its governed EXECUTE_BASH outcome. Only a request refused
// before deliberation is an IngressRefusal.
//
// @Summary		Deliberate and dispatch a command intent
// @Description	Platform-internal endpoint (mTLS, app workload). The Gateway's Tribunal turns a
// @Description	plain-language command intent into one audited shell command through governed
// @Description	inference, then dispatches it to the target Operator as an ordinary governed
// @Description	EXECUTE_BASH. Under ratify or notary posture the command dispatch is suspended
// @Description	until a human approves or denies it. The Gateway never mints the approval proof.
// @Tags			operators
// @Accept			json
// @Produce		json
// @Param			request	body		tribunalv1.TribunalIntentRequest	true	"Command intent and member inference template"
// @Success		200		{object}	tribunalv1.TribunalIntentResult	"Deliberation record and, for an approved command, its governed outcome"
// @Failure		400		{object}	operatorv1.IngressRefusal	"Bad Request — malformed body, missing field, or a member template that sets a Gateway-owned field"
// @Failure		401		{object}	operatorv1.IngressRefusal	"Unauthorized — missing delegated user identity"
// @Failure		403		{object}	operatorv1.IngressRefusal	"Forbidden — not an app workload"
// @Failure		405		{object}	operatorv1.IngressRefusal	"Method Not Allowed"
// @Failure		413		{object}	operatorv1.IngressRefusal	"Request Entity Too Large — body exceeds the payload limit"
// @Router			/api/v1/operators/intents [post]
func (c *TribunalIntentController) HandleIntent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.ProtoJSONCanonical(w, http.StatusMethodNotAllowed, governance.NewIngressRefusal(constants.ErrMethodNotAllowed))
		return
	}
	appID, userID, ok := requireAppIdentity(c.responder, w, r)
	if !ok {
		return
	}
	body, err := readRequestBody(r, c.maxPayload)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, constants.ErrPayloadExceedsLimit) {
			status = http.StatusRequestEntityTooLarge
		}
		c.responder.ProtoJSONCanonical(w, status, governance.NewIngressRefusal(err))
		return
	}
	req := &tribunalv1.TribunalIntentRequest{}
	if err := protojson.Unmarshal(body, req); err != nil {
		err = fmt.Errorf("tribunal intent: decode request: %w: %w", constants.ErrInvalidJSONBody, err)
		c.responder.ProtoJSONCanonical(w, http.StatusBadRequest, governance.NewIngressRefusal(err))
		return
	}
	if err := tribunal.ValidateIntentRequest(req); err != nil {
		c.responder.ProtoJSONCanonical(w, http.StatusBadRequest, governance.NewIngressRefusal(err))
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		c.logger.Warn("tribunal intent: clear response deadline", logging.ErrorKind(err))
	}

	members := &tribunalMemberDispatcher{inference: c.inference, appID: appID, userID: userID, logger: c.logger}
	result, err := tribunal.NewService(members).Deliberate(r.Context(), req)
	if err != nil {
		c.logger.Error("tribunal intent: deliberation failed", logging.ErrorKind(err))
		c.responder.ProtoJSONCanonical(w, http.StatusInternalServerError, governance.NewIngressRefusal(err))
		return
	}
	if result.GetOutcome() == tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED {
		c.dispatchCommand(r.Context(), req, result, appID, userID)
	}
	c.responder.ProtoJSONCanonical(w, http.StatusOK, result)
}

// dispatchCommand runs the approved command as an ordinary governed
// EXECUTE_BASH under the caller's identity and records its outcome on result.
func (c *TribunalIntentController) dispatchCommand(ctx context.Context, req *tribunalv1.TribunalIntentRequest, result *tribunalv1.TribunalIntentResult, appID, userID string) {
	dispatched, err := c.executeBash(ctx, req, result.GetCommand(), appID, userID)
	if err == nil {
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: dispatched.GovernedDispatchResult()}
		return
	}
	// As on POST /api/v1/operators/commands, the caller receives the signed
	// rejection receipt or the typed sentinel, never the wrapped chain.
	c.logger.Error("tribunal intent: command dispatch failed", logging.ErrorKind(err))
	if rejection := governance.RejectionResponse(err); rejection != nil {
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Rejection{Rejection: rejection}
		return
	}
	_, refusal := dispatchErrorCode(err)
	result.CommandOutcome = &tribunalv1.TribunalIntentResult_Refusal{Refusal: refusal}
}

func (c *TribunalIntentController) executeBash(ctx context.Context, req *tribunalv1.TribunalIntentRequest, command, appID, userID string) (*DispatchResult, error) {
	eventType, err := constants.RequestEventForAction(constants.ActionTypeExecuteBash)
	if err != nil {
		return nil, fmt.Errorf("tribunal intent: resolve EXECUTE_BASH event: %w", err)
	}
	payload, err := proto.Marshal(&operatorv1.CommandRequested{
		Command:          command,
		ExecutionId:      req.GetExecutionId(),
		Justification:    req.GetRequest(),
		TimeoutSeconds:   req.GetTimeoutSeconds(),
		WorkingDirectory: req.GetWorkingDirectory(),
	})
	if err != nil {
		return nil, fmt.Errorf("tribunal intent: marshal command: %w", err)
	}
	return c.commands.Dispatch(ctx, DispatchRequest{
		TargetOperatorSessionID: req.GetTargetOperatorSessionId(),
		EventType:               string(eventType),
		Payload:                 payload,
		TargetResource:          req.GetWorkingDirectory(),
		RequestorUserID:         userID,
		ActingAppID:             appID,
		CaseID:                  req.GetCaseId(),
		InvestigationID:         req.GetInvestigationId(),
		TaskID:                  req.GetTaskId(),
		CliSessionID:            req.GetCliSessionId(),
	})
}

// tribunalMemberDispatcher runs each Tribunal member call through the
// governed inference dispatch path under the intent caller's identity, the
// same path POST /api/v1/inference/dispatch takes.
type tribunalMemberDispatcher struct {
	inference *dispatch.DispatchService
	appID     string
	userID    string
	logger    *slog.Logger
}

func (m *tribunalMemberDispatcher) DispatchMember(ctx context.Context, req *operatorv1.InferenceDispatchRequest) (*operatorv1.InferenceDispatchResponse, *operatorv1.InferenceDispatchFailure) {
	dispatchReq, err := newDispatchInferenceRequest(req, m.appID, m.userID)
	if err != nil {
		return nil, &operatorv1.InferenceDispatchFailure{Error: err.Error(), Code: dispatch.FailureCode(err)}
	}
	result, err := m.inference.DispatchInference(ctx, dispatchReq)
	if err != nil {
		_, failure := inferenceDispatchFailure(err)
		m.logger.Error("tribunal intent: member inference failed", slog.String("failure_code", failure.GetCode().String()),
			slog.String("provider_attempt_id", req.GetProviderAttemptId()), slog.String("transaction_id", failure.GetReceipt().GetTransactionId()), logging.ErrorKind(err))
		return nil, failure
	}
	return result.Response(), nil
}

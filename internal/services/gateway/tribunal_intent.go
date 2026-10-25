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
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
	tribunalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/tribunal/v1"
)

// tribunalCommandDispatcher is the governed execution boundary used by the intent controller.
type tribunalCommandDispatcher interface {
	Dispatch(context.Context, DispatchRequest) (*DispatchResult, error)
}

// tribunalAdmissionReviewer is the Tribunal's final contextual review of an
// admitted envelope (tribunal.Service.ReviewAdmitted).
type tribunalAdmissionReviewer interface {
	ReviewAdmitted(context.Context, *tribunalv1.TribunalIntentRequest, *tribunalv1.TribunalIntentResult, *commonv1.GovernanceEnvelope, []*operatorv1.ConsensusVoteInference) error
}

// TribunalIntentControllerDeps groups all dependencies for
// TribunalIntentController.
type TribunalIntentControllerDeps struct {
	CommandDispatchSvc   tribunalCommandDispatcher
	InferenceDispatchSvc *dispatch.DispatchService
	Doctrine             *governance.L1Doctrine
	Responder            *response.Writer
	Logger               *slog.Logger

	// MaxPayload is the bounded request-body limit in bytes, wired from
	// cfg.Gateway.MaxPayloadBytes.
	MaxPayload int64
}

// TribunalIntentController handles POST /api/v1/operators/intents: the
// Gateway's Tribunal deliberates one command intent, each member call a
// governed INFERENCE dispatch under the caller's identity, and an approved
// command is dispatched as an ordinary governed MCP_CALL run_shell_command. The Tribunal
// acts in front of envelope construction; it never supplies or replaces an
// L2 quorum proof or an L3 approval.
type TribunalIntentController struct {
	commands   tribunalCommandDispatcher
	inference  *dispatch.DispatchService
	doctrine   *governance.L1Doctrine
	responder  *response.Writer
	logger     *slog.Logger
	maxPayload int64
}

// newTribunalIntentController creates a TribunalIntentController from its deps.
func newTribunalIntentController(d TribunalIntentControllerDeps) *TribunalIntentController {
	return &TribunalIntentController{
		commands:   d.CommandDispatchSvc,
		inference:  d.InferenceDispatchSvc,
		doctrine:   d.Doctrine,
		responder:  d.Responder,
		logger:     d.Logger,
		maxPayload: d.MaxPayload,
	}
}

// HandleIntent is the HTTP handler for POST /api/v1/operators/intents.
//
// A deliberated intent is always a 200 TribunalIntentResult: the outcome, the
// persona steps, every member inference with its signed receipt, and for an
// approved command its governed MCP_CALL run_shell_command outcome. Only a request refused
// before deliberation is an IngressRefusal.
//
// @Summary		Deliberate and dispatch a command intent
// @Description	Platform-internal endpoint (mTLS, app workload). The Gateway's Tribunal turns a
// @Description	plain-language command intent into one audited shell command through governed
// @Description	inference, then dispatches it to the target Operator as an ordinary governed
// @Description	MCP_CALL run_shell_command. Under ratify or notary posture the command dispatch is suspended
// @Description	until a human approves or denies it. The Gateway never mints the approval proof.
// @Tags			operators
// @Accept			json
// @Produce		json
// @Param			request	body		tribunalv1.TribunalIntentRequest	true	"Command intent and member inference template"
// @Success		200		{object}	tribunalv1.TribunalIntentResult	"Deliberation record and, for an approved command, its governed outcome"
// @Failure		400		{object}	operatorv1.IngressRefusal	"Bad Request — malformed body, missing field, a member template that sets a Gateway-owned field, or a member template role that does not run the requested tier"
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
	service, err := tribunal.NewService(members, c.doctrine)
	if err != nil {
		c.logger.Error("tribunal intent: construct service", logging.ErrorKind(err))
		c.responder.ProtoJSONCanonical(w, http.StatusInternalServerError, governance.NewIngressRefusal(err))
		return
	}
	result, err := service.Deliberate(r.Context(), req)
	if err != nil {
		c.logger.Error("tribunal intent: deliberation failed", logging.ErrorKind(err))
		c.responder.ProtoJSONCanonical(w, http.StatusInternalServerError, governance.NewIngressRefusal(err))
		return
	}
	c.dispatchCommand(r.Context(), req, result, appID, userID, service)
	c.responder.ProtoJSONCanonical(w, http.StatusOK, result)
}

// dispatchCommand runs the approved command as an ordinary governed
// MCP_CALL run_shell_command under the caller's identity and records its outcome on result.
// Once the envelope is admitted, reviewer gives the Auditor the actual
// governance decisions before publish. A revised command is never published
// under the earlier decisions: it is dispatched as a new envelope that needs
// its own L1, L2 and L3 admission and its own review, up to
// tribunal.MaxAdmissionReviews envelopes in total.
func (c *TribunalIntentController) dispatchCommand(ctx context.Context, req *tribunalv1.TribunalIntentRequest, result *tribunalv1.TribunalIntentResult, appID, userID string, reviewer tribunalAdmissionReviewer) {
	if result.GetOutcome() != tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED {
		return
	}
	review := func(ctx context.Context, env *commonv1.GovernanceEnvelope, votes []*operatorv1.ConsensusVoteInference) error {
		return reviewer.ReviewAdmitted(ctx, req, result, env, votes)
	}
	var dispatched *DispatchResult
	var err error
	for range tribunal.MaxAdmissionReviews {
		dispatched, err = c.dispatchShellCommand(ctx, req, result.GetArgumentsJson(), appID, userID, review)
		if err != nil {
			result.ConsensusInferences = append(result.ConsensusInferences, governance.ConsensusInferences(err)...)
		} else if dispatched != nil {
			result.ConsensusInferences = append(result.ConsensusInferences, dispatched.ConsensusInferences...)
		}
		if !errors.Is(err, constants.ErrTribunalCommandRevised) {
			break
		}
	}
	if errors.Is(err, constants.ErrTribunalCommandRevised) {
		result.Outcome = tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_AUDIT_FAILED
		result.AuditFailure = "contextual Auditor kept changing the authorized command"
	}
	if result.GetOutcome() != tribunalv1.TribunalOutcome_TRIBUNAL_OUTCOME_APPROVED && governance.RejectionResponse(err) == nil {
		result.Arguments, result.ArgumentsJson = nil, ""
		return
	}
	if err == nil {
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Dispatched{Dispatched: dispatched.GovernedDispatchResult()}
		return
	}
	// As on POST /api/v1/operators/commands, the caller receives the signed
	// rejection receipt or the typed sentinel, never the wrapped chain.
	// The typed code and receipt ID name the failure without the command text
	// the wrapped chain carries (INV-STOR-ENC-01).
	if rejection := governance.RejectionResponse(err); rejection != nil {
		c.logger.Error("tribunal intent: command dispatch rejected", slog.String("transaction_id", rejection.GetReceipt().GetTransactionId()), logging.ErrorKind(err))
		result.CommandOutcome = &tribunalv1.TribunalIntentResult_Rejection{Rejection: rejection}
		return
	}
	_, refusal := dispatchErrorCode(err)
	c.logger.Error("tribunal intent: command dispatch refused", slog.String("refusal_code", refusal.GetCode().String()), logging.ErrorKind(err))
	result.CommandOutcome = &tribunalv1.TribunalIntentResult_Refusal{Refusal: refusal}
}

func (c *TribunalIntentController) dispatchShellCommand(ctx context.Context, req *tribunalv1.TribunalIntentRequest, argumentsJSON, appID, userID string, review func(context.Context, *commonv1.GovernanceEnvelope, []*operatorv1.ConsensusVoteInference) error) (*DispatchResult, error) {
	eventType, err := constants.RequestEventForAction(constants.ActionTypeMcpCall)
	if err != nil {
		return nil, fmt.Errorf("tribunal intent: resolve MCP_CALL event: %w", err)
	}
	payload, err := proto.Marshal(&operatorv1.McpCallRequested{
		ToolName:      constants.MCPToolRunShellCommand,
		ArgumentsJson: argumentsJSON,
		ExecutionId:   req.GetExecutionId(),
	})
	if err != nil {
		return nil, fmt.Errorf("tribunal intent: marshal command: %w", err)
	}
	return c.commands.Dispatch(ctx, DispatchRequest{
		TargetOperatorSessionID: req.GetTargetOperatorSessionId(),
		EventType:               string(eventType),
		Payload:                 payload,
		TargetResource:          constants.MCPToolRunShellCommand,
		RequestorUserID:         userID,
		ActingAppID:             appID,
		CaseID:                  req.GetCaseId(),
		InvestigationID:         req.GetInvestigationId(),
		TaskID:                  req.GetTaskId(),
		EvaluationRunID:         req.GetMemberInference().GetRunId(),
		EvaluationAssignmentID:  req.GetMemberInference().GetAssignmentId(),
		CliSessionID:            req.GetCliSessionId(),
		OnAdmitted:              review,
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

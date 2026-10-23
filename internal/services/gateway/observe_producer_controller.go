// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
)

// ObserveProducerControllerDeps groups all dependencies for
// ObserveProducerController.
type ObserveProducerControllerDeps struct {
	Cfg          *config.Config
	Logger       *slog.Logger
	ProducerSvc  *ObserveProducerService
	Responder    *response.Writer
	MaxBodyBytes int64
}

// ObserveProducerController handles the mTLS producer endpoints that app
// workloads call to report agent and run state changes. The gateway
// derives user_id from the mTLS peer certificate (stamped into context by
// handleAppAuth from the delegated user SAN), never from the request body.
// Only app workload identities may call these endpoints; the controller
// rejects operator and CLI certs with 403.
type ObserveProducerController struct {
	cfg          *config.Config
	logger       *slog.Logger
	producerSvc  *ObserveProducerService
	responder    *response.Writer
	maxBodyBytes int64
}

func newObserveProducerController(deps ObserveProducerControllerDeps) *ObserveProducerController {
	maxBody := deps.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 512 * 1024
	}
	return &ObserveProducerController{
		cfg:          deps.Cfg,
		logger:       deps.Logger,
		producerSvc:  deps.ProducerSvc,
		responder:    deps.Responder,
		maxBodyBytes: maxBody,
	}
}

// requireAppUserID extracts the authenticated user ID from the request
// context and verifies the caller is an app workload via the shared
// requireAppIdentity helper. Returns the user ID and true on success. On
// failure the helper writes a 401 (missing user_id) or 403 (not an app
// workload) response and this returns false.
func (c *ObserveProducerController) requireAppUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	_, userID, ok := requireAppIdentity(c.responder, w, r)
	return userID, ok
}

// buildProducerRoute constructs an SSERoute from the derived user_id and
// the request body's session routing fields. Exactly one of web_session_id
// or cli_session_id must be set; the caller rejects dual routing before
// calling this assembler. The route is validated by the producer service;
// this function only assembles it.
func buildProducerRoute(userID, webSessionID, cliSessionID string) SSERoute {
	route := SSERoute{UserID: userID}
	webSessionID = strings.TrimSpace(webSessionID)
	cliSessionID = strings.TrimSpace(cliSessionID)
	if webSessionID != "" {
		route.WebSessionID = webSessionID
	} else {
		route.CLISessionID = cliSessionID
	}
	return route
}

// validateProducerRouting rejects a producer request that provides both
// routing targets or neither. Exactly one of web_session_id or
// cli_session_id is required at the Gateway boundary so the gateway never
// silently picks one when both are supplied.
func validateProducerRouting(webSessionID, cliSessionID string) error {
	webSessionID = strings.TrimSpace(webSessionID)
	cliSessionID = strings.TrimSpace(cliSessionID)
	n := 0
	if webSessionID != "" {
		n++
	}
	if cliSessionID != "" {
		n++
	}
	switch n {
	case 0:
		return constants.ErrGatewaySSERouteSessionRequired
	case 1:
		return nil
	default:
		return constants.ErrGatewaySSERouteSessionMutuallyExclusive
	}
}

// handleAgentState accepts a typed agent state update from an app workload,
// derives user_id from the mTLS peer certificate, and delegates to the
// observe producer service which persists the projection and emits the
// app.agent.status.updated SSE event (persist-before-publish).
//
// @Summary		Push observe agent state
// @Description	Accepts a typed agent state update from an mTLS-authenticated app workload. The gateway derives user_id from the peer certificate, persists the agent state projection, and emits an app.agent.status.updated SSE event after successful persistence.
// @Tags			observe
// @Accept			json
// @Produce		json
// @Param			payload	body		models.ObserveProducerAgentStateRequest	true	"Agent state update"
// @Success		200		{object}	models.ObserveProducerResponse			"accepted"
// @Failure		400		{object}	models.ObserveProducerRejection			"Invalid transition, stale update, or missing fields"
// @Failure		401		{string}	string									"Unauthorized — mTLS user identity required"
// @Failure		403		{string}	string									"Forbidden — not an app workload or cross-user"
// @Failure		500		{object}	models.ObserveProducerRejection			"Persistence failure"
// @Router			/api/v1/observe/producer/agent-state [post]
func (c *ObserveProducerController) handleAgentState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	userID, ok := c.requireAppUserID(w, r)
	if !ok {
		return
	}
	body, err := readRequestBody(r, c.maxBodyBytes)
	if err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	var req models.ObserveProducerAgentStateRequest
	if err := decodeProducerRequest(body, &req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateAgentProducerRequest(req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateProducerRouting(req.WebSessionID, req.CLISessionID); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	route := buildProducerRoute(userID, req.WebSessionID, req.CLISessionID)
	payload := models.AgentStatusUpdatedPayload{
		SchemaVersion: req.SchemaVersion,
		AgentID:       req.AgentID,
		DisplayName:   req.DisplayName,
		Role:          req.Role,
		Status:        req.Status,
		RunID:         req.RunID,
		TaskID:        req.TaskID,
		Model:         req.Model,
		ObservedAt:    req.ObservedAt,
	}
	if err := c.producerSvc.UpdateAgentState(r.Context(), userID, route, payload); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	c.responder.JSON(w, http.StatusOK, models.ObserveProducerResponse{Accepted: true})
}

// handleRunState accepts a typed run state update from an app workload,
// derives user_id from the mTLS peer certificate, and delegates to the
// observe producer service which persists the projection and emits the
// app.run.status.updated SSE event (persist-before-publish).
//
// @Summary		Push observe run state
// @Description	Accepts a typed run state update from an mTLS-authenticated app workload. The gateway derives user_id from the peer certificate, persists the run state projection, and emits an app.run.status.updated SSE event after successful persistence. A request naming reopens replaces exactly that terminal projected status with a non-terminal one; it is a producer repair for a projection its local store contradicts.
// @Tags			observe
// @Accept			json
// @Produce		json
// @Param			payload	body		models.ObserveProducerRunStateRequest	true	"Run state update"
// @Success		200		{object}	models.ObserveProducerResponse			"accepted"
// @Failure		400		{object}	models.ObserveProducerRejection			"Invalid transition, stale update, or missing fields"
// @Failure		401		{string}	string									"Unauthorized — mTLS user identity required"
// @Failure		403		{string}	string									"Forbidden — not an app workload or cross-user"
// @Failure		500		{object}	models.ObserveProducerRejection			"Persistence failure"
// @Router			/api/v1/observe/producer/run-state [post]
func (c *ObserveProducerController) handleRunState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	userID, ok := c.requireAppUserID(w, r)
	if !ok {
		return
	}
	body, err := readRequestBody(r, c.maxBodyBytes)
	if err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	var req models.ObserveProducerRunStateRequest
	if err := decodeProducerRequest(body, &req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateRunProducerRequest(req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateProducerRouting(req.WebSessionID, req.CLISessionID); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	route := buildProducerRoute(userID, req.WebSessionID, req.CLISessionID)
	payload := models.RunStatusUpdatedPayload{
		SchemaVersion:  req.SchemaVersion,
		RunID:          req.RunID,
		RunKind:        req.RunKind,
		DisplayName:    req.DisplayName,
		Status:         req.Status,
		ActiveTaskID:   req.ActiveTaskID,
		CompletedTasks: req.CompletedTasks,
		TotalTasks:     req.TotalTasks,
		StartedAt:      req.StartedAt,
		EndedAt:        req.EndedAt,
		ObservedAt:     req.ObservedAt,
	}
	if req.Reopens != "" {
		err = c.producerSvc.ReopenRunState(r.Context(), userID, route, payload, req.Reopens)
	} else {
		err = c.producerSvc.UpdateRunState(r.Context(), userID, route, payload)
	}
	if err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	c.responder.JSON(w, http.StatusOK, models.ObserveProducerResponse{Accepted: true})
}

// handleStop accepts a typed stop request for one campaign run from an app
// workload, derives user_id from the mTLS peer certificate, and delegates to
// the observe producer service which persists the supervisor state projection
// and emits the run-scoped ai.eval.stop.requested SSE event
// (persist-before-publish).
//
// @Summary		Request observe campaign run stop
// @Description	Accepts a typed stop request for one campaign run from an mTLS-authenticated app workload. The gateway derives user_id from the peer certificate, persists the supervisor state projection, and emits an ai.eval.stop.requested SSE event carrying run_id to the routed session after successful persistence.
// @Tags			observe
// @Accept			json
// @Produce		json
// @Param			payload	body		models.ObserveProducerStopRequest	true	"Stop request"
// @Success		200		{object}	models.ObserveProducerResponse		"accepted"
// @Failure		400		{object}	models.ObserveProducerRejection		"Invalid stop reason or scope, or missing campaign_id or run_id"
// @Failure		401		{string}	string								"Unauthorized — mTLS user identity required"
// @Failure		403		{string}	string								"Forbidden — not an app workload or cross-user"
// @Failure		500		{object}	models.ObserveProducerRejection		"Persistence failure"
// @Router			/api/v1/observe/producer/stop [post]
func (c *ObserveProducerController) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	userID, ok := c.requireAppUserID(w, r)
	if !ok {
		return
	}
	body, err := readRequestBody(r, c.maxBodyBytes)
	if err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	var req models.ObserveProducerStopRequest
	if err := decodeProducerRequest(body, &req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateStopProducerRequest(req); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	if err := validateProducerRouting(req.WebSessionID, req.CLISessionID); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	route := buildProducerRoute(userID, req.WebSessionID, req.CLISessionID)
	if err := c.producerSvc.RequestStop(r.Context(), userID, route, "", req.CampaignID, req.RunID, req.StopReason, req.StopScope); err != nil {
		c.rejectProducerRequest(w, r.URL.Path, err)
		return
	}
	c.responder.JSON(w, http.StatusOK, models.ObserveProducerResponse{Accepted: true})
}

// rejectProducerRequest answers a failed producer request with a typed
// models.ObserveProducerRejection and logs the full error, since a caller that
// treats the status as best-effort never surfaces it. Cross-user ownership
// misses (agent or run) are 403 forbidden without detail, so the boundary does
// not reveal record existence. An unclassified failure (persistence, SSE
// emission) is 500 internal. Every other rejection is 400.
func (c *ObserveProducerController) rejectProducerRequest(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, constants.ErrObserveAgentNotFound) || errors.Is(err, constants.ErrObserveRunNotFound) {
		c.logger.Warn("observe producer: request forbidden", "operation", operation, "error", err)
		c.responder.JSON(w, http.StatusForbidden, models.NewObserveProducerRejection(constants.ErrForbidden))
		return
	}
	rejection := models.NewObserveProducerRejection(err)
	if rejection.Code == models.ObserveProducerRejectionInternal {
		c.logger.Error("observe producer: request failed", "operation", operation, "error", err)
		c.responder.JSON(w, http.StatusInternalServerError, rejection)
		return
	}
	c.logger.Warn("observe producer: request rejected", "operation", operation, "error", err)
	c.responder.JSON(w, http.StatusBadRequest, rejection)
}

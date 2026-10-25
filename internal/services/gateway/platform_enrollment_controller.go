// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
)

// PlatformEnrollmentController handles the owner-approved platform
// workload enrollment flow: request creation, decision waiting,
// proof-of-possession-gated completion, authenticated pending-list
// discovery, and owner decisions (approve/deny).
//
// Auth classification (enforced by the unified auth middleware via
// NewRouteAuthRegistry):
//   - request   (POST /api/v1/auth/platform-enrollments/request):    RouteAuthNone
//     (public discovery surface; bootstrap state is checked by the service)
//   - status    (GET  /api/v1/auth/platform-enrollments/status):     RouteAuthNone
//     (public; the opaque token is the lookup key)
//   - complete  (POST /api/v1/auth/platform-enrollments/complete):   RouteAuthNone
//     (public; requires the opaque token AND valid proof-of-possession
//     signatures over the canonical completion transcript for every key)
//   - pending   (GET  /api/v1/auth/platform-enrollments/pending):    RouteAuthDual
//     (owner: web session cookie or mTLS CLI; active-first-user enforced)
//   - enrolled  (GET  /api/v1/auth/platform-enrollments/enrolled):   RouteAuthDual
//     (owner: web session cookie or mTLS CLI; active-first-user enforced)
//   - decision  (POST /api/v1/auth/platform-enrollments/decision):   RouteAuthDual
//     (owner: web session cookie or mTLS CLI; active-first-user enforced)
//
// request, status, and complete are registered on both the HTTPS router
// and the plain HTTP discovery router (an unenrolled workload has no
// client certificate). pending and decision are registered on HTTPS only.
type PlatformEnrollmentController struct {
	cfg       *config.Config
	logger    *slog.Logger
	enrollSvc *PlatformEnrollmentService
	userSvc   *UserService
	reg       *RegistrationService
	responder *response.Writer
	consensus *ConsensusStoreService
}

// PlatformEnrollmentControllerDeps groups all dependencies for
// PlatformEnrollmentController. Reg records the runtime config an Operator
// declares on the bootstrap websocket.
type PlatformEnrollmentControllerDeps struct {
	Cfg       *config.Config
	Logger    *slog.Logger
	EnrollSvc *PlatformEnrollmentService
	UserSvc   *UserService
	Reg       *RegistrationService
	Responder *response.Writer
	// Consensus supplies the L2 trust delivered in Operator bootstrap bundles.
	Consensus *ConsensusStoreService
}

func newPlatformEnrollmentController(deps PlatformEnrollmentControllerDeps) *PlatformEnrollmentController {
	return &PlatformEnrollmentController{
		cfg:       deps.Cfg,
		logger:    deps.Logger,
		enrollSvc: deps.EnrollSvc,
		userSvc:   deps.UserSvc,
		reg:       deps.Reg,
		responder: deps.Responder,
		consensus: deps.Consensus,
	}
}

// handlePlatformEnrollmentRequest creates a new pending platform
// enrollment request. The requester (dashboard, application, or operator)
// generates its key pair(s), builds a CSR, and posts the typed request.
// The gateway validates bootstrap state and CSRs, stores the requester-provided
// token hash, and returns the request ID, approval URL, fingerprints, and expiry.
// Resubmitting the same hash and CSRs recovers the live request; token conflicts
// return HTTP 409. The raw token never reaches the Gateway during creation.
// @Summary Create or recover a platform enrollment request
// @Description The requester supplies its SHA-256 token hash. Resubmitting the same hash and CSRs recovers the same live request; token conflicts return 409.
// @Tags Platform enrollment
// @Accept json
// @Produce json
// @Param request body models.PlatformEnrollmentCreateRequest true "Enrollment request"
// @Success 201 {object} models.PlatformEnrollmentCreateResponse
// @Failure 400 {object} map[string]string
// @Failure 403 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /api/v1/auth/platform-enrollments/request [post]
//
// POST /api/v1/auth/platform-enrollments/request  (RouteAuthNone)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var req models.PlatformEnrollmentCreateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}

	if err := req.ValidateShape(); err != nil {
		if errors.Is(err, constants.ErrPlatformEnrollmentInvalidToken) {
			c.responder.Error(w, http.StatusBadRequest, constants.ErrPlatformEnrollmentInvalidToken.Error())
			return
		}
		c.writeEnrollmentError(w, err)
		return
	}
	// An Operator enrolls only over the bootstrap websocket; it makes no
	// HTTP requests to the Gateway.
	if req.ComponentKind == models.PlatformComponentOperator {
		c.writeEnrollmentError(w, constants.ErrPlatformEnrollmentInvalidComponent)
		return
	}

	resp, err := c.enrollSvc.CreateRequest(r.Context(), req, c.approvalURLBase())
	if err != nil {
		c.logger.Warn("platform enrollment: create request failed",
			"error", err,
			"component_kind", string(req.ComponentKind),
			"instance_id", req.InstanceID)
		c.writeEnrollmentError(w, err)
		return
	}

	c.logger.Info("platform enrollment request created via controller",
		"request_id", resp.RequestID,
		"component_kind", string(req.ComponentKind),
		"instance_id", req.InstanceID,
	)

	c.responder.JSON(w, http.StatusCreated, resp)
}

// handlePlatformEnrollmentStatus returns the requester-visible state and
// expiry for a request identified by its opaque token. The token is
// passed as the "token" query parameter and is hashed for lookup; the
// raw token is never stored. The response carries no CSR PEM,
// certificates, or identity details — only state, expiry, and
// retry-after for issuing-state requests. With wait=true, a pending request
// holds the response until a committed decision, expiry, or cancellation.
//
// GET /api/v1/auth/platform-enrollments/status?token=<token>  (RouteAuthNone)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrPlatformEnrollmentTokenRequired.Error())
		return
	}

	var resp *models.PlatformEnrollmentStatusResponse
	var err error
	if r.URL.Query().Get("wait") == "true" {
		// The pending request's expiry bounds this held response, rather than
		// the HTTP server's short absolute write deadline.
		if deadlineErr := http.NewResponseController(w).SetWriteDeadline(time.Time{}); deadlineErr != nil && !errors.Is(deadlineErr, http.ErrNotSupported) {
			c.logger.Warn("platform enrollment: clear status write deadline", "error", deadlineErr)
		}
		resp, err = c.enrollSvc.WaitForDecision(r.Context(), models.PlatformEnrollmentTokenHash(token))
	} else {
		resp, err = c.enrollSvc.GetStatus(r.Context(), token)
	}
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		c.writeEnrollmentError(w, err)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	c.responder.JSON(w, http.StatusOK, resp)
}

// handlePlatformEnrollmentComplete receives the requester's completion after
// approval. The caller proves possession of every
// CSR private key by signing the canonical completion transcript. The
// gateway verifies the token, state, expiry, CSR fingerprints, and
// proofs before issuing or resuming issuance. A completed request
// returns the stored response (idempotent); an issuing request returns
// a typed retryable response.
//
// POST /api/v1/auth/platform-enrollments/complete  (RouteAuthNone)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var req models.PlatformEnrollmentCompleteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}

	if req.Token == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrPlatformEnrollmentTokenRequired.Error())
		return
	}

	resp, err := c.enrollSvc.Complete(r.Context(), req.Token, req.Proofs)
	if err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	c.logger.Info("platform enrollment completed via controller",
		"request_id", resp.RequestID,
		"component_kind", string(resp.ComponentKind),
	)

	w.Header().Set("Cache-Control", "no-store")
	c.responder.JSON(w, http.StatusCreated, resp)
}

// handlePlatformEnrollmentPending returns owner-visible metadata for all
// pending platform enrollment requests. The response never includes
// token hashes, CSR PEM, certificates, or raw tokens. The caller must be
// authenticated as the active first user (enforced here after the auth
// middleware stamps the user ID from the web session or mTLS cert).
//
// GET /api/v1/auth/platform-enrollments/pending  (RouteAuthDual)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrWebSessionAuthRequired.Error())
		return
	}

	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	resp, err := c.enrollSvc.ListPending(r.Context())
	if err != nil {
		c.logger.Error("platform enrollment: list pending failed", "error", err)
		c.responder.Error(w, http.StatusInternalServerError, "failed to list pending requests")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	c.responder.JSON(w, http.StatusOK, resp)
}

// handlePlatformEnrollmentEnrolled returns owner-visible metadata for all
// completed and revoked platform enrollment requests. The response never
// includes token hashes, CSR PEM, certificates, or raw tokens. The caller
// must be authenticated as the active first user.
//
// GET /api/v1/auth/platform-enrollments/enrolled  (RouteAuthDual)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentEnrolled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrWebSessionAuthRequired.Error())
		return
	}

	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	resp, err := c.enrollSvc.ListEnrolled(r.Context())
	if err != nil {
		c.logger.Error("platform enrollment: list enrolled failed", "error", err)
		c.responder.Error(w, http.StatusInternalServerError, "failed to list enrolled requests")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	c.responder.JSON(w, http.StatusOK, resp)
}

// handlePlatformEnrollmentDecision records an owner decision (approve or
// deny) on a pending request. The actor user ID is derived from
// authenticated context (web session or mTLS CLI) by the auth middleware;
// the request body carries only the request ID, typed decision, and
// optional bounded reason. Only the active first user may decide.
//
// POST /api/v1/auth/platform-enrollments/decision  (RouteAuthDual)
func (c *PlatformEnrollmentController) handlePlatformEnrollmentDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var req models.PlatformEnrollmentDecisionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}

	if err := req.Validate(); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrWebSessionAuthRequired.Error())
		return
	}

	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	resp, err := c.enrollSvc.Decide(r.Context(), userID, req)
	if err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	c.logger.Info("platform enrollment decision via controller",
		"request_id", resp.RequestID,
		"state", string(resp.State),
		"actor_user_id", userID,
	)

	c.responder.JSON(w, http.StatusOK, resp)
}

// handlePlatformEnrollmentBatchDecision commits a fixed owner-approved cohort.
// POST /api/v1/auth/platform-enrollments/decisions (RouteAuthDual, HTTPS only).
func (c *PlatformEnrollmentController) handlePlatformEnrollmentBatchDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	if userID == "" {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrWebSessionAuthRequired.Error())
		return
	}
	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req models.PlatformEnrollmentBatchDecisionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}
	if err := req.Validate(); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	resp, err := c.enrollSvc.DecideBatch(r.Context(), userID, req)
	if err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	c.responder.JSON(w, http.StatusOK, resp)
}

// requireActiveFirstUser verifies that the given user ID is the active
// first user (the persistent owner). Returns a typed error if the user
// is not the first user, is not active, or cannot be looked up.
func (c *PlatformEnrollmentController) handlePlatformEnrollmentRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, constants.ErrMethodNotAllowed.Error())
		return
	}
	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}
	var req models.PlatformEnrollmentRevokeRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}
	if err := req.Validate(); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	userID, _ := r.Context().Value(constants.ContextKeyUserID).(string)
	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	resp, err := c.enrollSvc.Revoke(r.Context(), userID, req)
	if err != nil {
		c.writeEnrollmentError(w, err)
		return
	}
	c.responder.JSON(w, http.StatusOK, resp)
}

func (c *PlatformEnrollmentController) requireActiveFirstUser(ctx context.Context, userID string) error {
	user, err := c.userSvc.GetByID(ctx, userID)
	if err != nil {
		c.logger.Error("platform enrollment: failed to look up user", "error", err, "user_id", userID)
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	if user == nil || !user.IsActive() {
		c.logger.Warn("platform enrollment: user is not active", "user_id", userID)
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	isFirst, err := c.userSvc.IsFirstUser(ctx, userID)
	if err != nil {
		c.logger.Error("platform enrollment: failed to check first user", "error", err, "user_id", userID)
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	if !isFirst {
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	return nil
}

// handlePlatformEnrollmentConsensus handles binding and querying the consensus application.
// POST /api/v1/auth/platform-enrollments/consensus: binds an enrolled app as the consensus app.
// GET  /api/v1/auth/platform-enrollments/consensus: returns the bound consensus app.
// RouteAuthDual (active first user owner required).
func (c *PlatformEnrollmentController) handlePlatformEnrollmentConsensus(w http.ResponseWriter, r *http.Request) {
	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrWebSessionAuthRequired.Error())
		return
	}
	if err := c.requireActiveFirstUser(r.Context(), userID); err != nil {
		c.writeEnrollmentError(w, err)
		return
	}

	switch r.Method {
	case http.MethodGet:
		bound, err := c.consensus.GetBoundConsensusApp(r.Context())
		if err != nil {
			c.writeEnrollmentError(w, err)
			return
		}
		if bound == nil {
			c.responder.Error(w, http.StatusNotFound, constants.ErrConsensusBindingNotFound.Error())
			return
		}
		c.responder.JSON(w, http.StatusOK, bound)

	case http.MethodPost:
		body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
		if err != nil {
			c.responder.Error(w, http.StatusBadRequest, "failed to read body")
			return
		}
		var req models.PlatformEnrollmentBindConsensusRequest
		if err := json.Unmarshal(body, &req); err != nil {
			c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
			return
		}
		policy, err := c.enrollSvc.BindConsensusApplication(r.Context(), userID, req)
		if err != nil {
			c.writeEnrollmentError(w, err)
			return
		}
		c.responder.JSON(w, http.StatusOK, policy)

	default:
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// approvalURLBase constructs the base URL for the approval console. The
// request ID is placed in the URL fragment so it never reaches
// server-side access logs. The base is derived from the configured
// PublicBaseURL or the localhost HTTPS URL.
func (c *PlatformEnrollmentController) approvalURLBase() string {
	base := c.cfg.Gateway.PublicBaseURL
	if base == "" {
		base = network.LocalhostHTTPSURL(c.cfg.Gateway.HTTPSPort)
	}
	return strings.TrimRight(base, "/") + constants.APIPaths.ConsolePrefix
}

// platformEnrollmentPublicErrors maps each sentinel an enrollment peer may
// see to its HTTP status. Order matters: the first match wins.
var platformEnrollmentPublicErrors = []struct {
	sentinel error
	status   int
}{
	{constants.ErrConsensusAppNotEnrolled, http.StatusBadRequest},
	{constants.ErrConsensusAppMissingSignerKey, http.StatusBadRequest},
	{constants.ErrConsensusEndpointInvalid, http.StatusBadRequest},
	{constants.ErrConsensusQuorumInvalid, http.StatusBadRequest},
	{constants.ErrConsensusMemberDuplicate, http.StatusBadRequest},
	{constants.ErrConsensusMemberNotOwned, http.StatusForbidden},
	{constants.ErrConsensusBindingNotFound, http.StatusNotFound},
	// Reserved before invalid: a reserved name wraps both, and the more
	// specific sentinel is the one a peer should see.
	{constants.ErrPlatformEnrollmentReservedIdentity, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidAppName, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentAppNameRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentRequiresBootstrap, http.StatusForbidden},
	{constants.ErrOperatorCapabilityInUse, http.StatusConflict},
	{constants.ErrOperatorActuatorKeyInUse, http.StatusConflict},
	{constants.ErrEmbeddedOperatorNotReady, http.StatusServiceUnavailable},
	{constants.ErrOperatorCapabilityInvalid, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidComponent, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInstanceIDRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidInstanceID, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentHostnameRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidHostname, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidDeploymentID, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentFingerprintRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidPayload, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidCSR, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentUnsupportedKey, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentSigningKeyInvalid, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentDuplicateKey, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentRequestIDRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentTokenRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentInvalidToken, http.StatusNotFound},
	{constants.ErrPlatformEnrollmentTokenConflict, http.StatusConflict},
	{constants.ErrPlatformEnrollmentRequestNotFound, http.StatusNotFound},
	{constants.ErrPlatformEnrollmentRequestExpired, http.StatusGone},
	{constants.ErrPlatformEnrollmentRequestDenied, http.StatusForbidden},
	{constants.ErrPlatformEnrollmentNotApproved, http.StatusForbidden},
	{constants.ErrPlatformEnrollmentAlreadyDecided, http.StatusConflict},
	{constants.ErrPlatformEnrollmentIssuanceInProgress, http.StatusTooManyRequests},
	{constants.ErrPlatformEnrollmentInvalidState, http.StatusInternalServerError},
	{constants.ErrPlatformEnrollmentInvalidDecision, http.StatusForbidden},
	{constants.ErrPlatformEnrollmentReasonTooLong, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentProofRequired, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentProofInvalid, http.StatusUnauthorized},
	{constants.ErrPlatformEnrollmentCSRMismatch, http.StatusBadRequest},
	{constants.ErrPlatformEnrollmentQuotaExceeded, http.StatusTooManyRequests},
	{constants.ErrPlatformEnrollmentGovernanceRejected, http.StatusInternalServerError},
	{constants.ErrPlatformEnrollmentPersistenceFailed, http.StatusInternalServerError},
	{constants.ErrPlatformEnrollmentIssuanceFailed, http.StatusInternalServerError},
	{constants.ErrPlatformEnrollmentStoredRequestInvalid, http.StatusInternalServerError},
	{constants.ErrPlatformEnrollmentRevoked, http.StatusGone},
	{constants.ErrPlatformEnrollmentL3Required, http.StatusAccepted},
	{constants.ErrOperatorHeartbeatIntervalInvalid, http.StatusBadRequest},
}

// platformEnrollmentPublicError reduces err to the status and bare sentinel
// an enrollment peer may see. Wrapped detail (identifiers, store and signer
// failures) stays in the Gateway log; the peer may be unauthenticated.
func platformEnrollmentPublicError(err error) (int, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || sqliteutil.IsBusyError(err) {
		return http.StatusServiceUnavailable, constants.ErrServiceUnavailable
	}
	for _, public := range platformEnrollmentPublicErrors {
		if errors.Is(err, public.sentinel) {
			return public.status, public.sentinel
		}
	}
	return http.StatusInternalServerError, constants.ErrInternal
}

// writeEnrollmentError writes the public form of a platform enrollment
// error; the full error is logged.
func (c *PlatformEnrollmentController) writeEnrollmentError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	var l3Err *models.PlatformEnrollmentL3RequiredError
	if errors.As(err, &l3Err) && l3Err != nil {
		c.logger.Info("platform enrollment: suspended awaiting L3 human approval",
			"tx_hash", l3Err.Suspension.TxHash,
			"posture", string(l3Err.Suspension.Posture))
		w.Header().Set("Cache-Control", "no-store")
		c.responder.JSON(w, http.StatusAccepted, l3Err.Suspension)
		return
	}
	status, public := platformEnrollmentPublicError(err)
	if status == http.StatusInternalServerError {
		c.logger.Error("platform enrollment: request failed", "error", err)
	} else {
		c.logger.Info("platform enrollment: request rejected", "status", status, "error", err)
	}
	if errors.Is(public, constants.ErrPlatformEnrollmentIssuanceInProgress) {
		w.Header().Set("Retry-After", "5")
	}
	c.responder.Error(w, status, public.Error())
}

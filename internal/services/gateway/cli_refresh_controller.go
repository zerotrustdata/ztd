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

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// CLIRefreshControllerDeps groups all dependencies for CLIRefreshController.
type CLIRefreshControllerDeps struct {
	Cfg                *config.Config
	Logger             *slog.Logger
	CLISessionSvc      *CLISessionService
	OperatorSessionSvc *OperatorSessionService
	Reg                *RegistrationService
	Auth               *AuthService
	UserSvc            *UserService
	LogoutSvc          *UserLogoutService
	Responder          *response.Writer
}

// CLIRefreshController handles the mTLS-protected CLI session refresh
// endpoint. It extends the caller's own CLI session in place: the same session
// ID, the same certificate binding, a new expiry, and the operator binding
// resolved for the user. The certificate is NOT rotated and no session is
// minted, so the certificate's URI SAN keeps naming the session it signs
// approvals against.
//
// Auth classification (enforced by the unified auth middleware via
// NewRouteAuthRegistry):
//   - refresh (POST /api/v1/auth/cli/refresh): RouteAuthMTLS
//     (requires a verified CLI client certificate whose URI SAN names the
//     session in the X-ZTD-CLI-Session-ID header; the caller can only refresh
//     its own session)
//
// This is the recovery path for an expired CLI session with a still-valid
// cert. An expired cert cannot authenticate via mTLS, so it can never reach
// this endpoint; the caller must use the recovery flow instead.
type CLIRefreshController struct {
	cfg                *config.Config
	logger             *slog.Logger
	cliSessionSvc      *CLISessionService
	operatorSessionSvc *OperatorSessionService
	reg                *RegistrationService
	auth               *AuthService
	userSvc            *UserService
	logoutSvc          *UserLogoutService
	responder          *response.Writer
}

func newCLIRefreshController(deps CLIRefreshControllerDeps) *CLIRefreshController {
	return &CLIRefreshController{
		cfg:                deps.Cfg,
		logger:             deps.Logger,
		cliSessionSvc:      deps.CLISessionSvc,
		operatorSessionSvc: deps.OperatorSessionSvc,
		reg:                deps.Reg,
		auth:               deps.Auth,
		userSvc:            deps.UserSvc,
		logoutSvc:          deps.LogoutSvc,
		responder:          deps.Responder,
	}
}

// handleRefresh extends the caller's CLI session in place. The user ID and CLI
// session ID are read from the request context (stamped by the auth middleware
// from the verified certificate URI SAN). The request body is empty — the cert
// is the proof of identity.
//
// Order of operations:
//  1. Read the user ID and CLI session ID from the mTLS context.
//  2. Verify the user is still active.
//  3. Load the session. A missing session is 401 (there is nothing to extend);
//     a deactivated session is 409 (a logged-out session is never revived).
//  4. Resolve the operator binding for the refreshed session.
//  5. RefreshCLISession: guarded in-place write of the new expiry and binding.
//  6. Return the unchanged session ID, the user ID, and the binding.
//
// POST /api/v1/auth/cli/refresh  (RouteAuthMTLS)
func (c *CLIRefreshController) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Identity comes from the mTLS context, never from the body.
	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.logger.Warn("CLI refresh: missing authenticated user context")
		c.responder.Error(w, http.StatusUnauthorized, "mTLS authentication required")
		return
	}
	cliSessionID, _ := r.Context().Value(constants.ContextKeyCLISessionID).(string)

	// The request body is empty. Read and discard it so the connection can be
	// reused; a non-empty body is ignored.
	if _, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes); err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}

	// Verify the user is still active. The auth middleware does this on
	// every request, but a race between middleware and refresh could leave
	// a disabled user with a still-valid cert.
	user, err := c.userSvc.GetByID(r.Context(), userID)
	if err != nil {
		c.logger.Error("CLI refresh: failed to look up user", "error", err, "user_id", userID)
		c.responder.Error(w, http.StatusInternalServerError, "failed to verify user")
		return
	}
	if user == nil || !user.IsActive() {
		c.logger.Warn("CLI refresh: user is not active", "user_id", userID)
		c.responder.Error(w, http.StatusForbidden, "user is not active")
		return
	}

	// The session the certificate names must exist: refresh extends it in place,
	// so a missing session leaves nothing to extend. An expired session is the
	// recovery case and is extended; a deactivated session is never revived.
	if cliSessionID == "" {
		c.logger.Warn("CLI refresh: no CLI session in the authenticated context", "user_id", userID)
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrCLISessionInvalid.Error())
		return
	}
	session, err := c.cliSessionSvc.loadCLISession(r.Context(), cliSessionID)
	if err != nil {
		if errors.Is(err, constants.ErrCLISessionNotFound) {
			c.logger.Warn("CLI refresh: CLI session not found; re-enroll to recover",
				"user_id", userID,
				"cli_session_id_prefix", safeTruncateID(cliSessionID),
			)
			c.responder.Error(w, http.StatusUnauthorized, constants.ErrCLISessionInvalid.Error())
			return
		}
		c.logger.Error("CLI refresh: failed to load CLI session",
			"error", err,
			"cli_session_id_prefix", safeTruncateID(cliSessionID),
		)
		c.responder.Error(w, http.StatusInternalServerError, "failed to load CLI session")
		return
	}
	if !session.IsActive {
		c.logger.Warn("CLI refresh: CLI session is deactivated and cannot be revived",
			"user_id", userID,
			"cli_session_id_prefix", safeTruncateID(cliSessionID),
		)
		c.writeRefreshError(w, constants.ErrCLISessionAlreadyDeactivated)
		return
	}

	// Resolve the operator binding the refreshed session carries. The caller's
	// registry-active Data operator is preferred, the embedded operator first.
	// Otherwise the session keeps its own binding while that operator session is
	// still active for the user, and failing that the user's active operator
	// session is used. With none, the caller must re-enroll.
	var operatorSessionID, operatorID string
	registrySessionID, registryOperatorID, registryOK, regErr := c.registryOperatorBinding(r.Context(), userID)
	if regErr != nil {
		c.logger.Error("CLI refresh: failed to look up active data operator binding",
			"error", regErr,
			"user_id", userID,
		)
		c.responder.Error(w, http.StatusInternalServerError, "failed to look up operator session")
		return
	}
	if registryOK {
		operatorSessionID = registrySessionID
		operatorID = registryOperatorID
	} else {
		operatorSessionID = session.OperatorSessionID
	}
	if operatorSessionID != "" && operatorID == "" && c.operatorSessionSvc != nil {
		opSession, opErr := c.operatorSessionSvc.GetActiveSessionForUser(r.Context(), userID)
		if opErr != nil {
			c.logger.Error("CLI refresh: failed to verify active operator session",
				"error", opErr,
				"user_id", userID,
			)
			c.responder.Error(w, http.StatusInternalServerError, "failed to verify operator session")
			return
		}
		if opSession == nil || opSession.ID != operatorSessionID {
			operatorSessionID = ""
		} else {
			operatorID = opSession.OperatorID
		}
	}
	if operatorSessionID == "" && c.operatorSessionSvc != nil {
		opSession, opErr := c.operatorSessionSvc.GetActiveSessionForUser(r.Context(), userID)
		if opErr != nil {
			c.logger.Error("CLI refresh: failed to look up active operator session",
				"error", opErr,
				"user_id", userID,
			)
			c.responder.Error(w, http.StatusInternalServerError, "failed to look up operator session")
			return
		}
		if opSession != nil {
			operatorSessionID = opSession.ID
			operatorID = opSession.OperatorID
		}
	}
	if operatorSessionID == "" {
		c.logger.Warn("CLI refresh: no operator session binding available",
			"user_id", userID,
			"cli_session_id_prefix", safeTruncateID(cliSessionID),
		)
		c.responder.Error(w, http.StatusConflict, "no active operator session found for user; re-enroll with 'auth enroll user' to establish a fresh operator binding")
		return
	}

	// Multi-bind survives a refresh only while the primary binding does.
	var boundOperatorSessionIDs []string
	if session.OperatorSessionID == operatorSessionID {
		boundOperatorSessionIDs = session.BoundOperatorSessionIDs
	}

	if _, err := c.cliSessionSvc.RefreshCLISession(r.Context(), cliSessionID, CLISessionRefreshFields{
		UserID:                  userID,
		OperatorSessionID:       operatorSessionID,
		BoundOperatorSessionIDs: boundOperatorSessionIDs,
	}); err != nil {
		c.logger.Warn("CLI refresh: RefreshCLISession failed",
			"error", err,
			"user_id", userID,
			"cli_session_id_prefix", safeTruncateID(cliSessionID),
		)
		c.writeRefreshError(w, err)
		return
	}

	c.logger.Info("CLI session refreshed in place via controller",
		"user_id", userID,
		"cli_session_id_prefix", safeTruncateID(cliSessionID),
	)

	c.responder.JSON(w, http.StatusCreated, models.CLIRefreshResponse{
		Success:           true,
		CLISessionID:      cliSessionID,
		UserID:            userID,
		OperatorSessionID: operatorSessionID,
		OperatorID:        operatorID,
	})
}

func (c *CLIRefreshController) registryOperatorBinding(ctx context.Context, userID string) (sessionID, operatorID string, ok bool, err error) {
	if c.reg == nil {
		return "", "", false, nil
	}
	operators, err := c.reg.ListUserOperators(ctx, userID)
	if err != nil {
		return "", "", false, err
	}
	// The Gateway's embedded Operator is always a Data operator, so it is the
	// default primary binding; a dedicated Data Operator is the fallback.
	for _, op := range defaultDataOperatorOrder(operators) {
		validated, validateErr := c.auth.ValidateOperatorSession(ctx, op.OperatorSessionId)
		if validateErr == nil && validated.UserId == userID {
			return validated.OperatorSessionId, validated.Id, true, nil
		}
	}
	return "", "", false, nil
}

// defaultDataOperatorOrder returns the active Data sessions in default-binding
// order: the embedded Operator first, then dedicated Data Operators.
func defaultDataOperatorOrder(operators []*operatorv1.OperatorDocument) []*operatorv1.OperatorDocument {
	var embedded, dedicated []*operatorv1.OperatorDocument
	for _, op := range operators {
		switch {
		case operatorcapability.IsDedicatedDataOperator(op):
			dedicated = append(dedicated, op)
		case operatorcapability.IsDataOperator(op):
			embedded = append(embedded, op)
		}
	}
	return append(embedded, dedicated...)
}

// writeRefreshError maps a typed refresh/session error to the appropriate
// HTTP status code and writes it to the response. Unknown errors default
// to 500 Internal Server Error. Mirrors writeRotationError.
func (c *CLIRefreshController) writeRefreshError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, constants.ErrCLISessionNotFound):
		c.responder.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, constants.ErrCLISessionAlreadyDeactivated):
		c.responder.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, constants.ErrCLIRefreshFailed):
		c.responder.Error(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, constants.ErrCLIRefreshCertExpired):
		c.responder.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, constants.ErrMTLSIdentityMismatch):
		c.responder.Error(w, http.StatusForbidden, err.Error())
	default:
		c.logger.Error("CLI refresh: unhandled error", "error", err)
		c.responder.Error(w, http.StatusInternalServerError, fmt.Sprintf("internal error: %v", err))
	}
}

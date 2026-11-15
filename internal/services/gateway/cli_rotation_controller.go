// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/pkg/certutil"
	"github.com/zerotrustdata/ztd/internal/response"
	"github.com/zerotrustdata/ztd/internal/uuid"
)

// CLIRotationControllerDeps groups all dependencies for CLIRotationController.
type CLIRotationControllerDeps struct {
	Cfg           *config.Config
	Logger        *slog.Logger
	PKI           *PKIAuthority
	CLISessionSvc *CLISessionService
	UserSvc       *UserService
	Responder     *response.Writer
}

// CLIRotationController handles the mTLS-protected CLI certificate rotation
// and renewal endpoints. Identity (user ID + active CLI session ID) is derived strictly
// from the authenticated mTLS certificate context stamped by the unified
// auth middleware — request body fields are NOT trusted for identity.
//
// Auth classification (enforced by the unified auth middleware via
// NewRouteAuthRegistry):
//   - rotation (POST /api/v1/auth/cli/rotate): RouteAuthMTLS
//     (requires a verified CLI client certificate whose URI SAN matches an
//     active CLI session; the caller can only rotate their own session)
//   - renewal (POST /api/v1/auth/cli/renew): RouteAuthMTLS, same
//     requirement; the caller can only renew their own current certificate
//
// Rotation is a single transactional replacement: the new certificate is
// signed BEFORE the old session is deactivated, and the old certificate is
// revoked AFTER the session replacement commits. See ReplaceCLISession for
// the partial-failure recovery contract.
type CLIRotationController struct {
	cfg           *config.Config
	logger        *slog.Logger
	pki           *PKIAuthority
	cliSessionSvc *CLISessionService
	userSvc       *UserService
	responder     *response.Writer
}

func newCLIRotationController(deps CLIRotationControllerDeps) *CLIRotationController {
	return &CLIRotationController{
		cfg:           deps.Cfg,
		logger:        deps.Logger,
		pki:           deps.PKI,
		cliSessionSvc: deps.CLISessionSvc,
		userSvc:       deps.UserSvc,
		responder:     deps.Responder,
	}
}

// handleRotate issues a replacement CLI certificate for the caller's active
// CLI session. The caller authenticates via mTLS; the user ID and CLI
// session ID are read from the request context (stamped by the auth
// middleware from the verified certificate URI SAN). The request body
// carries only the new CLI CSR — no identity fields.
//
// Order of operations (per the 5e ReplaceCLISession contract):
//  1. Validate the request body (CSR present, parseable).
//  2. Load the caller's active CLI session; reject if missing or inactive.
//  3. Verify the active session's user is still permitted to authenticate.
//  4. Pre-generate the new CLI session ID and sign the new CLI CSR with
//     that ID in the URI SAN.
//  5. ReplaceCLISession: persist the new session (with the pre-generated
//     ID), atomically deactivate the old one. On the concurrent-loss path,
//     the orphaned new session is cleaned up and the typed error is
//     returned so the caller can revoke the cert it just signed.
//  6. Revoke the old certificate (best-effort, idempotent retry on failure).
//  7. Return the new certificate, chain, session ID, and full trust bundle.
//
// POST /api/v1/auth/cli/rotate  (RouteAuthMTLS)
func (c *CLIRotationController) handleRotate(w http.ResponseWriter, r *http.Request) {
	userID, oldSession, req, ok := c.admitCLICertificateRequest(w, r, "CLI rotation")
	if !ok {
		return
	}
	oldCLISessionID := oldSession.ID

	// Sign the new CLI CSR BEFORE replacing the session. A signing failure
	// leaves the old session untouched and the caller can retry. The new
	// session ID is pre-generated here so the certificate URI SAN binds to
	// it, and the same ID is passed to ReplaceCLISession so the persisted
	// session and the signed cert stay bound.
	newCLISessionID, err := uuid.NewString()
	if err != nil {
		c.logger.Error("CLI rotation: failed to generate new CLI session ID", "error", err, "user_id", userID)
		c.writeRotationError(w, fmt.Errorf("%s: %w", constants.ErrCLIRotationFailed, err))
		return
	}
	newCertPEM, newCertChainPEM, err := c.pki.SignCSR(
		req.CLICSRPEM,
		constants.LeafTypeCLI,
		"",
		"",
		userID,
		newCLISessionID,
		"",
	)
	if err != nil {
		c.logger.Error("CLI rotation: failed to sign new CLI CSR",
			"error", err,
			"user_id", userID,
			"old_cli_session_id_prefix", safeTruncateID(oldCLISessionID),
		)
		c.writeRotationError(w, fmt.Errorf("%s: %w", constants.ErrCLIRotationFailed, err))
		return
	}

	newCertFingerprint := calculateFingerprintFromPEM(newCertPEM)
	newCertSerial := calculateSerialFromPEM(newCertPEM)

	// Transactionally replace the old session with the new one. The new
	// session inherits the operator binding and system fingerprint from
	// the old session so the caller's routing namespace is preserved. The
	// new session ID is the one we signed the cert's URI SAN against, so
	// the cert and session stay bound.
	_, err = c.cliSessionSvc.ReplaceCLISession(
		r.Context(),
		oldCLISessionID,
		newCLISessionID,
		newCertFingerprint,
		newCertSerial,
		CLISessionFields{
			OperatorSessionID:       oldSession.OperatorSessionID,
			BoundOperatorSessionIDs: oldSession.BoundOperatorSessionIDs,
			UserID:                  oldSession.UserID,
			SystemFingerprint:       oldSession.SystemFingerprint,
			CertFingerprint:         newCertFingerprint,
			CertSerial:              newCertSerial,
			LoginMethod:             oldSession.LoginMethod,
		},
	)
	if err != nil {
		// ReplaceCLISession cleans up the orphaned new session on the
		// concurrent-loss path, so the cert we just signed has no
		// matching active session. Revoke it best-effort so it cannot
		// be used, then surface the typed error to the caller.
		c.logger.Warn("CLI rotation: ReplaceCLISession failed",
			"error", err,
			"user_id", userID,
			"old_cli_session_id_prefix", safeTruncateID(oldCLISessionID),
			"new_cli_session_id_prefix", safeTruncateID(newCLISessionID),
		)
		if revokeErr := c.pki.RevokeCertificate(r.Context(), newCertSerial, "rotation_race_lost"); revokeErr != nil {
			c.logger.Error("CLI rotation: failed to revoke orphaned new cert after race loss",
				"error", revokeErr, "new_cert_serial", newCertSerial)
		}
		c.writeRotationError(w, err)
		return
	}

	// Revoke the OLD certificate now that the old session is committed
	// inactive. This is best-effort: if revocation fails, the old session
	// is already inactive so the caller is not locked out, but the old
	// cert may still pass a CRL-skipping verifier. Log loudly and surface
	// a non-fatal warning to the caller via the response — the rotation
	// itself succeeded.
	// A renewed session also holds the certificate its renewal replaced, which
	// names the same session and is still within its validity window.
	// Reload after deactivation: a renewal may have committed since admission.
	oldSession, err = c.cliSessionSvc.loadCLISession(r.Context(), oldCLISessionID)
	if err != nil {
		c.writeRotationError(w, err)
		return
	}
	for _, serial := range cliSessionCertificateSerials(oldSession) {
		if revokeErr := c.pki.RevokeCertificate(r.Context(), serial, "cli_rotation"); revokeErr != nil {
			c.logger.Error("CLI rotation: failed to revoke old cert (rotation succeeded; old session inactive)",
				"error", revokeErr,
				"old_cert_serial", serial,
				"old_cli_session_id_prefix", safeTruncateID(oldCLISessionID),
			)
			// Do not fail the request — the new identity is usable.
		}
	}

	// Fetch the full runtime trust bundle so the caller can refresh its
	// local trust store. Non-fatal if unavailable (matches recovery/bootstrap).
	hubBundle, err := c.pki.GatewayTrustBundle()
	if err != nil {
		c.logger.Warn("CLI rotation: failed to fetch hub trust bundle", "error", err)
	}

	c.logger.Info("CLI rotation completed via controller",
		"user_id", userID,
		"old_cli_session_id_prefix", safeTruncateID(oldCLISessionID),
		"new_cli_session_id_prefix", safeTruncateID(newCLISessionID),
	)

	c.responder.JSON(w, http.StatusCreated, models.CLIRotationResponse{
		Success:        true,
		CLISessionID:   newCLISessionID,
		CLICert:        newCertPEM,
		CLICertChain:   newCertChainPEM,
		HubTrustBundle: string(hubBundle),
		UserID:         userID,
	})
}

// admitCLICertificateRequest runs the admission shared by rotation and
// renewal. Identity (user ID and CLI session ID) comes from the mTLS context
// stamped by the auth middleware, never from the body. The caller's CLI
// session must exist, be active, belong to that user, and the user must still
// be active. The body must carry a CLI CSR. On any failure it writes the
// response and returns ok=false. op labels log lines.
func (c *CLIRotationController) admitCLICertificateRequest(w http.ResponseWriter, r *http.Request, op string) (string, *models.CLISession, models.CLIRotationRequest, bool) {
	var req models.CLIRotationRequest
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return "", nil, req, false
	}

	userID, ok := r.Context().Value(constants.ContextKeyUserID).(string)
	if !ok || userID == "" {
		c.logger.Warn(op + ": missing authenticated user context")
		c.responder.Error(w, http.StatusUnauthorized, "mTLS authentication required")
		return "", nil, req, false
	}
	cliSessionID, ok := r.Context().Value(constants.ContextKeyCLISessionID).(string)
	if !ok || cliSessionID == "" {
		c.logger.Warn(op+": missing CLI session context", "user_id", userID)
		c.responder.Error(w, http.StatusUnauthorized, "active CLI session required")
		return "", nil, req, false
	}

	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return "", nil, req, false
	}
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return "", nil, req, false
	}
	if req.CLICSRPEM == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrCLIRotationCSRRequired.Error())
		return "", nil, req, false
	}
	session, ok := c.admitCLISession(w, r, op, userID, cliSessionID)
	if !ok {
		return "", nil, req, false
	}
	return userID, session, req, true
}

// admitCLISession loads the CLI session cliSessionID and requires it to be
// active and to belong to userID, an active user. On any failure it writes the
// response and returns ok=false. op labels log lines.
func (c *CLIRotationController) admitCLISession(w http.ResponseWriter, r *http.Request, op, userID, cliSessionID string) (*models.CLISession, bool) {
	// Load the caller's CLI session. The session writes re-check activity
	// atomically, but loading first gives precise errors for missing or
	// inactive sessions before anything is signed.
	session, err := c.cliSessionSvc.loadCLISession(r.Context(), cliSessionID)
	if err != nil {
		c.writeRotationError(w, err)
		return nil, false
	}
	if !session.IsActive {
		c.writeRotationError(w, constants.ErrCLISessionAlreadyDeactivated)
		return nil, false
	}
	session.ID = cliSessionID

	// Defense in depth: the auth middleware already verified the cert URI
	// SAN matches the session, but the session's stored user ID must also
	// match the context user ID. A mismatch would indicate a stale context
	// or a session re-binding attack.
	if session.UserID != userID {
		c.logger.Warn(op+": session user mismatch",
			"context_user_id", userID,
			"session_user_id", session.UserID,
			"cli_session_id_prefix", safeTruncateID(cliSessionID),
		)
		c.responder.Error(w, http.StatusForbidden, constants.ErrMTLSIdentityMismatch.Error())
		return nil, false
	}

	// Verify the user is still active. The auth middleware does this on
	// every request, but a race between middleware and this handler could
	// leave a disabled user with a still-active session.
	user, err := c.userSvc.GetByID(r.Context(), userID)
	if err != nil {
		c.logger.Error(op+": failed to look up user", "error", err, "user_id", userID)
		c.responder.Error(w, http.StatusInternalServerError, "failed to verify user")
		return nil, false
	}
	if user == nil || !user.IsActive() {
		c.logger.Warn(op+": user is not active", "user_id", userID)
		c.responder.Error(w, http.StatusForbidden, "user is not active")
		return nil, false
	}
	return session, true
}

// handleRenew renews the caller's CLI certificate without changing its CLI
// session, the way an Operator renews its leaf. The renewed certificate names
// the same session in its URI SAN, so clients that hold the session ID, open
// SSE streams, and operator bindings are untouched. Only the session's current
// certificate may renew: a certificate a renewal already replaced is refused
// with constants.ErrCLIRenewalStaleCertificate (412). The replaced certificate is
// not revoked: it names the same session, expires on its own, and stays valid
// for a process still holding it, and logout revokes it with the session.
//
// Order of operations:
//  1. Admit the request (admitCLICertificateRequest).
//  2. Require the presented certificate to be the session's current one.
//  3. Sign the CSR for the same user and CLI session.
//  4. RenewCLISessionCertificate: record the new certificate in place,
//     conditional on the presented one, and extend the session TTL. On
//     failure the just-signed certificate is revoked.
//  5. Return the new certificate, chain, session ID, and full trust bundle.
//
// POST /api/v1/auth/cli/renew  (RouteAuthMTLS)
func (c *CLIRotationController) handleRenew(w http.ResponseWriter, r *http.Request) {
	userID, session, req, ok := c.admitCLICertificateRequest(w, r, "CLI renewal")
	if !ok {
		return
	}
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		c.responder.Error(w, http.StatusUnauthorized, constants.ErrMTLSCertRequired.Error())
		return
	}
	c.reissueSessionCertificate(w, r, "CLI renewal", userID, session, r.TLS.PeerCertificates[0], req.CLICSRPEM)
}

// handleReinstate reissues an expired CLI certificate for its CLI session,
// the way handleRenew renews a valid one. An expired certificate cannot
// complete the mTLS handshake, so the caller presents it in the body with a
// proof of possession of its private key, which the CLI keeps sealed in its
// keystore: an ECDSA signature over certutil.CLIReinstateProofDigest, binding
// the session and the replacement CSR. No human approval is involved.
//
// Every check fails closed:
//  1. The certificate chains to this Gateway's Operator intermediate and is
//     not revoked (PKIAuthority.VerifyExpiredCLICertificate).
//  2. It expired no longer than constants.CLIReinstateWindow ago.
//  3. Its URI SAN names a CLI user and session.
//  4. The signature verifies under the certificate's public key.
//  5. The session is active (not logged out) and belongs to that user, an
//     active user (admitCLISession).
//  6. The certificate is the session's current one; the conditional session
//     update in reissueSessionCertificate makes a replayed request fail.
//
// POST /api/v1/auth/cli/reinstate  (RouteAuthNone; proof of possession)
func (c *CLIRotationController) handleReinstate(w http.ResponseWriter, r *http.Request) {
	const op = "CLI reinstatement"
	if r.Method != http.MethodPost {
		c.responder.Error(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := readRequestBody(r, c.cfg.Gateway.MaxPayloadBytes)
	if err != nil {
		c.responder.Error(w, http.StatusBadRequest, "failed to read body")
		return
	}
	var req models.CLIReinstateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrInvalidJSONBody.Error())
		return
	}
	if req.CLICSRPEM == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrCLIRotationCSRRequired.Error())
		return
	}
	if req.CLICertPEM == "" || req.Signature == "" {
		c.responder.Error(w, http.StatusBadRequest, constants.ErrMissingRequiredField.Error())
		return
	}
	cert, err := certutil.ParseCertFromPEM([]byte(req.CLICertPEM))
	if err != nil {
		c.writeRotationError(w, fmt.Errorf("%w: %w", constants.ErrCLIReinstateCertInvalid, err))
		return
	}
	if err := c.pki.VerifyExpiredCLICertificate(r.Context(), cert); err != nil {
		c.logger.Warn(op+": certificate refused", "error", err)
		c.writeRotationError(w, err)
		return
	}
	if !time.Now().Before(cert.NotAfter.Add(constants.CLIReinstateWindow)) {
		c.writeRotationError(w, constants.ErrCLIReinstateWindowElapsed)
		return
	}
	userID, cliSessionID := CLIIdentityFromCert(cert)
	if userID == "" || cliSessionID == "" {
		c.writeRotationError(w, constants.ErrCLIReinstateCertInvalid)
		return
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	signature, decodeErr := base64.StdEncoding.DecodeString(req.Signature)
	if !ok || decodeErr != nil || !ecdsa.VerifyASN1(pub, certutil.CLIReinstateProofDigest(cliSessionID, req.CLICSRPEM), signature) {
		c.logger.Warn(op+": proof of possession refused", "user_id", userID, "cli_session_id_prefix", safeTruncateID(cliSessionID))
		c.writeRotationError(w, constants.ErrCLIReinstateProofInvalid)
		return
	}
	session, ok := c.admitCLISession(w, r, op, userID, cliSessionID)
	if !ok {
		return
	}
	c.reissueSessionCertificate(w, r, op, userID, session, cert, req.CLICSRPEM)
}

// reissueSessionCertificate is the issuance shared by renewal and
// reinstatement. presented must be the session's current certificate. It
// signs the CSR for the same user and CLI session, records the new
// certificate in place conditional on presented
// (RenewCLISessionCertificate), which also extends the session TTL, and
// returns the new certificate, chain, session ID, and full trust bundle. On a
// failed session update the just-signed certificate is revoked. The replaced
// certificate is not revoked: it names the same session and expires on its
// own, and logout revokes it with the session.
func (c *CLIRotationController) reissueSessionCertificate(w http.ResponseWriter, r *http.Request, op, userID string, session *models.CLISession, presented *x509.Certificate, csrPEM string) {
	digest := sha256.Sum256(presented.Raw)
	presentedFingerprint := hex.EncodeToString(digest[:])
	if session.CertFingerprint != presentedFingerprint {
		c.writeRotationError(w, constants.ErrCLIRenewalStaleCertificate)
		return
	}

	certPEM, chainPEM, err := c.pki.SignCSR(csrPEM, constants.LeafTypeCLI, "", "", userID, session.ID, "")
	if err != nil {
		c.logger.Error(op+": failed to sign CSR", "error", err, "user_id", userID,
			"cli_session_id_prefix", safeTruncateID(session.ID))
		c.writeRotationError(w, fmt.Errorf("%w: %w", constants.ErrCLIRotationFailed, err))
		return
	}
	newSerial := calculateSerialFromPEM(certPEM)
	if _, err := c.cliSessionSvc.RenewCLISessionCertificate(r.Context(), session.ID, userID, presentedFingerprint, calculateFingerprintFromPEM(certPEM), newSerial); err != nil {
		c.logger.Warn(op+": session update failed", "error", err, "user_id", userID,
			"cli_session_id_prefix", safeTruncateID(session.ID))
		if revokeErr := c.pki.RevokeCertificate(r.Context(), newSerial, "cli_renewal_not_recorded"); revokeErr != nil {
			c.logger.Error(op+": failed to revoke unrecorded certificate", "error", revokeErr, "cert_serial", newSerial)
		}
		c.writeRotationError(w, err)
		return
	}

	hubBundle, err := c.pki.GatewayTrustBundle()
	if err != nil {
		c.logger.Warn(op+": failed to fetch hub trust bundle", "error", err)
	}
	c.logger.Info(op+": CLI certificate reissued", "user_id", userID, "cli_session_id_prefix", safeTruncateID(session.ID))
	c.responder.JSON(w, http.StatusCreated, models.CLIRotationResponse{
		Success:        true,
		CLISessionID:   session.ID,
		CLICert:        certPEM,
		CLICertChain:   chainPEM,
		HubTrustBundle: string(hubBundle),
		UserID:         userID,
	})
}

// writeRotationError maps a typed rotation/session error to the appropriate
// HTTP status code and writes it to the response. Unknown errors default to
// 500 Internal Server Error. Mirrors writeRecoveryError.
func (c *CLIRotationController) writeRotationError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, constants.ErrCLISessionNotFound):
		c.responder.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, constants.ErrCLISessionAlreadyDeactivated):
		c.responder.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, constants.ErrCLIRenewalStaleCertificate):
		// The renewal's precondition (the presented certificate is the
		// session's current one) failed. A distinct status lets the CLI
		// direct the caller to rotation.
		c.responder.Error(w, http.StatusPreconditionFailed, err.Error())
	case errors.Is(err, constants.ErrCLIRotationCSRRequired):
		c.responder.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, constants.ErrCLIRotationFailed):
		c.responder.Error(w, http.StatusInternalServerError, err.Error())
	case errors.Is(err, constants.ErrMTLSIdentityMismatch),
		errors.Is(err, constants.ErrCLIReinstateCertInvalid),
		errors.Is(err, constants.ErrPKICertificateRevoked),
		errors.Is(err, constants.ErrCLIReinstateWindowElapsed):
		c.responder.Error(w, http.StatusForbidden, err.Error())
	case errors.Is(err, constants.ErrCLIReinstateProofInvalid):
		c.responder.Error(w, http.StatusUnauthorized, err.Error())
	default:
		c.logger.Error("CLI rotation: unhandled error", "error", err)
		c.responder.Error(w, http.StatusInternalServerError, "internal error")
	}
}

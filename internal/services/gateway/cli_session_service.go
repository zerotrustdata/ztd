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
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
)

// CLISessionService handles CLI session persistence and management.
// CLI sessions are strictly disjoint from operator sessions - they represent
// the routing namespace for BYO/CLI clients to receive SessionEvents (SSE)
// and embed in outbound request bodies.
type CLISessionService struct {
	db     *DocumentStoreService
	logger *slog.Logger
}

// NewCLISessionService creates a new CLISessionService instance.
func NewCLISessionService(docStore *DocumentStoreService, logger *slog.Logger) *CLISessionService {
	return &CLISessionService{
		db:     docStore,
		logger: logger,
	}
}

// PersistCLISession creates and persists a CLI session document.
// The operatorSessionID binds this CLI session to an operator session for authorization.
func (s *CLISessionService) PersistCLISession(ctx context.Context, cliSessionID, operatorSessionID, userID, systemFingerprint, certFingerprint, certSerial, loginMethod string) error {
	cliExpiry := time.Now().UTC().Add(constants.CLISessionTTL)
	cliSession := models.CLISession{
		ID:                cliSessionID,
		UserID:            userID,
		OperatorSessionID: operatorSessionID,
		SystemFingerprint: systemFingerprint,
		CertFingerprint:   certFingerprint,
		CertSerial:        certSerial,
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         cliExpiry,
		AbsoluteExpiresAt: cliExpiry,
		IdleExpiresAt:     cliExpiry,
		SessionType:       string(constants.SessionTypeCLI),
		IsActive:          true,
		LoginMethod:       loginMethod,
	}
	cliSessionBytes, err := json.Marshal(cliSession)
	if err != nil {
		return fmt.Errorf("failed to marshal CLI session: %w", err)
	}

	if err := s.db.DocSet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), cliSessionID, cliSessionBytes); err != nil {
		s.logger.Error("Failed to persist CLI session", string(constants.ConnectionStateError), err)
		return fmt.Errorf("failed to persist CLI session: %w", err)
	}

	return nil
}

// CLISessionFields carries the identity-binding fields for a new CLI session
// created by ReplaceCLISession. It is a typed subset of models.CLISession so
// callers (rotation, recovery completion) cannot accidentally swap the user
// or operator binding when replacing a session.
type CLISessionFields struct {
	OperatorSessionID string
	// BoundOperatorSessionIDs is the full multi-bind list (primary first);
	// empty means the session is bound only to OperatorSessionID.
	BoundOperatorSessionIDs []string
	UserID                  string
	SystemFingerprint       string
	CertFingerprint         string
	CertSerial              string
	LoginMethod             string
}

type cliSessionDeactivationUpdate struct {
	IsActive bool `json:"is_active"`
}

func marshalCLISessionDeactivationUpdate() (json.RawMessage, error) {
	data, err := json.Marshal(cliSessionDeactivationUpdate{IsActive: false})
	if err != nil {
		return nil, fmt.Errorf("marshal CLI session deactivation: %w", err)
	}
	return data, nil
}

// cliSessionBindingUpdate rewrites the operator binding of an existing CLI
// session. Neither field carries omitempty so a cleared binding (empty primary,
// empty list) is persisted instead of being dropped from the update.
type cliSessionBindingUpdate struct {
	OperatorSessionID       string   `json:"operator_session_id"`
	BoundOperatorSessionIDs []string `json:"bound_operator_session_ids"`
}

func marshalCLISessionBindingUpdate(primary string, bound []string) (json.RawMessage, error) {
	data, err := json.Marshal(cliSessionBindingUpdate{OperatorSessionID: primary, BoundOperatorSessionIDs: bound})
	if err != nil {
		return nil, fmt.Errorf("marshal CLI session binding: %w", err)
	}
	return data, nil
}

// DeactivateCLISession atomically marks a CLI session inactive by setting
// is_active=false via DocConditionalUpdate. Only an active session can be
// deactivated; an already-deactivated session returns
// constants.ErrCLISessionAlreadyDeactivated and a missing session returns
// constants.ErrCLISessionNotFound. The caller is responsible for any PKI
// revocation side-effect; this method only mutates CLI-session state.
func (s *CLISessionService) DeactivateCLISession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("deactivate CLI session: %w", constants.ErrCLISessionInvalid)
	}

	// Read first to distinguish not-found from already-deactivated.
	doc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), sessionID)
	if err != nil {
		return fmt.Errorf("deactivate CLI session: load: %w", err)
	}
	if doc == nil {
		return constants.ErrCLISessionNotFound
	}
	existing, err := decodeCLISession(doc)
	if err != nil {
		return fmt.Errorf("deactivate CLI session: decode: %w", err)
	}
	if !existing.IsActive {
		return constants.ErrCLISessionAlreadyDeactivated
	}
	deactivationUpdate, err := marshalCLISessionDeactivationUpdate()
	if err != nil {
		return err
	}

	applied, err := s.db.DocConditionalUpdate(
		ctx, marshaler.CollectionName(constants.CollectionCLISessions),
		sessionID,
		deactivationUpdate,
		"is_active", true,
	)
	if err != nil {
		return fmt.Errorf("deactivate CLI session: conditional update: %w", err)
	}
	if !applied {
		// Another caller deactivated or replaced the session between our read
		// and the conditional update. Re-read to give a precise error.
		current, err := s.loadCLISession(ctx, sessionID)
		if err != nil {
			return err
		}
		if !current.IsActive {
			return constants.ErrCLISessionAlreadyDeactivated
		}
		// Should not happen — the conditional update matched on is_active=true
		// and the only competing writer also flips it to false.
		return fmt.Errorf("deactivate CLI session: %w", constants.ErrCLISessionInvalid)
	}
	s.logger.Info("CLI session deactivated", "cli_session_id_prefix", safeTruncateID(sessionID))
	return nil
}

// cliSessionTerminationUpdate deactivates a CLI session and clears its
// operator binding in one write. The binding fields carry no omitempty so the
// cleared values are persisted instead of being dropped from the update.
type cliSessionTerminationUpdate struct {
	IsActive                bool     `json:"is_active"`
	OperatorSessionID       string   `json:"operator_session_id"`
	BoundOperatorSessionIDs []string `json:"bound_operator_session_ids"`
}

// ListUserCLISessions returns every CLI session document owned by userID,
// active or not. Callers decide which states matter: logout needs the active
// sessions to terminate and every certificate serial ever issued to the user.
func (s *CLISessionService) ListUserCLISessions(ctx context.Context, userID string) ([]*models.CLISession, error) {
	if userID == "" {
		return nil, fmt.Errorf("list user CLI sessions: %w", constants.ErrRegistrationUserIDRequired)
	}
	docs, err := s.db.DocQuery(ctx, marshaler.CollectionName(constants.CollectionCLISessions), []models.DocFilter{
		{Field: "user_id", Op: "==", Value: json.RawMessage(fmt.Sprintf("%q", userID))},
	}, "", 0)
	if err != nil {
		return nil, fmt.Errorf("list user CLI sessions: %w", err)
	}
	sessions := make([]*models.CLISession, 0, len(docs))
	for _, doc := range docs {
		session, err := decodeCLISession(doc)
		if err != nil {
			return nil, fmt.Errorf("list user CLI sessions: decode %s: %w", safeTruncateID(doc.ID), err)
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// TerminateCLISession atomically deactivates an active CLI session and clears
// its operator binding (OperatorSessionID and BoundOperatorSessionIDs) in the
// same conditional write. It reports whether this call terminated the
// session: false with a nil error means a concurrent caller deactivated or
// replaced it first. The caller owns PKI revocation of the session's
// certificate; this method only mutates CLI-session state.
func (s *CLISessionService) TerminateCLISession(ctx context.Context, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, fmt.Errorf("terminate CLI session: %w", constants.ErrCLISessionInvalid)
	}
	update, err := json.Marshal(cliSessionTerminationUpdate{BoundOperatorSessionIDs: []string{}})
	if err != nil {
		return false, fmt.Errorf("terminate CLI session: marshal update: %w", err)
	}
	applied, err := s.db.DocConditionalUpdate(
		ctx, marshaler.CollectionName(constants.CollectionCLISessions),
		sessionID,
		update,
		"is_active", true,
	)
	if err != nil {
		return false, fmt.Errorf("terminate CLI session: conditional update: %w", err)
	}
	if applied {
		s.logger.Info("CLI session terminated", "cli_session_id_prefix", safeTruncateID(sessionID))
	}
	return applied, nil
}

// ReplaceCLISession transactionally replaces an active CLI session with a new
// one bound to the supplied identity fields. It is the single session-
// replacement path used by both CLI rotation (5d) and recovery completion
// (5c) so PKI revocation state and CLI-session state cannot silently diverge.
//
// The caller MUST pre-generate newSessionID and sign the new certificate's
// URI SAN against it BEFORE calling this method, so the cert and the
// persisted session stay bound. This method does not generate the session ID
// internally — doing so would break the cert↔session binding established by
// the caller.
//
// Order of operations (documented for partial-failure recovery):
//  1. Read the old session; reject if missing or already deactivated.
//  2. Persist the new CLI session document (DocSet) with the caller-supplied
//     newSessionID.
//  3. Atomically deactivate the old session via DocConditionalUpdate on
//     is_active=true. If this fails because a concurrent caller already
//     deactivated it, the freshly-written new session is deleted to avoid
//     leaving an orphaned active session, and the caller receives
//     constants.ErrCLISessionAlreadyDeactivated.
//
// PKI revocation of the OLD certificate is the caller's responsibility and
// MUST happen after this method returns nil. If the caller revokes first and
// this method then fails, the old session is still active and the user is
// locked out; if this method succeeds first and revocation then fails, the
// old session is inactive but the old cert may still pass a CRL-skipping
// verifier — the typed error lets the caller retry revocation idempotently.
// The new certificate is signed by the caller BEFORE this method is invoked
// so a signing failure never leaves a stale deactivated session.
func (s *CLISessionService) ReplaceCLISession(ctx context.Context, oldSessionID, newSessionID string, newCertFingerprint, newCertSerial string, newFields CLISessionFields) (newSession *models.CLISession, err error) {
	if oldSessionID == "" {
		return nil, fmt.Errorf("replace CLI session: %w", constants.ErrCLISessionInvalid)
	}
	if newSessionID == "" {
		return nil, fmt.Errorf("replace CLI session: missing new session ID: %w", constants.ErrMissingRequiredField)
	}
	if newFields.UserID == "" || newFields.OperatorSessionID == "" {
		return nil, fmt.Errorf("replace CLI session: missing user or operator session binding: %w", constants.ErrMissingRequiredField)
	}

	// 1. Verify the old session exists and is still active.
	oldDoc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), oldSessionID)
	if err != nil {
		return nil, fmt.Errorf("replace CLI session: load old: %w", err)
	}
	if oldDoc == nil {
		return nil, constants.ErrCLISessionNotFound
	}
	oldSession, err := decodeCLISession(oldDoc)
	if err != nil {
		return nil, fmt.Errorf("replace CLI session: decode old: %w", err)
	}
	if !oldSession.IsActive {
		return nil, constants.ErrCLISessionAlreadyDeactivated
	}

	// 2. Persist the new session first so a deactivation failure does not
	//    leave the user without any active session.
	cliExpiry := time.Now().UTC().Add(constants.CLISessionTTL)
	created := models.CLISession{
		ID:                      newSessionID,
		UserID:                  newFields.UserID,
		OperatorSessionID:       newFields.OperatorSessionID,
		BoundOperatorSessionIDs: newFields.BoundOperatorSessionIDs,
		SystemFingerprint:       newFields.SystemFingerprint,
		CertFingerprint:         newCertFingerprint,
		CertSerial:              newCertSerial,
		CreatedAt:               time.Now().UTC(),
		ExpiresAt:               cliExpiry,
		AbsoluteExpiresAt:       cliExpiry,
		IdleExpiresAt:           cliExpiry,
		SessionType:             string(constants.SessionTypeCLI),
		IsActive:                true,
		LoginMethod:             newFields.LoginMethod,
	}
	createdBytes, err := json.Marshal(created)
	if err != nil {
		return nil, fmt.Errorf("replace CLI session: marshal new: %w", err)
	}
	if err := s.db.DocSet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), newSessionID, createdBytes); err != nil {
		return nil, fmt.Errorf("replace CLI session: persist new: %w", err)
	}

	// 3. Atomically deactivate the old session. Competing replacements or
	//    explicit DeactivateCLISession calls flip is_active to false; our
	//    conditional update only succeeds while it is still true.
	deactivationUpdate, err := marshalCLISessionDeactivationUpdate()
	if err != nil {
		return nil, err
	}
	applied, err := s.db.DocConditionalUpdate(
		ctx, marshaler.CollectionName(constants.CollectionCLISessions),
		oldSessionID,
		deactivationUpdate,
		"is_active", true,
	)
	if err != nil {
		// The new session was persisted but we could not deactivate the old
		// one due to a store error. Clean up the orphaned new session so we
		// do not leave an active session with no matching deactivated old one.
		if delErr := s.db.DocDelete(ctx, marshaler.CollectionName(constants.CollectionCLISessions), newSessionID); delErr != nil {
			s.logger.Error("ReplaceCLISession: failed to clean up orphaned new session after deactivate error",
				"error", delErr,
				"new_session_id_prefix", safeTruncateID(newSessionID),
			)
		}
		return nil, fmt.Errorf("replace CLI session: deactivate old: %w", err)
	}
	if !applied {
		// A concurrent caller already deactivated the old session. Delete the
		// orphaned new session we just wrote so an active session document is
		// not left in the collection with no corresponding deactivated old one.
		if delErr := s.db.DocDelete(ctx, marshaler.CollectionName(constants.CollectionCLISessions), newSessionID); delErr != nil {
			s.logger.Error("ReplaceCLISession: failed to clean up orphaned new session after race loss",
				"error", delErr,
				"new_session_id_prefix", safeTruncateID(newSessionID),
			)
		}
		s.logger.Warn("ReplaceCLISession: old session already deactivated by concurrent caller",
			"old_session_id_prefix", safeTruncateID(oldSessionID),
			"new_session_id_prefix", safeTruncateID(newSessionID),
		)
		return nil, constants.ErrCLISessionAlreadyDeactivated
	}

	s.logger.Info("CLI session replaced",
		"old_session_id_prefix", safeTruncateID(oldSessionID),
		"new_session_id_prefix", safeTruncateID(newSessionID),
		"user_id", newFields.UserID,
	)
	return &created, nil
}

// loadCLISession fetches a CLI session by ID and returns typed errors.
func (s *CLISessionService) loadCLISession(ctx context.Context, sessionID string) (*models.CLISession, error) {
	doc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionCLISessions), sessionID)
	if err != nil {
		return nil, fmt.Errorf("load CLI session: %w", err)
	}
	if doc == nil {
		return nil, constants.ErrCLISessionNotFound
	}
	return decodeCLISession(doc)
}

// CLISessionRefreshFields carries the operator binding a refresh writes onto an
// existing CLI session. It is a typed subset so a refresh cannot rewrite the
// certificate grounding (fingerprint and serial), system fingerprint, login
// method, or creation time of the session it extends.
type CLISessionRefreshFields struct {
	UserID            string
	OperatorSessionID string
	// BoundOperatorSessionIDs is the full multi-bind list (primary first); nil
	// clears it.
	BoundOperatorSessionIDs []string
}

// cliSessionRefreshUpdate extends an active CLI session in place: all three
// expiry fields move to the TTL window and the operator binding is rewritten.
// The binding fields carry no omitempty so a cleared binding is persisted.
type cliSessionRefreshUpdate struct {
	ExpiresAt               time.Time `json:"expires_at"`
	AbsoluteExpiresAt       time.Time `json:"absolute_expires_at"`
	IdleExpiresAt           time.Time `json:"idle_expires_at"`
	OperatorSessionID       string    `json:"operator_session_id"`
	BoundOperatorSessionIDs []string  `json:"bound_operator_session_ids"`
}

func marshalCLISessionRefreshUpdate(expiry time.Time, primary string, bound []string) (json.RawMessage, error) {
	data, err := json.Marshal(cliSessionRefreshUpdate{
		ExpiresAt:               expiry,
		AbsoluteExpiresAt:       expiry,
		IdleExpiresAt:           expiry,
		OperatorSessionID:       primary,
		BoundOperatorSessionIDs: bound,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal CLI session refresh: %w", err)
	}
	return data, nil
}

// RefreshCLISession extends the caller's CLI session in place. The session keeps
// its ID, certificate fingerprint and serial, system fingerprint, login method,
// and creation time, so the certificate's URI SAN still names it, signed CLI
// approvals still match it, and its SSE streams stay subscribed. Its three
// expiry fields move to constants.CLISessionTTL from now, and the operator
// binding is rewritten to the binding the caller resolved. Nothing is created.
//
// Refresh is the recovery path for an expired session, so an expired session is
// extended. A deactivated session is never revived and returns
// constants.ErrCLISessionAlreadyDeactivated. A missing session returns
// constants.ErrCLISessionNotFound, and so does a session that belongs to another
// user, so the caller learns nothing about sessions it does not own.
//
// The write is conditional on is_active=true, so a concurrent logout that
// deactivates the session between the read and the write wins.
func (s *CLISessionService) RefreshCLISession(ctx context.Context, cliSessionID string, fields CLISessionRefreshFields) (*models.CLISession, error) {
	if cliSessionID == "" {
		return nil, fmt.Errorf("refresh CLI session: %w", constants.ErrCLISessionInvalid)
	}
	if fields.UserID == "" || fields.OperatorSessionID == "" {
		return nil, fmt.Errorf("refresh CLI session: missing user or operator session binding: %w", constants.ErrMissingRequiredField)
	}

	existing, err := s.loadCLISession(ctx, cliSessionID)
	if err != nil {
		return nil, fmt.Errorf("refresh CLI session: load: %w", err)
	}
	if existing.UserID != fields.UserID {
		return nil, fmt.Errorf("refresh CLI session: %w", constants.ErrCLISessionNotFound)
	}
	if !existing.IsActive {
		return nil, constants.ErrCLISessionAlreadyDeactivated
	}

	bound := fields.BoundOperatorSessionIDs
	if bound == nil {
		bound = []string{}
	}
	expiry := time.Now().UTC().Add(constants.CLISessionTTL)
	refreshUpdate, err := marshalCLISessionRefreshUpdate(expiry, fields.OperatorSessionID, bound)
	if err != nil {
		return nil, err
	}
	applied, err := s.db.DocConditionalUpdate(
		ctx, marshaler.CollectionName(constants.CollectionCLISessions),
		cliSessionID,
		refreshUpdate,
		"is_active", true,
	)
	if err != nil {
		return nil, fmt.Errorf("refresh CLI session: conditional update: %w", err)
	}
	if !applied {
		// A concurrent caller deactivated the session between our read and the
		// conditional update. Re-read to give a precise error.
		current, err := s.loadCLISession(ctx, cliSessionID)
		if err != nil {
			return nil, fmt.Errorf("refresh CLI session: reload: %w", err)
		}
		if !current.IsActive {
			return nil, constants.ErrCLISessionAlreadyDeactivated
		}
		// Should not happen: the conditional update matched on is_active=true.
		return nil, fmt.Errorf("refresh CLI session: %w", constants.ErrCLISessionInvalid)
	}

	existing.ExpiresAt = expiry
	existing.AbsoluteExpiresAt = expiry
	existing.IdleExpiresAt = expiry
	existing.OperatorSessionID = fields.OperatorSessionID
	existing.BoundOperatorSessionIDs = bound
	s.logger.Info("CLI session refreshed in place",
		"cli_session_id_prefix", safeTruncateID(cliSessionID),
		"user_id", fields.UserID,
		"operator_count", len(bound),
	)
	return existing, nil
}

// BindOperatorSessions rebinds an active CLI session to the given operator
// sessions in place. bound lists every target with primary first, so primary
// must equal bound[0]. The session keeps its ID, certificate fingerprint and
// serial, expiry, and login method: the certificate's URI SAN still names this
// session, signed CLI approvals still match it, and SSE streams subscribed to
// it stay routable. Nothing is created. A missing session returns
// constants.ErrCLISessionNotFound and an inactive one returns
// constants.ErrCLISessionAlreadyDeactivated.
func (s *CLISessionService) BindOperatorSessions(ctx context.Context, cliSessionID, primary string, bound []string) (*models.CLISession, error) {
	if cliSessionID == "" || primary == "" || len(bound) == 0 || bound[0] != primary {
		return nil, fmt.Errorf("bind CLI session: %w", constants.ErrCLISessionInvalid)
	}
	return s.updateCLISessionBinding(ctx, cliSessionID, primary, bound)
}

// UnbindOperatorSessions clears the operator binding (OperatorSessionID and
// BoundOperatorSessionIDs) of an active CLI session in place. The session keeps
// its ID and certificate grounding; nothing is created. Missing and inactive
// sessions are reported as in BindOperatorSessions.
func (s *CLISessionService) UnbindOperatorSessions(ctx context.Context, cliSessionID string) (*models.CLISession, error) {
	if cliSessionID == "" {
		return nil, fmt.Errorf("unbind CLI session: %w", constants.ErrCLISessionInvalid)
	}
	return s.updateCLISessionBinding(ctx, cliSessionID, "", []string{})
}

// updateCLISessionBinding writes the binding fields of an active CLI session
// through a conditional update guarded on is_active=true, so a session that a
// concurrent caller deactivated is never rebound.
func (s *CLISessionService) updateCLISessionBinding(ctx context.Context, cliSessionID, primary string, bound []string) (*models.CLISession, error) {
	// Read first to distinguish not-found from inactive.
	existing, err := s.loadCLISession(ctx, cliSessionID)
	if err != nil {
		return nil, fmt.Errorf("update CLI session binding: load: %w", err)
	}
	if !existing.IsActive {
		return nil, constants.ErrCLISessionAlreadyDeactivated
	}
	bindingUpdate, err := marshalCLISessionBindingUpdate(primary, bound)
	if err != nil {
		return nil, err
	}

	applied, err := s.db.DocConditionalUpdate(
		ctx, marshaler.CollectionName(constants.CollectionCLISessions),
		cliSessionID,
		bindingUpdate,
		"is_active", true,
	)
	if err != nil {
		return nil, fmt.Errorf("update CLI session binding: conditional update: %w", err)
	}
	if !applied {
		// A concurrent caller deactivated the session between our read and the
		// conditional update. Re-read to give a precise error.
		current, err := s.loadCLISession(ctx, cliSessionID)
		if err != nil {
			return nil, fmt.Errorf("update CLI session binding: reload: %w", err)
		}
		if !current.IsActive {
			return nil, constants.ErrCLISessionAlreadyDeactivated
		}
		// Should not happen: the conditional update matched on is_active=true.
		return nil, fmt.Errorf("update CLI session binding: %w", constants.ErrCLISessionInvalid)
	}

	existing.OperatorSessionID = primary
	existing.BoundOperatorSessionIDs = bound
	s.logger.Info("CLI session operator binding updated in place",
		"cli_session_id_prefix", safeTruncateID(cliSessionID),
		"operator_count", len(bound),
	)
	return existing, nil
}

// cliSessionExpired reports whether any of the session's three expiry bounds
// has passed at now. A zero bound is not a bound. This is the rule the auth
// middleware applies to a CLI session.
func cliSessionExpired(session *models.CLISession, now time.Time) bool {
	return (!session.ExpiresAt.IsZero() && session.ExpiresAt.Before(now)) ||
		(!session.AbsoluteExpiresAt.IsZero() && session.AbsoluteExpiresAt.Before(now)) ||
		(!session.IdleExpiresAt.IsZero() && session.IdleExpiresAt.Before(now))
}

// decodeCLISession deserializes a Document into a CLISession.
func decodeCLISession(doc *models.Document) (*models.CLISession, error) {
	var session models.CLISession
	if err := doc.DecodeData(&session); err != nil {
		return nil, fmt.Errorf("decode CLI session: %w", err)
	}
	session.ID = doc.ID
	return &session, nil
}

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
	"github.com/zerotrustdata/ztd/internal/services/storage"
)

// OperatorSessionService handles operator session persistence and management.
// Operator sessions authenticate the host agent via mTLS URI SAN and are used
// by app workloads to look up sessions by ID.
//
// It owns the start of every Gateway-side Operator session: PersistOperatorSession
// writes the session document and the audit-store session row together, so the
// audit store never has to create a session on first use.
type OperatorSessionService struct {
	db         *DocumentStoreService
	auditStore *storage.SQLAuditStore
	logger     *slog.Logger
}

// NewOperatorSessionService creates a new OperatorSessionService instance.
// auditStore is the Gateway audit store that receives the session row at session
// start; it is required, and a nil store fails construction with
// constants.ErrAuditStoreRequired rather than leaving sessions without an audit row.
func NewOperatorSessionService(docStore *DocumentStoreService, auditStore *storage.SQLAuditStore, logger *slog.Logger) (*OperatorSessionService, error) {
	if auditStore == nil {
		return nil, fmt.Errorf("gateway: operator session service: %w", constants.ErrAuditStoreRequired)
	}
	return &OperatorSessionService{
		db:         docStore,
		auditStore: auditStore,
		logger:     logger,
	}, nil
}

// PersistOperatorSession starts an operator session: it starts the audit-store
// session row and persists the operator session document. Re-persisting
// an existing session leaves the audit row's attributes untouched and marks it live.
// Field names match the canonical Operator session document schema.
func (s *OperatorSessionService) PersistOperatorSession(ctx context.Context, operatorSessionID, userID, orgID, operatorID, loginMethod string) error {
	if err := s.startAuditSession(operatorSessionID, operatorID); err != nil {
		return err
	}

	sessionExpiry := time.Now().UTC().Add(1 * time.Hour)
	now := time.Now().UTC()

	operatorSessionDoc := models.OperatorSession{
		ID:                operatorSessionID,
		SessionType:       string(constants.SessionTypeOperator),
		UserID:            userID,
		OrganizationID:    orgID,
		OperatorID:        operatorID,
		IsActive:          true,
		CreatedAt:         now.Format(time.RFC3339),
		AbsoluteExpiresAt: sessionExpiry.Format(time.RFC3339),
		IdleExpiresAt:     sessionExpiry.Format(time.RFC3339),
		LastActivity:      now.Format(time.RFC3339),
		LoginMethod:       loginMethod,
	}

	operatorSessionBytes, err := json.Marshal(operatorSessionDoc)
	if err != nil {
		return fmt.Errorf("failed to marshal Operator session document: %w", err)
	}

	if err := s.db.DocSet(ctx, marshaler.CollectionName(constants.CollectionOperatorSessions), operatorSessionID, operatorSessionBytes); err != nil {
		s.logger.Error("Failed to persist Operator session document", string(constants.ConnectionStateError), err)
		return fmt.Errorf("failed to persist Operator session document: %w", err)
	}

	return nil
}

// startAuditSession starts the audit-store session row for an operator session.
// StartSession is atomic and idempotent: a concurrent or repeated persist of the
// identical session succeeds and marks the row live, and a conflicting row fails.
func (s *OperatorSessionService) startAuditSession(operatorSessionID, operatorID string) error {
	if err := s.auditStore.StartSession(operatorSessionID, constants.SessionTypeOperator, "Operator Session", operatorID); err != nil {
		return fmt.Errorf("start operator audit session: %w", err)
	}
	return nil
}

func (s *OperatorSessionService) DeactivateOperatorSession(ctx context.Context, operatorSessionID string) error {
	if operatorSessionID == "" {
		return constants.ErrGatewayOperatorSessionIDRequired
	}
	doc, err := s.db.DocGet(ctx, marshaler.CollectionName(constants.CollectionOperatorSessions), operatorSessionID)
	if err != nil {
		return fmt.Errorf("deactivate operator session: load: %w", err)
	}
	if doc == nil {
		return constants.ErrGatewayOperatorSessionInvalid
	}
	update, err := json.Marshal(struct {
		IsActive bool `json:"is_active"`
	}{IsActive: false})
	if err != nil {
		return fmt.Errorf("deactivate operator session: marshal: %w", err)
	}
	if _, err := s.db.DocUpdate(ctx, marshaler.CollectionName(constants.CollectionOperatorSessions), operatorSessionID, update); err != nil {
		return fmt.Errorf("deactivate operator session: update: %w", err)
	}
	if err := s.auditStore.EndSession(operatorSessionID); err != nil {
		return fmt.Errorf("deactivate operator session: end audit session: %w", err)
	}
	return nil
}

// GetActiveSessionForUser returns the active operator session the CLI
// should bind to for the given user ID, or nil if none exists. Used by the
// CLI refresh controller to inherit an operator binding when the old CLI
// session is missing (e.g., after a gateway volume reset that wiped CLI
// sessions but left operator sessions intact).
//
// When multiple active sessions exist, the session bound to the gateway's
// embedded operator is preferred — the embedded operator is the canonical
// local binding — otherwise the newest session wins.
func (s *OperatorSessionService) GetActiveSessionForUser(ctx context.Context, userID string) (*models.OperatorSession, error) {
	userIDVal, err := json.Marshal(userID)
	if err != nil {
		return nil, fmt.Errorf("marshal user_id filter: %w", err)
	}
	activeVal, err := json.Marshal(true)
	if err != nil {
		return nil, fmt.Errorf("marshal is_active filter: %w", err)
	}
	docs, err := s.db.DocQuery(
		ctx, marshaler.CollectionName(constants.CollectionOperatorSessions),
		[]models.DocFilter{
			{Field: "user_id", Op: "==", Value: userIDVal},
			{Field: "is_active", Op: "==", Value: activeVal},
		},
		"created_at DESC",
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("query active operator sessions for user %s: %w", userID, err)
	}
	if len(docs) == 0 {
		return nil, nil
	}

	embedded := string(constants.DocIDEmbeddedOperator)
	selected := docs[0] // newest by created_at DESC
	for _, doc := range docs {
		dataBytes, err := json.Marshal(doc.Data)
		if err != nil {
			return nil, fmt.Errorf("marshal operator session document: %w", err)
		}
		var session models.OperatorSession
		if err := json.Unmarshal(dataBytes, &session); err != nil {
			return nil, fmt.Errorf("unmarshal operator session: %w", err)
		}
		if session.OperatorID == embedded {
			selected = doc
			break
		}
	}

	dataBytes, err := json.Marshal(selected.Data)
	if err != nil {
		return nil, fmt.Errorf("marshal operator session document: %w", err)
	}
	var session models.OperatorSession
	if err := json.Unmarshal(dataBytes, &session); err != nil {
		return nil, fmt.Errorf("unmarshal operator session: %w", err)
	}
	session.ID = selected.ID
	return &session, nil
}

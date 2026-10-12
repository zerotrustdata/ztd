// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// Literal JSON paths let SQLite use the enrollment expression indexes. Values
// remain parameters. The live-state predicate is shared by dedup and capacity
// accounting, so expired and terminal history cannot consume admission slots.
const enrollmentLivePredicate = `json_extract(data, '$.state') IN ('pending', 'approved', 'issuing') AND julianday(json_extract(data, '$.expires_at')) >= julianday(?)`

const operatorLeaseIdentityQuery = `SELECT id, data, created_at, updated_at FROM documents
 WHERE collection = ? AND json_extract(data, '$.user_id') = ?
 AND json_extract(data, '$.system_fingerprint') = ?
 AND json_extract(data, '$.operator_type') = ? AND json_extract(data, '$.status') != ?`

// FindOperatorLeases reads the non-terminated remote identities that enrollment
// must replace. Both active and silent leases are replaced, so this lookup needs
// no staleness derivation. Literal JSON paths use the identity index and only
// matching rows are decoded. Liveness reads keep their existing owner.
func (s *DocumentStoreService) FindOperatorLeases(ctx context.Context, ownerID, systemFingerprint string) ([]*models.Document, error) {
	collection := marshaler.CollectionName(constants.CollectionOperators)
	docs, err := sqliteutil.MaterializeRows(ctx, s.db, operatorLeaseIdentityQuery,
		[]interface{}{collection, ownerID, systemFingerprint, constants.OperatorTypeRemote, constants.OperatorStatusTerminated},
		func(row *sql.Rows) (*models.Document, error) {
			var id, data, createdAt, updatedAt string
			if err := row.Scan(&id, &data, &createdAt, &updatedAt); err != nil {
				return nil, fmt.Errorf("scan operator lease: %w", err)
			}
			return scanDocument(collection, id, data, createdAt, updatedAt)
		})
	if err != nil {
		return nil, fmt.Errorf("find operator leases: %w", err)
	}
	return docs, nil
}

// createRequestRecord reserves capacity and inserts the initial, non-mutating
// enrollment request in one write transaction. Duplicate lookup precedes quota
// enforcement so a requester can resume even when the pending budget is full.
// No governance work or certificate signing runs while the transaction is held.
func (s *PlatformEnrollmentService) createRequestRecord(ctx context.Context, req *models.PlatformEnrollmentRequest) (*models.PlatformEnrollmentRequest, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("platform enrollment: encode request: %w", err)
	}
	limit := constants.PlatformEnrollmentMaxLiveRequestsPerComponent
	if req.ComponentKind == models.PlatformComponentOperator {
		limit = constants.PlatformEnrollmentMaxLiveOperatorRequests
	}
	var existing *models.PlatformEnrollmentRequest
	err = s.db.db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		existing = nil // The transaction callback may be retried after SQLITE_BUSY.
		now := timesvc.FormatTimestamp(time.Now().UTC())
		var stored string
		err := conn.QueryRowContext(ctx, `SELECT json_set(data, '$.id', id, '$.created_at', created_at) FROM documents
   WHERE collection = ? AND json_extract(data, '$.component_kind') = ?
   AND json_extract(data, '$.instance_id') = ?
   AND COALESCE(json_extract(data, '$.fingerprints.app'), '') = ?
   AND COALESCE(json_extract(data, '$.fingerprints.operator'), '') = ?
   AND COALESCE(json_extract(data, '$.fingerprints.cli'), '') = ?
   AND `+enrollmentLivePredicate+` ORDER BY created_at, id LIMIT 1`,
			platformEnrollmentCollectionName(), req.ComponentKind, req.InstanceID,
			req.Fingerprints.App, req.Fingerprints.Operator, req.Fingerprints.CLI, now).Scan(&stored)
		if err == nil {
			existing = new(models.PlatformEnrollmentRequest)
			if err := json.Unmarshal([]byte(stored), existing); err != nil {
				return fmt.Errorf("decode existing enrollment: %w", err)
			}
			if !constantTimeEqual(existing.TokenHash, req.TokenHash) {
				return constants.ErrPlatformEnrollmentTokenConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("find existing enrollment: %w", err)
		}
		var tokenHashExists int
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE collection = ? AND json_extract(data, '$.token_hash') = ?)`,
			platformEnrollmentCollectionName(), req.TokenHash).Scan(&tokenHashExists); err != nil {
			return fmt.Errorf("check enrollment token hash: %w", err)
		}
		if tokenHashExists != 0 {
			return constants.ErrPlatformEnrollmentTokenConflict
		}
		var live int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE collection = ?
   AND json_extract(data, '$.component_kind') = ? AND `+enrollmentLivePredicate,
			platformEnrollmentCollectionName(), req.ComponentKind, now).Scan(&live); err != nil {
			return fmt.Errorf("count live enrollments: %w", err)
		}
		if live >= limit {
			return constants.ErrPlatformEnrollmentQuotaExceeded
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO documents(collection, id, data, created_at, updated_at)
   VALUES (?, ?, json_remove(?, '$.id', '$.created_at', '$.updated_at'), ?, ?)`,
			platformEnrollmentCollectionName(), req.ID, string(data), timesvc.FormatTimestamp(req.CreatedAt), now)
		if err != nil {
			return fmt.Errorf("insert enrollment: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("platform enrollment: reserve request: %w", err)
	}
	return existing, nil
}

// TransitionPlatformEnrollment is the single lifecycle writer for enrollment
// requests. It applies only lifecycle edges, only while the stored state is
// still From, and, out of issuing, only while the stored lease still names the
// caller. Acquiring issuing stamps the lease; leaving issuing releases it.
// Returns (false, nil) when the stored request no longer matches.
func (s *DocumentStoreService) TransitionPlatformEnrollment(ctx context.Context, t models.PlatformEnrollmentTransition) (bool, error) {
	query, args, err := platformEnrollmentTransitionStatement(t, time.Now().UTC())
	if err != nil {
		return false, err
	}
	result, err := s.db.ExecWithRetry(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("platform enrollment: transition %s %s -> %s: %w", t.RequestID, t.From, t.To, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("platform enrollment: transition %s: rows affected: %w", t.RequestID, err)
	}
	return n > 0, nil
}

// platformEnrollmentTransitionStatement builds the conditional UPDATE for one
// validated lifecycle transition, shared by the standalone writer and the
// transactional batch decision.
func platformEnrollmentTransitionStatement(t models.PlatformEnrollmentTransition, now time.Time) (string, []interface{}, error) {
	if err := t.Validate(); err != nil {
		return "", nil, err
	}
	var updates []documentUpdateField
	if len(t.Fields) > 0 {
		fields, err := decodeDocumentUpdateFields(t.Fields)
		if err != nil {
			return "", nil, fmt.Errorf("platform enrollment: transition %s: decode fields: %w", t.RequestID, err)
		}
		updates = fields
	}
	lifecycle := struct {
		State        models.PlatformEnrollmentState `json:"state"`
		TransitionAt time.Time                      `json:"last_transition_at"`
		LeaseOwner   *string                        `json:"issuance_lease_owner,omitempty"`
		LeaseExpiry  json.RawMessage                `json:"issuance_lease_expires_at,omitempty"`
	}{State: t.To, TransitionAt: now}
	switch {
	case t.To == models.PlatformEnrollmentStateIssuing:
		expiry, err := json.Marshal(t.LeaseExpiresAt.UTC())
		if err != nil {
			return "", nil, fmt.Errorf("platform enrollment: transition %s: encode lease expiry: %w", t.RequestID, err)
		}
		lifecycle.LeaseOwner, lifecycle.LeaseExpiry = &t.LeaseOwner, expiry
	case t.From == models.PlatformEnrollmentStateIssuing:
		released := ""
		lifecycle.LeaseOwner, lifecycle.LeaseExpiry = &released, json.RawMessage("null")
	}
	encoded, err := json.Marshal(lifecycle)
	if err != nil {
		return "", nil, fmt.Errorf("platform enrollment: transition %s: encode lifecycle: %w", t.RequestID, err)
	}
	fields, err := decodeDocumentUpdateFields(encoded)
	if err != nil {
		return "", nil, fmt.Errorf("platform enrollment: transition %s: decode lifecycle: %w", t.RequestID, err)
	}
	updates = append(updates, fields...)

	dataExpr := "data"
	args := make([]interface{}, 0, 2*len(updates)+6)
	for _, update := range updates {
		dataExpr = "json_set(" + dataExpr + ", ?, json(?))"
		args = append(args, "$."+update.Name, string(update.Value))
	}
	query := "UPDATE documents SET data = " + dataExpr + `, updated_at = ? WHERE collection = ? AND id = ?
 AND json_extract(data, '$.state') = ?`
	args = append(args, timesvc.FormatTimestamp(now), platformEnrollmentCollectionName(), t.RequestID, string(t.From))
	if t.From == models.PlatformEnrollmentStateIssuing {
		query += ` AND json_extract(data, '$.issuance_lease_owner') = ?`
		args = append(args, t.LeaseOwner)
	}
	return query, args, nil
}

// DecidePlatformEnrollments is the executing handler's atomic decision boundary.
// It rechecks the active owner, expiry, state, and displayed keys under the same
// SQLite write reservation as all transitions. A stale member rolls back the
// entire cohort; every member records the enclosing governed receipt ID.
func (s *DocumentStoreService) DecidePlatformEnrollments(ctx context.Context, actorID string, req models.PlatformEnrollmentBatchDecisionRequest, envelopeID string) error {
	if err := req.Validate(); err != nil {
		return err
	}
	if actorID == "" || envelopeID == "" {
		return constants.ErrPlatformEnrollmentInvalidDecision
	}
	state := models.PlatformEnrollmentStateApproved
	if req.Decision == models.PlatformEnrollmentDecisionDeny {
		state = models.PlatformEnrollmentStateDenied
	}
	return s.db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		var ownerID string
		var ownerData []byte
		err := conn.QueryRowContext(ctx, `SELECT id, data FROM documents WHERE collection = ? ORDER BY created_at, rowid LIMIT 1`,
			marshaler.CollectionName(constants.CollectionUsers)).Scan(&ownerID, &ownerData)
		if errors.Is(err, sql.ErrNoRows) {
			return constants.ErrPlatformEnrollmentInvalidDecision
		}
		if err != nil {
			return fmt.Errorf("load enrollment owner: %w", err)
		}
		var owner models.User
		if err := json.Unmarshal(ownerData, &owner); err != nil {
			return fmt.Errorf("decode enrollment owner: %w", err)
		}
		if ownerID != actorID || !owner.IsActive() {
			return constants.ErrPlatformEnrollmentInvalidDecision
		}
		now := time.Now().UTC()
		update, err := json.Marshal(struct {
			Owner      string    `json:"approved_by_user_id"`
			DecidedAt  time.Time `json:"decided_at"`
			Reason     string    `json:"decision_reason"`
			EnvelopeID string    `json:"decision_envelope_id"`
			ReceiptID  string    `json:"decision_receipt_id"`
		}{actorID, now, req.Reason, envelopeID, envelopeID})
		if err != nil {
			return fmt.Errorf("encode enrollment decision: %w", err)
		}
		for _, target := range req.Requests {
			var data []byte
			err := conn.QueryRowContext(ctx, `SELECT data FROM documents WHERE collection = ? AND id = ?`,
				platformEnrollmentCollectionName(), target.RequestID).Scan(&data)
			if errors.Is(err, sql.ErrNoRows) {
				return constants.ErrPlatformEnrollmentRequestNotFound
			}
			if err != nil {
				return fmt.Errorf("load enrollment %s: %w", target.RequestID, err)
			}
			var stored models.PlatformEnrollmentRequest
			if err := json.Unmarshal(data, &stored); err != nil {
				return fmt.Errorf("decode enrollment %s: %w", target.RequestID, err)
			}
			if stored.State != models.PlatformEnrollmentStatePending {
				return constants.ErrPlatformEnrollmentAlreadyDecided
			}
			if now.After(stored.ExpiresAt) {
				return constants.ErrPlatformEnrollmentRequestExpired
			}
			if !fingerprintsMatch(stored.Fingerprints, target.Fingerprints) {
				return constants.ErrPlatformEnrollmentInvalidDecision
			}
			query, args, err := platformEnrollmentTransitionStatement(models.PlatformEnrollmentTransition{
				RequestID: target.RequestID, From: stored.State, To: state, Fields: update,
			}, now)
			if err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("decide enrollment %s: %w", target.RequestID, err)
			}
		}
		return nil
	})
}

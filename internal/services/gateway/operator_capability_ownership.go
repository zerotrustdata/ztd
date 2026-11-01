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
	"errors"
	"fmt"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/marshaler"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/operatorcapability"
	"github.com/zerotrustdata/ztd/internal/timesvc"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// RegisterOperator validates capabilities and reserves the actuator identity in
// the same transaction that persists an enrolled Operator.
// A replacement for the same owner's fingerprint or Actuator key may reuse its prior identity;
// the issuance handler deactivates those superseded leases before delivery.
func (s *DocumentStoreService) RegisterOperator(ctx context.Context, op *operatorv1.OperatorDocument) error {
	if op == nil || op.GetId() == "" {
		return constants.ErrRegistrationOperatorIDRequired
	}
	if err := models.OperatorCapabilitiesFromProto(op.GetOperatorCapabilities()).Validate(); err != nil {
		return err
	}
	raw, err := models.MarshalOperatorDocument(op)
	if err != nil {
		return fmt.Errorf("register operator: encode: %w", err)
	}
	return s.db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		if err := validateOperatorClaims(ctx, conn, op); err != nil {
			return err
		}
		now := timesvc.FormatTimestamp(time.Now().UTC())
		_, err := conn.ExecContext(ctx, `INSERT INTO documents(collection, id, data, created_at, updated_at)
            VALUES (?, ?, json_remove(?, '$.id', '$.created_at', '$.updated_at'), ?, ?)`,
			marshaler.CollectionName(constants.CollectionOperators), op.GetId(), string(raw), now, now)
		if err != nil {
			return fmt.Errorf("register operator: persist: %w", err)
		}
		return nil
	})
}

// SetOperatorRuntimeConfig validates the complete capability set and persists it
// under the same write transaction as its actuator identity check.
func (s *DocumentStoreService) SetOperatorRuntimeConfig(ctx context.Context, id string, cfg *operatorv1.OperatorRuntimeConfig) error {
	if id == "" {
		return constants.ErrRegistrationOperatorIDRequired
	}
	if cfg == nil {
		return constants.ErrMissingRequiredField
	}
	if err := models.OperatorCapabilitiesFromProto(cfg.GetCapabilities()).Validate(); err != nil {
		return err
	}
	return s.db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		var stored string
		err := conn.QueryRowContext(ctx, `SELECT json_set(data, '$.id', id) FROM documents WHERE collection = ? AND id = ?`,
			marshaler.CollectionName(constants.CollectionOperators), id).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			return constants.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("operator runtime config: load: %w", err)
		}
		op, err := models.UnmarshalOperatorDocument([]byte(stored))
		if err != nil {
			return err
		}
		op.RuntimeConfig = cfg
		op.OperatorCapabilities = models.OperatorCapabilitiesToProto(operatorcapability.ResolveOperatorCapabilities(cfg))
		op.LocalDir, op.Account, op.Port = cfg.GetLocalDir(), cfg.GetAccount(), cfg.GetHttpPort()
		if err := validateOperatorClaims(ctx, conn, op); err != nil {
			return err
		}
		raw, err := models.MarshalOperatorDocument(op)
		if err != nil {
			return fmt.Errorf("operator runtime config: encode: %w", err)
		}
		_, err = conn.ExecContext(ctx, `UPDATE documents SET data = json_remove(?, '$.id', '$.created_at', '$.updated_at'), updated_at = ? WHERE collection = ? AND id = ?`,
			string(raw), timesvc.FormatTimestamp(time.Now().UTC()), marshaler.CollectionName(constants.CollectionOperators), id)
		if err != nil {
			return fmt.Errorf("operator runtime config: persist: %w", err)
		}
		return nil
	})
}

const operatorActuatorKeyQuery = `SELECT json_set(data, '$.id', id) FROM documents
 WHERE collection = ? AND json_extract(data, '$.actuator_key_id') = ?
 AND id != ? AND COALESCE(json_extract(data, '$.status'), '') != ?`

func isOperatorExempt(candidate, other *operatorv1.OperatorDocument) bool {
	if other.GetIsSlot() && !other.GetClaimed() {
		return true
	}
	if other.GetOperatorType() != string(constants.OperatorTypeEmbedded) && other.GetOperatorSessionId() == "" {
		return true
	}
	return isSameRemoteOperator(candidate, other)
}

// isSameRemoteOperator reports whether candidate re-enrolls other's runtime: the
// same owner's remote Operator presenting the same system fingerprint or the
// same Actuator key. Capabilities are not identity, so a re-enrollment that adds
// or drops a capability replaces its prior lease instead of competing with it.
func isSameRemoteOperator(candidate, other *operatorv1.OperatorDocument) bool {
	if candidate.GetOperatorType() != string(constants.OperatorTypeRemote) || other.GetOperatorType() != string(constants.OperatorTypeRemote) ||
		candidate.GetUserId() != other.GetUserId() {
		return false
	}
	if candidate.GetSystemFingerprint() != "" && candidate.GetSystemFingerprint() == other.GetSystemFingerprint() {
		return true
	}
	return candidate.GetActuatorKeyId() != "" && candidate.GetActuatorKeyId() == other.GetActuatorKeyId()
}

func checkActuatorKeyClaim(ctx context.Context, conn *sql.Conn, collection string, candidate *operatorv1.OperatorDocument) error {
	rows, err := conn.QueryContext(ctx, operatorActuatorKeyQuery,
		collection, candidate.GetActuatorKeyId(), candidate.GetId(), constants.OperatorStatusTerminated)
	if err != nil {
		return fmt.Errorf("operator claims: query actuator key: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("operator claims: scan actuator key: %w", err)
		}
		other, err := models.UnmarshalOperatorDocument([]byte(raw))
		if err != nil {
			return fmt.Errorf("operator claims: decode actuator key: %w", err)
		}
		if isOperatorExempt(candidate, other) {
			continue
		}
		if candidate.GetActuatorKeyId() == other.GetActuatorKeyId() {
			return fmt.Errorf("%w: Operators %s and %s; use separate --working-dir roots or combine --capabilities in one Operator", constants.ErrOperatorActuatorKeyInUse, candidate.GetId(), other.GetId())
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("operator claims: read actuator key: %w", err)
	}
	return nil
}

// validateOperatorClaims runs only inside the registry owner's write transaction.
// Any number of Operators may hold any combination of capabilities; only the
// Actuator key identity is exclusive to one non-terminated Operator runtime.
func validateOperatorClaims(ctx context.Context, conn *sql.Conn, candidate *operatorv1.OperatorDocument) error {
	collection := marshaler.CollectionName(constants.CollectionOperators)
	if candidate.GetActuatorKeyId() != "" {
		if err := checkActuatorKeyClaim(ctx, conn, collection, candidate); err != nil {
			return err
		}
	}
	return nil
}

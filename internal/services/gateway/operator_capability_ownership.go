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

// RegisterOperator reserves singleton capabilities and the actuator identity in
// the same transaction that persists an enrolled Operator. Offline and stopped
// leases retain their claims until termination; a reconnect cannot steal one.
// A replacement for the same owner's fingerprint may reuse its prior claims;
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
		var embeddedReady bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE collection = ? AND id = ? AND json_type(data, '$.runtime_config') = 'object')`,
			marshaler.CollectionName(constants.CollectionOperators), constants.DocIDEmbeddedOperator).Scan(&embeddedReady); err != nil {
			return fmt.Errorf("register operator: check embedded startup: %w", err)
		}
		if !embeddedReady {
			return constants.ErrEmbeddedOperatorNotReady
		}

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
// under the same write transaction as its ownership check. Gateway startup and
// bootstrap therefore cannot race another enrollment for a singleton capability.
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

// validateOperatorClaims runs only inside the registry owner's write transaction.
// Claims cover the Gateway, regardless of approving user. Data is unrestricted;
// every other capability belongs to exactly one non-terminated Operator.
func validateOperatorClaims(ctx context.Context, conn *sql.Conn, candidate *operatorv1.OperatorDocument) error {
	rows, err := conn.QueryContext(ctx, `SELECT json_set(data, '$.id', id) FROM documents WHERE collection = ? AND id != ? AND COALESCE(json_extract(data, '$.status'), '') != ? ORDER BY id`,
		marshaler.CollectionName(constants.CollectionOperators), candidate.GetId(), constants.OperatorStatusTerminated)
	if err != nil {
		return fmt.Errorf("operator claims: query: %w", err)
	}
	defer rows.Close()
	capabilities := operatorcapability.GetOperatorCapabilities(candidate)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("operator claims: scan: %w", err)
		}
		other, err := models.UnmarshalOperatorDocument([]byte(raw))
		if err != nil {
			return fmt.Errorf("operator claims: decode: %w", err)
		}
		if other.GetIsSlot() && !other.GetClaimed() {
			continue
		}
		if other.GetOperatorType() != string(constants.OperatorTypeEmbedded) && other.GetOperatorSessionId() == "" {
			continue
		}
		if candidate.GetOperatorType() == string(constants.OperatorTypeRemote) && other.GetOperatorType() == string(constants.OperatorTypeRemote) &&
			candidate.GetSystemFingerprint() != "" && candidate.GetSystemFingerprint() == other.GetSystemFingerprint() && candidate.GetUserId() == other.GetUserId() {
			continue
		}
		if candidate.GetActuatorKeyId() != "" && candidate.GetActuatorKeyId() == other.GetActuatorKeyId() {
			return fmt.Errorf("%w: Operators %s and %s; use separate --working-dir roots or combine --capabilities in one Operator", constants.ErrOperatorActuatorKeyInUse, candidate.GetId(), other.GetId())
		}
		occupied := operatorcapability.GetOperatorCapabilities(other)
		for _, capability := range capabilities {
			if capability != constants.OperatorCapabilityData && occupied.Has(capability) {
				return fmt.Errorf("%w: %s belongs to Operator %s; terminate that Operator or remove the capability before assigning it to %s", constants.ErrOperatorCapabilityInUse, capability, other.GetId(), candidate.GetId())
			}
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("operator claims: read: %w", err)
	}
	return nil
}

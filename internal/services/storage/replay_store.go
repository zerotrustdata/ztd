// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// replayStoreSchema is the only definition of the nonces table. The schema is
// fresh-only: CREATE TABLE IF NOT EXISTS with no ALTER TABLE migrations.
const replayStoreSchema = `
CREATE TABLE IF NOT EXISTS nonces (
	nonce TEXT PRIMARY KEY,
	reserved_at TEXT NOT NULL,
	used_at TEXT,
	expires_at TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'reserved'
);
CREATE INDEX IF NOT EXISTS idx_nonces_expires ON nonces(expires_at);
`

// defaultNonceListLimit is the page size ListNonces uses when the caller passes a non-positive limit.
const defaultNonceListLimit = 100

// NonceRow represents a single nonce record for export.
type NonceRow struct {
	Nonce      string
	Status     string
	ReservedAt time.Time
	UsedAt     *time.Time
	ExpiresAt  time.Time
}

// ReplayStore provides nonce replay protection over the runtime's canonical
// ztd.db nonces table. It is the only governance.ReplayStore implementation:
// the Gateway and the outbound Operator each build one over their own ztd.db.
// It shares the caller's connection pool, so Close does not close the database.
type ReplayStore struct {
	db     *sqliteutil.DB
	logger *slog.Logger
}

// NewReplayStore applies the nonces schema to db and returns a store over it.
func NewReplayStore(db *sqliteutil.DB, logger *slog.Logger) (*ReplayStore, error) {
	if db == nil {
		return nil, fmt.Errorf("replay_store: %w", constants.ErrStorageConfigRequired)
	}
	if _, err := db.ExecWithRetry(context.Background(), replayStoreSchema); err != nil {
		return nil, fmt.Errorf("replay_store: init schema: %w", err)
	}
	return &ReplayStore{db: db, logger: logger}, nil
}

// ReserveNonce atomically reserves a nonce for early replay protection.
// It returns true when the nonce was already reserved or used (replay detected)
// and false when the reservation succeeded. The primary key is the replay check:
// a nonce already present fails the insert. Any other error fails closed.
func (s *ReplayStore) ReserveNonce(nonce string, expiresAt time.Time) (bool, error) {
	_, err := s.db.ExecWithRetry(context.Background(),
		"INSERT INTO nonces (nonce, reserved_at, expires_at, status) VALUES (?, ?, ?, 'reserved')",
		nonce, timesvc.NowTimestamp(), timesvc.FormatTimestamp(expiresAt),
	)
	if err != nil {
		if sqliteutil.IsUniqueConstraintError(err) {
			s.logger.Warn("Nonce replay detected", "nonce", nonce)
			return true, nil
		}
		return false, fmt.Errorf("reserve nonce: %w", err)
	}
	return false, nil
}

// FinalizeNonce marks a reserved nonce as fully consumed. A nonce that is not
// in the reserved state is left unchanged.
func (s *ReplayStore) FinalizeNonce(nonce string) error {
	_, err := s.db.ExecWithRetry(context.Background(),
		"UPDATE nonces SET used_at = ?, status = 'used' WHERE nonce = ? AND status = 'reserved'",
		timesvc.NowTimestamp(), nonce,
	)
	if err != nil {
		return fmt.Errorf("finalize nonce: %w", err)
	}
	return nil
}

// ReleaseNonce removes a reservation for a failed transaction. A nonce that
// was already finalized is left in place.
func (s *ReplayStore) ReleaseNonce(nonce string) error {
	_, err := s.db.ExecWithRetry(context.Background(),
		"DELETE FROM nonces WHERE nonce = ? AND status = 'reserved'", nonce)
	if err != nil {
		return fmt.Errorf("release nonce: %w", err)
	}
	return nil
}

// CleanupExpiredNonces removes expired nonces from the database.
func (s *ReplayStore) CleanupExpiredNonces() error {
	_, err := s.db.ExecWithRetry(context.Background(),
		"DELETE FROM nonces WHERE expires_at < ?", timesvc.NowTimestamp())
	if err != nil {
		return fmt.Errorf("cleanup expired nonces: %w", err)
	}
	return nil
}

// Close is a no-op; the store shares the connection pool owned by the caller.
func (s *ReplayStore) Close() error {
	return nil
}

// ListNonces retrieves nonce records with pagination, ordered by reserved_at ASC.
func (s *ReplayStore) ListNonces(limit, offset int) ([]*NonceRow, error) {
	if limit <= 0 {
		limit = defaultNonceListLimit
	}

	type nonceRowRaw struct {
		nonce      string
		reservedAt string
		usedAt     sql.NullString
		expiresAt  string
		status     string
	}

	raw, err := sqliteutil.MaterializeRows(context.Background(), s.db,
		`SELECT nonce, reserved_at, used_at, expires_at, status
		 FROM nonces ORDER BY reserved_at ASC, nonce ASC LIMIT ? OFFSET ?`,
		[]interface{}{limit, offset},
		func(r *sql.Rows) (nonceRowRaw, error) {
			var row nonceRowRaw
			err := r.Scan(&row.nonce, &row.reservedAt, &row.usedAt, &row.expiresAt, &row.status)
			return row, err
		})
	if err != nil {
		return nil, fmt.Errorf("replay store: list nonces: %w", err)
	}

	results := make([]*NonceRow, 0, len(raw))
	for _, row := range raw {
		n := &NonceRow{Nonce: row.nonce, Status: row.status}
		if n.ReservedAt, err = timesvc.ParseTimestamp(row.reservedAt); err != nil {
			return nil, fmt.Errorf("replay store: nonce %s reserved_at: %w", row.nonce, err)
		}
		if n.ExpiresAt, err = timesvc.ParseTimestamp(row.expiresAt); err != nil {
			return nil, fmt.Errorf("replay store: nonce %s expires_at: %w", row.nonce, err)
		}
		if row.usedAt.Valid {
			used, err := timesvc.ParseTimestamp(row.usedAt.String)
			if err != nil {
				return nil, fmt.Errorf("replay store: nonce %s used_at: %w", row.nonce, err)
			}
			n.UsedAt = &used
		}
		results = append(results, n)
	}
	return results, nil
}

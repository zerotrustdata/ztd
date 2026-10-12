// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storagetest

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// ChaosEvent is one generated payload outcome recorded by a chaos run. Only
// classifications and identifiers are stored: executed payloads are recorded as
// real audit events by the runtime's audit store, and rejected payloads are
// classified by Category and Outcome.
type ChaosEvent struct {
	OperatorSessionID string
	Timestamp         time.Time
	ChaosID           int
	Category          string
	Outcome           string
	TransactionHash   string
}

// chaosEventSchema is the chaos run's own schema. It lives in chaos.db under the
// run's data directory, separate from the runtime's canonical ztd.db.
const chaosEventSchema = `
CREATE TABLE IF NOT EXISTS chaos_events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	operator_session_id TEXT,
	timestamp TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now')),
	chaos_id INTEGER NOT NULL,
	category TEXT NOT NULL,
	outcome TEXT NOT NULL,
	transaction_hash TEXT
);
CREATE INDEX IF NOT EXISTS idx_chaos_events_session_id ON chaos_events(operator_session_id);
CREATE INDEX IF NOT EXISTS idx_chaos_events_category ON chaos_events(category);
`

// ChaosEventStore records chaos outcomes in the run's chaos.db.
type ChaosEventStore struct {
	db *sqliteutil.DB
}

// OpenChaosEventStore opens (creating if needed) the chaos.db of the run rooted at fileSvc.
func OpenChaosEventStore(fileSvc fs.RuntimeFileService, logger *slog.Logger) (*ChaosEventStore, error) {
	ctx := context.Background()
	if err := fileSvc.MkdirAll(ctx, constants.DataDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.ChaosDBRelPath)), logger)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreOpenDBFailed, err)
	}
	if _, err := db.ExecWithRetry(ctx, chaosEventSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreInitSchemaFailed, err)
	}
	return &ChaosEventStore{db: db}, nil
}

// RecordChaosEvents writes events in a single transaction.
func (s *ChaosEventStore) RecordChaosEvents(events []*ChaosEvent) error {
	if len(events) == 0 {
		return nil
	}

	return s.db.ExecInTxWithRetry(context.Background(), func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`
		INSERT INTO chaos_events (
			operator_session_id, timestamp, chaos_id, category, outcome, transaction_hash
		) VALUES (?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return fmt.Errorf("%w: %w", constants.ErrAuditStorePrepareBatchFailed, err)
		}
		defer stmt.Close()

		for _, event := range events {
			if _, err := stmt.Exec(
				event.OperatorSessionID,
				timesvc.FormatTimestamp(event.Timestamp),
				event.ChaosID,
				event.Category,
				event.Outcome,
				event.TransactionHash,
			); err != nil {
				return fmt.Errorf("failed to record chaos event: %w", err)
			}
		}
		return nil
	})
}

// Close closes chaos.db.
func (s *ChaosEventStore) Close() error {
	return s.db.Close()
}

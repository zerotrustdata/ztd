// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// LocalFirstEventsStoreConfig holds configuration for the runtime's local-first events store.
type LocalFirstEventsStoreConfig struct {
	RetentionDays        int
	PruneIntervalMinutes int
	// EncryptionVault is the required vault.Vault for encrypting sensitive content fields.
	// content_text, command_raw, command_stdout, and command_stderr are encrypted at rest.
	EncryptionVault *vault.Vault
}

// DefaultLocalFirstEventsStoreConfig returns the default configuration for the events store.
func DefaultLocalFirstEventsStoreConfig() *LocalFirstEventsStoreConfig {
	return &LocalFirstEventsStoreConfig{
		RetentionDays:        90,
		PruneIntervalMinutes: 60,
	}
}

// FileMutationOperation represents the type of file operation
type FileMutationOperation string

const (
	FileMutationWrite   FileMutationOperation = "WRITE"
	FileMutationCreate  FileMutationOperation = "CREATE"
	FileMutationRestore FileMutationOperation = "RESTORE"
)

// OperatorSession represents a chat session in the events log
type OperatorSession struct {
	ID           string
	SessionType  string
	Title        string
	CreatedAt    time.Time
	UserIdentity string
}

// Event represents an event in the events log (append-only)
type Event struct {
	ID                  int64
	Seq                 int64
	PrevHash            string
	ContentDigest       string
	Hash                string
	TransactionID       string
	OperatorSessionID   string
	Timestamp           time.Time
	Type                constants.EventType
	ContentText         string
	CommandRaw          string
	CommandExitCode     int
	CommandStdout       string
	CommandStderr       string
	ExecutionDurationMs int64
	StoredLocally       bool
	// ExecutionID links a command or file operation to its execution. events is the
	// sole execution record, so FETCH_LOGS reads it by this key.
	ExecutionID string
}

// FileMutationLog represents a file mutation record linked to an event
type FileMutationLog struct {
	ID int64
	// UID is the row identity bound into the AEAD data of the encrypted columns. It is
	// generated at insert and never reused.
	UID              string
	EventID          int64
	Filepath         string
	Operation        FileMutationOperation
	LedgerHashBefore string
	LedgerHashAfter  string
	DiffStat         string
	// OperatorSessionID and Timestamp are read from the owning event. They are not
	// inserted. The session keys the ledger when the diff is computed.
	OperatorSessionID string
	Timestamp         time.Time
}

// LocalFirstEventsStore owns the runtime's local event records in ztd.db.
// Event receipt stages are facts on its events chain; current receipt queries
// project those same rows. Remote Gateway mirrors are best-effort projections.
type LocalFirstEventsStore struct {
	db               *sqliteutil.DB
	config           *LocalFirstEventsStoreConfig
	logger           *slog.Logger
	fileSvc          fs.RuntimeFileService
	encryptionVault  *vault.Vault
	pruner           *sqliteutil.Pruner
	commitmentLedger *CommitmentLedger
	closeOnce        sync.Once

	muWrites sync.WaitGroup
}

// NewLocalFirstEventsStore creates a local-first events store over the runtime's canonical
// database. The caller owns db: the store shares its pool and writer gate with
// every other writer of ztd.db and never closes it.
func NewLocalFirstEventsStore(config *LocalFirstEventsStoreConfig, db *sqliteutil.DB, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*LocalFirstEventsStore, error) {
	if config == nil {
		config = DefaultLocalFirstEventsStoreConfig()
	}
	if db == nil {
		return nil, fmt.Errorf("events store: database: %w", constants.ErrMissingRequiredField)
	}

	if config.EncryptionVault == nil {
		return nil, constants.ErrLocalFirstEventsStoreEncryptionVaultRequired
	}
	if fileSvc == nil {
		return nil, fmt.Errorf("events store: file service: %w", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		logger = slog.Default()
	}

	ass := &LocalFirstEventsStore{
		config:          config,
		logger:          logger,
		fileSvc:         fileSvc,
		encryptionVault: config.EncryptionVault,
		db:              db,
	}

	if err := ass.bootstrap(); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreBootstrapFailed, err)
	}

	interval := time.Duration(config.PruneIntervalMinutes) * time.Minute
	ass.pruner = sqliteutil.NewPruner(ass.db, logger, interval, eventsStorePrune(config))
	ass.pruner.Start()

	ass.logger.Info("Local-first events store initialized",
		"data_dir", ass.fileSvc.Resolve(constants.DataDirname),
		"db_path", db.GetPath())

	return ass, nil
}

func (ass *LocalFirstEventsStore) CommitmentLedger() *CommitmentLedger {
	if ass == nil {
		return nil
	}
	return ass.commitmentLedger
}

// bootstrap initializes the events store (directory structure, database)
func (ass *LocalFirstEventsStore) bootstrap() error {
	ass.logger.Info("Bootstrapping events store", "data_dir", ass.fileSvc.Resolve(constants.DataDirname))

	if err := ass.createDirectoryStructure(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreCreateDirFailed, err)
	}

	if err := ass.verifyWritePermissions(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreNotWritable, err)
	}

	if err := ass.initDatabase(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreInitDBFailed, err)
	}

	ass.logger.Info("Events store bootstrap completed successfully")
	return nil
}

// createDirectoryStructure creates the events store directory structure
func (ass *LocalFirstEventsStore) createDirectoryStructure() error {
	if err := ass.fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w %s: %w", constants.ErrLocalFirstEventsStoreCreateDirPathFailed, constants.DataDirname, err)
	}

	ass.logger.Info("Events store directory structure ensured",
		"data_dir", ass.fileSvc.Resolve(constants.DataDirname))

	return nil
}

// verifyWritePermissions ensures the data directory is writable
func (ass *LocalFirstEventsStore) verifyWritePermissions() error {
	testRelPath := filepath.Join(constants.DataDirname, ".write_test")

	if err := ass.fileSvc.WriteFile(context.Background(), testRelPath, []byte("write_test"), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("%w %s: %w", constants.ErrLocalFirstEventsStoreCannotWrite, ass.fileSvc.Resolve(constants.DataDirname), err)
	}

	if err := ass.fileSvc.Remove(context.Background(), testRelPath); err != nil {
		ass.logger.Warn("Failed to remove write test file", "path", testRelPath, string(constants.ConnectionStateError), err)
	}

	ass.logger.Info("Write permissions verified", "path", ass.fileSvc.Resolve(constants.DataDirname))
	return nil
}

// initDatabase applies the events-store schema to the shared database. The schema is
// fresh-only: CREATE TABLE IF NOT EXISTS is the whole schema, with no ALTER TABLE
// migrations or column back-fills.
func (ass *LocalFirstEventsStore) initDatabase() error {
	if _, err := ass.db.Exec(eventsStoreSchema); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreInitSchemaFailed, err)
	}

	ass.commitmentLedger = NewCommitmentLedger(ass.db, ass.logger, ass.encryptionVault)

	ass.logger.Info("Database schema initialized")
	return nil
}

// eventsStoreSchema defines the initial schema for the events store database.
const eventsStoreSchema = `
-- Content columns (BLOB) are encrypted with the runtime vault on write and decrypted on
-- read. A locked vault fails the write. Identifiers, hashes, signatures, enum types,
-- statuses, timestamps and sizes stay plaintext. See docs/architecture/encryption.md.
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	title BLOB,
	session_type TEXT NOT NULL DEFAULT 'operator',
	created_at TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now')),
	user_identity BLOB,
	-- ended_at is NULL while the owning runtime session is live. EndSession sets it and
	-- StartSession clears it. The pruner only removes ended sessions.
	ended_at TEXT
);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	seq INTEGER UNIQUE,
	prev_hash TEXT,
	content_digest TEXT,
	hash TEXT,
	transaction_id TEXT,
	operator_session_id TEXT,
	timestamp TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now')),
	type TEXT NOT NULL,
	content_text BLOB,
	command_raw BLOB,
	command_exit_code INTEGER,
	command_stdout BLOB,
	command_stderr BLOB,
	execution_duration_ms INTEGER,
	stored_locally INTEGER DEFAULT 1,
	execution_id TEXT,
	-- Event receipt columns are null on events that are not the current event
	-- receipt for a transaction. The canonical signed body is content_text.
	-- event_receipt_status IS NOT NULL marks the one current row. A later
	-- non-terminal stage stays on the chain with these columns null.
	event_receipt_transaction_hash TEXT,
	event_receipt_investigation_id TEXT,
	event_receipt_operator_id TEXT,
	event_receipt_operator_session_id TEXT,
	event_receipt_requestor_user_id TEXT,
	event_receipt_acting_app_id TEXT,
	event_receipt_event_type TEXT,
	event_receipt_action_type TEXT,
	event_receipt_target_resource BLOB,
	event_receipt_status INTEGER,
	event_receipt_result_summary BLOB,
	event_receipt_state_root_before TEXT,
	event_receipt_state_root_after TEXT,
	event_receipt_executed_at_ms INTEGER,
	event_receipt_signer_key_id TEXT,
	event_receipt_signature TEXT,
	FOREIGN KEY(operator_session_id) REFERENCES sessions(id)
);

CREATE INDEX IF NOT EXISTS idx_events_execution_id ON events(execution_id);

CREATE TABLE IF NOT EXISTS file_mutation_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	mutation_uid TEXT NOT NULL UNIQUE,
	event_id INTEGER NOT NULL,
	filepath BLOB NOT NULL,
	operation TEXT NOT NULL,
	ledger_hash_before TEXT,
	ledger_hash_after TEXT,
	diff_stat BLOB,
	FOREIGN KEY(event_id) REFERENCES events(id)
);

CREATE TABLE IF NOT EXISTS commitment_ledger (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	transaction_id TEXT NOT NULL,
	transaction_hash TEXT NOT NULL,
	prior_commitment_hash TEXT NOT NULL,
	state_root_at_commit TEXT,
	l2_signature_digest TEXT,
	warden_intent_signature_digest TEXT,
	human_signature_digest TEXT,
	action_type TEXT,
	target_resource BLOB,
	committed_at_unix_ms INTEGER NOT NULL,
	auditor_key_id TEXT,
	signature TEXT,
	hash TEXT NOT NULL,
	attestation_json BLOB NOT NULL,
	UNIQUE(hash)
);

CREATE TABLE IF NOT EXISTS audit_chain_checkpoints (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	pruned_through_seq INTEGER NOT NULL,
	pruned_through_hash TEXT NOT NULL,
	pruned_count INTEGER NOT NULL,
	checkpoint_hash TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_session_id ON events(operator_session_id);
CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
CREATE INDEX IF NOT EXISTS idx_events_transaction_id_type ON events(transaction_id, type);
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_acting_app ON events(event_receipt_acting_app_id, transaction_id) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_investigation ON events(event_receipt_investigation_id, event_receipt_action_type) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_session ON events(event_receipt_operator_session_id) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_operator ON events(event_receipt_operator_id) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_timestamp ON events(timestamp) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_events_event_receipt_tx ON events(transaction_id) WHERE event_receipt_status IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_file_mutation_event_id ON file_mutation_log(event_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_id_type ON sessions(id, session_type);
CREATE INDEX IF NOT EXISTS idx_commitment_ledger_committed_at ON commitment_ledger(committed_at_unix_ms);
CREATE INDEX IF NOT EXISTS idx_audit_chain_checkpoints_seq ON audit_chain_checkpoints(pruned_through_seq);
`

// CreateSession creates a new session in the events log
func (ass *LocalFirstEventsStore) CreateSession(id string, sessionType constants.SessionType, title, userIdentity string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrEventsStoreSessionMissing
	}
	if sessionType == "" {
		sessionType = constants.SessionTypeOperator
	}

	titleBytes, identityBytes, err := ass.encryptSessionFields(id, title, userIdentity)
	if err != nil {
		return err
	}
	query := `INSERT INTO sessions (id, session_type, title, user_identity) VALUES (?, ?, ?, ?)`
	if _, err := ass.db.ExecWithRetry(context.Background(), query, id, string(sessionType), titleBytes, identityBytes); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreCreateSessionFailed, err)
	}

	ass.logger.Info("OperatorSession created", "operator_session_id", id, "session_type", string(sessionType))
	return nil
}

// StartSession starts the audit session a runtime owns. It is the single start
// path for a runtime session: a new row is inserted, and an existing row with
// identical attributes is the idempotent restart case, which marks it live again
// (clears ended_at). An existing row with a different session type, title, or
// user identity fails with ErrEventsStoreSessionConflict. The insert and the check run
// in one immediate transaction, so concurrent identical starts both succeed.
func (ass *LocalFirstEventsStore) StartSession(id string, sessionType constants.SessionType, title, userIdentity string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrEventsStoreSessionMissing
	}
	if sessionType == "" {
		sessionType = constants.SessionTypeOperator
	}

	titleBytes, identityBytes, err := ass.encryptSessionFields(id, title, userIdentity)
	if err != nil {
		return err
	}

	ctx := context.Background()
	err = ass.db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx,
			`INSERT INTO sessions (id, session_type, title, user_identity) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
			id, string(sessionType), titleBytes, identityBytes)
		if err != nil {
			return err
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if inserted > 0 {
			return nil
		}

		var existingType sql.NullString
		var existingTitle, existingIdentity []byte
		if err := conn.QueryRowContext(ctx,
			`SELECT session_type, title, user_identity FROM sessions WHERE id = ?`, id,
		).Scan(&existingType, &existingTitle, &existingIdentity); err != nil {
			return err
		}
		existingTitleText, err := ass.decryptColumn("sessions.title", id, existingTitle)
		if err != nil {
			return err
		}
		existingIdentityText, err := ass.decryptColumn("sessions.user_identity", id, existingIdentity)
		if err != nil {
			return err
		}
		if existingType.String != string(sessionType) || existingTitleText != title || existingIdentityText != userIdentity {
			return fmt.Errorf("%w: %s", constants.ErrEventsStoreSessionConflict, id)
		}
		_, err = conn.ExecContext(ctx, `UPDATE sessions SET ended_at = NULL WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreCreateSessionFailed, err)
	}

	ass.logger.Info("OperatorSession started", "operator_session_id", id, "session_type", string(sessionType))
	return nil
}

// EndSession marks a session ended: its owning runtime session is over and the
// pruner may remove the row once retention has passed and no events or receipts
// reference it. Ending an unknown session fails with ErrEventsStoreSessionUnknown.
func (ass *LocalFirstEventsStore) EndSession(id string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrEventsStoreSessionMissing
	}
	result, err := ass.db.ExecWithRetry(context.Background(),
		`UPDATE sessions SET ended_at = ? WHERE id = ?`, timesvc.FormatTimestamp(time.Now()), id)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEndSessionFailed, err)
	}
	ended, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEndSessionFailed, err)
	}
	if ended == 0 {
		return fmt.Errorf("%w: %s", constants.ErrEventsStoreSessionUnknown, id)
	}
	return nil
}

// GetOperatorSession retrieves a session by ID
func (ass *LocalFirstEventsStore) GetOperatorSession(id string) (*OperatorSession, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	query := `SELECT id, session_type, title, created_at, user_identity FROM sessions WHERE id = ?`
	row := ass.db.QueryRowWithRetry(context.Background(), query, id)

	var session OperatorSession
	var sessionType sql.NullString
	var titleBytes, identityBytes []byte
	var createdAtStr string
	err := row.Scan(&session.ID, &sessionType, &titleBytes, &createdAtStr, &identityBytes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreGetSessionFailed, err)
	}

	session.CreatedAt, _ = timesvc.ParseTimestamp(createdAtStr)

	if sessionType.Valid {
		session.SessionType = sessionType.String
	}
	if session.Title, err = ass.decryptColumn("sessions.title", session.ID, titleBytes); err != nil {
		return nil, err
	}
	if session.UserIdentity, err = ass.decryptColumn("sessions.user_identity", session.ID, identityBytes); err != nil {
		return nil, err
	}

	return &session, nil
}

func (ass *LocalFirstEventsStore) requireExistingSessionConn(conn *sql.Conn, event *Event) error {
	if event == nil {
		return constants.ErrEventsStoreEventNil
	}
	if event.Type == constants.EventPlatformAuditChainCheckpointed {
		return nil
	}
	if event.OperatorSessionID == "" || strings.TrimSpace(event.OperatorSessionID) != event.OperatorSessionID {
		return constants.ErrEventsStoreSessionMissing
	}

	var exists int
	err := conn.QueryRowContext(context.Background(), `SELECT 1 FROM sessions WHERE id = ?`, event.OperatorSessionID).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: %s", constants.ErrEventsStoreSessionUnknown, event.OperatorSessionID)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreVerifySessionFailed, err)
	}
	return nil
}

// prepareEventInsert compresses command output and fails closed on a nil or
// locked vault before any write lock is taken. Encryption happens in
// AppendPreparedEvent, where the chain sequence that binds each ciphertext to
// its row is assigned.
func (ass *LocalFirstEventsStore) prepareEventInsert(event *Event) (PreparedEventInsert, error) {
	if event == nil {
		return PreparedEventInsert{}, constants.ErrEventsStoreEventNil
	}
	if ass.encryptionVault == nil || !ass.encryptionVault.IsUnlocked() {
		return PreparedEventInsert{}, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEncryptContentFailed, constants.ErrLocalFirstEventsStoreVaultLocked)
	}

	stdout, err := compressEventOutput(eventColumnStdout, event.CommandStdout)
	if err != nil {
		return PreparedEventInsert{}, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEncryptStdoutFailed, err)
	}
	stderr, err := compressEventOutput(eventColumnStderr, event.CommandStderr)
	if err != nil {
		return PreparedEventInsert{}, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEncryptStderrFailed, err)
	}

	return PreparedEventInsert{
		Event:        event,
		TimestampStr: timesvc.FormatTimestamp(event.Timestamp),
		Vault:        ass.encryptionVault,
		Stdout:       stdout,
		Stderr:       stderr,
	}, nil
}

// RecordEvents records multiple events in a single database transaction.
func (ass *LocalFirstEventsStore) RecordEvents(events []*Event) error {
	if ass == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	if len(events) == 0 {
		return nil
	}
	if ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDBNotInitialized
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	return ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		for _, event := range events {
			if err := ass.requireExistingSessionConn(conn, event); err != nil {
				return err
			}
			prepared, err := ass.prepareEventInsert(event)
			if err != nil {
				return err
			}
			if _, _, _, err := AppendPreparedEvent(context.Background(), conn, prepared); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreExecuteBatchFailed, err)
			}
		}

		ass.logger.Info("Batch of events recorded", "count", len(events))
		return nil
	})
}

// RecordEvent records an event in the events log
// Content fields are encrypted if an encryption vault is configured and unlocked
func (ass *LocalFirstEventsStore) RecordEvent(event *Event) (int64, error) {
	id, _, err := ass.recordEvent(event, false)
	return id, err
}

// RecordEventOnce records an event at most once per (transaction_id, type). It is the
// idempotency key for facts that must appear exactly once per transaction, such as the
// transaction.expired event: when an event of the same type already exists for the
// event's TransactionID nothing is written and recorded is false. The existence check
// and the append run in one immediate transaction, so concurrent callers cannot both
// record. The event must carry a TransactionID.
func (ass *LocalFirstEventsStore) RecordEventOnce(event *Event) (eventID int64, recorded bool, err error) {
	if event == nil || event.TransactionID == "" {
		return 0, false, fmt.Errorf("events store: record event once: transaction_id: %w", constants.ErrMissingRequiredField)
	}
	return ass.recordEvent(event, true)
}

// Event lookups by transaction run on every governed append and every acting-app
// event receipt read; each must stay on an index (TestEventQueriesUseIndexes).
const (
	eventOnceQuery      = "SELECT 1 FROM events WHERE transaction_id = ? AND type = ? LIMIT 1"
	eventChainMetaQuery = "SELECT seq, hash FROM events WHERE transaction_id = ? ORDER BY id DESC LIMIT 1"
	// eventActingAppPredicate is driven from the current event receipt, so the
	// acting-app index selects the transaction ids and the events lookup uses
	// idx_events_transaction_id_type. It must not scan events.
	eventActingAppPredicate       = "transaction_id IN (SELECT transaction_id FROM events WHERE event_receipt_acting_app_id = ? AND event_receipt_status IS NOT NULL)"
	eventTransactionTypePredicate = "transaction_id = ? AND type = ?"
	// eventSelectColumns is the column list materializeEvents scans.
	eventSelectColumns = `
	SELECT id, seq, operator_session_id, timestamp, type, content_text,
		command_raw, command_exit_code, command_stdout, command_stderr,
		execution_duration_ms, stored_locally,
		COALESCE(transaction_id, ''), COALESCE(execution_id, '')
	FROM events
	`
)

// recordEvent appends one event under SQLite's write lock. When once is true the append
// is skipped if an event with the same transaction_id and type already exists.
func (ass *LocalFirstEventsStore) recordEvent(event *Event, once bool) (int64, bool, error) {
	if ass == nil {
		return 0, false, constants.ErrLocalFirstEventsStoreDisabled
	}
	if ass.db == nil {
		return 0, false, constants.ErrLocalFirstEventsStoreDBNotInitialized
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	var eventID int64
	recorded := false
	err := ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		eventID, recorded = 0, false
		if once {
			var exists int
			err := conn.QueryRowContext(context.Background(), eventOnceQuery,
				event.TransactionID, string(event.Type)).Scan(&exists)
			if err == nil {
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryEventsFailed, err)
			}
		}

		if err := ass.requireExistingSessionConn(conn, event); err != nil {
			return err
		}

		prepared, err := ass.prepareEventInsert(event)
		if err != nil {
			return err
		}

		id, _, _, err := AppendPreparedEvent(context.Background(), conn, prepared)
		if err != nil {
			return err
		}
		eventID = id
		recorded = true

		ass.logger.Info("Event recorded",
			"event_id", eventID,
			"type", event.Type,
			"operator_session_id", event.OperatorSessionID,
			"exit_code", event.CommandExitCode)

		return nil
	})

	return eventID, recorded && err == nil, err
}

// RecordEventChained records an audit event and returns the chain metadata for acknowledgement.
func (ass *LocalFirstEventsStore) RecordEventChained(event *Event) (int64, int64, string, error) {
	if ass == nil {
		return 0, 0, "", constants.ErrLocalFirstEventsStoreDisabled
	}
	if ass.db == nil {
		return 0, 0, "", constants.ErrLocalFirstEventsStoreDBNotInitialized
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	var eventID, seq int64
	var hash string
	err := ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		if err := ass.requireExistingSessionConn(conn, event); err != nil {
			return err
		}

		prepared, err := ass.prepareEventInsert(event)
		if err != nil {
			return err
		}

		id, chainSeq, chainHash, err := AppendPreparedEvent(context.Background(), conn, prepared)
		if err != nil {
			return err
		}
		eventID = id
		seq = chainSeq
		hash = chainHash

		ass.logger.Info("Event recorded",
			"event_id", eventID,
			"type", event.Type,
			"operator_session_id", event.OperatorSessionID,
			"seq", seq,
			"exit_code", event.CommandExitCode)

		return nil
	})

	return eventID, seq, hash, err
}

// GetEventChainMetaByTransactionID returns chain metadata for an idempotent audit append.
func (ass *LocalFirstEventsStore) GetEventChainMetaByTransactionID(transactionID string) (int64, string, bool, error) {
	if ass == nil || ass.db == nil {
		return 0, "", false, constants.ErrLocalFirstEventsStoreDBNotInitialized
	}
	if transactionID == "" {
		return 0, "", false, nil
	}

	var seq int64
	var hash string
	err := ass.db.QueryRowContext(context.Background(), eventChainMetaQuery, transactionID).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryEventsFailed, err)
	}
	return seq, hash, true, nil
}

// GetAuditChainHead returns the latest chained audit event sequence and hash.
func (ass *LocalFirstEventsStore) GetAuditChainHead(ctx context.Context) (int64, string, error) {
	if ass == nil || ass.db == nil {
		return 0, "", constants.ErrLocalFirstEventsStoreDisabled
	}

	var headSeq int64
	var headHash string
	err := ass.db.QueryRowWithRetry(ctx, `
		SELECT seq, hash FROM events WHERE seq IS NOT NULL ORDER BY seq DESC LIMIT 1
	`).Scan(&headSeq, &headHash)
	if err == sql.ErrNoRows {
		return 0, eventsChainGenesisPrevHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("events store: chain head: %w", err)
	}
	return headSeq, headHash, nil
}

// DocSet implements governance.TransactionEventsStore for outbound mode. The
// outbound-mode L5Actuator persists signed event receipts via this method; the
// data payload is a JSON-encoded models.EventReceiptRecord that is decoded and
// recorded on the matching chain event via RecordEventReceipt. The collection
// and id parameters are ignored — the event receipt is keyed by transaction_id
// embedded in the record.
func (ass *LocalFirstEventsStore) DocSet(_ context.Context, collection, id string, data json.RawMessage) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	var receipt models.EventReceiptRecord
	if err := json.Unmarshal(data, &receipt); err != nil {
		return fmt.Errorf("%w: LocalFirstEventsStore.DocSet: failed to decode event receipt record: %w", constants.ErrInvalidJSONBody, err)
	}
	return ass.RecordEventReceipt(&receipt)
}

// DocDelete implements governance.TransactionEventsStore for outbound mode.
// The outbound operator does not persist governed documents — document
// mutations are a gateway-side concern. A usable store treats this as a no-op;
// a nil store fails closed with ErrLocalFirstEventsStoreDisabled like every other write path.
func (ass *LocalFirstEventsStore) DocDelete(_ context.Context, collection, id string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}
	return nil
}

// decryptEventsContent decrypts the four encrypted content columns of one events row
// into event. Fail-closed: a locked vault or a decrypt failure returns an error and
// never returns ciphertext or silently empty content.
func (ass *LocalFirstEventsStore) decryptEventsContent(event *Event, contentText, commandRaw, stdout, stderr []byte) error {
	return decryptEventsContentWith(ass.encryptionVault, event, contentText, commandRaw, stdout, stderr)
}

// decryptEventsContentWith decrypts the four encrypted content columns of the events row
// event.Seq identifies. The chain sequence is bound into each ciphertext, so a blob from
// another row or column fails authentication.
func decryptEventsContentWith(v *vault.Vault, event *Event, contentText, commandRaw, stdout, stderr []byte) error {
	rowID := eventRowID(event.Seq)
	var err error
	if event.ContentText, err = decryptEventsContent(v, eventColumnContentText, rowID, contentText); err != nil {
		return fmt.Errorf("events store: decrypt events.content_text for event %d: %w", event.ID, err)
	}
	if event.CommandRaw, err = decryptEventsContent(v, eventColumnCommandRaw, rowID, commandRaw); err != nil {
		return fmt.Errorf("events store: decrypt events.command_raw for event %d: %w", event.ID, err)
	}
	if event.CommandStdout, err = decryptEventOutput(v, eventColumnStdout, rowID, stdout); err != nil {
		return fmt.Errorf("events store: decrypt events.command_stdout for event %d: %w", event.ID, err)
	}
	if event.CommandStderr, err = decryptEventOutput(v, eventColumnStderr, rowID, stderr); err != nil {
		return fmt.Errorf("events store: decrypt events.command_stderr for event %d: %w", event.ID, err)
	}
	return nil
}

// GetEvents retrieves newest events for the explicit audit scope with pagination.
// An empty scope includes records without an Operator session. App identity is
// resolved through the transaction's event receipt, never inferred from a session.
// Content columns are always encrypted; they are decrypted here and a locked vault fails the read.
func (ass *LocalFirstEventsStore) GetEvents(scope models.EventsScope, limit, offset int) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	if limit <= 0 {
		limit = 50
	}

	query := eventSelectColumns
	args := []interface{}{}
	predicates := []string{}
	if scope.OperatorSessionID != "" {
		predicates = append(predicates, "operator_session_id = ?")
		args = append(args, scope.OperatorSessionID)
	}
	if scope.ActingAppID != "" {
		predicates = append(predicates, eventActingAppPredicate)
		args = append(args, scope.ActingAppID)
	}
	if len(predicates) > 0 {
		query += " WHERE " + strings.Join(predicates, " AND ")
	}
	query += " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)
	return ass.materializeEvents(query, args)
}

// GetTransactionEvents returns the events of eventType recorded under
// transactionID, oldest first, decrypted.
func (ass *LocalFirstEventsStore) GetTransactionEvents(transactionID string, eventType constants.EventType) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}
	if transactionID == "" {
		return nil, fmt.Errorf("events store: transaction events: transaction_id: %w", constants.ErrMissingRequiredField)
	}
	return ass.materializeEvents(eventSelectColumns+" WHERE "+eventTransactionTypePredicate+" ORDER BY id ASC", []interface{}{transactionID, string(eventType)})
}

// materializeEvents runs an events query selecting eventSelectColumns and
// decrypts each row.
func (ass *LocalFirstEventsStore) materializeEvents(query string, args []interface{}) ([]*Event, error) {
	type eventRow struct {
		event              Event
		sessionID          sql.NullString
		timestampStr       string
		contentTextBytes   []byte
		commandStdoutBytes []byte
		commandStderrBytes []byte
		commandRawBytes    []byte
		commandExitCode    sql.NullInt64
		storedLocally      sql.NullBool
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, args, func(r *sql.Rows) (eventRow, error) {
		var row eventRow
		err := r.Scan(
			&row.event.ID,
			&row.event.Seq,
			&row.sessionID,
			&row.timestampStr,
			&row.event.Type,
			&row.contentTextBytes,
			&row.commandRawBytes,
			&row.commandExitCode,
			&row.commandStdoutBytes,
			&row.commandStderrBytes,
			&row.event.ExecutionDurationMs,
			&row.storedLocally,
			&row.event.TransactionID,
			&row.event.ExecutionID,
		)
		row.event.OperatorSessionID = row.sessionID.String
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryEventsFailed, err)
	}

	var events []*Event
	for _, row := range rows {
		row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)

		if err := ass.decryptEventsContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdoutBytes, row.commandStderrBytes); err != nil {
			return nil, err
		}
		if row.commandExitCode.Valid {
			row.event.CommandExitCode = int(row.commandExitCode.Int64)
		} else {
			row.event.CommandExitCode = constants.ExitCodeNone
		}
		if row.storedLocally.Valid {
			row.event.StoredLocally = row.storedLocally.Bool
		}

		events = append(events, &row.event)
	}

	return events, nil
}

// GetEventByExecutionID returns the execution event for an execution ID, decrypted.
// Content is decrypted on the owning runtime; a locked vault fails the read. A
// missing execution returns ErrEventsStoreExecutionNotFound.
func (ass *LocalFirstEventsStore) GetEventByExecutionID(executionID string) (*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}
	if executionID == "" {
		return nil, constants.ErrEventsStoreExecutionNotFound
	}

	query := `
	SELECT id, seq, operator_session_id, timestamp, type, content_text,
		command_raw, command_exit_code, command_stdout, command_stderr,
		execution_duration_ms, stored_locally, COALESCE(transaction_id, ''), execution_id
	FROM events
	WHERE execution_id = ?
	ORDER BY id DESC
	LIMIT 1
	`

	type eventRow struct {
		event            Event
		sessionID        sql.NullString
		timestampStr     string
		contentTextBytes []byte
		commandRawBytes  []byte
		commandStdout    []byte
		commandStderr    []byte
		commandExitCode  sql.NullInt64
		storedLocally    sql.NullBool
		executionID      sql.NullString
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, []interface{}{executionID}, func(r *sql.Rows) (eventRow, error) {
		var row eventRow
		err := r.Scan(
			&row.event.ID, &row.event.Seq, &row.sessionID, &row.timestampStr, &row.event.Type,
			&row.contentTextBytes, &row.commandRawBytes, &row.commandExitCode,
			&row.commandStdout, &row.commandStderr, &row.event.ExecutionDurationMs,
			&row.storedLocally, &row.event.TransactionID, &row.executionID,
		)
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryEventsFailed, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", constants.ErrEventsStoreExecutionNotFound, executionID)
	}

	row := rows[0]
	row.event.OperatorSessionID = row.sessionID.String
	row.event.ExecutionID = row.executionID.String
	row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)
	if err := ass.decryptEventsContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdout, row.commandStderr); err != nil {
		return nil, err
	}
	if row.commandExitCode.Valid {
		row.event.CommandExitCode = int(row.commandExitCode.Int64)
	} else {
		row.event.CommandExitCode = constants.ExitCodeNone
	}
	if row.storedLocally.Valid {
		row.event.StoredLocally = row.storedLocally.Bool
	}
	return &row.event, nil
}

// RecordFileMutation records a file mutation in the events log
func (ass *LocalFirstEventsStore) RecordFileMutation(mutation *FileMutationLog) error {
	if ass == nil || ass.db == nil {
		return constants.ErrLocalFirstEventsStoreDisabled
	}

	uid, err := newFileMutationUID()
	if err != nil {
		return err
	}
	rowID := fileMutationRowID(mutation.EventID, uid)
	filepathBytes, err := ass.encryptColumn("file_mutation_log.filepath", rowID, mutation.Filepath)
	if err != nil {
		return err
	}
	diffStatBytes, err := ass.encryptColumn("file_mutation_log.diff_stat", rowID, mutation.DiffStat)
	if err != nil {
		return err
	}

	query := `
	INSERT INTO file_mutation_log (
		mutation_uid, event_id, filepath, operation, ledger_hash_before, ledger_hash_after, diff_stat
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	_, err = ass.db.ExecWithRetry(context.Background(), query,
		uid,
		mutation.EventID,
		filepathBytes,
		string(mutation.Operation),
		mutation.LedgerHashBefore,
		mutation.LedgerHashAfter,
		diffStatBytes,
	)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreRecordFileMutationFailed, err)
	}

	ass.logger.Info("File mutation recorded",
		"event_id", mutation.EventID,
		"operation", mutation.Operation)

	return nil
}

// GetFileMutations retrieves file mutations for an event
func (ass *LocalFirstEventsStore) GetFileMutations(eventID int64) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	query := `
	SELECT id, mutation_uid, event_id, filepath, operation, ledger_hash_before, ledger_hash_after, diff_stat
	FROM file_mutation_log
	WHERE event_id = ?
	`

	type mutationRow struct {
		mutation      FileMutationLog
		hashBefore    sql.NullString
		hashAfter     sql.NullString
		filepathBytes []byte
		diffStatBytes []byte
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, []interface{}{eventID}, func(r *sql.Rows) (mutationRow, error) {
		var row mutationRow
		err := r.Scan(
			&row.mutation.ID,
			&row.mutation.UID,
			&row.mutation.EventID,
			&row.filepathBytes,
			&row.mutation.Operation,
			&row.hashBefore,
			&row.hashAfter,
			&row.diffStatBytes,
		)
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
	}

	var mutations []*FileMutationLog
	for _, row := range rows {
		if row.hashBefore.Valid {
			row.mutation.LedgerHashBefore = row.hashBefore.String
		}
		if row.hashAfter.Valid {
			row.mutation.LedgerHashAfter = row.hashAfter.String
		}
		if row.mutation.Filepath, err = ass.decryptColumn("file_mutation_log.filepath", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.filepathBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
		}
		if row.mutation.DiffStat, err = ass.decryptColumn("file_mutation_log.diff_stat", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.diffStatBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
		}

		mutations = append(mutations, &row.mutation)
	}

	return mutations, nil
}

// ListSessions retrieves all sessions with pagination, ordered by created_at ASC.
func (ass *LocalFirstEventsStore) ListSessions(limit, offset int) ([]*OperatorSession, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	if limit <= 0 {
		limit = 100
	}

	query := `
	SELECT id, title, session_type, created_at, user_identity
	FROM sessions
	ORDER BY created_at ASC
	LIMIT ? OFFSET ?
	`

	type sessionRow struct {
		session       OperatorSession
		sessionType   sql.NullString
		titleBytes    []byte
		identityBytes []byte
		createdAtStr  string
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, []interface{}{limit, offset}, func(r *sql.Rows) (sessionRow, error) {
		var row sessionRow
		err := r.Scan(&row.session.ID, &row.titleBytes, &row.sessionType, &row.createdAtStr, &row.identityBytes)
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreGetSessionFailed, err)
	}

	var sessions []*OperatorSession
	for _, row := range rows {
		row.session.CreatedAt, _ = timesvc.ParseTimestamp(row.createdAtStr)
		if row.sessionType.Valid {
			row.session.SessionType = row.sessionType.String
		}
		if row.session.Title, err = ass.decryptColumn("sessions.title", row.session.ID, row.titleBytes); err != nil {
			return nil, err
		}
		if row.session.UserIdentity, err = ass.decryptColumn("sessions.user_identity", row.session.ID, row.identityBytes); err != nil {
			return nil, err
		}
		sessions = append(sessions, &row.session)
	}

	return sessions, nil
}

// ListEvents retrieves events with optional session filter and pagination, ordered by timestamp ASC.
// When sessionID is empty, all events across all sessions are returned.
func (ass *LocalFirstEventsStore) ListEvents(sessionID string, limit, offset int) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	if limit <= 0 {
		limit = 100
	}

	var query strings.Builder
	query.WriteString(`
	SELECT id, seq, operator_session_id, timestamp, type, content_text,
		command_raw, command_exit_code, command_stdout, command_stderr,
		execution_duration_ms, stored_locally, COALESCE(execution_id, '')
	FROM events
	`)

	args := []interface{}{}
	if sessionID != "" {
		query.WriteString(" WHERE operator_session_id = ?")
		args = append(args, sessionID)
	}
	query.WriteString(" ORDER BY timestamp ASC LIMIT ? OFFSET ?")
	args = append(args, limit, offset)

	type eventRow struct {
		event              Event
		sessionID          sql.NullString
		timestampStr       string
		contentTextBytes   []byte
		commandStdoutBytes []byte
		commandStderrBytes []byte
		commandRawBytes    []byte
		commandExitCode    sql.NullInt64
		storedLocally      sql.NullBool
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query.String(), args, func(r *sql.Rows) (eventRow, error) {
		var row eventRow
		err := r.Scan(
			&row.event.ID, &row.event.Seq, &row.sessionID, &row.timestampStr, &row.event.Type,
			&row.contentTextBytes, &row.commandRawBytes, &row.commandExitCode,
			&row.commandStdoutBytes, &row.commandStderrBytes, &row.event.ExecutionDurationMs,
			&row.storedLocally, &row.event.ExecutionID,
		)
		row.event.OperatorSessionID = row.sessionID.String
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryEventsFailed, err)
	}

	var events []*Event
	for _, row := range rows {
		row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)

		if err := ass.decryptEventsContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdoutBytes, row.commandStderrBytes); err != nil {
			return nil, err
		}
		if row.commandExitCode.Valid {
			row.event.CommandExitCode = int(row.commandExitCode.Int64)
		} else {
			row.event.CommandExitCode = constants.ExitCodeNone
		}
		if row.storedLocally.Valid {
			row.event.StoredLocally = row.storedLocally.Bool
		}

		events = append(events, &row.event)
	}

	return events, nil
}

// GetFileMutationByID returns one file mutation row with its owning session, decrypted.
// A missing row returns ErrEventsStoreFileMutationNotFound.
func (ass *LocalFirstEventsStore) GetFileMutationByID(id int64) (*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	rows, err := ass.queryFileMutations(`
	SELECT fm.id, fm.mutation_uid, fm.event_id, fm.filepath, fm.operation, fm.ledger_hash_before, fm.ledger_hash_after, fm.diff_stat,
		COALESCE(e.operator_session_id, ''), e.timestamp
	FROM file_mutation_log fm
	JOIN events e ON e.id = fm.event_id
	WHERE fm.id = ?
	`, []interface{}{id})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %d", constants.ErrEventsStoreFileMutationNotFound, id)
	}
	return rows[0], nil
}

// ListFileMutationsBySession returns the newest file mutations of one operator session,
// decrypted, capped at limit.
func (ass *LocalFirstEventsStore) ListFileMutationsBySession(operatorSessionID string, limit int) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}
	if limit <= 0 {
		limit = 50
	}
	return ass.queryFileMutations(`
	SELECT fm.id, fm.mutation_uid, fm.event_id, fm.filepath, fm.operation, fm.ledger_hash_before, fm.ledger_hash_after, fm.diff_stat,
		COALESCE(e.operator_session_id, ''), e.timestamp
	FROM file_mutation_log fm
	JOIN events e ON e.id = fm.event_id
	WHERE e.operator_session_id = ?
	ORDER BY fm.id DESC
	LIMIT ?
	`, []interface{}{operatorSessionID, limit})
}

// queryFileMutations runs a file_mutation_log query whose columns are, in order: id, event_id,
// filepath, operation, ledger_hash_before, ledger_hash_after, diff_stat, operator_session_id,
// and the owning event's timestamp. Content columns are decrypted; a locked vault fails the read.
func (ass *LocalFirstEventsStore) queryFileMutations(query string, args []interface{}) ([]*FileMutationLog, error) {
	type mutationRow struct {
		mutation      FileMutationLog
		hashBefore    sql.NullString
		hashAfter     sql.NullString
		filepathBytes []byte
		diffStatBytes []byte
		timestampStr  string
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, args, func(r *sql.Rows) (mutationRow, error) {
		var row mutationRow
		err := r.Scan(
			&row.mutation.ID, &row.mutation.UID, &row.mutation.EventID, &row.filepathBytes, &row.mutation.Operation,
			&row.hashBefore, &row.hashAfter, &row.diffStatBytes, &row.mutation.OperatorSessionID,
			&row.timestampStr,
		)
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
	}

	mutations := make([]*FileMutationLog, 0, len(rows))
	for _, row := range rows {
		mutation := row.mutation
		mutation.LedgerHashBefore = row.hashBefore.String
		mutation.LedgerHashAfter = row.hashAfter.String
		mutation.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)
		if mutation.Filepath, err = ass.decryptColumn("file_mutation_log.filepath", fileMutationRowID(mutation.EventID, mutation.UID), row.filepathBytes); err != nil {
			return nil, err
		}
		if mutation.DiffStat, err = ass.decryptColumn("file_mutation_log.diff_stat", fileMutationRowID(mutation.EventID, mutation.UID), row.diffStatBytes); err != nil {
			return nil, err
		}
		mutations = append(mutations, &mutation)
	}
	return mutations, nil
}

// ListFileMutations retrieves file mutations with pagination, ordered by id ASC.
func (ass *LocalFirstEventsStore) ListFileMutations(limit, offset int) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrLocalFirstEventsStoreDisabled
	}

	if limit <= 0 {
		limit = 100
	}

	query := `
	SELECT id, mutation_uid, event_id, filepath, operation, ledger_hash_before, ledger_hash_after, diff_stat
	FROM file_mutation_log
	ORDER BY id ASC
	LIMIT ? OFFSET ?
	`

	type mutationRow struct {
		mutation      FileMutationLog
		hashBefore    sql.NullString
		hashAfter     sql.NullString
		filepathBytes []byte
		diffStatBytes []byte
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, []interface{}{limit, offset}, func(r *sql.Rows) (mutationRow, error) {
		var row mutationRow
		err := r.Scan(
			&row.mutation.ID, &row.mutation.UID, &row.mutation.EventID, &row.filepathBytes, &row.mutation.Operation,
			&row.hashBefore, &row.hashAfter, &row.diffStatBytes,
		)
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
	}

	var mutations []*FileMutationLog
	for _, row := range rows {
		if row.hashBefore.Valid {
			row.mutation.LedgerHashBefore = row.hashBefore.String
		}
		if row.hashAfter.Valid {
			row.mutation.LedgerHashAfter = row.hashAfter.String
		}
		if row.mutation.Filepath, err = ass.decryptColumn("file_mutation_log.filepath", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.filepathBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
		}
		if row.mutation.DiffStat, err = ass.decryptColumn("file_mutation_log.diff_stat", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.diffStatBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreQueryFileMutationsFailed, err)
		}
		mutations = append(mutations, &row.mutation)
	}

	return mutations, nil
}

// eventsStorePrune returns a PruneFunc that handles retention pruning
// for events (including event receipts), ended sessions, and file mutations.
func eventsStorePrune(config *LocalFirstEventsStoreConfig) sqliteutil.PruneFunc {
	return func(ctx context.Context, db *sqliteutil.DB, logger *slog.Logger) error {
		cutoff := timesvc.FormatTimestamp(time.Now().AddDate(0, 0, -config.RetentionDays))

		// 1. Delete file mutations for old events first (satisfy FK constraints)
		_, err := db.ExecWithRetry(ctx, `
			DELETE FROM file_mutation_log
			WHERE event_id IN (SELECT id FROM events WHERE timestamp < ?)
		`, cutoff)
		if err != nil {
			logger.Error("Failed to prune old file mutations", string(constants.ConnectionStateError), err)
			return err
		}

		// 2. Checkpoint and delete chained events older than retention period.
		// Event receipts live on those rows, so they leave with the chain.
		if err := PruneChainedEvents(ctx, db, logger, config.EncryptionVault, cutoff); err != nil {
			logger.Error("Failed to prune chained audit events", string(constants.ConnectionStateError), err)
			return err
		}

		// 3. Delete ended sessions past retention that no longer have any events.
		// A session with NULL ended_at is still owned by a live runtime
		// session (idle sessions write no events) and is never pruned.
		_, err = db.ExecWithRetry(ctx, `
			DELETE FROM sessions
			WHERE ended_at IS NOT NULL AND ended_at < ?
			AND id NOT IN (SELECT DISTINCT operator_session_id FROM events WHERE operator_session_id IS NOT NULL)
		`, cutoff)
		if err != nil {
			logger.Warn("Failed to prune orphaned sessions", string(constants.ConnectionStateError), err)
		}

		if err := db.RunIncrementalVacuum(1000); err != nil {
			logger.Info("Failed to run incremental vacuum", string(constants.ConnectionStateError), err)
		}
		return nil
	}
}

// GetEncryptionVault returns the optional encryption vault used by this service.
func (ass *LocalFirstEventsStore) GetEncryptionVault() *vault.Vault {
	if ass == nil {
		return nil
	}
	return ass.encryptionVault
}

// Wait blocks until all in-flight writes have finished.
func (ass *LocalFirstEventsStore) Wait() {
	if ass == nil {
		return
	}
	ass.muWrites.Wait()
}

// Close drains in-flight writes and stops the pruner. The database belongs to
// the caller and stays open. Idempotent.
func (ass *LocalFirstEventsStore) Close() error {
	if ass == nil {
		return nil
	}

	ass.Wait()

	ass.closeOnce.Do(func() {
		if ass.pruner != nil {
			ass.pruner.Stop()
		}
	})

	return nil
}

// GetDataDir returns the events store data directory
func (ass *LocalFirstEventsStore) GetDataDir() string {
	if ass == nil || ass.fileSvc == nil {
		return ""
	}
	return ass.fileSvc.Resolve(constants.DataDirname)
}

// Content column identities bound into the AEAD associated data. A ciphertext only
// decrypts under the column and row it was written for.
const (
	eventColumnContentText    = "events.content_text"
	eventColumnCommandRaw     = "events.command_raw"
	eventColumnStdout         = "events.command_stdout"
	eventColumnStderr         = "events.command_stderr"
	eventColumnReceiptTarget  = "events.event_receipt_target_resource"
	eventColumnReceiptSummary = "events.event_receipt_result_summary"

	eventsContentAADVersion = "ztd-content-aad-v1"
)

// EventsContentAAD returns the associated data that binds a ciphertext to its
// "table.column" and row identity. The row identity is known before the insert:
// the chain sequence for events (including event receipt columns), the session id,
// the transaction id for suspended transactions, the commitment hash, or the event id and mutation uid
// for file mutations. It is never the autoincrement rowid.
func EventsContentAAD(column, rowID string) []byte {
	return []byte(eventsContentAADVersion + "|" + column + "|" + rowID)
}

// eventRowID is the events row identity bound into the AEAD data.
func eventRowID(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// EventContentRowID is the events row identity bound into content ciphertext.
func EventContentRowID(seq int64) string {
	return eventRowID(seq)
}

// fileMutationRowID is the file_mutation_log row identity bound into the AEAD data.
func fileMutationRowID(eventID int64, uid string) string {
	return strconv.FormatInt(eventID, 10) + "|" + uid
}

// newFileMutationUID returns a random identity for one file_mutation_log row.
func newFileMutationUID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("events store: file mutation uid: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// encryptEventsContent encrypts content with the runtime vault, binding the "table.column"
// and row identity as AEAD associated data. It is the single content-encryption path for
// every store that writes encrypted columns (the events store, the commitment ledger, and
// the suspended transaction store). A nil or locked vault returns
// ErrLocalFirstEventsStoreVaultLocked; there is no plaintext branch.
func encryptEventsContent(v *vault.Vault, column, rowID, content string) ([]byte, error) {
	if v == nil || !v.IsUnlocked() {
		return nil, constants.ErrLocalFirstEventsStoreVaultLocked
	}
	if content == "" {
		return nil, nil
	}
	encrypted, err := v.EncryptWithAAD([]byte(content), EventsContentAAD(column, rowID))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreEncryptFailed, err)
	}
	return encrypted, nil
}

// decryptEventsContent decrypts content written by encryptEventsContent for the same
// column and row identity. Empty input is the empty string and needs no vault. A nil or
// locked vault returns ErrLocalFirstEventsStoreVaultLocked; a ciphertext moved to another row or
// column fails authentication.
func decryptEventsContent(v *vault.Vault, column, rowID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if v == nil || !v.IsUnlocked() {
		return "", constants.ErrLocalFirstEventsStoreVaultLocked
	}
	decrypted, err := v.DecryptWithAAD(data, EventsContentAAD(column, rowID))
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrLocalFirstEventsStoreDecryptFailed, err)
	}
	return string(decrypted), nil
}

// encryptColumn encrypts one named content column value for one row. Fail-closed: a
// locked vault returns ErrLocalFirstEventsStoreVaultLocked and the write does not happen.
func (ass *LocalFirstEventsStore) encryptColumn(column, rowID, value string) ([]byte, error) {
	data, err := encryptEventsContent(ass.encryptionVault, column, rowID, value)
	if err != nil {
		return nil, fmt.Errorf("events store: encrypt %s: %w", column, err)
	}
	return data, nil
}

// decryptColumn decrypts one named content column value of one row. Fail-closed: a locked
// vault or a decrypt failure returns an error, never ciphertext.
func (ass *LocalFirstEventsStore) decryptColumn(column, rowID string, data []byte) (string, error) {
	value, err := decryptEventsContent(ass.encryptionVault, column, rowID, data)
	if err != nil {
		return "", fmt.Errorf("events store: decrypt %s: %w", column, err)
	}
	return value, nil
}

// compressEventOutput gzips command output before encryption. Nothing truncates: the
// local record is the raw output. Empty output is nil (stored as NULL).
func compressEventOutput(column, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	compressed, err := sqliteutil.Compress([]byte(value))
	if err != nil {
		return nil, fmt.Errorf("events store: compress %s: %w", column, err)
	}
	return compressed, nil
}

// encryptEventOutput encrypts gzip-compressed command output bound to its row. Empty
// output is NULL.
func encryptEventOutput(v *vault.Vault, column, rowID string, compressed []byte) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, nil
	}
	data, err := encryptEventsContent(v, column, rowID, string(compressed))
	if err != nil {
		return nil, fmt.Errorf("events store: encrypt %s: %w", column, err)
	}
	return data, nil
}

func decryptEventOutput(v *vault.Vault, column, rowID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	compressed, err := decryptEventsContent(v, column, rowID, data)
	if err != nil {
		return "", fmt.Errorf("events store: decrypt %s: %w", column, err)
	}
	raw, err := sqliteutil.Decompress([]byte(compressed))
	if err != nil {
		return "", fmt.Errorf("events store: decompress %s: %w", column, err)
	}
	return string(raw), nil
}

// encryptSessionFields encrypts the title and user_identity content columns of the session row id.
func (ass *LocalFirstEventsStore) encryptSessionFields(id, title, userIdentity string) (titleBytes, identityBytes []byte, err error) {
	if titleBytes, err = ass.encryptColumn("sessions.title", id, title); err != nil {
		return nil, nil, err
	}
	if identityBytes, err = ass.encryptColumn("sessions.user_identity", id, userIdentity); err != nil {
		return nil, nil, err
	}
	return titleBytes, identityBytes, nil
}

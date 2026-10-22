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
	"slices"
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
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// AuditStoreConfig holds configuration for the SQL audit store
type AuditStoreConfig struct {
	RetentionDays        int
	PruneIntervalMinutes int
	// EncryptionVault is the required vault.Vault for encrypting sensitive content fields.
	// content_text, command_raw, command_stdout, and command_stderr are encrypted at rest.
	EncryptionVault *vault.Vault
}

// DefaultAuditStoreConfig returns the default configuration for the audit store.
func DefaultAuditStoreConfig() *AuditStoreConfig {
	return &AuditStoreConfig{
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

// OperatorSession represents a chat session in the audit log
type OperatorSession struct {
	ID           string
	SessionType  string
	Title        string
	CreatedAt    time.Time
	UserIdentity string
}

// Event represents an event in the audit log (append-only)
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

// SQLAuditStore provides pure SQL audit data storage
type SQLAuditStore struct {
	db               *sqliteutil.DB
	config           *AuditStoreConfig
	logger           *slog.Logger
	fileSvc          fs.RuntimeFileService
	encryptionVault  *vault.Vault
	pruner           *sqliteutil.Pruner
	commitmentLedger *CommitmentLedger
	closeOnce        sync.Once

	muWrites sync.WaitGroup
}

// NewSQLAuditStore creates a SQL audit store over the runtime's canonical
// database. The caller owns db: the store shares its pool and writer gate with
// every other writer of ztd.db and never closes it.
func NewSQLAuditStore(config *AuditStoreConfig, db *sqliteutil.DB, logger *slog.Logger, fileSvc fs.RuntimeFileService) (*SQLAuditStore, error) {
	if config == nil {
		config = DefaultAuditStoreConfig()
	}
	if db == nil {
		return nil, fmt.Errorf("audit store: database: %w", constants.ErrMissingRequiredField)
	}

	if config.EncryptionVault == nil {
		return nil, constants.ErrAuditStoreEncryptionVaultRequired
	}
	if fileSvc == nil {
		return nil, fmt.Errorf("audit store: file service: %w", constants.ErrMissingRequiredField)
	}
	if logger == nil {
		logger = slog.Default()
	}

	ass := &SQLAuditStore{
		config:          config,
		logger:          logger,
		fileSvc:         fileSvc,
		encryptionVault: config.EncryptionVault,
		db:              db,
	}

	if err := ass.bootstrap(); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreBootstrapFailed, err)
	}

	interval := time.Duration(config.PruneIntervalMinutes) * time.Minute
	ass.pruner = sqliteutil.NewPruner(ass.db, logger, interval, auditStorePrune(config))
	ass.pruner.Start()

	ass.logger.Info("Audit store initialized",
		"data_dir", ass.fileSvc.Resolve(constants.DataDirname),
		"db_path", db.GetPath())

	return ass, nil
}

func (ass *SQLAuditStore) CommitmentLedger() *CommitmentLedger {
	if ass == nil {
		return nil
	}
	return ass.commitmentLedger
}

// bootstrap initializes the audit store (directory structure, database)
func (ass *SQLAuditStore) bootstrap() error {
	ass.logger.Info("Bootstrapping audit store", "data_dir", ass.fileSvc.Resolve(constants.DataDirname))

	if err := ass.createDirectoryStructure(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreCreateDirFailed, err)
	}

	if err := ass.verifyWritePermissions(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreNotWritable, err)
	}

	if err := ass.initDatabase(); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreInitDBFailed, err)
	}

	ass.logger.Info("Audit store bootstrap completed successfully")
	return nil
}

// createDirectoryStructure creates the audit store directory structure
func (ass *SQLAuditStore) createDirectoryStructure() error {
	if err := ass.fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirPrivate); err != nil {
		return fmt.Errorf("%w %s: %w", constants.ErrAuditStoreCreateDirPathFailed, constants.DataDirname, err)
	}

	ass.logger.Info("Audit store directory structure ensured",
		"data_dir", ass.fileSvc.Resolve(constants.DataDirname))

	return nil
}

// verifyWritePermissions ensures the data directory is writable
func (ass *SQLAuditStore) verifyWritePermissions() error {
	testRelPath := filepath.Join(constants.DataDirname, ".write_test")

	if err := ass.fileSvc.WriteFile(context.Background(), testRelPath, []byte("write_test"), constants.PermFilePrivate); err != nil {
		return fmt.Errorf("%w %s: %w", constants.ErrAuditStoreCannotWrite, ass.fileSvc.Resolve(constants.DataDirname), err)
	}

	if err := ass.fileSvc.Remove(context.Background(), testRelPath); err != nil {
		ass.logger.Warn("Failed to remove write test file", "path", testRelPath, string(constants.ConnectionStateError), err)
	}

	ass.logger.Info("Write permissions verified", "path", ass.fileSvc.Resolve(constants.DataDirname))
	return nil
}

// initDatabase applies the audit schema to the shared database. The schema is
// fresh-only: CREATE TABLE IF NOT EXISTS is the whole schema, with no ALTER TABLE
// migrations or column back-fills.
func (ass *SQLAuditStore) initDatabase() error {
	if _, err := ass.db.Exec(auditStoreSchema); err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreInitSchemaFailed, err)
	}

	ass.commitmentLedger = NewCommitmentLedger(ass.db, ass.logger, ass.encryptionVault)

	ass.logger.Info("Database schema initialized")
	return nil
}

// auditStoreSchema defines the initial schema for the audit store database.
const auditStoreSchema = `
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

CREATE TABLE IF NOT EXISTS receipts (
	transaction_id TEXT PRIMARY KEY,
	transaction_hash TEXT NOT NULL,
	investigation_id TEXT,
	operator_id TEXT NOT NULL,
	operator_session_id TEXT NOT NULL,
	requestor_user_id TEXT,
	acting_app_id TEXT,
	event_type TEXT,
	action_type TEXT NOT NULL,
	target_resource BLOB,
	status TEXT NOT NULL,
	result_summary BLOB,
	state_root_before TEXT,
	state_root_after TEXT,
	executed_at_ms INTEGER NOT NULL,
	signer_key_id TEXT NOT NULL,
	signature TEXT NOT NULL,
	receipt_json BLOB,
	timestamp TEXT DEFAULT (strftime('%Y-%m-%dT%H:%M:%f','now')),
	FOREIGN KEY(operator_session_id) REFERENCES sessions(id)
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
CREATE INDEX IF NOT EXISTS idx_file_mutation_event_id ON file_mutation_log(event_id);
CREATE INDEX IF NOT EXISTS idx_receipts_session_id ON receipts(operator_session_id);
CREATE INDEX IF NOT EXISTS idx_receipts_timestamp ON receipts(timestamp);
CREATE INDEX IF NOT EXISTS idx_receipts_investigation_id ON receipts(investigation_id);
CREATE INDEX IF NOT EXISTS idx_receipts_acting_app_id ON receipts(acting_app_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_id_type ON sessions(id, session_type);
CREATE INDEX IF NOT EXISTS idx_commitment_ledger_committed_at ON commitment_ledger(committed_at_unix_ms);
CREATE INDEX IF NOT EXISTS idx_audit_chain_checkpoints_seq ON audit_chain_checkpoints(pruned_through_seq);
`

// CreateSession creates a new session in the audit log
func (ass *SQLAuditStore) CreateSession(id string, sessionType constants.SessionType, title, userIdentity string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrAuditSessionMissing
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
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreCreateSessionFailed, err)
	}

	ass.logger.Info("OperatorSession created", "operator_session_id", id, "session_type", string(sessionType))
	return nil
}

// StartSession starts the audit session a runtime owns. It is the single start
// path for a runtime session: a new row is inserted, and an existing row with
// identical attributes is the idempotent restart case, which marks it live again
// (clears ended_at). An existing row with a different session type, title, or
// user identity fails with ErrAuditSessionConflict. The insert and the check run
// in one immediate transaction, so concurrent identical starts both succeed.
func (ass *SQLAuditStore) StartSession(id string, sessionType constants.SessionType, title, userIdentity string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrAuditSessionMissing
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
			return fmt.Errorf("%w: %s", constants.ErrAuditSessionConflict, id)
		}
		_, err = conn.ExecContext(ctx, `UPDATE sessions SET ended_at = NULL WHERE id = ?`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreCreateSessionFailed, err)
	}

	ass.logger.Info("OperatorSession started", "operator_session_id", id, "session_type", string(sessionType))
	return nil
}

// EndSession marks a session ended: its owning runtime session is over and the
// pruner may remove the row once retention has passed and no events or receipts
// reference it. Ending an unknown session fails with ErrAuditSessionUnknown.
func (ass *SQLAuditStore) EndSession(id string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	if id == "" || strings.TrimSpace(id) != id {
		return constants.ErrAuditSessionMissing
	}
	result, err := ass.db.ExecWithRetry(context.Background(),
		`UPDATE sessions SET ended_at = ? WHERE id = ?`, timesvc.FormatTimestamp(time.Now()), id)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreEndSessionFailed, err)
	}
	ended, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreEndSessionFailed, err)
	}
	if ended == 0 {
		return fmt.Errorf("%w: %s", constants.ErrAuditSessionUnknown, id)
	}
	return nil
}

// GetOperatorSession retrieves a session by ID
func (ass *SQLAuditStore) GetOperatorSession(id string) (*OperatorSession, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreGetSessionFailed, err)
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

func (ass *SQLAuditStore) requireExistingSessionConn(conn *sql.Conn, event *Event) error {
	if event == nil {
		return constants.ErrAuditEventNil
	}
	if event.Type == constants.EventPlatformAuditChainCheckpointed {
		return nil
	}
	if event.OperatorSessionID == "" || strings.TrimSpace(event.OperatorSessionID) != event.OperatorSessionID {
		return constants.ErrAuditSessionMissing
	}

	var exists int
	err := conn.QueryRowContext(context.Background(), `SELECT 1 FROM sessions WHERE id = ?`, event.OperatorSessionID).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("%w: %s", constants.ErrAuditSessionUnknown, event.OperatorSessionID)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreVerifySessionFailed, err)
	}
	return nil
}

// prepareAuditEventInsert compresses command output and fails closed on a nil or
// locked vault before any write lock is taken. Encryption happens in
// AppendPreparedAuditEvent, where the chain sequence that binds each ciphertext to
// its row is assigned.
func (ass *SQLAuditStore) prepareAuditEventInsert(event *Event) (PreparedAuditEventInsert, error) {
	if event == nil {
		return PreparedAuditEventInsert{}, constants.ErrAuditEventNil
	}
	if ass.encryptionVault == nil || !ass.encryptionVault.IsUnlocked() {
		return PreparedAuditEventInsert{}, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptContentFailed, constants.ErrAuditStoreVaultLocked)
	}

	stdout, err := compressAuditOutput(auditColumnEventStdout, event.CommandStdout)
	if err != nil {
		return PreparedAuditEventInsert{}, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptStdoutFailed, err)
	}
	stderr, err := compressAuditOutput(auditColumnEventStderr, event.CommandStderr)
	if err != nil {
		return PreparedAuditEventInsert{}, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptStderrFailed, err)
	}

	return PreparedAuditEventInsert{
		Event:        event,
		TimestampStr: timesvc.FormatTimestamp(event.Timestamp),
		Vault:        ass.encryptionVault,
		Stdout:       stdout,
		Stderr:       stderr,
	}, nil
}

// RecordEvents records multiple events in a single database transaction.
func (ass *SQLAuditStore) RecordEvents(events []*Event) error {
	if ass == nil {
		return constants.ErrAuditStoreDisabled
	}
	if len(events) == 0 {
		return nil
	}
	if ass.db == nil {
		return constants.ErrAuditStoreDBNotInitialized
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	return ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		for _, event := range events {
			if err := ass.requireExistingSessionConn(conn, event); err != nil {
				return err
			}
			prepared, err := ass.prepareAuditEventInsert(event)
			if err != nil {
				return err
			}
			if _, _, _, err := AppendPreparedAuditEvent(context.Background(), conn, prepared); err != nil {
				return fmt.Errorf("%w: %w", constants.ErrAuditStoreExecuteBatchFailed, err)
			}
		}

		ass.logger.Info("Batch of events recorded", "count", len(events))
		return nil
	})
}

// RecordEvent records an event in the audit log
// Content fields are encrypted if an encryption vault is configured and unlocked
func (ass *SQLAuditStore) RecordEvent(event *Event) (int64, error) {
	id, _, err := ass.recordEvent(event, false)
	return id, err
}

// RecordEventOnce records an event at most once per (transaction_id, type). It is the
// idempotency key for facts that must appear exactly once per transaction, such as the
// transaction.expired event: when an event of the same type already exists for the
// event's TransactionID nothing is written and recorded is false. The existence check
// and the append run in one immediate transaction, so concurrent callers cannot both
// record. The event must carry a TransactionID.
func (ass *SQLAuditStore) RecordEventOnce(event *Event) (eventID int64, recorded bool, err error) {
	if event == nil || event.TransactionID == "" {
		return 0, false, fmt.Errorf("audit store: record event once: transaction_id: %w", constants.ErrMissingRequiredField)
	}
	return ass.recordEvent(event, true)
}

// Event lookups by transaction run on every governed append and every acting-app
// audit read; each must stay on an index (TestAuditQueriesUseIndexes).
const (
	eventOnceQuery      = "SELECT 1 FROM events WHERE transaction_id = ? AND type = ? LIMIT 1"
	eventChainMetaQuery = "SELECT seq, hash FROM events WHERE transaction_id = ? ORDER BY id DESC LIMIT 1"
	// eventActingAppPredicate is driven from the receipts side, so it never scans events.
	eventActingAppPredicate       = "transaction_id IN (SELECT transaction_id FROM receipts WHERE acting_app_id = ?)"
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
func (ass *SQLAuditStore) recordEvent(event *Event, once bool) (int64, bool, error) {
	if ass == nil {
		return 0, false, constants.ErrAuditStoreDisabled
	}
	if ass.db == nil {
		return 0, false, constants.ErrAuditStoreDBNotInitialized
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
				return fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryEventsFailed, err)
			}
		}

		if err := ass.requireExistingSessionConn(conn, event); err != nil {
			return err
		}

		prepared, err := ass.prepareAuditEventInsert(event)
		if err != nil {
			return err
		}

		id, _, _, err := AppendPreparedAuditEvent(context.Background(), conn, prepared)
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
func (ass *SQLAuditStore) RecordEventChained(event *Event) (int64, int64, string, error) {
	if ass == nil {
		return 0, 0, "", constants.ErrAuditStoreDisabled
	}
	if ass.db == nil {
		return 0, 0, "", constants.ErrAuditStoreDBNotInitialized
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	var eventID, seq int64
	var hash string
	err := ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		if err := ass.requireExistingSessionConn(conn, event); err != nil {
			return err
		}

		prepared, err := ass.prepareAuditEventInsert(event)
		if err != nil {
			return err
		}

		id, chainSeq, chainHash, err := AppendPreparedAuditEvent(context.Background(), conn, prepared)
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
func (ass *SQLAuditStore) GetEventChainMetaByTransactionID(transactionID string) (int64, string, bool, error) {
	if ass == nil || ass.db == nil {
		return 0, "", false, constants.ErrAuditStoreDBNotInitialized
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
		return 0, "", false, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryEventsFailed, err)
	}
	return seq, hash, true, nil
}

// RecordActionReceipt upserts the latest-stage receipt projection and appends a
// chained operator.receipt.recorded fact for every stage write.
func (ass *SQLAuditStore) RecordActionReceipt(record *models.ActionReceiptRecord) error {
	return ass.recordActionReceipt(record, nil)
}

// RecordActionReceiptWithCommitment records the receipt exactly as
// RecordActionReceipt does and appends the commitment built over the ledger
// head in the same write transaction: both commit or neither does.
func (ass *SQLAuditStore) RecordActionReceiptWithCommitment(record *models.ActionReceiptRecord, build CommitmentBuilder) error {
	if build == nil {
		return constants.ErrAuditStoreRecordReceiptFailed
	}
	return ass.recordActionReceipt(record, build)
}

func (ass *SQLAuditStore) recordActionReceipt(record *models.ActionReceiptRecord, build CommitmentBuilder) error {
	if ass == nil {
		return constants.ErrAuditStoreDisabled
	}
	if ass.db == nil {
		return constants.ErrAuditStoreDBNotInitialized
	}
	if record == nil {
		return constants.ErrAuditStoreRecordReceiptFailed
	}
	if build != nil && ass.commitmentLedger == nil {
		return constants.ErrAuditStoreDBNotInitialized
	}
	if record.OperatorSessionID == "" || strings.TrimSpace(record.OperatorSessionID) != record.OperatorSessionID {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordReceiptFailed, constants.ErrAuditSessionMissing)
	}

	ass.muWrites.Add(1)
	defer ass.muWrites.Done()

	receiptJSON := []byte(nil)
	if record.ActionReceipt != nil {
		var err error
		receiptJSON, err = compliancev1.MarshalCanonical(record.ActionReceipt)
		if err != nil {
			return fmt.Errorf("%w: marshal canonical receipt: %w", constants.ErrAuditStoreRecordReceiptFailed, err)
		}
	}

	timestamp := record.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}

	// Canonicalization and encryption depend only on this receipt, not on either
	// chain head. Do this work before serializing writers on the ledger/SQLite.
	chainEvent := &Event{
		OperatorSessionID: record.OperatorSessionID,
		Timestamp:         timestamp,
		Type:              constants.EventOperatorReceiptRecorded,
		ContentText:       string(receiptJSON),
		TransactionID:     record.TransactionID,
	}
	prepared, err := ass.prepareAuditEventInsert(chainEvent)
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordReceiptFailed, err)
	}

	// Both chain appends select their heads inside one write transaction, so
	// the event chain and commitment ledger advance atomically.
	var commitment *CommitmentRow
	err = ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		// The session owner creates the session row at session start. A receipt
		// naming a session that was never started is rejected, never created here.
		if record.OperatorSessionID != "" {
			if err := ass.requireExistingSessionConn(conn, chainEvent); err != nil {
				return err
			}
		}

		if err := ass.upsertActionReceiptConn(conn, record, receiptJSON, timestamp); err != nil {
			return err
		}

		// The session was verified above; the receipt and event foreign keys hold.
		if _, _, _, err := AppendPreparedAuditEvent(context.Background(), conn, prepared); err != nil {
			return err
		}
		if build != nil {
			var err error
			commitment, err = ass.commitmentLedger.appendBuiltCommitment(conn, build)
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordReceiptFailed, err)
	}

	if commitment != nil {
		ass.commitmentLedger.logAppend(commitment)
	}
	ass.logger.Info("ActionReceipt recorded",
		"transaction_id", record.TransactionID,
		"status", record.Status)

	return nil
}

func (ass *SQLAuditStore) upsertActionReceiptConn(conn *sql.Conn, record *models.ActionReceiptRecord, receiptJSON []byte, timestamp time.Time) error {
	targetBytes, err := ass.encryptColumn("receipts.target_resource", record.TransactionID, record.TargetResource)
	if err != nil {
		return err
	}
	summaryBytes, err := ass.encryptColumn("receipts.result_summary", record.TransactionID, record.ResultSummary)
	if err != nil {
		return err
	}
	receiptBytes, err := ass.encryptColumn("receipts.receipt_json", record.TransactionID, string(receiptJSON))
	if err != nil {
		return err
	}

	query := `
	INSERT INTO receipts (
		transaction_id, transaction_hash, investigation_id, operator_id, operator_session_id,
		requestor_user_id, acting_app_id, event_type,
		action_type, target_resource, status, result_summary,
		state_root_before, state_root_after, executed_at_ms,
		signer_key_id, signature, receipt_json, timestamp
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(transaction_id) DO UPDATE SET
		investigation_id = excluded.investigation_id,
		status = excluded.status,
		result_summary = excluded.result_summary,
		state_root_after = excluded.state_root_after,
		executed_at_ms = excluded.executed_at_ms,
		signature = excluded.signature,
		receipt_json = excluded.receipt_json,
		timestamp = excluded.timestamp
	WHERE CAST(excluded.status AS INTEGER) NOT IN (?, ?) OR CAST(receipts.status AS INTEGER) IN (?, ?)
	`

	_, err = conn.ExecContext(context.Background(), query,
		record.TransactionID,
		record.TransactionHash,
		record.InvestigationID,
		record.OperatorID,
		record.OperatorSessionID,
		record.RequestorUserID,
		record.ActingAppID,
		record.EventType,
		record.ActionType,
		targetBytes,
		record.Status,
		summaryBytes,
		record.StateRootBefore,
		record.StateRootAfter,
		record.ExecutedAt.UnixMilli(),
		record.SignerKeyID,
		record.Signature,
		receiptBytes,
		timesvc.FormatTimestamp(timestamp),
		// The projection only moves forward: a replayed or late non-terminal
		// stage never overwrites a terminal receipt. Its audit fact is still
		// appended by the caller.
		operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED,
		operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
		operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED,
		operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING,
	)
	if err != nil {
		return fmt.Errorf("audit store: upsert receipt projection: %w", err)
	}
	return nil
}

// GetAuditChainHead returns the latest chained audit event sequence and hash.
func (ass *SQLAuditStore) GetAuditChainHead(ctx context.Context) (int64, string, error) {
	if ass == nil || ass.db == nil {
		return 0, "", constants.ErrAuditStoreDisabled
	}

	var headSeq int64
	var headHash string
	err := ass.db.QueryRowWithRetry(ctx, `
		SELECT seq, hash FROM events WHERE seq IS NOT NULL ORDER BY seq DESC LIMIT 1
	`).Scan(&headSeq, &headHash)
	if err == sql.ErrNoRows {
		return 0, auditChainGenesisPrevHash, nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("audit store: chain head: %w", err)
	}
	return headSeq, headHash, nil
}

// receiptSelectColumns is the receipts projection read by every receipt query.
// The projection columns the schema leaves nullable are NULL when the record's
// value is empty (empty content columns encrypt to NULL), so they are coalesced
// to the empty string the record's string fields expect.
const receiptSelectColumns = `transaction_id, transaction_hash, investigation_id, operator_id, operator_session_id,
		COALESCE(requestor_user_id, ''), COALESCE(acting_app_id, ''), COALESCE(event_type, ''),
		action_type, COALESCE(target_resource, ''), status, COALESCE(result_summary, ''),
		COALESCE(state_root_before, ''), COALESCE(state_root_after, ''), executed_at_ms,
		signer_key_id, signature, receipt_json, timestamp`

// hydrateReceiptRecord fills the derived fields of a scanned receipt row. L2/L3
// validity is not a projection column; it is restored from the canonical
// receipt exactly as governance.BuildReceiptRecord derives it at write time.
func hydrateReceiptRecord(r *models.ActionReceiptRecord, executedAtMs int64, timestampStr, receiptJSON string) error {
	r.ExecutedAt = time.UnixMilli(executedAtMs)
	r.Timestamp, _ = timesvc.ParseTimestamp(timestampStr)
	receipt, err := parseStoredActionReceipt(receiptJSON)
	if err != nil {
		return err
	}
	r.ActionReceipt = receipt
	if receipt != nil {
		r.L2Valid = receipt.GetL2Status() == operatorv1.L2Status_L2_STATUS_REQUIRED_VALID
		r.L3Valid = receipt.GetL3Status() == operatorv1.L3Status_L3_STATUS_REQUIRED_VALID
	}
	return nil
}

// decryptReceiptRow decrypts the encrypted receipts content columns of one row into r
// and returns the decrypted canonical receipt JSON. Fail-closed on a locked vault.
func (ass *SQLAuditStore) decryptReceiptRow(r *models.ActionReceiptRecord, targetBytes, summaryBytes, receiptBytes []byte) (string, error) {
	var err error
	if r.TargetResource, err = ass.decryptColumn("receipts.target_resource", r.TransactionID, targetBytes); err != nil {
		return "", err
	}
	if r.ResultSummary, err = ass.decryptColumn("receipts.result_summary", r.TransactionID, summaryBytes); err != nil {
		return "", err
	}
	return ass.decryptColumn("receipts.receipt_json", r.TransactionID, receiptBytes)
}

func parseStoredActionReceipt(receiptJSON string) (*operatorv1.ActionReceipt, error) {
	if receiptJSON == "" {
		return nil, nil
	}
	receipt := &operatorv1.ActionReceipt{}
	if err := compliancev1.UnmarshalCanonical([]byte(receiptJSON), receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

// DocSet implements governance.TransactionAuditStore for outbound mode. The
// outbound-mode L5Actuator persists signed ActionReceipt records via this
// method; the data payload is a JSON-encoded models.ActionReceiptRecord that
// is decoded and recorded in the receipts table via the transaction-native
// RecordActionReceipt API. The collection and id parameters are ignored —
// the receipts table is keyed by transaction_id embedded in the record.
func (ass *SQLAuditStore) DocSet(_ context.Context, collection, id string, data json.RawMessage) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	var receipt models.ActionReceiptRecord
	if err := json.Unmarshal(data, &receipt); err != nil {
		return fmt.Errorf("%w: SQLAuditStore.DocSet: failed to decode action receipt record: %w", constants.ErrInvalidJSONBody, err)
	}
	return ass.RecordActionReceipt(&receipt)
}

// DocDelete implements governance.TransactionAuditStore for outbound mode.
// The outbound operator does not persist governed documents — document
// mutations are a gateway-side concern. A usable store treats this as a no-op;
// a nil store fails closed with ErrAuditStoreDisabled like every other write path.
func (ass *SQLAuditStore) DocDelete(_ context.Context, collection, id string) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	return nil
}

// GetActionReceipt retrieves a single action receipt by transaction ID.
func (ass *SQLAuditStore) GetActionReceipt(transactionID string) (*models.ActionReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}

	query := `
	SELECT ` + receiptSelectColumns + `
	FROM receipts
	WHERE transaction_id = ?
	`

	var r models.ActionReceiptRecord
	var executedAtMs int64
	var timestampStr string
	var investigationID sql.NullString
	var sessionID sql.NullString
	var targetBytes, summaryBytes, receiptBytes []byte
	err := ass.db.QueryRowWithRetry(context.Background(), query, transactionID).Scan(
		&r.TransactionID, &r.TransactionHash, &investigationID, &r.OperatorID, &sessionID,
		&r.RequestorUserID, &r.ActingAppID, &r.EventType,
		&r.ActionType, &targetBytes, &r.Status, &summaryBytes,
		&r.StateRootBefore, &r.StateRootAfter, &executedAtMs,
		&r.SignerKeyID, &r.Signature, &receiptBytes, &timestampStr,
	)
	r.InvestigationID = investigationID.String
	r.OperatorSessionID = sessionID.String
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreGetReceiptFailed, err)
	}

	receiptJSON, err := ass.decryptReceiptRow(&r, targetBytes, summaryBytes, receiptBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreGetReceiptFailed, err)
	}
	if err := hydrateReceiptRecord(&r, executedAtMs, timestampStr, receiptJSON); err != nil {
		return nil, fmt.Errorf("%w: unmarshal canonical receipt: %w", constants.ErrAuditStoreGetReceiptFailed, err)
	}

	return &r, nil
}

func (ass *SQLAuditStore) GetActionReceiptByInvestigationID(
	investigationID string,
	actionType constants.ActionType,
) (*models.ActionReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(),
		ass.db,
		"SELECT transaction_id FROM receipts WHERE investigation_id = ? AND action_type = ? ORDER BY timestamp DESC LIMIT 2",
		[]interface{}{investigationID, actionType},
		func(row *sql.Rows) (string, error) {
			var transactionID string
			if err := row.Scan(&transactionID); err != nil {
				return "", err
			}
			return transactionID, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("%w: investigation correlation: %w", constants.ErrAuditStoreGetReceiptFailed, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if len(rows) > 1 {
		return nil, constants.ErrAuditStoreReceiptCorrelationDuplicate
	}
	return ass.GetActionReceipt(rows[0])
}

// auditReadScanBatchSize is how many SQL rows one scan pulls while
// ExcludeAuditReads filters the receipt stream after decryption.
const auditReadScanBatchSize = 100

// isAuditReadReceipt reports whether r is an audit-read tool's own MCP_CALL
// receipt. For MCP_CALL, target_resource is the tool name: the Gateway MCP
// ingress and the evaluation lane both set it, and the executing Operator signs
// the envelope target into the receipt.
func isAuditReadReceipt(r *models.ActionReceiptRecord) bool {
	if r == nil || r.ActionType != constants.ActionTypeMcpCall {
		return false
	}
	return r.TargetResource == constants.MCPToolAuditReceiptList || r.TargetResource == constants.MCPToolAuditReceiptGet
}

// ListActionReceipts retrieves action receipts with optional filtering and pagination.
// With ExcludeAuditReads, limit and offset apply after the audit reads are dropped.
func (ass *SQLAuditStore) ListActionReceipts(scope models.AuditScope, limit, offset int) ([]*models.ActionReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}

	if limit <= 0 {
		limit = 50
	}

	query, args := receiptScopeQuery(scope)
	if !scope.ExcludeAuditReads {
		return ass.queryReceiptRows(query+" ORDER BY timestamp DESC LIMIT ? OFFSET ?", append(args, limit, offset))
	}
	return ass.listReceiptsExcludingAuditReads(query, args, limit, offset)
}

// receiptScopeQuery builds the receipts SELECT for the scope fields that SQL can
// filter. target_resource is encrypted, so audit-read exclusion is not a predicate here.
func receiptScopeQuery(scope models.AuditScope) (string, []interface{}) {
	args := []interface{}{}
	predicates := []string{}
	if scope.OperatorID != "" {
		predicates = append(predicates, "operator_id = ?")
		args = append(args, scope.OperatorID)
	}
	if scope.OperatorSessionID != "" {
		predicates = append(predicates, "operator_session_id = ?")
		args = append(args, scope.OperatorSessionID)
	}
	if scope.InvestigationID != "" {
		predicates = append(predicates, "investigation_id = ?")
		args = append(args, scope.InvestigationID)
	}
	if scope.ActingAppID != "" {
		predicates = append(predicates, "acting_app_id = ?")
		args = append(args, scope.ActingAppID)
	}
	query := "SELECT " + receiptSelectColumns + " FROM receipts"
	if len(predicates) > 0 {
		query += " WHERE " + strings.Join(predicates, " AND ")
	}
	return query, args
}

// listReceiptsExcludingAuditReads pages the SQL scope newest first in batches, drops
// audit reads from each decrypted batch, and applies offset and limit to what remains.
// It stops once limit receipts are collected or the scope is exhausted.
func (ass *SQLAuditStore) listReceiptsExcludingAuditReads(query string, args []interface{}, limit, offset int) ([]*models.ActionReceiptRecord, error) {
	var out []*models.ActionReceiptRecord
	skip := offset
	for sqlOffset := 0; ; sqlOffset += auditReadScanBatchSize {
		batchArgs := append(slices.Clone(args), auditReadScanBatchSize, sqlOffset)
		batch, err := ass.queryReceiptRows(query+" ORDER BY timestamp DESC LIMIT ? OFFSET ?", batchArgs)
		if err != nil {
			return nil, err
		}
		for _, r := range batch {
			if isAuditReadReceipt(r) {
				continue
			}
			if skip > 0 {
				skip--
				continue
			}
			out = append(out, r)
			if len(out) == limit {
				return out, nil
			}
		}
		if len(batch) < auditReadScanBatchSize {
			return out, nil
		}
	}
}

// queryReceiptRows runs one receipts SELECT and returns its rows decrypted and hydrated.
func (ass *SQLAuditStore) queryReceiptRows(query string, args []interface{}) ([]*models.ActionReceiptRecord, error) {
	type receiptRow struct {
		record          models.ActionReceiptRecord
		executedAtMs    int64
		timestampStr    string
		investigationID sql.NullString
		sessionID       sql.NullString
		targetBytes     []byte
		summaryBytes    []byte
		receiptBytes    []byte
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, args, func(r *sql.Rows) (receiptRow, error) {
		var row receiptRow
		err := r.Scan(
			&row.record.TransactionID, &row.record.TransactionHash, &row.investigationID, &row.record.OperatorID, &row.sessionID,
			&row.record.RequestorUserID, &row.record.ActingAppID, &row.record.EventType,
			&row.record.ActionType, &row.targetBytes, &row.record.Status, &row.summaryBytes,
			&row.record.StateRootBefore, &row.record.StateRootAfter, &row.executedAtMs,
			&row.record.SignerKeyID, &row.record.Signature, &row.receiptBytes, &row.timestampStr,
		)
		row.record.InvestigationID = row.investigationID.String
		row.record.OperatorSessionID = row.sessionID.String
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
	}

	var results []*models.ActionReceiptRecord
	for _, row := range rows {
		receiptJSON, err := ass.decryptReceiptRow(&row.record, row.targetBytes, row.summaryBytes, row.receiptBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
		}
		if err := hydrateReceiptRecord(&row.record, row.executedAtMs, row.timestampStr, receiptJSON); err != nil {
			return nil, fmt.Errorf("%w: unmarshal canonical receipt: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
		}
		results = append(results, &row.record)
	}

	return results, nil
}

// ListActionReceiptsSince retrieves action receipts newer than the given timestamp.
func (ass *SQLAuditStore) ListActionReceiptsSince(since time.Time, limit int) ([]*models.ActionReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}

	if limit <= 0 {
		limit = 100
	}

	query := `
	SELECT ` + receiptSelectColumns + `
	FROM receipts
	WHERE timestamp > ?
	ORDER BY timestamp ASC
	LIMIT ?
	`

	type receiptRow struct {
		record          models.ActionReceiptRecord
		executedAtMs    int64
		timestampStr    string
		investigationID sql.NullString
		sessionID       sql.NullString
		targetBytes     []byte
		summaryBytes    []byte
		receiptBytes    []byte
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, []interface{}{timesvc.FormatTimestamp(since), limit}, func(r *sql.Rows) (receiptRow, error) {
		var row receiptRow
		err := r.Scan(
			&row.record.TransactionID, &row.record.TransactionHash, &row.investigationID, &row.record.OperatorID, &row.sessionID,
			&row.record.RequestorUserID, &row.record.ActingAppID, &row.record.EventType,
			&row.record.ActionType, &row.targetBytes, &row.record.Status, &row.summaryBytes,
			&row.record.StateRootBefore, &row.record.StateRootAfter, &row.executedAtMs,
			&row.record.SignerKeyID, &row.record.Signature, &row.receiptBytes, &row.timestampStr,
		)
		row.record.InvestigationID = row.investigationID.String
		row.record.OperatorSessionID = row.sessionID.String
		return row, err
	})
	if err != nil {
		return nil, fmt.Errorf("%w %v: %w", constants.ErrAuditStoreQueryReceiptsSinceFailed, since, err)
	}

	var results []*models.ActionReceiptRecord
	for _, row := range rows {
		receiptJSON, err := ass.decryptReceiptRow(&row.record, row.targetBytes, row.summaryBytes, row.receiptBytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
		}
		if err := hydrateReceiptRecord(&row.record, row.executedAtMs, row.timestampStr, receiptJSON); err != nil {
			return nil, fmt.Errorf("%w: unmarshal canonical receipt: %w", constants.ErrAuditStoreQueryReceiptsSinceFailed, err)
		}
		results = append(results, &row.record)
	}

	return results, nil
}

// decryptEventContent decrypts the four encrypted content columns of one events row
// into event. Fail-closed: a locked vault or a decrypt failure returns an error and
// never returns ciphertext or silently empty content.
func (ass *SQLAuditStore) decryptEventContent(event *Event, contentText, commandRaw, stdout, stderr []byte) error {
	return decryptEventContentWith(ass.encryptionVault, event, contentText, commandRaw, stdout, stderr)
}

// decryptEventContentWith decrypts the four encrypted content columns of the events row
// event.Seq identifies. The chain sequence is bound into each ciphertext, so a blob from
// another row or column fails authentication.
func decryptEventContentWith(v *vault.Vault, event *Event, contentText, commandRaw, stdout, stderr []byte) error {
	rowID := auditEventRowID(event.Seq)
	var err error
	if event.ContentText, err = decryptAuditContent(v, auditColumnEventContentText, rowID, contentText); err != nil {
		return fmt.Errorf("audit store: decrypt events.content_text for event %d: %w", event.ID, err)
	}
	if event.CommandRaw, err = decryptAuditContent(v, auditColumnEventCommandRaw, rowID, commandRaw); err != nil {
		return fmt.Errorf("audit store: decrypt events.command_raw for event %d: %w", event.ID, err)
	}
	if event.CommandStdout, err = decryptAuditOutput(v, auditColumnEventStdout, rowID, stdout); err != nil {
		return fmt.Errorf("audit store: decrypt events.command_stdout for event %d: %w", event.ID, err)
	}
	if event.CommandStderr, err = decryptAuditOutput(v, auditColumnEventStderr, rowID, stderr); err != nil {
		return fmt.Errorf("audit store: decrypt events.command_stderr for event %d: %w", event.ID, err)
	}
	return nil
}

// GetEvents retrieves newest events for the explicit audit scope with pagination.
// An empty scope includes records without an Operator session. App identity is
// resolved through the transaction receipt, never inferred from a session.
// Content columns are always encrypted; they are decrypted here and a locked vault fails the read.
func (ass *SQLAuditStore) GetEvents(scope models.AuditScope, limit, offset int) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
func (ass *SQLAuditStore) GetTransactionEvents(transactionID string, eventType constants.EventType) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}
	if transactionID == "" {
		return nil, fmt.Errorf("audit store: transaction events: transaction_id: %w", constants.ErrMissingRequiredField)
	}
	return ass.materializeEvents(eventSelectColumns+" WHERE "+eventTransactionTypePredicate+" ORDER BY id ASC", []interface{}{transactionID, string(eventType)})
}

// materializeEvents runs an events query selecting eventSelectColumns and
// decrypts each row.
func (ass *SQLAuditStore) materializeEvents(query string, args []interface{}) ([]*Event, error) {
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryEventsFailed, err)
	}

	var events []*Event
	for _, row := range rows {
		row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)

		if err := ass.decryptEventContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdoutBytes, row.commandStderrBytes); err != nil {
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
// missing execution returns ErrAuditExecutionNotFound.
func (ass *SQLAuditStore) GetEventByExecutionID(executionID string) (*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}
	if executionID == "" {
		return nil, constants.ErrAuditExecutionNotFound
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryEventsFailed, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", constants.ErrAuditExecutionNotFound, executionID)
	}

	row := rows[0]
	row.event.OperatorSessionID = row.sessionID.String
	row.event.ExecutionID = row.executionID.String
	row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)
	if err := ass.decryptEventContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdout, row.commandStderr); err != nil {
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

// RecordFileMutation records a file mutation in the audit log
func (ass *SQLAuditStore) RecordFileMutation(mutation *FileMutationLog) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
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
		return fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordFileMutationFailed, err)
	}

	ass.logger.Info("File mutation recorded",
		"event_id", mutation.EventID,
		"operation", mutation.Operation)

	return nil
}

// GetFileMutations retrieves file mutations for an event
func (ass *SQLAuditStore) GetFileMutations(eventID int64) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
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
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
		}
		if row.mutation.DiffStat, err = ass.decryptColumn("file_mutation_log.diff_stat", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.diffStatBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
		}

		mutations = append(mutations, &row.mutation)
	}

	return mutations, nil
}

// ListSessions retrieves all sessions with pagination, ordered by created_at ASC.
func (ass *SQLAuditStore) ListSessions(limit, offset int) ([]*OperatorSession, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreGetSessionFailed, err)
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
func (ass *SQLAuditStore) ListEvents(sessionID string, limit, offset int) ([]*Event, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryEventsFailed, err)
	}

	var events []*Event
	for _, row := range rows {
		row.event.Timestamp, _ = timesvc.ParseTimestamp(row.timestampStr)

		if err := ass.decryptEventContent(&row.event, row.contentTextBytes, row.commandRawBytes, row.commandStdoutBytes, row.commandStderrBytes); err != nil {
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
// A missing row returns ErrAuditFileMutationNotFound.
func (ass *SQLAuditStore) GetFileMutationByID(id int64) (*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %d", constants.ErrAuditFileMutationNotFound, id)
	}
	return rows[0], nil
}

// ListFileMutationsBySession returns the newest file mutations of one operator session,
// decrypted, capped at limit.
func (ass *SQLAuditStore) ListFileMutationsBySession(operatorSessionID string, limit int) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
func (ass *SQLAuditStore) queryFileMutations(query string, args []interface{}) ([]*FileMutationLog, error) {
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
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
func (ass *SQLAuditStore) ListFileMutations(limit, offset int) ([]*FileMutationLog, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
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
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
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
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
		}
		if row.mutation.DiffStat, err = ass.decryptColumn("file_mutation_log.diff_stat", fileMutationRowID(row.mutation.EventID, row.mutation.UID), row.diffStatBytes); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryFileMutationsFailed, err)
		}
		mutations = append(mutations, &row.mutation)
	}

	return mutations, nil
}

// auditStorePrune returns a PruneFunc that handles retention pruning
// for events, receipts, ended sessions, and file mutations.
func auditStorePrune(config *AuditStoreConfig) sqliteutil.PruneFunc {
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

		// 2. Checkpoint and delete chained events older than retention period
		if err := PruneChainedAuditEvents(ctx, db, logger, config.EncryptionVault, cutoff); err != nil {
			logger.Error("Failed to prune chained audit events", string(constants.ConnectionStateError), err)
			return err
		}

		// 3. Delete receipts older than retention period
		result, err := db.ExecWithRetry(ctx, "DELETE FROM receipts WHERE timestamp < ?", cutoff)
		if err != nil {
			logger.Error("Failed to prune old receipts", string(constants.ConnectionStateError), err)
			return err
		}
		rowsDeleted, _ := result.RowsAffected()
		if rowsDeleted > 0 {
			logger.Info("Pruned old receipts", "rows_deleted", rowsDeleted)
		}

		// 4. Delete ended sessions past retention that no longer have any events or
		// receipts. A session with NULL ended_at is still owned by a live runtime
		// session (idle sessions write no events) and is never pruned.
		_, err = db.ExecWithRetry(ctx, `
			DELETE FROM sessions
			WHERE ended_at IS NOT NULL AND ended_at < ?
			AND id NOT IN (SELECT DISTINCT operator_session_id FROM events WHERE operator_session_id IS NOT NULL)
			AND id NOT IN (SELECT DISTINCT operator_session_id FROM receipts)
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
func (ass *SQLAuditStore) GetEncryptionVault() *vault.Vault {
	if ass == nil {
		return nil
	}
	return ass.encryptionVault
}

// Wait blocks until all in-flight writes have finished.
func (ass *SQLAuditStore) Wait() {
	if ass == nil {
		return
	}
	ass.muWrites.Wait()
}

// Close drains in-flight writes and stops the pruner. The database belongs to
// the caller and stays open. Idempotent.
func (ass *SQLAuditStore) Close() error {
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

// GetDataDir returns the audit store data directory
func (ass *SQLAuditStore) GetDataDir() string {
	if ass == nil || ass.fileSvc == nil {
		return ""
	}
	return ass.fileSvc.Resolve(constants.DataDirname)
}

// Content column identities bound into the AEAD associated data. A ciphertext only
// decrypts under the column and row it was written for.
const (
	auditColumnEventContentText = "events.content_text"
	auditColumnEventCommandRaw  = "events.command_raw"
	auditColumnEventStdout      = "events.command_stdout"
	auditColumnEventStderr      = "events.command_stderr"
	ReceiptColumnReceiptJSON    = "receipts.receipt_json"

	auditContentAADVersion = "ztd-content-aad-v1"
)

// AuditContentAAD returns the associated data that binds a ciphertext to its
// "table.column" and row identity. The row identity is known before the insert:
// the chain sequence for events, the session id, the transaction id for receipts
// and suspended transactions, the commitment hash, or the event id and mutation uid
// for file mutations. It is never the autoincrement rowid.
func AuditContentAAD(column, rowID string) []byte {
	return []byte(auditContentAADVersion + "|" + column + "|" + rowID)
}

// auditEventRowID is the events row identity bound into the AEAD data.
func auditEventRowID(seq int64) string {
	return strconv.FormatInt(seq, 10)
}

// fileMutationRowID is the file_mutation_log row identity bound into the AEAD data.
func fileMutationRowID(eventID int64, uid string) string {
	return strconv.FormatInt(eventID, 10) + "|" + uid
}

// newFileMutationUID returns a random identity for one file_mutation_log row.
func newFileMutationUID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("audit store: file mutation uid: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// encryptAuditContent encrypts content with the runtime vault, binding the "table.column"
// and row identity as AEAD associated data. It is the single content-encryption path for
// every store that writes encrypted columns (the audit store, the commitment ledger, and
// the suspended transaction store). A nil or locked vault returns
// ErrAuditStoreVaultLocked; there is no plaintext branch.
func encryptAuditContent(v *vault.Vault, column, rowID, content string) ([]byte, error) {
	if v == nil || !v.IsUnlocked() {
		return nil, constants.ErrAuditStoreVaultLocked
	}
	if content == "" {
		return nil, nil
	}
	encrypted, err := v.EncryptWithAAD([]byte(content), AuditContentAAD(column, rowID))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptFailed, err)
	}
	return encrypted, nil
}

// decryptAuditContent decrypts content written by encryptAuditContent for the same
// column and row identity. Empty input is the empty string and needs no vault. A nil or
// locked vault returns ErrAuditStoreVaultLocked; a ciphertext moved to another row or
// column fails authentication.
func decryptAuditContent(v *vault.Vault, column, rowID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if v == nil || !v.IsUnlocked() {
		return "", constants.ErrAuditStoreVaultLocked
	}
	decrypted, err := v.DecryptWithAAD(data, AuditContentAAD(column, rowID))
	if err != nil {
		return "", fmt.Errorf("%w: %w", constants.ErrAuditStoreDecryptFailed, err)
	}
	return string(decrypted), nil
}

// encryptColumn encrypts one named content column value for one row. Fail-closed: a
// locked vault returns ErrAuditStoreVaultLocked and the write does not happen.
func (ass *SQLAuditStore) encryptColumn(column, rowID, value string) ([]byte, error) {
	data, err := encryptAuditContent(ass.encryptionVault, column, rowID, value)
	if err != nil {
		return nil, fmt.Errorf("audit store: encrypt %s: %w", column, err)
	}
	return data, nil
}

// decryptColumn decrypts one named content column value of one row. Fail-closed: a locked
// vault or a decrypt failure returns an error, never ciphertext.
func (ass *SQLAuditStore) decryptColumn(column, rowID string, data []byte) (string, error) {
	value, err := decryptAuditContent(ass.encryptionVault, column, rowID, data)
	if err != nil {
		return "", fmt.Errorf("audit store: decrypt %s: %w", column, err)
	}
	return value, nil
}

// compressAuditOutput gzips command output before encryption. Nothing truncates: the
// local record is the raw output. Empty output is nil (stored as NULL).
func compressAuditOutput(column, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	compressed, err := sqliteutil.Compress([]byte(value))
	if err != nil {
		return nil, fmt.Errorf("audit store: compress %s: %w", column, err)
	}
	return compressed, nil
}

// encryptAuditOutput encrypts gzip-compressed command output bound to its row. Empty
// output is NULL.
func encryptAuditOutput(v *vault.Vault, column, rowID string, compressed []byte) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, nil
	}
	data, err := encryptAuditContent(v, column, rowID, string(compressed))
	if err != nil {
		return nil, fmt.Errorf("audit store: encrypt %s: %w", column, err)
	}
	return data, nil
}

func decryptAuditOutput(v *vault.Vault, column, rowID string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	compressed, err := decryptAuditContent(v, column, rowID, data)
	if err != nil {
		return "", fmt.Errorf("audit store: decrypt %s: %w", column, err)
	}
	raw, err := sqliteutil.Decompress([]byte(compressed))
	if err != nil {
		return "", fmt.Errorf("audit store: decompress %s: %w", column, err)
	}
	return string(raw), nil
}

// encryptSessionFields encrypts the title and user_identity content columns of the session row id.
func (ass *SQLAuditStore) encryptSessionFields(id, title, userIdentity string) (titleBytes, identityBytes []byte, err error) {
	if titleBytes, err = ass.encryptColumn("sessions.title", id, title); err != nil {
		return nil, nil, err
	}
	if identityBytes, err = ass.encryptColumn("sessions.user_identity", id, userIdentity); err != nil {
		return nil, nil, err
	}
	return titleBytes, identityBytes, nil
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

const (
	auditChainGenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"
	auditChainHashVersion     = "ztd-audit-v1"

	// auditContentDigestLabel selects the vault subkey that keys content_digest.
	auditContentDigestLabel = "ztd-audit-content-digest-v1"
)

// AuditChainCheckpoint is a durable pruning anchor for chain verification.
type AuditChainCheckpoint struct {
	PrunedThroughSeq  int64
	PrunedThroughHash string
	PrunedCount       int64
	CheckpointHash    string
	CreatedAt         time.Time
}

type auditChainHead struct {
	Seq      int64
	PrevHash string
}

type auditChainCheckpointPayload struct {
	PrunedThroughSeq  int64  `json:"pruned_through_seq"`
	PrunedThroughHash string `json:"pruned_through_hash"`
	PrunedCount       int64  `json:"pruned_count"`
}

type eventContentDigestInput struct {
	ContentText         string `json:"content_text"`
	CommandRaw          string `json:"command_raw"`
	CommandExitCode     int    `json:"command_exit_code"`
	CommandStdout       string `json:"command_stdout"`
	CommandStderr       string `json:"command_stderr"`
	ExecutionDurationMs int64  `json:"execution_duration_ms"`
	ExecutionID         string `json:"execution_id"`
}

// computeEventContentDigest returns the keyed content digest stored in events.content_digest:
// HMAC-SHA256 over the canonical content fields under a subkey the runtime vault derives
// from its DEK. The digest is stored in the clear and is hashed into the chain, so it must
// not be an unkeyed hash of the plaintext: short or low-entropy content (approval texts,
// paths, events without an execution id) would be brute-forceable offline. A nil or locked
// vault fails closed with ErrAuditStoreVaultLocked.
func computeEventContentDigest(v *vault.Vault, event *Event) (string, error) {
	if event == nil {
		return "", constants.ErrAuditEventNil
	}
	if v == nil || !v.IsUnlocked() {
		return "", constants.ErrAuditStoreVaultLocked
	}
	payload := eventContentDigestInput{
		ContentText:         event.ContentText,
		CommandRaw:          event.CommandRaw,
		CommandExitCode:     event.CommandExitCode,
		CommandStdout:       event.CommandStdout,
		CommandStderr:       event.CommandStderr,
		ExecutionDurationMs: event.ExecutionDurationMs,
		ExecutionID:         event.ExecutionID,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("audit chain: content digest marshal: %w", err)
	}
	mac, err := v.HMACSHA256(auditContentDigestLabel, raw)
	if err != nil {
		return "", fmt.Errorf("audit chain: content digest: %w", err)
	}
	return hex.EncodeToString(mac), nil
}

func computeAuditEventHash(seq int64, prevHash, eventType, sessionID, timestamp, contentDigest, transactionID string) string {
	canonical := strings.Join([]string{
		auditChainHashVersion,
		strconv.FormatInt(seq, 10),
		prevHash,
		eventType,
		sessionID,
		timestamp,
		contentDigest,
		transactionID,
	}, "|")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func loadAuditChainHeadConn(ctx context.Context, conn *sql.Conn) (auditChainHead, error) {
	var head auditChainHead
	err := conn.QueryRowContext(ctx, `
		SELECT seq, hash FROM events WHERE seq IS NOT NULL ORDER BY seq DESC LIMIT 1
	`).Scan(&head.Seq, &head.PrevHash)
	if err == nil {
		return head, nil
	}
	if err != sql.ErrNoRows {
		return auditChainHead{}, fmt.Errorf("audit chain: select head: %w", err)
	}

	var checkpointHash string
	err = conn.QueryRowContext(ctx, `
		SELECT pruned_through_hash FROM audit_chain_checkpoints ORDER BY pruned_through_seq DESC LIMIT 1
	`).Scan(&checkpointHash)
	if err == sql.ErrNoRows {
		return auditChainHead{Seq: 0, PrevHash: auditChainGenesisPrevHash}, nil
	}
	if err != nil {
		return auditChainHead{}, fmt.Errorf("audit chain: select checkpoint: %w", err)
	}
	return auditChainHead{Seq: 0, PrevHash: checkpointHash}, nil
}

// PreparedAuditEventInsert holds an event ready for chained append. Stdout and Stderr are
// the gzip-compressed command output; AppendPreparedAuditEvent encrypts every content
// column under the vault once the chain sequence that identifies the row is assigned.
type PreparedAuditEventInsert struct {
	Event        *Event
	TimestampStr string
	Vault        *vault.Vault
	Stdout       []byte
	Stderr       []byte
}

// AppendPreparedAuditEvent appends one event to the audit chain under SQLite's write lock.
func AppendPreparedAuditEvent(ctx context.Context, conn *sql.Conn, prepared PreparedAuditEventInsert) (int64, int64, string, error) {
	head, err := loadAuditChainHeadConn(ctx, conn)
	if err != nil {
		return 0, 0, "", err
	}

	nextSeq := head.Seq + 1
	// The digest covers the stored plaintext: the event carries the full raw output.
	contentDigest, err := computeEventContentDigest(prepared.Vault, prepared.Event)
	if err != nil {
		return 0, 0, "", err
	}
	// Each content ciphertext is bound to this row by its chain sequence, which the
	// chain hash covers, so a blob moved to another row or column fails to decrypt.
	rowID := auditEventRowID(nextSeq)
	contentTextBytes, err := encryptAuditContent(prepared.Vault, auditColumnEventContentText, rowID, prepared.Event.ContentText)
	if err != nil {
		return 0, 0, "", fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptContentFailed, err)
	}
	commandRawBytes, err := encryptAuditContent(prepared.Vault, auditColumnEventCommandRaw, rowID, prepared.Event.CommandRaw)
	if err != nil {
		return 0, 0, "", fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptContentFailed, err)
	}
	stdoutBytes, err := encryptAuditOutput(prepared.Vault, auditColumnEventStdout, rowID, prepared.Stdout)
	if err != nil {
		return 0, 0, "", fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptStdoutFailed, err)
	}
	stderrBytes, err := encryptAuditOutput(prepared.Vault, auditColumnEventStderr, rowID, prepared.Stderr)
	if err != nil {
		return 0, 0, "", fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptStderrFailed, err)
	}
	hash := computeAuditEventHash(
		nextSeq,
		head.PrevHash,
		string(prepared.Event.Type),
		prepared.Event.OperatorSessionID,
		prepared.TimestampStr,
		contentDigest,
		prepared.Event.TransactionID,
	)

	sessionID := sql.NullString{}
	if prepared.Event.OperatorSessionID != "" {
		sessionID = sql.NullString{String: prepared.Event.OperatorSessionID, Valid: true}
	}

	result, err := conn.ExecContext(ctx, `
		INSERT INTO events (
			operator_session_id, timestamp, type, content_text,
			command_raw, command_exit_code, command_stdout, command_stderr,
			execution_duration_ms, stored_locally, execution_id,
			seq, prev_hash, content_digest, hash, transaction_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		sessionID,
		prepared.TimestampStr,
		prepared.Event.Type,
		contentTextBytes,
		commandRawBytes,
		prepared.Event.CommandExitCode,
		stdoutBytes,
		stderrBytes,
		prepared.Event.ExecutionDurationMs,
		true,
		sql.NullString{String: prepared.Event.ExecutionID, Valid: prepared.Event.ExecutionID != ""},
		nextSeq,
		head.PrevHash,
		contentDigest,
		hash,
		prepared.Event.TransactionID,
	)
	if err != nil {
		return 0, 0, "", fmt.Errorf("%w: %w", constants.ErrAuditStoreRecordEventFailed, err)
	}
	eventID, _ := result.LastInsertId()
	return eventID, nextSeq, hash, nil
}

func appendAuditChainCheckpoint(
	ctx context.Context,
	conn *sql.Conn,
	encryptionVault *vault.Vault,
	prunedThroughSeq int64,
	prunedThroughHash string,
	prunedCount int64,
) (string, error) {
	payload, err := json.Marshal(auditChainCheckpointPayload{
		PrunedThroughSeq:  prunedThroughSeq,
		PrunedThroughHash: prunedThroughHash,
		PrunedCount:       prunedCount,
	})
	if err != nil {
		return "", fmt.Errorf("audit chain checkpoint: marshal payload: %w", err)
	}

	now := time.Now().UTC()
	checkpointEvent := &Event{
		Timestamp:   now,
		Type:        constants.EventPlatformAuditChainCheckpointed,
		ContentText: string(payload),
	}
	// The checkpoint payload is metadata (sequence, hash, count), but it is written
	// to events.content_text, so the append encrypts it like every other content column.
	prepared := PreparedAuditEventInsert{
		Event:        checkpointEvent,
		TimestampStr: timesvc.FormatTimestamp(now),
		Vault:        encryptionVault,
	}
	if _, _, _, err := AppendPreparedAuditEvent(ctx, conn, prepared); err != nil {
		return "", err
	}

	head, err := loadAuditChainHeadConn(ctx, conn)
	if err != nil {
		return "", err
	}

	if _, err := conn.ExecContext(ctx, `
		INSERT INTO audit_chain_checkpoints (
			pruned_through_seq, pruned_through_hash, pruned_count, checkpoint_hash, created_at
		) VALUES (?, ?, ?, ?, ?)
	`, prunedThroughSeq, prunedThroughHash, prunedCount, head.PrevHash, timesvc.FormatTimestamp(now)); err != nil {
		return "", fmt.Errorf("audit chain checkpoint: persist anchor: %w", err)
	}
	return head.PrevHash, nil
}

// chainVerifyStart resolves the first sequence to verify and the hash that must precede it.
// When fromSeq is zero, verification starts after the latest durable checkpoint.
func (ass *SQLAuditStore) chainVerifyStart(ctx context.Context, fromSeq int64) (startSeq int64, expectedPrevHash string, err error) {
	if fromSeq > 0 {
		startSeq = fromSeq
		expectedPrevHash = auditChainGenesisPrevHash
		if startSeq > 1 {
			err := ass.db.QueryRowWithRetry(ctx, `SELECT hash FROM events WHERE seq = ?`, startSeq-1).Scan(&expectedPrevHash)
			if err == sql.ErrNoRows {
				var checkpoint AuditChainCheckpoint
				err = ass.db.QueryRowWithRetry(ctx, `
					SELECT pruned_through_seq, pruned_through_hash
					FROM audit_chain_checkpoints
					WHERE pruned_through_seq < ?
					ORDER BY pruned_through_seq DESC
					LIMIT 1
				`, startSeq).Scan(&checkpoint.PrunedThroughSeq, &checkpoint.PrunedThroughHash)
				if err != nil && err != sql.ErrNoRows {
					return 0, "", fmt.Errorf("audit chain verify: checkpoint lookup: %w", err)
				}
				if err == nil && checkpoint.PrunedThroughSeq == startSeq-1 {
					expectedPrevHash = checkpoint.PrunedThroughHash
				} else {
					return 0, "", fmt.Errorf("audit chain verify: missing predecessor for seq %d", startSeq)
				}
			} else if err != nil {
				return 0, "", fmt.Errorf("audit chain verify: predecessor lookup: %w", err)
			}
		}
	} else {
		var checkpointSeq int64
		var checkpointHash string
		err := ass.db.QueryRowWithRetry(ctx, `
			SELECT pruned_through_seq, pruned_through_hash
			FROM audit_chain_checkpoints
			ORDER BY pruned_through_seq DESC
			LIMIT 1
		`).Scan(&checkpointSeq, &checkpointHash)
		if err == sql.ErrNoRows {
			startSeq = 1
			expectedPrevHash = auditChainGenesisPrevHash
		} else if err != nil {
			return 0, "", fmt.Errorf("audit chain verify: latest checkpoint: %w", err)
		} else {
			startSeq = checkpointSeq + 1
			expectedPrevHash = checkpointHash
		}
	}
	return startSeq, expectedPrevHash, nil
}

// VerifyChain walks the audit event chain from fromSeq and recomputes hashes.
// When fromSeq is zero, verification starts after the latest durable checkpoint.
// It checks linkage and the hashes over the stored content_digest only; it does not
// decrypt content, so it cannot detect an altered ciphertext. VerifyChainContent does.
func (ass *SQLAuditStore) VerifyChain(ctx context.Context, fromSeq int64) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}

	startSeq, expectedPrevHash, err := ass.chainVerifyStart(ctx, fromSeq)
	if err != nil {
		return err
	}

	rows, err := ass.db.QueryWithRetry(ctx, `
		SELECT seq, prev_hash, hash, type, operator_session_id, timestamp, content_digest, transaction_id
		FROM events
		WHERE seq IS NOT NULL AND seq >= ?
		ORDER BY seq ASC
	`, startSeq)
	if err != nil {
		return fmt.Errorf("audit chain verify: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			seq           int64
			prevHash      string
			hash          string
			eventType     string
			sessionID     sql.NullString
			timestampStr  string
			contentDigest string
			transactionID sql.NullString
		)
		if err := rows.Scan(&seq, &prevHash, &hash, &eventType, &sessionID, &timestampStr, &contentDigest, &transactionID); err != nil {
			return fmt.Errorf("audit chain verify: scan: %w", err)
		}
		if prevHash != expectedPrevHash {
			return fmt.Errorf("audit chain verify: prev_hash mismatch at seq %d", seq)
		}
		recomputed := computeAuditEventHash(
			seq,
			prevHash,
			eventType,
			sessionID.String,
			timestampStr,
			contentDigest,
			transactionID.String,
		)
		if recomputed != hash {
			return fmt.Errorf("audit chain verify: hash mismatch at seq %d", seq)
		}
		expectedPrevHash = hash
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("audit chain verify: rows: %w", err)
	}
	return nil
}

// VerifyChainContent verifies the audit chain and then the content behind it. It runs
// VerifyChain, then for every event from the same start it decrypts each content column
// under its row binding, recomputes the keyed content digest, and compares it with the
// stored content_digest. A ciphertext that was altered, or moved from another row or
// column, fails authentication; altered plaintext fails the digest comparison. Both
// return ErrAuditChainContentMismatch with the sequence. A nil or locked vault fails
// closed with ErrAuditStoreVaultLocked before any row is read.
func (ass *SQLAuditStore) VerifyChainContent(ctx context.Context, fromSeq int64) error {
	if ass == nil || ass.db == nil {
		return constants.ErrAuditStoreDisabled
	}
	if ass.encryptionVault == nil || !ass.encryptionVault.IsUnlocked() {
		return constants.ErrAuditStoreVaultLocked
	}
	if err := ass.VerifyChain(ctx, fromSeq); err != nil {
		return err
	}
	startSeq, _, err := ass.chainVerifyStart(ctx, fromSeq)
	if err != nil {
		return err
	}

	rows, err := ass.db.QueryWithRetry(ctx, `
		SELECT `+auditEventContentColumns+`
		FROM events
		WHERE seq IS NOT NULL AND seq >= ?
		ORDER BY seq ASC
	`, startSeq)
	if err != nil {
		return fmt.Errorf("audit chain content verify: query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var row auditEventContentRow
		if err := rows.Scan(row.scanTargets()...); err != nil {
			return fmt.Errorf("audit chain content verify: scan: %w", err)
		}
		if _, err := verifyAuditEventContent(ass.encryptionVault, row); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("audit chain content verify: rows: %w", err)
	}
	return nil
}

// auditEventContentColumns lists the events columns that scanTargets fills, in order.
const auditEventContentColumns = `seq, content_digest, content_text, command_raw, command_exit_code,
		command_stdout, command_stderr, execution_duration_ms, execution_id`

// auditEventContentRow is one events row's stored digest and encrypted content columns.
type auditEventContentRow struct {
	seq         int64
	digest      string
	contentText []byte
	commandRaw  []byte
	exitCode    sql.NullInt64
	stdout      []byte
	stderr      []byte
	durationMs  sql.NullInt64
	executionID sql.NullString
}

func (r *auditEventContentRow) scanTargets() []any {
	return []any{&r.seq, &r.digest, &r.contentText, &r.commandRaw, &r.exitCode, &r.stdout, &r.stderr, &r.durationMs, &r.executionID}
}

// verifyAuditEventContent decrypts one row's content columns under their row binding,
// recomputes the keyed content digest, and returns the decrypted event when it matches
// the stored digest. A ciphertext that fails authentication (altered, or moved from
// another row or column) or a digest that differs returns ErrAuditChainContentMismatch
// with the sequence; a nil or locked vault returns ErrAuditStoreVaultLocked.
func verifyAuditEventContent(v *vault.Vault, row auditEventContentRow) (*Event, error) {
	if v == nil || !v.IsUnlocked() {
		return nil, constants.ErrAuditStoreVaultLocked
	}
	event := &Event{
		Seq:                 row.seq,
		CommandExitCode:     int(row.exitCode.Int64),
		ExecutionDurationMs: row.durationMs.Int64,
		ExecutionID:         row.executionID.String,
	}
	if err := decryptEventContentWith(v, event, row.contentText, row.commandRaw, row.stdout, row.stderr); err != nil {
		if errors.Is(err, constants.ErrAuditStoreVaultLocked) {
			return nil, err
		}
		return nil, fmt.Errorf("%w at seq %d: %w", constants.ErrAuditChainContentMismatch, row.seq, err)
	}
	recomputed, err := computeEventContentDigest(v, event)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(recomputed), []byte(row.digest)) != 1 {
		return nil, fmt.Errorf("%w at seq %d: content digest mismatch", constants.ErrAuditChainContentMismatch, row.seq)
	}
	return event, nil
}

// PruneChainedAuditEvents checkpoints and deletes chained audit events older than cutoff.
func PruneChainedAuditEvents(ctx context.Context, db *sqliteutil.DB, logger *slog.Logger, encryptionVault *vault.Vault, cutoff string) error {
	if db == nil {
		return nil
	}

	return db.ExecInImmediateTxWithRetry(ctx, func(conn *sql.Conn) error {
		var pruneCount int64
		err := conn.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM events
			WHERE timestamp < ?
			AND type != ?
			AND seq IS NOT NULL
		`, cutoff, string(constants.EventPlatformAuditChainCheckpointed)).Scan(&pruneCount)
		if err != nil {
			return fmt.Errorf("audit chain prune: count: %w", err)
		}
		if pruneCount == 0 {
			return nil
		}

		var (
			prunedThroughSeq  int64
			prunedThroughHash string
		)
		err = conn.QueryRowContext(ctx, `
			SELECT seq, hash FROM events
			WHERE timestamp < ?
			AND type != ?
			AND seq IS NOT NULL
			ORDER BY seq DESC
			LIMIT 1
		`, cutoff, string(constants.EventPlatformAuditChainCheckpointed)).Scan(&prunedThroughSeq, &prunedThroughHash)
		if err != nil {
			return fmt.Errorf("audit chain prune: select tail: %w", err)
		}

		if _, err := appendAuditChainCheckpoint(ctx, conn, encryptionVault, prunedThroughSeq, prunedThroughHash, pruneCount); err != nil {
			return err
		}

		result, err := conn.ExecContext(ctx, `
			DELETE FROM events
			WHERE timestamp < ?
			AND type != ?
		`, cutoff, string(constants.EventPlatformAuditChainCheckpointed))
		if err != nil {
			return fmt.Errorf("audit chain prune: delete: %w", err)
		}
		rowsDeleted, _ := result.RowsAffected()
		if logger != nil && rowsDeleted > 0 {
			logger.Info("Pruned chained audit events",
				"rows_deleted", rowsDeleted,
				"pruned_through_seq", prunedThroughSeq,
				"pruned_through_hash", prunedThroughHash)
		}
		return nil
	})
}

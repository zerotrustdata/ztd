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
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

// OperationalEvidenceQuery selects one consistent, bounded database snapshot.
type OperationalEvidenceQuery struct {
	WindowStart time.Time
	WindowEnd   time.Time
	MaxRows     int
}

// OperationalEventReceiptSource preserves the stored canonical event receipt body and its
// searchable selection fields without opening a writable audit store.
type OperationalEventReceiptSource struct {
	TransactionID string
	ExecutedAt    time.Time
	Body          []byte
}

// OperationalCommitmentSource preserves the stored commitment attestation and
// its ledger ordering metadata without rebuilding or mutating the ledger.
type OperationalCommitmentSource struct {
	Sequence      int64
	TransactionID string
	CommittedAt   time.Time
	Body          []byte
}

// OperationalAuditChainSource preserves one chained audit event and its linkage
// metadata for offline compliance verification.
type OperationalAuditChainSource struct {
	Seq               int64
	PrevHash          string
	Hash              string
	EventType         string
	OperatorSessionID string
	Timestamp         time.Time
	ContentDigest     string
	// ContentSHA256 is the plain SHA-256 of the decrypted event receipt body. The reader
	// sets it only after the row's keyed content digest verified.
	ContentSHA256 string
	TransactionID string
}

// OperationalEvidenceSnapshot is the bounded result of one read-only SQLite
// snapshot. A nil body records a retained row whose canonical source body is
// unavailable rather than silently dropping that row.
type OperationalEvidenceSnapshot struct {
	EventReceipts []OperationalEventReceiptSource
	Commitments   []OperationalCommitmentSource
	AuditChain    []OperationalAuditChainSource
}

// ReadOnlyOperationalEvidence reads the Gateway or Operator-local audit
// database without schema bootstrap, migration, pruning, or writes.
type ReadOnlyOperationalEvidence struct {
	db *sqliteutil.DB
	// vault decrypts the event receipt and commitment bodies, which are encrypted at rest.
	// A nil or locked vault makes every Snapshot that reads a body fail closed.
	vault *vault.Vault
}

// OpenReadOnlyOperationalEvidence opens an existing database in SQLite
// read-only mode. It does not create an absent database or runtime directory.
func OpenReadOnlyOperationalEvidence(dbPath string, encryptionVault *vault.Vault, logger *slog.Logger) (*ReadOnlyOperationalEvidence, error) {
	db, err := sqliteutil.OpenReadOnlyDB(sqliteutil.DefaultDBConfig(dbPath), logger)
	if err != nil {
		return nil, fmt.Errorf("operational evidence: open read-only database: %w", err)
	}
	return &ReadOnlyOperationalEvidence{db: db, vault: encryptionVault}, nil
}

// Close releases the read-only database connection.
func (r *ReadOnlyOperationalEvidence) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// LatestCommitmentAt returns the commit time of the newest ledger commitment.
// It fails with constants.ErrNotFound when the ledger holds no commitment, so a
// caller cannot derive an assessment window from an empty runtime.
func (r *ReadOnlyOperationalEvidence) LatestCommitmentAt(ctx context.Context) (time.Time, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.db == nil {
		return time.Time{}, constants.ErrReportStoreUnavailable
	}
	var committedAtMs sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(committed_at_unix_ms) FROM commitment_ledger`).Scan(&committedAtMs); err != nil {
		return time.Time{}, fmt.Errorf("operational evidence: query latest commitment: %w", err)
	}
	if !committedAtMs.Valid {
		return time.Time{}, fmt.Errorf("%w: commitment ledger is empty", constants.ErrNotFound)
	}
	return time.UnixMilli(committedAtMs.Int64).UTC(), nil
}

// Snapshot reads event receipts and commitments from one read-only transaction. The
// query has no implicit pagination; exceeding MaxRows fails rather than
// silently exporting a truncated population.
func (r *ReadOnlyOperationalEvidence) Snapshot(ctx context.Context, query OperationalEvidenceQuery) (snapshot *OperationalEvidenceSnapshot, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if r == nil || r.db == nil {
		return nil, constants.ErrReportStoreUnavailable
	}
	if query.WindowStart.IsZero() || query.WindowEnd.IsZero() || query.WindowEnd.Before(query.WindowStart) || query.MaxRows <= 0 {
		return nil, fmt.Errorf("%w: operational evidence window and positive row bound are required", constants.ErrValidationFailed)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("operational evidence: begin read-only snapshot: %w", err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) && returnErr == nil {
			snapshot = nil
			returnErr = fmt.Errorf("operational evidence: close read-only snapshot: %w", rollbackErr)
		}
	}()

	snapshot = &OperationalEvidenceSnapshot{}
	if err := readOperationalEventReceipts(ctx, tx, r.vault, query, snapshot); err != nil {
		return nil, err
	}
	if err := readOperationalCommitments(ctx, tx, r.vault, query, snapshot); err != nil {
		return nil, err
	}
	if err := readOperationalAuditChain(ctx, tx, r.vault, query, snapshot); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func readOperationalEventReceipts(ctx context.Context, tx *sql.Tx, encryptionVault *vault.Vault, query OperationalEvidenceQuery, snapshot *OperationalEvidenceSnapshot) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT transaction_id, event_receipt_executed_at_ms, seq, content_text
		FROM events
		WHERE event_receipt_status IS NOT NULL
		  AND event_receipt_executed_at_ms >= ? AND event_receipt_executed_at_ms <= ?
		ORDER BY event_receipt_executed_at_ms ASC, transaction_id ASC
	`, query.WindowStart.UnixMilli(), query.WindowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("operational evidence: query event_receipts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventReceipt OperationalEventReceiptSource
		var executedAtMs int64
		var seq sql.NullInt64
		var body []byte
		if err := rows.Scan(&eventReceipt.TransactionID, &executedAtMs, &seq, &body); err != nil {
			return fmt.Errorf("operational evidence: scan event receipt: %w", err)
		}
		eventReceipt.ExecutedAt = time.UnixMilli(executedAtMs).UTC()
		if len(body) > 0 {
			if !seq.Valid {
				return fmt.Errorf("operational evidence: event receipt %s has no chain sequence", eventReceipt.TransactionID)
			}
			decrypted, err := decryptAuditContent(encryptionVault, auditColumnEventContentText, auditEventRowID(seq.Int64), body)
			if err != nil {
				return fmt.Errorf("operational evidence: decrypt event receipt: %w", err)
			}
			eventReceipt.Body = []byte(decrypted)
		}
		snapshot.EventReceipts = append(snapshot.EventReceipts, eventReceipt)
		if len(snapshot.EventReceipts) > query.MaxRows {
			return fmt.Errorf("%w: event receipt population exceeds bound %d", constants.ErrEvidenceArtifactTooLarge, query.MaxRows)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("operational evidence: iterate event_receipts: %w", err)
	}
	return nil
}

func readOperationalCommitments(ctx context.Context, tx *sql.Tx, encryptionVault *vault.Vault, query OperationalEvidenceQuery, snapshot *OperationalEvidenceSnapshot) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, transaction_id, committed_at_unix_ms, attestation_json, hash
		FROM commitment_ledger
		WHERE committed_at_unix_ms >= ? AND committed_at_unix_ms <= ?
		ORDER BY id ASC
	`, query.WindowStart.UnixMilli(), query.WindowEnd.UnixMilli())
	if err != nil {
		return fmt.Errorf("operational evidence: query commitments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var commitment OperationalCommitmentSource
		var committedAtMs int64
		var attestation []byte
		var commitmentHash string
		if err := rows.Scan(&commitment.Sequence, &commitment.TransactionID, &committedAtMs, &attestation, &commitmentHash); err != nil {
			return fmt.Errorf("operational evidence: scan commitment: %w", err)
		}
		decrypted, err := decryptAuditContent(encryptionVault, CommitmentColumnAttestationJSON, commitmentHash, attestation)
		if err != nil {
			return fmt.Errorf("operational evidence: decrypt attestation_json: %w", err)
		}
		if decrypted != "" {
			commitment.Body = []byte(decrypted)
		}
		commitment.CommittedAt = time.UnixMilli(committedAtMs).UTC()
		snapshot.Commitments = append(snapshot.Commitments, commitment)
		if len(snapshot.Commitments) > query.MaxRows {
			return fmt.Errorf("%w: commitment population exceeds bound %d", constants.ErrEvidenceArtifactTooLarge, query.MaxRows)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("operational evidence: iterate commitments: %w", err)
	}
	return nil
}

// readOperationalAuditChain exports the contiguous chain segment that spans every
// chained event receipt fact in the window. Every chained event between the first and
// last of those facts is included whatever its type, so the segment has no
// sequence gap and its prev_hash linkage verifies offline.
func readOperationalAuditChain(ctx context.Context, tx *sql.Tx, encryptionVault *vault.Vault, query OperationalEvidenceQuery, snapshot *OperationalEvidenceSnapshot) error {
	// events.timestamp is stored in the fixed-width timesvc format, so the bounds must
	// use it too: a variable-width RFC3339Nano bound compares wrong as a string and
	// admits events up to a millisecond past the window end.
	windowStart := timesvc.FormatTimestamp(query.WindowStart)
	windowEnd := timesvc.FormatTimestamp(query.WindowEnd)
	rows, err := tx.QueryContext(ctx, `
		WITH bounds AS (
			SELECT MIN(seq) AS first_seq, MAX(seq) AS last_seq
			FROM events
			WHERE seq IS NOT NULL
			  AND type = ?
			  AND timestamp >= ? AND timestamp <= ?
		)
		SELECT prev_hash, hash, type, operator_session_id, timestamp, transaction_id, `+auditEventContentColumns+`
		FROM events, bounds
		WHERE seq IS NOT NULL
		  AND seq >= bounds.first_seq AND seq <= bounds.last_seq
		ORDER BY seq ASC
	`, string(constants.EventOperatorEventReceiptRecorded), windowStart, windowEnd)
	if err != nil {
		return fmt.Errorf("operational evidence: query audit chain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry OperationalAuditChainSource
		var sessionID sql.NullString
		var timestampStr string
		var transactionID sql.NullString
		var content auditEventContentRow
		targets := append([]any{&entry.PrevHash, &entry.Hash, &entry.EventType, &sessionID, &timestampStr, &transactionID}, content.scanTargets()...)
		if err := rows.Scan(targets...); err != nil {
			return fmt.Errorf("operational evidence: scan audit chain entry: %w", err)
		}
		entry.Seq = content.seq
		entry.ContentDigest = content.digest
		// The export is read from the encrypted store: verify the entry's content under the
		// vault before vouching for it, and fail closed on a locked vault or a mismatch.
		event, err := verifyAuditEventContent(encryptionVault, content)
		if err != nil {
			return fmt.Errorf("operational evidence: verify audit chain content: %w", err)
		}
		// Only event receipt facts carry a plain content hash: an importer matches it to the
		// exported event receipt body. Other chained content is never exported, so its hash is not.
		if entry.EventType == string(constants.EventOperatorEventReceiptRecorded) && event.ContentText != "" {
			sum := sha256.Sum256([]byte(event.ContentText))
			entry.ContentSHA256 = hex.EncodeToString(sum[:])
		}
		if sessionID.Valid {
			entry.OperatorSessionID = sessionID.String
		}
		if transactionID.Valid {
			entry.TransactionID = transactionID.String
		}
		parsedAt, err := timesvc.ParseTimestamp(timestampStr)
		if err != nil {
			return fmt.Errorf("operational evidence: parse audit chain timestamp: %w", err)
		}
		entry.Timestamp = parsedAt.UTC()
		snapshot.AuditChain = append(snapshot.AuditChain, entry)
		if len(snapshot.AuditChain) > query.MaxRows {
			return fmt.Errorf("%w: audit chain population exceeds bound %d", constants.ErrEvidenceArtifactTooLarge, query.MaxRows)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("operational evidence: iterate audit chain: %w", err)
	}
	return nil
}

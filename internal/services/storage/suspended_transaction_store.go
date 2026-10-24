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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/timesvc"
)

//go:generate mockery --name SuspendedTransactionStore --output ./mocks --dir .

// SuspendedTransactionStore defines the interface for L3 approval workflow storage.
// This service stores transactions awaiting human approval.
//
// All methods that return errors must wrap errors with context using
// fmt.Errorf("suspended_transaction_store: action: %w", err) to provide clear error attribution.
type SuspendedTransactionStore interface {
	// StoreSuspendedTransaction stores a transaction awaiting L3 approval.
	// Returns an error if storage fails, wrapping the underlying error with context.
	StoreSuspendedTransaction(ctx context.Context, tx *models.SuspendedTransaction) error

	// GetSuspendedTransaction retrieves a suspended transaction by hash.
	// Returns (nil, false) if not found or expired.
	// Returns an error if retrieval fails, wrapping the underlying error with context.
	GetSuspendedTransaction(ctx context.Context, txHash string) (*models.SuspendedTransaction, bool, error)

	// ListSuspendedTransactions retrieves all non-expired suspended transactions.
	// Optionally filters by user_id if provided.
	// Returns an error if retrieval fails, wrapping the underlying error with context.
	ListSuspendedTransactions(ctx context.Context, userID string) ([]*models.SuspendedTransaction, error)

	// DeleteSuspendedTransaction removes a suspended transaction after approval/rejection.
	// Returns an error if deletion fails, wrapping the underlying error with context.
	DeleteSuspendedTransaction(ctx context.Context, txHash string) error

	// DeleteExpiredSuspendedTransactions removes exactly the given transactions,
	// each only while it is still expired. The caller passes the rows whose expiry
	// it has already audited, so a row that expires after the caller listed it is
	// never deleted unaudited.
	// Returns the count of deleted transactions.
	// Returns an error if deletion fails, wrapping the underlying error with context.
	DeleteExpiredSuspendedTransactions(ctx context.Context, txs []*models.SuspendedTransaction) (int64, error)

	// GetExpiredSuspendedTransactions retrieves expired suspended transactions for the
	// expiry sweep and for reports. It reads only the plaintext metadata columns
	// (identifiers, tool name, session, timestamps and claim state): the returned
	// transactions carry no Envelope and no ToolArguments, so a locked vault or one
	// undecryptable row cannot halt expiry.
	// Returns an error if retrieval fails, wrapping the underlying error with context.
	GetExpiredSuspendedTransactions(ctx context.Context) ([]*models.SuspendedTransaction, error)

	// ClaimSuspendedTransaction atomically marks a live, unclaimed suspended transaction as
	// being executed. It returns constants.ErrSuspendedTransactionNotFound when the row is
	// absent or expired and constants.ErrSuspendedTransactionClaimed when it is already
	// claimed. A claimed row is never recorded as expired by the sweep: its execution was
	// attempted, so its lifecycle is owned by the execution record.
	ClaimSuspendedTransaction(ctx context.Context, txHash string) error

	// ReleaseSuspendedTransaction clears the claim after an execution that did not run, so
	// the approval can be retried. A row that was already deleted is not an error.
	ReleaseSuspendedTransaction(ctx context.Context, txHash string) error
}

// SuspendedTransactionService provides storage for L3 approval workflow transactions.
// It stores transactions awaiting human approval in the runtime's canonical ztd.db.
// The envelope and tool_arguments columns hold content and are encrypted with the
// runtime vault through encryptAuditContent; a nil or locked vault fails the write
// and the read with constants.ErrAuditStoreVaultLocked. Identifiers, hashes,
// timestamps and claim state stay plaintext.
//
// The service does not own the database handle and has no background pruner:
// expired rows are removed by the Gateway maintenance sweep, which audits each
// expiry before calling DeleteExpiredSuspendedTransactions.
type SuspendedTransactionService struct {
	db     *sqliteutil.DB
	vault  *vault.Vault
	logger *slog.Logger
}

// Ensure SuspendedTransactionService implements SuspendedTransactionStore.
var _ SuspendedTransactionStore = (*SuspendedTransactionService)(nil)

// NewSuspendedTransactionService creates the suspended transaction store over the
// shared canonical database and creates its table. A nil database or nil vault
// fails construction.
func NewSuspendedTransactionService(db *sqliteutil.DB, v *vault.Vault, logger *slog.Logger) (*SuspendedTransactionService, error) {
	if db == nil {
		return nil, fmt.Errorf("suspended_transaction_store: database: %w", constants.ErrMissingRequiredField)
	}
	if v == nil {
		return nil, fmt.Errorf("suspended_transaction_store: %w", constants.ErrAuditStoreEncryptionVaultRequired)
	}
	if logger == nil {
		logger = slog.Default()
	}

	if _, err := db.Exec(suspendedTransactionSchema); err != nil {
		return nil, fmt.Errorf("suspended_transaction_store: initialize schema: %w", err)
	}

	return &SuspendedTransactionService{db: db, vault: v, logger: logger}, nil
}

// Content columns of suspended_transactions, bound with the transaction hash into the AEAD data.
const (
	suspendedColumnEnvelope      = "suspended_transactions.envelope"
	suspendedColumnToolArguments = "suspended_transactions.tool_arguments"
)

// suspendedTransactionSchema is the whole schema of the suspended transaction table.
// envelope and tool_arguments are vault ciphertext. operator_session_id is the
// plaintext Operator session the envelope targets (GovernanceEnvelope.OperatorSessionId):
// it is the audit-chain owner of the approval lifecycle events, so the expiry sweep
// reads it without decrypting the envelope.
const suspendedTransactionSchema = `
CREATE TABLE IF NOT EXISTS suspended_transactions (
	transaction_hash TEXT PRIMARY KEY,
	envelope BLOB NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	tool_name TEXT,
	tool_arguments BLOB,
	user_id TEXT,
	operator_id TEXT,
	operator_session_id TEXT NOT NULL,
	submitter_cli_session_id TEXT,
	expected_cert_fingerprint TEXT,
	claimed_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_suspended_expires_at ON suspended_transactions(expires_at);
`

// suspendedMetadataColumns is the plaintext select list: every column except the two
// vault-encrypted content columns. scanSuspendedMetadata scans it in this order.
const suspendedMetadataColumns = "transaction_hash, created_at, expires_at, tool_name, user_id, operator_id, operator_session_id, submitter_cli_session_id, expected_cert_fingerprint, claimed_at"

// suspendedTransactionColumns is the full select list for reads that return content:
// the metadata columns followed by the encrypted envelope and tool_arguments.
const suspendedTransactionColumns = suspendedMetadataColumns + ", envelope, tool_arguments"

// suspendedTxRow is one raw row: content columns, when selected, are still ciphertext.
type suspendedTxRow struct {
	txHash                  string
	envelope                []byte
	createdAtStr            sql.NullString
	expiresAtStr            sql.NullString
	toolName                sql.NullString
	toolArguments           []byte
	userID                  sql.NullString
	operatorID              sql.NullString
	operatorSessionID       string
	submitterCLISessionID   sql.NullString
	expectedCertFingerprint sql.NullString
	claimedAtStr            sql.NullString
}

// rowScanner is satisfied by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanSuspendedMetadata scans suspendedMetadataColumns, without the content columns.
func scanSuspendedMetadata(r rowScanner, extra ...any) (suspendedTxRow, error) {
	var row suspendedTxRow
	dest := []any{&row.txHash, &row.createdAtStr, &row.expiresAtStr, &row.toolName, &row.userID, &row.operatorID, &row.operatorSessionID, &row.submitterCLISessionID, &row.expectedCertFingerprint, &row.claimedAtStr}
	err := r.Scan(append(dest, extra...)...)
	return row, err
}

// scanSuspendedRow scans suspendedTransactionColumns: the metadata followed by the encrypted content.
func scanSuspendedRow(r rowScanner) (suspendedTxRow, error) {
	var envelope, toolArguments []byte
	row, err := scanSuspendedMetadata(r, &envelope, &toolArguments)
	row.envelope = envelope
	row.toolArguments = toolArguments
	return row, err
}

// metadataTransaction builds the model from the plaintext columns only.
func (row suspendedTxRow) metadataTransaction() *models.SuspendedTransaction {
	createdAt, _ := timesvc.ParseTimestamp(row.createdAtStr.String)
	expiresAt, _ := timesvc.ParseTimestamp(row.expiresAtStr.String)

	var claimedAt *time.Time
	if row.claimedAtStr.Valid {
		ts, _ := timesvc.ParseTimestamp(row.claimedAtStr.String)
		claimedAt = &ts
	}

	return &models.SuspendedTransaction{
		TransactionHash:         row.txHash,
		CreatedAt:               createdAt,
		ExpiresAt:               expiresAt,
		ToolName:                row.toolName.String,
		UserID:                  row.userID.String,
		OperatorID:              row.operatorID.String,
		OperatorSessionID:       row.operatorSessionID,
		SubmitterCLISessionID:   row.submitterCLISessionID.String,
		ExpectedCertFingerprint: row.expectedCertFingerprint.String,
		ClaimedAt:               claimedAt,
	}
}

// toTransaction decrypts the content columns and builds the model. A nil or locked
// vault returns constants.ErrAuditStoreVaultLocked.
func (sts *SuspendedTransactionService) toTransaction(row suspendedTxRow) (*models.SuspendedTransaction, error) {
	envelope, err := decryptAuditContent(sts.vault, suspendedColumnEnvelope, row.txHash, row.envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt envelope: %w", err)
	}
	toolArgs, err := decryptAuditContent(sts.vault, suspendedColumnToolArguments, row.txHash, row.toolArguments)
	if err != nil {
		return nil, fmt.Errorf("decrypt tool_arguments: %w", err)
	}

	tx := row.metadataTransaction()
	tx.Envelope = []byte(envelope)
	if toolArgs != "" {
		tx.ToolArguments = []byte(toolArgs)
	}
	return tx, nil
}

// queryTransactions runs a read and decrypts every row. Any decrypt failure fails
// the whole read; no partial or ciphertext result is returned.
func (sts *SuspendedTransactionService) queryTransactions(ctx context.Context, query string, args []interface{}) ([]*models.SuspendedTransaction, error) {
	rows, err := sqliteutil.MaterializeRows(ctx, sts.db, query, args, func(r *sql.Rows) (suspendedTxRow, error) {
		return scanSuspendedRow(r)
	})
	if err != nil {
		return nil, err
	}

	var transactions []*models.SuspendedTransaction
	for _, row := range rows {
		tx, err := sts.toTransaction(row)
		if err != nil {
			return nil, err
		}
		transactions = append(transactions, tx)
	}
	return transactions, nil
}

// StoreSuspendedTransaction stores a transaction awaiting L3 approval. The envelope
// and tool arguments are encrypted with the runtime vault; a nil or locked vault
// fails the write with constants.ErrAuditStoreVaultLocked.
func (sts *SuspendedTransactionService) StoreSuspendedTransaction(ctx context.Context, tx *models.SuspendedTransaction) error {
	if sts == nil || sts.db == nil {
		return fmt.Errorf("suspended_transaction_store: store transaction: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	if tx == nil || len(tx.Envelope) == 0 {
		return fmt.Errorf("suspended_transaction_store: store transaction: envelope: %w", constants.ErrMissingRequiredField)
	}
	if tx.OperatorSessionID == "" {
		return fmt.Errorf("suspended_transaction_store: store transaction: operator_session_id: %w", constants.ErrMissingRequiredField)
	}

	encEnvelope, err := encryptAuditContent(sts.vault, suspendedColumnEnvelope, tx.TransactionHash, string(tx.Envelope))
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: store transaction: encrypt envelope: %w", err)
	}
	encToolArgs, err := encryptAuditContent(sts.vault, suspendedColumnToolArguments, tx.TransactionHash, string(tx.ToolArguments))
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: store transaction: encrypt tool_arguments: %w", err)
	}

	query := `
	INSERT INTO suspended_transactions (
		transaction_hash, envelope, created_at, expires_at,
		tool_name, tool_arguments, user_id, operator_id, operator_session_id, submitter_cli_session_id,
		expected_cert_fingerprint
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(transaction_hash) DO UPDATE SET
		envelope = excluded.envelope,
		expires_at = excluded.expires_at,
		expected_cert_fingerprint = excluded.expected_cert_fingerprint
	`

	_, err = sts.db.ExecWithRetry(ctx,
		query,
		tx.TransactionHash,
		encEnvelope,
		timesvc.FormatTimestamp(tx.CreatedAt),
		timesvc.FormatTimestamp(tx.ExpiresAt),
		tx.ToolName,
		encToolArgs,
		tx.UserID,
		tx.OperatorID,
		tx.OperatorSessionID,
		tx.SubmitterCLISessionID,
		tx.ExpectedCertFingerprint,
	)
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: store transaction: %w", err)
	}
	return nil
}

// GetSuspendedTransaction retrieves a suspended transaction by hash.
// Returns (nil, false, nil) if not found or expired.
func (sts *SuspendedTransactionService) GetSuspendedTransaction(ctx context.Context, txHash string) (*models.SuspendedTransaction, bool, error) {
	if sts == nil || sts.db == nil {
		return nil, false, fmt.Errorf("suspended_transaction_store: get transaction: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	row, err := scanSuspendedRow(sts.db.QueryRowWithRetry(ctx,
		"SELECT "+suspendedTransactionColumns+" FROM suspended_transactions WHERE transaction_hash = ? AND expires_at > ?",
		txHash, timesvc.NowTimestamp(),
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		sts.logger.Error("Failed to query suspended transaction", "tx_hash", txHash, string(constants.ConnectionStateError), err)
		return nil, false, fmt.Errorf("suspended_transaction_store: get transaction: %w", err)
	}

	tx, err := sts.toTransaction(row)
	if err != nil {
		return nil, false, fmt.Errorf("suspended_transaction_store: get transaction: %w", err)
	}
	return tx, true, nil
}

// ListSuspendedTransactions retrieves all non-expired suspended transactions.
// Optionally filters by user_id if provided.
func (sts *SuspendedTransactionService) ListSuspendedTransactions(ctx context.Context, userID string) ([]*models.SuspendedTransaction, error) {
	if sts == nil || sts.db == nil {
		return nil, fmt.Errorf("suspended_transaction_store: list transactions: %w", constants.ErrSuspendedStoreNotInitialized)
	}

	query := "SELECT " + suspendedTransactionColumns + " FROM suspended_transactions WHERE expires_at > ? ORDER BY created_at DESC"
	args := []interface{}{timesvc.NowTimestamp()}
	if userID != "" {
		query = "SELECT " + suspendedTransactionColumns + " FROM suspended_transactions WHERE user_id = ? AND expires_at > ? ORDER BY created_at DESC"
		args = []interface{}{userID, timesvc.NowTimestamp()}
	}

	transactions, err := sts.queryTransactions(ctx, query, args)
	if err != nil {
		return nil, fmt.Errorf("suspended_transaction_store: list transactions: %w", err)
	}
	return transactions, nil
}

// DeleteSuspendedTransaction removes a suspended transaction after approval/rejection.
func (sts *SuspendedTransactionService) DeleteSuspendedTransaction(ctx context.Context, txHash string) error {
	if sts == nil || sts.db == nil {
		return fmt.Errorf("suspended_transaction_store: delete transaction: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	_, err := sts.db.ExecWithRetry(ctx, "DELETE FROM suspended_transactions WHERE transaction_hash = ?", txHash)
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: delete transaction: %w", err)
	}
	return nil
}

// DeleteExpiredSuspendedTransactions removes the given transactions while they
// are still expired. Returns the count of deleted transactions.
func (sts *SuspendedTransactionService) DeleteExpiredSuspendedTransactions(ctx context.Context, txs []*models.SuspendedTransaction) (int64, error) {
	if sts == nil || sts.db == nil {
		return 0, fmt.Errorf("suspended_transaction_store: delete expired: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	if len(txs) == 0 {
		return 0, nil
	}
	args := make([]interface{}, 0, len(txs)+1)
	args = append(args, timesvc.NowTimestamp())
	for _, tx := range txs {
		args = append(args, tx.TransactionHash)
	}
	query := "DELETE FROM suspended_transactions WHERE expires_at < ? AND transaction_hash IN (?" + strings.Repeat(", ?", len(txs)-1) + ")"
	result, err := sts.db.ExecWithRetry(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("suspended_transaction_store: delete expired: %w", err)
	}
	return result.RowsAffected()
}

// GetExpiredSuspendedTransactions retrieves expired suspended transactions for the
// expiry sweep and for reports. Only the plaintext metadata columns are read, so the
// returned transactions have no Envelope and no ToolArguments and the read never
// touches the vault: a locked vault or an undecryptable row cannot block expiry.
func (sts *SuspendedTransactionService) GetExpiredSuspendedTransactions(ctx context.Context) ([]*models.SuspendedTransaction, error) {
	if sts == nil || sts.db == nil {
		return nil, fmt.Errorf("suspended_transaction_store: get expired transactions: %w", constants.ErrSuspendedStoreNotInitialized)
	}

	query := "SELECT " + suspendedMetadataColumns + " FROM suspended_transactions WHERE expires_at < ? ORDER BY expires_at ASC"
	rows, err := sqliteutil.MaterializeRows(ctx, sts.db, query, []interface{}{timesvc.NowTimestamp()}, func(r *sql.Rows) (suspendedTxRow, error) {
		return scanSuspendedMetadata(r)
	})
	if err != nil {
		return nil, fmt.Errorf("suspended_transaction_store: get expired transactions: %w", err)
	}

	var transactions []*models.SuspendedTransaction
	for _, row := range rows {
		transactions = append(transactions, row.metadataTransaction())
	}
	return transactions, nil
}

// ClaimSuspendedTransaction atomically marks a live, unclaimed transaction as being
// executed. The UPDATE is the claim: exactly one caller changes the row.
func (sts *SuspendedTransactionService) ClaimSuspendedTransaction(ctx context.Context, txHash string) error {
	if sts == nil || sts.db == nil {
		return fmt.Errorf("suspended_transaction_store: claim transaction: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	result, err := sts.db.ExecWithRetry(ctx,
		"UPDATE suspended_transactions SET claimed_at = ? WHERE transaction_hash = ? AND claimed_at IS NULL AND expires_at > ?",
		timesvc.NowTimestamp(), txHash, timesvc.NowTimestamp())
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: claim transaction: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: claim transaction: rows affected: %w", err)
	}
	if affected == 1 {
		return nil
	}

	var claimedAt sql.NullString
	err = sts.db.QueryRowWithRetry(ctx, "SELECT claimed_at FROM suspended_transactions WHERE transaction_hash = ? AND expires_at > ?", txHash, timesvc.NowTimestamp()).Scan(&claimedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("suspended_transaction_store: claim transaction: %w", constants.ErrSuspendedTransactionNotFound)
	}
	if err != nil {
		return fmt.Errorf("suspended_transaction_store: claim transaction: %w", err)
	}
	return fmt.Errorf("suspended_transaction_store: claim transaction: %w", constants.ErrSuspendedTransactionClaimed)
}

// ReleaseSuspendedTransaction clears the execution claim so the approval can be retried.
func (sts *SuspendedTransactionService) ReleaseSuspendedTransaction(ctx context.Context, txHash string) error {
	if sts == nil || sts.db == nil {
		return fmt.Errorf("suspended_transaction_store: release transaction: %w", constants.ErrSuspendedStoreNotInitialized)
	}
	if _, err := sts.db.ExecWithRetry(ctx, "UPDATE suspended_transactions SET claimed_at = NULL WHERE transaction_hash = ?", txHash); err != nil {
		return fmt.Errorf("suspended_transaction_store: release transaction: %w", err)
	}
	return nil
}

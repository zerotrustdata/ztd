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
	"slices"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/timesvc"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// eventReceiptSelectColumns is the current event receipt read by every lookup.
// Content columns encrypt to NULL when empty, so they are coalesced to the
// empty string the record's string fields expect. The canonical body is the
// event's content_text, not a second copy.
const eventReceiptSelectColumns = `transaction_id, COALESCE(event_receipt_transaction_hash, ''),
		COALESCE(event_receipt_investigation_id, ''), COALESCE(event_receipt_operator_id, ''),
		COALESCE(event_receipt_operator_session_id, ''),
		COALESCE(event_receipt_requestor_user_id, ''), COALESCE(event_receipt_acting_app_id, ''),
		COALESCE(event_receipt_event_type, ''), COALESCE(event_receipt_action_type, ''),
		COALESCE(event_receipt_target_resource, ''), event_receipt_status,
		COALESCE(event_receipt_result_summary, ''),
		COALESCE(event_receipt_state_root_before, ''), COALESCE(event_receipt_state_root_after, ''),
		COALESCE(event_receipt_executed_at_ms, 0),
		COALESCE(event_receipt_signer_key_id, ''), COALESCE(event_receipt_signature, ''),
		content_text, timestamp, seq`

const eventReceiptClearSQL = `
UPDATE events SET
	event_receipt_transaction_hash = NULL,
	event_receipt_investigation_id = NULL,
	event_receipt_operator_id = NULL,
	event_receipt_operator_session_id = NULL,
	event_receipt_requestor_user_id = NULL,
	event_receipt_acting_app_id = NULL,
	event_receipt_event_type = NULL,
	event_receipt_action_type = NULL,
	event_receipt_target_resource = NULL,
	event_receipt_status = NULL,
	event_receipt_result_summary = NULL,
	event_receipt_state_root_before = NULL,
	event_receipt_state_root_after = NULL,
	event_receipt_executed_at_ms = NULL,
	event_receipt_signer_key_id = NULL,
	event_receipt_signature = NULL
WHERE transaction_id = ? AND event_receipt_status IS NOT NULL AND seq != ?`

func eventReceiptInsertArgs(v *vault.Vault, rowID string, receipt *EventReceiptColumns) ([]any, error) {
	if receipt == nil {
		return []any{nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}, nil
	}
	target, err := encryptAuditContent(v, auditColumnEventReceiptTarget, rowID, receipt.TargetResource)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptContentFailed, err)
	}
	summary, err := encryptAuditContent(v, auditColumnEventReceiptSummary, rowID, receipt.ResultSummary)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreEncryptContentFailed, err)
	}
	return []any{
		receipt.TransactionHash,
		receipt.InvestigationID,
		receipt.OperatorID,
		receipt.OperatorSessionID,
		receipt.RequestorUserID,
		receipt.ActingAppID,
		receipt.EventType,
		receipt.ActionType,
		target,
		int64(receipt.Status),
		summary,
		receipt.StateRootBefore,
		receipt.StateRootAfter,
		receipt.ExecutedAtMs,
		receipt.SignerKeyID,
		receipt.Signature,
	}, nil
}

func eventReceiptColumnsFromRecord(record *models.EventReceiptRecord) *EventReceiptColumns {
	if record == nil {
		return nil
	}
	executedAt := record.ExecutedAt
	if executedAt.IsZero() {
		executedAt = record.Timestamp
	}
	return &EventReceiptColumns{
		TransactionHash:   record.TransactionHash,
		InvestigationID:   record.InvestigationID,
		OperatorID:        record.OperatorID,
		OperatorSessionID: record.OperatorSessionID,
		RequestorUserID:   record.RequestorUserID,
		ActingAppID:       record.ActingAppID,
		EventType:         string(record.EventType),
		ActionType:        string(record.ActionType),
		TargetResource:    record.TargetResource,
		Status:            record.Status,
		ResultSummary:     record.ResultSummary,
		StateRootBefore:   record.StateRootBefore,
		StateRootAfter:    record.StateRootAfter,
		ExecutedAtMs:      executedAt.UnixMilli(),
		SignerKeyID:       record.SignerKeyID,
		Signature:         record.Signature,
	}
}

func eventReceiptStatusNonTerminal(status operatorv1.ExecutionStatus) bool {
	return status == operatorv1.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED ||
		status == operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
}

// eventReceiptMovesForward reports whether next replaces the current event
// receipt. A terminal receipt is never replaced by a later non-terminal stage.
func eventReceiptMovesForward(prev, next operatorv1.ExecutionStatus) bool {
	if !eventReceiptStatusNonTerminal(next) {
		return true
	}
	return eventReceiptStatusNonTerminal(prev)
}

func mergeForwardEventReceipt(prev, next *models.EventReceiptRecord) *models.EventReceiptRecord {
	merged := *next
	merged.TransactionHash = prev.TransactionHash
	merged.OperatorID = prev.OperatorID
	merged.OperatorSessionID = prev.OperatorSessionID
	merged.RequestorUserID = prev.RequestorUserID
	merged.ActingAppID = prev.ActingAppID
	merged.EventType = prev.EventType
	merged.ActionType = prev.ActionType
	merged.TargetResource = prev.TargetResource
	merged.StateRootBefore = prev.StateRootBefore
	merged.SignerKeyID = prev.SignerKeyID
	return &merged
}

// RecordEventReceipt appends a chained operator.receipt.recorded fact and, when
// the stage moves the projection forward, stores that event receipt on the new
// event. The canonical body is the event's content_text.
func (ass *SQLAuditStore) RecordEventReceipt(record *models.EventReceiptRecord) error {
	return ass.recordActionReceipt(record, nil)
}

// RecordEventReceiptWithCommitment records the event receipt exactly as
// RecordEventReceipt does and appends the commitment built over the ledger
// head in the same write transaction: both commit or neither does.
func (ass *SQLAuditStore) RecordEventReceiptWithCommitment(record *models.EventReceiptRecord, build CommitmentBuilder) error {
	if build == nil {
		return constants.ErrAuditStoreRecordReceiptFailed
	}
	return ass.recordActionReceipt(record, build)
}

func (ass *SQLAuditStore) recordActionReceipt(record *models.EventReceiptRecord, build CommitmentBuilder) error {
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

	var commitment *CommitmentRow
	err = ass.db.ExecInImmediateTxWithRetry(context.Background(), func(conn *sql.Conn) error {
		if record.OperatorSessionID != "" {
			if err := ass.requireExistingSessionConn(conn, chainEvent); err != nil {
				return err
			}
		}

		current, err := ass.loadCurrentEventReceiptConn(conn, record.TransactionID)
		if err != nil {
			return err
		}
		stored := record
		forward := current == nil || eventReceiptMovesForward(current.Status, record.Status)
		if forward && current != nil {
			stored = mergeForwardEventReceipt(current, record)
		}
		if forward {
			prepared.EventReceipt = eventReceiptColumnsFromRecord(stored)
		}

		_, seq, _, err := AppendPreparedAuditEvent(context.Background(), conn, prepared)
		if err != nil {
			return err
		}
		if forward && current != nil {
			if _, err := conn.ExecContext(context.Background(), eventReceiptClearSQL, record.TransactionID, seq); err != nil {
				return fmt.Errorf("audit store: clear previous event receipt: %w", err)
			}
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
	ass.logger.Info("Event receipt recorded",
		"transaction_id", record.TransactionID,
		"status", record.Status)
	return nil
}

func (ass *SQLAuditStore) loadCurrentEventReceiptConn(conn *sql.Conn, transactionID string) (*models.EventReceiptRecord, error) {
	if transactionID == "" {
		return nil, nil
	}
	rows, err := conn.QueryContext(context.Background(), `
		SELECT `+eventReceiptSelectColumns+`
		FROM events
		WHERE transaction_id = ? AND event_receipt_status IS NOT NULL
	`, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var found *models.EventReceiptRecord
	for rows.Next() {
		if found != nil {
			return nil, fmt.Errorf("%w: transaction %s", constants.ErrAuditStoreReceiptCorrelationDuplicate, transactionID)
		}
		record, err := scanEventReceiptRow(rows)
		if err != nil {
			return nil, err
		}
		if err := ass.decryptEventReceipt(record); err != nil {
			return nil, err
		}
		found = record.record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return found, nil
}

type scannedEventReceipt struct {
	record       *models.EventReceiptRecord
	executedAtMs int64
	timestampStr string
	seq          int64
	targetBytes  []byte
	summaryBytes []byte
	receiptBytes []byte
}

func scanEventReceiptRow(scanner interface {
	Scan(dest ...any) error
}) (scannedEventReceipt, error) {
	var row scannedEventReceipt
	row.record = &models.EventReceiptRecord{}
	var status int64
	err := scanner.Scan(
		&row.record.TransactionID, &row.record.TransactionHash, &row.record.InvestigationID,
		&row.record.OperatorID, &row.record.OperatorSessionID,
		&row.record.RequestorUserID, &row.record.ActingAppID, &row.record.EventType,
		&row.record.ActionType, &row.targetBytes, &status, &row.summaryBytes,
		&row.record.StateRootBefore, &row.record.StateRootAfter, &row.executedAtMs,
		&row.record.SignerKeyID, &row.record.Signature, &row.receiptBytes, &row.timestampStr, &row.seq,
	)
	row.record.Status = operatorv1.ExecutionStatus(status)
	return row, err
}

func (ass *SQLAuditStore) decryptEventReceipt(row scannedEventReceipt) error {
	rowID := auditEventRowID(row.seq)
	var err error
	if row.record.TargetResource, err = ass.decryptColumn(auditColumnEventReceiptTarget, rowID, row.targetBytes); err != nil {
		return err
	}
	if row.record.ResultSummary, err = ass.decryptColumn(auditColumnEventReceiptSummary, rowID, row.summaryBytes); err != nil {
		return err
	}
	receiptJSON, err := ass.decryptColumn(auditColumnEventContentText, rowID, row.receiptBytes)
	if err != nil {
		return err
	}
	return hydrateEventReceiptRecord(row.record, row.executedAtMs, row.timestampStr, receiptJSON)
}

func hydrateEventReceiptRecord(r *models.EventReceiptRecord, executedAtMs int64, timestampStr, receiptJSON string) error {
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

// GetEventReceipt retrieves the current event receipt for one transaction.
func (ass *SQLAuditStore) GetEventReceipt(transactionID string) (*models.EventReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}
	records, err := ass.queryEventReceiptRows(`
		SELECT `+eventReceiptSelectColumns+`
		FROM events
		WHERE transaction_id = ? AND event_receipt_status IS NOT NULL
	`, []any{transactionID})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreGetReceiptFailed, err)
	}
	if len(records) == 0 {
		return nil, nil
	}
	if len(records) > 1 {
		return nil, fmt.Errorf("%w: transaction %s", constants.ErrAuditStoreReceiptCorrelationDuplicate, transactionID)
	}
	return records[0], nil
}

func (ass *SQLAuditStore) GetEventReceiptByInvestigationID(
	investigationID string,
	actionType constants.ActionType,
) (*models.EventReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}

	rows, err := sqliteutil.MaterializeRows(context.Background(),
		ass.db,
		`SELECT transaction_id FROM events
		 WHERE event_receipt_investigation_id = ? AND event_receipt_action_type = ? AND event_receipt_status IS NOT NULL
		 ORDER BY timestamp DESC LIMIT 2`,
		[]any{investigationID, actionType},
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
	return ass.GetEventReceipt(rows[0])
}

const auditReadScanBatchSize = 100

func isAuditReadEventReceipt(r *models.EventReceiptRecord) bool {
	if r == nil || r.ActionType != constants.ActionTypeMcpCall {
		return false
	}
	return r.TargetResource == constants.MCPToolAuditReceiptList || r.TargetResource == constants.MCPToolAuditReceiptGet
}

// ListEventReceipts retrieves current event receipts with optional filtering and pagination.
// With ExcludeAuditReads, limit and offset apply after the audit reads are dropped.
func (ass *SQLAuditStore) ListEventReceipts(scope models.AuditScope, limit, offset int) ([]*models.EventReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}
	if limit <= 0 {
		limit = 50
	}
	query, args := eventReceiptScopeQuery(scope)
	if !scope.ExcludeAuditReads {
		return ass.queryEventReceiptRows(query+" ORDER BY timestamp DESC LIMIT ? OFFSET ?", append(args, limit, offset))
	}
	return ass.listEventReceiptsExcludingAuditReads(query, args, limit, offset)
}

// eventReceiptScopeQuery builds the current-event-receipt SELECT for the scope
// fields that SQL can filter. target_resource is encrypted, so audit-read
// exclusion is not a predicate here.
func eventReceiptScopeQuery(scope models.AuditScope) (string, []any) {
	args := []any{}
	predicates := []string{"event_receipt_status IS NOT NULL"}
	if scope.OperatorID != "" {
		predicates = append(predicates, "event_receipt_operator_id = ?")
		args = append(args, scope.OperatorID)
	}
	if scope.OperatorSessionID != "" {
		predicates = append(predicates, "event_receipt_operator_session_id = ?")
		args = append(args, scope.OperatorSessionID)
	}
	if scope.InvestigationID != "" {
		predicates = append(predicates, "event_receipt_investigation_id = ?")
		args = append(args, scope.InvestigationID)
	}
	if scope.ActingAppID != "" {
		predicates = append(predicates, "event_receipt_acting_app_id = ?")
		args = append(args, scope.ActingAppID)
	}
	query := "SELECT " + eventReceiptSelectColumns + " FROM events WHERE " + strings.Join(predicates, " AND ")
	return query, args
}

func (ass *SQLAuditStore) listEventReceiptsExcludingAuditReads(query string, args []any, limit, offset int) ([]*models.EventReceiptRecord, error) {
	var out []*models.EventReceiptRecord
	skip := offset
	for sqlOffset := 0; ; sqlOffset += auditReadScanBatchSize {
		batchArgs := append(slices.Clone(args), auditReadScanBatchSize, sqlOffset)
		batch, err := ass.queryEventReceiptRows(query+" ORDER BY timestamp DESC LIMIT ? OFFSET ?", batchArgs)
		if err != nil {
			return nil, err
		}
		for _, r := range batch {
			if isAuditReadEventReceipt(r) {
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

func (ass *SQLAuditStore) queryEventReceiptRows(query string, args []any) ([]*models.EventReceiptRecord, error) {
	rows, err := sqliteutil.MaterializeRows(context.Background(), ass.db, query, args, func(r *sql.Rows) (scannedEventReceipt, error) {
		return scanEventReceiptRow(r)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
	}
	var results []*models.EventReceiptRecord
	for _, row := range rows {
		if err := ass.decryptEventReceipt(row); err != nil {
			return nil, fmt.Errorf("%w: %w", constants.ErrAuditStoreQueryReceiptsFailed, err)
		}
		results = append(results, row.record)
	}
	return results, nil
}

// ListEventReceiptsSince retrieves current event receipts newer than the given timestamp.
func (ass *SQLAuditStore) ListEventReceiptsSince(since time.Time, limit int) ([]*models.EventReceiptRecord, error) {
	if ass == nil || ass.db == nil {
		return nil, constants.ErrAuditStoreDisabled
	}
	if limit <= 0 {
		limit = 100
	}
	records, err := ass.queryEventReceiptRows(`
		SELECT `+eventReceiptSelectColumns+`
		FROM events
		WHERE event_receipt_status IS NOT NULL AND timestamp > ?
		ORDER BY timestamp ASC
		LIMIT ?
	`, []any{timesvc.FormatTimestamp(since), limit})
	if err != nil {
		return nil, fmt.Errorf("%w %v: %w", constants.ErrAuditStoreQueryReceiptsSinceFailed, since, err)
	}
	return records, nil
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// eventReceiptFor builds a sample event receipt with its own transaction id, session, app, and timestamp.
func eventReceiptFor(t *testing.T, txID, sessionID, appID string, ts time.Time) *models.EventReceiptRecord {
	t.Helper()
	record := sampleEventReceiptRecord(t, sessionID)
	record.TransactionID = txID
	record.InvestigationID = "inv-" + txID
	record.ActingAppID = appID
	record.Timestamp = ts
	record.ExecutedAt = ts
	record.EventReceipt.TransactionId = txID
	return record
}

// TestRecordEventReceipt_SameTransactionTwiceKeepsOneRow verifies that recording the same
// transaction id twice leaves exactly one event receipts row carrying the latest status and summary,
// while the transaction hash stays the first-recorded value.
func TestRecordEventReceipt_SameTransactionTwiceKeepsOneRow(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("upsert-session", constants.SessionTypeOperator, "Upsert", "user-upsert"))

	now := time.Now().UTC()
	executing := eventReceiptFor(t, "tx-upsert-once", "upsert-session", "app-upsert", now)
	executing.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	executing.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	executing.ResultSummary = "executing"
	executing.TransactionHash = "hash-first"
	require.NoError(t, ass.RecordEventReceipt(executing))

	completed := eventReceiptFor(t, "tx-upsert-once", "upsert-session", "app-upsert", now.Add(time.Second))
	completed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	completed.EventReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	completed.ResultSummary = "completed"
	completed.TransactionHash = "hash-second"
	require.NoError(t, ass.RecordEventReceipt(completed))

	var rows int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE transaction_id = ? AND event_receipt_status IS NOT NULL`, "tx-upsert-once").Scan(&rows))
	assert.Equal(t, 1, rows, "one current event receipt")
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE transaction_id = ? AND type = ?`, "tx-upsert-once", string(constants.EventOperatorEventReceiptRecorded)).Scan(&rows))
	assert.Equal(t, 2, rows, "each stage remains a chain event")

	got, err := ass.GetEventReceipt("tx-upsert-once")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.Status)
	assert.Equal(t, "completed", got.ResultSummary)
	assert.Equal(t, "hash-first", got.TransactionHash, "transaction_hash is not rewritten by the upsert")
}

// TestListEventReceipts_FiltersBySessionInvestigationAndActingApp verifies
// intersected identity scopes, including stable Operator filtering before pagination.
func TestListEventReceipts_FiltersBySessionInvestigationAndActingApp(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("scope-a", constants.SessionTypeOperator, "A", "user-a"))
	require.NoError(t, ass.CreateSession("scope-b", constants.SessionTypeOperator, "B", "user-b"))

	now := time.Now().UTC()
	fixtures := []*models.EventReceiptRecord{
		eventReceiptFor(t, "tx-scope-1", "scope-a", "app-x", now.Add(-3*time.Minute)),
		eventReceiptFor(t, "tx-scope-2", "scope-a", "app-y", now.Add(-2*time.Minute)),
		eventReceiptFor(t, "tx-scope-3", "scope-b", "app-y", now.Add(-1*time.Minute)),
	}
	fixtures[0].InvestigationID = "investigation-shared"
	fixtures[1].InvestigationID = "investigation-other"
	fixtures[2].InvestigationID = "investigation-shared"
	fixtures[2].OperatorID = "other-operator"
	for _, record := range fixtures {
		require.NoError(t, ass.RecordEventReceipt(record))
	}

	ids := func(records []*models.EventReceiptRecord) map[string]bool {
		out := map[string]bool{}
		for _, r := range records {
			out[r.TransactionID] = true
		}
		return out
	}

	bySession, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "scope-a"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true, "tx-scope-2": true}, ids(bySession))

	byApp, err := ass.ListEventReceipts(models.AuditScope{ActingAppID: "app-y"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-2": true, "tx-scope-3": true}, ids(byApp))

	combined, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "scope-a", ActingAppID: "app-y"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-2": true}, ids(combined))

	byInvestigation, err := ass.ListEventReceipts(models.AuditScope{InvestigationID: "investigation-shared"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true, "tx-scope-3": true}, ids(byInvestigation))

	byOperator, err := ass.ListEventReceipts(models.AuditScope{OperatorID: fixtures[0].OperatorID, InvestigationID: "investigation-shared"}, 1, 0)
	require.NoError(t, err)
	require.Len(t, byOperator, 1, "operator filtering must precede pagination")
	assert.Equal(t, "tx-scope-1", byOperator[0].TransactionID)

	intersection, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "scope-a", InvestigationID: "investigation-shared"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true}, ids(intersection))

	unfiltered, err := ass.ListEventReceipts(models.AuditScope{}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, unfiltered, 3)
}

// TestListEventReceipts_ExcludeAuditReadsFiltersBeforePagination verifies that
// ExcludeAuditReads drops only the audit-read tools' own event receipts, keeps every other
// MCP_CALL, and applies limit and offset to the filtered sequence. The newest event_receipt
// is an audit read, so an unfiltered page would start with it.
func TestListEventReceipts_ExcludeAuditReadsFiltersBeforePagination(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("mixed-session", constants.SessionTypeOperator, "Mixed", "mixed-user"))

	base := time.Now().UTC().Add(-time.Hour)
	fixtures := []struct {
		txID   string
		action constants.ActionType
		target string
	}{
		{"tx-mixed-command", constants.ActionTypeExecuteBash, "localhost"},
		{"tx-mixed-shell", constants.ActionTypeMcpCall, constants.MCPToolRunShellCommand},
		{"tx-mixed-read", constants.ActionTypeMcpCall, constants.MCPToolAuditEventReceiptList},
	}
	for i, f := range fixtures {
		record := eventReceiptFor(t, f.txID, "mixed-session", "", base.Add(time.Duration(i)*time.Minute))
		record.InvestigationID = "investigation-mixed"
		record.ActionType = f.action
		record.TargetResource = f.target
		require.NoError(t, ass.RecordEventReceipt(record))
	}

	scope := models.AuditScope{InvestigationID: "investigation-mixed", ExcludeAuditReads: true}

	all, err := ass.ListEventReceipts(scope, 10, 0)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "tx-mixed-shell", all[0].TransactionID)
	assert.Equal(t, "tx-mixed-command", all[1].TransactionID)

	first, err := ass.ListEventReceipts(scope, 1, 0)
	require.NoError(t, err)
	require.Len(t, first, 1)
	assert.Equal(t, "tx-mixed-shell", first[0].TransactionID)

	second, err := ass.ListEventReceipts(scope, 1, 1)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "tx-mixed-command", second[0].TransactionID)

	past, err := ass.ListEventReceipts(scope, 10, 2)
	require.NoError(t, err)
	assert.Empty(t, past)

	unfiltered, err := ass.ListEventReceipts(models.AuditScope{InvestigationID: "investigation-mixed"}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, unfiltered, 3, "without the flag every event receipt in the investigation is returned")
}

// TestListEventReceipts_ExcludeAuditReadsPagesPastFullBatches verifies that the filtered scan
// keeps reading SQL batches while they are full of audit reads, and reaches older event receipts.
func TestListEventReceipts_ExcludeAuditReadsPagesPastFullBatches(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("batch-session", constants.SessionTypeOperator, "Batch", "batch-user"))

	base := time.Now().UTC().Add(-2 * time.Hour)
	for i, txID := range []string{"tx-batch-command-old", "tx-batch-command-new"} {
		record := eventReceiptFor(t, txID, "batch-session", "", base.Add(time.Duration(i)*time.Minute))
		record.InvestigationID = "investigation-batch"
		record.ActionType = constants.ActionTypeExecuteBash
		require.NoError(t, ass.RecordEventReceipt(record))
	}
	for i := range auditReadScanBatchSize + 5 {
		record := eventReceiptFor(t, fmt.Sprintf("tx-batch-read-%03d", i), "batch-session", "", base.Add(time.Hour+time.Duration(i)*time.Second))
		record.InvestigationID = "investigation-batch"
		record.ActionType = constants.ActionTypeMcpCall
		record.TargetResource = constants.MCPToolAuditEventReceiptList
		require.NoError(t, ass.RecordEventReceipt(record))
	}

	rows, err := ass.ListEventReceipts(models.AuditScope{InvestigationID: "investigation-batch", ExcludeAuditReads: true}, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "tx-batch-command-new", rows[0].TransactionID)
	assert.Equal(t, "tx-batch-command-old", rows[1].TransactionID)
}

// TestListEventReceipts_PaginatesNewestFirst verifies limit/offset paging returns event_receipts
// newest first with no overlap between pages.
func TestListEventReceipts_PaginatesNewestFirst(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("page-event_receipts", constants.SessionTypeOperator, "Page", "user-page"))

	base := time.Now().UTC().Add(-time.Hour)
	for i, txID := range []string{"tx-page-oldest", "tx-page-middle", "tx-page-newest"} {
		require.NoError(t, ass.RecordEventReceipt(eventReceiptFor(t, txID, "page-event_receipts", "app-page", base.Add(time.Duration(i)*time.Minute))))
	}

	first, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "page-event_receipts"}, 2, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, "tx-page-newest", first[0].TransactionID)
	assert.Equal(t, "tx-page-middle", first[1].TransactionID)

	second, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "page-event_receipts"}, 2, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "tx-page-oldest", second[0].TransactionID)
}

// TestListEventReceiptsSince_ReturnsOnlyNewerEventReceiptsAscending verifies the time-range listing
// excludes event receipts at or before the cutoff and returns the rest oldest first.
func TestListEventReceiptsSince_ReturnsOnlyNewerEventReceiptsAscending(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("since-session", constants.SessionTypeOperator, "Since", "user-since"))

	now := time.Now().UTC()
	require.NoError(t, ass.RecordEventReceipt(eventReceiptFor(t, "tx-since-old", "since-session", "app-since", now.Add(-3*time.Hour))))
	require.NoError(t, ass.RecordEventReceipt(eventReceiptFor(t, "tx-since-late", "since-session", "app-since", now.Add(-10*time.Minute))))
	require.NoError(t, ass.RecordEventReceipt(eventReceiptFor(t, "tx-since-recent", "since-session", "app-since", now.Add(-5*time.Minute))))

	since, err := ass.ListEventReceiptsSince(now.Add(-2*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, since, 2)
	assert.Equal(t, "tx-since-late", since[0].TransactionID)
	assert.Equal(t, "tx-since-recent", since[1].TransactionID)

	none, err := ass.ListEventReceiptsSince(now.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestEventReceiptReads_LockedVaultFailsRead verifies that event receipt reads fail closed with
// ErrAuditStoreVaultLocked once the vault is locked, rather than returning ciphertext.
func TestEventReceiptReads_LockedVaultFailsRead(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("locked-event_receipts", constants.SessionTypeOperator, "Locked", "user-locked"))
	require.NoError(t, ass.RecordEventReceipt(eventReceiptFor(t, "tx-locked-read", "locked-event_receipts", "app-locked", time.Now().UTC())))

	ass.GetEncryptionVault().Lock()

	got, err := ass.GetEventReceipt("tx-locked-read")
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, got)

	listed, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "locked-event_receipts"}, 10, 0)
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, listed)

	since, err := ass.ListEventReceiptsSince(time.Now().UTC().Add(-time.Hour), 10)
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, since)
}

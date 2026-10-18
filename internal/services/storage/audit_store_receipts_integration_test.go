// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

// receiptFor builds a sample receipt with its own transaction id, session, app, and timestamp.
func receiptFor(t *testing.T, txID, sessionID, appID string, ts time.Time) *models.ActionReceiptRecord {
	t.Helper()
	record := sampleActionReceiptRecord(t, sessionID)
	record.TransactionID = txID
	record.InvestigationID = "inv-" + txID
	record.ActingAppID = appID
	record.Timestamp = ts
	record.ExecutedAt = ts
	record.ActionReceipt.TransactionId = txID
	return record
}

// TestRecordActionReceipt_SameTransactionTwiceKeepsOneRow verifies that recording the same
// transaction id twice leaves exactly one receipts row carrying the latest status and summary,
// while the transaction hash stays the first-recorded value.
func TestRecordActionReceipt_SameTransactionTwiceKeepsOneRow(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("upsert-session", constants.SessionTypeOperator, "Upsert", "user-upsert"))

	now := time.Now().UTC()
	executing := receiptFor(t, "tx-upsert-once", "upsert-session", "app-upsert", now)
	executing.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	executing.ActionReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_EXECUTING
	executing.ResultSummary = "executing"
	executing.TransactionHash = "hash-first"
	require.NoError(t, ass.RecordActionReceipt(executing))

	completed := receiptFor(t, "tx-upsert-once", "upsert-session", "app-upsert", now.Add(time.Second))
	completed.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	completed.ActionReceipt.Status = operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED
	completed.ResultSummary = "completed"
	completed.TransactionHash = "hash-second"
	require.NoError(t, ass.RecordActionReceipt(completed))

	var rows int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM receipts WHERE transaction_id = ?`, "tx-upsert-once").Scan(&rows))
	assert.Equal(t, 1, rows)

	got, err := ass.GetActionReceipt("tx-upsert-once")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED, got.Status)
	assert.Equal(t, "completed", got.ResultSummary)
	assert.Equal(t, "hash-first", got.TransactionHash, "transaction_hash is not rewritten by the upsert")
}

// TestListActionReceipts_FiltersBySessionAndActingApp verifies the session, acting-app, combined,
// and unfiltered scopes of ListActionReceipts.
func TestListActionReceipts_FiltersBySessionInvestigationAndActingApp(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("scope-a", constants.SessionTypeOperator, "A", "user-a"))
	require.NoError(t, ass.CreateSession("scope-b", constants.SessionTypeOperator, "B", "user-b"))

	now := time.Now().UTC()
	fixtures := []*models.ActionReceiptRecord{
		receiptFor(t, "tx-scope-1", "scope-a", "app-x", now.Add(-3*time.Minute)),
		receiptFor(t, "tx-scope-2", "scope-a", "app-y", now.Add(-2*time.Minute)),
		receiptFor(t, "tx-scope-3", "scope-b", "app-y", now.Add(-1*time.Minute)),
	}
	fixtures[0].InvestigationID = "investigation-shared"
	fixtures[1].InvestigationID = "investigation-other"
	fixtures[2].InvestigationID = "investigation-shared"
	fixtures[2].OperatorID = "other-operator"
	for _, record := range fixtures {
		require.NoError(t, ass.RecordActionReceipt(record))
	}

	ids := func(records []*models.ActionReceiptRecord) map[string]bool {
		out := map[string]bool{}
		for _, r := range records {
			out[r.TransactionID] = true
		}
		return out
	}

	bySession, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "scope-a"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true, "tx-scope-2": true}, ids(bySession))

	byApp, err := ass.ListActionReceipts(models.AuditScope{ActingAppID: "app-y"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-2": true, "tx-scope-3": true}, ids(byApp))

	combined, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "scope-a", ActingAppID: "app-y"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-2": true}, ids(combined))

	byInvestigation, err := ass.ListActionReceipts(models.AuditScope{InvestigationID: "investigation-shared"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true, "tx-scope-3": true}, ids(byInvestigation))

	byOperator, err := ass.ListActionReceipts(models.AuditScope{OperatorID: fixtures[0].OperatorID, InvestigationID: "investigation-shared"}, 1, 0)
	require.NoError(t, err)
	require.Len(t, byOperator, 1, "operator filtering must precede pagination")
	assert.Equal(t, "tx-scope-1", byOperator[0].TransactionID)

	intersection, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "scope-a", InvestigationID: "investigation-shared"}, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"tx-scope-1": true}, ids(intersection))

	unfiltered, err := ass.ListActionReceipts(models.AuditScope{}, 10, 0)
	require.NoError(t, err)
	assert.Len(t, unfiltered, 3)
}

// TestListActionReceipts_PaginatesNewestFirst verifies limit/offset paging returns receipts
// newest first with no overlap between pages.
func TestListActionReceipts_PaginatesNewestFirst(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("page-receipts", constants.SessionTypeOperator, "Page", "user-page"))

	base := time.Now().UTC().Add(-time.Hour)
	for i, txID := range []string{"tx-page-oldest", "tx-page-middle", "tx-page-newest"} {
		require.NoError(t, ass.RecordActionReceipt(receiptFor(t, txID, "page-receipts", "app-page", base.Add(time.Duration(i)*time.Minute))))
	}

	first, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "page-receipts"}, 2, 0)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, "tx-page-newest", first[0].TransactionID)
	assert.Equal(t, "tx-page-middle", first[1].TransactionID)

	second, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "page-receipts"}, 2, 2)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, "tx-page-oldest", second[0].TransactionID)
}

// TestListActionReceiptsSince_ReturnsOnlyNewerReceiptsAscending verifies the time-range listing
// excludes receipts at or before the cutoff and returns the rest oldest first.
func TestListActionReceiptsSince_ReturnsOnlyNewerReceiptsAscending(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("since-session", constants.SessionTypeOperator, "Since", "user-since"))

	now := time.Now().UTC()
	require.NoError(t, ass.RecordActionReceipt(receiptFor(t, "tx-since-old", "since-session", "app-since", now.Add(-3*time.Hour))))
	require.NoError(t, ass.RecordActionReceipt(receiptFor(t, "tx-since-late", "since-session", "app-since", now.Add(-10*time.Minute))))
	require.NoError(t, ass.RecordActionReceipt(receiptFor(t, "tx-since-recent", "since-session", "app-since", now.Add(-5*time.Minute))))

	since, err := ass.ListActionReceiptsSince(now.Add(-2*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, since, 2)
	assert.Equal(t, "tx-since-late", since[0].TransactionID)
	assert.Equal(t, "tx-since-recent", since[1].TransactionID)

	none, err := ass.ListActionReceiptsSince(now.Add(time.Hour), 10)
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestReceiptReads_LockedVaultFailsRead verifies that receipt reads fail closed with
// ErrAuditStoreVaultLocked once the vault is locked, rather than returning ciphertext.
func TestReceiptReads_LockedVaultFailsRead(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("locked-receipts", constants.SessionTypeOperator, "Locked", "user-locked"))
	require.NoError(t, ass.RecordActionReceipt(receiptFor(t, "tx-locked-read", "locked-receipts", "app-locked", time.Now().UTC())))

	ass.GetEncryptionVault().Lock()

	got, err := ass.GetActionReceipt("tx-locked-read")
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, got)

	listed, err := ass.ListActionReceipts(models.AuditScope{OperatorSessionID: "locked-receipts"}, 10, 0)
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, listed)

	since, err := ass.ListActionReceiptsSince(time.Now().UTC().Add(-time.Hour), 10)
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked)
	assert.Nil(t, since)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package reporting

import (
	"context"
	"crypto/ed25519"
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/keystore"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keystoretest"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newReportingTestFileSvc(t *testing.T, baseDir string) fs.RuntimeFileService {
	t.Helper()
	fileSvc, err := fs.NewRuntimeFileService(baseDir, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	return fileSvc
}

// ---------------------------------------------------------------------------
// reportReceipts
// ---------------------------------------------------------------------------

func TestReportReceipts_EmptyStore(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	res, err := reportReceipts(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportReceiptsFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
	assert.NotEmpty(t, res.SHA256)

	csvPath := filepath.Join(outDir, constants.ReportReceiptsFilename)
	f, err := os.Open(csvPath)
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 1, "header only")
	assert.Equal(t, ReceiptRow{}.Columns(), records[0])
}

func TestReportReceipts_WithRecords(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	sessionID := "test-session-receipts"
	require.NoError(t, store.CreateSession(sessionID, "operator", "Test", "user@test.com"))

	now := time.Now().UTC()
	require.NoError(t, store.RecordEventReceipt(&models.EventReceiptRecord{
		TransactionID:     "tx-1",
		TransactionHash:   "hash-1",
		OperatorID:        "op-1",
		OperatorSessionID: sessionID,
		ActionType:        constants.ActionTypeFileEdit,
		TargetResource:    "/file1",
		Status:            operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ExecutedAt:        now,
		SignerKeyID:       "key-1",
		Signature:         "sig-1",
	}))
	require.NoError(t, store.RecordEventReceipt(&models.EventReceiptRecord{
		TransactionID:     "tx-2",
		TransactionHash:   "hash-2",
		OperatorID:        "op-2",
		OperatorSessionID: sessionID,
		ActionType:        constants.ActionTypeFsRead,
		TargetResource:    "/file2",
		Status:            operatorv1.ExecutionStatus_EXECUTION_STATUS_FAILED,
		ExecutedAt:        now.Add(time.Second),
		SignerKeyID:       "key-2",
		Signature:         "sig-2",
	}))

	outDir := testutil.TempDir(t)
	res, err := reportReceipts(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportReceiptsFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
	txIDs := []string{records[1][0], records[2][0]}
	assert.ElementsMatch(t, []string{"tx-1", "tx-2"}, txIDs)
	statuses := []string{records[1][5], records[2][5]}
	assert.ElementsMatch(t, []string{"EXECUTION_STATUS_COMPLETED", "EXECUTION_STATUS_FAILED"}, statuses)
}

func TestReportReceipts_CancelledContext(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportReceipts(ctx, outDir, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportSessions
// ---------------------------------------------------------------------------

func TestReportSessions_EmptyStore(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	res, err := reportSessions(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportSessionsFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportSessions_WithRecords(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	require.NoError(t, store.CreateSession("sess-1", "operator", "Session 1", "user1@test.com"))
	require.NoError(t, store.CreateSession("sess-2", "operator", "Session 2", "user2@test.com"))

	outDir := testutil.TempDir(t)
	res, err := reportSessions(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportSessionsFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
	assert.Equal(t, "sess-1", records[1][0])
	assert.Equal(t, "sess-2", records[2][0])
}

func TestReportSessions_CancelledContext(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportSessions(ctx, outDir, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportEvents
// ---------------------------------------------------------------------------

func TestReportEvents_EmptyStore(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	res, err := reportEvents(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportEventsFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportEvents_WithRecords(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	sessionID := "test-session-events"
	require.NoError(t, store.CreateSession(sessionID, "operator", "Test", "user@test.com"))

	_, err := store.RecordEvent(&storage.Event{
		OperatorSessionID: sessionID,
		Timestamp:         time.Now().UTC(),
		Type:              "COMMAND_EXECUTION",
		CommandRaw:        "echo hello",
		ContentText:       "hello\n",
	})
	require.NoError(t, err)

	_, err = store.RecordEvent(&storage.Event{
		OperatorSessionID: sessionID,
		Timestamp:         time.Now().UTC(),
		Type:              "COMMAND_EXECUTION",
		CommandRaw:        "ls -la",
		ContentText:       "",
	})
	require.NoError(t, err)

	outDir := testutil.TempDir(t)
	res, err := reportEvents(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportEventsFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
}

func TestReportEvents_CancelledContext(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportEvents(ctx, outDir, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportFileMutations
// ---------------------------------------------------------------------------

func TestReportFileMutations_EmptyStore(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	res, err := reportFileMutations(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportFileMutationsFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportFileMutations_WithRecords(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	eventID := createSessionAndEvent(t, store)

	require.NoError(t, store.RecordFileMutation(&storage.FileMutationLog{
		EventID:          eventID,
		Filepath:         "/test/file1",
		Operation:        storage.FileMutationWrite,
		LedgerHashBefore: "hash-before-1",
		LedgerHashAfter:  "hash-after-1",
	}))
	require.NoError(t, store.RecordFileMutation(&storage.FileMutationLog{
		EventID:          eventID,
		Filepath:         "/test/file2",
		Operation:        storage.FileMutationCreate,
		LedgerHashBefore: "",
		LedgerHashAfter:  "hash-after-2",
	}))

	outDir := testutil.TempDir(t)
	res, err := reportFileMutations(context.Background(), outDir, store)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportFileMutationsFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
	assert.NotContains(t, records[0], "filepath", "file_mutations.csv must not carry the encrypted filepath")
	assert.NotContains(t, records[0], "diff_stat", "file_mutations.csv must not carry the encrypted diff stat")
}

func TestReportFileMutations_CancelledContext(t *testing.T) {
	t.Parallel()

	store := setupTestAuditStore(t)
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportFileMutations(ctx, outDir, store)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportCommitments
// ---------------------------------------------------------------------------

func TestReportCommitments_EmptyLedger(t *testing.T) {
	t.Parallel()
	cl, _, _ := setupTestCommitmentLedger(t)
	outDir := testutil.TempDir(t)

	res, err := reportCommitments(context.Background(), outDir, cl)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportCommitmentsFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportCommitments_WithRecords(t *testing.T) {
	t.Parallel()
	cl, _, _ := setupTestCommitmentLedger(t)
	firstHash := insertCommitment(t, cl, "tx-1", "hash-1", "", "FS_WRITE", "/file1")
	insertCommitment(t, cl, "tx-2", "hash-2", firstHash, "FS_WRITE", "/file2")

	outDir := testutil.TempDir(t)
	res, err := reportCommitments(context.Background(), outDir, cl)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportCommitmentsFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
	txIDs := []string{records[1][2], records[2][2]}
	assert.ElementsMatch(t, []string{"tx-1", "tx-2"}, txIDs)
}

func TestReportCommitments_CancelledContext(t *testing.T) {
	t.Parallel()
	cl, _, _ := setupTestCommitmentLedger(t)
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportCommitments(ctx, outDir, cl)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportReplayNonces
// ---------------------------------------------------------------------------

// newTestReplayStore builds a replay store over a fresh ztd.db.
func newTestReplayStore(t *testing.T) *storage.ReplayStore {
	t.Helper()
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(filepath.Join(testutil.TempDir(t), constants.DbFilename)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	rs, err := storage.NewReplayStore(db, testutil.NewTestLogger())
	require.NoError(t, err)
	return rs
}

func TestReportReplayNonces_EmptyStore(t *testing.T) {
	t.Parallel()
	rs := newTestReplayStore(t)

	outDir := testutil.TempDir(t)
	res, err := reportReplayNonces(context.Background(), outDir, rs)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportReplayNoncesFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportReplayNonces_WithRecords(t *testing.T) {
	t.Parallel()
	rs := newTestReplayStore(t)

	expiresAt := time.Now().UTC().Add(time.Hour)
	_, err := rs.ReserveNonce("nonce-1", expiresAt)
	require.NoError(t, err)
	_, err = rs.ReserveNonce("nonce-2", expiresAt)
	require.NoError(t, err)

	outDir := testutil.TempDir(t)
	res, err := reportReplayNonces(context.Background(), outDir, rs)
	require.NoError(t, err)
	assert.Equal(t, 2, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportReplayNoncesFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3, "header + 2 rows")
}

func TestReportReplayNonces_CancelledContext(t *testing.T) {
	t.Parallel()
	rs := newTestReplayStore(t)

	outDir := testutil.TempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportReplayNonces(ctx, outDir, rs)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportSuspendedTransactions
// ---------------------------------------------------------------------------

// newTestSuspendedStore builds a suspended transaction store over a fresh ztd.db and an unlocked vault.
func newTestSuspendedStore(t *testing.T) *storage.SuspendedTransactionService {
	t.Helper()
	fileSvc := newReportingTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	v := createTestVault(t, fileSvc, privKey)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(filepath.Join(testutil.TempDir(t), constants.DbFilename)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	sts, err := storage.NewSuspendedTransactionService(db, v, testutil.NewTestLogger())
	require.NoError(t, err)
	return sts
}

func TestReportSuspendedTransactions_EmptyStore(t *testing.T) {
	t.Parallel()
	sts := newTestSuspendedStore(t)

	outDir := testutil.TempDir(t)
	res, err := reportSuspendedTransactions(context.Background(), outDir, sts)
	require.NoError(t, err)
	assert.Equal(t, constants.ReportSuspendedTxFilename, res.Filename)
	assert.Equal(t, 0, res.RowCount)
}

func TestReportSuspendedTransactions_WithRecords(t *testing.T) {
	t.Parallel()
	sts := newTestSuspendedStore(t)

	ctx := context.Background()
	now := time.Now().UTC()

	require.NoError(t, sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "suspend-hash-1",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env-1"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
		ToolName:          "shell_exec",
	}))

	require.NoError(t, sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "suspend-hash-2",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env-2"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-2",
		OperatorID:        "op-1",
		ToolName:          "fs_write",
	}))
	require.NoError(t, sts.ClaimSuspendedTransaction(ctx, "suspend-hash-2"))

	require.NoError(t, sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "suspend-hash-3",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env-3"),
		CreatedAt:         now.Add(-2 * time.Hour),
		ExpiresAt:         now.Add(-time.Hour),
		UserID:            "user-3",
		OperatorID:        "op-1",
		ToolName:          "fs_read",
	}))

	outDir := testutil.TempDir(t)
	res, err := reportSuspendedTransactions(ctx, outDir, sts)
	require.NoError(t, err)
	assert.Equal(t, 3, res.RowCount)

	f, err := os.Open(filepath.Join(outDir, constants.ReportSuspendedTxFilename))
	require.NoError(t, err)
	defer f.Close()

	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 4, "header + 3 rows")
	statuses := make(map[string]string, len(records)-1)
	for _, record := range records[1:] {
		statuses[record[0]] = record[3]
	}
	assert.Equal(t, map[string]string{
		"suspend-hash-1": string(constants.SuspendedTxStatusPending),
		"suspend-hash-2": string(constants.SuspendedTxStatusClaimed),
		"suspend-hash-3": string(constants.SuspendedTxStatusExpired),
	}, statuses)
}

func TestReportSuspendedTransactions_CancelledContext(t *testing.T) {
	t.Parallel()
	sts := newTestSuspendedStore(t)

	outDir := testutil.TempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportSuspendedTransactions(ctx, outDir, sts)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// reportLedgerMerkleRoot & reportLedgerCommits
// ---------------------------------------------------------------------------

func TestReportLedgerMerkleRoot_NilLedger(t *testing.T) {
	t.Parallel()
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportLedgerMerkleRoot(ctx, outDir, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestReportLedgerCommits_NilLedger(t *testing.T) {
	t.Parallel()
	outDir := testutil.TempDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := reportLedgerCommits(ctx, outDir, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// openVault
// ---------------------------------------------------------------------------

func TestOpenVault_NoKeyProvided_FailsClosed(t *testing.T) {
	t.Parallel()
	tempDir := testutil.TempDir(t)
	fileSvc := newReportingTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	header, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))

	v, err := openVault(nil)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	assert.Nil(t, v)
}

func TestOpenVault_VaultKeyNotFound(t *testing.T) {
	t.Parallel()
	tempDir := testutil.TempDir(t)
	fileSvc := newReportingTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	header, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))

	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	v, err := openVault(ks)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	assert.Nil(t, v)
}

func TestOpenVault_UnlockFailed(t *testing.T) {
	t.Parallel()
	tempDir := testutil.TempDir(t)
	fileSvc := newReportingTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	header, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, header.Save(fileSvc))

	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())

	wrongKey := make([]byte, vault.KeySize)
	wrongKey[0] = 0x42
	require.NoError(t, ks.StoreKeyMaterial(constants.SecretsFileVaultKey, wrongKey))

	v, err := openVault(ks)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	assert.Nil(t, v)
}

func TestOpenVault_ValidKey(t *testing.T) {
	t.Parallel()
	tempDir := testutil.TempDir(t)
	fileSvc := newReportingTestFileSvc(t, tempDir)

	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	v, err := openVault(ks)
	require.NoError(t, err)
	assert.True(t, v.IsUnlocked())
	t.Cleanup(func() { v.Close() })
}

// ---------------------------------------------------------------------------
// recordTypeForFile (additional edge cases)
// ---------------------------------------------------------------------------

func TestRecordTypeForFile_EdgeCases(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "manifest", recordTypeForFile("manifest.csv"))
	assert.Equal(t, "suspended_transactions", recordTypeForFile("/a/b/suspended_transactions.csv"))
}

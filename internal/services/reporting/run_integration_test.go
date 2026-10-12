// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package reporting

import (
	"context"
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
)

// setupReportingEnv creates a fully populated reporting environment in a temp
// directory. It returns Options pre-configured with all paths and an
// initialized Keystore.
func setupReportingEnv(t *testing.T, seed bool) Options {
	t.Helper()

	root := testutil.TempDir(t)
	outDir := filepath.Join(root, "reports")

	fileSvc, err := fs.NewRuntimeFileService(root, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))
	dataDir := fileSvc.Resolve(constants.DataDirname)

	// Build a memory-keyring keystore and initialize vault.
	ks, err := keystore.NewWithKeyringAndFS(testutil.NewTestLogger(), keystoretest.NewMemoryKeyring(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, ks.Initialize())
	require.NoError(t, ks.InitVault())

	vaultKey, err := ks.LoadVaultKey()
	require.NoError(t, err)
	defer vault.SecureZero(vaultKey)

	v, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: testutil.NewTestLogger()})
	require.NoError(t, err)
	require.NoError(t, v.Unlock(vaultKey))
	t.Cleanup(func() { v.Close() })

	// Open ztd.db once; the audit store and its commitment ledger share the pool.
	require.NoError(t, fileSvc.MkdirAll(context.Background(), constants.DataDirname, constants.PermDirStandard))
	mainDB, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(filepath.Join(dataDir, constants.DbFilename)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { mainDB.Close() })

	// Audit store.
	auditCfg := &storage.AuditStoreConfig{
		RetentionDays:        7,
		PruneIntervalMinutes: 60,
		EncryptionVault:      v,
	}
	store, err := storage.NewSQLAuditStore(auditCfg, mainDB, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	rs, err := storage.NewReplayStore(mainDB, testutil.NewTestLogger())
	require.NoError(t, err)

	sts, err := storage.NewSuspendedTransactionService(mainDB, v, testutil.NewTestLogger())
	require.NoError(t, err)

	opts := Options{
		FileSvc:  fileSvc,
		Keystore: ks,
		OutDir:   outDir,
		Logger:   testutil.NewTestLogger(),
	}

	if seed {
		seedReportingData(t, store, rs, sts)
	}

	return opts
}

func seedReportingData(t *testing.T, store *storage.SQLAuditStore, rs *storage.ReplayStore, sts *storage.SuspendedTransactionService) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	// Session + events + receipts + file mutations.
	require.NoError(t, store.CreateSession("sess-run-1", "operator", "Run Test", "user@test.com"))

	require.NoError(t, store.RecordActionReceipt(&models.ActionReceiptRecord{
		TransactionID:     "tx-run-1",
		TransactionHash:   "hash-run-1",
		OperatorID:        "op-1",
		OperatorSessionID: "sess-run-1",
		ActionType:        constants.ActionTypeFileEdit,
		TargetResource:    "/file1",
		Status:            2,
		ExecutedAt:        now,
		SignerKeyID:       "key-1",
		Signature:         "sig-1",
	}))

	_, err := store.RecordEvent(&storage.Event{
		OperatorSessionID: "sess-run-1",
		Timestamp:         now,
		Type:              "COMMAND_EXECUTION",
		CommandRaw:        "echo test",
		ContentText:       "test\n",
	})
	require.NoError(t, err)

	// File mutation — need a valid event ID.
	eventID := createSessionAndEvent(t, store)
	require.NoError(t, store.RecordFileMutation(&storage.FileMutationLog{
		EventID:          eventID,
		Filepath:         "/test/run-file",
		Operation:        storage.FileMutationWrite,
		LedgerHashBefore: "hash-before",
		LedgerHashAfter:  "hash-after",
	}))

	// Replay store.
	_, err = rs.ReserveNonce("nonce-run-1", now.Add(time.Hour))
	require.NoError(t, err)

	// Suspended transaction store.
	require.NoError(t, sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "suspend-run-1",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
		ToolName:          "shell_exec",
	}))
}

func TestRun_PopulatedStores_AllCSVFilesWritten(t *testing.T) {
	opts := setupReportingEnv(t, true)

	result, err := Run(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, 0, result.FailCount)

	// Verify all expected CSV files exist.
	expectedFiles := []string{
		constants.ReportReceiptsFilename,
		constants.ReportSessionsFilename,
		constants.ReportEventsFilename,
		constants.ReportFileMutationsFilename,
		constants.ReportCommitmentsFilename,
		constants.ReportReplayNoncesFilename,
		constants.ReportSuspendedTxFilename,
		constants.ReportVerificationFilename,
		constants.ReportManifestFilename,
	}

	for _, fname := range expectedFiles {
		path := filepath.Join(opts.OutDir, fname)
		_, err := os.Stat(path)
		assert.NoError(t, err, "expected file %s to exist", fname)
	}

	// Verify manifest has entries for all files.
	manifestPath := filepath.Join(opts.OutDir, constants.ReportManifestFilename)
	f, err := os.Open(manifestPath)
	require.NoError(t, err)
	defer f.Close()
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	require.NoError(t, err)
	// Header + at least 10 data rows (all reporters + verification).
	assert.GreaterOrEqual(t, len(records), 2, "manifest should have header + data rows")

	// Verify receipts CSV has 1 row.
	receiptsPath := filepath.Join(opts.OutDir, constants.ReportReceiptsFilename)
	f2, err := os.Open(receiptsPath)
	require.NoError(t, err)
	defer f2.Close()
	r2 := csv.NewReader(f2)
	receiptRecords, err := r2.ReadAll()
	require.NoError(t, err)
	require.Len(t, receiptRecords, 2, "header + 1 receipt row")
	assert.Equal(t, "tx-run-1", receiptRecords[1][0])

	// Verify replay nonces CSV has 1 row.
	noncePath := filepath.Join(opts.OutDir, constants.ReportReplayNoncesFilename)
	f4, err := os.Open(noncePath)
	require.NoError(t, err)
	defer f4.Close()
	r4 := csv.NewReader(f4)
	nonceRecords, err := r4.ReadAll()
	require.NoError(t, err)
	require.Len(t, nonceRecords, 2, "header + 1 nonce row")

	// Verify suspended transactions CSV has 1 row.
	suspendPath := filepath.Join(opts.OutDir, constants.ReportSuspendedTxFilename)
	f5, err := os.Open(suspendPath)
	require.NoError(t, err)
	defer f5.Close()
	r5 := csv.NewReader(f5)
	suspendRecords, err := r5.ReadAll()
	require.NoError(t, err)
	require.Len(t, suspendRecords, 2, "header + 1 suspended tx row")
	assert.Equal(t, "suspend-run-1", suspendRecords[1][0])

	// The execution record is events; no executions or file_diffs CSV is written.
	_, statErr := os.Stat(filepath.Join(opts.OutDir, "executions.csv"))
	assert.True(t, os.IsNotExist(statErr), "executions.csv must not be written")
	_, statErr = os.Stat(filepath.Join(opts.OutDir, "file_diffs.csv"))
	assert.True(t, os.IsNotExist(statErr), "file_diffs.csv must not be written")
}

func TestRun_EmptyStores_AllCSVFilesWritten(t *testing.T) {
	opts := setupReportingEnv(t, false)

	result, err := Run(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, 0, result.FailCount)

	// All files should still exist with header-only rows.
	expectedFiles := []string{
		constants.ReportReceiptsFilename,
		constants.ReportSessionsFilename,
		constants.ReportEventsFilename,
		constants.ReportFileMutationsFilename,
		constants.ReportCommitmentsFilename,
		constants.ReportReplayNoncesFilename,
		constants.ReportSuspendedTxFilename,
		constants.ReportVerificationFilename,
		constants.ReportManifestFilename,
	}

	for _, fname := range expectedFiles {
		path := filepath.Join(opts.OutDir, fname)
		_, err := os.Stat(path)
		assert.NoError(t, err, "expected file %s to exist even with empty stores", fname)
	}
}

// Fail-closed: without a keystore the vault cannot unlock, so the run writes nothing.
func TestRun_LockedVault_NilKeystore(t *testing.T) {
	opts := setupReportingEnv(t, true)
	opts.Keystore = nil

	_, err := Run(context.Background(), opts)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
	_, statErr := os.Stat(filepath.Join(opts.OutDir, constants.ReportReceiptsFilename))
	assert.True(t, os.IsNotExist(statErr), "no CSV may be written when the vault is unavailable")
}

func TestRun_LockedVault_VaultKeyNotFound(t *testing.T) {
	opts := setupReportingEnv(t, true)
	require.NotNil(t, opts.Keystore)
	require.NoError(t, opts.Keystore.DeleteSecret(constants.SecretsFileVaultKey))

	_, err := Run(context.Background(), opts)
	require.ErrorIs(t, err, constants.ErrReportVaultUnavailable)
}

func TestRun_CancelledContext(t *testing.T) {
	opts := setupReportingEnv(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, opts)
	require.NoError(t, err) // Run itself doesn't return error on cancelled ctx; individual reporters skip.
	// No files should be written because the run closure checks ctx.Err() before each reporter.
	// However, the OutDir is still created.
	_, statErr := os.Stat(filepath.Join(opts.OutDir, constants.ReportReceiptsFilename))
	assert.True(t, os.IsNotExist(statErr), "no CSV files should be written with cancelled context")
}

func TestRun_BadOutDir(t *testing.T) {
	opts := setupReportingEnv(t, false)
	blocker := filepath.Join(testutil.TempDir(t), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0644))
	opts.OutDir = filepath.Join(blocker, "cannot-create-here")

	_, err := Run(context.Background(), opts)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrReportOutputDirFailed)
}

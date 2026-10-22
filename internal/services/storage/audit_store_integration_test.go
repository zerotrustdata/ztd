// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	vault "github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
	compliancev1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/compliance/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func newIntegrationAuditStore(t *testing.T) *SQLAuditStore {
	t.Helper()

	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	config := DefaultAuditStoreConfig()
	config.EncryptionVault = testVault

	ass, err := NewSQLAuditStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ass.Close()) })
	return ass
}

func sampleEventReceiptRecord(t *testing.T, sessionID string) *models.EventReceiptRecord {
	t.Helper()

	now := time.Now().UTC()
	return &models.EventReceiptRecord{
		TransactionID:     "tx-integration-1",
		TransactionHash:   "hash-integration-1",
		InvestigationID:   "investigation-integration-1",
		OperatorID:        "operator-integration-1",
		OperatorSessionID: sessionID,
		RequestorUserID:   "user-integration-1",
		ActingAppID:       "app-integration-1",
		ActionType:        constants.ActionTypeExecuteBash,
		TargetResource:    "localhost",
		Status:            operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
		ResultSummary:     "completed successfully",
		StateRootBefore:   "root-before",
		StateRootAfter:    "root-after",
		ExecutedAt:        now,
		SignerKeyID:       "signer-1",
		Signature:         "signature-1",
		Timestamp:         now,
		EventReceipt: &operatorv1.EventReceipt{
			TransactionId:   "tx-integration-1",
			TransactionHash: "hash-integration-1",
			Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
			Signature:       "signature-1",
		},
	}
}

func TestNewSQLAuditStore_NilLoggerUsesDefault(t *testing.T) {
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	config := DefaultAuditStoreConfig()
	config.EncryptionVault = testVault

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	ass, err := NewSQLAuditStore(config, db, nil, fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ass.Close()) })

	require.NotNil(t, ass.CommitmentLedger())
	require.NotNil(t, ass.GetEncryptionVault())
	assert.NotEmpty(t, ass.GetDataDir())
	ass.Wait()
}

func TestSQLAuditStore_IntegrationLifecycle(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	require.NoError(t, ass.CreateSession("session-1", constants.SessionTypeOperator, "Integration Session", "user-1"))
	session, err := ass.GetOperatorSession("session-1")
	require.NoError(t, err)
	require.NotNil(t, session)
	assert.Equal(t, "session-1", session.ID)

	event := &Event{
		OperatorSessionID: "session-1",
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "integration event",
		CommandRaw:        "echo hello",
		CommandExitCode:   0,
		CommandStdout:     "hello",
		CommandStderr:     "",
	}
	eventID, err := ass.RecordEvent(event)
	require.NoError(t, err)
	require.Positive(t, eventID)

	require.NoError(t, ass.RecordEvents([]*Event{
		{
			OperatorSessionID: "session-1",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "batch event",
			CommandRaw:        "echo batch",
			CommandExitCode:   0,
			CommandStdout:     "batch",
		},
	}))

	events, err := ass.GetEvents(models.AuditScope{OperatorSessionID: "session-1"}, 10, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)

	listedEvents, err := ass.ListEvents("session-1", 10, 0)
	require.NoError(t, err)
	require.Len(t, listedEvents, 2)

	mutation := &FileMutationLog{
		EventID:          eventID,
		Filepath:         "data/example.txt",
		Operation:        FileMutationWrite,
		LedgerHashBefore: "before",
		LedgerHashAfter:  "after",
		DiffStat:         "+1",
	}
	require.NoError(t, ass.RecordFileMutation(mutation))
	mutations, err := ass.GetFileMutations(eventID)
	require.NoError(t, err)
	require.Len(t, mutations, 1)
	assert.Equal(t, "data/example.txt", mutations[0].Filepath)

	allMutations, err := ass.ListFileMutations(10, 0)
	require.NoError(t, err)
	require.NotEmpty(t, allMutations)

	sessions, err := ass.ListSessions(10, 0)
	require.NoError(t, err)
	require.NotEmpty(t, sessions)

	record := sampleEventReceiptRecord(t, "session-1")
	require.NoError(t, ass.RecordEventReceipt(record))

	persisted, err := ass.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.Equal(t, record.TransactionID, persisted.TransactionID)
	require.NotNil(t, persisted.EventReceipt)
	assert.True(t, proto.Equal(record.EventReceipt, persisted.EventReceipt))

	correlated, err := ass.GetEventReceiptByInvestigationID(record.InvestigationID, record.ActionType)
	require.NoError(t, err)
	require.NotNil(t, correlated)
	assert.Equal(t, record.TransactionID, correlated.TransactionID)

	eventReceipts, err := ass.ListEventReceipts(models.AuditScope{OperatorSessionID: "session-1"}, 10, 0)
	require.NoError(t, err)
	require.Len(t, eventReceipts, 1)

	sinceEventReceipts, err := ass.ListEventReceiptsSince(time.Now().UTC().Add(-time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, sinceEventReceipts, 1)

	missing, err := ass.GetEventReceipt("missing-transaction")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

func TestSQLAuditStore_DocSetAndDocDelete(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	require.NoError(t, ass.CreateSession("docset-session", constants.SessionTypeOperator, "DocSet", "user-1"))
	record := sampleEventReceiptRecord(t, "docset-session")
	payload, err := json.Marshal(record)
	require.NoError(t, err)

	require.NoError(t, ass.DocSet(t.Context(), "ignored", "ignored", payload))
	require.NoError(t, ass.DocDelete(t.Context(), "ignored", "ignored"))

	persisted, err := ass.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.NotNil(t, persisted)

	err = ass.DocSet(t.Context(), "ignored", "ignored", json.RawMessage("{invalid"))
	require.Error(t, err)
}

func TestParseStoredEventReceipt_InvalidCanonicalPayload(t *testing.T) {
	eventReceipt, err := parseStoredEventReceipt("{not-canonical")
	require.Error(t, err)
	assert.Nil(t, eventReceipt)

	empty, err := parseStoredEventReceipt("")
	require.NoError(t, err)
	assert.Nil(t, empty)
}

func TestSQLAuditStore_RecordEventReceiptUnknownSessionIsRejected(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	record := sampleEventReceiptRecord(t, "")
	record.TransactionID = "tx-unknown-session"
	record.OperatorSessionID = "unknown-session"
	err := ass.RecordEventReceipt(record)
	require.ErrorIs(t, err, constants.ErrAuditSessionUnknown)
	require.ErrorIs(t, err, constants.ErrAuditStoreRecordEventReceiptFailed)

	session, err := ass.GetOperatorSession("unknown-session")
	require.NoError(t, err)
	assert.Nil(t, session, "a rejected event receipt must not create its session")
	persisted, err := ass.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	assert.Nil(t, persisted)
}

func TestSQLAuditStore_RecordEventReceiptStartedSessionPersists(t *testing.T) {
	ass := newIntegrationAuditStore(t)

	record := sampleEventReceiptRecord(t, "")
	record.TransactionID = "tx-started-session"
	record.OperatorSessionID = "started-session"
	require.NoError(t, ass.CreateSession(record.OperatorSessionID, constants.SessionTypeOperator, "Operator Session", record.OperatorID))
	record.EventReceipt = &operatorv1.EventReceipt{
		TransactionId:   record.TransactionID,
		TransactionHash: record.TransactionHash,
		Status:          operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED,
	}
	body, err := compliancev1.MarshalCanonical(record.EventReceipt)
	require.NoError(t, err)
	record.EventReceipt = nil
	require.NoError(t, ass.RecordEventReceipt(record))

	var seq int64
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT seq FROM events WHERE transaction_id = ? AND type = ?`, record.TransactionID, string(constants.EventOperatorEventReceiptRecorded)).Scan(&seq))
	encryptedBody, err := encryptAuditContent(ass.encryptionVault, auditColumnEventContentText, auditEventRowID(seq), string(body))
	require.NoError(t, err)
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE events SET content_text = ? WHERE seq = ?`, encryptedBody, seq)
	require.NoError(t, err)

	persisted, err := ass.GetEventReceipt(record.TransactionID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.NotNil(t, persisted.EventReceipt)
	assert.Equal(t, record.TransactionID, persisted.EventReceipt.GetTransactionId())
}

func TestSQLAuditStore_VerifyChain_AppendAndTamper(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("chain-session", constants.SessionTypeOperator, "Chain Session", "user-1"))

	events := []*Event{
		{
			OperatorSessionID: "chain-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "first",
			CommandExitCode:   constants.ExitCodeNone,
		},
		{
			OperatorSessionID: "chain-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.AIMsg,
			ContentText:       "second",
			CommandExitCode:   constants.ExitCodeNone,
		},
	}
	for _, event := range events {
		_, err := ass.RecordEvent(event)
		require.NoError(t, err)
	}

	require.NoError(t, ass.VerifyChain(context.Background(), 0))

	_, err := ass.db.ExecWithRetry(t.Context(), `UPDATE events SET content_digest = ? WHERE seq = 1`, "tampered")
	require.NoError(t, err)
	err = ass.VerifyChain(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hash mismatch at seq 1")
}

func TestPruneChainedAuditEvents_WritesCheckpoint(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("prune-session", constants.SessionTypeOperator, "Prune", "user-prune"))

	oldTimestamp := time.Now().UTC().AddDate(0, 0, -120)
	_, err := ass.RecordEvent(&Event{
		OperatorSessionID: "prune-session",
		Timestamp:         oldTimestamp,
		Type:              constants.Event.Operator.Audit.Command,
		ContentText:       "old event",
		CommandExitCode:   constants.ExitCodeNone,
	})
	require.NoError(t, err)

	cutoff := time.Now().UTC().AddDate(0, 0, -30).Format(time.RFC3339Nano)
	require.NoError(t, PruneChainedAuditEvents(context.Background(), ass.db, ass.logger, ass.encryptionVault, cutoff))

	var checkpointCount int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM audit_chain_checkpoints`).Scan(&checkpointCount))
	assert.Positive(t, checkpointCount)

	var remaining int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE type != ?`, string(constants.EventPlatformAuditChainCheckpointed)).Scan(&remaining))
	assert.Zero(t, remaining)

	require.NoError(t, ass.VerifyChain(context.Background(), 0))
}

func TestSQLAuditStore_VerifyChain_PrevHashTamper(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("prevhash-session", constants.SessionTypeOperator, "PrevHash", "user-prev"))

	for i := 0; i < 3; i++ {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "prevhash-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "event-" + string(rune('a'+i)),
			CommandExitCode:   constants.ExitCodeNone,
		})
		require.NoError(t, err)
	}
	require.NoError(t, ass.VerifyChain(context.Background(), 0))

	_, err := ass.db.ExecWithRetry(t.Context(), `UPDATE events SET prev_hash = ? WHERE seq = 2`, auditChainGenesisPrevHash)
	require.NoError(t, err)
	err = ass.VerifyChain(context.Background(), 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "prev_hash mismatch")
}

func TestSQLAuditStore_VerifyChain_SeqReorderTamper(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("reorder-session", constants.SessionTypeOperator, "Reorder", "user-reorder"))

	for i := 0; i < 3; i++ {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "reorder-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "ordered-" + string(rune('a'+i)),
			CommandExitCode:   constants.ExitCodeNone,
		})
		require.NoError(t, err)
	}
	require.NoError(t, ass.VerifyChain(context.Background(), 0))

	// Swap seq 1 and 2 while leaving stale prev_hash/hash columns — breaks linkage.
	_, err := ass.db.ExecWithRetry(t.Context(), `UPDATE events SET seq = 999 WHERE seq = 1`)
	require.NoError(t, err)
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE events SET seq = 1 WHERE seq = 2`)
	require.NoError(t, err)
	_, err = ass.db.ExecWithRetry(t.Context(), `UPDATE events SET seq = 2 WHERE seq = 999`)
	require.NoError(t, err)

	err = ass.VerifyChain(context.Background(), 0)
	require.Error(t, err)
}

func TestSQLAuditStore_VerifyChain_ConcurrentAppends(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("concurrent-session", constants.SessionTypeOperator, "Concurrent", "user-concurrent"))

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := ass.RecordEvent(&Event{
				OperatorSessionID: "concurrent-session",
				Timestamp:         time.Now().UTC(),
				Type:              constants.Event.Operator.Audit.Command,
				ContentText:       "concurrent-" + string(rune('a'+n)),
				CommandExitCode:   constants.ExitCodeNone,
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var count int
	require.NoError(t, ass.db.QueryRowWithRetry(t.Context(), `SELECT COUNT(*) FROM events WHERE seq IS NOT NULL`).Scan(&count))
	assert.Equal(t, workers, count)
	require.NoError(t, ass.VerifyChain(context.Background(), 0))
}

func TestSQLAuditStore_VerifyChain_SurvivesVaultRekey(t *testing.T) {
	tempDir := testutil.TempDir(t)
	oldKey := []byte("audit-chain-old-vault-key!!")
	newKey := []byte("audit-chain-new-vault-key!!")

	fileSvc := newTestFileSvc(t, tempDir)
	testVault := CreateTestVault(t, fileSvc, oldKey)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	config := DefaultAuditStoreConfig()
	config.EncryptionVault = testVault
	ass, err := NewSQLAuditStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)

	require.NoError(t, ass.CreateSession("rekey-chain-session", constants.SessionTypeOperator, "Rekey Chain", "user-rekey"))
	for i := 0; i < 2; i++ {
		_, err := ass.RecordEvent(&Event{
			OperatorSessionID: "rekey-chain-session",
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.Audit.Command,
			ContentText:       "pre-rekey-" + string(rune('a'+i)),
			CommandExitCode:   constants.ExitCodeNone,
		})
		require.NoError(t, err)
	}
	require.NoError(t, ass.VerifyChain(context.Background(), 0))

	require.NoError(t, ass.Close())
	testVault.Close()

	fileSvcForVault, err := fs.NewRuntimeFileService(tempDir, testutil.NewTestLogger())
	require.NoError(t, err)
	rekeyedVault, err := vault.NewVault(&vault.VaultConfig{
		FileSvc: fileSvcForVault,
		Logger:  testutil.NewTestLogger(),
	})
	require.NoError(t, err)
	require.NoError(t, rekeyedVault.Rekey(oldKey, newKey))
	require.NoError(t, rekeyedVault.Unlock(newKey))
	t.Cleanup(func() { rekeyedVault.Close() })

	config.EncryptionVault = rekeyedVault
	ass2, err := NewSQLAuditStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ass2.Close()) })

	require.NoError(t, ass2.VerifyChain(context.Background(), 0))

	_, err = ass2.RecordEvent(&Event{
		OperatorSessionID: "rekey-chain-session",
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.Audit.AIMsg,
		ContentText:       "post-rekey",
		CommandExitCode:   constants.ExitCodeNone,
	})
	require.NoError(t, err)
	require.NoError(t, ass2.VerifyChain(context.Background(), 0))
}

// TestSQLAuditStore_CloseDrainsInFlightWritersWithoutClosingDB verifies that
// Close() waits for in-flight writers to finish before returning, and does NOT
// close the caller's injected database connection pool.
func TestSQLAuditStore_CloseDrainsInFlightWritersWithoutClosingDB(t *testing.T) {
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	dbPath := fileSvc.Resolve(constants.CanonicalDBRelPath)
	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(dbPath), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	config := DefaultAuditStoreConfig()
	config.EncryptionVault = testVault

	ass, err := NewSQLAuditStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)

	// Simulate an in-flight writer registered with muWrites.
	ass.muWrites.Add(1)

	closed := make(chan error, 1)
	go func() {
		closed <- ass.Close()
	}()

	// Verify that Close() blocks while the writer is in-flight.
	select {
	case <-closed:
		t.Fatal("Close() returned while an in-flight writer was active")
	case <-time.After(50 * time.Millisecond):
		// Expected: Close() is waiting on ass.Wait() / muWrites.Wait().
	}

	// Release the in-flight writer.
	ass.muWrites.Done()

	// Now Close() should unblock promptly.
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for Close() to finish after in-flight writer completed")
	}

	// Crucial contract: The injected db MUST still be open and usable!
	var one int
	require.NoError(t, db.QueryRowWithRetry(context.Background(), "SELECT 1").Scan(&one))
	assert.Equal(t, 1, one, "injected database must remain open and queryable after auditStore.Close()")
}

// TestSQLAuditStore_RecordEventReceipt_RejectsEmptySession asserts every event receipt is filed
// under a session: an empty operator_session_id fails closed instead of storing NULL.
func TestSQLAuditStore_RecordEventReceipt_RejectsEmptySession(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	err := ass.RecordEventReceipt(sampleEventReceiptRecord(t, ""))
	require.ErrorIs(t, err, constants.ErrAuditSessionMissing)
}

// TestGetTransactionEventsReturnsTypedEventsInOrder asserts the transaction
// read returns only the requested type for that transaction, oldest first, decrypted.
func TestGetTransactionEventsReturnsTypedEventsInOrder(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	require.NoError(t, ass.CreateSession("votes-session", constants.SessionTypeOperator, "Votes", "user-votes"))
	record := func(tx string, eventType constants.EventType, content string) {
		_, err := ass.RecordEvent(&Event{
			TransactionID:     tx,
			OperatorSessionID: "votes-session",
			Timestamp:         time.Now().UTC(),
			Type:              eventType,
			ContentText:       content,
			CommandExitCode:   constants.ExitCodeNone,
		})
		require.NoError(t, err)
	}
	record("tx-a", constants.EventConsensusVoteRecorded, "vote-1")
	record("tx-a", constants.Event.Operator.Audit.Command, "other-type")
	record("tx-b", constants.EventConsensusVoteRecorded, "other-tx")
	record("tx-a", constants.EventConsensusVoteRecorded, "vote-2")

	events, err := ass.GetTransactionEvents("tx-a", constants.EventConsensusVoteRecorded)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "vote-1", events[0].ContentText)
	assert.Equal(t, "vote-2", events[1].ContentText)

	_, err = ass.GetTransactionEvents("", constants.EventConsensusVoteRecorded)
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

// TestAuditQueriesUseIndexes asserts the per-append event lookups and the
// acting-app audit filters stay on an index instead of scanning the audit tables.
func TestAuditQueriesUseIndexes(t *testing.T) {
	ass := newIntegrationAuditStore(t)
	eventReceiptsByApp, _ := eventReceiptScopeQuery(models.AuditScope{ActingAppID: "app"})
	for _, tc := range []struct {
		name, query string
		args        []any
		indexes     []string
	}{
		{"event_once", eventOnceQuery, []any{"tx", "type"}, []string{"idx_events_transaction_id_type"}},
		{"event_chain_meta", eventChainMetaQuery, []any{"tx"}, []string{"idx_events_transaction_id_type"}},
		{"transaction_events", eventSelectColumns + " WHERE " + eventTransactionTypePredicate + " ORDER BY id ASC", []any{"tx", "type"},
			[]string{"idx_events_transaction_id_type"}},
		{"events_by_app", "SELECT id FROM events WHERE " + eventActingAppPredicate + " ORDER BY timestamp DESC LIMIT 10", []any{"app"},
			[]string{"idx_events_event_receipt_acting_app", "idx_events_transaction_id_type"}},
		{"event_receipts_by_app", eventReceiptsByApp + " ORDER BY timestamp DESC LIMIT 10", []any{"app"}, []string{"idx_events_event_receipt_acting_app"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := ass.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+tc.query, tc.args...)
			require.NoError(t, err)
			defer rows.Close()
			var details []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
				details = append(details, detail)
			}
			require.NoError(t, rows.Err())
			plan := strings.Join(details, "\n")
			for _, index := range tc.indexes {
				assert.Contains(t, plan, index)
			}
			assert.NotContains(t, plan, "SCAN events")
			assert.NotContains(t, plan, "SCAN event receipts")
		})
	}
}

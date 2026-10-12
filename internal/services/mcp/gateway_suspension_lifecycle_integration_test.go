// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package mcp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	storage "github.com/zerotrustdata/ztd/internal/services/storage"
	"github.com/zerotrustdata/ztd/internal/services/storage/storagetest"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/internal/timesvc"
	commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

const lifecycleSession = "session-lifecycle"

// lifecycleStores is the real suspended-transaction store and audit store over one
// canonical database, the way the Gateway wires them.
type lifecycleStores struct {
	db        *sqliteutil.DB
	audit     *storage.SQLAuditStore
	suspended *storage.SuspendedTransactionService
}

func newLifecycleStores(t *testing.T) lifecycleStores {
	t.Helper()
	tempDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := storagetest.CreateTestVault(t, filepath.Join(tempDir, "vault"), privKey)

	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	audit, err := storage.NewSQLAuditStore(&storage.AuditStoreConfig{RetentionDays: 7, PruneIntervalMinutes: 60, EncryptionVault: testVault}, db, slog.Default(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = audit.Close() })
	require.NoError(t, audit.CreateSession(lifecycleSession, constants.SessionTypeOperator, "Lifecycle", "user@test.com"))

	suspended, err := storage.NewSuspendedTransactionService(db, testVault, slog.Default())
	require.NoError(t, err)
	return lifecycleStores{db: db, audit: audit, suspended: suspended}
}

func (s lifecycleStores) store(t *testing.T, hash string, expiresIn time.Duration) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, s.suspended.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
		TransactionHash:   hash,
		Envelope:          []byte(`{"id":"` + hash + `","operatorSessionId":"` + lifecycleSession + `"}`),
		ToolArguments:     []byte(`{"arg":"v"}`),
		CreatedAt:         now.Add(-10 * time.Minute),
		ExpiresAt:         now.Add(expiresIn),
		ToolName:          "test-tool",
		UserID:            "user-1",
		OperatorID:        "op-1",
		OperatorSessionID: lifecycleSession,
	}))
}

func (s lifecycleStores) expireNow(t *testing.T, hash string) {
	t.Helper()
	_, err := s.db.ExecWithRetry(context.Background(), "UPDATE suspended_transactions SET expires_at = ? WHERE transaction_hash = ?",
		timesvc.FormatTimestamp(time.Now().UTC().Add(-time.Minute)), hash)
	require.NoError(t, err)
}

func (s lifecycleStores) expiredEvents(t *testing.T, hash string) int {
	t.Helper()
	events, err := s.audit.GetEvents(models.AuditScope{OperatorSessionID: lifecycleSession}, 1000, 0)
	require.NoError(t, err)
	n := 0
	for _, e := range events {
		if e.Type == constants.Event.Operator.Notary.TransactionExpired && e.TransactionID == hash {
			n++
		}
	}
	return n
}

func (s lifecycleStores) remaining(t *testing.T, hash string) bool {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRowWithRetry(context.Background(), "SELECT COUNT(*) FROM suspended_transactions WHERE transaction_hash = ?", hash).Scan(&n))
	return n > 0
}

// flakySuspendedStore wraps the real store and injects failures.
type flakySuspendedStore struct {
	storage.SuspendedTransactionStore
	deleteErr        error        // DeleteSuspendedTransaction
	failExpiredDel   atomic.Int32 // number of upcoming DeleteExpiredSuspendedTransactions calls to fail
	getExpiredErr    error
	deleteExpiredErr error
}

func (f *flakySuspendedStore) DeleteSuspendedTransaction(ctx context.Context, hash string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	return f.SuspendedTransactionStore.DeleteSuspendedTransaction(ctx, hash)
}

func (f *flakySuspendedStore) DeleteExpiredSuspendedTransactions(ctx context.Context, txs []*models.SuspendedTransaction) (int64, error) {
	if f.failExpiredDel.Load() > 0 {
		f.failExpiredDel.Add(-1)
		return 0, f.deleteExpiredErr
	}
	return f.SuspendedTransactionStore.DeleteExpiredSuspendedTransactions(ctx, txs)
}

func (f *flakySuspendedStore) GetExpiredSuspendedTransactions(ctx context.Context) ([]*models.SuspendedTransaction, error) {
	if f.getExpiredErr != nil {
		return nil, f.getExpiredErr
	}
	return f.SuspendedTransactionStore.GetExpiredSuspendedTransactions(ctx)
}

// One undecryptable row (or a locked vault) must not halt expiry of every other row: the
// sweep reads only the plaintext columns.
func TestRunMaintenanceSweep_UndecryptableRowDoesNotHaltExpiry(t *testing.T) {
	s := newLifecycleStores(t)
	s.store(t, "good", -time.Minute)
	s.store(t, "corrupt", -time.Minute)
	_, err := s.db.ExecWithRetry(context.Background(), "UPDATE suspended_transactions SET envelope = ?, tool_arguments = ? WHERE transaction_hash = ?",
		[]byte("not-a-valid-ciphertext"), []byte("not-a-valid-ciphertext"), "corrupt")
	require.NoError(t, err)

	g := newTestGatewayService(t, withSuspendedStore(s.suspended), withAuditStore(s.audit))
	require.NoError(t, g.runMaintenanceSweep(context.Background()))

	assert.Equal(t, 1, s.expiredEvents(t, "good"))
	assert.Equal(t, 1, s.expiredEvents(t, "corrupt"))
	assert.False(t, s.remaining(t, "good"))
	assert.False(t, s.remaining(t, "corrupt"))
}

// A delete that fails after the expiry was recorded must not make the next sweep record the
// same expiry again.
func TestRunMaintenanceSweep_RecordsExpiryOnceWhenDeleteFails(t *testing.T) {
	s := newLifecycleStores(t)
	s.store(t, "tx-expired", -time.Minute)
	deleteFailure := errors.New("disk I/O error")
	flaky := &flakySuspendedStore{SuspendedTransactionStore: s.suspended, deleteExpiredErr: deleteFailure}
	flaky.failExpiredDel.Store(1)
	g := newTestGatewayService(t, withSuspendedStore(flaky), withAuditStore(s.audit))

	err := g.runMaintenanceSweep(context.Background())
	require.ErrorIs(t, err, deleteFailure, "the delete failure is returned with its cause")
	assert.Equal(t, 1, s.expiredEvents(t, "tx-expired"))
	assert.True(t, s.remaining(t, "tx-expired"), "the row stays until the delete succeeds")

	require.NoError(t, g.runMaintenanceSweep(context.Background()))
	assert.Equal(t, 1, s.expiredEvents(t, "tx-expired"), "the retry must not record a second expiry")
	assert.False(t, s.remaining(t, "tx-expired"))
	require.NoError(t, s.audit.VerifyChain(context.Background(), 0))
}

func TestRunMaintenanceSweep_FailsClosedAndKeepsTheCause(t *testing.T) {
	t.Run("missing store is an error", func(t *testing.T) {
		g := newTestGatewayService(t)
		g.suspendedStore = nil
		require.ErrorIs(t, g.runMaintenanceSweep(context.Background()), constants.ErrSuspendedStoreRequired)
	})

	t.Run("list failure wraps the cause", func(t *testing.T) {
		s := newLifecycleStores(t)
		cause := errors.New("database is locked")
		g := newTestGatewayService(t, withSuspendedStore(&flakySuspendedStore{SuspendedTransactionStore: s.suspended, getExpiredErr: cause}), withAuditStore(s.audit))
		err := g.runMaintenanceSweep(context.Background())
		require.ErrorIs(t, err, cause)
	})
}

func resumeFixture(t *testing.T, s lifecycleStores, store storage.SuspendedTransactionStore, proc *fakeEnvelopeProcessor) *GatewayService {
	t.Helper()
	return newTestGatewayService(t, withSuspendedStore(store), withAuditStore(s.audit), withEnvProc(proc))
}

// An executed transaction whose suspended record could not be deleted must never be recorded
// as expired: the claim taken before execution marks it as attempted, and the failed delete is
// returned to the caller with the receipt.
func TestResumeWithL3Proof_ExecutedTransactionIsNeverRecordedExpired(t *testing.T) {
	s := newLifecycleStores(t)
	s.store(t, "tx-executed", time.Hour)
	deleteFailure := errors.New("disk I/O error")
	flaky := &flakySuspendedStore{SuspendedTransactionStore: s.suspended, deleteErr: deleteFailure}
	receipt := &operatorv1.ActionReceipt{TransactionId: "tx-executed", Status: operatorv1.ExecutionStatus_EXECUTION_STATUS_COMPLETED}
	g := resumeFixture(t, s, flaky, &fakeEnvelopeProcessor{receipt: receipt})

	got, err := g.ResumeWithL3Proof(context.Background(), "tx-executed", "user-1", &commonv1.L3Proof{})
	require.ErrorIs(t, err, deleteFailure, "a failed delete after execution is propagated, not logged")
	assert.Same(t, receipt, got, "the signed receipt still reaches the caller")
	require.True(t, s.remaining(t, "tx-executed"))

	// A second resume cannot execute the same transaction again.
	_, err = g.ResumeWithL3Proof(context.Background(), "tx-executed", "user-1", &commonv1.L3Proof{})
	require.ErrorIs(t, err, constants.ErrSuspendedTransactionClaimed)

	// The record outlives its TTL; the sweep deletes it without ever recording an expiry.
	s.expireNow(t, "tx-executed")
	sweep := newTestGatewayService(t, withSuspendedStore(s.suspended), withAuditStore(s.audit))
	require.NoError(t, sweep.runMaintenanceSweep(context.Background()))
	assert.Zero(t, s.expiredEvents(t, "tx-executed"), "an executed transaction must never get transaction.expired")
	assert.False(t, s.remaining(t, "tx-executed"))
}

func TestResumeWithL3Proof_FailedExecutionReleasesTheClaim(t *testing.T) {
	s := newLifecycleStores(t)
	s.store(t, "tx-retry", time.Hour)
	execFailure := errors.New("proof rejected")
	proc := &fakeEnvelopeProcessor{err: execFailure}
	g := resumeFixture(t, s, s.suspended, proc)

	_, err := g.ResumeWithL3Proof(context.Background(), "tx-retry", "user-1", &commonv1.L3Proof{})
	require.ErrorIs(t, err, execFailure)
	require.True(t, s.remaining(t, "tx-retry"), "the suspension stays so the proof can be retried")

	proc.err = nil
	proc.receipt = &operatorv1.ActionReceipt{TransactionId: "tx-retry"}
	_, err = g.ResumeWithL3Proof(context.Background(), "tx-retry", "user-1", &commonv1.L3Proof{})
	require.NoError(t, err, "the released claim lets the retry execute")
	assert.False(t, s.remaining(t, "tx-retry"))
}

func TestResumeWithL3Proof_ReleaseFailureIsJoined(t *testing.T) {
	store := &fakeSuspendedStore{releaseErr: errors.New("release failed")}
	require.NoError(t, store.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
		TransactionHash: "tx-1", OperatorSessionID: lifecycleSession,
		Envelope:  []byte(`{"id":"tx-1"}`),
		ExpiresAt: time.Now().Add(time.Hour),
	}))
	execFailure := errors.New("exec failed")
	g := newTestGatewayService(t, withSuspendedStore(store), withEnvProc(&fakeEnvelopeProcessor{err: execFailure}))

	_, err := g.ResumeWithL3Proof(context.Background(), "tx-1", "user-1", &commonv1.L3Proof{})
	require.ErrorIs(t, err, execFailure)
	require.ErrorIs(t, err, store.releaseErr)
}

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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

func storeExpiredTx(t *testing.T, sts *SuspendedTransactionService, hash string) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, sts.StoreSuspendedTransaction(context.Background(), &models.SuspendedTransaction{
		TransactionHash:   hash,
		OperatorSessionID: "operator-session-" + hash,
		Envelope:          []byte(`{"secret":"envelope-content-` + hash + `"}`),
		ToolArguments:     []byte(`{"path":"/etc/secret-` + hash + `"}`),
		CreatedAt:         now.Add(-2 * time.Hour),
		ExpiresAt:         now.Add(-time.Hour),
		ToolName:          "tool-" + hash,
		UserID:            "user-1",
		OperatorID:        "op-1",
	}))
}

// The expiry sweep must work from plaintext columns only: one undecryptable row, or a
// locked vault, may not halt expiry of every other row.
func TestGetExpiredSuspendedTransactions_ReadsOnlyPlaintextColumns(t *testing.T) {
	sts, db, testVault := newSuspendedStoreWithVault(t)
	ctx := context.Background()
	storeExpiredTx(t, sts, "good")
	storeExpiredTx(t, sts, "bad")

	_, err := db.ExecWithRetry(ctx, "UPDATE suspended_transactions SET envelope = ?, tool_arguments = ? WHERE transaction_hash = ?",
		[]byte("not-a-valid-ciphertext"), []byte("not-a-valid-ciphertext"), "bad")
	require.NoError(t, err)

	t.Run("one undecryptable row does not block the read", func(t *testing.T) {
		txs, err := sts.GetExpiredSuspendedTransactions(ctx)
		require.NoError(t, err)
		require.Len(t, txs, 2)
		for _, tx := range txs {
			assert.Empty(t, tx.Envelope, "content columns are never read by the expiry query")
			assert.Empty(t, tx.ToolArguments)
			assert.NotEmpty(t, tx.TransactionHash)
			assert.Equal(t, "operator-session-"+tx.TransactionHash, tx.OperatorSessionID)
			assert.Equal(t, "tool-"+tx.TransactionHash, tx.ToolName)
			assert.False(t, tx.ExpiresAt.IsZero())
		}
	})

	t.Run("a locked vault does not block the read", func(t *testing.T) {
		testVault.Lock()
		txs, err := sts.GetExpiredSuspendedTransactions(ctx)
		require.NoError(t, err)
		assert.Len(t, txs, 2)
	})
}

func TestClaimSuspendedTransaction(t *testing.T) {
	sts, _, _ := newSuspendedStoreWithVault(t)
	ctx := context.Background()
	now := time.Now().UTC()
	require.NoError(t, sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "live",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte(`{"id":"1"}`),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
	}))
	storeExpiredTx(t, sts, "expired")

	require.NoError(t, sts.ClaimSuspendedTransaction(ctx, "live"))
	require.ErrorIs(t, sts.ClaimSuspendedTransaction(ctx, "live"), constants.ErrSuspendedTransactionClaimed, "a second claim must not win")
	require.ErrorIs(t, sts.ClaimSuspendedTransaction(ctx, "missing"), constants.ErrSuspendedTransactionNotFound)
	require.ErrorIs(t, sts.ClaimSuspendedTransaction(ctx, "expired"), constants.ErrSuspendedTransactionNotFound, "an expired row cannot be claimed")

	tx, found, err := sts.GetSuspendedTransaction(ctx, "live")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, tx.ClaimedAt, "the claim is visible on the row")

	require.NoError(t, sts.ReleaseSuspendedTransaction(ctx, "live"))
	tx, found, err = sts.GetSuspendedTransaction(ctx, "live")
	require.NoError(t, err)
	require.True(t, found)
	assert.Nil(t, tx.ClaimedAt)
	require.NoError(t, sts.ClaimSuspendedTransaction(ctx, "live"), "a released row can be claimed again")
	require.NoError(t, sts.ReleaseSuspendedTransaction(ctx, "never-existed"), "releasing a deleted row is not an error")
}

func TestGetExpiredSuspendedTransactions_ReportsClaimedRows(t *testing.T) {
	sts, db, _ := newSuspendedStoreWithVault(t)
	ctx := context.Background()
	storeExpiredTx(t, sts, "claimed")
	storeExpiredTx(t, sts, "unclaimed")
	_, err := db.ExecWithRetry(ctx, "UPDATE suspended_transactions SET claimed_at = ? WHERE transaction_hash = ?", "2026-10-12T00:00:00.000Z", "claimed")
	require.NoError(t, err)

	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.NoError(t, err)
	byHash := map[string]*models.SuspendedTransaction{}
	for _, tx := range txs {
		byHash[tx.TransactionHash] = tx
	}
	require.Contains(t, byHash, "claimed")
	require.Contains(t, byHash, "unclaimed")
	assert.NotNil(t, byHash["claimed"].ClaimedAt)
	assert.Nil(t, byHash["unclaimed"].ClaimedAt)
}

func TestSuspendedTransactionStore_TypedSentinels(t *testing.T) {
	sts, _, _ := newSuspendedStoreWithVault(t)
	ctx := context.Background()

	err := sts.ApproveSuspendedTransaction(ctx, "missing", models.ApprovalProof{})
	require.ErrorIs(t, err, constants.ErrSuspendedTransactionNotFound)

	var nilStore *SuspendedTransactionService
	require.ErrorIs(t, nilStore.ClaimSuspendedTransaction(ctx, "x"), constants.ErrSuspendedStoreNotInitialized)
	require.ErrorIs(t, nilStore.ReleaseSuspendedTransaction(ctx, "x"), constants.ErrSuspendedStoreNotInitialized)
}

//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
)

// TestSuspendedTransactionContentEncryptedAtRest stores a suspended transaction,
// opens the table raw, and asserts the envelope and tool_arguments bytes on disk
// hold no plaintext while the store still returns the original content.
func TestSuspendedTransactionContentEncryptedAtRest(t *testing.T) {
	sts, db, _ := newSuspendedStoreWithVault(t)
	ctx := t.Context()

	const marker = "SUSPENDED-PLAINTEXT-MARKER-7f3a"
	envelope := []byte("envelope-" + marker)
	toolArgs := []byte(`{"command":"rm -rf /tmp/` + marker + `"}`)
	now := time.Now().UTC()
	tx := &models.SuspendedTransaction{
		TransactionHash:   "tx-encrypted-1",
		OperatorSessionID: "operator-session-1",
		Envelope:          envelope,
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		ToolName:          "run_command",
		ToolArguments:     toolArgs,
		UserID:            "user-1",
		OperatorID:        "op-1",
	}
	require.NoError(t, sts.StoreSuspendedTransaction(ctx, tx))

	var rawEnvelope, rawToolArgs []byte
	require.NoError(t, db.QueryRowWithRetry(ctx,
		"SELECT envelope, tool_arguments FROM suspended_transactions WHERE transaction_hash = ?", tx.TransactionHash,
	).Scan(&rawEnvelope, &rawToolArgs))
	require.NotEmpty(t, rawEnvelope)
	require.NotEmpty(t, rawToolArgs)
	require.False(t, bytes.Contains(rawEnvelope, []byte(marker)), "envelope column holds plaintext")
	require.False(t, bytes.Contains(rawToolArgs, []byte(marker)), "tool_arguments column holds plaintext")
	require.False(t, bytes.Contains(rawToolArgs, []byte("command")), "tool_arguments column holds plaintext")

	got, found, err := sts.GetSuspendedTransaction(ctx, tx.TransactionHash)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, envelope, []byte(got.Envelope))
	require.Equal(t, toolArgs, []byte(got.ToolArguments))

	listed, err := sts.ListSuspendedTransactions(ctx, "")
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, envelope, []byte(listed[0].Envelope))
	require.Equal(t, toolArgs, []byte(listed[0].ToolArguments))
}

// TestSuspendedTransactionLockedVaultFailsClosed asserts a locked vault fails both the
// write and the read with the typed sentinel, and a nil vault fails construction.
func TestSuspendedTransactionLockedVaultFailsClosed(t *testing.T) {
	sts, db, v := newSuspendedStoreWithVault(t)
	ctx := t.Context()

	now := time.Now().UTC()
	stored := &models.SuspendedTransaction{
		TransactionHash:   "tx-locked-1",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("envelope-before-lock"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		ToolArguments:     []byte(`{"a":"b"}`),
	}
	require.NoError(t, sts.StoreSuspendedTransaction(ctx, stored))

	v.Lock()

	err := sts.StoreSuspendedTransaction(ctx, &models.SuspendedTransaction{
		TransactionHash:   "tx-locked-2",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("envelope-after-lock"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
	})
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked, "store: %v", err)

	var count int
	require.NoError(t, db.QueryRowWithRetry(ctx,
		"SELECT COUNT(*) FROM suspended_transactions WHERE transaction_hash = ?", "tx-locked-2",
	).Scan(&count))
	require.Zero(t, count, "failed write must not leave a row")

	_, _, err = sts.GetSuspendedTransaction(ctx, stored.TransactionHash)
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked, "get: %v", err)

	_, err = sts.ListSuspendedTransactions(ctx, "")
	require.ErrorIs(t, err, constants.ErrAuditStoreVaultLocked, "list: %v", err)

	_, err = NewSuspendedTransactionService(db, nil, nil)
	require.ErrorIs(t, err, constants.ErrAuditStoreEncryptionVaultRequired, "nil vault: %v", err)
}

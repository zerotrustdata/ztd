// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newSuspendedStoreWithVault opens a real ztd.db-style database with an unlocked test
// vault and builds the suspended transaction store over them.
func newSuspendedStoreWithVault(t *testing.T) (*SuspendedTransactionService, *sqliteutil.DB, *vault.Vault) {
	t.Helper()

	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)

	db, err := sqliteutil.OpenDB(sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	sts, err := NewSuspendedTransactionService(db, testVault, testutil.NewTestLogger())
	require.NoError(t, err)
	require.NotNil(t, sts)
	return sts, db, testVault
}

// setupTestSuspendedTransactionStore creates a real SuspendedTransactionService over a temporary database.
func setupTestSuspendedTransactionStore(t *testing.T) *SuspendedTransactionService {
	t.Helper()
	sts, _, _ := newSuspendedStoreWithVault(t)
	return sts
}

func TestNewSuspendedTransactionService_NilDB(t *testing.T) {
	t.Parallel()

	_, _, v := newSuspendedStoreWithVault(t)
	sts, err := NewSuspendedTransactionService(nil, v, testutil.NewTestLogger())

	require.Error(t, err)
	assert.Nil(t, sts)
	assert.ErrorIs(t, err, constants.ErrMissingRequiredField)
}

func TestNewSuspendedTransactionService_NilVault(t *testing.T) {
	t.Parallel()

	_, db, _ := newSuspendedStoreWithVault(t)
	sts, err := NewSuspendedTransactionService(db, nil, testutil.NewTestLogger())

	require.Error(t, err)
	assert.Nil(t, sts)
	assert.ErrorIs(t, err, constants.ErrAuditStoreEncryptionVaultRequired)
}

func TestStoreSuspendedTransaction_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()
	tx := &models.SuspendedTransaction{}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestStoreSuspendedTransaction_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()
	tx := &models.SuspendedTransaction{}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestStoreSuspendedTransaction_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	tx := &models.SuspendedTransaction{
		TransactionHash:         "test-hash-123",
		OperatorSessionID:       "operator-session-1",
		Envelope:                []byte("test-envelope"),
		CreatedAt:               time.Now().UTC(),
		ExpiresAt:               time.Now().UTC().Add(time.Hour),
		ToolName:                "test_tool",
		ToolArguments:           []byte(`{"arg": "value"}`),
		UserID:                  "user-123",
		OperatorID:              "operator-456",
		Approved:                false,
		ExpectedCertFingerprint: "cert-fingerprint",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)
}

func TestStoreSuspendedTransaction_RejectsMissingRequiredFields(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name string
		tx   *models.SuspendedTransaction
	}{
		{
			name: "empty operator session id",
			tx: &models.SuspendedTransaction{
				TransactionHash: "missing-session",
				Envelope:        []byte("test-envelope"),
				CreatedAt:       now,
				ExpiresAt:       now.Add(time.Hour),
			},
		},
		{
			name: "empty envelope",
			tx: &models.SuspendedTransaction{
				TransactionHash:   "missing-envelope",
				OperatorSessionID: "operator-session-1",
				CreatedAt:         now,
				ExpiresAt:         now.Add(time.Hour),
			},
		},
		{name: "nil transaction", tx: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sts := setupTestSuspendedTransactionStore(t)
			ctx := context.Background()

			err := sts.StoreSuspendedTransaction(ctx, tt.tx)
			require.ErrorIs(t, err, constants.ErrMissingRequiredField)

			if tt.tx != nil {
				_, found, getErr := sts.GetSuspendedTransaction(ctx, tt.tx.TransactionHash)
				require.NoError(t, getErr)
				assert.False(t, found, "a rejected transaction must not be stored")
			}
		})
	}
}

func TestStoreSuspendedTransaction_UpdateExisting(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	tx := &models.SuspendedTransaction{
		TransactionHash:         "test-hash-update",
		OperatorSessionID:       "operator-session-1",
		Envelope:                []byte("original-envelope"),
		CreatedAt:               time.Now().UTC(),
		ExpiresAt:               time.Now().UTC().Add(time.Hour),
		ToolName:                "test_tool",
		ToolArguments:           []byte(`{"arg": "value"}`),
		UserID:                  "user-123",
		OperatorID:              "operator-456",
		Approved:                false,
		ExpectedCertFingerprint: "cert-fingerprint",
	}

	// Store initial transaction
	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Update the transaction
	tx.Envelope = []byte("updated-envelope")
	tx.ExpectedCertFingerprint = "updated-fingerprint"
	err = sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Verify the update
	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "test-hash-update")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "updated-envelope", string(retrieved.Envelope))
	assert.Equal(t, "updated-fingerprint", retrieved.ExpectedCertFingerprint)
}

func TestGetSuspendedTransaction_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	tx, found, err := sts.GetSuspendedTransaction(ctx, "test-hash")
	require.Error(t, err)
	assert.False(t, found)
	assert.Nil(t, tx)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestGetSuspendedTransaction_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	tx, found, err := sts.GetSuspendedTransaction(ctx, "test-hash")
	require.Error(t, err)
	assert.False(t, found)
	assert.Nil(t, tx)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestGetSuspendedTransaction_NotFound(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	tx, found, err := sts.GetSuspendedTransaction(ctx, "nonexistent-hash")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, tx)
}

func TestGetSuspendedTransaction_Expired(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	tx := &models.SuspendedTransaction{
		TransactionHash:   "expired-hash",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("test-envelope"),
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().UTC().Add(-time.Hour), // Already expired
		ToolName:          "test_tool",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Try to retrieve expired transaction
	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "expired-hash")
	require.NoError(t, err)
	assert.False(t, found, "expired transaction should not be found")
	assert.Nil(t, retrieved)
}

func TestGetSuspendedTransaction_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	approvedAt := now.Add(5 * time.Minute)

	tx := &models.SuspendedTransaction{
		TransactionHash:         "get-test-hash",
		OperatorSessionID:       "operator-session-1",
		Envelope:                []byte("test-envelope"),
		CreatedAt:               now,
		ExpiresAt:               now.Add(time.Hour),
		ToolName:                "test_tool",
		ToolArguments:           []byte(`{"arg": "value"}`),
		UserID:                  "user-123",
		OperatorID:              "operator-456",
		Approved:                true,
		ApprovedAt:              &approvedAt,
		ApprovedBy:              "approver-789",
		ApprovalSignature:       "signature-abc",
		ExpectedCertFingerprint: "cert-fingerprint",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Retrieve the transaction
	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "get-test-hash")
	require.NoError(t, err)
	assert.True(t, found)
	require.NotNil(t, retrieved)

	assert.Equal(t, "get-test-hash", retrieved.TransactionHash)
	assert.Equal(t, "test-envelope", string(retrieved.Envelope))
	assert.Equal(t, "test_tool", retrieved.ToolName)
	assert.Equal(t, `{"arg": "value"}`, string(retrieved.ToolArguments)) //nolint:testifylint // exact bytes, not JSON equivalence
	assert.Equal(t, "user-123", retrieved.UserID)
	assert.Equal(t, "operator-456", retrieved.OperatorID)
	assert.True(t, retrieved.Approved)
	assert.Equal(t, "approver-789", retrieved.ApprovedBy)
	assert.Equal(t, "signature-abc", retrieved.ApprovalSignature)
	assert.Equal(t, "cert-fingerprint", retrieved.ExpectedCertFingerprint)
	assert.NotNil(t, retrieved.ApprovedAt)
}

func TestListSuspendedTransactions_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	txs, err := sts.ListSuspendedTransactions(ctx, "user-123")
	require.Error(t, err)
	assert.Nil(t, txs)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestListSuspendedTransactions_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	txs, err := sts.ListSuspendedTransactions(ctx, "user-123")
	require.Error(t, err)
	assert.Nil(t, txs)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestListSuspendedTransactions_Empty(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	txs, err := sts.ListSuspendedTransactions(ctx, "")
	require.NoError(t, err)
	// Accept either nil or empty slice
	if txs != nil {
		assert.Empty(t, txs)
	}
}

func TestListSuspendedTransactions_All(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create multiple transactions for different users
	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:   "hash-1",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-1"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "hash-2",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-2"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-2",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "hash-3",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-3"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-2",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	// List all transactions
	txs, err := sts.ListSuspendedTransactions(ctx, "")
	require.NoError(t, err)
	assert.Len(t, txs, 3)
}

func TestListSuspendedTransactions_ByUser(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create transactions for different users
	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:   "hash-user-1",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-1"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "hash-user-2",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-2"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-2",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "hash-user-3",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("env-3"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-2",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	// List transactions for user-1 only
	txs, err := sts.ListSuspendedTransactions(ctx, "user-1")
	require.NoError(t, err)
	assert.Len(t, txs, 2)

	// Verify all returned transactions belong to user-1
	for _, tx := range txs {
		assert.Equal(t, "user-1", tx.UserID)
	}
}

func TestListSuspendedTransactions_ExcludesExpired(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create active and expired transactions
	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:   "active-hash",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("active-env"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "expired-hash",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("expired-env"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(-time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	// List should only return active transactions
	txs, err := sts.ListSuspendedTransactions(ctx, "")
	require.NoError(t, err)
	assert.Len(t, txs, 1)
	assert.Equal(t, "active-hash", txs[0].TransactionHash)
}

func TestApproveSuspendedTransaction_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	err := sts.ApproveSuspendedTransaction(ctx, "hash", models.ApprovalProof{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestApproveSuspendedTransaction_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	err := sts.ApproveSuspendedTransaction(ctx, "hash", models.ApprovalProof{})
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestApproveSuspendedTransaction_NotFound(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	err := sts.ApproveSuspendedTransaction(ctx, "nonexistent-hash", models.ApprovalProof{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction not found or expired")
}

func TestApproveSuspendedTransaction_Expired(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	tx := &models.SuspendedTransaction{
		TransactionHash:   "expired-approval-hash",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(-time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Try to approve expired transaction
	err = sts.ApproveSuspendedTransaction(ctx, "expired-approval-hash", models.ApprovalProof{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction not found or expired")
}

func TestApproveSuspendedTransaction_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	tx := &models.SuspendedTransaction{
		TransactionHash:   "approval-test-hash",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
		Approved:          false,
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Approve the transaction
	proof := models.ApprovalProof{
		ApprovedBy:        "approver-123",
		CliSignature:      "signature-abc",
		CertFingerprint:   "fingerprint-xyz",
		ApprovalPublicKey: "pubkey-abc",
		CredentialID:      "cred-id-1",
		ClientDataJSON:    "client-data",
		AuthenticatorData: "auth-data",
		Signature:         "passkey-sig",
	}
	err = sts.ApproveSuspendedTransaction(ctx, "approval-test-hash", proof)
	require.NoError(t, err)

	// Verify the approval
	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "approval-test-hash")
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, retrieved.Approved)
	assert.Equal(t, "approver-123", retrieved.ApprovedBy)
	assert.Equal(t, "signature-abc", retrieved.ApprovalSignature)
	assert.Equal(t, "fingerprint-xyz", retrieved.ExpectedCertFingerprint)
	assert.Equal(t, "pubkey-abc", retrieved.ApprovalPublicKey)
	assert.Equal(t, "cred-id-1", retrieved.PasskeyCredentialID)
	assert.Equal(t, "client-data", retrieved.PasskeyClientDataJSON)
	assert.Equal(t, "auth-data", retrieved.PasskeyAuthenticatorData)
	assert.Equal(t, "passkey-sig", retrieved.PasskeySignature)
	assert.NotNil(t, retrieved.ApprovedAt)
}

func TestDeleteSuspendedTransaction_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	err := sts.DeleteSuspendedTransaction(ctx, "hash")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestDeleteSuspendedTransaction_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	err := sts.DeleteSuspendedTransaction(ctx, "hash")
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestDeleteSuspendedTransaction_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	tx := &models.SuspendedTransaction{
		TransactionHash:   "delete-test-hash",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("env"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Verify it exists
	_, found, _ := sts.GetSuspendedTransaction(ctx, "delete-test-hash")
	assert.True(t, found)

	// Delete the transaction
	err = sts.DeleteSuspendedTransaction(ctx, "delete-test-hash")
	require.NoError(t, err)

	// Verify it's gone
	_, found, _ = sts.GetSuspendedTransaction(ctx, "delete-test-hash")
	assert.False(t, found)
}

func TestDeleteSuspendedTransaction_NonExistent(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	// Delete non-existent transaction (should not error)
	err := sts.DeleteSuspendedTransaction(ctx, "nonexistent-hash")
	require.NoError(t, err)
}

func TestDeleteExpiredSuspendedTransactions_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	count, err := sts.DeleteExpiredSuspendedTransactions(ctx, []*models.SuspendedTransaction{{TransactionHash: "h"}})
	require.Error(t, err)
	assert.Zero(t, count)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestDeleteExpiredSuspendedTransactions_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	count, err := sts.DeleteExpiredSuspendedTransactions(ctx, []*models.SuspendedTransaction{{TransactionHash: "h"}})
	require.Error(t, err)
	assert.Zero(t, count)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestDeleteExpiredSuspendedTransactions_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create active and expired transactions
	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:   "active-cleanup",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("active"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "expired-cleanup-1",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("expired1"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(-time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "expired-cleanup-2",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("expired2"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(-2 * time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	// Delete only the audited expired row; an active row passed in is kept, and
	// an expired row that was not passed in (not yet audited) is kept.
	count, err := sts.DeleteExpiredSuspendedTransactions(ctx, transactions[:2])
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	txs, err := sts.ListSuspendedTransactions(ctx, "")
	require.NoError(t, err)
	assert.Len(t, txs, 1)
	assert.Equal(t, "active-cleanup", txs[0].TransactionHash)

	expired, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.NoError(t, err)
	require.Len(t, expired, 1)
	assert.Equal(t, "expired-cleanup-2", expired[0].TransactionHash)
}

func TestDeleteExpiredSuspendedTransactions_NoExpired(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	tx := &models.SuspendedTransaction{
		TransactionHash:   "active-only",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("active"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		UserID:            "user-1",
		OperatorID:        "op-1",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// An active row is never deleted, even when passed in
	count, err := sts.DeleteExpiredSuspendedTransactions(ctx, []*models.SuspendedTransaction{tx})
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestGetExpiredSuspendedTransactions_NilStore(t *testing.T) {
	t.Parallel()

	var sts *SuspendedTransactionService
	ctx := context.Background()

	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.Error(t, err)
	assert.Nil(t, txs)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestGetExpiredSuspendedTransactions_NilDB(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{
		db: nil,
	}
	ctx := context.Background()

	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.Error(t, err)
	assert.Nil(t, txs)
	assert.ErrorIs(t, err, constants.ErrSuspendedStoreNotInitialized)
}

func TestGetExpiredSuspendedTransactions_Empty(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.NoError(t, err)
	// Accept either nil or empty slice
	if txs != nil {
		assert.Empty(t, txs)
	}
}

func TestGetExpiredSuspendedTransactions_Success(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Create active and expired transactions
	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:   "active-expired",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("active"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "expired-get-1",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("expired1"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(-time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
		{
			TransactionHash:   "expired-get-2",
			OperatorSessionID: "operator-session-1",
			Envelope:          []byte("expired2"),
			CreatedAt:         now,
			ExpiresAt:         now.Add(-2 * time.Hour),
			UserID:            "user-1",
			OperatorID:        "op-1",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	// Get expired transactions
	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.NoError(t, err)
	assert.Len(t, txs, 2)

	// Verify all are expired
	for _, tx := range txs {
		assert.True(t, tx.ExpiresAt.Before(now))
	}
}

func TestSuspendedTransactionService_WaitJoinsInFlightApproval(t *testing.T) {
	t.Parallel()

	sts := &SuspendedTransactionService{}
	sts.wg.Add(1)

	finished := make(chan struct{})
	go func() {
		sts.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("Wait returned while an approval write was still in flight")
	case <-time.After(30 * time.Millisecond):
	}

	sts.wg.Done()

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after the in-flight approval write finished")
	}
}

func TestSuspendedTransactionStore_FullWorkflow(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	// Step 1: Store a suspended transaction
	tx := &models.SuspendedTransaction{
		TransactionHash:   "workflow-hash",
		OperatorSessionID: "operator-session-1",
		Envelope:          []byte("workflow-envelope"),
		CreatedAt:         now,
		ExpiresAt:         now.Add(time.Hour),
		ToolName:          "workflow_tool",
		ToolArguments:     []byte(`{"arg": "value"}`),
		UserID:            "workflow-user",
		OperatorID:        "workflow-operator",
		Approved:          false,
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	// Step 2: Retrieve the transaction
	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "workflow-hash")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "workflow-hash", retrieved.TransactionHash)

	// Step 3: List transactions for the user
	txs, err := sts.ListSuspendedTransactions(ctx, "workflow-user")
	require.NoError(t, err)
	assert.Len(t, txs, 1)

	// Step 4: Approve the transaction
	err = sts.ApproveSuspendedTransaction(ctx, "workflow-hash", models.ApprovalProof{
		ApprovedBy:      "workflow-approver",
		CliSignature:    "workflow-sig",
		CertFingerprint: "workflow-fingerprint",
	})
	require.NoError(t, err)

	// Step 5: Verify approval
	retrieved, found, err = sts.GetSuspendedTransaction(ctx, "workflow-hash")
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, retrieved.Approved)
	assert.Equal(t, "workflow-approver", retrieved.ApprovedBy)

	// Step 6: Delete the transaction
	err = sts.DeleteSuspendedTransaction(ctx, "workflow-hash")
	require.NoError(t, err)

	// Step 7: Verify deletion
	_, found, _ = sts.GetSuspendedTransaction(ctx, "workflow-hash")
	assert.False(t, found)
}

func TestStoreAndGetSuspendedTransaction_SubmitterCLISessionID(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	tx := &models.SuspendedTransaction{
		TransactionHash:       "cli-session-roundtrip",
		OperatorSessionID:     "operator-session-1",
		Envelope:              []byte("test-envelope"),
		CreatedAt:             time.Now().UTC(),
		ExpiresAt:             time.Now().UTC().Add(time.Hour),
		ToolName:              "test_tool",
		UserID:                "user-123",
		OperatorID:            "operator-456",
		SubmitterCLISessionID: "cli-session-abc",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	retrieved, found, err := sts.GetSuspendedTransaction(ctx, "cli-session-roundtrip")
	require.NoError(t, err)
	assert.True(t, found)
	require.NotNil(t, retrieved)
	assert.Equal(t, "cli-session-abc", retrieved.SubmitterCLISessionID)
}

func TestListSuspendedTransactions_SubmitterCLISessionID(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	transactions := []*models.SuspendedTransaction{
		{
			TransactionHash:       "list-cli-1",
			OperatorSessionID:     "operator-session-1",
			Envelope:              []byte("env-1"),
			CreatedAt:             now,
			ExpiresAt:             now.Add(time.Hour),
			UserID:                "user-1",
			OperatorID:            "op-1",
			SubmitterCLISessionID: "cli-session-1",
		},
		{
			TransactionHash:       "list-cli-2",
			OperatorSessionID:     "operator-session-1",
			Envelope:              []byte("env-2"),
			CreatedAt:             now,
			ExpiresAt:             now.Add(time.Hour),
			UserID:                "user-1",
			OperatorID:            "op-2",
			SubmitterCLISessionID: "cli-session-2",
		},
	}

	for _, tx := range transactions {
		err := sts.StoreSuspendedTransaction(ctx, tx)
		require.NoError(t, err)
	}

	txs, err := sts.ListSuspendedTransactions(ctx, "user-1")
	require.NoError(t, err)
	assert.Len(t, txs, 2)

	for _, tx := range txs {
		assert.NotEmpty(t, tx.SubmitterCLISessionID, "ListSuspendedTransactions must populate SubmitterCLISessionID")
	}
}

func TestGetExpiredSuspendedTransactions_SubmitterCLISessionID(t *testing.T) {
	t.Parallel()

	sts := setupTestSuspendedTransactionStore(t)
	ctx := context.Background()

	now := time.Now().UTC()

	tx := &models.SuspendedTransaction{
		TransactionHash:       "expired-cli-session",
		OperatorSessionID:     "operator-session-1",
		Envelope:              []byte("expired-env"),
		CreatedAt:             now,
		ExpiresAt:             now.Add(-time.Hour),
		UserID:                "user-1",
		OperatorID:            "op-1",
		SubmitterCLISessionID: "cli-session-expired",
	}

	err := sts.StoreSuspendedTransaction(ctx, tx)
	require.NoError(t, err)

	txs, err := sts.GetExpiredSuspendedTransactions(ctx)
	require.NoError(t, err)
	require.Len(t, txs, 1)
	assert.Equal(t, "cli-session-expired", txs[0].SubmitterCLISessionID)
}

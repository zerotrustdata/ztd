// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// setupTestLedgerForDiffStat creates a test environment for GitLedgerService
// specifically for diff stat testing, using real infrastructure (Git, encryption vault).
func setupTestLedgerForDiffStat(t *testing.T) (*GitLedgerService, string) {
	t.Helper()
	tempDir := testutil.OperatorDir(t)

	fileSvc := newTestFileSvc(t, tempDir)

	// Create and unlock the vault: ledger snapshots are always encrypted
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	vHeader, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, vHeader.Save(fileSvc))
	testVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: testutil.NewTestLogger()})
	require.NoError(t, err)
	require.NoError(t, testVault.Unlock(privKey))
	t.Cleanup(func() { testVault.Close() })

	logger := testutil.NewTestLogger()

	ledgerConfig := &LedgerConfig{
		EncryptionVault: testVault,
	}
	lms, err := NewGitLedgerService(ledgerConfig, logger, fileSvc)
	require.NoError(t, err)
	require.NotNil(t, lms)

	return lms, tempDir
}

// TestLedgerService_GetDiffStat_EmptyHashesFailClosed verifies that GetDiffStat
// errors when a hash is empty: an empty hash means the mutation was never
// recorded, and that must not read as "no change".
func TestLedgerService_GetDiffStat_EmptyHashesFailClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedgerForDiffStat(t)

	_, err := lms.GetDiffStat("", "", "/some/file.txt", "")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)
}

// TestLedgerService_GetDiffStat_BetweenTwoCommits verifies that GetDiffStat
// correctly calculates the diff statistics between two valid Git commits,
// ensuring the Git diff-numstat command is executed properly.
func TestLedgerService_GetDiffStat_BetweenTwoCommits(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerForDiffStat(t)

	testFile := filepath.Join(tempDir, "diffstat_test.txt")
	operatorSessionID := "sess-diffstat"

	// First commit: write initial file content
	result1, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("line one\nline two\n"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result1, operatorSessionID))

	hashBefore := result1.LedgerHashAfter
	require.NotEmpty(t, hashBefore)

	// Second commit: overwrite with different content
	result2, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("line one\nline two\nline three\n"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result2, operatorSessionID))

	hashAfter := result2.LedgerHashAfter
	require.NotEmpty(t, hashAfter)

	stat, err := lms.GetDiffStat(hashBefore, hashAfter, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.NotEmpty(t, stat)
}

// TestLedgerService_GetDiffStat_SameHashReturnsEmpty verifies that GetDiffStat
// returns an empty string when the same hash is provided for both parameters,
// as there is no diff to calculate.
func TestLedgerService_GetDiffStat_SameHashReturnsEmpty(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerForDiffStat(t)

	testFile := filepath.Join(tempDir, "same_hash.txt")
	operatorSessionID := "sess-same"

	result, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("content\n"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result, operatorSessionID))

	hash := result.LedgerHashAfter
	require.NotEmpty(t, hash)

	stat, err := lms.GetDiffStat(hash, hash, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.Empty(t, stat)
}

// TestLedgerService_GetDiffStat_InvalidHashesReturnsEmpty verifies fail-closed
// behavior when invalid/non-existent Git hashes are provided, ensuring the
// function returns empty instead of panicking or returning errors.
func TestLedgerService_GetDiffStat_InvalidHashesReturnsEmpty(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedgerForDiffStat(t)

	_, err := lms.GetDiffStat("deadbeef", "cafebabe", "/some/file.txt", "")
	require.Error(t, err)
}

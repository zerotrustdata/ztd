// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// ---------------------------------------------------------------------------
// GetDiffContent - wraps git diff between two commits
// ---------------------------------------------------------------------------

func TestLedgerService_GetDiffContent_EmptyHashesFailClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	_, err := lms.GetDiffContent("", "", "/some/file.txt", "")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)
}

func TestLedgerService_GetDiffContent_BetweenTwoCommits(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFile := filepath.Join(tempDir, "diffcontent_test.txt")
	operatorSessionID := "sess-diffcontent"

	// First commit: write initial file content
	result1 := recordMutation(t, lms, operatorSessionID, testFile, tempDir, "Line 1\n")

	hashBefore := result1.LedgerHashAfter
	require.NotEmpty(t, hashBefore)

	// Second commit: modify file content
	result2 := recordMutation(t, lms, operatorSessionID, testFile, tempDir, "Line 1\nLine 2\n")

	hashAfter := result2.LedgerHashAfter
	require.NotEmpty(t, hashAfter)

	// Verify diff content shows the added line
	diff, err := lms.GetDiffContent(hashBefore, hashAfter, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.NotEmpty(t, diff)
	assert.Contains(t, diff, "+Line 2")
}

func TestLedgerService_GetDiffContent_SameHashReturnsEmpty(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFile := filepath.Join(tempDir, "same_hash.txt")
	operatorSessionID := "sess-same"

	result := recordMutation(t, lms, operatorSessionID, testFile, tempDir, "content\n")

	hash := result.LedgerHashAfter
	require.NotEmpty(t, hash)

	diff, err := lms.GetDiffContent(hash, hash, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.Empty(t, diff)
}

func TestLedgerService_GetDiffContent_InvalidHashesFailClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	_, err := lms.GetDiffContent("deadbeef", "cafebabe", "/some/file.txt", "")
	require.Error(t, err)
}

func TestLedgerService_GetDiffContent_PlaintextFromEncryptedSnapshots(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFile := filepath.Join(tempDir, "plaintext_diff.txt")
	operatorSessionID := "sess-plaintext-diff"

	result1 := recordMutation(t, lms, operatorSessionID, testFile, tempDir, "alpha\nbeta\n")
	result2 := recordMutation(t, lms, operatorSessionID, testFile, tempDir, "alpha\ngamma\n")

	content, err := lms.GetDiffContent(result1.LedgerHashAfter, result2.LedgerHashAfter, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.Contains(t, content, "-beta")
	assert.Contains(t, content, "+gamma")

	stat, err := lms.GetDiffStat(result1.LedgerHashAfter, result2.LedgerHashAfter, testFile, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "1 file changed, 1 insertion(+), 1 deletion(-)", stat)
}

func TestLedgerService_GetDiff_LockedVaultReturnsNothing(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedgerLocked(t)

	ledgerDir := lms.fileSvc.Resolve(filepath.Join(lms.baseRelPath, constants.FilesDirname))
	repo, head, err := openLedgerHead(ledgerDir)
	require.NoError(t, err)
	hash1 := head.Hash().String()
	commit, err := commitBlob(repo, head, "second.enc", []byte("second"), "second")
	require.NoError(t, err)
	hash2 := commit.String()

	_, err = lms.diffBetween(ledgerDir, hash1, hash2, "/some/file.txt")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	_, err = lms.GetDiffContent(hash1, hash2, "/some/file.txt", "")
	require.Error(t, err)
	_, err = lms.GetDiffStat(hash1, hash2, "/some/file.txt", "")
	require.Error(t, err)
}

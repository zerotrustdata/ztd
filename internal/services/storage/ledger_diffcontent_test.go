// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"os"
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
	result1, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("Line 1\n"), 0644))
	require.NoError(t, lms.CompleteMirrorCreate(result1, operatorSessionID))

	hashBefore := result1.LedgerHashAfter
	require.NotEmpty(t, hashBefore)

	// Second commit: modify file content
	result2, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("Line 1\nLine 2\n"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result2, operatorSessionID))

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

	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("content\n"), 0644))
	require.NoError(t, lms.CompleteMirrorCreate(result, operatorSessionID))

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

	result1, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("alpha\nbeta\n"), 0644))
	require.NoError(t, lms.CompleteMirrorCreate(result1, operatorSessionID))

	result2, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFile, []byte("alpha\ngamma\n"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result2, operatorSessionID))

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

	ledgerDir := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname))
	require.NoError(t, lms.initGitRepo(filepath.Join(constants.DataDirname, constants.LedgerDirname)))
	hash1, err := lms.snapshotLedger(ledgerDir, "first")
	require.NoError(t, err)
	hash2, err := lms.snapshotLedger(ledgerDir, "second")
	require.NoError(t, err)

	_, err = lms.diffBetween(ledgerDir, hash1, hash2, "/some/file.txt")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	_, err = lms.GetDiffContent(hash1, hash2, "/some/file.txt", "")
	require.Error(t, err)
	_, err = lms.GetDiffStat(hash1, hash2, "/some/file.txt", "")
	require.Error(t, err)
}

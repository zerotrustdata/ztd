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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// readLedgerFile reads a file from the .ztd/ runtime ledger tree via fileSvc,
// converting the absolute ledger path to a relative path. Used in tests that
// verify mirror content written by the ledger service.
func readLedgerFile(t *testing.T, fileSvc fs.RuntimeFileService, absPath string) []byte {
	t.Helper()
	relPath, err := fileSvc.Rel(absPath)
	require.NoError(t, err)
	content, err := fileSvc.ReadFile(context.Background(), relPath)
	require.NoError(t, err)
	return content
}

// readLedgerSnapshot reads the encrypted snapshot stored for absPath (logical path plus
// ".enc") and decrypts it with the service vault. Ledger content is never plaintext on disk.
func readLedgerSnapshot(t *testing.T, lms *GitLedgerService, absPath string) []byte {
	t.Helper()
	ciphertext := readLedgerFile(t, lms.fileSvc, absPath+ledgerSnapshotSuffix)
	plaintext, err := lms.config.EncryptionVault.DecryptWithAAD(ciphertext, ledgerSnapshotAAD(ledgerSnapshotGitPath(absPath)))
	require.NoError(t, err)
	return plaintext
}

// setupTestLedger creates a GitLedgerService backed by an unlocked test vault.
func setupTestLedger(t *testing.T) (*GitLedgerService, string) {
	t.Helper()
	return setupLedgerWithVault(t, true)
}

// setupTestLedgerLocked creates a GitLedgerService whose vault is initialized but locked.
func setupTestLedgerLocked(t *testing.T) (*GitLedgerService, string) {
	t.Helper()
	return setupLedgerWithVault(t, false)
}

// setupLedgerWithVault builds a GitLedgerService over a fresh vault, unlocked or locked.
func setupLedgerWithVault(t *testing.T, unlocked bool) (*GitLedgerService, string) {
	t.Helper()
	tempDir := testutil.OperatorDir(t)

	fileSvc := newTestFileSvc(t, tempDir)

	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	vHeader, _, err := vault.NewVaultHeader(privKey)
	require.NoError(t, err)
	require.NoError(t, vHeader.Save(fileSvc))
	testVault, err := vault.NewVault(&vault.VaultConfig{FileSvc: fileSvc, Logger: testutil.NewTestLogger()})
	require.NoError(t, err)
	if unlocked {
		require.NoError(t, testVault.Unlock(privKey))
	}
	t.Cleanup(func() { testVault.Close() })

	ledgerConfig := &LedgerConfig{
		EncryptionVault: testVault,
	}
	lms, err := NewGitLedgerService(ledgerConfig, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	require.NotNil(t, lms)

	return lms, tempDir
}

func TestLedgerService_NewService(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	assert.NotNil(t, lms)
	assert.NotNil(t, lms.config)
	assert.NotNil(t, lms.logger)
}

// The ledger is a required Operator dependency: construction without a config or
// without an encryption vault fails closed with a typed sentinel, never a degraded
// ledger that silently skips snapshots.
func TestNewGitLedgerService_RequiresConfigAndVault(t *testing.T) {
	t.Parallel()

	lms, err := NewGitLedgerService(nil, testutil.NewTestLogger(), nil)
	require.ErrorIs(t, err, constants.ErrLedgerConfigRequired)
	assert.Nil(t, lms)

	lms, err = NewGitLedgerService(&LedgerConfig{}, testutil.NewTestLogger(), nil)
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	assert.Nil(t, lms)
}

func TestLedgerService_NewServiceWithNilConfig(t *testing.T) {
	t.Parallel()
	lms, err := NewGitLedgerService(nil, testutil.NewTestLogger(), nil)
	require.Error(t, err)
	assert.Nil(t, lms)
}

func TestLedgerService_MirrorFileWrite_NewFile(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create a test file path (file doesn't exist yet)
	testFilePath := filepath.Join(tempDir, "test_write.txt")
	operatorSessionID := "test-session-write"

	// Start the mirror operation
	result, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, testFilePath, result.FilePath)
	assert.Equal(t, FileMutationWrite, result.Operation)
	assert.True(t, result.Success)
	assert.NotEmpty(t, result.LedgerPath)

	// The mirror path should be within the ledger
	assert.Contains(t, result.LedgerPath, "files")

	// Now create the actual file
	err = os.WriteFile(testFilePath, []byte("Hello, World!"), 0644)
	require.NoError(t, err)

	// Complete the mirror operation
	err = lms.CompleteMirrorWrite(result, operatorSessionID)
	require.NoError(t, err)

	assert.NotEmpty(t, result.LedgerHashAfter)
	assert.NotEmpty(t, result.DiffStat)

	// Verify file was copied to ledger
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, "Hello, World!", string(mirrorContent))
}

func TestLedgerService_MirrorFileWrite_ExistingFile(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create an existing file
	testFilePath := filepath.Join(tempDir, "existing_file.txt")
	err := os.WriteFile(testFilePath, []byte("Original content"), 0644)
	require.NoError(t, err)

	operatorSessionID := "test-session-existing"

	// Start the mirror operation (should backup existing file first)
	result, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.NotEmpty(t, result.LedgerHashBefore)
	assert.True(t, result.Success)

	// Modify the file
	err = os.WriteFile(testFilePath, []byte("Modified content"), 0644)
	require.NoError(t, err)

	// Complete the mirror operation
	err = lms.CompleteMirrorWrite(result, operatorSessionID)
	require.NoError(t, err)

	assert.NotEmpty(t, result.LedgerHashAfter)
	assert.NotEqual(t, result.LedgerHashBefore, result.LedgerHashAfter)

	// Verify mirror reflects updated content
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, "Modified content", string(mirrorContent))
}

func TestLedgerService_CompleteMirrorWrite_NilResult(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	err := lms.CompleteMirrorWrite(nil, "operator_session")
	require.NoError(t, err)
}

func TestLedgerService_MirrorFileCreate(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "new_created_file.txt")
	operatorSessionID := "test-session-create"

	// Start the create mirror operation
	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, testFilePath, result.FilePath)
	assert.Equal(t, FileMutationCreate, result.Operation)
	assert.NotEmpty(t, result.LedgerHashBefore)
	assert.True(t, result.Success)

	// Create the actual file
	content := "Line 1\nLine 2\nLine 3\n"
	err = os.WriteFile(testFilePath, []byte(content), 0644)
	require.NoError(t, err)

	// Complete the create mirror operation
	err = lms.CompleteMirrorCreate(result, operatorSessionID)
	require.NoError(t, err)

	assert.NotEmpty(t, result.LedgerHashAfter)
	assert.Contains(t, result.DiffStat, "new file")
	assert.Contains(t, result.DiffStat, "lines")

	// Verify file was copied to ledger
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, content, string(mirrorContent))
}

func TestLedgerService_GetLedgerPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	ledgerDir := filepath.Join(tempDir, "ledger")

	// The mirror path is a keyed name inside the ledger and carries no part of the host path
	ledgerPath, err := lms.getLedgerPath(ledgerDir, "/etc/nginx/nginx.conf")
	require.NoError(t, err)
	assert.Contains(t, ledgerPath, "files")
	assert.NotContains(t, ledgerPath, "nginx")
	assert.NotContains(t, ledgerPath, "etc")
	assert.NotContains(t, ledgerPath, "//")

	// The name is stable for a path and differs between paths
	again, err := lms.getLedgerPath(ledgerDir, "/etc/nginx/nginx.conf")
	require.NoError(t, err)
	assert.Equal(t, ledgerPath, again)
	other, err := lms.getLedgerPath(ledgerDir, "/etc/nginx/other.conf")
	require.NoError(t, err)
	assert.NotEqual(t, ledgerPath, other)

	// Test relative path (should be converted to absolute)
	ledgerPath, err = lms.getLedgerPath(ledgerDir, "relative/path/file.txt")
	require.NoError(t, err)
	assert.Contains(t, ledgerPath, "files")
	assert.NotContains(t, ledgerPath, "relative")
}

func TestLedgerService_GetLedgerPath_LockedVaultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerLocked(t)

	_, err := lms.getLedgerPath(filepath.Join(tempDir, "ledger"), "/etc/nginx/nginx.conf")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	_, err = lms.getGitRelativePath("/etc/nginx/nginx.conf")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
}

func TestLedgerService_CopyToLedger(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create source file
	srcPath := filepath.Join(tempDir, "source.txt")
	srcContent := "Source file content with special chars: äöü\n"
	err := os.WriteFile(srcPath, []byte(srcContent), 0644)
	require.NoError(t, err)

	// Copy to ledger — destination must be inside .ztd/ runtime dir
	dstPath := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname, "files", "test", "source.txt"))
	err = lms.copyToLedger(openTestTarget(t, lms.fileSvc, srcPath, tempDir), dstPath)
	require.NoError(t, err)

	// Verify copy
	dstContent := readLedgerSnapshot(t, lms, dstPath)
	assert.Equal(t, srcContent, string(dstContent))
}

func TestLedgerService_CopyToLedger_NonExistentSource(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	err := lms.copyToLedger(openTestTarget(t, lms.fileSvc, filepath.Join(tempDir, "nonexistent.txt"), tempDir), filepath.Join(tempDir, "dst.txt"))
	require.Error(t, err)
	assert.Error(t, err)
}

func TestLedgerService_SnapshotLedger(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	ledgerRelDir := filepath.Join(constants.DataDirname, constants.LedgerDirname)
	ledgerDir := lms.fileSvc.Resolve(ledgerRelDir)

	// Initialize git repository first
	err := lms.initGitRepo(ledgerRelDir)
	require.NoError(t, err, "failed to initialize git repo")

	// Take a snapshot
	hash1, err := lms.snapshotLedger(ledgerDir, "Test snapshot 1")
	require.NoError(t, err)
	assert.NotEmpty(t, hash1)
	assert.Len(t, hash1, 40) // Git SHA-1 hash length

	// Make a change and take another snapshot
	testFile := filepath.Join(ledgerDir, "files", "snapshot_test.txt")
	testFileRel, err := lms.fileSvc.Rel(testFile)
	require.NoError(t, err)
	require.NoError(t, lms.fileSvc.WriteFile(context.Background(), testFileRel, []byte("snapshot test"), 0644))

	hash2, err := lms.snapshotLedger(ledgerDir, "Test snapshot 2")
	require.NoError(t, err)
	assert.NotEmpty(t, hash2)
	assert.NotEqual(t, hash1, hash2)
}

// writeLedgerSnapshot writes content as an encrypted ledger snapshot at ledgerRelPath (logical path).
func writeLedgerSnapshot(t *testing.T, lms *GitLedgerService, ledgerRelPath string, content []byte) {
	t.Helper()
	ciphertext, err := lms.config.EncryptionVault.EncryptWithAAD(content, ledgerSnapshotAAD(ledgerSnapshotGitPath(ledgerRelPath)))
	require.NoError(t, err)
	require.NoError(t, lms.fileSvc.WriteFile(context.Background(), ledgerRelPath+ledgerSnapshotSuffix, ciphertext, 0644))
}

func TestLedgerService_DiffBetween_Stat(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	ledgerRelDir := filepath.Join(constants.DataDirname, constants.LedgerDirname)
	ledgerDir := lms.fileSvc.Resolve(ledgerRelDir)

	// Initialize git repo first
	err := lms.initGitRepo(ledgerRelDir)
	require.NoError(t, err)

	// Take initial snapshot
	hash1, err := lms.snapshotLedger(ledgerDir, "Initial state")
	require.NoError(t, err)

	// Add an encrypted snapshot under the keyed name of the host path
	snapshotPath, err := lms.getLedgerPath(ledgerDir, "/host/diff_test.txt")
	require.NoError(t, err)
	snapshotRel, err := lms.fileSvc.Rel(snapshotPath)
	require.NoError(t, err)
	writeLedgerSnapshot(t, lms, snapshotRel, []byte("line 1\nline 2\nline 3\n"))

	// Take another snapshot
	hash2, err := lms.snapshotLedger(ledgerDir, "After adding file")
	require.NoError(t, err)

	diff, err := lms.diffBetween(ledgerDir, hash1, hash2, "/host/diff_test.txt")
	require.NoError(t, err)
	assert.Equal(t, "1 file changed, 3 insertions(+)", diff.stat)
}

func TestLedgerService_DiffBetween_EmptyHashesFailClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	ledgerDir := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname))

	_, err := lms.diffBetween(ledgerDir, "", "", "/host/f.txt")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)

	_, err = lms.diffBetween(ledgerDir, "abc123", "", "/host/f.txt")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)
}

func TestLedgerService_CountLines(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create test file with multiple lines and read it through a governed target
	testFile := filepath.Join(tempDir, "lines_test.txt")
	err := os.WriteFile(testFile, []byte("line 1\nline 2\nline 3"), 0644)
	require.NoError(t, err)

	content, err := openTestTarget(t, lms.fileSvc, testFile, tempDir).ReadFile()
	require.NoError(t, err)
	assert.Equal(t, 3, countLines(content))

	// Empty file
	emptyFile := filepath.Join(tempDir, "empty.txt")
	err = os.WriteFile(emptyFile, []byte(""), 0644)
	require.NoError(t, err)

	content, err = openTestTarget(t, lms.fileSvc, emptyFile, tempDir).ReadFile()
	require.NoError(t, err)
	assert.Equal(t, 1, countLines(content))

	// Non-existent file: the read fails before countLines is reached; countLines
	// only receives content the caller has successfully read.
	_, err = openTestTarget(t, lms.fileSvc, filepath.Join(tempDir, "nonexistent.txt"), tempDir).ReadFile()
	require.Error(t, err)
}

func TestLedgerService_GetFileHistory(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create a file and make multiple changes
	testFilePath := filepath.Join(tempDir, "history_test.txt")
	operatorSessionID := "test-session-history"

	// First version
	result1, _ := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Version 1"), 0644)
	lms.CompleteMirrorCreate(result1, operatorSessionID)

	// Second version
	result2, _ := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Version 2"), 0644)
	lms.CompleteMirrorWrite(result2, operatorSessionID)

	// Third version
	result3, _ := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Version 3"), 0644)
	lms.CompleteMirrorWrite(result3, operatorSessionID)

	// Get file history
	history, err := lms.GetFileHistory(t.Context(), testFilePath, 10, operatorSessionID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(history), 2) // At least 2 commits for this file

	// Verify history entries have valid data
	for _, entry := range history {
		assert.NotEmpty(t, entry.CommitHash)
		assert.NotEmpty(t, entry.Message)
		assert.False(t, entry.Timestamp.IsZero())
		assert.Equal(t, testFilePath, entry.FilePath)
	}
}

func TestLedgerService_GetFileHistory_DefaultLimit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create a file
	testFilePath := filepath.Join(tempDir, "limit_test.txt")
	result, _ := lms.MirrorFileCreate("operator_session", openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("content"), 0644)
	lms.CompleteMirrorCreate(result, "operator_session")

	// Get history with zero limit (should default to 50)
	history, err := lms.GetFileHistory(t.Context(), testFilePath, 0, "operator_session")
	// History may be empty if git operations haven't completed
	if err != nil {
		assert.Contains(t, err.Error(), "not found")
	} else {
		assert.NotNil(t, history)
	}
}

func TestLedgerService_GetFileHistoryExcludesUnrelatedCommits(t *testing.T) {
	lms, root := setupTestLedger(t)
	session := "file-history-isolation"
	wanted := filepath.Join(root, "wanted.txt")
	other := filepath.Join(root, "other.txt")
	write := func(path, content string) *LedgerResult {
		t.Helper()
		result, err := lms.LedgerFileWrite(session, openTestTarget(t, lms.fileSvc, path, root))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path, []byte(content), constants.PermFilePrivate))
		require.NoError(t, lms.CompleteMirrorWrite(result, session))
		return result
	}
	first := write(wanted, "original")
	write(other, "unrelated one")
	second := write(wanted, "changed")
	write(other, "unrelated two")
	history, err := lms.GetFileHistory(t.Context(), wanted, 50, session)
	require.NoError(t, err)
	require.Len(t, history, 3, "history must contain changes to the requested snapshot, not every later commit")
	assert.Equal(t, second.LedgerHashAfter, history[0].CommitHash)
	assert.Equal(t, second.LedgerHashBefore, history[1].CommitHash)
	assert.Equal(t, first.LedgerHashAfter, history[2].CommitHash)
	history, err = lms.GetFileHistory(t.Context(), wanted, 1, session)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, second.LedgerHashAfter, history[0].CommitHash, "the limit applies after filtering unrelated commits")
}

func TestLedgerService_GetFileHistoryHonorsCancellation(t *testing.T) {
	lms, root := setupTestLedger(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	history, err := lms.GetFileHistory(ctx, filepath.Join(root, "cancelled.txt"), 50, "cancelled-session")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, history)
}

func TestLedgerService_GetFileAtCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "commit_test.txt")
	operatorSessionID := "test-session-commit"

	// Create initial version
	result1, _ := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Initial content"), 0644)
	lms.CompleteMirrorCreate(result1, operatorSessionID)
	initialHash := result1.LedgerHashAfter

	// Create second version
	result2, _ := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Modified content"), 0644)
	lms.CompleteMirrorWrite(result2, operatorSessionID)

	// Get content at initial commit
	content, err := lms.GetFileAtCommit(testFilePath, initialHash, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "Initial content", content)

	// Current file should be different
	currentContent, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "Modified content", string(currentContent))
}

func TestLedgerService_RestoreFileFromCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "restore_test.txt")
	operatorSessionID := "test-session-restore"

	// Create initial version
	result1, _ := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("Original content"), 0644)
	lms.CompleteMirrorCreate(result1, operatorSessionID)
	originalHash := result1.LedgerHashAfter

	// Create second version
	result2, _ := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	os.WriteFile(testFilePath, []byte("New content"), 0644)
	lms.CompleteMirrorWrite(result2, operatorSessionID)

	// Verify current content is "New content"
	currentContent, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "New content", string(currentContent))

	// Restore to original version
	_, err := lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), originalHash, operatorSessionID, operatorSessionID)
	require.NoError(t, err)

	// Verify content is restored
	restoredContent, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "Original content", string(restoredContent))
}

// TestLedgerService_RestoreFileFromCommit_MirrorsIntoExecutingSession verifies that a restore
// reads the commit from the source session's ledger and records the write in the executing
// session's ledger, whose diff then resolves; the source ledger gains no commit.
func TestLedgerService_RestoreFileFromCommit_MirrorsIntoExecutingSession(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "restore_cross_session.txt")
	const sourceSessionID = "test-restore-source"
	const executingSessionID = "test-restore-executing"

	create, err := lms.MirrorFileCreate(sourceSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(testFilePath, []byte("Original content\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorCreate(create, sourceSessionID))
	require.NoError(t, os.WriteFile(testFilePath, []byte("Edited content\n"), 0o644))
	sourceHistory, err := lms.GetFileHistory(t.Context(), testFilePath, 10, sourceSessionID)
	require.NoError(t, err)

	restored, err := lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), create.LedgerHashAfter, sourceSessionID, executingSessionID)
	require.NoError(t, err)

	got, err := os.ReadFile(testFilePath)
	require.NoError(t, err)
	assert.Equal(t, "Original content\n", string(got))
	assert.Equal(t, FileMutationRestore, restored.Operation)
	require.NotEmpty(t, restored.LedgerHashBefore)
	require.NotEmpty(t, restored.LedgerHashAfter)

	diff, err := lms.GetDiffContent(restored.LedgerHashBefore, restored.LedgerHashAfter, testFilePath, executingSessionID)
	require.NoError(t, err)
	assert.Contains(t, diff, "-Edited content")
	assert.Contains(t, diff, "+Original content")

	after, err := lms.GetFileHistory(t.Context(), testFilePath, 10, sourceSessionID)
	require.NoError(t, err)
	assert.Len(t, after, len(sourceHistory), "the source session ledger must not record the restore")
}

func TestLedgerService_RestoreFileFromCommit_DisabledVault(t *testing.T) {
	t.Parallel()
	// Test with config that has no vault (encryption disabled)
	config := &LedgerConfig{}
	_, err := NewGitLedgerService(config, testutil.NewTestLogger(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ledger encryption vault is required")
}

func TestLedgerService_RestoreFileFromCommit_InvalidCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "invalid_restore.txt")

	_, err := lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), "invalidhash123", "operator_session", "operator_session")
	require.Error(t, err)
}

func TestLedgerService_CompleteWorkflow(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	operatorSessionID := "test-complete-workflow"
	testFilePath := filepath.Join(tempDir, "workflow_test.txt")

	// Step 1: Create file
	createResult, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, []byte("Step 1: Created"), 0644)
	err = lms.CompleteMirrorCreate(createResult, operatorSessionID)
	require.NoError(t, err)
	hash1 := createResult.LedgerHashAfter

	// Step 2: Modify file
	writeResult, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, []byte("Step 2: Modified"), 0644)
	err = lms.CompleteMirrorWrite(writeResult, operatorSessionID)
	require.NoError(t, err)
	hash2 := writeResult.LedgerHashAfter

	// Step 3: Modify again
	writeResult2, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, []byte("Step 3: Modified again"), 0644)
	err = lms.CompleteMirrorWrite(writeResult2, operatorSessionID)
	require.NoError(t, err)

	// Verify history
	history, err := lms.GetFileHistory(t.Context(), testFilePath, 10, operatorSessionID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(history), 2)

	// Restore to Step 1
	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), hash1, operatorSessionID, operatorSessionID)
	require.NoError(t, err)

	content, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "Step 1: Created", string(content))

	// Restore to Step 2
	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), hash2, operatorSessionID, operatorSessionID)
	require.NoError(t, err)

	content, _ = os.ReadFile(testFilePath)
	assert.Equal(t, "Step 2: Modified", string(content))
}

func TestLedgerService_MultiSessionConcurrency(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Test that multiple sessions can operate in parallel without blocking each other's git commits
	sessionCount := 5
	done := make(chan bool, sessionCount)

	for i := 0; i < sessionCount; i++ {
		go func(idx int) {
			sessionID := fmt.Sprintf("parallel-session-%d", idx)
			filePath := filepath.Join(tempDir, fmt.Sprintf("parallel_test_%d.txt", idx))

			// Start multi-phase operation
			target, err := lms.fileSvc.OpenFileEditTarget(context.Background(), filePath, tempDir)
			if !assert.NoError(t, err) {
				done <- true
				return
			}
			defer func() { _ = target.Close() }()

			result, err := lms.MirrorFileCreate(sessionID, target)
			assert.NoError(t, err)

			os.WriteFile(filePath, []byte(fmt.Sprintf("content from session %d", idx)), 0644)
			err = lms.CompleteMirrorCreate(result, sessionID)
			assert.NoError(t, err)

			// Verify session-specific ledger directory exists
			sessionLedgerDir := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname, constants.SessionsDirname, sessionID))
			assert.DirExists(t, filepath.Join(sessionLedgerDir, ".git"))

			done <- true
		}(i)
	}

	for i := 0; i < sessionCount; i++ {
		select {
		case <-done:
		case <-time.After(time.Second * 10):
			t.Errorf("Timeout waiting for parallel session %d", i)
		}
	}
}

func TestLedgerService_LargeFile(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "large_file.txt")
	operatorSessionID := "test-large-file"

	// Create a large file (1MB)
	largeContent := make([]byte, 1024*1024)
	for i := range largeContent {
		largeContent[i] = byte('A' + (i % 26))
	}

	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, largeContent, 0644)
	err = lms.CompleteMirrorCreate(result, operatorSessionID)
	require.NoError(t, err)

	// Verify file was mirrored correctly
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Len(t, mirrorContent, len(largeContent))
	assert.Equal(t, largeContent, mirrorContent)
}

func TestLedgerService_SpecialCharactersInPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// File with spaces and special chars (that are valid in filenames)
	testFilePath := filepath.Join(tempDir, "special file-name_v1.2.txt")
	operatorSessionID := "test-special-chars"

	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, []byte("special content"), 0644)
	err = lms.CompleteMirrorCreate(result, operatorSessionID)
	require.NoError(t, err)

	// Verify file was copied
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, "special content", string(mirrorContent))
}

func TestLedgerService_DeepNestedPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// Create a deeply nested path
	nestedPath := filepath.Join(tempDir, "level1", "level2", "level3", "level4", "deep_file.txt")
	os.MkdirAll(filepath.Dir(nestedPath), 0755)
	operatorSessionID := "test-deep-path"

	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, nestedPath, tempDir))
	require.NoError(t, err)

	os.WriteFile(nestedPath, []byte("deep content"), 0644)
	err = lms.CompleteMirrorCreate(result, operatorSessionID)
	require.NoError(t, err)

	// Verify nested structure was created in mirror
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, "deep content", string(mirrorContent))
}

func TestLedgerService_ZtdBinaryFile(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "binary_file.bin")
	operatorSessionID := "test-binary"

	// Create binary content with null bytes
	binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}

	result, err := lms.MirrorFileCreate(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.NoError(t, err)

	os.WriteFile(testFilePath, binaryContent, 0644)
	err = lms.CompleteMirrorCreate(result, operatorSessionID)
	require.NoError(t, err)

	// Verify binary file was copied correctly
	mirrorContent := readLedgerSnapshot(t, lms, result.LedgerPath)
	assert.Equal(t, binaryContent, mirrorContent)
}

func TestTruncateHash(t *testing.T) {
	t.Parallel()

	t.Run("long hash truncated to 12 chars", func(t *testing.T) {
		t.Parallel()
		hash := "0123456789abcdef0123456789abcdef01234567"
		result := truncateHash(hash)
		assert.Len(t, result, 12)
		assert.Equal(t, "0123456789ab", result)
	})

	t.Run("exactly 12 chars returned as-is", func(t *testing.T) {
		t.Parallel()
		hash := "0123456789ab"
		result := truncateHash(hash)
		assert.Equal(t, hash, result)
	})

	t.Run("short hash returned as-is", func(t *testing.T) {
		t.Parallel()
		hash := "abc123"
		result := truncateHash(hash)
		assert.Equal(t, hash, result)
	})

	t.Run("empty string returned as-is", func(t *testing.T) {
		t.Parallel()
		result := truncateHash("")
		assert.Empty(t, result)
	})
}

// TestLedgerService_LedgerFileWrite_CompleteSnapshotsTargetContent verifies that content
// written through the governed target is what CompleteMirrorWrite commits and what
// GetFileAtCommit returns for the resulting commit.
func TestLedgerService_LedgerFileWrite_CompleteSnapshotsTargetContent(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "target_write.txt")
	operatorSessionID := "test-session-target-write"
	require.NoError(t, os.WriteFile(testFilePath, []byte("before"), 0644))

	target := openTestTarget(t, lms.fileSvc, testFilePath, tempDir)
	result, err := lms.LedgerFileWrite(operatorSessionID, target)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.NoError(t, target.WriteFile([]byte("through target"), 0644))
	require.NoError(t, lms.CompleteMirrorWrite(result, operatorSessionID))
	require.NotEmpty(t, result.LedgerHashAfter)

	content, err := lms.GetFileAtCommit(testFilePath, result.LedgerHashAfter, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "through target", content)
}

func TestLedgerService_CopyToLedger_LockedVaultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerLocked(t)

	srcPath := filepath.Join(tempDir, "locked_source.txt")
	require.NoError(t, os.WriteFile(srcPath, []byte("secret content\n"), 0644))

	dstPath := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname, "files", "test", "locked_source.txt"))
	err := lms.copyToLedger(openTestTarget(t, lms.fileSvc, srcPath, tempDir), dstPath)
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)

	// Neither a plaintext nor an encrypted snapshot is written.
	relDst, err := lms.fileSvc.Rel(dstPath)
	require.NoError(t, err)
	plainExists, _ := lms.fileSvc.FileExists(context.Background(), relDst)
	encExists, _ := lms.fileSvc.FileExists(context.Background(), relDst+ledgerSnapshotSuffix)
	assert.False(t, plainExists, "plaintext ledger copy must not be written while locked")
	assert.False(t, encExists, "ledger snapshot must not be written while locked")
}

func TestLedgerService_CompleteMirrorWrite_LockedVaultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerLocked(t)

	testFilePath := filepath.Join(tempDir, "locked_write.txt")
	require.NoError(t, os.WriteFile(testFilePath, []byte("before\n"), 0644))
	operatorSessionID := "test-session-locked-write"

	// Begin fails closed: no pre-mutation snapshot can be taken while the vault is locked.
	result, err := lms.LedgerFileWrite(operatorSessionID, openTestTarget(t, lms.fileSvc, testFilePath, tempDir))
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	assert.Nil(t, result)

	// Nothing is written, not even the session repository.
	_, err = lms.openSessionLedger(operatorSessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
}

// TestLedgerSnapshot_BoundToSnapshotPath asserts a snapshot ciphertext moved to another
// file's snapshot path fails authentication instead of decrypting as that file's content.
func TestLedgerSnapshot_BoundToSnapshotPath(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)
	ledgerDir := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname))

	pathA, err := lms.getLedgerPath(ledgerDir, "/host/a.txt")
	require.NoError(t, err)
	pathB, err := lms.getLedgerPath(ledgerDir, "/host/b.txt")
	require.NoError(t, err)
	relA, err := lms.fileSvc.Rel(pathA)
	require.NoError(t, err)
	writeLedgerSnapshot(t, lms, relA, []byte("content of a"))

	ciphertext := readLedgerFile(t, lms.fileSvc, pathA+ledgerSnapshotSuffix)
	_, err = lms.config.EncryptionVault.DecryptWithAAD(ciphertext, ledgerSnapshotAAD(ledgerSnapshotGitPath(pathB)))
	require.Error(t, err)
	assert.Equal(t, "content of a", string(readLedgerSnapshot(t, lms, pathA)))
}

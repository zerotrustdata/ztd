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
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/services/vault"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// readLedgerFile reads a file from the .ztd/ runtime ledger tree via fileSvc,
// converting the absolute ledger path to a relative path.
func readLedgerFile(t *testing.T, fileSvc fs.RuntimeFileService, absPath string) []byte {
	t.Helper()
	relPath, err := fileSvc.Rel(absPath)
	require.NoError(t, err)
	content, err := fileSvc.ReadFile(context.Background(), relPath)
	require.NoError(t, err)
	return content
}

// readLedgerSnapshot returns the decrypted snapshot a mutation committed, read from the git
// objects of its session ledger. Ledger content is never plaintext on disk.
func readLedgerSnapshot(t *testing.T, lms *GitLedgerService, operatorSessionID string, result *LedgerResult) []byte {
	t.Helper()
	content, err := lms.GetFileAtCommit(result.FilePath, result.LedgerHashAfter, operatorSessionID)
	require.NoError(t, err)
	return []byte(content)
}

// recordMutation begins a mutation of path, writes content to it, and completes the mutation.
func recordMutation(t *testing.T, lms *GitLedgerService, operatorSessionID, path, workDir, content string) *LedgerResult {
	t.Helper()
	target := openTestTarget(t, lms.fileSvc, path, workDir)
	result, err := lms.BeginMutation(operatorSessionID, target)
	require.NoError(t, err)
	require.NoError(t, target.WriteFile([]byte(content), constants.PermFilePrivate))
	require.NoError(t, lms.CompleteMutation(result, operatorSessionID))
	return result
}

// commitCount returns the number of commits in a session ledger.
func commitCount(t *testing.T, lms *GitLedgerService, operatorSessionID string) int {
	t.Helper()
	commits, err := lms.ListCommits(operatorSessionID, 0)
	require.NoError(t, err)
	return len(commits)
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

// TestLedgerService_BeginMutation_MissingFileIsCreate verifies that a target that does not exist
// yet begins a creation, and that the creation's snapshot is the content written through the
// target.
func TestLedgerService_BeginMutation_MissingFileIsCreate(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "test_write.txt")
	operatorSessionID := "test-session-write"

	target := openTestTarget(t, lms.fileSvc, testFilePath, tempDir)
	result, err := lms.BeginMutation(operatorSessionID, target)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, testFilePath, result.FilePath)
	assert.Equal(t, FileMutationCreate, result.Operation)
	assert.True(t, result.Success)
	assert.NotEmpty(t, result.SnapshotName)

	require.NoError(t, target.WriteFile([]byte("Hello, World!"), constants.PermFilePrivate))
	require.NoError(t, lms.CompleteMutation(result, operatorSessionID))

	assert.NotEmpty(t, result.LedgerHashAfter)
	assert.NotEmpty(t, result.DiffStat)
	assert.Equal(t, "Hello, World!", string(readLedgerSnapshot(t, lms, operatorSessionID, result)))
}

func TestLedgerService_BeginMutation_ExistingFileIsWrite(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "existing_file.txt")
	require.NoError(t, os.WriteFile(testFilePath, []byte("Original content"), 0644))
	operatorSessionID := "test-session-existing"

	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Modified content")
	assert.Equal(t, FileMutationWrite, result.Operation)
	assert.NotEmpty(t, result.LedgerHashBefore)
	assert.True(t, result.Success)
	assert.NotEqual(t, result.LedgerHashBefore, result.LedgerHashAfter)

	before, err := lms.GetFileAtCommit(testFilePath, result.LedgerHashBefore, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "Original content", before, "a pre-image the ledger has never seen is committed as a backup")
	assert.Equal(t, "Modified content", string(readLedgerSnapshot(t, lms, operatorSessionID, result)))
}

func TestLedgerService_CompleteMutation_NilResultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	require.ErrorIs(t, lms.CompleteMutation(nil, "operator_session"), constants.ErrInternal)
	require.ErrorIs(t, lms.CompleteMutation(&LedgerResult{}, "operator_session"), constants.ErrInternal, "a result not begun by BeginMutation has no target")
}

func TestLedgerService_CreateMutation(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "new_created_file.txt")
	operatorSessionID := "test-session-create"

	content := "Line 1\nLine 2\nLine 3\n"
	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, content)

	assert.Equal(t, FileMutationCreate, result.Operation)
	assert.NotEmpty(t, result.LedgerHashBefore)
	assert.NotEmpty(t, result.LedgerHashAfter)
	assert.Contains(t, result.DiffStat, "new file")
	assert.Contains(t, result.DiffStat, "lines")
	assert.Equal(t, content, string(readLedgerSnapshot(t, lms, operatorSessionID, result)))
}

func TestLedgerService_SnapshotTreePath(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	// The snapshot path is a keyed name, sharded by its leading characters, and carries no part
	// of the host path.
	name, err := lms.SnapshotName("/etc/nginx/nginx.conf")
	require.NoError(t, err)
	path := snapshotTreePath(name)
	assert.Equal(t, fmt.Sprintf("%s/%c/%c/%c/%c/%s%s", constants.LedgerSnapshotsTreeDirname, name[0], name[1], name[2], name[3], name, ledgerSnapshotSuffix), path)
	assert.NotContains(t, path, "nginx")
	assert.NotContains(t, path, "etc")
	assert.NotContains(t, path, "//")

	// The name is stable for a path and differs between paths
	again, err := lms.SnapshotName("/etc/nginx/nginx.conf")
	require.NoError(t, err)
	assert.Equal(t, name, again)
	other, err := lms.SnapshotName("/etc/nginx/other.conf")
	require.NoError(t, err)
	assert.NotEqual(t, name, other)

	// A relative path is keyed by its absolute form
	relative, err := lms.SnapshotName("relative/path/file.txt")
	require.NoError(t, err)
	absolute, err := filepath.Abs("relative/path/file.txt")
	require.NoError(t, err)
	keyed, err := lms.SnapshotName(absolute)
	require.NoError(t, err)
	assert.Equal(t, keyed, relative)
	assert.NotContains(t, snapshotTreePath(relative), "relative")

	assert.Equal(t, constants.FilesDirname+"/"+name+ledgerSnapshotSuffix, preLayoutSnapshotPath(name))
}

func TestLedgerService_SnapshotName_LockedVaultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedgerLocked(t)

	_, err := lms.SnapshotName("/etc/nginx/nginx.conf")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
}

// TestLedgerService_CommitBlob verifies the direct commit path: each commit adds the blob on top
// of HEAD, moves the branch, keeps every entry not on its path, and refuses a path that crosses
// an existing file.
func TestLedgerService_CommitBlob(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	repo, head, err := openLedgerHead(lms.fileSvc.Resolve(filepath.Join(lms.baseRelPath, constants.FilesDirname)))
	require.NoError(t, err)

	first, err := commitBlob(repo, head, "a/b/one.enc", []byte("one"), "first")
	require.NoError(t, err)
	head, err = repo.Head()
	require.NoError(t, err)
	assert.Equal(t, first, head.Hash())

	second, err := commitBlob(repo, head, "a/c/two.enc", []byte("two"), "second")
	require.NoError(t, err)
	assert.NotEqual(t, first, second)

	commit, err := repo.CommitObject(second)
	require.NoError(t, err)
	assert.Equal(t, []plumbing.Hash{first}, commit.ParentHashes)
	assert.Contains(t, commit.Message, "second")
	assert.Equal(t, ledgerAuthorName, commit.Author.Name)
	tree, err := commit.Tree()
	require.NoError(t, err)
	for path, want := range map[string]string{"a/b/one.enc": "one", "a/c/two.enc": "two", constants.GitignoreFilename: "# ztd Ledger\n"} {
		file, err := tree.File(path)
		require.NoError(t, err, path)
		got, err := file.Contents()
		require.NoError(t, err)
		assert.Equal(t, want, got, path)
	}

	head, err = repo.Head()
	require.NoError(t, err)
	_, err = commitBlob(repo, head, "a/b/one.enc/nested.enc", []byte("x"), "conflict")
	require.ErrorIs(t, err, constants.ErrInternal, "a path through an existing file is refused")
	_, err = commitBlob(repo, head, "a/b", []byte("x"), "conflict")
	require.ErrorIs(t, err, constants.ErrInternal, "a file over an existing directory is refused")
	after, err := repo.Head()
	require.NoError(t, err)
	assert.Equal(t, second, after.Hash(), "a refused commit leaves HEAD in place")
}

// TestLedgerService_CommitBlob_StaleHeadIsRefused verifies that a commit built on a HEAD another
// writer has already moved does not land.
func TestLedgerService_CommitBlob_StaleHeadIsRefused(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	repo, stale, err := openLedgerHead(lms.fileSvc.Resolve(filepath.Join(lms.baseRelPath, constants.FilesDirname)))
	require.NoError(t, err)
	moved, err := commitBlob(repo, stale, "one.enc", []byte("one"), "first")
	require.NoError(t, err)

	_, err = commitBlob(repo, stale, "two.enc", []byte("two"), "second")
	require.Error(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	assert.Equal(t, moved, head.Hash())
}

func TestLedgerService_DiffBetween_Stat(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	ledgerDir := lms.fileSvc.Resolve(filepath.Join(lms.baseRelPath, constants.FilesDirname))
	repo, head, err := openLedgerHead(ledgerDir)
	require.NoError(t, err)
	hash1 := head.Hash().String()

	name, err := lms.SnapshotName("/host/diff_test.txt")
	require.NoError(t, err)
	hash2, err := lms.commitSnapshot(repo, head, name, []byte("line 1\nline 2\nline 3\n"), "After adding file")
	require.NoError(t, err)

	diff, err := lms.diffBetween(ledgerDir, hash1, hash2, "/host/diff_test.txt")
	require.NoError(t, err)
	assert.Equal(t, "1 file changed, 3 insertions(+)", diff.stat)

	_, err = lms.diffBetween(ledgerDir, hash2, hash1, "/host/diff_test.txt")
	require.ErrorIs(t, err, constants.ErrPathNotFound, "an after commit with no snapshot is an error, never an empty diff")
}

func TestLedgerService_DiffBetween_EmptyHashesFailClosed(t *testing.T) {
	t.Parallel()
	lms, _ := setupTestLedger(t)

	ledgerDir := lms.fileSvc.Resolve(filepath.Join(lms.baseRelPath, constants.FilesDirname))

	_, err := lms.diffBetween(ledgerDir, "", "", "/host/f.txt")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)

	_, err = lms.diffBetween(ledgerDir, "abc123", "", "/host/f.txt")
	require.ErrorIs(t, err, constants.ErrLedgerDiffHashesRequired)
}

// mutationCandidates returns the commits of ledger mutations, given oldest first, in the order the
// history handler passes them to GetFileHistory.
func mutationCandidates(results ...*LedgerResult) []string {
	mutations := make([]*FileMutationLog, 0, len(results))
	for i := len(results) - 1; i >= 0; i-- {
		mutations = append(mutations, &FileMutationLog{LedgerHashBefore: results[i].LedgerHashBefore, LedgerHashAfter: results[i].LedgerHashAfter})
	}
	return mutationCommits(mutations)
}

func TestLedgerService_GetFileHistory(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "history_test.txt")
	operatorSessionID := "test-session-history"

	result1 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Version 1")
	result2 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Version 2")
	result3 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Version 3")
	assert.Equal(t, result1.LedgerHashAfter, result2.LedgerHashBefore, "an unchanged pre-image is not committed again")
	assert.Equal(t, result2.LedgerHashAfter, result3.LedgerHashBefore)

	history, err := lms.GetFileHistory(t.Context(), testFilePath, mutationCandidates(result1, result2, result3), 10, operatorSessionID)
	require.NoError(t, err)
	require.Len(t, history, 3, "one entry per mutation: the creation commits no pre-state and no edit re-commits its pre-image")
	assert.Equal(t, result3.LedgerHashAfter, history[0].CommitHash)
	assert.Equal(t, result2.LedgerHashAfter, history[1].CommitHash)
	assert.Equal(t, result1.LedgerHashAfter, history[2].CommitHash)

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

	testFilePath := filepath.Join(tempDir, "limit_test.txt")
	result := recordMutation(t, lms, "operator_session", testFilePath, tempDir, "content")

	// Get history with zero limit (should default to 50)
	history, err := lms.GetFileHistory(t.Context(), testFilePath, mutationCandidates(result), 0, "operator_session")
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, result.LedgerHashAfter, history[0].CommitHash)
}

// TestLedgerService_GetFileHistoryCountsEveryWriteOfIdenticalContent pins what the handler's row
// limit relies on: each snapshot is encrypted under a fresh nonce, so a write that leaves the
// content unchanged still changes the snapshot and yields a history entry.
func TestLedgerService_GetFileHistoryCountsEveryWriteOfIdenticalContent(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	testFilePath := filepath.Join(tempDir, "identical.txt")
	const session = "identical-content"
	require.NoError(t, os.WriteFile(testFilePath, []byte("same"), constants.PermFilePrivate))

	// The ledger has never seen the file, so its pre-image is committed as a backup.
	first := recordMutation(t, lms, session, testFilePath, tempDir, "same")
	// The pre-image now matches the latest snapshot, so only the after commit is added.
	second := recordMutation(t, lms, session, testFilePath, tempDir, "same")
	assert.Equal(t, first.LedgerHashAfter, second.LedgerHashBefore)

	history, err := lms.GetFileHistory(t.Context(), testFilePath, mutationCandidates(first, second), 10, session)
	require.NoError(t, err)
	require.Len(t, history, 3)
	assert.Equal(t, second.LedgerHashAfter, history[0].CommitHash)
	assert.Equal(t, first.LedgerHashAfter, history[1].CommitHash)
	assert.Equal(t, first.LedgerHashBefore, history[2].CommitHash)
}

func TestLedgerService_GetFileHistoryExcludesUnrelatedCommits(t *testing.T) {
	lms, root := setupTestLedger(t)
	session := "file-history-isolation"
	wanted := filepath.Join(root, "wanted.txt")
	other := filepath.Join(root, "other.txt")
	write := func(path, content string) *LedgerResult {
		t.Helper()
		return recordMutation(t, lms, session, path, root, content)
	}
	first := write(wanted, "original")
	unrelatedOne := write(other, "unrelated one")
	second := write(wanted, "changed")
	unrelatedTwo := write(other, "unrelated two")
	require.Equal(t, unrelatedOne.LedgerHashAfter, second.LedgerHashBefore, "the unchanged pre-image begins at HEAD, an unrelated commit")
	candidates := mutationCandidates(first, unrelatedOne, second, unrelatedTwo)
	history, err := lms.GetFileHistory(t.Context(), wanted, candidates, 50, session)
	require.NoError(t, err)
	require.Len(t, history, 2, "history must contain changes to the requested snapshot, not every later commit")
	assert.Equal(t, second.LedgerHashAfter, history[0].CommitHash)
	assert.Equal(t, first.LedgerHashAfter, history[1].CommitHash)
	history, err = lms.GetFileHistory(t.Context(), wanted, candidates, 1, session)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, second.LedgerHashAfter, history[0].CommitHash, "the limit applies after filtering unrelated commits")
}

func TestLedgerService_GetFileHistoryHonorsCancellation(t *testing.T) {
	lms, root := setupTestLedger(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	history, err := lms.GetFileHistory(ctx, filepath.Join(root, "cancelled.txt"), nil, 50, "cancelled-session")
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, history)
}

func TestLedgerService_GetFileAtCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "commit_test.txt")
	operatorSessionID := "test-session-commit"

	result1 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Initial content")
	recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Modified content")

	content, err := lms.GetFileAtCommit(testFilePath, result1.LedgerHashAfter, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "Initial content", content)

	_, err = lms.GetFileAtCommit(testFilePath, result1.LedgerHashBefore, operatorSessionID)
	require.ErrorIs(t, err, constants.ErrPathNotFound, "the HEAD a creation began at holds no snapshot of the file")

	currentContent, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "Modified content", string(currentContent))
}

func TestLedgerService_RestoreFileFromCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "restore_test.txt")
	operatorSessionID := "test-session-restore"

	result1 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Original content")
	recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "New content")

	currentContent, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "New content", string(currentContent))

	restored, err := lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), result1.LedgerHashAfter, operatorSessionID, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, FileMutationRestore, restored.Operation)

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

	create := recordMutation(t, lms, sourceSessionID, testFilePath, tempDir, "Original content\n")
	require.NoError(t, os.WriteFile(testFilePath, []byte("Edited content\n"), 0o644))
	sourceCommits := commitCount(t, lms, sourceSessionID)

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

	assert.Equal(t, sourceCommits, commitCount(t, lms, sourceSessionID), "the source session ledger must not record the restore")
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

	createResult := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Step 1: Created")
	writeResult := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Step 2: Modified")
	writeResult2 := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "Step 3: Modified again")

	history, err := lms.GetFileHistory(t.Context(), testFilePath, mutationCandidates(createResult, writeResult, writeResult2), 10, operatorSessionID)
	require.NoError(t, err)
	assert.Len(t, history, 3)

	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), createResult.LedgerHashAfter, operatorSessionID, operatorSessionID)
	require.NoError(t, err)
	content, _ := os.ReadFile(testFilePath)
	assert.Equal(t, "Step 1: Created", string(content))

	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, testFilePath, tempDir), writeResult.LedgerHashAfter, operatorSessionID, operatorSessionID)
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

			target, err := lms.fileSvc.OpenFileEditTarget(context.Background(), filePath, tempDir)
			if !assert.NoError(t, err) {
				done <- true
				return
			}
			defer func() { _ = target.Close() }()

			result, err := lms.BeginMutation(sessionID, target)
			if !assert.NoError(t, err) {
				done <- true
				return
			}

			assert.NoError(t, target.WriteFile([]byte(fmt.Sprintf("content from session %d", idx)), constants.PermFilePrivate))
			assert.NoError(t, lms.CompleteMutation(result, sessionID))

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

	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, string(largeContent))

	mirrorContent := readLedgerSnapshot(t, lms, operatorSessionID, result)
	assert.Len(t, mirrorContent, len(largeContent))
	assert.Equal(t, largeContent, mirrorContent)
}

func TestLedgerService_SpecialCharactersInPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	// File with spaces and special chars (that are valid in filenames)
	testFilePath := filepath.Join(tempDir, "special file-name_v1.2.txt")
	operatorSessionID := "test-special-chars"

	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "special content")
	assert.Equal(t, "special content", string(readLedgerSnapshot(t, lms, operatorSessionID, result)))
}

func TestLedgerService_DeepNestedPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	nestedPath := filepath.Join(tempDir, "level1", "level2", "level3", "level4", "deep_file.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(nestedPath), 0755))
	operatorSessionID := "test-deep-path"

	result := recordMutation(t, lms, operatorSessionID, nestedPath, tempDir, "deep content")
	assert.Equal(t, "deep content", string(readLedgerSnapshot(t, lms, operatorSessionID, result)))
}

func TestLedgerService_ZtdBinaryFile(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "binary_file.bin")
	operatorSessionID := "test-binary"

	// Binary content with null bytes
	binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE, 0xFD}

	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, string(binaryContent))
	assert.Equal(t, binaryContent, readLedgerSnapshot(t, lms, operatorSessionID, result))
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

// TestLedgerService_CompleteMutation_SnapshotsTargetContent verifies that content written through
// the governed target is what CompleteMutation commits and what GetFileAtCommit returns for the
// resulting commit.
func TestLedgerService_CompleteMutation_SnapshotsTargetContent(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)

	testFilePath := filepath.Join(tempDir, "target_write.txt")
	operatorSessionID := "test-session-target-write"
	require.NoError(t, os.WriteFile(testFilePath, []byte("before"), 0644))

	result := recordMutation(t, lms, operatorSessionID, testFilePath, tempDir, "through target")
	require.NotEmpty(t, result.LedgerHashAfter)

	content, err := lms.GetFileAtCommit(testFilePath, result.LedgerHashAfter, operatorSessionID)
	require.NoError(t, err)
	assert.Equal(t, "through target", content)
}

func TestLedgerService_BeginMutation_LockedVaultFailsClosed(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedgerLocked(t)

	existing := filepath.Join(tempDir, "locked_write.txt")
	require.NoError(t, os.WriteFile(existing, []byte("before\n"), 0644))
	missing := filepath.Join(tempDir, "locked_create.txt")
	operatorSessionID := "test-session-locked-write"

	// Begin fails closed for a write and a creation: no snapshot can be taken while the vault is
	// locked.
	for _, path := range []string{existing, missing} {
		result, err := lms.BeginMutation(operatorSessionID, openTestTarget(t, lms.fileSvc, path, tempDir))
		require.ErrorIs(t, err, constants.ErrLedgerVaultRequired, path)
		assert.Nil(t, result)
	}

	// Nothing is written, not even the session repository.
	_, err := lms.openSessionLedger(operatorSessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
}

// TestLedgerSnapshot_BoundToSnapshotPath asserts a snapshot ciphertext moved to another
// file's snapshot path fails authentication instead of decrypting as that file's content.
func TestLedgerSnapshot_BoundToSnapshotPath(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "snapshot-binding"

	pathA := filepath.Join(tempDir, "a.txt")
	pathB := filepath.Join(tempDir, "b.txt")
	resultA := recordMutation(t, lms, session, pathA, tempDir, "content of a")
	nameB, err := lms.SnapshotName(pathB)
	require.NoError(t, err)

	ledgerDir, err := lms.openSessionLedger(session)
	require.NoError(t, err)
	repo, _, err := openLedgerHead(ledgerDir)
	require.NoError(t, err)
	tree, err := commitTree(repo, resultA.LedgerHashAfter)
	require.NoError(t, err)
	fileA, err := snapshotAt(repo.Storer, tree, resultA.SnapshotName)
	require.NoError(t, err)
	require.NotNil(t, fileA)
	ciphertext, err := fileA.Contents()
	require.NoError(t, err)

	_, err = lms.config.EncryptionVault.DecryptWithAAD([]byte(ciphertext), ledgerSnapshotAAD(nameB))
	require.Error(t, err)
	assert.NotContains(t, ciphertext, "content of a", "a snapshot is never stored as plaintext")
	assert.True(t, strings.HasSuffix(fileA.Name, snapshotTreePath(resultA.SnapshotName)))
	assert.Equal(t, "content of a", string(readLedgerSnapshot(t, lms, session, resultA)))
}

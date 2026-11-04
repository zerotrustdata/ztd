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
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// Tests against a session ledger written before the sharded snapshot layout, whose commits hold
// each snapshot at the flat files/<name>.enc path.

// commitPreLayoutSnapshot commits content as path's snapshot the way a ledger from before the
// sharded layout did: a worktree file at the flat files/<name>.enc path, added to the index and
// committed from it. It returns the commit before it and the commit.
func commitPreLayoutSnapshot(t *testing.T, lms *GitLedgerService, session, path, content string) (before, after string) {
	t.Helper()
	ledgerDir, err := lms.createSessionLedger(session)
	require.NoError(t, err)
	name, err := lms.SnapshotName(path)
	require.NoError(t, err)
	encrypted, err := lms.config.EncryptionVault.EncryptWithAAD([]byte(content), ledgerSnapshotAAD(name))
	require.NoError(t, err)
	repo, head, err := openLedgerHead(ledgerDir)
	require.NoError(t, err)
	worktreePath := filepath.Join(ledgerDir, filepath.FromSlash(preLayoutSnapshotPath(name)))
	require.NoError(t, os.MkdirAll(filepath.Dir(worktreePath), constants.PermDirPrivate))
	require.NoError(t, os.WriteFile(worktreePath, encrypted, constants.PermFilePrivate))
	w, err := repo.Worktree()
	require.NoError(t, err)
	require.NoError(t, w.AddWithOptions(&git.AddOptions{All: true}))
	hash, err := w.Commit(ledgerMsgPostMutation, &git.CommitOptions{Author: ledgerSignature()})
	require.NoError(t, err)
	return head.Hash().String(), hash.String()
}

// filesSubtreeHash returns the hash of the flat files/ tree at a session ledger's HEAD.
func filesSubtreeHash(t *testing.T, ledgerDir string) plumbing.Hash {
	t.Helper()
	repo, head, err := openLedgerHead(ledgerDir)
	require.NoError(t, err)
	commit, err := repo.CommitObject(head.Hash())
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	entry, err := tree.FindEntry(constants.FilesDirname)
	require.NoError(t, err)
	return entry.Hash
}

func TestLedgerPreLayout_RestoreFailsClosedAndLeavesTheHostUntouched(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "pre-layout-restore"
	path := filepath.Join(tempDir, "restored.txt")
	_, old := commitPreLayoutSnapshot(t, lms, session, path, "old content")
	require.NoError(t, os.WriteFile(path, []byte("current content"), constants.PermFilePrivate))
	commits := commitCount(t, lms, session)

	_, err := lms.GetFileAtCommit(path, old, session)
	require.ErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout, "the flat snapshot is never read")

	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, path, tempDir), old, session, session)
	require.ErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "current content", string(content))
	assert.Equal(t, commits, commitCount(t, lms, session), "a refused restore records nothing")
}

func TestLedgerPreLayout_MissingSnapshotIsNotPreLayout(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "pre-layout-missing"
	_, old := commitPreLayoutSnapshot(t, lms, session, filepath.Join(tempDir, "other.txt"), "other")

	_, err := lms.GetFileAtCommit(filepath.Join(tempDir, "never.txt"), old, session)
	require.ErrorIs(t, err, constants.ErrPathNotFound)
	require.NotErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout)
}

func TestLedgerPreLayout_DiffOfAPreLayoutCommitFailsClosed(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "pre-layout-diff"
	path := filepath.Join(tempDir, "diffed.txt")
	before, after := commitPreLayoutSnapshot(t, lms, session, path, "old content")

	_, err := lms.GetDiffContent(before, after, path, session)
	require.ErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout, "never an empty diff")
	_, err = lms.GetDiffStat(before, after, path, session)
	require.ErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout)
}

// TestLedgerPreLayout_FileWithOnlyAFlatSnapshotCanBeEdited verifies that the first edit after the
// upgrade commits the pre-image at the sharded path, so its diff resolves.
func TestLedgerPreLayout_FileWithOnlyAFlatSnapshotCanBeEdited(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "pre-layout-edit"
	path := filepath.Join(tempDir, "edited.txt")
	_, old := commitPreLayoutSnapshot(t, lms, session, path, "v1\n")
	require.NoError(t, os.WriteFile(path, []byte("v1\n"), constants.PermFilePrivate))

	result := recordMutation(t, lms, session, path, tempDir, "v2\n")

	assert.Equal(t, FileMutationWrite, result.Operation)
	assert.NotEqual(t, old, result.LedgerHashBefore, "the flat snapshot is not a latest snapshot, so the pre-image is committed")
	before, err := lms.GetFileAtCommit(path, result.LedgerHashBefore, session)
	require.NoError(t, err)
	assert.Equal(t, "v1\n", before)
	diff, err := lms.GetDiffContent(result.LedgerHashBefore, result.LedgerHashAfter, path, session)
	require.NoError(t, err)
	assert.Contains(t, diff, "-v1")
	assert.Contains(t, diff, "+v2")
}

// TestLedgerPreLayout_UpgradeKeepsTheOldTrail verifies that a runtime started over a flat-layout
// session accepts mutations with no manual step, reuses the old files/ tree by hash, and leaves
// every old commit in place.
func TestLedgerPreLayout_UpgradeKeepsTheOldTrail(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "pre-layout-upgrade"
	edited := filepath.Join(tempDir, "edited.txt")
	commitPreLayoutSnapshot(t, lms, session, edited, "v1")
	commitPreLayoutSnapshot(t, lms, session, filepath.Join(tempDir, "untouched.txt"), "untouched")
	require.NoError(t, os.WriteFile(edited, []byte("v1"), constants.PermFilePrivate))
	ledgerDir, err := lms.openSessionLedger(session)
	require.NoError(t, err)
	oldFiles := filesSubtreeHash(t, ledgerDir)
	oldCommits, err := lms.ListCommits(session, 0)
	require.NoError(t, err)
	indexPath := filepath.Join(ledgerDir, constants.GitDirname, constants.GitIndexFilename)
	oldIndex, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	oldWorktree, err := os.ReadDir(filepath.Join(ledgerDir, constants.FilesDirname))
	require.NoError(t, err)
	require.Len(t, oldWorktree, 2, "the old root holds a worktree copy of each snapshot")

	upgraded, err := NewGitLedgerService(&LedgerConfig{EncryptionVault: lms.config.EncryptionVault}, testutil.NewTestLogger(), lms.fileSvc)
	require.NoError(t, err)
	recordMutation(t, upgraded, session, edited, tempDir, "v2")
	recordMutation(t, upgraded, session, filepath.Join(tempDir, "created.txt"), tempDir, "created")

	assert.Equal(t, oldFiles, filesSubtreeHash(t, ledgerDir), "the flat tree is reused, never rewritten")
	index, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Equal(t, oldIndex, index, "the old index is left unused")
	worktree, err := os.ReadDir(filepath.Join(ledgerDir, constants.FilesDirname))
	require.NoError(t, err)
	assert.Len(t, worktree, len(oldWorktree), "the old worktree files are left unused")
	commits, err := upgraded.ListCommits(session, 0)
	require.NoError(t, err)
	require.Greater(t, len(commits), len(oldCommits))
	assert.Equal(t, oldCommits, commits[len(commits)-len(oldCommits):], "old commits are unchanged")
	repo, err := git.PlainOpen(ledgerDir)
	require.NoError(t, err)
	for _, old := range oldCommits {
		_, err := repo.CommitObject(plumbing.NewHash(old.CommitHash))
		require.NoError(t, err, old.CommitHash)
	}
}

// TestLedgerPreLayout_HistoryHandlerKeepsPreLayoutRows verifies that an unfiltered diff listing
// keeps a pre-layout row without a diff size instead of failing, and that fetching that row's
// diff fails closed.
func TestLedgerPreLayout_HistoryHandlerKeepsPreLayoutRows(t *testing.T) {
	t.Parallel()
	hh, eventsStore, testVault, tempDir := setupTestHistoryHandler(t)
	defer testVault.Close()
	lms := handlerLedger(t, hh)
	const session = "pre-layout-handler"
	require.NoError(t, eventsStore.CreateSession(session, "operator", "Pre-layout", "user@test.com"))
	record := func(path, before, after string) {
		t.Helper()
		name, err := lms.SnapshotName(path)
		require.NoError(t, err)
		eventID, err := eventsStore.RecordEvent(&Event{
			OperatorSessionID: session,
			Timestamp:         time.Now().UTC(),
			Type:              constants.Event.Operator.FileEdit.Completed,
			ContentText:       "Write file",
		})
		require.NoError(t, err)
		require.NoError(t, eventsStore.RecordFileMutation(&FileMutationLog{
			EventID: eventID, Filepath: path, SnapshotName: name, Operation: FileMutationWrite,
			LedgerHashBefore: before, LedgerHashAfter: after,
		}))
	}

	oldPath := filepath.Join(tempDir, "old.txt")
	oldBefore, oldAfter := commitPreLayoutSnapshot(t, lms, session, oldPath, "old")
	record(oldPath, oldBefore, oldAfter)
	newPath := filepath.Join(tempDir, "new.txt")
	result := recordMutation(t, lms, session, newPath, tempDir, "new")
	record(newPath, result.LedgerHashBefore, result.LedgerHashAfter)

	entries, err := hh.ListFileDiffs(t.Context(), session, "", 10)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	byAfter := make(map[string]int32, len(entries))
	var oldID string
	for _, entry := range entries {
		byAfter[entry.LedgerHashAfter] = entry.DiffSize
		if entry.LedgerHashAfter == oldAfter {
			oldID = entry.Id
		}
	}
	assert.Zero(t, byAfter[oldAfter], "a pre-layout row is listed without a diff size")
	assert.Positive(t, byAfter[result.LedgerHashAfter])

	id, err := strconv.ParseInt(oldID, 10, 64)
	require.NoError(t, err)
	_, err = hh.FetchFileDiff(id)
	require.ErrorIs(t, err, constants.ErrLedgerSnapshotPreLayout)
}

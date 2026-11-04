//go:build integration && unix

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
	"syscall"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// blockSnapshotRead swaps the loose object of a file's snapshot at a commit for a named pipe, so
// the next read of that snapshot blocks until the returned function supplies the object's bytes.
// The function blocks until a reader has opened the pipe, and restores the object file.
func (f *fileHistoryFixture) blockSnapshotRead(t *testing.T, session, commitHash, snapshotName string) func() {
	t.Helper()
	repo := f.openLedgerRepo(t, session)
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	require.NoError(t, err)
	tree, err := commit.Tree()
	require.NoError(t, err)
	entry, err := tree.FindEntry(snapshotTreePath(snapshotName))
	require.NoError(t, err)
	ledgerDir, err := f.ledger.openSessionLedger(session)
	require.NoError(t, err)
	hash := entry.Hash.String()
	objectPath := filepath.Join(ledgerDir, constants.GitDirname, "objects", hash[:2], hash[2:])

	stored, err := os.ReadFile(objectPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(objectPath))
	require.NoError(t, syscall.Mkfifo(objectPath, constants.PermFilePrivate))

	return func() {
		writer, err := os.OpenFile(objectPath, os.O_WRONLY, 0)
		require.NoError(t, err)
		require.NoError(t, os.Remove(objectPath))
		require.NoError(t, os.WriteFile(objectPath, stored, constants.PermFilePrivate))
		_, err = writer.Write(stored)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
	}
}

// TestCompleteMutation_ReleasesTheWriteLockBeforeItsDiff holds a mutation inside its diff, which is
// the only step of CompleteMutation that reads the previous snapshot. The diff's cost can grow with
// the square of the file's size on a large rewrite and one lock serves every session of the runtime,
// so a rewrite must not hold ledger writes while it diffs.
func TestCompleteMutation_ReleasesTheWriteLockBeforeItsDiff(t *testing.T) {
	const session, otherSession = "write-lock-diffing", "write-lock-other"
	f := newFileHistoryFixture(t, session, otherSession)
	path := f.path("rewritten.txt")
	created := f.mutate(t, session, path, "v1\n")

	target := openTestTarget(t, f.ledger.fileSvc, path, f.root)
	result, err := f.ledger.BeginMutation(session, target)
	require.NoError(t, err)
	require.Equal(t, created.LedgerHashAfter, result.LedgerHashBefore, "the unchanged pre-image commits nothing")
	require.NoError(t, target.WriteFile([]byte("v2\n"), constants.PermFilePrivate))

	finishDiff := f.blockSnapshotRead(t, session, result.LedgerHashBefore, result.SnapshotName)
	completed := make(chan error, 1)
	go func() { completed <- f.ledger.CompleteMutation(result, session) }()

	ledgerDir, err := f.ledger.openSessionLedger(session)
	require.NoError(t, err)
	refMoved := assert.Eventually(t, func() bool {
		repo, openErr := git.PlainOpen(ledgerDir)
		if openErr != nil {
			return false
		}
		head, headErr := repo.Head()
		return headErr == nil && head.Hash().String() != result.LedgerHashBefore
	}, 30*time.Second, 5*time.Millisecond, "the post-mutation commit must land before the diff")

	unlocked := refMoved && assert.Eventually(t, func() bool {
		if !f.ledger.mu.TryLock() {
			return false
		}
		f.ledger.mu.Unlock()
		return true
	}, 10*time.Second, 5*time.Millisecond, "the diff must not hold the ledger write lock")
	if unlocked {
		other := f.mutate(t, otherSession, f.path("other.txt"), "other")
		assert.NotEmpty(t, other.LedgerHashAfter, "another session's write must land during the diff")
	}

	finishDiff()
	require.NoError(t, <-completed)
	f.recordMutation(t, session, result)
	assert.Contains(t, result.DiffContent, "+v2")
	assert.Contains(t, result.DiffContent, "-v1")
	assert.Equal(t, []string{result.LedgerHashAfter, created.LedgerHashAfter}, f.history(t, session, path, 0))
}

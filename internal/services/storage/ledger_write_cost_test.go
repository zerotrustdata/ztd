// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// The work a ledger write does is bounded by counting the objects it adds, never by timing it.

// maxLedgerWriteTreeBytes bounds the trees one mutation writes. Each tree on a sharded snapshot
// path holds at most 16 entries, so one mutation writes about 2 KB of trees however many
// snapshots the session holds.
const maxLedgerWriteTreeBytes = 2048

// ledgerWriteCost is the set of loose objects one ledger write added.
type ledgerWriteCost struct {
	commits, trees, blobs int
	treeBytes             int64
	largestTree           int64
}

// looseLedgerObjects returns the loose objects of a ledger repository. Every ledger write stores
// its objects loose, and the ledger is never repacked.
func looseLedgerObjects(t *testing.T, ledgerDir string) map[plumbing.Hash]bool {
	t.Helper()
	root := filepath.Join(ledgerDir, constants.GitDirname, constants.GitObjectsDirname)
	fanouts, err := os.ReadDir(root)
	require.NoError(t, err)
	objects := make(map[plumbing.Hash]bool)
	for _, fanout := range fanouts {
		if !fanout.IsDir() || len(fanout.Name()) != 2 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, fanout.Name()))
		require.NoError(t, err)
		for _, entry := range entries {
			objects[plumbing.NewHash(fanout.Name()+entry.Name())] = true
		}
	}
	return objects
}

// measureLedgerWrite runs write and returns the objects it added to the ledger at ledgerDir.
func measureLedgerWrite(t *testing.T, ledgerDir string, write func()) ledgerWriteCost {
	t.Helper()
	before := looseLedgerObjects(t, ledgerDir)
	write()
	repo, err := git.PlainOpen(ledgerDir)
	require.NoError(t, err)
	var cost ledgerWriteCost
	for hash := range looseLedgerObjects(t, ledgerDir) {
		if before[hash] {
			continue
		}
		obj, err := repo.Storer.EncodedObject(plumbing.AnyObject, hash)
		require.NoError(t, err)
		switch obj.Type() {
		case plumbing.CommitObject:
			cost.commits++
		case plumbing.TreeObject:
			cost.trees++
			cost.treeBytes += obj.Size()
			cost.largestTree = max(cost.largestTree, obj.Size())
		case plumbing.BlobObject:
			cost.blobs++
		}
	}
	return cost
}

// seedLedgerSnapshots commits n snapshots of unrelated files to a session ledger.
func seedLedgerSnapshots(t *testing.T, ledgerDir string, n int) {
	t.Helper()
	for i := range n {
		sum := sha256.Sum256(fmt.Appendf(nil, "seeded snapshot %d", i))
		repo, head, err := openLedgerHead(ledgerDir)
		require.NoError(t, err)
		_, err = commitBlob(repo, head, snapshotTreePath(hex.EncodeToString(sum[:])), sum[:], "seed")
		require.NoError(t, err)
	}
}

func TestLedgerWriteCost_CreationAddsOneCommit(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "cost-create"
	recordMutation(t, lms, session, filepath.Join(tempDir, "first.txt"), tempDir, "first")
	commits := commitCount(t, lms, session)

	result := recordMutation(t, lms, session, filepath.Join(tempDir, "created.txt"), tempDir, "created")

	assert.Equal(t, FileMutationCreate, result.Operation)
	assert.Equal(t, commits+1, commitCount(t, lms, session), "a creation commits only its post-creation snapshot")
}

func TestLedgerWriteCost_EditAddsOneCommitUnlessThePreImageDrifted(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "cost-edit"
	path := filepath.Join(tempDir, "edited.txt")
	created := recordMutation(t, lms, session, path, tempDir, "v1")

	commits := commitCount(t, lms, session)
	edited := recordMutation(t, lms, session, path, tempDir, "v2")
	assert.Equal(t, commits+1, commitCount(t, lms, session), "an unchanged pre-image is not committed again")
	assert.Equal(t, created.LedgerHashAfter, edited.LedgerHashBefore, "the previous after commit already holds the pre-image")

	require.NoError(t, os.WriteFile(path, []byte("changed outside the ledger"), constants.PermFilePrivate))
	commits = commitCount(t, lms, session)
	drifted := recordMutation(t, lms, session, path, tempDir, "v3")
	assert.Equal(t, commits+2, commitCount(t, lms, session), "a drifted pre-image is committed as a backup first")
	backup, err := lms.GetFileAtCommit(path, drifted.LedgerHashBefore, session)
	require.NoError(t, err)
	assert.Equal(t, "changed outside the ledger", backup)
}

// TestLedgerWriteCost_ObjectsPerMutationDoNotGrowWithSnapshots verifies that an edit writes the
// same objects whether the session holds few snapshots or many: one blob, the trees along its
// sharded path, and one commit.
func TestLedgerWriteCost_ObjectsPerMutationDoNotGrowWithSnapshots(t *testing.T) {
	t.Parallel()
	costs := make(map[int]ledgerWriteCost)
	for _, snapshots := range []int{16, 512} {
		lms, tempDir := setupTestLedger(t)
		const session = "cost-objects"
		ledgerDir, err := lms.createSessionLedger(session)
		require.NoError(t, err)
		seedLedgerSnapshots(t, ledgerDir, snapshots)
		path := filepath.Join(tempDir, "edited.txt")
		recordMutation(t, lms, session, path, tempDir, "v1")

		costs[snapshots] = measureLedgerWrite(t, ledgerDir, func() {
			recordMutation(t, lms, session, path, tempDir, "v2")
		})
	}

	// The root tree, the snapshots tree, one tree per shard level, and the leaf directory.
	pathTrees := 1 + 1 + ledgerSnapshotShardDepth
	for snapshots, cost := range costs {
		assert.Equal(t, 1, cost.commits, "snapshots=%d", snapshots)
		assert.Equal(t, pathTrees, cost.trees, "snapshots=%d", snapshots)
		assert.Equal(t, 1, cost.blobs, "snapshots=%d", snapshots)
		assert.Less(t, cost.treeBytes, int64(maxLedgerWriteTreeBytes), "snapshots=%d", snapshots)
	}
	assert.LessOrEqual(t, costs[512].largestTree, costs[16].largestTree*2,
		"the largest tree is one shard level, not a list of every snapshot")
}

func TestLedgerWriteCost_NothingIsWrittenToTheWorktree(t *testing.T) {
	t.Parallel()
	lms, tempDir := setupTestLedger(t)
	const session = "cost-worktree"
	ledgerDir, err := lms.createSessionLedger(session)
	require.NoError(t, err)
	indexPath := filepath.Join(ledgerDir, constants.GitDirname, constants.GitIndexFilename)
	index, err := os.ReadFile(indexPath)
	require.NoError(t, err)

	path := filepath.Join(tempDir, "edited.txt")
	recordMutation(t, lms, session, path, tempDir, "v1")
	recordMutation(t, lms, session, path, tempDir, "v2")

	entries, err := os.ReadDir(ledgerDir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.ElementsMatch(t, []string{constants.GitDirname, constants.GitignoreFilename}, names,
		"snapshots exist only as git objects")
	after, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	assert.Equal(t, index, after, "no mutation updates the git index")
}

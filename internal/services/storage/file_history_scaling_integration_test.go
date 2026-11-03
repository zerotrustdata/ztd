// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/sqliteutil"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// fileHistoryFixture is a real events store and a real git ledger behind one history handler.
// Its mutate method follows the production order: ledger begin, host write, ledger complete,
// event, then file mutation row.
type fileHistoryFixture struct {
	handler *HistoryHandler
	store   *LocalFirstEventsStore
	ledger  *GitLedgerService
	root    string
}

func newFileHistoryFixture(t *testing.T, sessions ...string) *fileHistoryFixture {
	t.Helper()
	handler, store, _, root := setupTestHistoryHandler(t)
	ledger, ok := handler.ledger.(*GitLedgerService)
	require.True(t, ok)
	for _, session := range sessions {
		require.NoError(t, store.CreateSession(session, constants.SessionTypeOperator, "file history", "owner"))
	}
	return &fileHistoryFixture{handler: handler, store: store, ledger: ledger, root: root}
}

func (f *fileHistoryFixture) path(name string) string {
	return filepath.Join(f.root, name)
}

func (f *fileHistoryFixture) mutate(t *testing.T, session, path, content string) *LedgerResult {
	t.Helper()
	target := openTestTarget(t, f.ledger.fileSvc, path, f.root)
	var (
		result *LedgerResult
		err    error
	)
	_, statErr := target.Stat()
	if statErr == nil {
		result, err = f.ledger.LedgerFileWrite(session, target)
	} else {
		result, err = f.ledger.MirrorFileCreate(session, target)
	}
	require.NoError(t, err)
	require.NoError(t, target.WriteFile([]byte(content), constants.PermFilePrivate))
	if statErr == nil {
		require.NoError(t, f.ledger.CompleteMirrorWrite(result, session))
	} else {
		require.NoError(t, f.ledger.CompleteMirrorCreate(result, session))
	}
	f.recordMutation(t, session, result)
	return result
}

func (f *fileHistoryFixture) recordMutation(t *testing.T, session string, result *LedgerResult) {
	t.Helper()
	eventID, err := f.store.RecordEvent(&Event{
		OperatorSessionID: session,
		Timestamp:         time.Now().UTC(),
		Type:              constants.Event.Operator.FileEdit.Completed,
		ContentText:       "file mutation",
	})
	require.NoError(t, err)
	require.NoError(t, f.store.RecordFileMutation(&FileMutationLog{
		EventID:          eventID,
		Filepath:         result.FilePath,
		SnapshotName:     result.SnapshotName,
		Operation:        result.Operation,
		LedgerHashBefore: result.LedgerHashBefore,
		LedgerHashAfter:  result.LedgerHashAfter,
		DiffStat:         result.DiffStat,
	}))
}

func (f *fileHistoryFixture) mutateUnrelated(t *testing.T, session, prefix string, count int) {
	t.Helper()
	for i := range count {
		f.mutate(t, session, f.path(fmt.Sprintf("%s-%d.txt", prefix, i)), "unrelated")
	}
}

func (f *fileHistoryFixture) history(t *testing.T, session, path string, limit int32) []string {
	t.Helper()
	result, err := f.handler.HandleFetchFileHistory(t.Context(), testutil.BuildFetchFileHistoryRequestedPayload(t, "exec", path, limit, session))
	require.NoError(t, err)
	require.True(t, result.GetSuccess())
	commits := make([]string, 0, len(result.GetHistory()))
	for _, entry := range result.GetHistory() {
		commits = append(commits, entry.GetCommitHash())
	}
	return commits
}

func (f *fileHistoryFixture) openLedgerRepo(t *testing.T, session string) *git.Repository {
	t.Helper()
	ledgerDir, err := f.ledger.openSessionLedger(session)
	require.NoError(t, err)
	repo, err := git.PlainOpen(ledgerDir)
	require.NoError(t, err)
	return repo
}

// walkAllCommits reads the whole commit chain the way an O(session) history lookup would.
func walkAllCommits(repo *git.Repository) ([]plumbing.Hash, error) {
	iter, err := repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("open commit log: %w", err)
	}
	defer iter.Close()
	var hashes []plumbing.Hash
	err = iter.ForEach(func(c *object.Commit) error {
		hashes = append(hashes, c.Hash)
		return nil
	})
	return hashes, err
}

// deleteCommitsExcept removes every commit object that is neither one of the mutations' before and
// after commits nor a parent of one. A lookup that reads any deleted commit fails with a missing
// object, so a passing lookup proves its work never reached the rest of the session. This asserts
// the work bound deterministically; a wall-clock threshold would measure the host instead.
func (f *fileHistoryFixture) deleteCommitsExcept(t *testing.T, session string, mutations ...*LedgerResult) int {
	t.Helper()
	repo := f.openLedgerRepo(t, session)
	keep := make(map[plumbing.Hash]struct{})
	for _, mutation := range mutations {
		for _, hash := range []string{mutation.LedgerHashBefore, mutation.LedgerHashAfter} {
			commit, err := repo.CommitObject(plumbing.NewHash(hash))
			require.NoError(t, err)
			keep[commit.Hash] = struct{}{}
			for _, parent := range commit.ParentHashes {
				keep[parent] = struct{}{}
			}
		}
	}
	all, err := walkAllCommits(repo)
	require.NoError(t, err)
	store, ok := repo.Storer.(*filesystem.Storage)
	require.True(t, ok)
	deleted := 0
	for _, hash := range all {
		if _, kept := keep[hash]; kept {
			continue
		}
		require.NoError(t, store.DeleteLooseObject(hash))
		deleted++
	}
	_, err = walkAllCommits(f.openLedgerRepo(t, session))
	require.Error(t, err, "a full walk must be impossible, or the work bound is not being tested")
	return deleted
}

func TestFileHistory_ReadsOnlyTheFilesOwnCommits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		unrelated int
	}{
		{name: "in_a_session_smaller_than_the_default_limit", unrelated: 20},
		{name: "in_a_session_larger_than_the_default_limit", unrelated: 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const session = "history-work-bound"
			f := newFileHistoryFixture(t, session)
			wanted := f.path("wanted.txt")

			f.mutateUnrelated(t, session, "before", tc.unrelated/2)
			created := f.mutate(t, session, wanted, "v1")
			f.mutateUnrelated(t, session, "between", tc.unrelated/2)
			written := f.mutate(t, session, wanted, "v2")
			f.mutateUnrelated(t, session, "after", 2)

			deleted := f.deleteCommitsExcept(t, session, created, written)
			require.GreaterOrEqual(t, deleted, tc.unrelated, "the unrelated session history must be gone")

			assert.Equal(t, []string{written.LedgerHashAfter, written.LedgerHashBefore, created.LedgerHashAfter},
				f.history(t, session, wanted, 0))
		})
	}
}

func TestFileHistory_NeverMutatedFileReadsNoCommit(t *testing.T) {
	const session = "history-never-mutated"
	f := newFileHistoryFixture(t, session)
	f.mutateUnrelated(t, session, "unrelated", 5)

	repo := f.openLedgerRepo(t, session)
	all, err := walkAllCommits(repo)
	require.NoError(t, err)
	store, ok := repo.Storer.(*filesystem.Storage)
	require.True(t, ok)
	for _, hash := range all {
		require.NoError(t, store.DeleteLooseObject(hash))
	}

	assert.Empty(t, f.history(t, session, f.path("never-mutated.txt"), 0))
}

func TestFileHistory_KeepsEveryRestorePointOfAPreexistingFile(t *testing.T) {
	const session = "history-restore-points"
	f := newFileHistoryFixture(t, session)
	path := f.path("preexisting.txt")
	require.NoError(t, os.WriteFile(path, []byte("original"), constants.PermFilePrivate))

	written := f.mutate(t, session, path, "edited")

	assert.Equal(t, []string{written.LedgerHashAfter, written.LedgerHashBefore}, f.history(t, session, path, 0))
	backup, err := f.ledger.GetFileAtCommit(path, written.LedgerHashBefore, session)
	require.NoError(t, err)
	assert.Equal(t, "original", backup, "the pre-mutation backup is the restore point of the original content")

	restoreResult, ledgerResult, err := f.handler.HandleRestoreFile(t.Context(), session,
		testutil.BuildRestoreFileRequestedPayload(t, "exec", path, written.LedgerHashBefore, session))
	require.NoError(t, err)
	require.True(t, restoreResult.GetSuccess())
	f.recordMutation(t, session, ledgerResult)

	assert.Equal(t, []string{
		ledgerResult.LedgerHashAfter, ledgerResult.LedgerHashBefore,
		written.LedgerHashAfter, written.LedgerHashBefore,
	}, f.history(t, session, path, 0))
}

func TestFileHistory_LimitReturnsTheNewestChanges(t *testing.T) {
	const session = "history-limit"
	f := newFileHistoryFixture(t, session)
	path := f.path("limited.txt")
	f.mutate(t, session, path, "v1")
	second := f.mutate(t, session, path, "v2")
	third := f.mutate(t, session, path, "v3")

	assert.Equal(t, []string{third.LedgerHashAfter}, f.history(t, session, path, 1))
	assert.Equal(t, []string{third.LedgerHashAfter, third.LedgerHashBefore, second.LedgerHashAfter}, f.history(t, session, path, 3))
	assert.Len(t, f.history(t, session, path, 50), 5, "v1 adds one change; v2 and v3 add two each")
}

func TestFileHistory_ScopedToTheRequestedSession(t *testing.T) {
	const sessionA, sessionB = "history-session-a", "history-session-b"
	f := newFileHistoryFixture(t, sessionA, sessionB)
	path := f.path("shared.txt")
	inA := f.mutate(t, sessionA, path, "from a")
	inB := f.mutate(t, sessionB, path, "from b")

	assert.Equal(t, []string{inA.LedgerHashAfter}, f.history(t, sessionA, path, 0))
	assert.Equal(t, []string{inB.LedgerHashAfter, inB.LedgerHashBefore}, f.history(t, sessionB, path, 0))

	_, err := f.handler.HandleFetchFileHistory(t.Context(), testutil.BuildFetchFileHistoryRequestedPayload(t, "exec", path, 0, "session-without-ledger"))
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
}

func TestFileHistory_MissingCandidateCommitFailsClosed(t *testing.T) {
	const session = "history-missing-commit"
	f := newFileHistoryFixture(t, session)
	path := f.path("tampered.txt")
	written := f.mutate(t, session, path, "v1")

	repo := f.openLedgerRepo(t, session)
	store, ok := repo.Storer.(*filesystem.Storage)
	require.True(t, ok)
	require.NoError(t, store.DeleteLooseObject(plumbing.NewHash(written.LedgerHashAfter)))

	result, err := f.handler.HandleFetchFileHistory(t.Context(), testutil.BuildFetchFileHistoryRequestedPayload(t, "exec", path, 0, session))
	require.ErrorIs(t, err, plumbing.ErrObjectNotFound)
	assert.False(t, result.GetSuccess())
}

// TestFileHistory_NotBlockedByAnInFlightLedgerWrite holds the lock every ledger write holds across
// its snapshot and commit. Those are O(files) on a large session, so a read queued behind that lock
// would inherit the writer's latency without any way to observe its own deadline.
func TestFileHistory_NotBlockedByAnInFlightLedgerWrite(t *testing.T) {
	const session = "history-writer-lock"
	f := newFileHistoryFixture(t, session)
	path := f.path("read-during-write.txt")
	written := f.mutate(t, session, path, "v1")

	f.ledger.mu.Lock()
	commits := make(chan []string, 1)
	failure := make(chan error, 1)
	go func() {
		result, err := f.handler.HandleFetchFileHistory(t.Context(), testutil.BuildFetchFileHistoryRequestedPayload(t, "exec", path, 0, session))
		if err != nil {
			failure <- err
			return
		}
		hashes := make([]string, 0, len(result.GetHistory()))
		for _, entry := range result.GetHistory() {
			hashes = append(hashes, entry.GetCommitHash())
		}
		commits <- hashes
	}()

	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	var (
		got    []string
		err    error
		queued bool
	)
	select {
	case got = <-commits:
	case err = <-failure:
	case <-timeout.C:
		queued = true
	}
	f.ledger.mu.Unlock()
	if queued {
		select {
		case <-commits:
		case <-failure:
		}
		require.Fail(t, "file history waited for the ledger write lock")
	}
	require.NoError(t, err)
	assert.Equal(t, []string{written.LedgerHashAfter}, got)
}

func TestListFileDiffs_AppliesTheLimitAfterSelectingTheFile(t *testing.T) {
	const session = "diffs-limit-after-filter"
	f := newFileHistoryFixture(t, session)
	wanted := f.path("wanted.txt")
	first := f.mutate(t, session, wanted, "v1\n")
	second := f.mutate(t, session, wanted, "v2\n")
	const limit = 50
	f.mutateUnrelated(t, session, "newer", limit+5)

	entries, err := f.handler.ListFileDiffs(t.Context(), session, wanted, limit)
	require.NoError(t, err)
	require.Len(t, entries, 2, "newer mutations of other files must not push this file out of the window")
	assert.Equal(t, second.LedgerHashAfter, entries[0].GetLedgerHashAfter())
	assert.Equal(t, first.LedgerHashAfter, entries[1].GetLedgerHashAfter())

	entries, err = f.handler.ListFileDiffs(t.Context(), session, wanted, 1)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, second.LedgerHashAfter, entries[0].GetLedgerHashAfter())

	entries, err = f.handler.ListFileDiffs(t.Context(), session, "", 3)
	require.NoError(t, err)
	assert.Len(t, entries, 3, "without a file the newest mutations of the whole session are listed")
}

// TestFileMutationsBySnapshotQuery_StaysOnTheIndex fixes the plan under planner statistics that
// favor the wrong join order: one path mutated across many sessions makes the snapshot index look
// unselective and the session lookup look selective. Starting from the session's events would visit
// every mutation of that session to find one file.
func TestFileMutationsBySnapshotQuery_StaysOnTheIndex(t *testing.T) {
	const sessions, perSession = 50, 8
	f := newFileHistoryFixture(t)
	for i := range sessions {
		session := fmt.Sprintf("plan-session-%d", i)
		require.NoError(t, f.store.CreateSession(session, constants.SessionTypeOperator, "plan", "owner"))
		for range perSession {
			eventID, err := f.store.RecordEvent(&Event{
				OperatorSessionID: session, Timestamp: time.Now().UTC(),
				Type: constants.Event.Operator.FileEdit.Completed, ContentText: "file mutation",
			})
			require.NoError(t, err)
			require.NoError(t, f.store.RecordFileMutation(&FileMutationLog{
				EventID: eventID, Filepath: "file", SnapshotName: "one-popular-snapshot", Operation: FileMutationWrite,
			}))
		}
	}
	_, err := f.store.db.ExecContext(t.Context(), "ANALYZE")
	require.NoError(t, err)

	rows, err := f.store.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+fileMutationsBySnapshotQuery, "one-popular-snapshot", "plan-session-1", 50)
	require.NoError(t, err)
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())

	require.Len(t, details, 3, strings.Join(details, "\n"))
	assert.Contains(t, details[0], "SEARCH fs USING COVERING INDEX idx_file_mutation_snapshot_name (snapshot_name=?)")
	assert.Contains(t, details[1], "SEARCH fm USING INTEGER PRIMARY KEY (rowid=?)")
	assert.Contains(t, details[2], "SEARCH e USING INTEGER PRIMARY KEY (rowid=?)")
	plan := strings.Join(details, "\n")
	assert.NotContains(t, plan, "SCAN", "a scan makes the lookup O(session)")
	assert.NotContains(t, plan, "TEMP B-TREE", "a sort makes the LIMIT read every row of the file")
}

func TestFileMutationRow_StoresTheKeyedNameAndNeverThePath(t *testing.T) {
	const session = "snapshot-name-opaque"
	f := newFileHistoryFixture(t, session)
	const marker = "secret-path-marker"
	path := f.path(marker + ".txt")
	result := f.mutate(t, session, path, "content")

	expected, err := f.ledger.SnapshotName(path)
	require.NoError(t, err)
	assert.Equal(t, expected, result.SnapshotName)

	var stored string
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `SELECT snapshot_name FROM file_mutation_snapshot`).Scan(&stored))
	assert.Equal(t, expected, stored)
	assert.Regexp(t, `^[0-9a-f]{64}$`, stored)
	assert.NotContains(t, stored, marker)
}

func TestRecordFileMutation_RejectsAMissingSnapshotName(t *testing.T) {
	const session = "snapshot-name-required"
	f := newFileHistoryFixture(t, session)
	eventID, err := f.store.RecordEvent(&Event{
		OperatorSessionID: session, Timestamp: time.Now().UTC(),
		Type: constants.Event.Operator.FileEdit.Completed, ContentText: "file mutation",
	})
	require.NoError(t, err)

	err = f.store.RecordFileMutation(&FileMutationLog{EventID: eventID, Filepath: "file", Operation: FileMutationCreate})
	require.ErrorIs(t, err, constants.ErrMissingRequiredField)
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreRecordFileMutationFailed)
}

// TestEventsStore_OpensADatabaseRecordedBeforeTheSnapshotTable covers a runtime root from before
// file_mutation_snapshot existed. Startup creates the table and leaves the earlier rows untouched
// (INV-STOR-SCHEMA-01): they stay readable by id and by session, and only later mutations are
// selected by file.
func TestEventsStore_OpensADatabaseRecordedBeforeTheSnapshotTable(t *testing.T) {
	fileSvc := newTestFileSvc(t, testutil.TempDir(t))
	_, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	testVault := CreateTestVault(t, fileSvc, privKey)
	db, err := sqliteutil.OpenDB(fileSvc, sqliteutil.DefaultDBConfig(fileSvc.Resolve(constants.CanonicalDBRelPath)), testutil.NewTestLogger())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	config := DefaultLocalFirstEventsStoreConfig()
	config.EncryptionVault = testVault

	const session = "pre-upgrade-session"
	record := func(store *LocalFirstEventsStore, snapshot string) int64 {
		t.Helper()
		eventID, err := store.RecordEvent(&Event{
			OperatorSessionID: session, Timestamp: time.Now().UTC(),
			Type: constants.Event.Operator.FileEdit.Completed, ContentText: "file mutation",
		})
		require.NoError(t, err)
		require.NoError(t, store.RecordFileMutation(&FileMutationLog{
			EventID: eventID, Filepath: "file", SnapshotName: snapshot, Operation: FileMutationWrite,
		}))
		return eventID
	}

	before, err := NewLocalFirstEventsStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	require.NoError(t, before.CreateSession(session, constants.SessionTypeOperator, "upgrade", "owner"))
	preUpgradeEvent := record(before, "snapshot")
	require.NoError(t, before.Close())
	_, err = db.ExecContext(t.Context(), `DROP TABLE file_mutation_snapshot`)
	require.NoError(t, err)

	after, err := NewLocalFirstEventsStore(config, db, testutil.NewTestLogger(), fileSvc)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, after.Close()) })

	bySession, err := after.ListFileMutationsBySession(session, 10)
	require.NoError(t, err)
	require.Len(t, bySession, 1, "the earlier row stays readable")
	assert.Equal(t, preUpgradeEvent, bySession[0].EventID)
	bySnapshot, err := after.ListFileMutationsBySnapshot(t.Context(), session, "snapshot", 10)
	require.NoError(t, err)
	assert.Empty(t, bySnapshot, "the earlier row has no key and was not back-filled")

	postUpgradeEvent := record(after, "snapshot")
	bySnapshot, err = after.ListFileMutationsBySnapshot(t.Context(), session, "snapshot", 10)
	require.NoError(t, err)
	require.Len(t, bySnapshot, 1)
	assert.Equal(t, postUpgradeEvent, bySnapshot[0].EventID)
}

// TestFileMutationSnapshot_LeavesWithItsMutationRow covers both ways the two rows could diverge: a
// mutation row deleted by retention takes its key with it, and a failed key insert rolls the
// mutation back.
func TestFileMutationSnapshot_LeavesWithItsMutationRow(t *testing.T) {
	const session = "snapshot-row-lifecycle"
	f := newFileHistoryFixture(t, session)
	result := f.mutate(t, session, f.path("pruned.txt"), "v1")

	var keys int
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM file_mutation_snapshot`).Scan(&keys))
	require.Equal(t, 1, keys)

	_, err := f.store.db.ExecWithRetry(t.Context(), `DELETE FROM file_mutation_log`)
	require.NoError(t, err)
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM file_mutation_snapshot`).Scan(&keys))
	assert.Zero(t, keys, "retention deletes file_mutation_log rows; their keys must not outlive them")
	assert.Empty(t, f.history(t, session, result.FilePath, 0))

	_, err = f.store.db.ExecWithRetry(t.Context(), `DROP TABLE file_mutation_snapshot`)
	require.NoError(t, err)
	eventID, err := f.store.RecordEvent(&Event{
		OperatorSessionID: session, Timestamp: time.Now().UTC(),
		Type: constants.Event.Operator.FileEdit.Completed, ContentText: "file mutation",
	})
	require.NoError(t, err)
	err = f.store.RecordFileMutation(&FileMutationLog{EventID: eventID, Filepath: "file", SnapshotName: "snapshot", Operation: FileMutationWrite})
	require.ErrorIs(t, err, constants.ErrLocalFirstEventsStoreRecordFileMutationFailed)
	var mutations int
	require.NoError(t, f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM file_mutation_log`).Scan(&mutations))
	assert.Zero(t, mutations, "a mutation row must not be committed without its key")
}

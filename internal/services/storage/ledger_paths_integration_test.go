// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package storage

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

const (
	ledgerPathMarkerDir     = "ZTDPATHMARKERDIR"
	ledgerPathMarkerFile    = "ZTDPATHMARKERFILE.txt"
	ledgerContentMarkerText = "ZTDCONTENTMARKER"
)

// ledgerRoot is the on-disk ledger tree of a service.
func ledgerRoot(lms *GitLedgerService) string {
	return lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname))
}

// assertLedgerHoldsNoMarkers walks every entry of the ledger tree and asserts that no marker
// appears in any directory or file name, any file's bytes, or any decompressed git object
// (commit messages, trees, blobs) of the default repository or a session repository.
func assertLedgerHoldsNoMarkers(t *testing.T, root string, markers ...string) {
	t.Helper()
	contains := func(data []byte) (string, bool) {
		for _, m := range markers {
			if strings.Contains(string(data), m) {
				return m, true
			}
		}
		return "", false
	}

	repoDirs := map[string]bool{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		if m, found := contains([]byte(rel)); found {
			t.Errorf("ledger entry name carries marker %q", m)
		}
		if d.IsDir() && d.Name() == constants.GitDirname {
			repoDirs[filepath.Dir(path)] = true
		}
		if d.Type().IsRegular() {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			if m, found := contains(data); found {
				t.Errorf("ledger file %s carries marker %q", filepath.Base(path), m)
			}
		}
		return nil
	}))
	require.NotEmpty(t, repoDirs, "the ledger tree must contain git repositories")

	objects := 0
	for dir := range repoDirs {
		repo, err := git.PlainOpen(dir)
		require.NoError(t, err)
		iter, err := repo.Storer.IterEncodedObjects(plumbing.AnyObject)
		require.NoError(t, err)
		require.NoError(t, iter.ForEach(func(o plumbing.EncodedObject) error {
			r, err := o.Reader()
			require.NoError(t, err)
			defer func() { _ = r.Close() }()
			data, err := io.ReadAll(r)
			require.NoError(t, err)
			objects++
			if m, found := contains(data); found {
				t.Errorf("git %s object %s carries marker %q", o.Type(), o.Hash(), m)
			}
			return nil
		}))
	}
	require.NotZero(t, objects, "no git objects were inspected")
}

func TestLedgerPaths_NothingOnDiskCarriesAPath(t *testing.T) {
	lms, tempDir := setupTestLedger(t)
	hostDir := filepath.Join(tempDir, ledgerPathMarkerDir)
	require.NoError(t, os.MkdirAll(hostDir, 0o755))
	hostFile := filepath.Join(hostDir, ledgerPathMarkerFile)
	const sessionID = "sess-no-path-leak"

	create, err := lms.MirrorFileCreate(sessionID, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte(ledgerContentMarkerText+" v1\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorCreate(create, sessionID))

	write, err := lms.LedgerFileWrite(sessionID, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte(ledgerContentMarkerText+" v2\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorWrite(write, sessionID))

	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, hostFile, tempDir), create.LedgerHashAfter, sessionID, sessionID)
	require.NoError(t, err)

	// A mutation under the default (empty session) repository exercises the other repo.
	defaultCreate, err := lms.MirrorFileCreate("", openTestTarget(t, lms.fileSvc, filepath.Join(hostDir, "default-"+ledgerPathMarkerFile), tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(hostDir, "default-"+ledgerPathMarkerFile), []byte(ledgerContentMarkerText), 0o644))
	require.NoError(t, lms.CompleteMirrorCreate(defaultCreate, ""))

	assertLedgerHoldsNoMarkers(t, ledgerRoot(lms), ledgerPathMarkerDir, ledgerPathMarkerFile, ledgerContentMarkerText, tempDir)

	// Reads still resolve the path through the keyed name, and nothing they return carries a path.
	history, err := lms.GetFileHistory(hostFile, 50, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	for _, entry := range history {
		assert.NotContains(t, entry.Message, ledgerPathMarkerDir)
		assert.NotContains(t, entry.Message, tempDir)
	}
	content, err := lms.GetFileAtCommit(hostFile, create.LedgerHashAfter, sessionID)
	require.NoError(t, err)
	assert.Equal(t, ledgerContentMarkerText+" v1\n", content)

	commits, err := lms.ListCommits(sessionID, 0)
	require.NoError(t, err)
	require.NotEmpty(t, commits)
	for _, c := range commits {
		assert.NotContains(t, c.Message, ledgerPathMarkerDir)
		assert.NotContains(t, c.DiffStat, ledgerPathMarkerFile)
	}

	// The patch labels the file with the caller's path, never the keyed name.
	diff, err := lms.GetDiffContent(write.LedgerHashBefore, write.LedgerHashAfter, hostFile, sessionID)
	require.NoError(t, err)
	assert.Contains(t, diff, "+"+ledgerContentMarkerText+" v2")
	assert.Equal(t, write.DiffContent, diff)
}

func TestLedgerPaths_HistoryFailsClosedWhileVaultLocked(t *testing.T) {
	lms, tempDir := setupTestLedgerLocked(t)

	_, err := lms.GetFileHistory(filepath.Join(tempDir, "f.txt"), 10, "")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	_, err = lms.GetFileAtCommit(filepath.Join(tempDir, "f.txt"), strings.Repeat("a", 40), "")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
	_, err = lms.GetDiffContent(strings.Repeat("a", 40), strings.Repeat("b", 40), filepath.Join(tempDir, "f.txt"), "")
	require.ErrorIs(t, err, constants.ErrLedgerVaultRequired)
}

// corruptHead makes every commit in a repository fail while objects stay readable, which is how a
// snapshot-commit failure is induced without depending on filesystem permissions.
func corruptHead(t *testing.T, repoDir string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, constants.GitDirname, "HEAD"), []byte("ref: refs/heads/../../broken\n"), 0o600))
}

func TestLedgerPaths_SnapshotCommitFailureFailsTheMirror(t *testing.T) {
	lms, tempDir := setupTestLedger(t)
	hostFile := filepath.Join(tempDir, "snapshot-failure.txt")
	require.NoError(t, os.WriteFile(hostFile, []byte("before\n"), 0o644))

	// Begin: the pre-mutation commit fails, so the mutation is refused and no hash is invented.
	const beginSession = "sess-begin-failure"
	sessionDir, err := lms.createSessionLedger(beginSession)
	require.NoError(t, err)
	corruptHead(t, sessionDir)
	result, err := lms.LedgerFileWrite(beginSession, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.Error(t, err)
	assert.Nil(t, result)

	// Complete (write): the post-mutation commit fails, and the failure is returned, not logged away.
	const writeSession = "sess-complete-write-failure"
	write, err := lms.LedgerFileWrite(writeSession, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NotEmpty(t, write.LedgerHashBefore)
	writeDir, err := lms.openSessionLedger(writeSession)
	require.NoError(t, err)
	corruptHead(t, writeDir)
	require.NoError(t, os.WriteFile(hostFile, []byte("after\n"), 0o644))
	require.Error(t, lms.CompleteMirrorWrite(write, writeSession))
	assert.Empty(t, write.LedgerHashAfter)

	// Complete (create): same, for the creation path.
	const createSession = "sess-complete-create-failure"
	createFile := filepath.Join(tempDir, "snapshot-failure-create.txt")
	create, err := lms.MirrorFileCreate(createSession, openTestTarget(t, lms.fileSvc, createFile, tempDir))
	require.NoError(t, err)
	createDir, err := lms.openSessionLedger(createSession)
	require.NoError(t, err)
	corruptHead(t, createDir)
	require.NoError(t, os.WriteFile(createFile, []byte("new\n"), 0o644))
	require.Error(t, lms.CompleteMirrorCreate(create, createSession))
	assert.Empty(t, create.LedgerHashAfter)
}

func TestLedgerPaths_RestoreRefusesWhenBackupFails(t *testing.T) {
	lms, tempDir := setupTestLedger(t)
	hostFile := filepath.Join(tempDir, "restore-refused.txt")
	const sessionID = "sess-restore-refused"

	create, err := lms.MirrorFileCreate(sessionID, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte("original\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorCreate(create, sessionID))

	write, err := lms.LedgerFileWrite(sessionID, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte("current\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorWrite(write, sessionID))

	// The backup commit cannot be made: the restore is refused and the host file is untouched.
	sessionDir, err := lms.openSessionLedger(sessionID)
	require.NoError(t, err)
	corruptHead(t, sessionDir)
	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, hostFile, tempDir), create.LedgerHashAfter, sessionID, sessionID)
	require.Error(t, err)
	got, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, "current\n", string(got), "a refused restore must not modify the host file")
}

func TestLedgerPaths_RestoreRefusesWhenBackupSnapshotCannotBeWritten(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permissions do not constrain root")
	}
	if runtime.GOOS == "windows" {
		t.Skip("a read-only directory mode does not block file creation on Windows")
	}
	lms, tempDir := setupTestLedger(t)
	hostFile := filepath.Join(tempDir, "restore-backup-refused.txt")
	const sessionID = "sess-restore-backup-refused"

	create, err := lms.MirrorFileCreate(sessionID, openTestTarget(t, lms.fileSvc, hostFile, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(hostFile, []byte("original\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorCreate(create, sessionID))
	require.NoError(t, os.WriteFile(hostFile, []byte("edited outside the ledger\n"), 0o644))

	sessionDir, err := lms.openSessionLedger(sessionID)
	require.NoError(t, err)
	snapshots := filepath.Join(sessionDir, constants.FilesDirname)
	require.NoError(t, os.Chmod(snapshots, 0o500))
	t.Cleanup(func() { _ = os.Chmod(snapshots, 0o700) })

	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, hostFile, tempDir), create.LedgerHashAfter, sessionID, sessionID)
	require.Error(t, err)
	got, err := os.ReadFile(hostFile)
	require.NoError(t, err)
	assert.Equal(t, "edited outside the ledger\n", string(got), "a refused restore must not modify the host file")
}

func TestLedgerPaths_ReadOfUnknownSessionCreatesNothing(t *testing.T) {
	hh, _, _, tempDir := setupTestHistoryHandler(t)
	lms, ok := hh.ledger.(*GitLedgerService)
	require.True(t, ok)
	sessionsDir := lms.fileSvc.Resolve(filepath.Join(constants.DataDirname, constants.LedgerDirname, constants.SessionsDirname))
	hostFile := filepath.Join(tempDir, "unknown-session.txt")
	const sessionID = "sess-never-written"

	_, err := lms.GetFileHistory(hostFile, 10, sessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
	_, err = lms.GetFileAtCommit(hostFile, strings.Repeat("a", 40), sessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
	_, err = lms.GetDiffContent(strings.Repeat("a", 40), strings.Repeat("b", 40), hostFile, sessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
	_, err = lms.ListCommits(sessionID, 10)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)
	_, err = lms.RestoreFileFromCommit(openTestTarget(t, lms.fileSvc, hostFile, tempDir), strings.Repeat("a", 40), sessionID, sessionID)
	require.ErrorIs(t, err, constants.ErrLedgerSessionNotFound)

	// The request-payload paths (operator_session_id comes from the caller) fail typed and create nothing.
	history, err := hh.HandleFetchFileHistory(testutil.BuildFetchFileHistoryRequestedPayload(t, "exec-unknown-history", hostFile, 10, sessionID))
	require.NoError(t, err)
	assert.False(t, history.Success)
	restore, _ := hh.HandleRestoreFile(t.Context(), sessionID, testutil.BuildRestoreFileRequestedPayload(t, "exec-unknown", hostFile, strings.Repeat("a", 40), sessionID))
	assert.False(t, restore.Success)

	entries, err := os.ReadDir(sessionsDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "reading an unknown session must not create a ledger for it")
}

func TestLedgerPaths_ConcurrentEditsAttributeOnlyTheirOwnPath(t *testing.T) {
	lms, tempDir := setupTestLedger(t)
	const sessionID = "sess-concurrent"
	fileA := filepath.Join(tempDir, "concurrent-a.txt")
	fileB := filepath.Join(tempDir, "concurrent-b.txt")
	require.NoError(t, os.WriteFile(fileA, []byte("ALPHA-OLD\n"), 0o644))
	require.NoError(t, os.WriteFile(fileB, []byte("BETA-OLD\n"), 0o644))

	// Deterministic interleaving: B's whole mirror lands inside A's before..after commit range.
	a, err := lms.LedgerFileWrite(sessionID, openTestTarget(t, lms.fileSvc, fileA, tempDir))
	require.NoError(t, err)
	b, err := lms.LedgerFileWrite(sessionID, openTestTarget(t, lms.fileSvc, fileB, tempDir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fileA, []byte("ALPHA-NEW\n"), 0o644))
	require.NoError(t, os.WriteFile(fileB, []byte("BETA-NEW\n"), 0o644))
	require.NoError(t, lms.CompleteMirrorWrite(b, sessionID))
	require.NoError(t, lms.CompleteMirrorWrite(a, sessionID))

	assert.Contains(t, a.DiffContent, "+ALPHA-NEW")
	assert.NotContains(t, a.DiffContent, "BETA-NEW")
	assert.NotContains(t, a.DiffContent, "BETA-OLD")
	assert.Equal(t, "1 file changed, 1 insertion(+), 1 deletion(-)", a.DiffStat)
	assert.Contains(t, b.DiffContent, "+BETA-NEW")
	assert.NotContains(t, b.DiffContent, "ALPHA-NEW")

	// The stored-hash read path (FETCH_FILE_DIFF) agrees with what the mirror computed.
	fetched, err := lms.GetDiffContent(a.LedgerHashBefore, a.LedgerHashAfter, fileA, sessionID)
	require.NoError(t, err)
	assert.Equal(t, a.DiffContent, fetched)
}

func TestLedgerPaths_ConcurrentGoroutinesAttributeOnlyTheirOwnPath(t *testing.T) {
	lms, tempDir := setupTestLedger(t)
	const sessionID = "sess-concurrent-stress"
	const writers = 8

	results := make([]*LedgerResult, writers)
	files := make([]string, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		files[i] = filepath.Join(tempDir, "stress-"+string(rune('a'+i))+".txt")
		require.NoError(t, os.WriteFile(files[i], []byte("zero\n"), 0o644))
	}
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target, err := lms.fileSvc.OpenFileEditTarget(t.Context(), files[i], tempDir)
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = target.Close() }()
			res, err := lms.LedgerFileWrite(sessionID, target)
			if err != nil {
				errs[i] = err
				return
			}
			if err := target.WriteFile([]byte("marker-"+string(rune('a'+i))+"\n"), 0o644); err != nil {
				errs[i] = err
				return
			}
			errs[i] = lms.CompleteMirrorWrite(res, sessionID)
			results[i] = res
		}()
	}
	wg.Wait()

	for i := 0; i < writers; i++ {
		require.NoError(t, errs[i])
		own := "+marker-" + string(rune('a'+i))
		assert.Contains(t, results[i].DiffContent, own)
		assert.Equal(t, "1 file changed, 1 insertion(+), 1 deletion(-)", results[i].DiffStat)
		for j := 0; j < writers; j++ {
			if j != i {
				assert.NotContains(t, results[i].DiffContent, "marker-"+string(rune('a'+j)))
			}
		}
	}
}

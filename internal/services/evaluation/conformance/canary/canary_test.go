// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package canary

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

func newFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return files
}

func seedRun(t *testing.T, files fs.RuntimeFileService, runID string) *Set {
	t.Helper()
	set, err := Seed(context.Background(), files, runID)
	require.NoError(t, err)
	return set
}

func snapshot(t *testing.T, files fs.RuntimeFileService, root string) *evalv1.CanaryManifest {
	t.Helper()
	manifest, err := Snapshot(context.Background(), files, root)
	require.NoError(t, err)
	return manifest
}

func TestSeedCreatesFixedFilesAndEmptyCaseDirectory(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)

	set := seedRun(t, files, "run-1")

	assert.Equal(t, "eval/targets/conformance-run-1", set.Root)
	assert.Len(t, set.Files, 8)
	assert.Equal(t, "eval/targets/conformance-run-1/alpha.txt", set.ReadPath())

	content, err := files.ReadFile(ctx, set.ReadPath())
	require.NoError(t, err)
	assert.Equal(t, "ztd canary run-1 alpha.txt\n", string(content))

	manifest := snapshot(t, files, set.Root)
	entries := make(map[string]*evalv1.CanaryEntry, len(manifest.GetEntries()))
	for _, entry := range manifest.GetEntries() {
		entries[entry.GetPath()] = entry
	}
	assert.Len(t, entries, 10, "8 files, the nested directory and the cases directory")
	assert.Equal(t, int64(-1), entries[constants.EvaluationCanaryTargetDirname].GetSize())
	assert.Empty(t, entries[constants.EvaluationCanaryTargetDirname].GetSha256())
	assert.Equal(t, int64(-1), entries["nested"].GetSize())
	assert.Contains(t, entries, "nested/deep.txt")
}

func TestSeedRejectsExistingRoot(t *testing.T) {
	files := newFiles(t)
	seedRun(t, files, "run-dup")

	_, err := Seed(context.Background(), files, "run-dup")

	require.ErrorIs(t, err, constants.ErrConformanceCanaryInvalid)
}

func TestSeedRejectsUnsafeRunIDs(t *testing.T) {
	cases := []struct {
		name  string
		runID string
	}{
		{name: "empty", runID: ""},
		{name: "parent traversal", runID: "../escape"},
		{name: "path separator", runID: "a/b"},
		{name: "space", runID: "run 1"},
		{name: "shell metacharacter", runID: "run;rm"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Seed(context.Background(), newFiles(t), tc.runID)
			require.ErrorIs(t, err, constants.ErrConformanceCanaryInvalid)
		})
	}
}

func TestValidateAttemptIDRejectsUnsafeIDs(t *testing.T) {
	require.NoError(t, ValidateAttemptID("attempt-01.a_B"))
	for _, attemptID := range []string{"", "../x", "x/y", "x y"} {
		require.ErrorIs(t, ValidateAttemptID(attemptID), constants.ErrConformanceCanaryInvalid, attemptID)
	}
}

func TestTargetPathIsCaseFileUnderRoot(t *testing.T) {
	set := &Set{Root: "eval/targets/conformance-run-1", Files: []string{"alpha.txt"}}

	assert.Equal(t, "eval/targets/conformance-run-1/cases/attempt-1.txt", set.TargetPath("attempt-1"))
}

func TestTargetRelIsCaseFileRelativeToCanaryRoot(t *testing.T) {
	set := &Set{Root: "eval/targets/conformance-run-1", Files: []string{"alpha.txt"}}

	assert.Equal(t, "cases/attempt-1.txt", TargetRel("attempt-1"))
	assert.Equal(t, path.Join(set.Root, TargetRel("attempt-1")), set.TargetPath("attempt-1"))
}

func TestSnapshotRecordsTargetWriteAtTargetRel(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-target-rel")
	before := snapshot(t, files, set.Root)
	content := []byte("granted write\n")

	require.NoError(t, files.WriteFile(ctx, set.TargetPath("attempt-1"), content, constants.PermFilePrivate))
	after := snapshot(t, files, set.Root)

	var recorded []string
	for _, entry := range after.GetEntries() {
		recorded = append(recorded, entry.GetPath())
	}
	assert.Contains(t, recorded, TargetRel("attempt-1"))
	assert.Equal(t, []string{TargetRel("attempt-1")}, Diff(before, after).Added)
	assert.True(t, ExactlyWrote(before, after, TargetRel("attempt-1"), content))
}

func TestSnapshotWithoutChangeProducesEmptyDelta(t *testing.T) {
	files := newFiles(t)
	set := seedRun(t, files, "run-stable")

	before := snapshot(t, files, set.Root)
	after := snapshot(t, files, set.Root)

	assert.True(t, Diff(before, after).Empty())
}

func TestDiffReportsAddedFile(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-add")
	before := snapshot(t, files, set.Root)

	require.NoError(t, files.WriteFile(ctx, set.TargetPath("attempt-1"), []byte("new"), constants.PermFilePrivate))
	delta := Diff(before, snapshot(t, files, set.Root))

	assert.Equal(t, []string{"cases/attempt-1.txt"}, delta.Added)
	assert.Empty(t, delta.Removed)
	assert.Empty(t, delta.Changed)
}

func TestDiffReportsRemovedFile(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-remove")
	before := snapshot(t, files, set.Root)

	require.NoError(t, files.Remove(ctx, joinRel(set.Root, "bravo.txt")))
	delta := Diff(before, snapshot(t, files, set.Root))

	assert.Equal(t, []string{"bravo.txt"}, delta.Removed)
	assert.Empty(t, delta.Added)
	assert.Empty(t, delta.Changed)
}

func TestDiffReportsChangedFile(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-change")
	before := snapshot(t, files, set.Root)

	require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "charlie.txt"), []byte("altered"), constants.PermFilePrivate))
	delta := Diff(before, snapshot(t, files, set.Root))

	assert.Equal(t, []string{"charlie.txt"}, delta.Changed)
	assert.Empty(t, delta.Added)
	assert.Empty(t, delta.Removed)
}

func TestDiffReportsChangeInsideNestedDirectory(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-nested")
	before := snapshot(t, files, set.Root)

	require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "nested/deep.txt"), []byte("altered"), constants.PermFilePrivate))
	delta := Diff(before, snapshot(t, files, set.Root))

	assert.Equal(t, []string{"nested/deep.txt"}, delta.Changed)
	assert.Empty(t, delta.Added)
	assert.Empty(t, delta.Removed)
}

func TestDiffReportsAddedDirectory(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-dir")
	before := snapshot(t, files, set.Root)

	require.NoError(t, files.MkdirAll(ctx, joinRel(set.Root, "extra"), constants.PermDirPrivate))
	delta := Diff(before, snapshot(t, files, set.Root))

	assert.Equal(t, []string{"extra"}, delta.Added)
	assert.Empty(t, delta.Removed)
	assert.Empty(t, delta.Changed)
}

func TestExactlyWroteAcceptsSingleNewTargetWithExpectedContent(t *testing.T) {
	ctx := context.Background()
	files := newFiles(t)
	set := seedRun(t, files, "run-exact")
	before := snapshot(t, files, set.Root)
	content := []byte("granted write\n")

	require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "cases/attempt-1.txt"), content, constants.PermFilePrivate))

	assert.True(t, ExactlyWrote(before, snapshot(t, files, set.Root), "cases/attempt-1.txt", content))
}

func TestExactlyWroteRejectsWrongContentExtraFileSecondChangeAndNoChange(t *testing.T) {
	ctx := context.Background()
	content := []byte("granted write\n")

	t.Run("wrong content", func(t *testing.T) {
		files := newFiles(t)
		set := seedRun(t, files, "run-wrong")
		before := snapshot(t, files, set.Root)
		require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "cases/attempt-1.txt"), []byte("other"), constants.PermFilePrivate))

		assert.False(t, ExactlyWrote(before, snapshot(t, files, set.Root), "cases/attempt-1.txt", content))
	})

	t.Run("extra file", func(t *testing.T) {
		files := newFiles(t)
		set := seedRun(t, files, "run-extra")
		before := snapshot(t, files, set.Root)
		require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "cases/attempt-1.txt"), content, constants.PermFilePrivate))
		require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "stray.txt"), []byte("stray"), constants.PermFilePrivate))

		assert.False(t, ExactlyWrote(before, snapshot(t, files, set.Root), "cases/attempt-1.txt", content))
	})

	t.Run("second changed file", func(t *testing.T) {
		files := newFiles(t)
		set := seedRun(t, files, "run-second")
		before := snapshot(t, files, set.Root)
		require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "cases/attempt-1.txt"), content, constants.PermFilePrivate))
		require.NoError(t, files.WriteFile(ctx, joinRel(set.Root, "alpha.txt"), []byte("tampered"), constants.PermFilePrivate))

		assert.False(t, ExactlyWrote(before, snapshot(t, files, set.Root), "cases/attempt-1.txt", content))
	})

	t.Run("no change", func(t *testing.T) {
		files := newFiles(t)
		set := seedRun(t, files, "run-none")
		before := snapshot(t, files, set.Root)

		assert.False(t, ExactlyWrote(before, snapshot(t, files, set.Root), "cases/attempt-1.txt", content))
	})
}

func TestSnapshotRejectsSymlinkUnderRoot(t *testing.T) {
	files := newFiles(t)
	set := seedRun(t, files, "run-symlink")
	link := filepath.Join(files.Resolve(set.Root), "link.txt")
	require.NoError(t, os.Symlink(filepath.Join(files.Resolve(set.Root), "alpha.txt"), link))

	_, err := Snapshot(context.Background(), files, set.Root)

	require.ErrorIs(t, err, constants.ErrConformanceCanaryInvalid)
}

// joinRel joins a canary root and a relative entry with forward slashes.
func joinRel(root, rel string) string {
	return root + "/" + rel
}

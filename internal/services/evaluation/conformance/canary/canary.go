// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package canary seeds, snapshots and diffs a known file set under a
// runtime-relative root. It observes effects independently of the governed
// path: a conformance case passes only when the canary delta shows exactly the
// write it was granted and nothing else.
package canary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	evalv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/eval/v1"
)

// Set is a seeded canary root. Paths are relative to the RuntimeFileService
// base and use forward slashes.
type Set struct {
	Root  string   // e.g. evaluation/targets/conformance-<runID>
	Files []string // the seeded files, relative to Root, sorted
}

// Delta is the difference between two manifests.
type Delta struct{ Added, Removed, Changed []string }

// fixedFiles is the canary file set relative to a root, in sorted order.
// nested/deep.txt exercises a change inside a subdirectory.
var fixedFiles = []string{
	"alpha.txt",
	"bravo.txt",
	"charlie.txt",
	"delta.txt",
	"echo.txt",
	"foxtrot.txt",
	"golf.txt",
	"nested/deep.txt",
}

// idPattern admits run and attempt identifiers that are safe as a single path
// segment.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateAttemptID reports ErrConformanceCanaryInvalid unless attemptID is a
// safe path segment. TargetPath does not validate: callers must validate the
// attempt ID first.
func ValidateAttemptID(attemptID string) error {
	if !idPattern.MatchString(attemptID) {
		return fmt.Errorf("conformance canary: attempt id %q: %w", attemptID, constants.ErrConformanceCanaryInvalid)
	}
	return nil
}

// Seed creates a new canary root for runID under the controlled target
// directory with a fixed file set. It fails if the root already exists.
func Seed(ctx context.Context, files fs.RuntimeFileService, runID string) (*Set, error) {
	if !idPattern.MatchString(runID) {
		return nil, fmt.Errorf("conformance canary: run id %q: %w", runID, constants.ErrConformanceCanaryInvalid)
	}
	if err := files.MkdirAll(ctx, constants.EvaluationControlledTargetDirname, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("conformance canary: create controlled target directory: %w", err)
	}
	root := path.Join(constants.EvaluationControlledTargetDirname, constants.EvaluationConformanceCanaryPrefix+runID)
	set, err := SeedAt(ctx, files, root, runID)
	if err != nil {
		return nil, err
	}
	if err := files.MkdirAll(ctx, path.Join(root, constants.EvaluationCanaryTargetDirname), constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("conformance canary: create case directory: %w", err)
	}
	return set, nil
}

// SeedAt creates the same fixed file set under an arbitrary root (campaign
// workspaces). It fails if root already exists.
func SeedAt(ctx context.Context, files fs.RuntimeFileService, root, label string) (*Set, error) {
	if _, err := files.Lstat(ctx, root); err == nil {
		return nil, fmt.Errorf("conformance canary: root %s already exists: %w", root, constants.ErrConformanceCanaryInvalid)
	} else if !errors.Is(err, constants.ErrNotFound) {
		return nil, fmt.Errorf("conformance canary: stat root %s: %w", root, err)
	}
	if err := files.MkdirAll(ctx, root, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("conformance canary: create root %s: %w", root, err)
	}
	for _, name := range fixedFiles {
		content := []byte(fmt.Sprintf("ztd canary %s %s\n", label, name))
		if err := files.WriteFile(ctx, path.Join(root, name), content, constants.PermFilePrivate); err != nil {
			return nil, fmt.Errorf("conformance canary: write %s: %w", name, err)
		}
	}
	return &Set{Root: root, Files: slices.Clone(fixedFiles)}, nil
}

// ReadPath is the seeded file the sentinel and the read baseline read.
func (s *Set) ReadPath() string {
	return path.Join(s.Root, s.Files[0])
}

// TargetRel is the case target file for attemptID, relative to the canary root.
// It needs no Set state, so the verifier recomputes it from an attempt ID alone.
// It does not exist after Seed. Callers must validate attemptID first.
func TargetRel(attemptID string) string {
	return path.Join(constants.EvaluationCanaryTargetDirname, attemptID+".txt")
}

// TargetPath is the case target file for attemptID under the canary root.
func (s *Set) TargetPath(attemptID string) string {
	return path.Join(s.Root, TargetRel(attemptID))
}

// Snapshot lists every entry under root, sorted by path.
func Snapshot(ctx context.Context, files fs.RuntimeFileService, root string) (*evalv1.CanaryManifest, error) {
	info, err := files.Lstat(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("conformance canary: stat root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("conformance canary: root %s is not a directory: %w", root, constants.ErrConformanceCanaryInvalid)
	}
	var entries []*evalv1.CanaryEntry
	if err := walk(ctx, files, root, "", &entries); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].GetPath() < entries[j].GetPath() })
	return &evalv1.CanaryManifest{Entries: entries}, nil
}

// walk appends the entries under root/rel. Directories are recorded and then
// descended into; any entry that is not a regular file or directory fails.
func walk(ctx context.Context, files fs.RuntimeFileService, root, rel string, out *[]*evalv1.CanaryEntry) error {
	dir := path.Join(root, rel)
	dirEntries, err := files.ReadDir(ctx, dir)
	if err != nil {
		return fmt.Errorf("conformance canary: read directory %s: %w", dir, err)
	}
	for _, dirEntry := range dirEntries {
		entryRel := path.Join(rel, dirEntry.Name())
		full := path.Join(root, entryRel)
		info, err := files.Lstat(ctx, full)
		if err != nil {
			return fmt.Errorf("conformance canary: stat %s: %w", full, err)
		}
		switch {
		case info.IsDir():
			*out = append(*out, &evalv1.CanaryEntry{Path: entryRel, Sha256: "", Size: -1})
			if err := walk(ctx, files, root, entryRel, out); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			data, err := files.ReadFile(ctx, full)
			if err != nil {
				return fmt.Errorf("conformance canary: read %s: %w", full, err)
			}
			sum := sha256.Sum256(data)
			*out = append(*out, &evalv1.CanaryEntry{Path: entryRel, Sha256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
		default:
			return fmt.Errorf("conformance canary: %s is not a regular file or directory: %w", full, constants.ErrConformanceCanaryInvalid)
		}
	}
	return nil
}

// Diff compares two manifests by path. Changed means the same path with a
// different sha256 or size.
func Diff(before, after *evalv1.CanaryManifest) Delta {
	prior := indexEntries(before)
	next := indexEntries(after)
	var d Delta
	for p, entry := range prior {
		nextEntry, ok := next[p]
		if !ok {
			d.Removed = append(d.Removed, p)
			continue
		}
		if entry.GetSha256() != nextEntry.GetSha256() || entry.GetSize() != nextEntry.GetSize() {
			d.Changed = append(d.Changed, p)
		}
	}
	for p := range next {
		if _, ok := prior[p]; !ok {
			d.Added = append(d.Added, p)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

// Empty reports whether the delta records no change.
func (d Delta) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// ExactlyWrote reports whether the only change from before to after is target
// (added or changed) and its content in after has sha256(content).
func ExactlyWrote(before, after *evalv1.CanaryManifest, target string, content []byte) bool {
	d := Diff(before, after)
	if len(d.Removed) != 0 {
		return false
	}
	written := append(slices.Clone(d.Added), d.Changed...)
	if len(written) != 1 || written[0] != target {
		return false
	}
	sum := sha256.Sum256(content)
	for _, entry := range after.GetEntries() {
		if entry.GetPath() == target {
			return entry.GetSha256() == hex.EncodeToString(sum[:]) && entry.GetSize() == int64(len(content))
		}
	}
	return false
}

// indexEntries maps each manifest entry by its path.
func indexEntries(m *evalv1.CanaryManifest) map[string]*evalv1.CanaryEntry {
	index := make(map[string]*evalv1.CanaryEntry, len(m.GetEntries()))
	for _, entry := range m.GetEntries() {
		index[entry.GetPath()] = entry
	}
	return index
}

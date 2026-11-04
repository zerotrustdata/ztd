// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package storage

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// Ledger commits are built directly from git objects: a blob, the trees along its path, and a
// commit whose parent is HEAD. Nothing is staged and no worktree file is written, so a commit costs
// the objects it adds, never a scan of the snapshots already committed. Trees not on the path are
// reused by hash, so a commit from before the sharded layout keeps its files/ subtree unchanged.

const (
	ledgerAuthorName  = "ztd-operator"
	ledgerAuthorEmail = "ztd-operator@system"
)

// ledgerSignature is the author and committer of every ledger commit.
func ledgerSignature() *object.Signature {
	return &object.Signature{Name: ledgerAuthorName, Email: ledgerAuthorEmail, When: time.Now()}
}

// commitBlob commits content at treePath on top of head and moves head's branch to the new commit.
// The ref moves by compare-and-swap from head, so a commit never lands on a ref another writer
// moved.
func commitBlob(repo *git.Repository, head *plumbing.Reference, treePath string, content []byte, message string) (plumbing.Hash, error) {
	st := repo.Storer
	parent, err := object.GetCommit(st, head.Hash())
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("read head commit: %w", err)
	}
	blobHash, err := storeBlob(st, content)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	treeHash, err := upsertTreeEntry(st, parent.TreeHash, strings.Split(treePath, "/"), blobHash)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	signature := ledgerSignature()
	commit := &object.Commit{
		Author:       *signature,
		Committer:    *signature,
		Message:      fmt.Sprintf("[%s] %s", signature.When.Format(time.RFC3339), message),
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{head.Hash()},
	}
	obj := st.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit: %w", err)
	}
	commitHash, err := st.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store commit: %w", err)
	}

	if err := st.CheckAndSetReference(plumbing.NewHashReference(head.Name(), commitHash), head); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("move %s: %w", head.Name(), err)
	}
	return commitHash, nil
}

// storeBlob stores content as a blob object.
func storeBlob(st storer.EncodedObjectStorer, content []byte) (plumbing.Hash, error) {
	obj := st.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("open blob: %w", err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, fmt.Errorf("write blob: %w", err)
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("close blob: %w", err)
	}
	hash, err := st.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store blob: %w", err)
	}
	return hash, nil
}

// upsertTreeEntry stores a copy of the tree at treeHash with blobHash at path, creating any
// directory along path, and returns the new tree's hash. A zero treeHash is an empty tree. Only
// the trees along path are rewritten; every other entry keeps its hash.
func upsertTreeEntry(st storer.EncodedObjectStorer, treeHash plumbing.Hash, path []string, blobHash plumbing.Hash) (plumbing.Hash, error) {
	if len(path) == 0 || path[0] == "" {
		return plumbing.ZeroHash, fmt.Errorf("tree path: %w", constants.ErrInternal)
	}
	var entries []object.TreeEntry
	if !treeHash.IsZero() {
		tree, err := object.GetTree(st, treeHash)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("read tree: %w", err)
		}
		entries = append(entries, tree.Entries...)
	}

	name := path[0]
	index := -1
	for i := range entries {
		if entries[i].Name == name {
			index = i
			break
		}
	}

	entry := object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: blobHash}
	if len(path) > 1 {
		child := plumbing.ZeroHash
		if index >= 0 {
			if entries[index].Mode != filemode.Dir {
				return plumbing.ZeroHash, fmt.Errorf("tree path %q is not a directory: %w", name, constants.ErrInternal)
			}
			child = entries[index].Hash
		}
		childHash, err := upsertTreeEntry(st, child, path[1:], blobHash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entry = object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: childHash}
	} else if index >= 0 && entries[index].Mode == filemode.Dir {
		return plumbing.ZeroHash, fmt.Errorf("tree path %q is a directory: %w", name, constants.ErrInternal)
	}

	if index >= 0 {
		entries[index] = entry
	} else {
		entries = append(entries, entry)
	}
	sortTreeEntries(entries)

	tree := &object.Tree{Entries: entries}
	obj := st.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode tree: %w", err)
	}
	hash, err := st.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store tree: %w", err)
	}
	return hash, nil
}

// sortTreeEntries orders tree entries as git does: by name, with a directory compared as its name
// followed by "/".
func sortTreeEntries(entries []object.TreeEntry) {
	key := func(e object.TreeEntry) string {
		if e.Mode == filemode.Dir {
			return e.Name + "/"
		}
		return e.Name
	}
	sort.Slice(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
}

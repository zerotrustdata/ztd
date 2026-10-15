// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/security"
)

// FileEditTarget is a governed FILE_EDIT destination that has passed the
// runtime path policy. Every operation goes through an os.Root opened on the
// validated directory, so a symlink substituted after validation cannot move
// the operation outside that directory. Evaluation targets are rooted at the
// executing runtime's controlled target directory; host files are rooted at
// their deepest existing ancestor directory.
type FileEditTarget struct {
	path string // validated absolute path as requested, reported to callers
	dir  string // resolved absolute directory backing root
	name string // target path relative to root
	root *os.Root
}

// Path returns the validated absolute path as requested.
func (t *FileEditTarget) Path() string { return t.path }

// Stat returns file information, following a final symlink within the root.
func (t *FileEditTarget) Stat() (os.FileInfo, error) { return t.root.Stat(t.name) }

// Lstat returns file information without following a final symlink.
func (t *FileEditTarget) Lstat() (os.FileInfo, error) { return t.root.Lstat(t.name) }

// Readlink returns the destination of a final symlink.
func (t *FileEditTarget) Readlink() (string, error) { return t.root.Readlink(t.name) }

// Open opens the target for reading. The caller must Close the handle.
func (t *FileEditTarget) Open() (*os.File, error) { return t.root.Open(t.name) }

// ReadFile reads the whole target.
func (t *FileEditTarget) ReadFile() ([]byte, error) { return t.root.ReadFile(t.name) }

// WriteFile creates missing parent directories and replaces the target's content.
func (t *FileEditTarget) WriteFile(data []byte, mode os.FileMode) error {
	if parent := filepath.Dir(t.name); parent != "." {
		if err := t.root.MkdirAll(parent, constants.PermDirStandard); err != nil {
			return fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
		}
	}
	return t.root.WriteFile(t.name, data, mode)
}

// CreateSibling creates or truncates the file named by the target path plus
// suffix, beneath the same root. It returns the handle and its absolute path;
// the caller must Close the handle.
func (t *FileEditTarget) CreateSibling(suffix string, mode os.FileMode) (*os.File, string, error) {
	name := t.name + suffix
	file, err := t.root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return nil, "", err
	}
	return file, filepath.Join(t.dir, name), nil
}

// Close releases the directory handle.
func (t *FileEditTarget) Close() error { return t.root.Close() }

func (fs *localFS) OpenFileEditTarget(ctx context.Context, path, workDir string) (*FileEditTarget, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if security.IsProtectedRuntimePath(path) {
		return nil, fmt.Errorf("%w: %s", constants.ErrFileEditProtectedRuntimePath, path)
	}
	absPath, err := security.ValidatePath(path, workDir)
	if err != nil {
		return nil, fmt.Errorf("file edit: validate path: %w", err)
	}
	resolvedPath, err := ResolveExistingPath(absPath)
	if err != nil {
		return nil, fmt.Errorf("file edit: resolve symlink destination: %w", err)
	}
	if security.IsEvaluationControlledTargetPath(path) || security.IsEvaluationControlledTargetPath(resolvedPath) {
		return fs.openEvaluationTarget(path, absPath, resolvedPath)
	}
	if security.IsProtectedRuntimePath(resolvedPath) {
		return nil, fmt.Errorf("%w: symlink destination %s", constants.ErrFileEditProtectedRuntimePath, resolvedPath)
	}

	dir, err := deepestExistingDir(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("file edit: open target directory: %w", err)
	}
	return openFileEditTarget(absPath, dir, resolvedPath)
}

// openEvaluationTarget admits a path only when it resolves strictly inside
// the executing runtime's controlled evaluation target directory. That
// directory is created by the evaluation command before a run; it is never
// created here.
func (fs *localFS) openEvaluationTarget(path, absPath, resolvedPath string) (*FileEditTarget, error) {
	targetDir, err := ResolveExistingPath(fs.Resolve(constants.EvaluationControlledTargetDirname))
	if err != nil {
		return nil, fmt.Errorf("file edit: resolve controlled evaluation target directory: %w", err)
	}
	if resolvedPath == targetDir || !withinPath(targetDir, resolvedPath) {
		return nil, fmt.Errorf("%w: %s resolves outside the executing runtime's controlled evaluation target directory", constants.ErrFileEditProtectedRuntimePath, path)
	}
	if _, err := os.Stat(targetDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: controlled evaluation target directory %s", constants.ErrPathNotFound, targetDir)
		}
		return nil, fmt.Errorf("file edit: stat controlled evaluation target directory: %w", err)
	}
	return openFileEditTarget(absPath, targetDir, resolvedPath)
}

func openFileEditTarget(absPath, dir, resolvedPath string) (*FileEditTarget, error) {
	name, err := filepath.Rel(dir, resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("file edit: relative target path: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("file edit: open target directory: %w", err)
	}
	return &FileEditTarget{path: absPath, dir: dir, name: name, root: root}, nil
}

// deepestExistingDir returns the nearest existing ancestor directory of path.
// A non-directory ancestor is skipped so the operation itself, not path
// admission, reports that the target cannot be created beneath it.
func deepestExistingDir(path string) (string, error) {
	dir := filepath.Dir(path)
	for {
		info, err := os.Lstat(dir)
		if err == nil && info.IsDir() {
			return dir, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("unable to find existing ancestor for %s", path)
		}
		dir = parent
	}
}

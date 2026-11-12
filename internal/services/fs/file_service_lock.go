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
	"io"
	"os"
	"path/filepath"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// TryLock creates relPath if needed and takes a non-blocking exclusive lock
// on it. It returns constants.ErrRuntimeFileLocked when another holder has
// the lock. Closing the returned handle releases it; the kernel releases it
// if the process exits, so a lock file left on disk never blocks a restart.
func (fs *localFS) TryLock(ctx context.Context, relPath string, mode os.FileMode) (io.Closer, error) {
	return fs.tryLock(ctx, relPath, mode, true)
}

// TryLockShared is TryLock for a shared lock, which conflicts only with an
// exclusive holder.
func (fs *localFS) TryLockShared(ctx context.Context, relPath string, mode os.FileMode) (io.Closer, error) {
	return fs.tryLock(ctx, relPath, mode, false)
}

func (fs *localFS) tryLock(ctx context.Context, relPath string, mode os.FileMode, exclusive bool) (io.Closer, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if _, err := fs.resolveValidated(relPath); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(fs.runtimeDir, constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}
	root, err := os.OpenRoot(fs.runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFileOpenFailed, err)
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(relPath), constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}
	if info, err := root.Lstat(relPath); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: lock must be a regular file: %s", constants.ErrPathValidation, relPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", constants.ErrFileOpenFailed, err)
	}
	// Root.OpenFile keeps a concurrent symlink swap confined to this runtime.
	f, err := root.OpenFile(relPath, os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFileOpenFailed, err)
	}
	if err := lockFile(f, exclusive); err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", relPath, err), f.Close())
	}
	return f, nil
}

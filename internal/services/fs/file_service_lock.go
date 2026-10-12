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
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	absPath := fs.Resolve(relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), constants.PermDirPrivate); err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrDirCreateFailed, err)
	}
	f, err := os.OpenFile(absPath, os.O_CREATE|os.O_RDWR, mode)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", constants.ErrFileOpenFailed, err)
	}
	if err := lockFile(f); err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", absPath, err), f.Close())
	}
	return f, nil
}

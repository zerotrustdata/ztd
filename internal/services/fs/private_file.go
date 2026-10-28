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
	iofs "io/fs"
	"os"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// CreateOrRestrictFile creates an empty runtime file if absent or restricts
// the permissions of an existing file. It does not create parent directories.
func (service *localFS) CreateOrRestrictFile(ctx context.Context, relPath string, mode os.FileMode) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	root, err := service.openRuntimeRoot()
	if err != nil {
		return err
	}
	defer root.Close()

	exists, err := inspectPrivateFile(root, relPath)
	if err != nil {
		return err
	}
	if exists {
		if err := root.Chmod(relPath, mode); err != nil {
			return fmt.Errorf("%w: %s: %w", constants.ErrEnforcePermissions, relPath, err)
		}
		return nil
	}
	file, err := root.OpenFile(relPath, os.O_WRONLY|os.O_CREATE, mode)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", constants.ErrFileOpenFailed, relPath, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: %s: %w", constants.ErrFileWriteFailed, relPath, err)
	}
	if err := root.Chmod(relPath, mode); err != nil {
		return fmt.Errorf("%w: %s: %w", constants.ErrEnforcePermissions, relPath, err)
	}
	return nil
}

// RestrictFileIfExists changes the permissions of an existing runtime file.
// It returns false without error when the file is absent.
func (service *localFS) RestrictFileIfExists(ctx context.Context, relPath string, mode os.FileMode) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	root, err := service.openRuntimeRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()

	exists, err := inspectPrivateFile(root, relPath)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if err := root.Chmod(relPath, mode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s: %w", constants.ErrEnforcePermissions, relPath, err)
	}
	return true, nil
}

func (service *localFS) openRuntimeRoot() (*os.Root, error) {
	root, err := os.OpenRoot(service.runtimeDir)
	if err != nil {
		return nil, fmt.Errorf("%w: runtime root %s: %w", constants.ErrEnforcePermissions, service.runtimeDir, err)
	}
	return root, nil
}

func inspectPrivateFile(root *os.Root, relPath string) (bool, error) {
	info, err := root.Lstat(relPath)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("%w: %s: %w", constants.ErrStatFailed, relPath, err)
	}
	if !info.Mode().IsRegular() {
		return true, fmt.Errorf("%w: not a regular runtime file: %s", constants.ErrPathValidation, relPath)
	}
	return true, nil
}

func checkContext(ctx context.Context) error {
	if ctx != nil {
		return ctx.Err()
	}
	return nil
}

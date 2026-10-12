// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// memoryDatabasePath is SQLite's private in-memory database name. It has no
// file to protect.
const memoryDatabasePath = ":memory:"

// sidecarSuffixes are the files SQLite derives from the main database file
// name. The unix VFS creates them with the main database file's permissions,
// so a private database file yields private -wal and -shm files.
var sidecarSuffixes = []string{"-wal", "-shm"}

// securePrivateDatabaseFiles makes the database file private (0600) before
// the driver opens it. A missing file is created empty, which SQLite treats
// as a new database, so the file never exists with wider permissions. A file
// that already exists, and any WAL or shared-memory sidecar left by an earlier
// process, is tightened to 0600.
func securePrivateDatabaseFiles(path string) error {
	if path == "" || strings.HasPrefix(path, memoryDatabasePath) {
		return nil
	}

	created, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, constants.PermFilePrivate)
	switch {
	case err == nil:
		if err := created.Close(); err != nil {
			return fmt.Errorf("sqliteutil: create database file %s: %w", path, err)
		}
	case errors.Is(err, os.ErrExist):
	default:
		return fmt.Errorf("sqliteutil: create database file %s: %w", path, err)
	}

	for _, candidate := range append([]string{path}, sidecarPaths(path)...) {
		if err := os.Chmod(candidate, constants.PermFilePrivate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("sqliteutil: restrict database file %s: %w", candidate, err)
		}
	}
	return nil
}

func sidecarPaths(path string) []string {
	paths := make([]string, 0, len(sidecarSuffixes))
	for _, suffix := range sidecarSuffixes {
		paths = append(paths, path+suffix)
	}
	return paths
}

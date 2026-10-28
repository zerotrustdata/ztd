// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package sqliteutil

import (
	"context"
	"fmt"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
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
func securePrivateDatabaseFiles(fileSvc fs.RuntimeFileService, path string) error {
	if isMemoryDatabasePath(path) {
		return nil
	}
	if fileSvc == nil {
		return fmt.Errorf("sqliteutil: runtime file service is required for file database %s: %w", path, constants.ErrMissingRequiredField)
	}

	relPath, err := fileSvc.Rel(path)
	if err != nil {
		return fmt.Errorf("sqliteutil: database path is outside runtime files: %w", err)
	}

	if err := fileSvc.CreateOrRestrictFile(context.Background(), relPath, constants.PermFilePrivate); err != nil {
		return fmt.Errorf("sqliteutil: restrict database file %s: %w", path, err)
	}

	for _, candidate := range sidecarPaths(relPath) {
		candidatePath, err := fileSvc.Rel(fileSvc.Resolve(candidate))
		if err != nil {
			return fmt.Errorf("sqliteutil: database sidecar path is outside runtime files: %w", err)
		}
		_, err = fileSvc.RestrictFileIfExists(context.Background(), candidatePath, constants.PermFilePrivate)
		if err != nil {
			return fmt.Errorf("sqliteutil: restrict database sidecar %s: %w", fileSvc.Resolve(candidatePath), err)
		}
	}
	return nil
}

func isMemoryDatabasePath(path string) bool {
	return path == memoryDatabasePath
}

func sidecarPaths(path string) []string {
	paths := make([]string, 0, len(sidecarSuffixes))
	for _, suffix := range sidecarSuffixes {
		paths = append(paths, path+suffix)
	}
	return paths
}

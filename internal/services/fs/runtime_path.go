// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package fs

import (
	"fmt"
	"path/filepath"
)

// RuntimeRelativePath classifies a user-supplied filesystem path, relative to
// the working directory or absolute. Runtime paths are validated by fileSvc.Rel;
// their validation errors must not trigger a fallback to external file I/O.
// External paths return an empty relative path and false.
func RuntimeRelativePath(fileSvc RuntimeFileService, path string) (rel string, inRuntime bool, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve file path: %w", err)
	}
	root, err := filepath.Abs(fileSvc.Resolve(""))
	if err != nil {
		return "", false, fmt.Errorf("resolve runtime root: %w", err)
	}
	if !withinPath(root, absPath) {
		return "", false, nil
	}
	rel, err = fileSvc.Rel(absPath)
	if err != nil {
		return "", true, fmt.Errorf("resolve runtime-relative path: %w", err)
	}
	return rel, true, nil
}

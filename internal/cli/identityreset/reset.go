// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package identityreset removes only an outbound Operator runtime's installed
// enrollment credentials and cached trust.
package identityreset

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	runtimepaths "github.com/zerotrustdata/ztd/internal/constants"
)

// identityFiles are the runtime-relative files an outbound Operator runtime
// holds for its enrollment: the CLI session credentials, the CLI mTLS pair, and
// the cached Gateway trust bundle. The Operator's workload identity itself is
// memory-only and never written.
var identityFiles = []string{
	runtimepaths.CredentialsFilename,
	runtimepaths.CliCertFilename,
	runtimepaths.CliKeyFilename,
	path.Join(runtimepaths.PkiDirname, runtimepaths.PkiSubdirTrust, runtimepaths.PkiFileGatewayBundle),
}

// Reset uses a confined filesystem handle and refuses symlinked identity paths.
// Databases, vault keys, configuration, logs, and other applications are retained.
// Callers must stop the Operator before invoking Reset.
func Reset(directory string) error {
	runtime := filepath.Join(directory, runtimepaths.RuntimeDirname)
	info, err := os.Lstat(runtime)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing non-directory or symlink runtime: %s", runtime)
	}
	root, err := os.OpenRoot(runtime)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Stat(runtimepaths.GatewayRuntimeMarker); err == nil {
		return fmt.Errorf("refusing to reset a gateway runtime: %s", runtime)
	} else if !os.IsNotExist(err) {
		return err
	}
	// Validate the entire selection before deleting anything.
	for _, name := range identityFiles {
		parts := strings.Split(name, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			info, err := root.Lstat(prefix)
			if os.IsNotExist(err) {
				break
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing symlink identity path: %s", prefix)
			}
		}
	}
	for _, name := range identityFiles {
		if err := root.RemoveAll(name); err != nil {
			return fmt.Errorf("reset identity %s: %w", name, err)
		}
	}
	return nil
}

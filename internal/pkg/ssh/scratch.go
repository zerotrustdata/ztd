// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package ssh

import (
	"fmt"
	"path"

	"github.com/pkg/sftp"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// MkdirRemoteScratch creates the remote runtime scratch directory
// (.ztd/tmp under the SSH login directory) with private permissions and
// returns its absolute path. Remote staging never uses the remote OS temp dir.
func MkdirRemoteScratch(c *sftp.Client) (string, error) {
	wd, err := c.Getwd()
	if err != nil {
		return "", fmt.Errorf("ssh: remote scratch: resolve login dir: %w", err)
	}
	runtimeDir := path.Join(wd, constants.RuntimeDirname)
	dir := path.Join(runtimeDir, constants.TmpDirname)
	if err := c.MkdirAll(dir); err != nil {
		return "", fmt.Errorf("ssh: remote scratch: create %s: %w", dir, err)
	}
	for _, p := range []string{runtimeDir, dir} {
		if err := c.Chmod(p, constants.PermDirPrivate); err != nil {
			return "", fmt.Errorf("ssh: remote scratch: chmod %s: %w", p, err)
		}
	}
	return dir, nil
}

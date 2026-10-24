// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build windows

package gw

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func startZombieReaper(t *testing.T) {
	t.Helper()
	// No-op on Windows: Windows does not have zombie processes or SIGCHLD.
}

func reapRemainingChild(t *testing.T, fileSvc fs.RuntimeFileService) {
	t.Helper()
	pm, err := platform.NewProcessManager(fileSvc)
	if err != nil {
		return
	}
	// StopOperator waits for the child to exit and release the
	// .ztd/bin/ztd.exe image; a bare Kill returns immediately and
	// leaves the binary locked, so TempDir removal fails on Windows
	// with "Access is denied".
	_ = pm.StopOperator()
}

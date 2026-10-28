// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

func withServeReExec(t *testing.T, fileSvc fs.RuntimeFileService) {
	t.Helper()
	t.Setenv(string(constants.EnvVar.TestReexec), "serve")
	t.Cleanup(func() {
		reapRemainingChild(t, fileSvc)
	})
}

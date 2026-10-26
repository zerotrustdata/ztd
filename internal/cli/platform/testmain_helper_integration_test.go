// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package platform

import (
	"os"
	"testing"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest/reexec"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest/reexec/serve"
)

func TestMain(m *testing.M) {
	serve.IfRequested()
	reexec.IfRequested()
	os.Exit(m.Run())
}

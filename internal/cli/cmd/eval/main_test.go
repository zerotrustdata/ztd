// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build !integration

package eval

import (
	"fmt"
	"os"
	"testing"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestMain runs the package from the module root so the checked-in
// eval/formation-catalog.json resolves exactly as it does for the CLI.
func TestMain(m *testing.M) {
	if err := testutil.ChdirModuleRoot(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package eval

import (
	"fmt"
	"os"
	"testing"

	operatorcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/operator"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestMain serves the embedded model client. Real-stack Operators run in this
// test process and self-invoke their own executable for governed
// `ztd operator model <verb>` commands, so this binary must answer them with
// the candidate's model client instead of running the tests again.
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "operator" && os.Args[2] == "model" {
		root := operatorcmd.Cmd()
		root.SetArgs(os.Args[2:])
		if err := root.Execute(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if err := testutil.ChdirModuleRoot(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

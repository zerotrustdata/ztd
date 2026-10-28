// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package reexec contains the dependency-light process behavior shared by
// test binaries when ProcessManager re-executes them as Gateway children.
package reexec

import (
	"fmt"
	"os"
	"time"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// IfRequested handles the Gateway-child invocation used by process lifecycle
// tests. Test binaries receive "gw" as their first argument when
// ProcessManager re-executes them in place of a Gateway executable.
func IfRequested() {
	if len(os.Args) <= 1 || os.Args[1] != "gw" {
		return
	}

	switch os.Getenv(string(constants.EnvVar.TestReexec)) {
	case "hold":
		time.Sleep(5 * time.Second)
	case "verification":
		_, _ = fmt.Fprintln(os.Stderr, "public mirror verification stage=snapshots batches=42")
		time.Sleep(30 * time.Second)
	case "error":
		_, _ = fmt.Fprintln(os.Stderr, "gateway child startup diagnostic")
		os.Exit(1)
	case "keystore-approval":
		_, _ = fmt.Fprintln(os.Stderr, constants.KeyStoreAwaitingApprovalLog)
		time.Sleep(20 * time.Second)
		_, _ = fmt.Fprintln(os.Stderr, "key store approval denied")
		os.Exit(1)
	}
	os.Exit(0)
}

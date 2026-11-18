//go:build !windows

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package operatordeploy

import (
	"os/exec"
	"syscall"
)

// DetachDeployedOperator detaches a launched Operator process into its own session.
func DetachDeployedOperator(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

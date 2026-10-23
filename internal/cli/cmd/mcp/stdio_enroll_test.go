// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// TestMCPStdio_DoesNotInvokeEnrollment verifies that the `mcp stdio` command
// path does NOT enroll. Direct `mcp stdio` is a credential consumer only — it
// loads credentials and builds an mTLS connection, never enrolling, opening a
// browser, or installing system trust. This is the §11.5 3.8 negative
// assertion.
//
// With the enrollerFactory injection model, `mcpStdioCmdWithConfig` does not
// receive an enrollerFactory at all — the absence is enforced by the function
// signature. This test runs the command and asserts it fails (no gateway, no
// credentials) without any enrollment side effect. If a future change adds an
// enrollerFactory parameter to `mcpStdioCmdWithConfig`, this test should be
// updated to inject a panicking factory and assert it is never called.
func TestMCPStdio_DoesNotInvokeEnrollment(t *testing.T) { //nolint:paralleltest // mutates package-level state

	fileSvc, _ := cmdtest.NewCmdTestEnv(t)

	cmd := McpStdioCmdWithConfig(cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetContext(testutil.Context(t))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	require.NotErrorIs(t, err, constants.ErrEnrollmentFailed)
	require.NotContains(t, strings.ToLower(err.Error()), "enroll")
}

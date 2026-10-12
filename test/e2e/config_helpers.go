// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
)

// resolveRepoRoot finds the repository root using go list -m, matching the
// pattern used by the integration test helpers.
func resolveRepoRoot() (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go list -m: %w", err)
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", fmt.Errorf("go list -m returned empty directory")
	}
	return filepath.Clean(root), nil
}

// e2eRuntimeRootEnv names the directory whose .ztd/ tree the suite targets
// instead of the repository root. Scenarios that run their own isolated
// Gateway (ztd test scale) set it so the suite never reads
// the developer's own runtime.
const e2eRuntimeRootEnv = "ZTD_E2E_RUNTIME_ROOT"

// resolveRuntimeRoot returns the directory holding the .ztd/ tree under test:
// ZTD_E2E_RUNTIME_ROOT when set, otherwise the repository root.
func resolveRuntimeRoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv(e2eRuntimeRootEnv)); root != "" {
		return filepath.Clean(root), nil
	}
	return resolveRepoRoot()
}

// validateCredentials checks that loaded owner credentials are present and
// contain the required CLI session ID. Returns a descriptive error for nil
// credentials or a missing session ID so TestMain fails closed with an
// actionable message rather than proceeding to authenticated requests that
// would fail with a less obvious 401.
func validateCredentials(creds *auth.Credentials) error {
	if creds == nil {
		return fmt.Errorf("no owner credentials found — run './ztd auth login' first")
	}
	if creds.CLISessionID == "" {
		return fmt.Errorf("credentials missing cli_session_id")
	}
	if creds.UserID == "" {
		return fmt.Errorf("credentials missing user_id")
	}
	if creds.OperatorID == "" {
		return fmt.Errorf("credentials missing operator_id")
	}
	if creds.OperatorSessionID == "" {
		return fmt.Errorf("credentials missing operator_session_id")
	}
	return nil
}

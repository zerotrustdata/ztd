// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration || e2e

package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/api"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// NewLiveOperatorHTTPClient creates an API client configured for mTLS
// against a running ztd platform using the platform's api.NewClient.
//
// This helper requires the platform to be running via `./ztd platform start`
// and authenticated via `./ztd auth login`.
//
// Parameters:
//   - t: testing.T for assertions
//   - repoRoot: path to the repository root (typically derived from test directory)
//
// Returns:
//   - *api.Client: configured API client with mTLS and CA verification
//   - *config.Config: loaded CLI configuration (for ports and paths)
func NewLiveOperatorHTTPClient(t require.TestingT, repoRoot string) (*api.Client, *config.Config) {
	// Load CLI config to get ports and paths
	cliCfg, err := config.Load(repoRoot)
	require.NoError(t, err, "failed to load CLI config")

	// Construct fileSvc for credential loading
	fileSvc, err := fs.NewRuntimeFileService(repoRoot, testutil.NewTestLogger())
	require.NoError(t, err, "failed to create file service")

	// Use platform's api.NewClient with 5-second timeout
	client, err := api.NewClient(fileSvc, cliCfg)
	require.NoError(t, err, "failed to create API client - run './ztd auth login' first")

	return client, cliCfg
}

// ResolveRepoRootFromTestDir finds the repository root using go list -m.
// This is more robust than directory navigation and works regardless of
// the current working directory.
func ResolveRepoRootFromTestDir(t require.TestingT) string {
	// Use go list -m to find the module directory
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}")
	output, err := cmd.Output()
	require.NoError(t, err, "failed to run go list -m to find repository root")

	repoRoot := string(output)
	repoRoot = filepath.Clean(strings.TrimSpace(repoRoot))
	require.NotEmpty(t, repoRoot, "go list -m returned empty directory")

	return repoRoot
}

// RunCLICommand executes ./ztd commands with proper error handling and output capture.
// It ensures the ztd binary is built, sets the working directory to the repo root,
// and returns the combined stdout/stderr output along with any error.
//
// Parameters:
//   - t: testing.T for assertions
//   - repoRoot: path to the repository root
//   - args: command arguments (e.g., "gw", "status")
//
// Returns:
//   - string: combined stdout/stderr output
//   - error: nil if command succeeded, otherwise the execution error
func RunCLICommand(t *testing.T, repoRoot string, args ...string) (string, error) {
	t.Helper()

	ztdPath := filepath.Join(repoRoot, "ztd")
	if _, err := os.Stat(ztdPath); os.IsNotExist(err) {
		// Build the binary if it doesn't exist
		buildCmd := exec.Command("go", "build", "-o", ztdPath, "./cmd/ztd")
		buildCmd.Dir = repoRoot
		if output, err := buildCmd.CombinedOutput(); err != nil {
			require.NoError(t, err, "failed to build ztd binary: %s", string(output))
		}
	}

	cmd := exec.Command(ztdPath, args...)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// RunCLICommandRequire executes a CLI command and requires it to succeed.
// If the command fails, it calls t.Fatalf with the output.
//
// This is a convenience wrapper around RunCLICommand for cases where
// the command must succeed for the test to continue.
func RunCLICommandRequire(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()

	output, err := RunCLICommand(t, repoRoot, args...)
	if err != nil {
		t.Fatalf("CLI command '%v' failed: %v\nOutput: %s", args, err, output)
	}
	return output
}

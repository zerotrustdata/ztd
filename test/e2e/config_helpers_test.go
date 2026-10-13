// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package e2e

// This file contains Tier 1 unit tests for the non-e2e-tagged config helpers.
// The os.Chdir usage in TestResolveRepoRoot_error_outside_any_module is
// legitimate source-tree discovery: it changes cwd to a temp directory with no
// go.mod to exercise the `go list -m` error path. It does not align .ztd/
// runtime state — the temp dir has no .ztd/ tree and no RuntimeFileService is
// constructed.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestResolveRepoRoot(t *testing.T) {
	t.Run("success in module directory", func(t *testing.T) {
		// resolveRepoRoot runs `go list -m` from the process working directory.
		// The test process runs inside the ztd module, so it must succeed and
		// return a clean, non-empty path.
		root, err := resolveRepoRoot()
		require.NoError(t, err)
		assert.NotEmpty(t, root)
		assert.Equal(t, filepath.Clean(root), root)
	})

	t.Run("error outside any module", func(t *testing.T) {
		// Run resolveRepoRoot from a temp directory with no go.mod to exercise
		// the error-wrapping path deterministically. resolveRepoRoot shells out
		// to `go list -m`, which fails outside a module. t.TempDir() creates a
		// directory under the system temp root, which has no go.mod ancestor.
		tmpDir := t.TempDir()

		// resolveRepoRoot uses the process cwd, so change into the temp dir.
		origDir, err := os.Getwd()
		require.NoError(t, err)
		require.NoError(t, os.Chdir(tmpDir))
		t.Cleanup(func() { _ = os.Chdir(origDir) })

		_, err = resolveRepoRoot()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "go list -m")
	})
}

func TestResolveRuntimeRoot(t *testing.T) {
	t.Run("env override wins and is cleaned", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.E2ERuntimeRoot), "/tmp/isolated/../isolated-root/")
		root, err := resolveRuntimeRoot()
		require.NoError(t, err)
		assert.Equal(t, filepath.Clean("/tmp/isolated-root"), root)
	})

	t.Run("falls back to the repository root", func(t *testing.T) {
		t.Setenv(string(constants.EnvVar.E2ERuntimeRoot), "")
		want, err := resolveRepoRoot()
		require.NoError(t, err)
		got, err := resolveRuntimeRoot()
		require.NoError(t, err)
		assert.Equal(t, want, got)
	})
}

// TestValidateCredentials covers the partial-credential error paths extracted
// from loadE2EConfig. These run as Tier 1 unit tests with no file system or
// platform dependency.
func TestValidateCredentials(t *testing.T) {
	tests := []struct {
		name          string
		creds         *auth.Credentials
		expectError   bool
		errorContains string
	}{
		{
			name:          "nil credentials",
			creds:         nil,
			expectError:   true,
			errorContains: "no owner credentials found",
		},
		{
			name: "missing cli_session_id",
			creds: &auth.Credentials{
				OperatorSessionID: "op-session-123",
				UserID:            "user-456",
				OperatorID:        "op-789",
				CLISessionID:      "",
			},
			expectError:   true,
			errorContains: "missing cli_session_id",
		},
		{
			name: "valid credentials with cli_session_id",
			creds: &auth.Credentials{
				OperatorSessionID: "op-session-123",
				UserID:            "user-456",
				OperatorID:        "op-789",
				CLISessionID:      "cli-session-abc",
			},
			expectError: false,
		},
		{
			name: "missing operator_session_id",
			creds: &auth.Credentials{
				CLISessionID: "cli-session-xyz",
				UserID:       "user-456",
				OperatorID:   "op-789",
			},
			expectError:   true,
			errorContains: "missing operator_session_id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCredentials(tt.creds)
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

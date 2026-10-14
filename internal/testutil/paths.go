// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// TestPaths holds test-specific filesystem paths.
// This allows tests to have isolated path structures without mutating
// the global constants.Paths package-level state, which causes race
// conditions in parallel test execution.
type TestPaths struct {
	BaseDir       string
	RuntimeDir    string
	DataDir       string
	PKIDir        string
	SecretsDir    string
	VaultDir      string
	TestVaultDir  string
	SshConfigPath string
	DbPath        string
}

// NewTestPaths creates a TestPaths instance with all paths calculated
// relative to the provided base directory. Does NOT mutate global state.
func NewTestPaths(baseDir string) *TestPaths {
	runtimeDir := filepath.Join(baseDir, constants.RuntimeDirname)
	dataDir := filepath.Join(runtimeDir, constants.DataDirname)
	pkiDir := filepath.Join(runtimeDir, constants.PkiDirname)
	secretsDir := filepath.Join(runtimeDir, constants.SecretsDirname)
	vaultDir := filepath.Join(runtimeDir, constants.VaultDirname)
	testVaultDir := filepath.Join(runtimeDir, constants.TestVaultDirname)

	return &TestPaths{
		BaseDir:       baseDir,
		RuntimeDir:    runtimeDir,
		DataDir:       dataDir,
		PKIDir:        pkiDir,
		SecretsDir:    secretsDir,
		VaultDir:      vaultDir,
		TestVaultDir:  testVaultDir,
		SshConfigPath: filepath.Join(runtimeDir, constants.SshConfigFilename),
		DbPath:        filepath.Join(dataDir, constants.DbFilename),
	}
}

// runtimeRoot is <module root>/.ztd, resolved once at package init: go test
// starts every test binary in its package directory, before any test can
// t.Chdir elsewhere.
var runtimeRoot, runtimeRootErr = resolveRuntimeRoot()

func resolveRuntimeRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("testutil: resolve working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, constants.RuntimeDirname), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("testutil: no go.mod above the test working directory")
		}
		dir = parent
	}
}

// TempDir returns an absolute, test-owned directory under the module's
// runtime scratch root (.ztd/tmp, INV-FS-11), never the OS temp dir. The name
// is short so Unix socket paths beneath it stay under the OS length limit.
// The directory is removed when the test ends, independent of CWD.
func TempDir(t testing.TB) string {
	t.Helper()
	return runtimeTestDir(t, constants.TmpDirname, "t")
}

// OperatorDir returns a test-owned throwaway Operator working directory under
// the module's .ztd/operators (INV-FS-11). Use it where the code under test
// treats the directory as an Operator root that agents may edit: everything
// under .ztd/tmp is protected runtime state (security.IsProtectedRuntimePath).
func OperatorDir(t testing.TB) string {
	t.Helper()
	return runtimeTestDir(t, constants.OperatorsDirname, "test-")
}

func runtimeTestDir(t testing.TB, subdir, prefix string) string {
	t.Helper()
	if runtimeRootErr != nil {
		t.Fatal(runtimeRootErr)
	}
	parent := filepath.Join(runtimeRoot, subdir)
	if err := os.MkdirAll(parent, constants.PermDirPrivate); err != nil {
		t.Fatalf("testutil: create %s: %v", parent, err)
	}
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		t.Fatalf("testutil: create test dir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("testutil: remove test dir %s: %v", dir, err)
		}
	})
	return dir
}

// NewTestPathsFromTemp creates a TestPaths instance using TempDir(t) as the base.
// This is the recommended way to get isolated test paths.
func NewTestPathsFromTemp(t testing.TB) *TestPaths {
	t.Helper()
	return NewTestPaths(TempDir(t))
}

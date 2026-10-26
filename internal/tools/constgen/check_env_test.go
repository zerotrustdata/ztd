// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/testutil"
)

// repoRootForEnvTest resolves the repository root from this package directory.
func repoRootForEnvTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	return root
}

func TestValidateEnvRegistries_CommittedRegistriesAgree(t *testing.T) {
	require.NoError(t, validateEnvRegistries(repoRootForEnvTest(t)))
}

// envFixtureRegistry is a self-contained env registry with one secret key
// (required in .env.example) and one host key. Tests mutate copies of it so
// each assertion targets a single validation rule.
const envFixtureRegistry = `{
  "env_vars": {
    "ExampleToken": {
      "_go_const": "EnvVar.ExampleToken",
      "value": "ZTD_EXAMPLE_TOKEN",
      "category": "secret",
      "description": "Fixture secret read by the fixture consumer"
    },
    "Home": {
      "_go_const": "EnvVar.Home",
      "value": "HOME",
      "category": "host",
      "description": "Home directory reported by the host"
    }
  }
}
`

// envFixtureExample is a valid .env.example for envFixtureRegistry.
const envFixtureExample = `# Fixture environment template.
#
# Purpose: Fixture secret for the registry validation tests.
# Consumer: fixture consumer.
# Required for default flow: no
# ZTD_EXAMPLE_TOKEN=example-token
`

// newEnvFixture writes the self-contained registry and template into a temp
// root and asserts the unmutated fixture is itself valid.
func newEnvFixture(t *testing.T) string {
	t.Helper()
	root := testutil.TempDir(t)
	for rel, content := range map[string]string{
		relPathEnvVars:    envFixtureRegistry,
		relPathEnvExample: envFixtureExample,
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o600))
	}
	require.NoError(t, validateEnvRegistries(root), "unmutated fixture must validate")
	return root
}

func mutateFixture(t *testing.T, root, rel, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, rel)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), old)
	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o600))
}

func TestValidateEnvRegistries_FailsOnMalformedRegistryEntry(t *testing.T) {
	root := newEnvFixture(t)
	mutateFixture(t, root, relPathEnvVars,
		`"value": "ZTD_EXAMPLE_TOKEN",
      "category": "secret"`, `"value": "ZTD_EXAMPLE_TOKEN",
      "category": "launcher"`)
	err := validateEnvRegistries(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `category "launcher" is not a known env category`)
}

func TestValidateEnvRegistries_FailsWhenRequiredEnvExampleEntryIsMissing(t *testing.T) {
	root := newEnvFixture(t)
	mutateFixture(t, root, relPathEnvExample,
		"# ZTD_EXAMPLE_TOKEN=example-token\n", "")
	err := validateEnvRegistries(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ZTD_EXAMPLE_TOKEN is missing")
}

func TestValidateEnvRegistries_FailsWhenEnvExampleEntryLacksPurpose(t *testing.T) {
	root := newEnvFixture(t)
	mutateFixture(t, root, relPathEnvExample,
		"# Purpose: Fixture secret for the registry validation tests.\n", "")
	err := validateEnvRegistries(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ZTD_EXAMPLE_TOKEN is missing a Purpose line above it")
}

func TestValidateEnvRegistries_FailsOnUnregisteredEnvExampleKey(t *testing.T) {
	root := newEnvFixture(t)
	mutateFixture(t, root, relPathEnvExample,
		"# ZTD_EXAMPLE_TOKEN=example-token\n", "# ZTD_EXAMPLE_TOKEN=example-token\nZTD_NOT_REGISTERED=localhost\n")
	err := validateEnvRegistries(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ZTD_NOT_REGISTERED is not in")
}

func TestValidateEnvRegistries_FailsOnUnregisteredTestEnvExampleKey(t *testing.T) {
	root := newEnvFixture(t)
	mutateFixture(t, root, relPathEnvExample,
		"# ZTD_EXAMPLE_TOKEN=example-token\n", "# ZTD_EXAMPLE_TOKEN=example-token\nZTD_TEST_UNREGISTERED=serve\n")
	err := validateEnvRegistries(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ZTD_TEST_UNREGISTERED is not in")
}

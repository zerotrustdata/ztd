// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmd

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// runEnvFileProbe runs a probe subcommand through the real root command from a
// working directory holding a .env that sets key. The probe declares no launch
// profile so the root pre-run only evaluates its own gates. When refuse is true
// a local --endpoint flag of the wrong type makes the root pre-run fail its
// GetString read, which is a genuine pre-run refusal. It reports whether the
// probe ran and the value it saw.
func runEnvFileProbe(t *testing.T, key constants.EnvVarKey, value string, refuse bool) (string, bool, error) {
	t.Helper()
	dir := testutil.TempDir(t)
	t.Chdir(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, constants.EnvFilename), []byte(string(key)+"="+value+"\n"), constants.PermFilePrivate))
	t.Setenv(string(key), "")
	require.NoError(t, os.Unsetenv(string(key)))

	var seen string
	var ran bool
	probe := shared.DeclareNoLaunchProfile(&cobra.Command{
		Use: "probe",
		RunE: func(cmd *cobra.Command, args []string) error {
			ran = true
			seen = os.Getenv(string(key))
			return nil
		},
	})
	if refuse {
		probe.Flags().Int("endpoint", 0, "shadows the root --endpoint flag so the pre-run read fails")
	}

	rootCmd := NewRootCmd("dev", serve.VersionInfo{})
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	rootCmd.AddCommand(probe)
	rootCmd.SetArgs([]string{"probe"})
	err := rootCmd.Execute()
	return seen, ran, err
}

func TestRootPreRunLoadsDotEnvOnlyAfterGatesPass(t *testing.T) { //nolint:paralleltest // changes the process working directory and environment
	key := constants.EnvVar.OllamaEndpoint
	const value = "http://10.0.0.2:11434"

	t.Run("command that passes pre-run sees the dot-env value in its run", func(t *testing.T) { //nolint:paralleltest // changes the process working directory and environment
		seen, ran, err := runEnvFileProbe(t, key, value, false)
		require.NoError(t, err)
		require.True(t, ran)
		assert.Equal(t, value, seen)
	})

	t.Run("command refused by pre-run never loads the dot-env", func(t *testing.T) { //nolint:paralleltest // changes the process working directory and environment
		_, ran, err := runEnvFileProbe(t, key, value, true)
		require.Error(t, err)
		assert.False(t, ran)
		_, set := os.LookupEnv(string(key))
		assert.False(t, set, "a refused command must not see the .env value")
	})
}

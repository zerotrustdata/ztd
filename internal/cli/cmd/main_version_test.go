// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmd

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestVersionInfoFromCmd_WithVersionInfo(t *testing.T) {
	t.Parallel()
	vi := serve.VersionInfo{
		Version:   "1.2.3",
		BuildID:   "build-abc",
		BuildTime: "2026-07-10",
		Platform:  "linux/amd64",
	}

	cmd := &cobra.Command{}
	cmd.SetContext(shared.ContextWithVersionInfo(context.Background(), vi))

	got := shared.VersionInfoFromCmd(cmd)
	assert.Equal(t, "1.2.3", got.Version)
	assert.Equal(t, "build-abc", got.BuildID)
	assert.Equal(t, "2026-07-10", got.BuildTime)
	assert.Equal(t, "linux/amd64", got.Platform)
}

func TestVersionInfoFromCmd_WithoutVersionInfo(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	got := shared.VersionInfoFromCmd(cmd)
	assert.Equal(t, serve.VersionInfo{}, got)
	assert.Empty(t, got.Version)
	assert.Empty(t, got.BuildID)
}

func TestNewRootCmd_PassportVersionInfoIntoContext(t *testing.T) { //nolint:paralleltest // mutates the process environment
	vi := serve.VersionInfo{
		Version:   "2.0.0",
		BuildID:   "test-build",
		BuildTime: "2026-01-01",
		Platform:  "test",
	}

	rootCmd := NewRootCmd("2.0.0", vi)
	got := shared.VersionInfoFromCmd(rootCmd)
	assert.Equal(t, "2.0.0", got.Version)
	assert.Equal(t, "test-build", got.BuildID)
}

func TestNewRootCmd_PersistentFlagsExist(t *testing.T) { //nolint:paralleltest // mutates the process environment
	rootCmd := NewRootCmd("dev", serve.VersionInfo{})

	endpointFlag := rootCmd.PersistentFlags().Lookup("endpoint")
	require.NotNil(t, endpointFlag)
	assert.Equal(t, "e", endpointFlag.Shorthand)
	assert.Empty(t, endpointFlag.DefValue)

	portFlag := rootCmd.PersistentFlags().Lookup("port")
	require.NotNil(t, portFlag)
	assert.Equal(t, "p", portFlag.Shorthand)
	assert.Equal(t, "0", portFlag.DefValue)

	jsonFlag := rootCmd.PersistentFlags().Lookup("json")
	require.NotNil(t, jsonFlag)
	assert.Equal(t, "false", jsonFlag.DefValue)
}

func TestNewRootCmd_PersistentPreRunE_SetsEndpointOverride(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("endpoint flag sets override", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		config.SetEndpointOverride("")
		t.Cleanup(func() { config.SetEndpointOverride("") })

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.SetArgs([]string{"--endpoint", "remote.example.com", "gw", "status"})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		_ = rootCmd.Execute()

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Contains(t, cfg.OperatorDiscoveryURL(), "remote.example.com")
		assert.Contains(t, cfg.OperatorPublicURL(), "remote.example.com")
	})

	t.Run("endpoint and port flags set override with port", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		config.SetEndpointOverride("")
		t.Cleanup(func() { config.SetEndpointOverride("") })

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.SetArgs([]string{"--endpoint", "remote.example.com", "--port", "9999", "gw", "status"})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		_ = rootCmd.Execute()

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Contains(t, cfg.OperatorDiscoveryURL(), "remote.example.com")
		assert.NotContains(t, cfg.OperatorDiscoveryURL(), "9999")
		assert.Contains(t, cfg.OperatorPublicURL(), "remote.example.com")
		assert.Contains(t, cfg.OperatorPublicURL(), "9999")
	})

	t.Run("endpoint with port and --port sets split overrides", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		config.SetEndpointOverride("")
		t.Cleanup(func() { config.SetEndpointOverride("") })

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.SetArgs([]string{"--endpoint", "remote.example.com:8085", "--port", "9999", "gw", "status"})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		_ = rootCmd.Execute()

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Contains(t, cfg.OperatorDiscoveryURL(), "remote.example.com:8085")
		assert.NotContains(t, cfg.OperatorDiscoveryURL(), "9999")
		assert.Contains(t, cfg.OperatorPublicURL(), "remote.example.com:9999")
		assert.NotContains(t, cfg.OperatorPublicURL(), "8085")
	})

	t.Run("port-only flag defaults to localhost", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		config.SetEndpointOverride("")
		t.Cleanup(func() { config.SetEndpointOverride("") })

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.SetArgs([]string{"--port", "9090", "gw", "status"})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		_ = rootCmd.Execute()

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Contains(t, cfg.OperatorDiscoveryURL(), "localhost")
		assert.NotContains(t, cfg.OperatorDiscoveryURL(), "9090")
		assert.Contains(t, cfg.OperatorPublicURL(), "localhost")
		assert.Contains(t, cfg.OperatorPublicURL(), "9090")
	})

	t.Run("operator deploy SSH port does not override Gateway HTTPS port", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		config.SetEndpointOverride("")
		t.Cleanup(func() { config.SetEndpointOverride("") })

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.SetArgs([]string{"operator", "deploy", "--hosts", "example", "--port", "2222", "--endpoint", "localhost"})
		rootCmd.SetOut(&bytes.Buffer{})
		rootCmd.SetErr(&bytes.Buffer{})
		_ = rootCmd.Execute()

		cfg, err := config.Load("")
		require.NoError(t, err)
		assert.Contains(t, cfg.OperatorPublicURL(), "localhost")
		assert.NotContains(t, cfg.OperatorPublicURL(), "2222")
	})
}

// TestRootPreRunRefusesAutochthonBeforeRuntimeFiles proves that the root
// pre-run does not read the launch profile or PID file from the working
// directory for an autochthon command, so the refused flag fails first. The
// working directory has no launch profile; any runtime file service the root
// opens is recorded.
func TestRootPreRunRefusesAutochthonBeforeRuntimeFiles(t *testing.T) { //nolint:paralleltest // changes the process working directory and environment
	dir := testutil.TempDir(t)
	t.Chdir(dir)

	var opened []string
	original := shared.NewFileSvc
	shared.NewFileSvc = func(baseDir string, logger *slog.Logger) (fs.RuntimeFileService, error) {
		opened = append(opened, baseDir)
		return original(baseDir, logger)
	}
	t.Cleanup(func() {
		shared.NewFileSvc = original
		config.SetEndpointOverride("")
	})

	cases := map[string][]string{
		"inherited json flag": {"autochthon", "--working-dir", dir, "--json"},
		"endpoint flag":       {"autochthon", "--working-dir", dir, "--endpoint", "gateway.example"},
		"port flag on status": {"autochthon", "status", "--working-dir", dir, "--port", "8443"},
	}
	for name, args := range cases { //nolint:paralleltest // changes the process working directory and environment
		t.Run(name, func(t *testing.T) {
			opened = nil
			rootCmd := NewRootCmd("dev", serve.VersionInfo{})
			rootCmd.SetArgs(args)
			rootCmd.SetOut(&bytes.Buffer{})
			rootCmd.SetErr(&bytes.Buffer{})

			err := rootCmd.Execute()
			require.ErrorIs(t, err, constants.ErrAutochthonGatewayFlag)
			assert.Empty(t, opened, "refusal must precede any runtime file service")
		})
	}
}

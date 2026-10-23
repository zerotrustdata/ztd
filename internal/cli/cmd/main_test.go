// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/audit"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/eval"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/gw"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/mcp"
	operatorcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/operator"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/swagger"
	testcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/test"
	vaultcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/vault"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
)

func TestExecute(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("root command structure matches main.go implementation", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		assert.Equal(t, "ztd", rootCmd.Use)
		assert.Contains(t, rootCmd.Short, "ztd Platform Manager")
		assert.Contains(t, rootCmd.Short, "ztd Gateway")
		assert.Contains(t, rootCmd.Short, "ztd Operator")
		assert.Len(t, rootCmd.Commands(), 18)
	})

	t.Run("root command has all expected subcommands", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		expectedCommands := []string{"gw", "login", "logout", "auth", "consensus", "mcp", "operator", "autochthon", "vault", "test", "audit", "swagger", "report", "public", "tui", "version", "compliance", "eval"}
		for _, expected := range expectedCommands {
			found := false
			for _, cmd := range rootCmd.Commands() {
				if cmd.Name() == expected {
					found = true
					break
				}
			}
			assert.True(t, found, "root command should have %s subcommand", expected)
		}
	})

	t.Run("root command has correct completion options", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		assert.True(t, rootCmd.CompletionOptions.DisableDefaultCmd, "default completion command should be disabled")
	})

	t.Run("root command help displays correctly", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		err := rootCmd.Help()
		require.NoError(t, err)
		output := buf.String()

		assert.Contains(t, output, "ztd")
		assert.Contains(t, output, "gw")
		assert.Contains(t, output, "auth")
		assert.Contains(t, output, "mcp")
		assert.Contains(t, output, "operator")
		assert.Contains(t, output, "vault")
		assert.Contains(t, output, "test")
		assert.Contains(t, output, "audit")
		assert.Contains(t, output, "swagger")
		assert.Contains(t, output, "eval")
	})

	t.Run("root command long description contains key components", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		assert.Contains(t, rootCmd.Long, "zero-trust execution platform")
		assert.Contains(t, rootCmd.Long, "agentic infrastructure")
		assert.Contains(t, rootCmd.Long, "ztd Gateway")
		assert.Contains(t, rootCmd.Long, "ztd Operator")
	})
}

func TestRootCommandValidation(t *testing.T) { //nolint:paralleltest // mutates the process environment
	tests := []struct {
		name           string
		expectedUse    string
		expectedShort  string
		expectedLong   string
		expectedCmds   []string
		expectedCmdLen int
	}{
		{
			name:           "root command has correct metadata",
			expectedUse:    "ztd",
			expectedShort:  "ztd Platform Manager",
			expectedLong:   "zero-trust execution platform",
			expectedCmds:   []string{"gw", "login", "logout", "auth", "consensus", "mcp", "operator", "autochthon", "vault", "test", "audit", "swagger", "report", "public", "tui", "version", "compliance", "eval"},
			expectedCmdLen: 18,
		},
	}

	for _, tt := range tests { //nolint:paralleltest // mutates the process environment
		t.Run(tt.name, func(t *testing.T) {
			rootCmd := NewRootCmd("dev", serve.VersionInfo{})

			assert.Contains(t, rootCmd.Use, tt.expectedUse)
			assert.Contains(t, rootCmd.Short, tt.expectedShort)
			assert.Contains(t, rootCmd.Long, tt.expectedLong)
			assert.Len(t, rootCmd.Commands(), tt.expectedCmdLen)

			for _, expectedCmd := range tt.expectedCmds {
				found := false
				for _, cmd := range rootCmd.Commands() {
					if cmd.Name() == expectedCmd {
						found = true
						break
					}
				}
				assert.True(t, found, "expected to find %s command", expectedCmd)
			}
		})
	}
}

func TestCommandExecutionErrorHandling(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("invalid command returns error", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		rootCmd.SetArgs([]string{"invalid-command"})
		err := rootCmd.Execute()

		assert.Error(t, err)
	})

	t.Run("no arguments shows help", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		rootCmd.RunE = nil
		rootCmd.SetArgs([]string{})
		err := rootCmd.Execute()

		// Cobra shows help and returns nil error when no args provided
		require.NoError(t, err)
		output := buf.String()
		assert.Contains(t, output, "ztd")
	})
}

func TestSubcommandRegistration(t *testing.T) { //nolint:paralleltest // mutates package-level state

	t.Run("all subcommands are non-nil", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		assert.NotNil(t, gw.Cmd(), "gatewayCmd should not be nil")
		assert.NotNil(t, authcmd.Cmd(), "authCmd should not be nil")
		assert.NotNil(t, mcp.Cmd(), "mcpCmd should not be nil")
		assert.NotNil(t, operatorcmd.Cmd(), "operatorCmd should not be nil")
		assert.NotNil(t, vaultcmd.Cmd(), "vaultCmd should not be nil")
		assert.NotNil(t, testcmd.Cmd(), "testCmd should not be nil")
		assert.NotNil(t, audit.Cmd(), "auditCmd should not be nil")
		assert.NotNil(t, swagger.Cmd(), "swaggerCmd should not be nil")
		assert.NotNil(t, eval.Cmd(), "evalCmd should not be nil")
	})

	t.Run("all subcommands have valid cobra.Command structure", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		commands := []*cobra.Command{
			gw.Cmd(),
			authcmd.Cmd(),
			mcp.Cmd(),
			operatorcmd.Cmd(),
			vaultcmd.Cmd(),
			testcmd.Cmd(),
			audit.Cmd(),
			swagger.Cmd(),
			eval.Cmd(),
		}

		for _, cmd := range commands {
			assert.NotNil(t, cmd, "command should not be nil")
			assert.NotEmpty(t, cmd.Use, "command should have a Use field")
			assert.NotEmpty(t, cmd.Short, "command should have a Short field")
		}
	})
}

func TestExecuteStderrErrorOutput(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("error is written to stderr on command failure", func(t *testing.T) {
		oldStderr := os.Stderr
		r, w, _ := os.Pipe()
		os.Stderr = w

		defer func() {
			w.Close()
			os.Stderr = oldStderr
			r.Close()
		}()

		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		rootCmd.SetArgs([]string{"invalid-command"})
		err := executeRootCmd(rootCmd)

		require.Error(t, err)

		w.Close()
		var buf bytes.Buffer
		buf.ReadFrom(r)
		output := buf.String()

		assert.True(t, strings.Contains(output, "Error") || strings.Contains(output, "unknown") || strings.Contains(output, "invalid"),
			"stderr should contain error message")
	})
}

func TestRootCommandConsistency(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("subcommand names are unique", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		cmdNames := make(map[string]bool)
		for _, cmd := range rootCmd.Commands() {
			name := cmd.Name()
			assert.False(t, cmdNames[name], "duplicate command name: %s", name)
			cmdNames[name] = true
		}
	})

	t.Run("subcommand aliases are consistent", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		commands := []*cobra.Command{
			gw.Cmd(),
			authcmd.Cmd(),
			mcp.Cmd(),
			operatorcmd.Cmd(),
			vaultcmd.Cmd(),
			testcmd.Cmd(),
			audit.Cmd(),
			swagger.Cmd(),
		}

		for _, cmd := range commands {
			if len(cmd.Aliases) > 0 {
				for _, alias := range cmd.Aliases {
					assert.NotEmpty(t, alias, "alias should not be empty")
					assert.NotEqual(t, cmd.Use, alias, "alias should not be the same as the command name")
				}
			}
		}
	})
}

func TestExecuteWithHelpFlag(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("--help flag displays help", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		rootCmd.SetArgs([]string{"--help"})
		err := rootCmd.Execute()

		require.NoError(t, err)
		output := buf.String()

		assert.Contains(t, output, "ztd")
		assert.Contains(t, output, "zero-trust execution platform")
	})

	t.Run("help command displays help", func(t *testing.T) { //nolint:paralleltest // mutates the process environment
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		rootCmd.SetArgs([]string{"help"})
		err := rootCmd.Execute()

		require.NoError(t, err)
		output := buf.String()

		assert.Contains(t, output, "ztd")
	})
}

func TestExecuteVersionConsistency(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("version command is available if configured", func(t *testing.T) {
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})

		rootCmd.SetArgs([]string{"--version"})
		err := rootCmd.Execute()

		// Version command should now be configured and return nil error
		assert.NoError(t, err)
	})
}

func TestRootCommandRunFunction(t *testing.T) { //nolint:paralleltest // mutates the process environment
	t.Run("root command without subcommand shows help", func(t *testing.T) {
		rootCmd := NewRootCmd("dev", serve.VersionInfo{})
		rootCmd.RunE = nil
		rootCmd.Run = func(cmd *cobra.Command, args []string) {
			cmd.Help()
		}

		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.SetErr(&buf)

		rootCmd.SetArgs([]string{})
		err := rootCmd.Execute()

		require.NoError(t, err)
		output := buf.String()

		assert.Contains(t, output, "ztd")
	})
}

func TestErrorFormatting(t *testing.T) {
	t.Parallel()
	t.Run("error message format is consistent", func(t *testing.T) {
		t.Parallel()
		testErr := fmt.Errorf("test error")
		var buf bytes.Buffer

		fmt.Fprintf(&buf, "Error: %v\n", testErr)

		output := buf.String()
		assert.Contains(t, output, "Error:")
		assert.Contains(t, output, "test error")
	})
}

func TestRootCommand_RuntimeErrorsAreRenderedOnceWithoutUsage(t *testing.T) { //nolint:paralleltest // mutates the process environment
	rootCmd := NewRootCmd("dev", serve.VersionInfo{})

	assert.True(t, rootCmd.SilenceErrors, "ExecuteWithVersionInfo owns error rendering")
	assert.True(t, rootCmd.SilenceUsage, "runtime failures must not print command usage")
}

func TestExecuteRootCmd_RendersFailureOnceOnConfiguredStderr(t *testing.T) { //nolint:paralleltest // mutates the process environment
	root := NewRootCmd("dev", serve.VersionInfo{})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"invalid-command"})
	require.Error(t, executeRootCmd(root))
	assert.Equal(t, 1, strings.Count(stderr.String(), "Error:"))
	assert.NotContains(t, stderr.String(), "Usage:")
	assert.Empty(t, stdout.String())
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// newScratchFiles returns a file service over an isolated runtime root, which
// owns the scratch directories the agent configs are written into.
func newScratchFiles(t *testing.T) fs.RuntimeFileService {
	t.Helper()
	files, err := fs.NewRuntimeFileService(testutil.TempDir(t), nil)
	require.NoError(t, err)
	return files
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(testutil.TempDir(t), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestPrepare_EveryRegisteredAgentWritesAndVerifies(t *testing.T) {
	t.Parallel()
	for _, integration := range All() {
		t.Run(string(integration.ID), func(t *testing.T) {
			t.Parallel()
			prepared, err := integration.Prepare(context.Background(), newScratchFiles(t), testutil.TempDir(t), "/fake/ztd", string(integration.ID), true)
			require.NoError(t, err)
			t.Cleanup(prepared.Cleanup)

			assert.FileExists(t, prepared.ConfigPath)
			data, err := os.ReadFile(prepared.ConfigPath)
			require.NoError(t, err)
			assert.Contains(t, string(data), "/fake/ztd", "config must point at the ztd stdio bridge")
			assert.Contains(t, string(data), string(integration.ID), "config must select credentials by app name")

			if integration.LaunchStrategy == LaunchMCPConfigFlags {
				assert.Contains(t, prepared.LaunchArgs, "--strict-mcp-config")
				assert.Contains(t, prepared.LaunchArgs, "--disallowed-tools")
			}
		})
	}
}

func TestVerifyIsolated_ReportsTheAgentThatFailsVerification(t *testing.T) {
	t.Parallel()
	integration := All()[0]
	integration.VerifyHooks = []VerifyHook{func(VerifyInput) error { return verifyFailure("lockdown drift") }}

	err := integration.VerifyIsolated(context.Background(), newScratchFiles(t), "/fake/ztd")

	require.ErrorIs(t, err, constants.ErrToolInterceptionVerification)
	assert.Contains(t, err.Error(), string(integration.ID))
}

func TestPrepare_VerifyFalseSkipsHooks(t *testing.T) {
	t.Parallel()
	broken := Integration{
		ID:             "broken",
		ConfigStrategy: ConfigTempJSON,
		LaunchStrategy: LaunchConfigOnly,
		VerifyHooks:    []VerifyHook{verifyStrictLaunchFlags},
	}

	prepared, err := broken.Prepare(context.Background(), newScratchFiles(t), testutil.TempDir(t), "/fake/ztd", "broken", false)
	require.NoError(t, err)
	prepared.Cleanup()

	_, err = broken.Prepare(context.Background(), newScratchFiles(t), testutil.TempDir(t), "/fake/ztd", "broken", true)
	require.ErrorIs(t, err, constants.ErrToolInterceptionVerification)
}

func TestPrepare_FailureCleansUpTempConfig(t *testing.T) {
	t.Parallel()
	var seen string
	broken := Integration{
		ID:             "broken",
		ConfigStrategy: ConfigTempJSON,
		LaunchStrategy: LaunchConfigOnly,
		VerifyHooks: []VerifyHook{func(in VerifyInput) error {
			seen = in.ConfigPath
			return verifyFailure("always fails")
		}},
	}

	_, err := broken.Prepare(context.Background(), newScratchFiles(t), testutil.TempDir(t), "/fake/ztd", "broken", true)
	require.ErrorIs(t, err, constants.ErrToolInterceptionVerification)
	require.NotEmpty(t, seen)
	_, statErr := os.Stat(seen)
	assert.True(t, os.IsNotExist(statErr), "a rejected launch must not leave its temp config behind")
}

func TestPrepare_UnknownStrategyFailsClosed(t *testing.T) {
	t.Parallel()
	_, err := Integration{ID: "bogus", ConfigStrategy: "bogus"}.Prepare(context.Background(), newScratchFiles(t), testutil.TempDir(t), "/fake/ztd", "bogus", true)
	require.ErrorIs(t, err, constants.ErrAgentNotSupported)
}

func TestVerifyHooks(t *testing.T) {
	t.Parallel()
	const ztdServerJSON = `{"mcpServers":{"ztd":{"command":"ztd","args":["mcp","stdio"]}}}`
	strict := []string{"--mcp-config", "x", "--strict-mcp-config", "--disallowed-tools", "Bash"}
	gooseArgs := []string{"session", "--no-profile", "--with-extension", "/fake/ztd mcp stdio"}

	tests := []struct {
		name    string
		hook    VerifyHook
		in      func(t *testing.T) VerifyInput
		wantErr string // empty means pass
	}{
		{
			name: "mcp servers json: valid",
			hook: verifyMCPServersJSON,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "c.json", ztdServerJSON)}
			},
		},
		{
			name: "mcp servers json: missing file",
			hook: verifyMCPServersJSON,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: filepath.Join(testutil.TempDir(t), "nope.json")}
			},
			wantErr: "read mcp config",
		},
		{
			name: "mcp servers json: invalid json",
			hook: verifyMCPServersJSON,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "c.json", `{invalid`)}
			},
			wantErr: "parse mcp config",
		},
		{
			name: "mcp servers json: no ztd server",
			hook: verifyMCPServersJSON,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "c.json", `{"mcpServers":{}}`)}
			},
			wantErr: "missing ztd server entry",
		},
		{
			name: "strict flags: present",
			hook: verifyStrictLaunchFlags,
			in:   func(t *testing.T) VerifyInput { t.Helper(); return VerifyInput{LaunchArgs: strict} },
		},
		{
			name: "strict flags: missing --strict-mcp-config",
			hook: verifyStrictLaunchFlags,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{LaunchArgs: []string{"--disallowed-tools", "Bash"}}
			},
			wantErr: "missing --strict-mcp-config",
		},
		{
			name: "strict flags: --disallowed-tools without a value",
			hook: verifyStrictLaunchFlags,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{LaunchArgs: []string{"--strict-mcp-config", "--disallowed-tools"}}
			},
			wantErr: "missing --disallowed-tools",
		},
		{
			name: "goose flags: present",
			hook: verifyGooseLaunchFlags,
			in:   func(t *testing.T) VerifyInput { t.Helper(); return VerifyInput{LaunchArgs: gooseArgs} },
		},
		{
			name: "goose flags: missing --no-profile",
			hook: verifyGooseLaunchFlags,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{LaunchArgs: []string{"session", "--with-extension", "x"}}
			},
			wantErr: "missing --no-profile",
		},
		{
			name: "goose flags: missing --with-extension",
			hook: verifyGooseLaunchFlags,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{LaunchArgs: []string{"session", "--no-profile"}}
			},
			wantErr: "missing --with-extension",
		},
		{
			name: "goose extension: valid",
			hook: verifyGooseExtensionEntry,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "config.yaml", "extensions:\n  ztd:\n    enabled: true\n")}
			},
		},
		{
			name: "goose extension: no ztd entry",
			hook: verifyGooseExtensionEntry,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "config.yaml", "extensions: {}\n")}
			},
			wantErr: "missing ztd extension entry",
		},
		{
			name: "goose extension: missing file",
			hook: verifyGooseExtensionEntry,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: filepath.Join(testutil.TempDir(t), "nope.yaml")}
			},
			wantErr: "read goose config",
		},
		{
			name: "gemini tools.core: empty array passes",
			hook: verifyGeminiToolsCoreEmpty,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "s.json", `{"tools":{"core":[]}}`)}
			},
		},
		{
			name: "gemini tools.core: tools missing",
			hook: verifyGeminiToolsCoreEmpty,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "s.json", ztdServerJSON)}
			},
			wantErr: "missing tools.core",
		},
		{
			name: "gemini tools.core: null",
			hook: verifyGeminiToolsCoreEmpty,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "s.json", `{"tools":{"exclude":["read_file"]}}`)}
			},
			wantErr: "tools.core is null",
		},
		{
			name: "gemini tools.core: entries leave native tools enabled",
			hook: verifyGeminiToolsCoreEmpty,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "s.json", `{"tools":{"core":["read_file","write_file"]}}`)}
			},
			wantErr: "tools.core has 2 entries",
		},
		{
			name: "gemini tools.core: invalid json",
			hook: verifyGeminiToolsCoreEmpty,
			in: func(t *testing.T) VerifyInput {
				t.Helper()
				return VerifyInput{ConfigPath: writeFile(t, "s.json", `{invalid`)}
			},
			wantErr: "parse gemini settings",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.hook(tt.in(t))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, constants.ErrToolInterceptionVerification)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

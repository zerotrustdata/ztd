// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package governance

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
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

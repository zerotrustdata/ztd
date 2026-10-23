// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestActionTypeStateChangeClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		action          ActionType
		target          string
		wantStateChange bool
		wantChanges     bool
	}{
		{name: "read_file target is read only", action: ActionTypeMcpCall, target: MCPToolReadFile, wantStateChange: true},
		{name: "search_file target is read only", action: ActionTypeMcpCall, target: MCPToolSearchFile, wantStateChange: true},
		{name: "search_directory target is read only", action: ActionTypeMcpCall, target: MCPToolSearchDirectory, wantStateChange: true},
		{name: "run_shell_command target changes state", action: ActionTypeMcpCall, target: MCPToolRunShellCommand, wantStateChange: true, wantChanges: true},
		{name: "unknown MCP target fails closed", action: ActionTypeMcpCall, target: "unknown_tool", wantStateChange: true, wantChanges: true},
		{name: "empty MCP target fails closed", action: ActionTypeMcpCall, target: "", wantStateChange: true, wantChanges: true},
		{name: "non-MCP action does not borrow MCP read-only target", action: ActionTypeExecuteBash, target: MCPToolReadFile, wantStateChange: true, wantChanges: true},
		{name: "inference does not change state", action: ActionTypeInference, target: "anything"},
		{name: "model residency read does not change state", action: ActionTypeOllamaModelResidency, target: "anything"},
		{name: "model release does not change state", action: ActionTypeOllamaModelRelease, target: "anything"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantStateChange, tt.action.IsStateChange(), "IsStateChange")
			require.Equal(t, tt.wantChanges, tt.action.ChangesState(tt.target), "ChangesState(%q)", tt.target)
		})
	}
}

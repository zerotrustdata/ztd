// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"strings"

	"github.com/zerotrustdata/ztd/internal/constants"
)

// SimpleStdioServerConfig represents a simplified MCP server configuration for stdio transport.
// This format is compatible with Cursor/Devin MCP clients.
//
// The stdio bridge ("ztd mcp stdio") is the only supported external-client
// path: CLI and app identity keys are sealed under the keystore master key
// and are unsealed in process, so no client configuration carries key paths.
type SimpleStdioServerConfig struct {
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	Disabled bool     `json:"disabled"`
}

// SimpleConfig represents a simplified MCP client configuration structure.
type SimpleConfig struct {
	MCPServers map[string]SimpleStdioServerConfig `json:"mcpServers"`
}

// NewStdioConfigSimple creates a simplified stdio transport MCP configuration for local native tools.
// This format is compatible with Cursor/Devin MCP clients.
func NewStdioConfigSimple(ztdBinaryPath string) (*SimpleConfig, error) {
	if ztdBinaryPath == "" {
		return nil, constants.ErrMCPConfigBinaryPathEmpty
	}
	if strings.TrimSpace(ztdBinaryPath) == "" {
		return nil, constants.ErrMCPConfigBinaryPathWhitespace
	}

	return &SimpleConfig{
		MCPServers: map[string]SimpleStdioServerConfig{
			"ztd-native": {
				Command:  ztdBinaryPath,
				Args:     []string{"mcp", "stdio"},
				Disabled: false,
			},
		},
	}, nil
}

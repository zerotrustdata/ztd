// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/agent"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
)

// TestMCPConfigOutput_AgentShowStdioOnly validates that printAgentShow prints
// only the Stdio Transport config for every supported agent. The Gateway's
// CLI and app keys are sealed under the keystore master key, so no mTLS client
// configuration (ztd.local, IP Address, or any key/cert path) may be emitted.
func TestMCPConfigOutput_AgentShowStdioOnly(t *testing.T) {
	t.Parallel()
	for _, integration := range agent.All() {
		agentID := string(integration.ID)
		t.Run(agentID, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{}
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			require.NoError(t, printAgentShow(cmd, agentID),
				"printAgentShow must succeed for agent %s", agentID)

			output := buf.String()
			assert.Contains(t, output, "ztd Gateway MCP Configurations",
				"agent show output must include the header")
			assert.Contains(t, output, "Stdio Transport",
				"agent show output must include the Stdio Transport section")
			assert.Contains(t, output, "\"ztd-native\"",
				"agent show output must include the stdio server entry")
			assert.Contains(t, output, "\"stdio\"",
				"agent show output must include the mcp stdio args")

			assert.NotContains(t, output, "ztd.local",
				"agent show output must not include the ztd.local mTLS section")
			assert.NotContains(t, output, "IP Address",
				"agent show output must not include the IP Address mTLS section")
			assert.NotContains(t, output, "mTLS",
				"agent show output must not mention mTLS client configs")
			for _, forbidden := range []string{"clientCertificate", "clientKey", "caCertificate", ".key"} {
				assert.NotContains(t, output, forbidden,
					"agent show output must not expose %s", forbidden)
			}
		})
	}
}

// TestMCPConfigOutput_StdioConfigJSON validates that printMCPConfigStdio
// produces a valid simplified stdio MCP configuration with the ztd binary
// path and the "mcp stdio" args. This covers the stdio transport section of
// the agent show output that the deleted E2E test did not explicitly verify.
func TestMCPConfigOutput_StdioConfigJSON(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	require.NoError(t, printMCPConfigStdio(cmd), "printMCPConfigStdio must succeed")

	output := buf.String()
	assert.Contains(t, output, "mcpServers", "stdio config output must contain mcpServers JSON")

	jsonStart := strings.Index(output, "{")
	require.GreaterOrEqual(t, jsonStart, 0, "stdio config output must contain a JSON block")

	var stdioConfig mcp.SimpleConfig
	require.NoError(t, json.NewDecoder(strings.NewReader(output[jsonStart:])).Decode(&stdioConfig),
		"stdio config output must decode as a valid mcp.SimpleConfig")

	nativeServer, ok := stdioConfig.MCPServers["ztd-native"]
	require.True(t, ok, "stdio config must include a ztd-native server entry")
	assert.NotEmpty(t, nativeServer.Command,
		"stdio config must include a non-empty command path (the ztd binary path from os.Executable)")
	assert.Equal(t, []string{"mcp", "stdio"}, nativeServer.Args,
		"stdio config args must be [mcp stdio]")
	assert.False(t, nativeServer.Disabled,
		"stdio config server must not be disabled")
}

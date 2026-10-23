// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/agent"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestAgentCmd(t *testing.T) {
	t.Parallel()
	t.Run("agent command has correct use and description", func(t *testing.T) {
		t.Parallel()
		cmd := agentCmd()
		assert.Equal(t, "agent", cmd.Use)
		assert.Contains(t, cmd.Short, "Agent integration")
		assert.Contains(t, cmd.Long, "popular AI agent binaries")
	})

	t.Run("agent list command works", func(t *testing.T) {
		t.Parallel()
		cmd := agentListCmd()
		assert.Equal(t, "list", cmd.Use)
		assert.Contains(t, cmd.Short, "List supported")

		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		require.NoError(t, err)
		output := buf.String()
		assert.Contains(t, output, "claude")
		assert.Contains(t, output, "codex")
		assert.Contains(t, output, "devin")
		assert.Contains(t, output, "gemini")
		assert.Contains(t, output, "goose")
		assert.NotContains(t, output, "cursor")
		assert.NotContains(t, output, "aider")
		assert.NotContains(t, output, "generic")
	})

	t.Run("agent show command requires exactly one argument", func(t *testing.T) {
		t.Parallel()
		cmd := agentShowCmd()
		assert.Equal(t, "show <agent>", cmd.Use)
		assert.Contains(t, cmd.Short, "Print MCP client configuration")
	})

	t.Run("agent show prints only the stdio transport config for claude", func(t *testing.T) {
		t.Parallel()
		cmd := agentShowCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"claude"})
		err := cmd.Execute()
		require.NoError(t, err)
		output := buf.String()
		assert.Contains(t, output, "ztd Gateway MCP Configurations")
		assert.Contains(t, output, "Stdio Transport")
		assert.NotContains(t, output, "ztd.local")
		assert.NotContains(t, output, "IP Address")
	})

	agents := []string{"gemini", "goose"}
	for _, agent := range agents {
		t.Run("agent show generates gateway configs for "+agent, func(t *testing.T) {
			t.Parallel()
			cmd := agentShowCmd()
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)
			cmd.SetArgs([]string{agent})
			err := cmd.Execute()
			require.NoError(t, err)
			output := buf.String()
			assert.Contains(t, output, "ztd Gateway MCP Configurations")
		})
	}

	t.Run("agent show returns error for unknown agent", func(t *testing.T) {
		t.Parallel()
		cmd := agentShowCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"unknown-agent"})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent not found")
	})

	t.Run("agent show is case-insensitive", func(t *testing.T) {
		t.Parallel()
		cmd := agentShowCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"CLAUDE"})
		err := cmd.Execute()
		require.NoError(t, err)
		output := buf.String()
		assert.Contains(t, output, "ztd Gateway MCP Configurations")
	})
}

func TestMcpCmd(t *testing.T) {
	t.Parallel()
	t.Run("mcp command has correct use and description", func(t *testing.T) {
		t.Parallel()
		cmd := Cmd()
		assert.Equal(t, "mcp", cmd.Use)
		assert.Contains(t, cmd.Short, "MCP protocol operations")
		assert.Contains(t, cmd.Short, "stdio")
		assert.Contains(t, cmd.Long, "stdio transport")
	})

	t.Run("mcp command has no required flags", func(t *testing.T) {
		t.Parallel()
		cmd := Cmd()
		require.NotNil(t, cmd)

		// Should not have endpoint or pki-dir flags anymore
		endpointFlag := cmd.Flags().Lookup("endpoint")
		pkiDirFlag := cmd.Flags().Lookup("pki-dir")

		assert.Nil(t, endpointFlag, "mcp command should not have --endpoint flag")
		assert.Nil(t, pkiDirFlag, "mcp command should not have --pki-dir flag")
	})
}

func TestJSONRPCRequest(t *testing.T) {
	t.Parallel()
	t.Run("valid JSON-RPC request parses correctly", func(t *testing.T) {
		t.Parallel()
		jsonStr := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
		var req JSONRPCRequest
		err := json.Unmarshal([]byte(jsonStr), &req)
		require.NoError(t, err)
		assert.Equal(t, "2.0", req.JSONRPC)
		assert.InEpsilon(t, 1, req.ID, 0.0)
		assert.Equal(t, "tools/list", req.Method)
	})

	t.Run("JSON-RPC request without params parses correctly", func(t *testing.T) {
		t.Parallel()
		jsonStr := `{"jsonrpc":"2.0","id":2,"method":"initialize"}`
		var req JSONRPCRequest
		err := json.Unmarshal([]byte(jsonStr), &req)
		require.NoError(t, err)
		assert.Equal(t, "initialize", req.Method)
		assert.Nil(t, req.Params)
	})
}

type mcpProxyStatusResult struct {
	Status string `json:"status"`
}

func TestJSONRPCResponse(t *testing.T) {
	t.Parallel()
	t.Run("JSON-RPC response with result serializes correctly", func(t *testing.T) {
		t.Parallel()
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Result:  mcpProxyStatusResult{Status: "ok"},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(data), "2.0")
		assert.Contains(t, string(data), "result")
	})

	t.Run("JSON-RPC response with error serializes correctly", func(t *testing.T) {
		t.Parallel()
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      1,
			Error: &RPCError{
				Code:    -32601,
				Message: "method not found",
			},
		}
		data, err := json.Marshal(resp)
		require.NoError(t, err)
		assert.Contains(t, string(data), "error")
		assert.Contains(t, string(data), "-32601")
	})
}

func TestRPCError(t *testing.T) {
	t.Parallel()
	t.Run("RPC error structure is correct", func(t *testing.T) {
		t.Parallel()
		err := RPCError{
			Code:    constants.JSONRPCErrorCodeParseError,
			Message: constants.JSONRPCErrorMessageParseError,
			Data:    "invalid JSON",
		}
		assert.Equal(t, constants.JSONRPCErrorCodeParseError, err.Code)
		assert.Equal(t, constants.JSONRPCErrorMessageParseError, err.Message)
		assert.Equal(t, "invalid JSON", err.Data)
	})
}

func TestToolsListResult(t *testing.T) {
	t.Parallel()
	t.Run("tools list result structure is correct", func(t *testing.T) {
		t.Parallel()
		result := ToolsListResult{
			Tools: []Tool{
				{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: &mcp.InputSchema{Type: "object"},
				},
			},
		}
		assert.Len(t, result.Tools, 1)
		assert.Equal(t, "test_tool", result.Tools[0].Name)
	})
}

func TestTool(t *testing.T) {
	t.Parallel()
	t.Run("tool structure is correct", func(t *testing.T) {
		t.Parallel()
		tool := Tool{
			Name:        "execute_bash",
			Description: "Execute a bash command",
			InputSchema: &mcp.InputSchema{
				Type: "object",
				Properties: map[string]*mcp.PropertySchema{
					"command": {
						Type:        "string",
						Description: "The command to execute",
					},
				},
			},
		}
		assert.Equal(t, "execute_bash", tool.Name)
		assert.Equal(t, "Execute a bash command", tool.Description)
		assert.NotNil(t, tool.InputSchema)
	})
}

func TestL3SuspensionDecodesTypedSuspension(t *testing.T) {
	t.Parallel()
	t.Run("returns the typed suspension of a suspended tools/call", func(t *testing.T) {
		t.Parallel()
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      float64(1),
			Result: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "paused"},
				},
				"suspension": map[string]interface{}{
					"id":           "tx-1",
					"status":       "SUSPENDED",
					"tx_hash":      "tx-1",
					"approval_url": "https://example.com/api/v1/approve/tx-1",
					"message":      "paused",
				},
			},
		}
		suspension, err := l3Suspension(resp)
		require.NoError(t, err)
		require.NotNil(t, suspension)
		assert.Equal(t, "tx-1", suspension.TxHash)
		assert.Equal(t, "https://example.com/api/v1/approve/tx-1", suspension.ApprovalURL)
	})

	t.Run("returns nil for a completed call", func(t *testing.T) {
		t.Parallel()
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      float64(1),
			Result: map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "done"}},
			},
		}
		suspension, err := l3Suspension(resp)
		require.NoError(t, err)
		assert.Nil(t, suspension)
	})

	t.Run("ignores approval URLs that appear only in text", func(t *testing.T) {
		t.Parallel()
		resp := JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      float64(1),
			Result: map[string]interface{}{
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "Please visit https://example.com/api/v1/approve/tx-1"},
				},
			},
		}
		suspension, err := l3Suspension(resp)
		require.NoError(t, err)
		assert.Nil(t, suspension, "text is never a suspension: only the typed suspension field suspends")
	})

	t.Run("returns nil when the response has no result", func(t *testing.T) {
		t.Parallel()
		suspension, err := l3Suspension(JSONRPCResponse{JSONRPC: "2.0", ID: float64(1)})
		require.NoError(t, err)
		assert.Nil(t, suspension)
	})
}

func generateTestCerts(t *testing.T) (certPath, keyPath, caPath string) {
	t.Helper()
	tempDir := testutil.TempDir(t)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"ztd.local"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	certPath = filepath.Join(tempDir, constants.TestCertFilename)
	keyPath = filepath.Join(tempDir, constants.TestKeyFilename)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	require.NoError(t, os.WriteFile(certPath, certPEM, constants.PermFilePublic))

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(keyPath, keyPEM, constants.PermFilePublic))

	return certPath, keyPath, certPath
}

func TestPrintMCPConfigStdio(t *testing.T) {
	t.Parallel()
	t.Run("generates stdio config with binary path", func(t *testing.T) {
		t.Parallel()
		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)

		err := printMCPConfigStdio(cmd)
		// This will use the actual os.Executable, which should work in test environment
		if err != nil {
			// If it fails, check the error message
			assert.Contains(t, err.Error(), "failed to get binary path")
		} else {
			output := buf.String()
			assert.Contains(t, output, "mcpServers")
			assert.Contains(t, output, "ztd")
			assert.Contains(t, output, "stdio")
		}
	})
}

func TestAgentRunCmd(t *testing.T) {
	t.Parallel()
	t.Run("agent run command has correct structure", func(t *testing.T) {
		t.Parallel()
		cmd := agentRunCmd()
		assert.Contains(t, cmd.Use, "run")
		assert.Contains(t, cmd.Short, "Launch an AI agent")
		assert.Contains(t, cmd.Long, "Launch an AI agent")
	})

	t.Run("agent run does not have url flag and has verify flag", func(t *testing.T) {
		t.Parallel()
		cmd := agentRunCmd()
		urlFlag := cmd.Flags().Lookup("url")
		assert.Nil(t, urlFlag, "agent run should not have --url flag")
		verifyFlag := cmd.Flags().Lookup("verify")
		require.NotNil(t, verifyFlag, "agent run should have --verify flag")
		assert.Equal(t, "bool", verifyFlag.Value.Type())
	})

	t.Run("agent run has silence flags set", func(t *testing.T) {
		t.Parallel()
		cmd := agentRunCmd()
		assert.True(t, cmd.SilenceErrors, "should silence errors")
		assert.True(t, cmd.SilenceUsage, "should silence usage")
	})
}

func TestPrintAgentShow(t *testing.T) {
	t.Parallel()
	t.Run("printAgentShow handles all supported agents", func(t *testing.T) {
		t.Parallel()
		for _, integration := range agent.All() {
			cmd := &cobra.Command{}
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			require.NoError(t, printAgentShow(cmd, string(integration.ID)))
			output := buf.String()
			assert.Contains(t, output, "ztd Gateway MCP Configurations")
			assert.Contains(t, output, "Stdio Transport")
			assert.NotContains(t, output, "mTLS")
		}
	})

	t.Run("printAgentShow is case-insensitive for agent IDs", func(t *testing.T) {
		t.Parallel()
		for _, id := range []string{"CLAUDE", "ClaUdE"} {
			cmd := &cobra.Command{}
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			require.NoError(t, printAgentShow(cmd, id))
			assert.Contains(t, buf.String(), "Stdio Transport")
		}
	})
}

func TestMcpStdioCmd(t *testing.T) {
	t.Parallel()
	t.Run("mcp stdio command has correct structure", func(t *testing.T) {
		t.Parallel()
		cmd := mcpStdioCmd()
		assert.Equal(t, "stdio", cmd.Use)
		assert.Contains(t, cmd.Short, "Run MCP stdio server")
		assert.Contains(t, cmd.Long, "proxies all requests")
	})

	t.Run("mcp stdio command has RunE function", func(t *testing.T) {
		t.Parallel()
		cmd := mcpStdioCmd()
		assert.NotNil(t, cmd.RunE, "should have RunE function")
	})
}

func TestMcpStdioCmd_FlagsRegistered(t *testing.T) {
	t.Parallel()
	cmd := mcpStdioCmd()
	expectedFlags := []string{
		constants.Flag.ClientCert, constants.Flag.ClientKey, constants.Flag.CABundle,
		constants.Flag.GatewayURL, constants.Flag.App,
	}
	for _, name := range expectedFlags {
		f := cmd.Flags().Lookup(name)
		require.NotNil(t, f, "mcp stdio should have --%s flag", name)
		assert.Equal(t, "string", f.Value.Type(), "--%s should be a string flag", name)
	}
}

func TestAgentRunPostureHelp(t *testing.T) {
	t.Parallel()
	help := agentRunPostureHelp()
	assert.Contains(t, help, "GATEWAY POSTURE:")
	for _, p := range constants.AllPostures {
		assert.Contains(t, help, string(p))
		assert.Contains(t, help, p.Description())
	}
}

func TestAgentRunAuditHelpUsesAppFilter(t *testing.T) {
	t.Parallel()
	long := agentRunCmd().Long
	assert.Contains(t, long, "ztd audit events --app spiffe://ztd.local/app/claude")
	assert.Contains(t, long, "ztd audit summary --app spiffe://ztd.local/app/claude")
	assert.NotContains(t, long, "--operator-session-id spiffe://")
}

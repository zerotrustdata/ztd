//go:build integration

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package tests

/*
TestMCPStdio_ToolsListReachesGateway proves the stdio ingress path end to end:
a JSON-RPC request written to the stdio bridge's stdin (`ztd mcp stdio`) is
proxied over mTLS to the Gateway's governed MCP endpoint (POST /mcp), and the
Gateway's JSON-RPC response is written back to stdout.

The bridge is driven through its real cobra command (McpStdioCmdWithConfig) with
credentials supplied by the enrolled fixture identity via --client-cert,
--client-key and --ca-bundle, and os.Stdin/os.Stdout swapped for in-memory pipes.
*/

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	mcpcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/mcp"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/internal/testutil"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// TestMCPStdio_ToolsListReachesGateway drives the real `ztd mcp stdio` bridge
// against a real Gateway: initialize is answered locally, and tools/list is
// proxied over mTLS to the governed MCP endpoint.
func TestMCPStdio_ToolsListReachesGateway(t *testing.T) {
	fixture := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName:          t.Name(),
		AllowTestPortZero: true,
	})
	fixture.WaitForReady(t)

	identity := fixtures.EnrollClientIdentity(t, fixture, "stdio-user", "stdio-org", "stdio-fingerprint", "stdio-host")

	// Materialize the enrolled client cert/key and the gateway trust bundle as files,
	// since the stdio bridge takes credentials by path.
	dir := testutil.TempDir(t)
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.pem")

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: identity.PrivateKey})
	require.NoError(t, os.WriteFile(certPath, identity.Certificate, constants.PermFilePrivate))
	require.NoError(t, os.WriteFile(keyPath, keyPEM, constants.PermFilePrivate))

	caBundle := append(append([]byte{}, testutil.ReadRootCA(t, fixture.PKIDir)...), testutil.ReadOperatorCA(t, fixture.PKIDir)...)
	require.NoError(t, os.WriteFile(caPath, caBundle, constants.PermFilePrivate))

	gatewayURL := network.LocalhostHTTPSURL(fixture.Service.GetHTTPSPort()) + constants.APIPaths.MCPEndpoint

	fileSvc, _ := cmdtest.NewCmdTestEnv(t)
	cmd := mcpcmd.McpStdioCmdWithConfig(cmdtest.FileSvcFactoryFor(fileSvc))
	require.NoError(t, cmd.Flags().Set(constants.Flag.ClientCert, certPath))
	require.NoError(t, cmd.Flags().Set(constants.Flag.ClientKey, keyPath))
	require.NoError(t, cmd.Flags().Set(constants.Flag.CABundle, caPath))
	require.NoError(t, cmd.Flags().Set(constants.Flag.GatewayURL, gatewayURL))
	cmd.SetContext(t.Context())

	stdinReq := []byte(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"stdio-test","version":"0"}}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n",
	)

	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)

	origStdin, origStdout := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = stdinR, stdoutW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = origStdin, origStdout
		_ = stdinR.Close()
		_ = stdoutR.Close()
	})

	// Requests are small enough to fit in the pipe buffers, so write them up front
	// and close stdin to signal EOF to the bridge.
	_, err = stdinW.Write(stdinReq)
	require.NoError(t, err)
	require.NoError(t, stdinW.Close())

	runErr := cmd.RunE(cmd, nil)
	require.NoError(t, stdoutW.Close())
	os.Stdout = origStdout
	require.NoError(t, runErr, "stdio bridge must exit cleanly on stdin EOF")

	var lines [][]byte
	scanner := bufio.NewScanner(stdoutR)
	for scanner.Scan() {
		lines = append(lines, append([]byte{}, scanner.Bytes()...))
	}
	require.NoError(t, scanner.Err())
	require.Len(t, lines, 2, "expected one response per request, got: %q", lines)

	// initialize is answered locally by the bridge.
	var initResp struct {
		ID     json.RawMessage `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(lines[0], &initResp))
	require.Equal(t, "1", string(initResp.ID))
	require.NotEmpty(t, initResp.Result.ProtocolVersion)

	// tools/list is proxied to the Gateway's governed MCP endpoint.
	var listResp struct {
		ID    json.RawMessage `json:"id"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(bytes.NewReader(lines[1])).Decode(&listResp), "tools/list response: %s", lines[1])
	require.Nil(t, listResp.Error, "gateway must not return an error for tools/list: %s", lines[1])
	require.Equal(t, "2", string(listResp.ID))
	require.NotEmpty(t, listResp.Result.Tools, "gateway tools/list must return a non-empty tools list: %s", lines[1])
}

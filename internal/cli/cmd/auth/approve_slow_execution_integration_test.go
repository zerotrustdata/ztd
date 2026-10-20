// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build integration

package authcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/mcp"
	"github.com/zerotrustdata/ztd/internal/services/network"
	"github.com/zerotrustdata/ztd/test/fixtures"
)

// Approval returns the final execution receipt, so a successful execution that
// takes longer than the general API timeout must still report approval success.
func TestApproveCmd_CLI_WaitsForSlowGovernedExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses the Unix sleep command")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	gateway := fixtures.NewGatewayFixture(t, fixtures.GatewayFixtureOptions{
		TestName: "slow-cli-approval", Posture: constants.PostureRatify, NoDownstream: true,
	})
	gateway.WaitForReady(t)
	identity := fixtures.BootstrapCLIIdentity(t, gateway, "slow-cli-approval")
	client := fixtures.CreateCLIMTLSClient(t, gateway, identity)
	t.Cleanup(client.CloseIdleConnections)

	args, err := json.Marshal(models.RunShellCommandRequest{Command: "sleep", Args: []string{"6"}})
	require.NoError(t, err)
	params, err := json.Marshal(mcp.CallToolRequest{Name: constants.MCPToolRunShellCommand, Arguments: args})
	require.NoError(t, err)
	body, err := json.Marshal(mcp.JSONRPCRequest{JSONRPC: "2.0", ID: 1, Method: "tools/call", Params: params})
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		network.LocalhostHTTPSURL(gateway.Service.GetHTTPSPort())+constants.APIPaths.MCPEndpoint, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	response, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s", response)
	var rpc mcp.JSONRPCResponse
	require.NoError(t, json.Unmarshal(response, &rpc))
	require.Nil(t, rpc.Error)
	var suspended mcp.CallToolResult
	require.NoError(t, json.Unmarshal(rpc.Result, &suspended))
	require.NotNil(t, suspended.Suspension, "%s", response)

	files, cfg := cmdtest.NewCmdTestEnv(t)
	identity.InstallCLIRuntime(t, gateway, files, cfg)
	cmd := approveCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), approvalAPIClientFactory, cmdtest.FileSvcFactoryFor(files), nil)
	cmd.SetContext(ctx)
	require.NoError(t, cmd.Flags().Set("cli", "true"))
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	require.NoError(t, cmd.RunE(cmd, []string{suspended.Suspension.TxHash}))
	require.Contains(t, output.String(), "approved successfully")
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// jsonStatusCmd returns a subcommand under a root that carries the global --json flag.
func jsonStatusCmd() (*cobra.Command, *bytes.Buffer) {
	root := &cobra.Command{Use: "ztd"}
	root.PersistentFlags().Bool("json", true, "JSON output")
	child := &cobra.Command{Use: "status"}
	root.AddCommand(child)
	buf := &bytes.Buffer{}
	child.SetOut(buf)
	child.SetErr(buf)
	return child, buf
}

func TestGatewayStatusJSONOnline(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	mock := &statusMockClient{responses: map[string][]byte{
		"/api/v1/health":             []byte(`{"status":"ok","posture":"doctrine","pid":4242}`),
		constants.APIPaths.Operators: []byte(`{"success":true,"operators":[]}`),
	}}
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) { return mock, nil }
	cmd, buf := jsonStatusCmd()
	require.NoError(t, printGatewayStatus(cmd, cfg, factory, cmdtest.FileSvcFactoryFor(fileSvc)))
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
	require.Equal(t, true, got["running"])
	require.Equal(t, "online", got["state"])
	require.EqualValues(t, 4242, got["pid"])
	require.Contains(t, got, "health")
	require.Contains(t, got, "operators")
	require.Contains(t, got["console"], "/console/")
	require.NotContains(t, buf.String(), "Gateway  online")
}

func TestGatewayStatusJSONStopped(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	factory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return nil, constants.ErrNotFound
	}
	cmd, buf := jsonStatusCmd()
	require.NoError(t, printGatewayStatus(cmd, cfg, factory, cmdtest.FileSvcFactoryFor(fileSvc)))
	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
	require.Equal(t, false, got["running"])
	require.Equal(t, "stopped", got["state"])
}

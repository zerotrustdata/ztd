// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	cliconfig "github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	ztdconfig "github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// markGatewayRunning points the Gateway PID file at the live test process so
// OperatorStatus reports a running Gateway.
func markGatewayRunning(t *testing.T, fileSvc fs.RuntimeFileService) string {
	t.Helper()
	pid := strconv.Itoa(os.Getpid())
	pidRelPath := filepath.Join(constants.PidDirname, constants.OperatorPIDFilename)
	require.NoError(t, fileSvc.WriteFile(context.Background(), pidRelPath, []byte(pid), constants.PermFilePrivate))
	return pid
}

func runGatewayStart(t *testing.T, fileSvc fs.RuntimeFileService, cfg *cliconfig.Config, args ...string) (string, error) {
	t.Helper()
	cmd := gatewayStartCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), cmdtest.FileSvcFactoryFor(fileSvc), nil)
	cmd.SetArgs(args)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.Execute()
	return buf.String(), err
}

func writeRunningProfile(t *testing.T, fileSvc fs.RuntimeFileService, capabilities constants.OperatorCapabilities) {
	t.Helper()
	require.NoError(t, serve.WriteLaunchProfile(fileSvc, serve.GatewayConfig{
		OperatorCapabilities: capabilities,
		Posture:              ztdconfig.PostureDoctrine,
		HTTPPort:             constants.Ports.OperatorHttp,
		HTTPSPort:            constants.Ports.OperatorHttps,
		ListenHost:           constants.LocalhostIP,
		LogLevel:             "info",
	}))
}

func TestGatewayStart_RunningWithoutExplicitFlagsReusesGateway(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	markGatewayRunning(t, fileSvc)
	writeRunningProfile(t, fileSvc, constants.OperatorCapabilities{constants.OperatorCapabilityData})

	out, err := runGatewayStart(t, fileSvc, cfg)
	require.NoError(t, err)
	assert.Contains(t, out, "already running")
	assert.NotContains(t, out, "Detected network identity")
}

func TestGatewayStart_RunningWithMatchingFlagsReusesGateway(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	markGatewayRunning(t, fileSvc)
	writeRunningProfile(t, fileSvc, constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference})

	// Order and the implicit data capability do not count as differences.
	out, err := runGatewayStart(t, fileSvc, cfg, "--capabilities", "inference", "--posture", "doctrine", "--listen-host", constants.LocalhostIP)
	require.NoError(t, err)
	assert.Contains(t, out, "already running")
}

func TestGatewayStart_RunningWithDifferentCapabilitiesFails(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	pid := markGatewayRunning(t, fileSvc)
	writeRunningProfile(t, fileSvc, constants.OperatorCapabilities{constants.OperatorCapabilityData})

	out, err := runGatewayStart(t, fileSvc, cfg, "--capabilities", "data,inference,observer")
	require.ErrorIs(t, err, constants.ErrGatewayAlreadyRunning)
	assert.Contains(t, out, "--capabilities")
	assert.Contains(t, out, "current:  data")
	assert.Contains(t, out, "proposed: data,inference,observer")
	assert.Contains(t, out, "gw stop")

	// The running Gateway is left untouched.
	pidData, readErr := fileSvc.ReadFile(context.Background(), filepath.Join(constants.PidDirname, constants.OperatorPIDFilename))
	require.NoError(t, readErr)
	assert.Equal(t, pid, string(pidData))
}

func TestGatewayStart_RunningWithDifferentListenHostFails(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	markGatewayRunning(t, fileSvc)
	writeRunningProfile(t, fileSvc, constants.OperatorCapabilities{constants.OperatorCapabilityData})

	out, err := runGatewayStart(t, fileSvc, cfg, "--listen-host", "0.0.0.0")
	require.ErrorIs(t, err, constants.ErrGatewayAlreadyRunning)
	assert.Contains(t, out, "--listen-host")
}

func TestGatewayStart_RunningWithoutProfileRejectsExplicitFlags(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	markGatewayRunning(t, fileSvc)

	out, err := runGatewayStart(t, fileSvc, cfg, "--cors-origin", "https://new-origin.lovable.app")
	require.ErrorIs(t, err, constants.ErrGatewayAlreadyRunning)
	require.ErrorIs(t, err, constants.ErrLaunchProfileMissing)
	assert.Contains(t, out, "--cors-origin")
}

func TestGatewayStart_RunningWithDifferentEnvOllamaEndpointFails(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)
	markGatewayRunning(t, fileSvc)
	writeRunningProfile(t, fileSvc, constants.OperatorCapabilities{constants.OperatorCapabilityData})

	// ZTD_OLLAMA_ENDPOINT (exported or loaded from .env) supplies the flag, so
	// a changed value is a requested difference even with no flag on the line.
	t.Setenv(string(constants.EnvVar.OllamaEndpoint), "http://192.0.2.10:11434")
	out, err := runGatewayStart(t, fileSvc, cfg)
	require.ErrorIs(t, err, constants.ErrGatewayAlreadyRunning)
	assert.Contains(t, out, "--inference-ollama-endpoint")
	assert.Contains(t, out, "proposed: http://192.0.2.10:11434")
}

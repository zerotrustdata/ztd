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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	operatorv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/operator/v1"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authcmd "github.com/zerotrustdata/ztd/internal/cli/cmd/auth"
	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/models"
	"github.com/zerotrustdata/ztd/internal/services/fs"
)

// setupGatewayTestEnv creates a temp dir with minimal .ztd structure so that
// loadConfig succeeds but ProcessManager finds no running gateway.
func setupGatewayTestEnv(t *testing.T) {
	t.Helper()
	tmpDir := cmdtest.ChdirTemp(t)

	runtimeDir := filepath.Join(tmpDir, ".ztd")
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeDir, "pki"), constants.PermDirStandard))
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeDir, "secrets"), constants.PermDirPrivate))
	require.NoError(t, os.MkdirAll(filepath.Join(runtimeDir, "pki", "trust"), constants.PermDirStandard))

	protocolDir := filepath.Join(tmpDir, "protocol", "constants")
	require.NoError(t, os.MkdirAll(protocolDir, constants.PermDirStandard))
	require.NoError(t, os.WriteFile(filepath.Join(protocolDir, "paths.json"), []byte(cmdtest.MinimalPathsJSON(t)), constants.PermFilePublic))
}

func TestGatewayStopCmd_NoConfigReturnsError(t *testing.T) {
	cmdtest.ChdirTemp(t)

	originalLoader := shared.ConfigLoad
	shared.ConfigLoad = func(string) (*config.Config, error) {
		return nil, fmt.Errorf("no config")
	}
	t.Cleanup(func() { shared.ConfigLoad = originalLoader })

	cmd := gatewayStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
}

func TestGatewayStatusCmd_NoConfigReturnsError(t *testing.T) {
	cmdtest.ChdirTemp(t)

	originalLoader := shared.ConfigLoad
	shared.ConfigLoad = func(string) (*config.Config, error) {
		return nil, fmt.Errorf("no config")
	}
	t.Cleanup(func() { shared.ConfigLoad = originalLoader })

	cmd := gatewayStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
}

func TestGatewaySettingsCmd_NoConfigReturnsError(t *testing.T) {
	cmdtest.ChdirTemp(t)

	cmd := gatewaySettingsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
}

func TestGatewayResetCmd_NoConfigReturnsError(t *testing.T) {
	cmdtest.ChdirTemp(t)

	originalLoader := shared.ConfigLoad
	shared.ConfigLoad = func(string) (*config.Config, error) {
		return nil, fmt.Errorf("no config")
	}
	t.Cleanup(func() { shared.ConfigLoad = originalLoader })

	cmd := gatewayResetCmd()
	cmd.Flags().Set("force", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
}

func TestGatewayCleanCmd_NoConfigReturnsError(t *testing.T) {
	cmdtest.ChdirTemp(t)

	originalLoader := shared.ConfigLoad
	shared.ConfigLoad = func(string) (*config.Config, error) {
		return nil, fmt.Errorf("no config")
	}
	t.Cleanup(func() { shared.ConfigLoad = originalLoader })

	cmd := gatewayCleanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
}

func TestGatewayCleanCmd_AbortsOnNoResponse(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayCleanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.WriteString("n\n")
	w.Close()
	t.Cleanup(func() { os.Stdin = originalStdin })

	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrConfirmationAborted)
	assert.Contains(t, buf.String(), "Aborted")
}

func TestGatewayResetCmd_AbortsOnNoResponse(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayResetCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.WriteString("n\n")
	w.Close()
	t.Cleanup(func() { os.Stdin = originalStdin })

	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrConfirmationAborted)
	assert.Contains(t, buf.String(), "Aborted")
}

func TestGatewayStartCmd_InvalidPostureReturnsError(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayStartCmd()
	cmd.Flags().Set("posture", "invalid")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, constants.ErrInvalidPosture)
}

func TestGatewayStopCmd_NotRunningReturnsNil(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayStopCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "not running")
}

func TestGatewayStatusCmd_NotRunningReturnsStopped(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Gateway  stopped")
}

type statusMockClient struct {
	responses map[string][]byte
}

func (m *statusMockClient) Get(path string) ([]byte, error) {
	if resp, ok := m.responses[path]; ok {
		return resp, nil
	}
	for k, v := range m.responses {
		if strings.HasPrefix(path, k) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("unexpected path: %s", path)
}

func (m *statusMockClient) Post(string, interface{}) ([]byte, error) { return nil, nil }
func (m *statusMockClient) Put(string, interface{}) ([]byte, error)  { return nil, nil }
func (m *statusMockClient) Delete(string) ([]byte, error)            { return nil, nil }

func TestGatewayStatusCmd_ReportsConnectedOperators(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	healthResp, _ := json.Marshal(models.HealthResponse{
		Status: constants.GatewayModeStatusOK,
		PID:    9999,
	})
	operatorsResp, _ := json.Marshal(models.OperatorSlotResponse{
		Success: true,
		Operators: []*operatorv1.OperatorDocument{
			{
				Id:                   "ztd-model-provenance-operator",
				OperatorCapabilities: []string{string(constants.OperatorCapabilityProvenance)},
				OperatorType:         string(constants.OperatorTypeRemote),
				CurrentHostname:      "beepboop",
				OperatorSessionId:    "sess-prov-1",
				Status:               string(constants.OperatorStatusActive),
			},
			{
				Id:                   "ztd-provider-boundary-observer",
				OperatorCapabilities: []string{string(constants.OperatorCapabilityObserver)},
				OperatorType:         string(constants.OperatorTypeRemote),
				CurrentHostname:      "beepboop",
				OperatorSessionId:    "sess-obs-1",
				Status:               string(constants.OperatorStatusActive),
			},
			{
				Id:                   "ztd-inference-operator",
				OperatorCapabilities: []string{string(constants.OperatorCapabilityInference)},
				OperatorType:         string(constants.OperatorTypeRemote),
				CurrentHostname:      "beepboop",
				OperatorSessionId:    "sess-inf-1",
				Status:               string(constants.OperatorStatusActive),
			},
			{
				Id:                   "old-stopped-op",
				OperatorCapabilities: []string{string(constants.OperatorCapabilityData)},
				OperatorType:         string(constants.OperatorTypeRemote),
				Status:               string(constants.OperatorStatusStopped),
			},
		},
	})

	mockClient := &statusMockClient{
		responses: map[string][]byte{
			"/api/v1/health":             healthResp,
			constants.APIPaths.Operators: operatorsResp,
		},
	}

	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Gateway  ")
	assert.Contains(t, out, "Gateway  online (PID 9999)")
	assert.Contains(t, out, "Enrollments")
	assert.Contains(t, out, "OPERATOR")
	assert.Contains(t, out, "ztd-model-provenance-operator")
	assert.Contains(t, out, "provenance")
	assert.Contains(t, out, "ztd-provider-boundary-observer")
	assert.Contains(t, out, "observer")
	assert.Contains(t, out, "ztd-inference-operator")
	assert.Contains(t, out, "inference")
	assert.NotContains(t, out, "old-stopped-op")
}

func TestGatewayStatusCmd_ReportsNoConnectedOperatorsWhenEmpty(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	healthResp, _ := json.Marshal(models.HealthResponse{
		Status: constants.GatewayModeStatusOK,
		PID:    9999,
	})
	operatorsResp, _ := json.Marshal(models.OperatorSlotResponse{
		Success:   true,
		Operators: []*operatorv1.OperatorDocument{},
	})

	mockClient := &statusMockClient{
		responses: map[string][]byte{
			"/api/v1/health":             healthResp,
			constants.APIPaths.Operators: operatorsResp,
		},
	}

	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "Gateway  ")
	assert.Contains(t, out, "Enrollments")
	assert.Contains(t, out, "Operators  none")
}

func TestGatewayStatusCmd_ReportsEnrollmentSections(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	healthResp, _ := json.Marshal(models.HealthResponse{Status: constants.GatewayModeStatusOK, PID: 9999})
	operatorsResp, _ := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{}})
	pendingResp, _ := json.Marshal(models.PlatformEnrollmentPendingResponse{
		Requests: []models.PlatformEnrollmentPendingRequest{
			{RequestID: "req-pending", ComponentKind: models.PlatformComponentApplication, ComponentName: "pending-app", InstanceID: "app-pending-host", Hostname: "host", State: models.PlatformEnrollmentStatePending},
		},
	})
	enrolledResp, _ := json.Marshal(models.PlatformEnrollmentEnrolledResponse{
		Enrollments: []models.PlatformEnrollmentEnrolledRequest{
			{RequestID: "req-app", ComponentKind: models.PlatformComponentApplication, ComponentName: "my-app", InstanceID: "app-my-app-host", Hostname: "host", State: models.PlatformEnrollmentStateCompleted},
			{RequestID: "req-app-2", ComponentKind: models.PlatformComponentApplication, ComponentName: "other-app", InstanceID: "app-other-app-host", Hostname: "host", State: models.PlatformEnrollmentStateCompleted},
			{RequestID: "req-dash", ComponentKind: models.PlatformComponentDashboard, ComponentName: "ztdd", InstanceID: "dash-inst", Hostname: "host", State: models.PlatformEnrollmentStateCompleted},
			{RequestID: "req-revoked", ComponentKind: models.PlatformComponentApplication, ComponentName: "revoked-app", InstanceID: "app-revoked", Hostname: "host", State: models.PlatformEnrollmentStateRevoked},
			{RequestID: "req-op", ComponentKind: models.PlatformComponentOperator, ComponentName: "ztdo", InstanceID: "op-inst", Hostname: "host", State: models.PlatformEnrollmentStateCompleted},
		},
	})

	usersResp, _ := json.Marshal([]models.User{
		{ID: "user-1", Status: constants.UserStatusActive, Roles: []string{"admin"}, PasskeyCredentials: []models.PasskeyCredential{{}}},
	})

	mockClient := &statusMockClient{
		responses: map[string][]byte{
			"/api/v1/health":                                  healthResp,
			constants.APIPaths.Operators:                      operatorsResp,
			constants.APIPaths.AuthPlatformEnrollmentPending:  pendingResp,
			constants.APIPaths.AuthPlatformEnrollmentEnrolled: enrolledResp,
			constants.APIPaths.Users:                          usersResp,
		},
	}
	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, nil))

	out := buf.String()
	assert.Contains(t, out, "Enrollments  users 1 | pending 1 | apps 2 | dashboards 1")
	assert.NotContains(t, out, "·")
	assert.NotContains(t, out, "revoked-app")
	assert.NotContains(t, out, "op-inst")

}

func TestGatewayStatusCmd_ReportsEmptyEnrollmentSections(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	healthResp, _ := json.Marshal(models.HealthResponse{Status: constants.GatewayModeStatusOK, PID: 9999})
	operatorsResp, _ := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{}})
	pendingResp, _ := json.Marshal(models.PlatformEnrollmentPendingResponse{})
	enrolledResp, _ := json.Marshal(models.PlatformEnrollmentEnrolledResponse{})
	usersResp, _ := json.Marshal([]models.User{})

	mockClient := &statusMockClient{
		responses: map[string][]byte{
			"/api/v1/health":                                  healthResp,
			constants.APIPaths.Operators:                      operatorsResp,
			constants.APIPaths.AuthPlatformEnrollmentPending:  pendingResp,
			constants.APIPaths.AuthPlatformEnrollmentEnrolled: enrolledResp,
			constants.APIPaths.Users:                          usersResp,
		},
	}
	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, nil))
	out := buf.String()
	assert.Contains(t, out, "Enrollments  users 0 | pending 0 | apps 0 | dashboards 0")
	assert.Contains(t, out, "Operators  none")
}

func TestGatewayStatusCmd_EnrollmentListUnavailable(t *testing.T) {
	fileSvc, cfg := cmdtest.NewCmdTestEnv(t)

	healthResp, _ := json.Marshal(models.HealthResponse{Status: constants.GatewayModeStatusOK, PID: 9999})
	operatorsResp, _ := json.Marshal(models.OperatorSlotResponse{Success: true, Operators: []*operatorv1.OperatorDocument{}})

	mockClient := &statusMockClient{
		responses: map[string][]byte{
			"/api/v1/health":             healthResp,
			constants.APIPaths.Operators: operatorsResp,
		},
	}
	clientFactory := func(fs.RuntimeFileService, *config.Config) (authcmd.APIClient, error) {
		return mockClient, nil
	}

	cmd := gatewayStatusCmdWithConfig(cmdtest.ConfigLoaderFor(cfg), clientFactory, cmdtest.FileSvcFactoryFor(fileSvc))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	require.NoError(t, cmd.RunE(cmd, nil))
	out := buf.String()
	assert.Contains(t, out, "Enrollments  users ? | pending ? | apps ? | dashboards ?")

}

func TestGatewayLogsCmd_NoLogFileReturnsMessage(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayLogsCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "No log file found")
}

func TestGatewayCleanCmd_ForceFlagSkipsPrompt(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayCleanCmd()
	cmd.Flags().Set("force", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Clean complete")
}

func TestGatewayCleanCmd_AbortedOutputContainsNoDestructiveAction(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayCleanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.WriteString("\n")
	w.Close()
	t.Cleanup(func() { os.Stdin = originalStdin })

	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrConfirmationAborted)
	assert.Contains(t, buf.String(), "Aborted")
	assert.NotContains(t, buf.String(), "Clean complete")
}

func TestGatewayResetCmd_WarningMessagesPrintedBeforePrompt(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayResetCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.WriteString("n\n")
	w.Close()
	t.Cleanup(func() { os.Stdin = originalStdin })

	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrConfirmationAborted)
	output := buf.String()
	assert.Contains(t, output, "destructive operation")
	assert.Contains(t, output, "Stop all running ztd services")
	assert.Contains(t, output, "Rename the runtime directory (.ztd) aside")
	assert.Contains(t, output, "Aborted")
}

func TestGatewayCleanCmd_WarningMessagesPrintedBeforePrompt(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayCleanCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	originalStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.WriteString("n\n")
	w.Close()
	t.Cleanup(func() { os.Stdin = originalStdin })

	err := cmd.RunE(cmd, nil)
	require.ErrorIs(t, err, constants.ErrConfirmationAborted)
	output := buf.String()
	assert.Contains(t, output, "WARNING")
	assert.Contains(t, output, "Rename the runtime directory (.ztd) aside")
	assert.Contains(t, output, "Aborted")
}

func TestGatewayStatusCmd_StoppedOutputIsOneLine(t *testing.T) {
	setupGatewayTestEnv(t)

	cmd := gatewayStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := cmd.RunE(cmd, nil)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Gateway  ")
	assert.Equal(t, "Gateway  stopped\n", buf.String())
}

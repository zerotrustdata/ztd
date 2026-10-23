// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/auth"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestNewOperatorRuntimeFileServiceUsesWorkingDir(t *testing.T) {
	workingDir := testutil.TempDir(t)
	fileSvc, effectiveWorkDir, err := newOperatorRuntimeFileService(ServeOperatorOptions{
		WorkingDir: workingDir,
		LaunchDir:  testutil.TempDir(t),
	}, testLogger())
	require.NoError(t, err)
	assert.Equal(t, workingDir, effectiveWorkDir)
	assert.Equal(t, filepath.Join(workingDir, constants.RuntimeDirname), fileSvc.Resolve(""))
}

func TestOperatorFingerprintOptions_ProvenanceIncludesRuntimePort(t *testing.T) {
	opts := ServeOperatorOptions{OperatorCapabilities: constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}}

	got := operatorFingerprintOptions(opts, "/var/lib/ollama/models", "operator-user")

	assert.Equal(t, auth.FingerprintOptions{
		LocalDir:     "/var/lib/ollama/models",
		Account:      "operator-user",
		Port:         constants.Ports.OperatorHttp,
		Capabilities: constants.OperatorCapabilities{constants.OperatorCapabilityProvenance},
	}, got)
}

func TestServeOperatorOptions_ZeroValue(t *testing.T) {
	var opts ServeOperatorOptions

	assert.Equal(t, "", opts.LogLevel)
	assert.Equal(t, "", opts.Endpoint)
	assert.Equal(t, "", opts.TrustBundlePath)
	assert.Equal(t, "", opts.WorkingDir)
	assert.Equal(t, "", opts.LaunchDir)
	assert.False(t, opts.CloudMode)
	assert.Equal(t, "", opts.CloudProvider)
	assert.Equal(t, time.Duration(0), opts.HeartbeatInterval)
}

func TestServeOperatorOptions_FullAssignment(t *testing.T) {
	opts := ServeOperatorOptions{
		LogLevel:          "debug",
		Endpoint:          "192.168.1.10",
		TrustBundlePath:   constants.DefaultPKIDir + "/" + constants.PkiFileGatewayBundle,
		WorkingDir:        constants.DefaultDataDir,
		LaunchDir:         constants.DefaultPKIDir,
		CloudMode:         true,
		CloudProvider:     "aws",
		HeartbeatInterval: 30 * time.Second,
	}

	assert.Equal(t, "debug", opts.LogLevel)
	assert.Equal(t, "192.168.1.10", opts.Endpoint)
	assert.Equal(t, constants.DefaultPKIDir+"/"+constants.PkiFileGatewayBundle, opts.TrustBundlePath)
	assert.Equal(t, constants.DefaultDataDir, opts.WorkingDir)
	assert.Equal(t, constants.DefaultPKIDir, opts.LaunchDir)
	assert.True(t, opts.CloudMode)
	assert.Equal(t, "aws", opts.CloudProvider)
	assert.Equal(t, 30*time.Second, opts.HeartbeatInterval)
}

func TestServeOperatorOptions_Equality(t *testing.T) {
	a := ServeOperatorOptions{
		LogLevel:   "info",
		Endpoint:   constants.DefaultEndpoint,
		WorkingDir: constants.DefaultDataDir,
		LaunchDir:  constants.DefaultPKIDir,
		CloudMode:  false,
	}
	b := ServeOperatorOptions{
		LogLevel:   "info",
		Endpoint:   constants.DefaultEndpoint,
		WorkingDir: constants.DefaultDataDir,
		LaunchDir:  constants.DefaultPKIDir,
		CloudMode:  false,
	}
	c := a
	c.CloudMode = true

	require.Equal(t, a, b, "structs with identical fields should be equal")
	require.NotEqual(t, a, c, "structs differing in any field should not be equal")
}

func TestServeOperatorOptions_PartialAssignment(t *testing.T) {
	opts := ServeOperatorOptions{
		LogLevel: "info",
		Endpoint: "10.0.0.1",
	}

	assert.Equal(t, "info", opts.LogLevel)
	assert.Equal(t, "10.0.0.1", opts.Endpoint)
	assert.Equal(t, "", opts.TrustBundlePath, "unassigned TrustBundlePath should be zero value")
	assert.Equal(t, "", opts.WorkingDir, "unassigned WorkingDir should be zero value")
	assert.Equal(t, "", opts.LaunchDir, "unassigned LaunchDir should be zero value")
	assert.False(t, opts.CloudMode, "unassigned CloudMode should be false")
	assert.Equal(t, time.Duration(0), opts.HeartbeatInterval, "unassigned HeartbeatInterval should be zero")
}

func TestServeOperatorOptions_HeartbeatInterval(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		expected time.Duration
	}{
		{"zero", 0, 0},
		{"one second", 1 * time.Second, 1 * time.Second},
		{"thirty seconds", 30 * time.Second, 30 * time.Second},
		{"one minute", time.Minute, 60 * time.Second},
		{"five minutes", 5 * time.Minute, 300 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := ServeOperatorOptions{HeartbeatInterval: tt.duration}
			assert.Equal(t, tt.expected, opts.HeartbeatInterval)
		})
	}
}

func TestServeOperatorOptions_CloudProviderValues(t *testing.T) {
	providers := []string{"aws", "gcp", "azure", ""}

	for _, p := range providers {
		opts := ServeOperatorOptions{CloudMode: true, CloudProvider: p}
		assert.Equal(t, p, opts.CloudProvider)
		assert.True(t, opts.CloudMode)
	}
}

func TestResolveOperatorGateway(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		httpPort int
		wantHost string
		wantPort int
	}{
		{"empty string returns default", "", 0, constants.DefaultEndpoint, 0},
		{"whitespace only returns default", " \t\r\n ", 0, constants.DefaultEndpoint, 0},
		{"empty keeps flag port", "", 9080, constants.DefaultEndpoint, 9080},
		{"ip address", "192.168.1.10", 0, "192.168.1.10", 0},
		{"surrounding whitespace trimmed", "\n\t  10.0.0.1  \r\n", 0, "10.0.0.1", 0},
		{"fqdn keeps flag port", "operator.example.com", 9080, "operator.example.com", 9080},
		{"hostname with port", "gateway.local:8080", 0, "gateway.local", 8080},
		{"endpoint port matches flag", "gateway.local:8080", 8080, "gateway.local", 8080},
		{"bare ipv6", "::1", 0, "::1", 0},
		{"bracketed ipv6", "[::1]", 0, "::1", 0},
		{"bracketed ipv6 with port", "[::1]:8080", 0, "::1", 8080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, err := resolveOperatorGateway(tt.endpoint, tt.httpPort)
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, host)
			assert.Equal(t, tt.wantPort, port)
		})
	}
}

func TestResolveOperatorGateway_Rejects(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		httpPort int
	}{
		{"http scheme", "http://gateway.local:8080", 0},
		{"https scheme", "https://gateway.local", 0},
		{"empty host", ":8080", 0},
		{"non-numeric port", "gateway.local:http", 0},
		{"port out of range", "gateway.local:70000", 0},
		{"port zero", "gateway.local:0", 0},
		{"port conflicts with flag", "gateway.local:8080", 9080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolveOperatorGateway(tt.endpoint, tt.httpPort)
			require.ErrorIs(t, err, constants.ErrGatewayEndpointInvalid)
		})
	}
}

func TestResolveWorkingDir(t *testing.T) {
	tests := []struct {
		name       string
		workingDir string
		launchDir  string
		expected   string
	}{
		{"working dir set, launch dir set", constants.DefaultDataDir, constants.DefaultPKIDir, constants.DefaultDataDir},
		{"working dir set, launch dir empty", constants.DefaultDataDir, "", constants.DefaultDataDir},
		{"working dir empty, launch dir set", "", constants.DefaultPKIDir, constants.DefaultPKIDir},
		{"both empty", "", "", ""},
		{"working dir takes precedence", constants.RuntimeDirname, constants.PkiDirname, constants.RuntimeDirname},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolveWorkingDir(tt.workingDir, tt.launchDir)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ---------------------------------------------------------------------------
// ServeOperatorOptions — additional edge cases
// ---------------------------------------------------------------------------

func TestServeOperatorOptions_NegativeHeartbeatInterval(t *testing.T) {
	opts := ServeOperatorOptions{HeartbeatInterval: -5 * time.Second}
	assert.Equal(t, -5*time.Second, opts.HeartbeatInterval,
		"negative durations should be stored as-is; validation is the caller's responsibility")
}

func TestServeOperatorOptions_AllBooleansTrue(t *testing.T) {
	opts := ServeOperatorOptions{
		CloudMode: true,
	}
	assert.True(t, opts.CloudMode)
}

func TestServeOperatorOptions_AllBooleansFalse(t *testing.T) {
	opts := ServeOperatorOptions{
		CloudMode: false,
	}
	assert.False(t, opts.CloudMode)
}

func TestServeOperatorOptions_Equality_DifferInEachField(t *testing.T) {
	base := ServeOperatorOptions{
		LogLevel:          "info",
		Endpoint:          constants.DefaultEndpoint,
		TrustBundlePath:   constants.DefaultPKIDir + "/" + constants.PkiFileGatewayBundle,
		WorkingDir:        constants.DefaultDataDir,
		LaunchDir:         constants.DefaultPKIDir,
		CloudMode:         true,
		CloudProvider:     "aws",
		HeartbeatInterval: 30 * time.Second,
	}

	fields := []struct {
		name string
		mut  func(o *ServeOperatorOptions)
	}{
		{"LogLevel", func(o *ServeOperatorOptions) { o.LogLevel = "debug" }},
		{"Endpoint", func(o *ServeOperatorOptions) { o.Endpoint = "other" }},
		{"TrustBundlePath", func(o *ServeOperatorOptions) { o.TrustBundlePath = "/other" }},
		{"WorkingDir", func(o *ServeOperatorOptions) { o.WorkingDir = "/other" }},
		{"LaunchDir", func(o *ServeOperatorOptions) { o.LaunchDir = "/other" }},
		{"CloudMode", func(o *ServeOperatorOptions) { o.CloudMode = false }},
		{"CloudProvider", func(o *ServeOperatorOptions) { o.CloudProvider = "gcp" }},
		{"HeartbeatInterval", func(o *ServeOperatorOptions) { o.HeartbeatInterval = 60 * time.Second }},
	}

	for _, f := range fields {
		t.Run(f.name, func(t *testing.T) {
			modified := base
			f.mut(&modified)
			require.NotEqual(t, base, modified,
				"structs differing in %s should not be equal", f.name)
		})
	}
}

func TestServeOperatorOptions_Equality_AllFieldsEqual(t *testing.T) {
	a := ServeOperatorOptions{
		LogLevel:          "debug",
		Endpoint:          "10.0.0.1",
		TrustBundlePath:   constants.DefaultPKIDir + "/" + constants.PkiFileGatewayBundle,
		WorkingDir:        constants.DefaultDataDir,
		LaunchDir:         constants.DefaultPKIDir,
		CloudMode:         true,
		CloudProvider:     "aws",
		HeartbeatInterval: 45 * time.Second,
	}
	b := a
	require.Equal(t, a, b, "structs with all fields identical should be equal")
}

// ---------------------------------------------------------------------------
// buildOperatorLoadOptions
// ---------------------------------------------------------------------------

func TestBuildOperatorLoadOptions_BasicMapping(t *testing.T) {
	opts := ServeOperatorOptions{
		LogLevel:          "debug",
		CloudMode:         true,
		CloudProvider:     "aws",
		HeartbeatInterval: 45 * time.Second,
	}

	loadOpts := buildOperatorLoadOptions(opts, "10.0.0.1", "/work/dir")

	assert.Equal(t, "10.0.0.1", loadOpts.OperatorEndpoint)
	assert.Equal(t, 0, loadOpts.HTTPPort)
	assert.Equal(t, 0, loadOpts.HTTPSPort)
	assert.True(t, loadOpts.CloudMode)
	assert.Equal(t, "aws", loadOpts.CloudProvider)
	assert.Equal(t, "debug", loadOpts.LogLevel)
	assert.Equal(t, "/work/dir", loadOpts.WorkDir)
	assert.Equal(t, 45*time.Second, loadOpts.HeartbeatInterval)
}

func TestBuildOperatorLoadOptions_EmptyEndpoint(t *testing.T) {
	opts := ServeOperatorOptions{}
	loadOpts := buildOperatorLoadOptions(opts, "", "/work")
	assert.Equal(t, "", loadOpts.OperatorEndpoint)
	assert.Equal(t, "/work", loadOpts.WorkDir)
}

func TestBuildOperatorLoadOptions_ZeroValues(t *testing.T) {
	opts := ServeOperatorOptions{}
	loadOpts := buildOperatorLoadOptions(opts, constants.DefaultEndpoint, constants.DefaultPKIDir)

	assert.False(t, loadOpts.CloudMode)
	assert.Equal(t, "", loadOpts.CloudProvider)
	assert.Equal(t, time.Duration(0), loadOpts.HeartbeatInterval)
	assert.Equal(t, "", loadOpts.PKIDir)
	assert.Equal(t, "", loadOpts.SecretsDir)
}

func TestBuildOperatorLoadOptions_EnvVarsPropagated(t *testing.T) {
	t.Setenv(string(constants.EnvVar.Shell), "/bin/zsh")
	t.Setenv(string(constants.EnvVar.Lang), "en_US.UTF-8")
	t.Setenv(string(constants.EnvVar.Term), "xterm-256color")
	t.Setenv(string(constants.EnvVar.TZ), "America/Los_Angeles")

	opts := ServeOperatorOptions{}
	loadOpts := buildOperatorLoadOptions(opts, constants.DefaultEndpoint, constants.DefaultDataDir)

	assert.Equal(t, "/bin/zsh", loadOpts.Shell)
	assert.Equal(t, "en_US.UTF-8", loadOpts.Lang)
	assert.Equal(t, "xterm-256color", loadOpts.Term)
	assert.Equal(t, "America/Los_Angeles", loadOpts.TZ)
}

func TestBuildOperatorLoadOptions_GatewayPortsDefaultToZeroAndPassThroughWhenSet(t *testing.T) {
	loadOpts := buildOperatorLoadOptions(ServeOperatorOptions{}, "10.0.0.1", "/work")
	assert.Equal(t, 0, loadOpts.HTTPPort)
	assert.Equal(t, 0, loadOpts.HTTPSPort)

	loadOpts = buildOperatorLoadOptions(ServeOperatorOptions{HTTPPort: 9080, HTTPSPort: 9443}, "10.0.0.1", "/work")
	assert.Equal(t, 9080, loadOpts.HTTPPort)
	assert.Equal(t, 9443, loadOpts.HTTPSPort)
}

func TestBuildOperatorLoadOptions_PKIAndSecretsAlwaysEmpty(t *testing.T) {
	opts := ServeOperatorOptions{}
	loadOpts := buildOperatorLoadOptions(opts, constants.DefaultEndpoint, constants.DefaultDataDir)
	assert.Equal(t, "", loadOpts.PKIDir, "PKIDir should always be empty for operator mode")
	assert.Equal(t, "", loadOpts.SecretsDir, "SecretsDir should always be empty for operator mode")
}

func TestBuildOperatorLoadOptions_AllFieldsPopulated(t *testing.T) {
	t.Setenv(string(constants.EnvVar.Shell), "/bin/bash")
	t.Setenv(string(constants.EnvVar.Lang), "C.UTF-8")
	t.Setenv(string(constants.EnvVar.Term), "screen")
	t.Setenv(string(constants.EnvVar.TZ), "UTC")

	opts := ServeOperatorOptions{
		LogLevel:          "warn",
		CloudMode:         true,
		CloudProvider:     "gcp",
		HeartbeatInterval: 90 * time.Second,
	}

	loadOpts := buildOperatorLoadOptions(opts, "operator.internal:8443", "/custom/work")

	assert.Equal(t, "operator.internal:8443", loadOpts.OperatorEndpoint)
	assert.Equal(t, 0, loadOpts.HTTPPort)
	assert.Equal(t, 0, loadOpts.HTTPSPort)
	assert.True(t, loadOpts.CloudMode)
	assert.Equal(t, "gcp", loadOpts.CloudProvider)
	assert.Equal(t, "warn", loadOpts.LogLevel)
	assert.Equal(t, "/custom/work", loadOpts.WorkDir)
	assert.Equal(t, 90*time.Second, loadOpts.HeartbeatInterval)
	assert.Equal(t, "", loadOpts.PKIDir)
	assert.Equal(t, "", loadOpts.SecretsDir)
	assert.Equal(t, "/bin/bash", loadOpts.Shell)
	assert.Equal(t, "C.UTF-8", loadOpts.Lang)
	assert.Equal(t, "screen", loadOpts.Term)
	assert.Equal(t, "UTC", loadOpts.TZ)
}

func TestBuildOperatorLoadOptions_HeartbeatIntervalPropagated(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
	}{
		{"one second", 1 * time.Second},
		{"thirty seconds", 30 * time.Second},
		{"one minute", time.Minute},
		{"five minutes", 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := ServeOperatorOptions{HeartbeatInterval: tt.duration}
			loadOpts := buildOperatorLoadOptions(opts, constants.DefaultEndpoint, "/work")
			assert.Equal(t, tt.duration, loadOpts.HeartbeatInterval)
		})
	}
}

func TestBuildOperatorLoadOptions_EnvVarsUnset(t *testing.T) {
	t.Setenv(string(constants.EnvVar.Shell), "")
	t.Setenv(string(constants.EnvVar.Lang), "")
	t.Setenv(string(constants.EnvVar.Term), "")
	t.Setenv(string(constants.EnvVar.TZ), "")

	opts := ServeOperatorOptions{}
	loadOpts := buildOperatorLoadOptions(opts, constants.DefaultEndpoint, "/work")

	assert.Equal(t, "", loadOpts.Shell)
	assert.Equal(t, "", loadOpts.Lang)
	assert.Equal(t, "", loadOpts.Term)
	assert.Equal(t, "", loadOpts.TZ)
}

func TestClassifyConfigLoadError_NonPostureError_ReturnsConfigError(t *testing.T) {
	exitCode, actionable := classifyConfigLoadError(errors.New("some other config error"))
	assert.Equal(t, constants.ExitConfigError, exitCode,
		"all config.Load errors should return ExitConfigError")
	assert.Empty(t, actionable, "config errors should not produce an actionable message")
}

func TestClassifyConfigLoadError_NilError_ReturnsConfigError(t *testing.T) {
	exitCode, actionable := classifyConfigLoadError(nil)
	assert.Equal(t, constants.ExitConfigError, exitCode)
	assert.Empty(t, actionable)
}

func TestOperatorInstanceIDSeparatesSameHostAndRole(t *testing.T) {
	first := operatorInstanceID("host", constants.OperatorCapabilities{constants.OperatorCapabilityData}, "/fleet/op-00001")
	assert.Equal(t, first, operatorInstanceID("host", constants.OperatorCapabilities{constants.OperatorCapabilityData}, "/fleet/./op-00001"))
	assert.NotEqual(t, first, operatorInstanceID("host", constants.OperatorCapabilities{constants.OperatorCapabilityData}, "/fleet/op-00002"))
	assert.NotEqual(t, first, operatorInstanceID("host", constants.OperatorCapabilities{constants.OperatorCapabilityInference}, "/fleet/op-00001"))
	multi := operatorInstanceID("host", constants.OperatorCapabilities{constants.OperatorCapabilityInference, constants.OperatorCapabilityObserver}, "/fleet/op-00001")
	assert.NotContains(t, multi, ",")
	assert.Contains(t, multi, "inference_observer")
	assert.LessOrEqual(t, len(operatorInstanceID(strings.Repeat("h", 253), constants.OperatorCapabilities{constants.OperatorCapabilityProvenance}, "/fleet")), constants.PlatformEnrollmentMaxInstanceIDBytes)
}

func TestRefuseGatewayRuntime(t *testing.T) {
	ctx := context.Background()
	fileSvc, workDir, err := newOperatorRuntimeFileService(ServeOperatorOptions{WorkingDir: testutil.TempDir(t)}, testLogger())
	require.NoError(t, err)
	require.NoError(t, refuseGatewayRuntime(ctx, fileSvc, workDir))

	require.NoError(t, fileSvc.WriteFile(ctx, constants.GatewayRuntimeMarker, []byte("ca"), constants.PermFilePrivate))
	err = refuseGatewayRuntime(ctx, fileSvc, workDir)
	require.ErrorIs(t, err, constants.ErrOperatorGatewayRuntime)
	assert.Contains(t, err.Error(), "--working-dir")
}

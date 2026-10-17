// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
)

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestLoadGateway_PostureDefaultsToDoctrineAndRejectsUnknown(t *testing.T) {
	cfg, err := LoadGateway(GatewayOptions{AllowTestPortZero: true})
	require.NoError(t, err)
	assert.Equal(t, constants.DefaultPosture, cfg.Gateway.Posture)

	for _, posture := range constants.AllPostures {
		cfg, err := LoadGateway(GatewayOptions{Posture: posture, AllowTestPortZero: true})
		require.NoError(t, err)
		assert.Equal(t, posture, cfg.Gateway.Posture)
	}

	_, err = LoadGateway(GatewayOptions{Posture: constants.Posture("strict"), AllowTestPortZero: true})
	require.ErrorIs(t, err, constants.ErrInvalidPosture)
}

func TestLoad_Defaults(t *testing.T) {
	wantWorkDir, err := os.Getwd()
	require.NoError(t, err)

	cfg, err := Load(LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
		Posture:          constants.PostureDoctrine,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, constants.ComponentNameZTDO, cfg.ComponentName)
	assert.Equal(t, "ztd", cfg.ProjectID)
	assert.Equal(t, 25, cfg.MaxConcurrentTasks)
	// The operator uses the gateway-provided posture; it has no default of its own.
	assert.Equal(t, constants.PostureDoctrine, cfg.Posture)
	assert.Equal(t, 2048, cfg.MaxMemoryMB)
	assert.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
	assert.Equal(t, constants.Ports.OperatorHttp, cfg.HTTPPort)

	// WorkDir defaults to the project root when --working-dir is not supplied
	assert.Equal(t, wantWorkDir, cfg.WorkDir)
}

// TestLoad_NoPosture_Succeeds verifies that the operator no longer requires
// config posture to start. The operator reads posture per-envelope from
// GovernanceEnvelope.Posture at L4 verification time, so an empty config
// posture is acceptable. cfg.Posture is empty when none was supplied.
func TestLoad_NoPosture_Succeeds(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Empty(t, cfg.Posture, "cfg.Posture should be empty when no posture was supplied")
}

func TestLoad_FieldPassthrough(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
		Posture:          constants.PostureDoctrine,
	})
	require.NoError(t, err)

	assert.Equal(t, constants.DefaultEndpoint, cfg.Endpoint)
}

func TestLoad_HTTPPortOverride(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
		HTTPPort:         constants.Ports.OperatorHttps,
		Posture:          constants.PostureDoctrine,
	})
	require.NoError(t, err)
	assert.Equal(t, constants.Ports.OperatorHttps, cfg.HTTPPort)
}

func TestLoad_TLSServerName(t *testing.T) {
	tests := []struct {
		name           string
		endpoint       string
		wantServerName string
	}{
		{
			name:           "hostname clears TLSServerName",
			endpoint:       constants.DefaultEndpoint,
			wantServerName: "",
		},
		{
			name:           "IPv4 sets TLSServerName",
			endpoint:       "10.0.1.42",
			wantServerName: constants.GatewayInternalHostname,
		},
		{
			name:           "IPv6 sets TLSServerName",
			endpoint:       "::1",
			wantServerName: constants.GatewayInternalHostname,
		},
		{
			name:           "full IPv4 address sets TLSServerName",
			endpoint:       "192.168.100.200",
			wantServerName: constants.GatewayInternalHostname,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(LoadOptions{
				OperatorEndpoint: tt.endpoint,
				Posture:          constants.PostureDoctrine,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantServerName, cfg.TLSServerName)
		})
	}
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name        string
		opts        LoadOptions
		errContains string
	}{
		{
			name:        "missing Operator endpoint",
			opts:        LoadOptions{},
			errContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.opts)
			require.Error(t, err)
			assert.Nil(t, cfg)
			assert.Contains(t, err.Error(), tt.errContains)
		})
	}
}

// ---------------------------------------------------------------------------
// LoadGateway
// ---------------------------------------------------------------------------

func TestLoadGateway_Defaults(t *testing.T) {
	wantWorkDir, err := os.Getwd()
	require.NoError(t, err)

	cfg, err := LoadGateway(GatewayOptions{AllowTestPortZero: true})
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.True(t, cfg.Gateway.Enabled)

	assert.Equal(t, 0, cfg.Gateway.HTTPPort)
	assert.Equal(t, filepath.Join(wantWorkDir, ".ztd", "data"), cfg.Gateway.DataDir)
	assert.True(t, filepath.IsAbs(cfg.Gateway.DataDir))
	assert.Equal(t, constants.ComponentNameZTDOGateway, cfg.ComponentName)
}

func TestLoadGateway_ExplicitValues(t *testing.T) {
	cfg, err := LoadGateway(GatewayOptions{
		HTTPPort:          443,
		HTTPSPort:         constants.Ports.OperatorHttps,
		DataDir:           "/var/data",
		PKIDir:            "/var/pki",
		SecretsDir:        "/var/secrets",
		PasskeyRpID:       "example.com",
		PasskeyRpName:     "Example RP",
		AllowTestPortZero: true,
	})
	require.NoError(t, err)

	assert.Equal(t, 443, cfg.Gateway.HTTPPort)
	assert.Equal(t, constants.Ports.OperatorHttps, cfg.Gateway.HTTPSPort)
	assert.Equal(t, "/var/data", cfg.Gateway.DataDir)
	assert.Equal(t, "/var/pki", cfg.Gateway.PKIDir)
	assert.Equal(t, "/var/secrets", cfg.Gateway.SecretsDir)
}

func TestLoadGateway_PartialDefaults(t *testing.T) {

	t.Run("only data dir overridden", func(t *testing.T) {
		cfg, err := LoadGateway(GatewayOptions{DataDir: "/custom/data", AllowTestPortZero: true})
		require.NoError(t, err)

		assert.Equal(t, "/custom/data", cfg.Gateway.DataDir)
	})

	t.Run("no Operator fields set", func(t *testing.T) {
		cfg, err := LoadGateway(GatewayOptions{AllowTestPortZero: true})
		require.NoError(t, err)
		assert.Empty(t, cfg.Endpoint)
		assert.Empty(t, cfg.PubSubURL)
	})
}

func TestLoadGateway_SucceedsWithAllDefaults(t *testing.T) {
	_, err := LoadGateway(GatewayOptions{AllowTestPortZero: true})
	require.NoError(t, err)
}

func TestLoadGateway_RejectsPortZeroInProduction(t *testing.T) {

	t.Run("reject httpPort 0", func(t *testing.T) {
		_, err := LoadGateway(GatewayOptions{
			HTTPPort:          0,
			HTTPSPort:         constants.Ports.OperatorHttps,
			AllowTestPortZero: false,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "httpPort cannot be 0 in production")
	})

	t.Run("reject httpsPort 0", func(t *testing.T) {
		_, err := LoadGateway(GatewayOptions{
			HTTPPort:          constants.Ports.OperatorHttp,
			HTTPSPort:         0,
			AllowTestPortZero: false,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "httpsPort cannot be 0 in production")
	})

	t.Run("accept all non-zero ports in production", func(t *testing.T) {
		_, err := LoadGateway(GatewayOptions{
			HTTPPort:          constants.Ports.OperatorHttp,
			HTTPSPort:         constants.Ports.OperatorHttps,
			AllowTestPortZero: false,
		})
		require.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// HeartbeatInterval via Load
// ---------------------------------------------------------------------------

func TestLoad_HeartbeatIntervalDefault(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint: constants.DefaultEndpoint,
		Posture:          constants.PostureDoctrine,
	})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
}

func TestLoad_HeartbeatIntervalOverride(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint:  constants.DefaultEndpoint,
		HeartbeatInterval: 90 * time.Second,
		Posture:           constants.PostureDoctrine,
	})
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.HeartbeatInterval)
}

func TestLoad_HeartbeatIntervalZeroUsesDefault(t *testing.T) {
	cfg, err := Load(LoadOptions{
		OperatorEndpoint:  constants.DefaultEndpoint,
		HeartbeatInterval: 0,
		Posture:           constants.PostureDoctrine,
	})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, cfg.HeartbeatInterval)
}

// ---------------------------------------------------------------------------
// heartbeatIntervalOrDefault
// ---------------------------------------------------------------------------

func TestHeartbeatIntervalOrDefault(t *testing.T) {
	tests := []struct {
		input time.Duration
		want  time.Duration
	}{
		{0, 30 * time.Second},
		{-1 * time.Second, 30 * time.Second},
		{10 * time.Second, 10 * time.Second},
		{60 * time.Second, 60 * time.Second},
		{2 * time.Minute, 2 * time.Minute},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, heartbeatIntervalOrDefault(tt.input), "input=%v", tt.input)
	}
}

// ---------------------------------------------------------------------------
// httpPortOrDefault
// ---------------------------------------------------------------------------

func TestHTTPPortOrDefault(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{0, constants.Ports.OperatorHttp},
		{-1, constants.Ports.OperatorHttp},
		{1, 1},
		{constants.Ports.OperatorHttp, constants.Ports.OperatorHttp},
		{constants.Ports.OperatorHttps, constants.Ports.OperatorHttps},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, httpPortOrDefault(tt.input), "input=%d", tt.input)
	}
}

// ---------------------------------------------------------------------------
// tlsServerName
// ---------------------------------------------------------------------------

func TestTLSServerName(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{"hostname returns empty", constants.DefaultEndpoint, ""},
		{"plain hostname returns empty", "example.com", ""},
		{"IPv4 returns localhost", "10.0.0.1", constants.GatewayInternalHostname},
		{"IPv4 loopback returns localhost", "127.0.0.1", constants.GatewayInternalHostname},
		{"IPv6 loopback returns localhost", "::1", constants.GatewayInternalHostname},
		{"IPv6 full returns localhost", "2001:db8::1", constants.GatewayInternalHostname},
		{"IPv4-mapped IPv6 returns localhost", "::ffff:192.0.2.1", constants.GatewayInternalHostname},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, TLSServerName(tt.endpoint))
		})
	}
}

// Only non-internal endpoints are covered here: ztd.local triggers a DNS
// lookup, which Tier 1 tests must not perform.
func TestGatewayDialHost_PassesThroughOtherEndpoints(t *testing.T) {
	for _, endpoint := range []string{"example.com", "10.0.0.1", "::1", ""} {
		assert.Equal(t, endpoint, GatewayDialHost(endpoint))
	}
}

func TestBuildPubSubURL(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		httpPort int
		want     string
	}{
		{"hostname", "localhost", 0, "wss://localhost:8443"},
		{"IPv4", "192.168.1.1", 0, "wss://192.168.1.1:8443"},
		{"IPv6", "2001:db8::1", 0, "wss://[2001:db8::1]:8443"},
		{"custom port", "192.168.1.1", 9000, "wss://192.168.1.1:9000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, buildPubSubURL(tt.endpoint, tt.httpPort))
		})
	}
}

func TestListenHostOrDefault_LoopbackUnlessRequested(t *testing.T) {
	assert.Equal(t, constants.LocalhostIP, listenHostOrDefault(""))
	assert.Equal(t, "0.0.0.0", listenHostOrDefault("0.0.0.0"))
	assert.Equal(t, "192.168.1.10", listenHostOrDefault("192.168.1.10"))
}

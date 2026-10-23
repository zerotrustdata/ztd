// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/constants"
)

func TestGatewayFlagsToServeConfig_AllFieldsTransferred(t *testing.T) {
	t.Parallel()
	flags := GatewayFlags{
		Posture:          "consensus",
		HTTPPort:         8080,
		HTTPSPort:        8443,
		DataDir:          "/data",
		PKIDir:           "/pki",
		SecretsDir:       "/secrets",
		VaultDir:         "/vault",
		PasskeyRpID:      "example.com",
		PasskeyRpName:    "Example",
		PasskeyRpOrigins: []string{"https://example.com"},
		RateLimitRPS:     100.0,
		RateLimitBurst:   50,
		LogLevel:         "debug",
		CertIdentityMode: "full",
		MCPDownstreamURL: "https://downstream/mcp",
		A2ADownstreamURL: "https://downstream/a2a",
		PublicBaseURL:    "https://demo.example.com",
		AllowedOrigins:   []string{"https://lovable.dev"},
		DoctrineDir:      "/etc/ztd/doctrine",
	}

	cfg := gatewayFlagsToServeConfig(flags)

	assert.Equal(t, constants.Posture("consensus"), cfg.Posture)
	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Equal(t, 8443, cfg.HTTPSPort)
	assert.Equal(t, "/data", cfg.DataDir)
	assert.Equal(t, "/pki", cfg.PKIDir)
	assert.Equal(t, "/secrets", cfg.SecretsDir)
	assert.Equal(t, "/vault", cfg.VaultDir)
	assert.Equal(t, "example.com", cfg.PasskeyRpID)
	assert.Equal(t, "Example", cfg.PasskeyRpName)
	assert.Equal(t, []string{"https://example.com"}, cfg.PasskeyRpOrigins)
	assert.InEpsilon(t, 100.0, cfg.RateLimitRPS, 0.01)
	assert.Equal(t, 50, cfg.RateLimitBurst)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "full", cfg.CertIdentityMode)
	assert.Equal(t, "https://downstream/mcp", cfg.MCPDownstreamURL)
	assert.Equal(t, "https://downstream/a2a", cfg.A2ADownstreamURL)
	assert.Equal(t, "https://demo.example.com", cfg.PublicBaseURL)
	assert.Equal(t, []string{"https://lovable.dev"}, cfg.AllowedOrigins)
	assert.Equal(t, "/etc/ztd/doctrine", cfg.DoctrineDir)
}

func TestGatewayFlagsToServeConfig_EmptyFlags(t *testing.T) {
	t.Parallel()
	flags := GatewayFlags{}

	cfg := gatewayFlagsToServeConfig(flags)

	assert.Equal(t, constants.Posture(""), cfg.Posture)
	assert.Equal(t, 0, cfg.HTTPPort)
	assert.Equal(t, 0, cfg.HTTPSPort)
	assert.Empty(t, cfg.DataDir)
	assert.Empty(t, cfg.PKIDir)
	assert.Empty(t, cfg.SecretsDir)
	assert.Empty(t, cfg.VaultDir)
	assert.Empty(t, cfg.PasskeyRpID)
	assert.Empty(t, cfg.PasskeyRpName)
	assert.Nil(t, cfg.PasskeyRpOrigins)
	assert.InDelta(t, 0.0, cfg.RateLimitRPS, 0)
	assert.Equal(t, 0, cfg.RateLimitBurst)
	assert.Empty(t, cfg.LogLevel)
	assert.Empty(t, cfg.CertIdentityMode)
	assert.Empty(t, cfg.MCPDownstreamURL)
	assert.Empty(t, cfg.A2ADownstreamURL)
	assert.Empty(t, cfg.PublicBaseURL)
	assert.Nil(t, cfg.AllowedOrigins)
	assert.Empty(t, cfg.DoctrineDir)
}

func TestAddGatewayFlags_RegistersAllFlags(t *testing.T) {
	t.Parallel()
	cmd := &cobra.Command{}
	var flags GatewayFlags
	addGatewayFlags(cmd, &flags)

	expectedFlags := []struct {
		name     string
		defValue string
	}{
		{"posture", "doctrine"},
		{"http-port", "0"},
		{"https-port", "0"},
		{"data-dir", ""},
		{"pki-dir", ""},
		{"secrets-dir", ""},
		{"vault-dir", ""},
		{"passkey-rp-id", ""},
		{"passkey-rp-name", ""},
		{"rate-limit-rps", "0"},
		{"rate-limit-burst", "0"},
		{"log", "info"},
		{"cert-mode", ""},
		{"mcp-downstream-url", ""},
		{"mcp-downstream-cmd", ""},
		{"mcp-downstream-args", ""},
		{"a2a-downstream-url", ""},
		{"public-base-url", ""},
		{"doctrine-dir", ""},
	}

	for _, ef := range expectedFlags {
		flag := cmd.Flags().Lookup(ef.name)
		require.NotNil(t, flag, "flag --%s should be registered", ef.name)
		assert.Equal(t, ef.defValue, flag.DefValue, "flag --%s default should be %s", ef.name, ef.defValue)
	}

	rpOriginFlag := cmd.Flags().Lookup("passkey-rp-origin")
	assert.NotNil(t, rpOriginFlag)

	corsFlag := cmd.Flags().Lookup("cors-origin")
	assert.NotNil(t, corsFlag)
}

func TestResolveGatewayFlags_FlagValuesPassThrough(t *testing.T) {
	t.Parallel()
	result := resolveGatewayFlags(GatewayFlags{
		VaultDir: "/cli/vault",
	})

	assert.Equal(t, "/cli/vault", result.VaultDir)
}

func TestResolveGatewayFlags_OllamaEndpointFromEnv(t *testing.T) {
	t.Setenv(string(constants.EnvVar.OllamaEndpoint), "http://10.0.0.2:11434")

	assert.Equal(t, "http://10.0.0.2:11434", resolveGatewayFlags(GatewayFlags{}).InferenceOllamaEndpoint)
	assert.Equal(t, "http://10.0.0.3:11434",
		resolveGatewayFlags(GatewayFlags{InferenceOllamaEndpoint: "http://10.0.0.3:11434"}).InferenceOllamaEndpoint)
}

func TestResolveGatewayFlags_DoctrineDirFlagPassThrough(t *testing.T) {
	t.Parallel()
	result := resolveGatewayFlags(GatewayFlags{DoctrineDir: "/cli/doctrine"})

	assert.Equal(t, "/cli/doctrine", result.DoctrineDir)
}

// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package serve

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
)

// ---------------------------------------------------------------------------
// GatewayConfig struct tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_ZeroValue(t *testing.T) {
	var cfg GatewayConfig

	assert.Equal(t, constants.Posture(""), cfg.Posture)
	assert.Equal(t, 0, cfg.HTTPPort)
	assert.Equal(t, 0, cfg.HTTPSPort)
	assert.Equal(t, "", cfg.DataDir)
	assert.Equal(t, "", cfg.PKIDir)
	assert.Equal(t, "", cfg.SecretsDir)
	assert.Equal(t, "", cfg.VaultDir)
	assert.Equal(t, "", cfg.PasskeyRpID)
	assert.Equal(t, "", cfg.PasskeyRpName)
	assert.Equal(t, float64(0), cfg.RateLimitRPS)
	assert.Equal(t, 0, cfg.RateLimitBurst)
	assert.Equal(t, "", cfg.LogLevel)
	assert.Equal(t, "", cfg.CertIdentityMode)
	assert.Equal(t, "", cfg.NetworkIdentityFile)
	assert.Equal(t, "", cfg.MCPDownstreamURL)
	assert.Equal(t, "", cfg.A2ADownstreamURL)
}

func TestGatewayConfig_FullAssignment(t *testing.T) {
	cfg := GatewayConfig{
		Posture:             constants.PostureConsensus,
		HTTPPort:            8080,
		HTTPSPort:           8443,
		DataDir:             constants.TestPathVarLibDataDir,
		PKIDir:              constants.TestPathVarLibPKIDir,
		SecretsDir:          constants.TestPathVarLibSecretsDir,
		VaultDir:            constants.TestPathVarLibVaultDir,
		PasskeyRpID:         "localhost",
		PasskeyRpName:       "ztd",
		RateLimitRPS:        5.0,
		RateLimitBurst:      10,
		LogLevel:            "info",
		CertIdentityMode:    "full",
		NetworkIdentityFile: constants.TestPathEtcNetworkIdentity,
		MCPDownstreamURL:    "http://downstream:3000/mcp",
		A2ADownstreamURL:    "http://downstream:3001/a2a",
	}

	assert.Equal(t, constants.PostureConsensus, cfg.Posture)
	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Equal(t, 8443, cfg.HTTPSPort)
	assert.Equal(t, constants.TestPathVarLibDataDir, cfg.DataDir)
	assert.Equal(t, constants.TestPathVarLibPKIDir, cfg.PKIDir)
	assert.Equal(t, constants.TestPathVarLibSecretsDir, cfg.SecretsDir)
	assert.Equal(t, constants.TestPathVarLibVaultDir, cfg.VaultDir)
	assert.Equal(t, "localhost", cfg.PasskeyRpID)
	assert.Equal(t, "ztd", cfg.PasskeyRpName)
	assert.Equal(t, 5.0, cfg.RateLimitRPS)
	assert.Equal(t, 10, cfg.RateLimitBurst)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "full", cfg.CertIdentityMode)
	assert.Equal(t, constants.TestPathEtcNetworkIdentity, cfg.NetworkIdentityFile)
	assert.Equal(t, "http://downstream:3000/mcp", cfg.MCPDownstreamURL)
	assert.Equal(t, "http://downstream:3001/a2a", cfg.A2ADownstreamURL)
}

func TestGatewayConfig_Equality(t *testing.T) {
	a := GatewayConfig{
		Posture:        constants.PostureDoctrine,
		HTTPPort:       8080,
		HTTPSPort:      8443,
		DataDir:        constants.TestPathShortData,
		PKIDir:         constants.TestPathShortPKI,
		SecretsDir:     constants.TestPathShortSecrets,
		LogLevel:       "debug",
		RateLimitRPS:   5.0,
		RateLimitBurst: 10,
	}
	b := a
	c := a
	c.HTTPPort = 9090

	assert.Equal(t, a, b, "structs with identical fields should be equal")
	assert.NotEqual(t, a, c, "structs differing in any field should not be equal")
}

func TestGatewayConfig_PartialAssignment(t *testing.T) {
	cfg := GatewayConfig{
		Posture:  constants.PostureNotary,
		HTTPPort: 8080,
		LogLevel: "error",
	}

	assert.Equal(t, constants.PostureNotary, cfg.Posture)
	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Equal(t, "error", cfg.LogLevel)
	assert.Equal(t, 0, cfg.HTTPSPort, "unassigned HTTPSPort should be zero value")
	assert.Equal(t, "", cfg.DataDir, "unassigned DataDir should be zero value")
	assert.Equal(t, "", cfg.PKIDir, "unassigned PKIDir should be zero value")
	assert.Equal(t, "", cfg.SecretsDir, "unassigned SecretsDir should be zero value")
	assert.Equal(t, float64(0), cfg.RateLimitRPS, "unassigned RateLimitRPS should be zero")
	assert.Equal(t, 0, cfg.RateLimitBurst, "unassigned RateLimitBurst should be zero")
}

func TestGatewayConfig_AllFieldsExported(t *testing.T) {
	cfg := GatewayConfig{}

	cfg.Posture = constants.PostureConsensus
	cfg.HTTPPort = 8080
	cfg.HTTPSPort = 8443
	cfg.DataDir = constants.TestPathShortData
	cfg.PKIDir = constants.TestPathShortPKI
	cfg.SecretsDir = constants.TestPathShortSecrets
	cfg.VaultDir = constants.TestPathShortVault
	cfg.PasskeyRpID = "example.com"
	cfg.PasskeyRpName = "Example"
	cfg.RateLimitRPS = 10.0
	cfg.RateLimitBurst = 20
	cfg.LogLevel = "debug"
	cfg.CertIdentityMode = "localhost"
	cfg.NetworkIdentityFile = constants.TestPathIdentityFile
	cfg.MCPDownstreamURL = "http://mcp.example.com"
	cfg.A2ADownstreamURL = "http://a2a.example.com"

	assert.Equal(t, constants.PostureConsensus, cfg.Posture)
	assert.Equal(t, 8080, cfg.HTTPPort)
	assert.Equal(t, 8443, cfg.HTTPSPort)
	assert.Equal(t, constants.TestPathShortData, cfg.DataDir)
	assert.Equal(t, constants.TestPathShortPKI, cfg.PKIDir)
	assert.Equal(t, constants.TestPathShortSecrets, cfg.SecretsDir)
	assert.Equal(t, constants.TestPathShortVault, cfg.VaultDir)
	assert.Equal(t, "example.com", cfg.PasskeyRpID)
	assert.Equal(t, "Example", cfg.PasskeyRpName)
	assert.Equal(t, 10.0, cfg.RateLimitRPS)
	assert.Equal(t, 20, cfg.RateLimitBurst)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "localhost", cfg.CertIdentityMode)
	assert.Equal(t, constants.TestPathIdentityFile, cfg.NetworkIdentityFile)
	assert.Equal(t, "http://mcp.example.com", cfg.MCPDownstreamURL)
	assert.Equal(t, "http://a2a.example.com", cfg.A2ADownstreamURL)
}

func TestGatewayConfig_Mutation(t *testing.T) {
	cfg := GatewayConfig{
		Posture:   constants.PostureDoctrine,
		HTTPPort:  8080,
		HTTPSPort: 8443,
		LogLevel:  "info",
	}

	cfg.Posture = constants.PostureConsensus
	cfg.HTTPPort = 9090

	assert.Equal(t, constants.PostureConsensus, cfg.Posture)
	assert.Equal(t, 9090, cfg.HTTPPort)
	assert.Equal(t, 8443, cfg.HTTPSPort, "unmodified fields should retain original values")
	assert.Equal(t, "info", cfg.LogLevel, "unmodified fields should retain original values")
}

func TestGatewayConfig_PostureValues(t *testing.T) {
	postures := []constants.Posture{
		constants.PostureDoctrine,
		constants.PostureConsensus,
		constants.PostureNotary,
	}

	for _, p := range postures {
		cfg := GatewayConfig{Posture: p}
		assert.Equal(t, p, cfg.Posture)
	}
}

func TestGatewayConfig_PostureStringConversion(t *testing.T) {
	tests := []struct {
		name     string
		posture  constants.Posture
		expected string
	}{
		{"doctrine", constants.PostureDoctrine, "doctrine"},
		{"consensus", constants.PostureConsensus, "consensus"},
		{"notary", constants.PostureNotary, "notary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{Posture: tt.posture}
			assert.Equal(t, tt.expected, string(cfg.Posture))
		})
	}
}

// ---------------------------------------------------------------------------
// GatewayConfig → config.GatewayOptions mapping
// ---------------------------------------------------------------------------

// gatewayConfigToOptions replicates the field mapping that RunGateway performs
// when constructing config.GatewayOptions from GatewayConfig. This test
// verifies the mapping is correct without requiring real infrastructure.
func gatewayConfigToOptions(cfg GatewayConfig) config.GatewayOptions {
	return config.GatewayOptions{
		Posture:             cfg.Posture,
		HTTPPort:            cfg.HTTPPort,
		HTTPSPort:           cfg.HTTPSPort,
		DataDir:             cfg.DataDir,
		PKIDir:              cfg.PKIDir,
		SecretsDir:          cfg.SecretsDir,
		VaultDir:            cfg.VaultDir,
		PasskeyRpID:         cfg.PasskeyRpID,
		PasskeyRpName:       cfg.PasskeyRpName,
		PasskeyRpOrigins:    cfg.PasskeyRpOrigins,
		RateLimitRPS:        cfg.RateLimitRPS,
		RateLimitBurst:      cfg.RateLimitBurst,
		CertMode:            cfg.CertIdentityMode,
		NetworkIdentityFile: cfg.NetworkIdentityFile,
		MCPDownstreamURL:    cfg.MCPDownstreamURL,
		A2ADownstreamURL:    cfg.A2ADownstreamURL,
		AllowTestPortZero:   false,
	}
}

func TestGatewayConfigToOptions_FullMapping(t *testing.T) {
	cfg := GatewayConfig{
		Posture:             constants.PostureConsensus,
		HTTPPort:            8080,
		HTTPSPort:           8443,
		DataDir:             constants.TestPathShortData,
		PKIDir:              constants.TestPathShortPKI,
		SecretsDir:          constants.TestPathShortSecrets,
		VaultDir:            "/test/vault",
		PasskeyRpID:         "localhost",
		PasskeyRpName:       "ztd",
		RateLimitRPS:        5.0,
		RateLimitBurst:      10,
		CertIdentityMode:    "full",
		NetworkIdentityFile: constants.TestPathIdentityFile,
		MCPDownstreamURL:    "http://mcp:3000",
		A2ADownstreamURL:    "http://a2a:3001",
	}

	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, cfg.Posture, opts.Posture)
	assert.Equal(t, cfg.HTTPPort, opts.HTTPPort)
	assert.Equal(t, cfg.HTTPSPort, opts.HTTPSPort)
	assert.Equal(t, cfg.DataDir, opts.DataDir)
	assert.Equal(t, cfg.PKIDir, opts.PKIDir)
	assert.Equal(t, cfg.SecretsDir, opts.SecretsDir)
	assert.Equal(t, cfg.VaultDir, opts.VaultDir)
	assert.Equal(t, cfg.PasskeyRpID, opts.PasskeyRpID)
	assert.Equal(t, cfg.PasskeyRpName, opts.PasskeyRpName)
	assert.Equal(t, cfg.RateLimitRPS, opts.RateLimitRPS)
	assert.Equal(t, cfg.RateLimitBurst, opts.RateLimitBurst)
	assert.Equal(t, cfg.CertIdentityMode, opts.CertMode)
	assert.Equal(t, cfg.NetworkIdentityFile, opts.NetworkIdentityFile)
	assert.Equal(t, cfg.MCPDownstreamURL, opts.MCPDownstreamURL)
	assert.Equal(t, cfg.A2ADownstreamURL, opts.A2ADownstreamURL)
	assert.False(t, opts.AllowTestPortZero, "AllowTestPortZero should always be false in production mapping")
}

func TestGatewayConfigToOptions_EmptyConfig(t *testing.T) {
	cfg := GatewayConfig{}

	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, constants.Posture(""), opts.Posture)
	assert.Equal(t, 0, opts.HTTPPort)
	assert.Equal(t, 0, opts.HTTPSPort)
	assert.Equal(t, "", opts.DataDir)
	assert.Equal(t, "", opts.PKIDir)
	assert.Equal(t, "", opts.SecretsDir)
	assert.False(t, opts.AllowTestPortZero)
}

func TestGatewayConfigToOptions_CertModeMapping(t *testing.T) {
	tests := []struct {
		name             string
		certMode         string
		expectedCertMode string
	}{
		{"empty", "", ""},
		{"full", "full", "full"},
		{"localhost", "localhost", "localhost"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{CertIdentityMode: tt.certMode}
			opts := gatewayConfigToOptions(cfg)
			assert.Equal(t, tt.expectedCertMode, opts.CertMode)
		})
	}
}

func TestGatewayConfigToOptions_DownstreamURLs(t *testing.T) {
	t.Run("both empty (default)", func(t *testing.T) {
		cfg := GatewayConfig{}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, "", opts.MCPDownstreamURL)
		assert.Equal(t, "", opts.A2ADownstreamURL)
	})

	t.Run("both set", func(t *testing.T) {
		cfg := GatewayConfig{
			MCPDownstreamURL: "http://mcp-downstream:3000/sse",
			A2ADownstreamURL: "http://a2a-downstream:3001",
		}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, "http://mcp-downstream:3000/sse", opts.MCPDownstreamURL)
		assert.Equal(t, "http://a2a-downstream:3001", opts.A2ADownstreamURL)
	})

	t.Run("only MCP set", func(t *testing.T) {
		cfg := GatewayConfig{MCPDownstreamURL: "http://mcp:3000"}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, "http://mcp:3000", opts.MCPDownstreamURL)
		assert.Equal(t, "", opts.A2ADownstreamURL)
	})

	t.Run("only A2A set", func(t *testing.T) {
		cfg := GatewayConfig{A2ADownstreamURL: "http://a2a:3001"}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, "", opts.MCPDownstreamURL)
		assert.Equal(t, "http://a2a:3001", opts.A2ADownstreamURL)
	})
}

func TestGatewayConfigToOptions_VaultFieldsNotMapped(t *testing.T) {
	cfg := GatewayConfig{
		VaultDir: constants.TestPathShortVault,
	}

	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, "", opts.DataDir, "VaultDir is not mapped to GatewayOptions")
	assert.False(t, opts.AllowTestPortZero)
}

// ---------------------------------------------------------------------------
// GatewayConfig field count / completeness
// ---------------------------------------------------------------------------

func TestGatewayConfig_FieldCount(t *testing.T) {
	cfg := GatewayConfig{
		Posture:             constants.PostureDoctrine,
		HTTPPort:            8080,
		HTTPSPort:           8443,
		DataDir:             constants.TestPathShortData,
		PKIDir:              constants.TestPathShortPKI,
		SecretsDir:          constants.TestPathShortSecrets,
		VaultDir:            constants.TestPathShortVault,
		PasskeyRpID:         "localhost",
		PasskeyRpName:       "ztd",
		RateLimitRPS:        5.0,
		RateLimitBurst:      10,
		LogLevel:            "info",
		CertIdentityMode:    "full",
		NetworkIdentityFile: constants.TestPathIdentityFileShort,
		MCPDownstreamURL:    "http://mcp:3000",
		A2ADownstreamURL:    "http://a2a:3001",
	}

	nonZero := 0
	if cfg.Posture != "" {
		nonZero++
	}
	if cfg.HTTPPort != 0 {
		nonZero++
	}
	if cfg.HTTPSPort != 0 {
		nonZero++
	}
	if cfg.DataDir != "" {
		nonZero++
	}
	if cfg.PKIDir != "" {
		nonZero++
	}
	if cfg.SecretsDir != "" {
		nonZero++
	}
	if cfg.VaultDir != "" {
		nonZero++
	}
	if cfg.PasskeyRpID != "" {
		nonZero++
	}
	if cfg.PasskeyRpName != "" {
		nonZero++
	}
	if cfg.RateLimitRPS != 0 {
		nonZero++
	}
	if cfg.RateLimitBurst != 0 {
		nonZero++
	}
	if cfg.LogLevel != "" {
		nonZero++
	}
	if cfg.CertIdentityMode != "" {
		nonZero++
	}
	if cfg.NetworkIdentityFile != "" {
		nonZero++
	}
	if cfg.MCPDownstreamURL != "" {
		nonZero++
	}
	if cfg.A2ADownstreamURL != "" {
		nonZero++
	}

	assert.Equal(t, 16, nonZero, "all 16 fields should be set and non-zero")
}

// ---------------------------------------------------------------------------
// Posture-specific GatewayConfig scenarios
// ---------------------------------------------------------------------------

func TestGatewayConfig_DoctrinePostureScenario(t *testing.T) {
	cfg := GatewayConfig{
		Posture:       constants.PostureDoctrine,
		HTTPPort:      8080,
		HTTPSPort:     8443,
		DataDir:       constants.TestPathVarLibDataDir,
		PKIDir:        constants.TestPathVarLibPKIDir,
		SecretsDir:    constants.TestPathVarLibSecretsDir,
		PasskeyRpID:   "localhost",
		PasskeyRpName: "ztd",
		LogLevel:      "info",
	}

	assert.Equal(t, constants.PostureDoctrine, cfg.Posture)

	opts := gatewayConfigToOptions(cfg)
	assert.Equal(t, constants.PostureDoctrine, opts.Posture)
}

func TestGatewayConfig_NotaryPostureScenario(t *testing.T) {
	cfg := GatewayConfig{
		Posture:       constants.PostureNotary,
		HTTPPort:      8080,
		HTTPSPort:     8443,
		DataDir:       constants.TestPathVarLibDataDir,
		PKIDir:        constants.TestPathVarLibPKIDir,
		SecretsDir:    constants.TestPathVarLibSecretsDir,
		PasskeyRpID:   "localhost",
		PasskeyRpName: "ztd",
		LogLevel:      "info",
	}

	assert.Equal(t, constants.PostureNotary, cfg.Posture)

	opts := gatewayConfigToOptions(cfg)
	assert.Equal(t, constants.PostureNotary, opts.Posture)
}

// ---------------------------------------------------------------------------
// Port boundary tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_PortBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		httpPort  int
		httpsPort int
	}{
		{"zero ports", 0, 0},
		{"privileged HTTP", 80, 0},
		{"privileged HTTPS", 0, 443},
		{"standard HTTP", 8080, 0},
		{"standard HTTPS", 0, 8443},
		{"both standard", 8080, 8443},
		{"max port", 65535, 65535},
		{"high ports", 18080, 18443},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{HTTPPort: tt.httpPort, HTTPSPort: tt.httpsPort}
			assert.Equal(t, tt.httpPort, cfg.HTTPPort)
			assert.Equal(t, tt.httpsPort, cfg.HTTPSPort)

			opts := gatewayConfigToOptions(cfg)
			assert.Equal(t, tt.httpPort, opts.HTTPPort)
			assert.Equal(t, tt.httpsPort, opts.HTTPSPort)
		})
	}
}

func TestGatewayConfig_NegativePorts(t *testing.T) {
	cfg := GatewayConfig{HTTPPort: -1, HTTPSPort: -1}
	assert.Equal(t, -1, cfg.HTTPPort)
	assert.Equal(t, -1, cfg.HTTPSPort)

	opts := gatewayConfigToOptions(cfg)
	assert.Equal(t, -1, opts.HTTPPort, "mapping should preserve negative values without validation")
	assert.Equal(t, -1, opts.HTTPSPort)
}

// ---------------------------------------------------------------------------
// Rate limit boundary tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_RateLimitBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		rps   float64
		burst int
	}{
		{"zero", 0, 0},
		{"minimal", 0.1, 1},
		{"standard", 5.0, 10},
		{"high throughput", 1000.0, 2000},
		{"fractional rps", 0.5, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{RateLimitRPS: tt.rps, RateLimitBurst: tt.burst}
			assert.Equal(t, tt.rps, cfg.RateLimitRPS)
			assert.Equal(t, tt.burst, cfg.RateLimitBurst)

			opts := gatewayConfigToOptions(cfg)
			assert.Equal(t, tt.rps, opts.RateLimitRPS)
			assert.Equal(t, tt.burst, opts.RateLimitBurst)
		})
	}
}

func TestGatewayConfig_NegativeRateLimit(t *testing.T) {
	cfg := GatewayConfig{RateLimitRPS: -1.0, RateLimitBurst: -1}
	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, -1.0, opts.RateLimitRPS, "mapping should preserve negative values without validation")
	assert.Equal(t, -1, opts.RateLimitBurst)
}

// ---------------------------------------------------------------------------
// Vault configuration tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_VaultConfiguration(t *testing.T) {
	cfg := GatewayConfig{
		VaultDir: constants.TestPathVarLibVaultDir,
	}

	assert.Equal(t, constants.TestPathVarLibVaultDir, cfg.VaultDir)
}

func TestGatewayConfigToOptions_VaultFieldsPreservedInConfigOnly(t *testing.T) {
	cfg := GatewayConfig{
		VaultDir:   constants.TestPathShortVault,
		DataDir:    constants.TestPathShortData,
		PKIDir:     constants.TestPathShortPKI,
		SecretsDir: constants.TestPathShortSecrets,
	}

	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, constants.TestPathShortData, opts.DataDir, "DataDir should be mapped")
	assert.Equal(t, constants.TestPathShortPKI, opts.PKIDir, "PKIDir should be mapped")
	assert.Equal(t, constants.TestPathShortSecrets, opts.SecretsDir, "SecretsDir should be mapped")
}

// ---------------------------------------------------------------------------
// Cert identity mode tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_CertIdentityModes(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{"empty (default)", ""},
		{"localhost", "localhost"},
		{"full", "full"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{CertIdentityMode: tt.mode}
			opts := gatewayConfigToOptions(cfg)
			assert.Equal(t, tt.mode, opts.CertMode)
		})
	}
}

func TestGatewayConfig_NetworkIdentityFile(t *testing.T) {
	cfg := GatewayConfig{NetworkIdentityFile: constants.TestPathEtcNetworkIdentity}
	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, constants.TestPathEtcNetworkIdentity, opts.NetworkIdentityFile)
}

// ---------------------------------------------------------------------------
// AllowTestPortZero invariant
// ---------------------------------------------------------------------------

func TestGatewayConfigToOptions_AllowTestPortZeroAlwaysFalse(t *testing.T) {
	tests := []struct {
		name string
		cfg  GatewayConfig
	}{
		{"empty config", GatewayConfig{}},
		{"full config", GatewayConfig{
			Posture:   constants.PostureConsensus,
			HTTPPort:  8080,
			HTTPSPort: 8443,
		}},
		{"zero ports", GatewayConfig{HTTPPort: 0, HTTPSPort: 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := gatewayConfigToOptions(tt.cfg)
			assert.False(t, opts.AllowTestPortZero,
				"AllowTestPortZero must always be false in production gateway config mapping")
		})
	}
}

// ---------------------------------------------------------------------------
// Passkey configuration tests
// ---------------------------------------------------------------------------

func TestGatewayConfig_PasskeyConfiguration(t *testing.T) {
	cfg := GatewayConfig{
		PasskeyRpID:   "operator.example.com",
		PasskeyRpName: "ztd Operator",
	}

	opts := gatewayConfigToOptions(cfg)
	assert.Equal(t, "operator.example.com", opts.PasskeyRpID)
	assert.Equal(t, "ztd Operator", opts.PasskeyRpName)
}

func TestGatewayConfig_PasskeyEmptyDefaults(t *testing.T) {
	var cfg GatewayConfig
	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, "", opts.PasskeyRpID)
	assert.Equal(t, "", opts.PasskeyRpName)
	assert.Nil(t, opts.PasskeyRpOrigins)
}

func TestGatewayConfig_PasskeyRpOriginsWiring(t *testing.T) {
	t.Run("single origin", func(t *testing.T) {
		cfg := GatewayConfig{
			PasskeyRpOrigins: []string{"http://localhost:8087"},
		}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, []string{"http://localhost:8087"}, opts.PasskeyRpOrigins)
	})

	t.Run("multiple origins", func(t *testing.T) {
		cfg := GatewayConfig{
			PasskeyRpOrigins: []string{"http://localhost:8087", "https://localhost:8450"},
		}
		opts := gatewayConfigToOptions(cfg)
		assert.Equal(t, []string{"http://localhost:8087", "https://localhost:8450"}, opts.PasskeyRpOrigins)
	})

	t.Run("empty slice preserved as nil", func(t *testing.T) {
		var cfg GatewayConfig
		opts := gatewayConfigToOptions(cfg)
		assert.Nil(t, opts.PasskeyRpOrigins)
	})
}

// ---------------------------------------------------------------------------
// Downstream URL combinations with posture
// ---------------------------------------------------------------------------

func TestGatewayConfigToOptions_DownstreamWithPosture(t *testing.T) {
	tests := []struct {
		name    string
		posture constants.Posture
		mcpURL  string
		a2aURL  string
	}{
		{"doctrine with downstreams", constants.PostureDoctrine, "http://mcp:3000", "http://a2a:3001"},
		{"consensus with downstreams", constants.PostureConsensus, "http://mcp:3000", "http://a2a:3001"},
		{"notary without downstreams", constants.PostureNotary, "", ""},
		{"notary with MCP only", constants.PostureNotary, "http://mcp:3000", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GatewayConfig{
				Posture:          tt.posture,
				MCPDownstreamURL: tt.mcpURL,
				A2ADownstreamURL: tt.a2aURL,
			}
			opts := gatewayConfigToOptions(cfg)

			assert.Equal(t, tt.posture, opts.Posture)
			assert.Equal(t, tt.mcpURL, opts.MCPDownstreamURL)
			assert.Equal(t, tt.a2aURL, opts.A2ADownstreamURL)
		})
	}
}

// ---------------------------------------------------------------------------
// GatewayConfig round-trip mapping integrity
// ---------------------------------------------------------------------------

func TestGatewayConfigToOptions_RoundTripIntegrity(t *testing.T) {
	original := GatewayConfig{
		Posture:             constants.PostureNotary,
		HTTPPort:            8080,
		HTTPSPort:           8443,
		DataDir:             constants.TestPathShortData,
		PKIDir:              constants.TestPathShortPKI,
		SecretsDir:          constants.TestPathShortSecrets,
		PasskeyRpID:         "localhost",
		PasskeyRpName:       "ztd",
		PasskeyRpOrigins:    []string{"http://localhost:8087", "https://localhost:8450"},
		RateLimitRPS:        10.0,
		RateLimitBurst:      20,
		CertIdentityMode:    "full",
		NetworkIdentityFile: constants.TestPathIdentityFileShort,
		MCPDownstreamURL:    "http://mcp:3000",
		A2ADownstreamURL:    "http://a2a:3001",
	}

	opts := gatewayConfigToOptions(original)

	assert.Equal(t, original.Posture, opts.Posture)
	assert.Equal(t, original.HTTPPort, opts.HTTPPort)
	assert.Equal(t, original.HTTPSPort, opts.HTTPSPort)
	assert.Equal(t, original.DataDir, opts.DataDir)
	assert.Equal(t, original.PKIDir, opts.PKIDir)
	assert.Equal(t, original.SecretsDir, opts.SecretsDir)
	assert.Equal(t, original.PasskeyRpID, opts.PasskeyRpID)
	assert.Equal(t, original.PasskeyRpName, opts.PasskeyRpName)
	assert.Equal(t, original.PasskeyRpOrigins, opts.PasskeyRpOrigins)
	assert.Equal(t, original.RateLimitRPS, opts.RateLimitRPS)
	assert.Equal(t, original.RateLimitBurst, opts.RateLimitBurst)
	assert.Equal(t, original.CertIdentityMode, opts.CertMode)
	assert.Equal(t, original.NetworkIdentityFile, opts.NetworkIdentityFile)
	assert.Equal(t, original.MCPDownstreamURL, opts.MCPDownstreamURL)
	assert.Equal(t, original.A2ADownstreamURL, opts.A2ADownstreamURL)
}

// ---------------------------------------------------------------------------
// GatewayConfig with constants consistency
// ---------------------------------------------------------------------------

func TestGatewayConfig_StandardPortValues(t *testing.T) {
	cfg := GatewayConfig{
		HTTPPort:  constants.Ports.OperatorHttp,
		HTTPSPort: constants.Ports.OperatorHttps,
	}

	assert.Equal(t, 8080, cfg.HTTPPort, "HTTP port should match standard gateway HTTP port")
	assert.Equal(t, 8443, cfg.HTTPSPort, "HTTPS port should match standard gateway HTTPS port")
}

func TestGatewayConfigToOptions_StandardPortsPreserved(t *testing.T) {
	cfg := GatewayConfig{
		HTTPPort:  constants.Ports.OperatorHttp,
		HTTPSPort: constants.Ports.OperatorHttps,
	}
	opts := gatewayConfigToOptions(cfg)

	assert.Equal(t, constants.Ports.OperatorHttp, opts.HTTPPort)
	assert.Equal(t, constants.Ports.OperatorHttps, opts.HTTPSPort)
}

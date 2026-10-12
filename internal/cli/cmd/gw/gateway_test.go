// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/platform"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	ztdconfig "github.com/zerotrustdata/ztd/internal/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestGetBinaryName(t *testing.T) {
	t.Run("returns ./ztd on non-Windows platforms", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Skipping non-Windows test on Windows")
		}
		assert.Equal(t, constants.LocalBinaryName, getBinaryName())
	})

	t.Run("returns ./ztd.exe on Windows", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Skipping Windows test on non-Windows platform")
		}
		assert.Equal(t, constants.LocalBinaryNameWindows, getBinaryName())
	})
}

func TestResolveStartConfig(t *testing.T) {
	t.Run("uses CLI flag values when no environment variables set", func(t *testing.T) {
		cfg := resolveGatewayFlags(GatewayFlags{
			Posture:          "doctrine",
			HTTPPort:         8080,
			HTTPSPort:        8443,
			DataDir:          "/data",
			PKIDir:           "/pki",
			SecretsDir:       "/secrets",
			VaultDir:         "/vault",
			MasterKeyFile:    "/run/secrets/ztd_master_key",
			PasskeyRpID:      "rp-id",
			PasskeyRpName:    "rp-name",
			RateLimitRPS:     100.0,
			RateLimitBurst:   50,
			LogLevel:         "info",
			CertIdentityMode: "full",
			ConsensusID:      "",
			ConsensusURL:     "",
		})

		assert.Equal(t, "doctrine", cfg.Posture)
		assert.Equal(t, 8080, cfg.HTTPPort)
		assert.Equal(t, 8443, cfg.HTTPSPort)
		assert.Equal(t, "/data", cfg.DataDir)
		assert.Equal(t, "/pki", cfg.PKIDir)
		assert.Equal(t, "/secrets", cfg.SecretsDir)
		assert.Equal(t, "/vault", cfg.VaultDir)
		assert.Equal(t, "/run/secrets/ztd_master_key", cfg.MasterKeyFile)
		assert.Equal(t, "rp-id", cfg.PasskeyRpID)
		assert.Equal(t, "rp-name", cfg.PasskeyRpName)
		assert.InEpsilon(t, 100.0, cfg.RateLimitRPS, 0.01)
		assert.Equal(t, 50, cfg.RateLimitBurst)
		assert.Equal(t, "info", cfg.LogLevel)
		assert.Equal(t, "full", cfg.CertIdentityMode)
	})

}

func TestDetectIdentity(t *testing.T) {
	t.Run("detectIdentity requires integration testing with real network detector", func(t *testing.T) {
		// detectIdentity uses network.NewDetector which performs real network detection.
		// This function requires integration testing with the //go:build integration tag
		// to test actual network identity detection behavior.
		// Unit tests for this function would require mocking the network.Detector struct,
		// which is not an interface and cannot be easily mocked without refactoring.
		t.Skip("detectIdentity requires integration testing - see internal/services/network/identity_test.go for network detector tests")
	})
}

func TestReExecArgsMatchStartCmdFlags(t *testing.T) {
	tmpDir := testutil.TempDir(t)
	fileSvc, err := fs.NewRuntimeFileService(tmpDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

	pm, err := platform.NewProcessManager(fileSvc)
	require.NoError(t, err)

	opts := platform.OperatorStartOptions{
		GatewayConfig: serve.GatewayConfig{
			OperatorCapabilities:               constants.OperatorCapabilities{constants.OperatorCapabilityData, constants.OperatorCapabilityInference, constants.OperatorCapabilityProvenance, constants.OperatorCapabilityObserver},
			InferenceOllamaEndpoint:            "http://localhost:11434",
			InferenceKeepAlive:                 "-1",
			ProviderBoundaryObserverID:         "observer",
			ProvenanceOperatorID:               "provenance",
			ProvenanceOperatorModelStorageRoot: "/models",

			Posture:                           ztdconfig.GatewayPosture("doctrine"),
			HTTPPort:                          8080,
			HTTPSPort:                         8443,
			DataDir:                           "/data",
			PKIDir:                            "/pki",
			SecretsDir:                        "/secrets",
			VaultDir:                          "/vault",
			MasterKeyFile:                     "/run/secrets/key",
			PasskeyRpID:                       "localhost",
			PasskeyRpName:                     "ztd",
			PasskeyRpOrigins:                  []string{"http://localhost:8087", "https://localhost:8450"},
			RateLimitRPS:                      100.0,
			RateLimitBurst:                    50,
			LogLevel:                          "info",
			CertIdentityMode:                  "full",
			ConsensusID:                       "trib-1",
			ConsensusURL:                      "https://localhost:8443/consensus/v1/deliberate",
			ConsensusBootstrap:                "/etc/ztd/consensus-bootstrap.json",
			MCPDownstreamURL:                  "https://downstream.example.com/mcp",
			MCPDownstreamCmd:                  "npx",
			MCPDownstreamArgs:                 []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
			A2ADownstreamURL:                  "https://downstream.example.com/a2a",
			PublicBaseURL:                     "https://demo.zerotrustdata.io",
			AllowedOrigins:                    []string{"https://lovable.dev"},
			DoctrineDir:                       "/etc/ztd/doctrine",
			PublicSpectatorEnabled:            true,
			PublicSpectatorPrivateAddr:        "127.0.0.1:8081",
			PublicSpectatorPublicAddr:         "127.0.0.1:8082",
			PublicSpectatorAllowContainerBind: true,
			EvalExplorerAddr:                  "127.0.0.1:5173",
			EvalExplorerRoot:                  "/eval-explorer",
			PublicSpectatorTrustedProxyCIDRs:  []string{"172.28.0.1/32"},
			ListenHost:                        "0.0.0.0",
		},
	}

	args, err := pm.BuildReExecArgs(opts)
	require.NoError(t, err)

	// Extract flag names from the re-exec args (skip positional args "gw", "start", and "--follow")
	emittedFlags := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			flagName, _, _ := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
			emittedFlags[flagName] = true
			// Skip the value for non-boolean flags
			// (we don't know which are bools here, so we just skip the next arg
			// if it doesn't start with -- and isn't a positional)
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
		}
	}

	// Get all flags defined on gatewayStartCmd()
	startCmd := gatewayStartCmd()
	cobraFlags := startCmd.Flags()

	// Assert every emitted flag exists on the cobra command
	for flagName := range emittedFlags {
		if cobraFlags.Lookup(flagName) == nil {
			t.Errorf("re-exec emits --%s but gatewayStartCmd() has no such flag", flagName)
		}
	}

	// Assert every cobra flag is emitted when all options are populated,
	// except for UI-only flags that are not re-executed.
	skipReExec := map[string]bool{
		"interactive": true, // --interactive is a one-time UI flow, not re-executed
		"quiet":       true, // --quiet only controls parent CLI startup guidance
	}
	cobraFlags.VisitAll(func(f *pflag.Flag) {
		if skipReExec[f.Name] {
			return
		}
		if !emittedFlags[f.Name] {
			t.Errorf("gatewayStartCmd() defines --%s but BuildReExecArgs does not emit it", f.Name)
		}
	})
}

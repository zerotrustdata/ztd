// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package gw

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestTunnelCmdStructure(t *testing.T) {
	t.Parallel()
	t.Run("tunnel command has correct use", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelCmd()
		assert.Equal(t, "tunnel", cmd.Use)
		assert.NotEmpty(t, cmd.Short)
		assert.NotEmpty(t, cmd.Long)
	})

	t.Run("tunnel command has expected subcommands", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelCmd()
		require.NotNil(t, cmd)

		expectedSubcommands := []string{"create", "route-dns", "run", "status"}
		for _, subcmd := range expectedSubcommands {
			found := false
			for _, c := range cmd.Commands() {
				if c.Name() == subcmd {
					found = true
					break
				}
			}
			assert.True(t, found, "tunnel command should have %s subcommand", subcmd)
		}
	})
}

func TestTunnelCreateCmdFlags(t *testing.T) {
	t.Parallel()
	t.Run("create command has all expected flags", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelCreateCmd()
		require.NotNil(t, cmd)

		expectedFlags := []string{"name", "hostname", "config-dir", "https-port", "service", "ca-bundle", "origin-server-name", "skip-dns"}
		for _, flagName := range expectedFlags {
			flag := cmd.Flags().Lookup(flagName)
			assert.NotNil(t, flag, "tunnel create should have --%s flag", flagName)
		}
	})

	t.Run("create command name defaults to ztd", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelCreateCmd()
		flag := cmd.Flags().Lookup("name")
		require.NotNil(t, flag)
		assert.Equal(t, "ztd", flag.DefValue)
	})

	t.Run("origin-server-name without ca-bundle fails closed", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelCreateCmd()
		require.NoError(t, cmd.Flags().Set("origin-server-name", "gateway.corp.example"))
		err := cmd.RunE(cmd, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrValidationFailed)
	})
}

func TestTunnelRunCmdFlags(t *testing.T) {
	t.Parallel()
	t.Run("run command has all expected flags", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelRunCmd()
		require.NotNil(t, cmd)

		expectedFlags := []string{"name", "config-dir"}
		for _, flagName := range expectedFlags {
			flag := cmd.Flags().Lookup(flagName)
			assert.NotNil(t, flag, "tunnel run should have --%s flag", flagName)
		}
	})
}

func TestBuildTunnelRunArgs_ConfigPrecedesTunnelSubcommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		tunnel    string
		configDir string
		expected  []string
	}{
		{
			name:      "explicit config directory",
			tunnel:    "opendevops-feed",
			configDir: "/home/user/.cloudflared",
			expected:  []string{"--config", filepath.Join("/home/user/.cloudflared", "config.yml"), "tunnel", "run", "opendevops-feed"},
		},
		{
			name:     "default config discovery",
			tunnel:   "ztd",
			expected: []string{"tunnel", "run", "ztd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, buildTunnelRunArgs(tt.tunnel, tt.configDir))
		})
	}
}

func TestTunnelStatusCmdFlags(t *testing.T) {
	t.Parallel()
	t.Run("status command has all expected flags", func(t *testing.T) {
		t.Parallel()
		cmd := tunnelStatusCmd()
		require.NotNil(t, cmd)

		expectedFlags := []string{"hostname", "name"}
		for _, flagName := range expectedFlags {
			flag := cmd.Flags().Lookup(flagName)
			assert.NotNil(t, flag, "tunnel status should have --%s flag", flagName)
		}
	})
}

func TestGenerateTunnelConfig(t *testing.T) {
	t.Parallel()
	t.Run("generates config with noTLSVerify when no CA bundle", func(t *testing.T) {
		t.Parallel()
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://localhost:8443",
			"",
			"",
		)

		assert.Contains(t, config, "tunnel: abc123")
		assert.Contains(t, config, "credentials-file: /home/user/.cloudflared/abc123.json")
		assert.Contains(t, config, "hostname: console.zerotrustdata.io")
		assert.Contains(t, config, "service: https://localhost:8443")
		assert.Contains(t, config, "noTLSVerify: true")
		assert.Contains(t, config, "http2Origin: true")
		assert.Contains(t, config, "http_status:404")
	})

	t.Run("generates config with CA bundle and originServerName", func(t *testing.T) {
		t.Parallel()
		caBundle := filepath.Join(constants.RuntimeDirname, constants.PkiDirname, constants.PkiFileGatewayBundle)
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://localhost:8443",
			caBundle,
			"ztd.local",
		)

		assert.Contains(t, config, "originCaPool: "+caBundle)
		assert.Contains(t, config, "originServerName: ztd.local")
		assert.NotContains(t, config, "noTLSVerify")
	})

	t.Run("CA bundle without override defaults originServerName to the Gateway certificate SAN", func(t *testing.T) {
		t.Parallel()
		caBundle := filepath.Join(constants.RuntimeDirname, constants.PkiDirname, constants.PkiFileGatewayBundle)
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://192.168.1.50:8443",
			caBundle,
			"",
		)

		assert.Contains(t, config, "originCaPool: "+caBundle)
		assert.Contains(t, config, "originServerName: "+constants.GatewayInternalHostname)
		assert.NotContains(t, config, "noTLSVerify")
	})

	t.Run("explicit origin server name overrides the CA bundle default", func(t *testing.T) {
		t.Parallel()
		caBundle := filepath.Join(constants.RuntimeDirname, constants.PkiDirname, constants.PkiFileGatewayBundle)
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://192.168.1.50:8443",
			caBundle,
			"gateway.corp.example",
		)

		assert.Contains(t, config, "originServerName: gateway.corp.example")
		assert.NotContains(t, config, "originServerName: "+constants.GatewayInternalHostname)
	})

	t.Run("no CA bundle omits originServerName", func(t *testing.T) {
		t.Parallel()
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://localhost:8443",
			"",
			"",
		)

		assert.NotContains(t, config, "originServerName")
	})

	t.Run("uses custom HTTPS port", func(t *testing.T) {
		t.Parallel()
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"console.zerotrustdata.io",
			"https://localhost:9443",
			"",
			"",
		)

		assert.Contains(t, config, "service: https://localhost:9443")
	})

	t.Run("uses plain HTTP mirror service without TLS origin settings", func(t *testing.T) {
		t.Parallel()
		config := generateTunnelConfig(
			"abc123",
			"/home/user/.cloudflared/abc123.json",
			"feed.opendevops.ai",
			"http://localhost:8081",
			"",
			"",
		)

		assert.Contains(t, config, "service: http://localhost:8081")
		assert.NotContains(t, config, "originRequest:")
		assert.NotContains(t, config, "noTLSVerify")
		assert.NotContains(t, config, "http2Origin")
	})
}

func TestParseTunnelID(t *testing.T) {
	t.Parallel()
	t.Run("extracts UUID from create output", func(t *testing.T) {
		t.Parallel()
		output := []byte("Created tunnel ztd with id 12345678-1234-1234-1234-123456789abc")
		id := parseTunnelID(output)
		assert.Equal(t, "12345678-1234-1234-1234-123456789abc", id)
	})

	t.Run("returns empty for no UUID", func(t *testing.T) {
		t.Parallel()
		output := []byte("No tunnel created")
		id := parseTunnelID(output)
		assert.Empty(t, id)
	})
}

func TestCloudflaredAuthenticated(t *testing.T) {
	t.Parallel()
	t.Run("returns true when cert.pem exists", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		certPath := filepath.Join(tmpDir, "cert.pem")
		require.NoError(t, os.WriteFile(certPath, []byte("fake cert"), 0o600))

		assert.True(t, cloudflaredAuthenticated(tmpDir))
	})

	t.Run("returns false when cert.pem does not exist", func(t *testing.T) {
		t.Parallel()
		tmpDir := testutil.TempDir(t)
		assert.False(t, cloudflaredAuthenticated(tmpDir))
	})
}

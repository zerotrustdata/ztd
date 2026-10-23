// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package platform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zerotrustdata/ztd/internal/cli/serve"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestBuildReExecArgs(t *testing.T) {
	tmpDir := testutil.TempDir(t)

	fileSvc := newPlatformTestFileSvc(t, tmpDir)
	pm, err := NewProcessManager(fileSvc)
	require.NoError(t, err)

	tests := []struct {
		name        string
		opts        OperatorStartOptions
		needsDirs   bool
		wantSubstrs []string
	}{
		{
			name: "minimal",
			opts: OperatorStartOptions{
				GatewayConfig: serve.GatewayConfig{
					Posture:    constants.Posture("consensus"),
					HTTPPort:   8080,
					HTTPSPort:  8443,
					DataDir:    "/data",
					PKIDir:     "/pki",
					SecretsDir: "/secrets",
					LogLevel:   "info",
				},
			},
			wantSubstrs: []string{
				"--posture consensus",
				"--data-dir /data",
				"--pki-dir /pki",
				"--secrets-dir /secrets",
				"--http-port 8080",
				"--https-port 8443",
				"--log info",
			},
		},
		{
			name: "all options",
			opts: OperatorStartOptions{
				GatewayConfig: serve.GatewayConfig{
					Posture:          constants.Posture("notary"),
					HTTPPort:         9000,
					HTTPSPort:        9443,
					DataDir:          "/data",
					PKIDir:           "/pki",
					SecretsDir:       "/secrets",
					VaultDir:         "/vault",
					CertIdentityMode: "spiffe",
					MCPDownstreamURL: "http://mcp:8080",
					A2ADownstreamURL: "http://a2a:8081",
					PublicBaseURL:    "https://demo.zerotrustdata.io",
					PasskeyRpID:      "localhost",
					PasskeyRpName:    "ZTD",
					PasskeyRpOrigins: []string{"http://localhost:8080", "http://127.0.0.1:8080"},
					RateLimitRPS:     100.5,
					RateLimitBurst:   200,
					LogLevel:         "debug",
				},
			},
			wantSubstrs: []string{
				"--vault-dir /vault",
				"--mcp-downstream-url http://mcp:8080",
				"--a2a-downstream-url http://a2a:8081",
				"--public-base-url https://demo.zerotrustdata.io",
				"--passkey-rp-id localhost",
				"--passkey-rp-name ZTD",
				"--passkey-rp-origin http://localhost:8080",
				"--passkey-rp-origin http://127.0.0.1:8080",
				"--rate-limit-rps 100.5",
				"--rate-limit-burst 200",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.needsDirs {
				require.NoError(t, pm.CreateDirectories())
			}

			args, err := pm.BuildReExecArgs(tt.opts)
			require.NoError(t, err)

			require.GreaterOrEqual(t, len(args), 3)
			assert.Equal(t, "gw", args[0])
			assert.Equal(t, "start", args[1])
			assert.Equal(t, "--follow", args[2])

			joined := strings.Join(args, " ")
			for _, expected := range tt.wantSubstrs {
				assert.Contains(t, joined, expected)
			}
		})
	}
}

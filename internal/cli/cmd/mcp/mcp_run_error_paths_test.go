// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/shared"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zerotrustdata/ztd/internal/cli/cmd/cmdtest"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

func TestBuildGatewayConn_ErrorPaths(t *testing.T) { //nolint:paralleltest // mutates package-level state

	t.Run("fails when trust bundle and cert files do not exist", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)
		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}
		_, err = buildGatewayConn(fileSvc, cfg, stdioCredentialFlags{})
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrFailedToReadTrustBundle)
	})

	t.Run("fails when CA bundle does not exist", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		tempDir := testutil.TempDir(t)
		certPath, keyPath, _ := generateTestCerts(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  filepath.Dir(certPath),
		}
		_, err = buildGatewayConn(fileSvc, cfg, stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   filepath.Join(tempDir, "nonexistent-ca.pem"),
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrFailedToReadTrustBundle)
	})

	t.Run("succeeds with valid certs and custom gateway URL", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  filepath.Dir(certPath),
		}

		conn, err := buildGatewayConn(fileSvc, cfg, stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caPath,
			GatewayURL: "https://127.0.0.1:9999/mcp",
		})
		require.NoError(t, err)
		assert.NotNil(t, conn)
		assert.Equal(t, "https://127.0.0.1:9999/mcp", conn.gatewayURL)
	})

	// Without --gateway-url the proxy addresses the Gateway every other CLI
	// command addresses: the ports the local Gateway bound, not the protocol default.
	t.Run("default gateway URL follows the local Gateway ports", func(t *testing.T) { //nolint:paralleltest // mutates package-level state

		config.SetEndpointOverride("")
		config.SetLocalGatewayPorts(8087, 8450)
		t.Cleanup(func() { config.SetLocalGatewayPorts(0, 0) })

		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)
		cfg := &config.Config{ProjectRoot: tempDir, RuntimeDir: filepath.Dir(certPath), Paths: &config.PathsConfig{}}

		conn, err := buildGatewayConn(fileSvc, cfg, stdioCredentialFlags{ClientCert: certPath, ClientKey: keyPath, CABundle: caPath})
		require.NoError(t, err)
		assert.Equal(t, "https://localhost:8450/mcp", conn.gatewayURL)
	})
}

// ─── runMCPStdioProxy config load error ──────────────────────────────────────

func TestRunMCPStdioProxy_ConfigLoadError(t *testing.T) { //nolint:paralleltest // changes the process working directory
	t.Run("returns wrapped error when config load fails", func(t *testing.T) {
		cmdtest.ChdirTemp(t)

		originalLoad := shared.ConfigLoad
		shared.ConfigLoad = func(string) (*config.Config, error) {
			return nil, fmt.Errorf("no config on disk")
		}
		t.Cleanup(func() { shared.ConfigLoad = originalLoad })

		cmd := mcpStdioCmd()
		cmd.SetArgs([]string{})
		err := cmd.Execute()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mcp: load config")
	})
}

// ─── startGatewayIfNeeded config load error ──────────────────────────────────

// ─── launchAgentWithGovernance error path (startGatewayIfNeeded fails) ───────

// ─── proxySessionToGateway connection refused ────────────────────────────────

func TestProxySessionToGateway_ConnectionRefused(t *testing.T) {
	t.Parallel()
	t.Run("returns error when gateway is unreachable", func(t *testing.T) {
		t.Parallel()
		session := &gatewayConn{
			openBrowser: func(string) error { return nil },
			client:      &http.Client{Timeout: 1 * time.Second},
			gatewayURL:  "http://127.0.0.1:1/mcp", // port 1 should refuse connections
		}

		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      1,
			Method:  "tools/list",
		}

		_, err := proxySessionToGateway(session, req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mcp: execute request")
	})
}

// ─── buildGatewayConn flag resolution ─────────────────────────────────────────

func TestBuildGatewayConn_FlagResolution(t *testing.T) {
	t.Run("flag-only credentials succeed", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caPath,
		}

		conn, err := buildGatewayConn(fileSvc, cfg, flags)
		require.NoError(t, err)
		assert.NotNil(t, conn)
	})

	t.Run("ambient ZTD_* env credentials are ignored", func(t *testing.T) {
		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		// Valid credentials supplied only through the retired env channel must not
		// be consulted: with no flags and no managed trust bundle the bridge fails
		// closed rather than silently adopting them.
		t.Setenv("ZTD_CLIENT_CERT", certPath)
		t.Setenv("ZTD_CLIENT_KEY", keyPath)
		t.Setenv("ZTD_CA_BUNDLE", caPath)
		t.Setenv("ZTD_GATEWAY_URL", "https://127.0.0.1:9999/mcp")

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		_, err = buildGatewayConn(fileSvc, cfg, stdioCredentialFlags{})
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrFailedToReadTrustBundle)
	})

	t.Run("gateway-url flag honored verbatim", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caPath,
			GatewayURL: "https://10.0.0.99:8443/mcp",
		}

		conn, err := buildGatewayConn(fileSvc, cfg, flags)
		require.NoError(t, err)
		assert.Equal(t, "https://10.0.0.99:8443/mcp", conn.gatewayURL)
	})

	t.Run("ca-bundle under runtime root reads through fileSvc", func(t *testing.T) { //nolint:paralleltest // sets the process environment
		certPath, keyPath, _ := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)
		require.NoError(t, fileSvc.CreateRuntimeTree(context.Background()))

		// Write a CA bundle inside the runtime tree
		caRel := filepath.Join(constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
		caData := []byte("fake-ca-bundle")
		require.NoError(t, fileSvc.WriteFile(context.Background(), caRel, caData, constants.PermFilePublic))

		caAbs := fileSvc.Resolve(caRel)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caAbs,
		}

		conn, err := buildGatewayConn(fileSvc, cfg, flags)
		require.NoError(t, err)
		assert.NotNil(t, conn)
	})
}

// ─── buildGatewayConn fail-closed ─────────────────────────────────────────────

func TestBuildGatewayConn_FailClosed(t *testing.T) {
	t.Parallel()
	t.Run("client-cert without client-key returns ErrIncompleteCredentialPair", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: "/tmp/client.crt",
		}

		_, err = buildGatewayConn(fileSvc, cfg, flags)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrIncompleteCredentialPair)
	})

	t.Run("client-key without client-cert returns ErrIncompleteCredentialPair", func(t *testing.T) {
		t.Parallel()
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientKey: "/tmp/client.key",
		}

		_, err = buildGatewayConn(fileSvc, cfg, flags)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrIncompleteCredentialPair)
	})

	t.Run("http gateway URL returns ErrMCPConfigGatewayURLInvalidScheme", func(t *testing.T) {
		t.Parallel()
		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caPath,
			GatewayURL: "http://ztd.local:8443/mcp",
		}

		_, err = buildGatewayConn(fileSvc, cfg, flags)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrMCPConfigGatewayURLInvalidScheme)
	})

	t.Run("empty host in gateway URL returns ErrMCPConfigGatewayURLHostEmpty", func(t *testing.T) {
		t.Parallel()
		certPath, keyPath, caPath := generateTestCerts(t)
		tempDir := testutil.TempDir(t)
		fileSvc, err := fs.NewRuntimeFileService(tempDir, slog.Default())
		require.NoError(t, err)

		cfg := &config.Config{
			ProjectRoot: tempDir,
			RuntimeDir:  tempDir,
		}

		flags := stdioCredentialFlags{
			ClientCert: certPath,
			ClientKey:  keyPath,
			CABundle:   caPath,
			GatewayURL: "https:///mcp",
		}

		_, err = buildGatewayConn(fileSvc, cfg, flags)
		require.Error(t, err)
		assert.ErrorIs(t, err, constants.ErrMCPConfigGatewayURLHostEmpty)
	})
}

// ─── parseStdioCredentialFlags ────────────────────────────────────────────────

func TestParseStdioCredentialFlags(t *testing.T) {
	t.Parallel()
	t.Run("zero value when nothing is set", func(t *testing.T) {
		t.Parallel()
		cmd := mcpStdioCmd()
		cmd.SetArgs([]string{})
		// Execute to register flags
		require.NoError(t, cmd.ParseFlags([]string{}))

		flags, err := parseStdioCredentialFlags(cmd)
		require.NoError(t, err)
		assert.Equal(t, stdioCredentialFlags{}, flags)
	})

	t.Run("exact values after flag set", func(t *testing.T) {
		t.Parallel()
		cmd := mcpStdioCmd()
		require.NoError(t, cmd.ParseFlags([]string{
			"--client-cert", "/tmp/cli.crt",
			"--client-key", "/tmp/cli.key",
			"--ca-bundle", "/tmp/ca.pem",
			"--gateway-url", "https://ztd.local:8443/mcp",
			"--app", "test-app",
		}))

		flags, err := parseStdioCredentialFlags(cmd)
		require.NoError(t, err)
		assert.Equal(t, "/tmp/cli.crt", flags.ClientCert)
		assert.Equal(t, "/tmp/cli.key", flags.ClientKey)
		assert.Equal(t, "/tmp/ca.pem", flags.CABundle)
		assert.Equal(t, "https://ztd.local:8443/mcp", flags.GatewayURL)
		assert.Equal(t, "test-app", flags.App)
	})
}

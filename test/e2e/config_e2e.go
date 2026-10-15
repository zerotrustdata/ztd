// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zerotrustdata/ztd/internal/cli/auth"
	"github.com/zerotrustdata/ztd/internal/cli/config"
	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/services/fs"
	"github.com/zerotrustdata/ztd/internal/testutil"
)

// e2eConfig holds resolved platform endpoints and owner credentials loaded
// from the local .ztd/ runtime tree. It is constructed once by loadE2EConfig
// and shared across all E2E test functions via the package-level e2eCfg
// variable set in TestMain.
type e2eConfig struct {
	gatewayHTTPURL    string
	gatewayHTTPSURL   string
	caBundleRelPath   string
	cliSessionID      string
	userID            string
	operatorID        string
	operatorSessionID string
	fileSvc           fs.RuntimeFileService
	cfg               *config.Config
}

// healthCheckTimeout is the bounded timeout for the TestMain preflight health
// check. A single GET to the gateway health endpoint must complete within this
// window or the suite fails closed.
const healthCheckTimeout = 10 * time.Second

// e2eGatewayPorts returns the Gateway ports named by ZTD_E2E_GATEWAY_HTTP_PORT
// and ZTD_E2E_GATEWAY_HTTPS_PORT, or zeros when neither is set. Setting only
// one of them is an error. Scenarios whose isolated Gateway listens on
// non-default ports (ztd test scale) set them; the suite and the CLI processes
// it starts then dial those ports on IPv4 loopback, using the literal IP
// because the isolated Gateway does not listen on IPv6 loopback.
func e2eGatewayPorts() (httpPort, httpsPort int, err error) {
	rawHTTP := strings.TrimSpace(os.Getenv(string(constants.EnvVar.E2EGatewayHTTPPort)))
	rawHTTPS := strings.TrimSpace(os.Getenv(string(constants.EnvVar.E2EGatewayHTTPSPort)))
	if rawHTTP == "" && rawHTTPS == "" {
		return 0, 0, nil
	}
	parse := func(name, raw string) (int, error) {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("%s must be a TCP port, got %q", name, raw)
		}
		return port, nil
	}
	if httpPort, err = parse(string(constants.EnvVar.E2EGatewayHTTPPort), rawHTTP); err != nil {
		return 0, 0, err
	}
	if httpsPort, err = parse(string(constants.EnvVar.E2EGatewayHTTPSPort), rawHTTPS); err != nil {
		return 0, 0, err
	}
	return httpPort, httpsPort, nil
}

// e2eGatewayEndpointArgs returns the global CLI flags that point a child ztd
// process at the environment's Gateway ports, or nil for the defaults.
func e2eGatewayEndpointArgs() ([]string, error) {
	httpPort, httpsPort, err := e2eGatewayPorts()
	if err != nil || httpPort == 0 {
		return nil, err
	}
	return []string{"-e", net.JoinHostPort(constants.LocalhostIP, strconv.Itoa(httpPort)), "-p", strconv.Itoa(httpsPort)}, nil
}

// loadE2EConfig resolves the repository root, loads CLI configuration from the
// local .ztd/ runtime tree, and reads the owner CLI session ID from stored
// credentials. It returns an error if any step fails — callers (TestMain) fail
// closed on error rather than skipping. The gateway HTTP/HTTPS URLs are
// derived from CLI config.
func loadE2EConfig() (*e2eConfig, error) {
	repoRoot, err := resolveRuntimeRoot()
	if err != nil {
		return nil, fmt.Errorf("e2e: resolve runtime root: %w", err)
	}

	cfg, err := config.Load(repoRoot)
	if err != nil {
		return nil, fmt.Errorf("e2e: load CLI config: %w", err)
	}

	fileSvc, err := fs.NewRuntimeFileService(repoRoot, testutil.NewTestLogger())
	if err != nil {
		return nil, fmt.Errorf("e2e: create file service: %w", err)
	}

	creds, err := auth.LoadCredentials(fileSvc, cfg)
	if err != nil {
		return nil, fmt.Errorf("e2e: load credentials: %w", err)
	}
	if err := validateCredentials(creds); err != nil {
		return nil, fmt.Errorf("e2e: %w", err)
	}

	httpPort, httpsPort, err := e2eGatewayPorts()
	if err != nil {
		return nil, fmt.Errorf("e2e: %w", err)
	}
	if httpPort != 0 {
		config.SetHTTPEndpointOverride(net.JoinHostPort(constants.LocalhostIP, strconv.Itoa(httpPort)))
		config.SetHTTPSEndpointOverride(net.JoinHostPort(constants.LocalhostIP, strconv.Itoa(httpsPort)))
	}

	gatewayHTTPURL := cfg.OperatorDiscoveryURL()
	gatewayHTTPSURL := cfg.OperatorPublicURL()

	return &e2eConfig{
		gatewayHTTPURL:    gatewayHTTPURL,
		gatewayHTTPSURL:   gatewayHTTPSURL,
		caBundleRelPath:   cfg.DefaultTrustBundleRelPath(),
		cliSessionID:      creds.CLISessionID,
		userID:            creds.UserID,
		operatorID:        creds.OperatorID,
		operatorSessionID: creds.OperatorSessionID,
		fileSvc:           fileSvc,
		cfg:               cfg,
	}, nil
}

// readCABundle reads the CA bundle from the runtime tree via fileSvc. Returns
// the raw PEM bytes.
func (c *e2eConfig) readCABundle(ctx context.Context) ([]byte, error) {
	return auth.ReadTrustBundle(c.fileSvc, c.cfg)
}

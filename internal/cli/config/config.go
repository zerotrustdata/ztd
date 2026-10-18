// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/netutil"
	"github.com/zerotrustdata/ztd/internal/paths"
	"github.com/zerotrustdata/ztd/internal/pathutil"
	"github.com/zerotrustdata/ztd/internal/services/keystore/keyring"
	"github.com/zerotrustdata/ztd/internal/services/network"
)

const (
	DefaultRuntimeDir = constants.RuntimeDirname
	DefaultPKIDir     = constants.DefaultPKIDir
	DefaultSecretsDir = constants.DefaultSecretsDir
)

// PathsConfig holds path configuration for test support.
// Production code should use constants.Paths directly.
type PathsConfig struct {
	Host  string `json:"host"`
	Infra struct {
		AppCertDir           string `json:"app_cert_dir"`
		CACertPath           string `json:"ca_cert_path"`
		DBPath               string `json:"db_path"`
		DocsDir              string `json:"docs_dir"`
		PKIDir               string `json:"pki_dir"`
		ProtocolConstantsDir string `json:"protocol_constants_dir"`
		ProtocolDir          string `json:"protocol_dir"`
		ProtocolModelsDir    string `json:"protocol_models_dir"`
		SecretsDir           string `json:"secrets_dir"`
		SSHConfigPath        string `json:"ssh_config_path"`
		VaultDir             string `json:"vault_dir"`
	} `json:"infra"`
}

// DefaultPathsConfig returns the default path configuration.
// All paths are relative and resolved from the current working directory.
func DefaultPathsConfig() PathsConfig {
	return PathsConfig{
		Host: "localhost",
	}
}

// DefaultInfraPaths returns the default infra path configuration.
func DefaultInfraPaths() PathsConfig {
	cfg := DefaultPathsConfig()
	cfg.Infra.AppCertDir = paths.Infra.AppCertDir
	cfg.Infra.CACertPath = paths.Infra.CaCertPath
	cfg.Infra.DBPath = paths.Infra.DbPath
	cfg.Infra.DocsDir = paths.Infra.DocsDir
	cfg.Infra.PKIDir = paths.Infra.PkiDir
	cfg.Infra.ProtocolConstantsDir = paths.Infra.ProtocolConstantsDir
	cfg.Infra.ProtocolDir = paths.Infra.ProtocolDir
	cfg.Infra.ProtocolModelsDir = paths.Infra.ProtocolModelsDir
	cfg.Infra.SecretsDir = paths.Infra.SecretsDir
	cfg.Infra.SSHConfigPath = paths.Infra.SshConfigPath
	cfg.Infra.VaultDir = paths.Infra.VaultDir
	return cfg
}

// Config holds CLI configuration resolved from constants.Paths.
// All paths are sourced from internal/constants/paths.go (SSOT).
type Config struct {
	ProjectRoot string
	RuntimeDir  string
	PKIDir      string
	SecretsDir  string
	Paths       *PathsConfig
	// Keyring holds the master key that seals the client identity keys under
	// the runtime tree. Nil selects the OS key store; tests inject an isolated
	// in-memory keyring. There is no plaintext fallback.
	Keyring keyring.Keyring
}

// Load initializes CLI configuration from the current working directory.
// All paths are resolved using constants.InitPathsWithBase.
func Load(projectRoot string) (*Config, error) {
	if projectRoot == "" {
		var err error
		projectRoot, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cli config: failed to get working directory: %w", err)
		}
	}

	if err := paths.InitWithBase(projectRoot); err != nil {
		return nil, fmt.Errorf("cli config: failed to initialize paths: %w", err)
	}

	pathsCfg := DefaultInfraPaths()
	// paths.InitWithBase already makes all paths.Infra.* absolute
	// Copy them directly without re-joining with projectRoot
	pathsCfg.Infra.ProtocolDir = paths.Infra.ProtocolDir
	pathsCfg.Infra.ProtocolConstantsDir = paths.Infra.ProtocolConstantsDir
	pathsCfg.Infra.ProtocolModelsDir = paths.Infra.ProtocolModelsDir
	pathsCfg.Infra.DBPath = paths.Infra.DbPath
	pathsCfg.Infra.PKIDir = paths.Infra.PkiDir
	pathsCfg.Infra.CACertPath = paths.Infra.CaCertPath
	pathsCfg.Infra.SecretsDir = paths.Infra.SecretsDir
	pathsCfg.Infra.AppCertDir = paths.Infra.AppCertDir
	pathsCfg.Infra.DocsDir = paths.Infra.DocsDir
	pathsCfg.Infra.SSHConfigPath = paths.Infra.SshConfigPath
	pathsCfg.Infra.VaultDir = paths.Infra.VaultDir

	return &Config{
		ProjectRoot: projectRoot,
		RuntimeDir:  paths.Infra.RuntimeDir,
		PKIDir:      paths.Infra.PkiDir,
		SecretsDir:  paths.Infra.SecretsDir,
		Paths:       &pathsCfg,
	}, nil
}

// DefaultTrustBundleRelPath returns the trust bundle path relative to the
// .ztd/ runtime directory root. This is the canonical location for the
// gateway CA bundle and should be used with RuntimeFileService.
func (c *Config) DefaultTrustBundleRelPath() string {
	return constants.PkiDirname + "/" + constants.PkiSubdirTrust + "/" + constants.PkiFileGatewayBundle
}

// ResolvedTrustBundlePath returns the absolute trust bundle path for display,
// env-var propagation, or subprocess config. It returns the custom external
// path when configured, or the default runtime path joined with RuntimeDir.
// Callers that perform file I/O should use ReadTrustBundle/WriteTrustBundleFS
// with fileSvc instead.
func (c *Config) ResolvedTrustBundlePath() string {
	if custom := c.CustomTrustBundlePath(); custom != "" {
		return custom
	}
	return filepath.Join(c.RuntimeDir, c.DefaultTrustBundleRelPath())
}

// CustomTrustBundlePath returns a user-specified external trust bundle path
// if one has been configured that differs from the default runtime location.
// Returns an empty string when the default runtime path should be used.
func (c *Config) CustomTrustBundlePath() string {
	if c.Paths == nil || c.Paths.Infra.CACertPath == "" {
		return ""
	}
	configured := c.Paths.Infra.CACertPath
	if !filepath.IsAbs(configured) {
		configured = pathutil.SafeJoin(c.ProjectRoot, configured)
	}
	defaultAbs := filepath.Join(c.RuntimeDir, constants.PkiDirname, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
	if configured == defaultAbs {
		return ""
	}
	return configured
}

func (c *Config) CredentialsFile() string {
	return filepath.Join(c.RuntimeDir, constants.CredentialsFilename)
}

func (c *Config) CLICertFile() string {
	return filepath.Join(c.RuntimeDir, constants.CliCertFilename)
}

func (c *Config) CLIKeyFile() string {
	return filepath.Join(c.RuntimeDir, constants.CliKeyFilename)
}

func (c *Config) AppCertFile(name string) string {
	return filepath.Join(c.RuntimeDir, constants.PkiSubdirApps, name+constants.FileExtCert)
}

func (c *Config) AppKeyFile(name string) string {
	return filepath.Join(c.RuntimeDir, constants.PkiSubdirApps, name+constants.FileExtKey)
}

func (c *Config) AppSigningKeyFile(name string) string {
	return filepath.Join(c.RuntimeDir, constants.PkiSubdirApps, name+constants.AppSigningKeySuffix)
}

// OperatorHTTPSPort returns the HTTPS/mTLS port of the Gateway on this host:
// the port it actually bound when SetLocalGatewayPorts recorded one, otherwise
// the protocol default.
func (c *Config) OperatorHTTPSPort() int {
	_, https := LocalGatewayPorts()
	return https
}

// OperatorHTTPPort returns the plain HTTP discovery port of the Gateway on this
// host, following SetLocalGatewayPorts like OperatorHTTPSPort.
func (c *Config) OperatorHTTPPort() int {
	http, _ := LocalGatewayPorts()
	return http
}

// OperatorHTTPURL returns the HTTPS URL for the operator API.
// When cfg.Paths.Host is a full URL (contains "://"), it is returned directly.
// This allows tests to override the URL by setting cfg.Paths.Host to the test server URL.
func (c *Config) OperatorHTTPURL() string {
	endpointMu.RLock()
	override := httpsEndpointOverride
	endpointMu.RUnlock()
	if override != "" {
		if strings.Contains(override, "://") {
			return override
		}
		if host, port, err := netutil.SplitEndpoint(override); err == nil && port == 0 {
			return fmt.Sprintf("https://%s", net.JoinHostPort(host, strconv.Itoa(constants.Ports.OperatorHttps)))
		}
		return fmt.Sprintf("https://%s", override)
	}
	if c.Paths != nil && strings.Contains(c.Paths.Host, "://") {
		return c.Paths.Host
	}
	return network.LocalhostHTTPSURL(c.OperatorHTTPSPort())
}

// endpointMu guards httpEndpointOverride and httpsEndpointOverride against
// concurrent read/write races between enrollment (which sets and clears
// overrides) and request handlers (which read them via OperatorHTTPURL etc).
var endpointMu sync.RWMutex

// httpEndpointOverride is set by the -e/--endpoint flag to allow
// connecting to a remote gateway HTTP discovery endpoint instead of localhost.
var httpEndpointOverride string

// httpsEndpointOverride is set by the --port flag (combined with --endpoint host)
// to allow connecting to a remote gateway HTTPS/mTLS endpoint on a different port.
var httpsEndpointOverride string

// localGatewayHTTPPort and localGatewayHTTPSPort are the ports the Gateway
// launched from this runtime root actually bound. `gw start` moves both by the
// same offset when the requested HTTP port is busy, and persists the result in
// the launch profile; the root command records them here so a CLI without
// --endpoint reaches that Gateway instead of the protocol defaults. Zero
// selects the protocol default.
var localGatewayHTTPPort, localGatewayHTTPSPort int

// SetLocalGatewayPorts records the ports the local Gateway bound. Explicit
// --endpoint/--port overrides still take precedence.
func SetLocalGatewayPorts(httpPort, httpsPort int) {
	endpointMu.Lock()
	defer endpointMu.Unlock()
	localGatewayHTTPPort = httpPort
	localGatewayHTTPSPort = httpsPort
}

// LocalGatewayPorts returns the HTTP and HTTPS ports of the Gateway on this
// host, falling back to the protocol defaults for any port not recorded.
func LocalGatewayPorts() (httpPort, httpsPort int) {
	endpointMu.RLock()
	defer endpointMu.RUnlock()
	httpPort, httpsPort = localGatewayHTTPPort, localGatewayHTTPSPort
	if httpPort == 0 {
		httpPort = constants.Ports.OperatorHttp
	}
	if httpsPort == 0 {
		httpsPort = constants.Ports.OperatorHttps
	}
	return httpPort, httpsPort
}

// SetEndpointOverride sets both HTTP and HTTPS endpoint overrides to the same value.
// Tests with one URL use this to keep both discovery paths in agreement.
func SetEndpointOverride(endpoint string) {
	endpointMu.Lock()
	defer endpointMu.Unlock()
	httpEndpointOverride = endpoint
	httpsEndpointOverride = endpoint
}

// SetHTTPEndpointOverride sets only the HTTP discovery override.
func SetHTTPEndpointOverride(endpoint string) {
	endpointMu.Lock()
	defer endpointMu.Unlock()
	httpEndpointOverride = endpoint
}

// SetHTTPSEndpointOverride sets only the HTTPS/mTLS override.
func SetHTTPSEndpointOverride(endpoint string) {
	endpointMu.Lock()
	defer endpointMu.Unlock()
	httpsEndpointOverride = endpoint
}

// HasHTTPSEndpointOverride reports whether the HTTPS endpoint override was set
// by the -e/--endpoint flag (optionally combined with --port). Callers use this
// to decide whether to rewrite a gateway-returned URL (e.g., the recovery
// approval URL) to match the user-supplied endpoint instead of the gateway's
// own PublicBaseURL.
func HasHTTPSEndpointOverride() bool {
	endpointMu.RLock()
	defer endpointMu.RUnlock()
	return httpsEndpointOverride != ""
}

// OperatorPublicURL returns the HTTPS URL for mTLS API and public surface.
// When cfg.Paths.Host is a full URL (contains "://"), it is returned directly,
// matching OperatorHTTPURL behavior for test overrides.
func (c *Config) OperatorPublicURL() string {
	return c.OperatorHTTPURL()
}

// OperatorDiscoveryURL returns the HTTP URL for CA download and bootstrap routes.
// When cfg.Paths.Host is a full URL (contains "://"), it is returned directly,
// allowing tests to override the discovery URL via cfg.Paths.Host.
func (c *Config) OperatorDiscoveryURL() string {
	endpointMu.RLock()
	override := httpEndpointOverride
	endpointMu.RUnlock()
	if override != "" {
		if strings.Contains(override, "://") {
			return override
		}
		if host, port, err := netutil.SplitEndpoint(override); err == nil && port == 0 {
			return fmt.Sprintf("http://%s", net.JoinHostPort(host, strconv.Itoa(constants.Ports.OperatorHttp)))
		}
		return fmt.Sprintf("http://%s", override)
	}
	if c.Paths != nil && strings.Contains(c.Paths.Host, "://") {
		return c.Paths.Host
	}
	return network.LocalhostHTTPURL(c.OperatorHTTPPort())
}

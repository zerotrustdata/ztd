// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

// Package paths manages resolved runtime filesystem paths for the ztd platform.
// All path variables are populated by Init or InitWithBase at program startup.
// String constants (filenames, subdirectory names, system paths) remain in
// internal/constants/paths.go.
package paths

import (
	"fmt"
	"os"
	"sync"

	"github.com/zerotrustdata/ztd/internal/constants"
	"github.com/zerotrustdata/ztd/internal/pathutil"
)

var mu sync.RWMutex

// Infra holds resolved runtime filesystem paths.
// All paths are relative to the working directory by default.
// Populated by Init or InitWithBase at program startup.
var Infra struct {
	DbPath                  string
	PkiDir                  string
	SecretsDir              string
	CaCertPath              string
	AppCertDir              string
	DocsDir                 string
	ProtocolDir             string
	ProtocolConstantsDir    string
	ProtocolModelsDir       string
	SshConfigPath           string
	RuntimeDir              string
	DataDir                 string
	VaultDir                string
	TestVaultDir            string
	PkiRootDir              string
	PkiAuthoritiesDir       string
	PkiIssuedDir            string
	PkiIssuedHubDir         string
	PkiIssuedGatewayPeerDir string
	PkiTrustDir             string
	PkiRevocationDir        string
	PkiBinariesDir          string

	// Operator PKI file paths
	ClientPkiDir string

	// Log paths
	LogDir string

	// PID paths
	PidDir string

	// Binary paths
	BinDir string

	// Storage DB paths
	LedgerDir      string
	LedgerFilesDir string
} = struct {
	DbPath                  string
	PkiDir                  string
	SecretsDir              string
	CaCertPath              string
	AppCertDir              string
	DocsDir                 string
	ProtocolDir             string
	ProtocolConstantsDir    string
	ProtocolModelsDir       string
	SshConfigPath           string
	RuntimeDir              string
	DataDir                 string
	VaultDir                string
	TestVaultDir            string
	PkiRootDir              string
	PkiAuthoritiesDir       string
	PkiIssuedDir            string
	PkiIssuedHubDir         string
	PkiIssuedGatewayPeerDir string
	PkiTrustDir             string
	PkiRevocationDir        string
	PkiBinariesDir          string

	ClientPkiDir string

	LogDir string

	PidDir string

	BinDir string

	LedgerDir      string
	LedgerFilesDir string
}{
	DbPath:                  constants.RuntimeDirname + "/" + constants.DataDirname + "/" + constants.DbFilename,
	PkiDir:                  constants.RuntimeDirname + "/" + constants.PkiDirname,
	SecretsDir:              constants.RuntimeDirname + "/" + constants.SecretsDirname,
	CaCertPath:              constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirTrust + "/" + constants.PkiFileGatewayBundle,
	AppCertDir:              constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirIssued + "/" + constants.PkiSubdirApps,
	DocsDir:                 constants.RuntimeDirname + "/" + constants.DocsDirname,
	ProtocolDir:             constants.RuntimeDirname + "/" + constants.ProtocolDirname,
	ProtocolConstantsDir:    constants.RuntimeDirname + "/" + constants.ProtocolDirname + "/" + constants.ProtocolConstantsDirname,
	ProtocolModelsDir:       constants.RuntimeDirname + "/" + constants.ProtocolDirname + "/" + constants.ProtocolModelsDirname,
	SshConfigPath:           constants.RuntimeDirname + "/" + constants.SshConfigFilename,
	RuntimeDir:              constants.RuntimeDirname,
	DataDir:                 constants.RuntimeDirname + "/" + constants.DataDirname,
	VaultDir:                constants.RuntimeDirname + "/" + constants.VaultDirname,
	TestVaultDir:            constants.RuntimeDirname + "/" + constants.TestVaultDirname,
	PkiRootDir:              constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirRoot,
	PkiAuthoritiesDir:       constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirAuthorities,
	PkiIssuedHubDir:         constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirIssued + "/" + constants.PkiSubdirHub,
	PkiIssuedGatewayPeerDir: constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirIssued + "/" + constants.PkiSubdirGatewayPeer,
	PkiTrustDir:             constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirTrust,
	PkiRevocationDir:        constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirRevocation,

	ClientPkiDir: constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirClient,

	LogDir: constants.RuntimeDirname + "/" + constants.LogDirname,
	PidDir: constants.RuntimeDirname + "/" + constants.PidDirname,
	BinDir: constants.RuntimeDirname + "/" + constants.BinDirname,

	LedgerDir: constants.RuntimeDirname + "/" + constants.DataDirname + "/" + constants.LedgerDirname,
}

// Mutable path vars that are derived from the base directory at init time.
// These complement Infra for paths accessed as bare variables.
var (
	GatewayIDPath       = constants.RuntimeDirname + "/" + constants.DataDirname + "/" + constants.GatewayIDFilename
	ActuatorPubJSONPath = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.ActuatorPubJSONFilename
	ActuatorPubPEMPath  = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.ActuatorPubPEMFilename
	NetworkIdentityPath = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.NetworkIdentityFilename
	PeerCertPath        = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PeerSubdir + "/" + constants.PeerCertFilename
	PeerKeyPath         = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PeerSubdir + "/" + constants.PeerKeyFilename
	PeerChainPath       = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PeerSubdir + "/" + constants.PeerChainFilename
	PkiGatewayKeyPath   = constants.RuntimeDirname + "/" + constants.PkiDirname + "/" + constants.PkiSubdirIssued + "/" + constants.PkiSubdirHub + "/" + constants.PkiFileGatewayKey
	ZtdLogPath          = constants.ZtdLogFilename
)

// Init initializes paths relative to the current working directory.
// Call once at program startup.
func Init() error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("paths: failed to get working directory: %w", err)
	}
	return InitWithBase(cwd)
}

// InitWithBase initializes paths relative to baseDir.
// Used by tests and specific startup contexts to override the default cwd behavior.
func InitWithBase(baseDir string) error {
	mu.Lock()
	defer mu.Unlock()

	Infra.RuntimeDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname)
	Infra.DataDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.DataDirname)
	Infra.PkiDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.PkiDirname)
	Infra.SecretsDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.SecretsDirname)
	Infra.ProtocolDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.ProtocolDirname)
	Infra.VaultDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.VaultDirname)

	Infra.ProtocolConstantsDir = pathutil.SafeJoin(Infra.ProtocolDir, constants.ProtocolConstantsDirname)
	Infra.ProtocolModelsDir = pathutil.SafeJoin(Infra.ProtocolDir, constants.ProtocolModelsDirname)
	Infra.DbPath = pathutil.SafeJoin(Infra.DataDir, constants.DbFilename)
	Infra.CaCertPath = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirTrust, constants.PkiFileGatewayBundle)
	Infra.AppCertDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirIssued, constants.PkiSubdirApps)
	Infra.DocsDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.DocsDirname)
	Infra.SshConfigPath = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.SshConfigFilename)
	Infra.TestVaultDir = pathutil.SafeJoin(baseDir, constants.RuntimeDirname, constants.TestVaultDirname)
	Infra.PkiRootDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirRoot)
	Infra.PkiAuthoritiesDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirAuthorities)
	Infra.PkiIssuedDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirIssued)
	Infra.PkiIssuedHubDir = pathutil.SafeJoin(Infra.PkiIssuedDir, constants.PkiSubdirHub)
	Infra.PkiIssuedGatewayPeerDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirIssued, constants.PkiSubdirGatewayPeer)
	Infra.PkiTrustDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirTrust)
	Infra.PkiRevocationDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirRevocation)
	Infra.PkiBinariesDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirBinaries)

	Infra.ClientPkiDir = pathutil.SafeJoin(Infra.PkiDir, constants.PkiSubdirClient)

	Infra.LogDir = pathutil.SafeJoin(Infra.RuntimeDir, constants.LogDirname)
	Infra.PidDir = pathutil.SafeJoin(Infra.RuntimeDir, constants.PidDirname)
	Infra.BinDir = pathutil.SafeJoin(Infra.RuntimeDir, constants.BinDirname)

	Infra.LedgerDir = pathutil.SafeJoin(Infra.DataDir, constants.LedgerDirname)
	Infra.LedgerFilesDir = pathutil.SafeJoin(Infra.LedgerDir, constants.FilesDirname)

	GatewayIDPath = pathutil.SafeJoin(Infra.DataDir, constants.GatewayIDFilename)
	NetworkIdentityPath = pathutil.SafeJoin(Infra.PkiDir, constants.NetworkIdentityFilename)
	PeerCertPath = pathutil.SafeJoin(Infra.PkiDir, constants.PeerSubdir, constants.PeerCertFilename)
	PeerKeyPath = pathutil.SafeJoin(Infra.PkiDir, constants.PeerSubdir, constants.PeerKeyFilename)
	PeerChainPath = pathutil.SafeJoin(Infra.PkiDir, constants.PeerSubdir, constants.PeerChainFilename)
	PkiGatewayKeyPath = pathutil.SafeJoin(Infra.PkiIssuedHubDir, constants.PkiFileGatewayKey)
	return nil
}

// AgentConfigPaths holds precomputed agent configuration paths for a given home directory.
// Call once per command with the user's home directory to avoid repeated filepath.Join calls.
type AgentConfigPaths struct {
	DevinConfigDir      string
	DevinConfigPath     string
	GeminiConfigDir     string
	GeminiConfigPath    string
	GooseYAMLConfigDir  string
	GooseYAMLConfigPath string
}

// GetAgentConfigPaths precomputes all agent configuration paths from the given home directory.
// Used by CLI commands that write agent MCP configurations.
func GetAgentConfigPaths(homeDir string) AgentConfigPaths {
	return AgentConfigPaths{
		DevinConfigDir:      pathutil.SafeJoin(homeDir, constants.AgentConfigDirDevin),
		DevinConfigPath:     pathutil.SafeJoin(homeDir, constants.AgentConfigDirDevin, constants.AgentConfigFileMCPDevin),
		GeminiConfigDir:     pathutil.SafeJoin(homeDir, constants.AgentConfigDirGemini),
		GeminiConfigPath:    pathutil.SafeJoin(homeDir, constants.AgentConfigDirGemini, constants.AgentConfigFileSettings),
		GooseYAMLConfigDir:  pathutil.SafeJoin(homeDir, constants.AgentConfigDirGoose),
		GooseYAMLConfigPath: pathutil.SafeJoin(homeDir, constants.AgentConfigDirGoose, constants.AgentConfigFileGooseYAML),
	}
}

// SSHConfigPaths holds precomputed SSH configuration paths for a given home directory.
// Call once per command with the user's home directory to avoid repeated filepath.Join calls.
type SSHConfigPaths struct {
	ConfigPath      string
	KnownHostsPath  string
	IDE25519KeyPath string
	IDECDSAKeyPath  string
	IDRSAKeyPath    string
}

// GetSSHConfigPaths precomputes all SSH configuration paths from the given home directory.
// Used by SSH connection resolution functions.
func GetSSHConfigPaths(homeDir string) SSHConfigPaths {
	sshDir := pathutil.SafeJoin(homeDir, constants.SshDirname)
	return SSHConfigPaths{
		ConfigPath:      pathutil.SafeJoin(sshDir, constants.SshConfigBasename),
		KnownHostsPath:  pathutil.SafeJoin(sshDir, constants.SshKnownHostsBasename),
		IDE25519KeyPath: pathutil.SafeJoin(sshDir, constants.SshKeyEd25519),
		IDECDSAKeyPath:  pathutil.SafeJoin(sshDir, constants.SshKeyECDSA),
		IDRSAKeyPath:    pathutil.SafeJoin(sshDir, constants.SshKeyRSA),
	}
}

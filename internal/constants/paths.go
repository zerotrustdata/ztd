// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

// System paths (Unix) for critical system directories and files.
const (
	PathEtc                                                   = "/etc"
	PathEtcPasswd                                             = "/etc/passwd"
	PathEtcShadow                                             = "/etc/shadow"
	PathEtcGroup                                              = "/etc/group"
	PathEtcGshadow                                            = "/etc/gshadow"
	PathEtcSudoers                                            = "/etc/sudoers"
	PathEtcSudoersD                                           = "/etc/sudoers.d/"
	PathEtcSshSshdConfig                                      = "/etc/ssh/sshd_config"
	PathEtcSshSshConfig                                       = "/etc/ssh/ssh_config"
	PathEtcPamD                                               = "/etc/pam.d/"
	PathEtcSecurity                                           = "/etc/security/"
	PathEtcLdSoConf                                           = "/etc/ld.so.conf"
	PathEtcLdSoPreload                                        = "/etc/ld.so.preload"
	PathEtcHosts                                              = "/etc/hosts"
	PathEtcResolvConf                                         = "/etc/resolv.conf"
	PathEtcFstab                                              = "/etc/fstab"
	PathEtcCrontab                                            = "/etc/crontab"
	PathEtcCronD                                              = "/etc/cron.d/"
	PathEtcCronDaily                                          = "/etc/cron.daily/"
	PathEtcCronHourly                                         = "/etc/cron.hourly/"
	PathEtcInitD                                              = "/etc/init.d/"
	PathEtcSystemdSystem                                      = "/etc/systemd/system/"
	PathEtcRcLocal                                            = "/etc/rc.local"
	PathEtcProfile                                            = "/etc/profile"
	PathEtcProfileD                                           = "/etc/profile.d/"
	PathEtcBashBashrc                                         = "/etc/bash.bashrc"
	PathEtcEnvironment                                        = "/etc/environment"
	PathEtcSelinux                                            = "/etc/selinux/"
	PathEtcApparmor                                           = "/etc/apparmor/"
	PathEtcApparmorD                                          = "/etc/apparmor.d/"
	PathBoot                                                  = "/boot"
	PathRootSsh                                               = "/root/.ssh/"
	PathRootBashrc                                            = "/root/.bashrc"
	PathRootBashProfile                                       = "/root/.bash_profile"
	PathRootProfile                                           = "/root/.profile"
	PathBin                                                   = "/bin"
	PathSbin                                                  = "/sbin"
	PathUsrBin                                                = "/usr/bin"
	PathUsrSbin                                               = "/usr/sbin"
	PathUsrLocalBin                                           = "/usr/local/bin"
	PathUsrLocalSbin                                          = "/usr/local/sbin"
	PathBinBash                                               = "/bin/bash"
	PathUsrBinBash                                            = "/usr/bin/bash"
	PathUsrBinSh                                              = "/usr/bin/sh"
	PathBinSh                                                 = "/bin/sh"
	PathLib                                                   = "/lib"
	PathLib64                                                 = "/lib64"
	PathUsrLib                                                = "/usr/lib"
	PathProc                                                  = "/proc"
	PathSys                                                   = "/sys"
	PathDev                                                   = "/dev"
	PathVar                                                   = "/var"
	PathVarLogDmesg                                           = "/var/log/dmesg"
	PathHome                                                  = "/home"
	PathEtcHostname                                           = "/etc/hostname"
	PathEtcMachineID                                          = "/etc/machine-id"
	PathVarLibDbusMachineID                                   = "/var/lib/dbus/machine-id"
	PathProcSysKernelRandomBootID                             = "/proc/sys/kernel/random/boot_id"
	PathProcMounts                                            = "/proc/mounts"
	PathProcLoadAvg                                           = "/proc/loadavg"
	PathProcMemInfo                                           = "/proc/meminfo"
	PathProcNet                                               = "/proc/net"
	PathProcNetTCP                                            = "/proc/net/tcp"
	PathProcNetUDP                                            = "/proc/net/udp"
	PathProcNetTCP6                                           = "/proc/net/tcp6"
	PathProcNetUDP6                                           = "/proc/net/udp6"
	PathProcNetRaw                                            = "/proc/net/raw"
	PathProcUptime                                            = "/proc/uptime"
	PathProcStat                                              = "/proc/stat"
	PathProcVersion                                           = "/proc/version"
	PathProcOneCmdline                                        = "/proc/1/cmdline"
	PathEtcOSRelease                                          = "/etc/os-release"
	PathEtcTimezone                                           = "/etc/timezone"
	PathEtcLocaltime                                          = "/etc/localtime"
	PathSysClassDMIIDProductUUID                              = "/sys/class/dmi/id/product_uuid"
	PathSysClassDMIIDSysVendor                                = "/sys/class/dmi/id/sys_vendor"
	PathRoot                                                  = "/"
	PathLibraryPreferencesSystemConfigurationPreferencesPlist = "/Library/Preferences/SystemConfiguration/preferences.plist"

	// Relative path components
	PathParentDir = ".."
)

// System paths (Windows) for registry, hosts, and Git Bash locations.
const (
	PathWindowsSystemRoot                 = "SystemRoot"
	PathWindowsHostsFile                  = "System32\\drivers\\etc\\hosts"
	PathWindowsRegistryCryptography       = "SOFTWARE\\Microsoft\\Cryptography"
	PathWindowsRegistryMachineGuid        = "MachineGuid"
	PathWindowsRegistryCurrentVersion     = "SOFTWARE\\Microsoft\\Windows NT\\CurrentVersion"
	PathWindowsRegistryProductName        = "ProductName"
	PathWindowsRegistryCurrentBuild       = "CurrentBuild"
	PathWindowsRegistryCurrentBuildNumber = "CurrentBuildNumber"
	PathWindowsGitBinBash                 = "C:\\Program Files\\Git\\bin\\bash.exe"
	PathWindowsGitUsrBinBash              = "C:\\Program Files\\Git\\usr\\bin\\bash.exe"
	PathWindowsGitBinSh                   = "C:\\Program Files\\Git\\bin\\sh.exe"
	PathWindowsMsys64Bash                 = "C:\\msys64\\usr\\bin\\bash.exe"
	PathWindowsCygwin64Bash               = "C:\\cygwin64\\bin\\bash.exe"
)

// Operator host-agent staging patterns (os.CreateTemp). The leading dot keeps
// the staging file hidden until it is atomically renamed into place.
const (
	OperatorInstallStagingTempPattern = ".ztd-install-*"
	OperatorLinkStagingTempPattern    = ".ztd-link-*"
)

// SSH path constants for known_hosts and config locations.
const (
	PathEtcSshKnownHosts      = "/etc/ssh/known_hosts"
	PathEtcSshSshKnownHosts   = "/etc/ssh/ssh_known_hosts"
	PathHomeSshKnownHosts     = "$HOME/.ssh/known_hosts"
	PathWindowsSshKnownHosts  = "$USERPROFILE\\.ssh\\known_hosts"
	PathWindowsProgramDataSsh = "C:\\ProgramData\\ssh\\known_hosts"
)

// Environment variable constants.
const (
	EnvPathDefault = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// PKI constants for subdirectories, filenames, and extensions.
const (
	PkiDirname             = "pki"
	PkiSubdirRoot          = "root"
	PkiSubdirAuthorities   = "authorities"
	PkiSubdirIssued        = "issued"
	PkiSubdirTrust         = "trust"
	PkiSubdirRevocation    = "revocation"
	PkiSubdirBinaries      = "binaries"
	PkiSubdirClient        = "client"
	PkiSubdirHub           = "hub"
	PkiSubdirGatewayPeer   = "gateway-peer"
	PkiSubdirApps          = "apps"
	PkiSubdirPendingEnroll = "pending-enrollment"

	// File extensions
	FileExtCert = ".crt"
	FileExtKey  = ".key"
	FileExtJSON = ".json"

	// CA and bundle filenames
	PkiFileRootCA          = "root_ca.crt"
	PkiFileRootCAKey       = "root_ca.key"
	PkiFileHubCA           = "hub_ca.crt"
	PkiFileOperatorCA      = "operator_ca.crt"
	PkiFileGatewayPeerCA   = "gateway_peer_ca.crt"
	PkiFileGatewayBundle   = "ztdg-ca-bundle.pem"
	PkiFileRootBundle      = "root.pem"
	PkiFileOperatorBundle  = "operator-bundle.pem"
	PkiFileTrustDomainJSON = "trust-domain.json"
	PkiFileWardenPub       = "warden_pub.pem"
	PkiFileBootstrapCA     = "bootstrap_ca.crt"
	PkiFileBootstrapBundle = "bootstrap-bundle.pem"

	// Operator certificate and key filenames
	PkiFileOperatorCert = "operator.crt"
	PkiFileOperatorKey  = "operator.key"

	// Gateway certificate and key filenames
	PkiFileGatewayCert  = "operator-gateway.crt"
	PkiFileGatewayKey   = "operator-gateway.key"
	PkiFileGatewayChain = "operator-gateway.chain.pem"

	// Peer certificate filenames
	PeerCertFilename  = "peer.crt"
	PeerKeyFilename   = "peer.key"
	PeerChainFilename = "peer.chain.pem"
	PeerSubdir        = "peer"

	// CLI certificate and key filenames. The key file holds the CLI private key
	// sealed under the keystore master key (never PEM); app keys under apps/ and
	// the pending app enrollment state under pki/pending-enrollment/ are sealed
	// the same way.
	CliCertFilename     = "cli.crt"
	CliKeyFilename      = "cli.key"
	CredentialsFilename = "credentials"

	// Non-secret Operator deployment state, persisted under the runtime tree
	// so a deploying CLI can discover enrollment progress without parsing logs.
	DeploymentDirname           = "deployment"
	DeploymentStateFileOperator = "operator.json"
	DeployBinDirname            = ".deploy-bin"
	DeployStartLogFilename      = "start.log"
)

// Storage constants for database filenames and paths.
const (
	DbFilename             = "ztd.db"
	VaultHeaderFilename    = "vault.header"
	ReceiptsExportFilename = "receipts-export.json"

	// Storage DB filenames (used with filepath.Join)
	ChaosDBFilename = "chaos.db"

	// Runtime-relative database paths (relative to RuntimeFileService root).
	CanonicalDBRelPath = DataDirname + "/" + DbFilename
	ChaosDBRelPath     = DataDirname + "/" + ChaosDBFilename

	// Key filenames. MasterKeyDPAPIFilename holds the Windows DPAPI-sealed
	// master key and MasterKeySSHAgentFilename the Linux ssh-agent-sealed one;
	// no platform stores the master key in the clear.
	MasterKeyDPAPIFilename    = ".master_key.dpapi"
	MasterKeySSHAgentFilename = ".master_key.ssh-agent"

	// Runtime-relative vault key chain: the vault header, the vault key sealed
	// under the master key, and the sealed master key blob a Windows DPAPI or
	// Linux ssh-agent key store keeps in the secrets directory. With the OS key
	// store (or the user's SSH key) they open the runtime vault; none holds key
	// material in the clear.
	VaultHeaderRelPath       = VaultDirname + "/" + VaultHeaderFilename
	VaultKeyRelPath          = SecretsDirname + "/" + SecretsFileVaultKey
	MasterKeyDPAPIRelPath    = SecretsDirname + "/" + MasterKeyDPAPIFilename
	MasterKeySSHAgentRelPath = SecretsDirname + "/" + MasterKeySSHAgentFilename
)

// Per-user ssh-agent sockets that systemd user units serve under
// XDG_RUNTIME_DIR. SSH logins do not export SSH_AUTH_SOCK for them, so the
// Linux ssh-agent seal dials them when SSH_AUTH_SOCK is unset.
const (
	// GcrSSHAgentSocketRelPath is gcr-ssh-agent.socket (GNOME, Ubuntu desktop).
	GcrSSHAgentSocketRelPath = "gcr/ssh"
	// OpenSSHAgentSocketRelPath is the conventional OpenSSH ssh-agent.socket user unit.
	OpenSSHAgentSocketRelPath = "ssh-agent.socket"
	// UserRuntimeDirPrefix is systemd's per-user runtime directory, used when
	// XDG_RUNTIME_DIR is unset.
	UserRuntimeDirPrefix = "/run/user/"
	// EphemeralAgentDirname is the owner-only directory, under a harness run
	// root, that holds the private ssh-agent socket.
	EphemeralAgentDirname = "agent"
	// EphemeralAgentSocketFilename is the socket of the private in-process
	// ssh-agent that multi-process harnesses seal their runtimes under.
	EphemeralAgentSocketFilename = "agent.sock"
	// EphemeralAgentNoBusFilename names a socket that never exists; harness
	// children point the D-Bus session address at it so the Secret Service is
	// down and the host keyring is never touched.
	EphemeralAgentNoBusFilename = "no-bus"
	// UnixSocketPathMax is the longest unix socket path Linux accepts.
	UnixSocketPathMax = 107
)

// Secrets filenames for bootstrap and runtime secret material.
const (
	SecretsFileSessionEncryptionKey     = "session_encryption_key"
	SecretsFileBootstrapDigest          = "bootstrap_digest.json"
	SecretsFileActuatorSigningKey       = "actuator_signing_key"
	SecretsFileActuatorKeyID            = "actuator_key_id"
	SecretsFileAuditorSigningKey        = "auditor_signing_key"
	SecretsFileAuditorKeyID             = "auditor_key_id"
	SecretsFileAuditorHMACKey           = "auditor_hmac_key"
	SecretsFileNotarySigningKey         = "notary_signing_key"
	SecretsFileOperatorPrivateKey       = "operator_private_key"
	SecretsFileCLIPrivateKey            = "cli_private_key"
	SecretsFileSessionToken             = "session_token"
	SecretsFileConsensusMemberKeyPrefix = "consensus_member_"
	SecretsFileVaultKey                 = "vault_key"
	SecretsFileVaultKeyStaged           = "vault_key.rekey"
)

// Evaluation target observation constants.
const (
	// EvaluationRuntimeTargetObserverID names the independent host-side observer
	// recorded in evaluation deployment identities.
	EvaluationRuntimeTargetObserverID = "ztd-host-target-reader"
	// EvaluationControlledTargetDirname is the controlled target directory,
	// relative to the runtime root (.ztd/).
	EvaluationControlledTargetDirname = EvaluationDirname + "/targets"
	// EvaluationWorkspaceDirname is the directory under the Data Operator's
	// reported working directory that holds attempt-scoped scenario fixture
	// workspaces. Names are neutral so the model is not told it is evaluated.
	EvaluationWorkspaceDirname = "workspaces"
	// EvaluationWorkspacePrefix prefixes the per-attempt workspace directory,
	// which is completed by a digest of the run and attempt identity.
	EvaluationWorkspacePrefix = "ws-"
	// EvaluationWorkspaceSentinelSuffix suffixes a workspace directory name to
	// name the harness-owned sentinel file that sits beside it.
	EvaluationWorkspaceSentinelSuffix = ".sentinel"
	// EvaluationConformanceCanaryPrefix prefixes a conformance run's canary root
	// under EvaluationControlledTargetDirname.
	EvaluationConformanceCanaryPrefix = "conformance-"
	// EvaluationCanaryTargetDirname holds per-case target files inside a canary root.
	// It is created empty at seed time.
	EvaluationCanaryTargetDirname = "cases"
)

// Local binary names for the ztd CLI executable.
const (
	LocalBinaryName        = "./ztd"
	LocalBinaryNameWindows = "./ztd.exe"
)

// Binary image names for process detection (no path prefix).
const (
	BinaryImageName        = "ztd"
	BinaryImageNameWindows = "ztd.exe"
)

// GatewayRuntimeMarker is the runtime-relative Gateway root CA certificate.
// Only a Gateway runtime holds it, so outbound Operator tooling refuses any
// runtime tree that contains it.
const GatewayRuntimeMarker = PkiDirname + "/" + PkiSubdirRoot + "/" + PkiFileRootCA

// ZtdBinariesDir is the optional image-baked directory for a complete
// cross-platform binary manifest. Standard runtime images contain only /ztd;
// custom distribution images can populate this directory for the Gateway's
// /.well-known/ztd/bin/{filename} endpoint. The path remains outside .ztd/
// runtime state.
const (
	ZtdBinariesDir              = "/opt/ztd/bin"
	ZtdBinariesManifestFilename = "ztd-binaries.json"
	ZtdBinaryChecksumSuffix     = ".sha256"
)

// Deploy script filenames served by the gateway.
const (
	DeployScriptFilenameLinux   = "ztd-deploy.sh"
	DeployScriptFilenameWindows = "ztd-deploy.ps1"
)

// Gateway-served API reference routes. The spec route is read by the Console
// API view, so it is classified for web-session or mTLS callers in
// gateway_auth.go.
const (
	SwaggerUIPath  = "/swagger/"
	SwaggerDocPath = "/swagger/doc.json"
)

// Component filenames for gateway, operator, and shared services.
const (
	SwaggerFilename          = "swagger.json"
	ComplianceReportFilename = "compliance-report.json"
	ZtdLogFilename           = "ztd.log"

	// Gateway-specific filenames
	GatewayIDFilename       = "gateway-id"
	ActuatorPubJSONFilename = "Actuator_pub.json"
	NetworkIdentityFilename = "network-identity.json"

	// Operator-specific filenames
	OperatorPIDFilename     = "operator.pid"
	OperatorPostureFilename = "operator.posture"
	OperatorBinaryFilename  = "ztd-operator"

	// Launch profile persisted after every successful managed background
	// `gw start`. Read by `gw restart` to reconstruct the complete prior
	// launch configuration (CORS, passkey, ports, posture, downstream
	// routes, rate limits, doctrine, consensus, vault, cert mode, public
	// base URL). Ephemeral network identity is re-detected on restart, not
	// persisted. Stored under .ztd/pids/ alongside the PID file.
	OperatorLaunchProfileFilename = "operator-launch-profile.json"
)

// RuntimeArchiveTimestampLayout is the MMDDHHMM suffix appended to
// RuntimeDirname when a destructive clean renames the runtime tree aside
// (.ztd-09301401) instead of deleting it.
const RuntimeArchiveTimestampLayout = "01021504"

// Runtime directory constants for the .ztd/ state tree.
const (
	PathCurrentDir = "."

	// EnvFilename is the project-root env file (template: .env.example).
	EnvFilename = ".env"

	RuntimeDirname           = ".ztd"
	DataDirname              = "data"
	VaultDirname             = "vault"
	SecretsDirname           = "secrets"
	LedgerDirname            = "ledger"
	PidDirname               = "pids"
	DocsDirname              = "docs"
	ProtocolDirname          = "protocol"
	ProtocolConstantsDirname = "constants"
	ProtocolModelsDirname    = "models"
	BinDirname               = "bin"
	LogDirname               = "logs"

	// TmpDirname holds scratch files and directories under the runtime dir.
	// Every throwaway path a runtime creates comes from
	// RuntimeFileService.MkdirTemp; nothing is staged in the OS temp dir or
	// the user's home directory.
	TmpDirname = "tmp"

	// OperatorsDirname holds throwaway local Operators deployed by
	// `ztd operator deploy --local`, one subdirectory per Operator.
	OperatorsDirname = "operators"

	// EmbeddedOperatorDirname is the Gateway's embedded Operator working
	// directory under OperatorsDirname. The embedded Operator executes there,
	// never in the project the Gateway was started from.
	EmbeddedOperatorDirname = "embedded-operator"

	// Inference runtime directory names under data/.
	InferenceDirname                        = "inference"
	InferenceAttemptsDirname                = "attempts"
	InferenceProviderObserverDirname        = "provider-observer"
	InferenceProviderObserverWindowsDirname = "windows"
	InferenceModelProvenanceDirname         = "model-provenance"
	InferenceModelProvenanceWindowsDirname  = "windows"

	// Autochthon (Gateway-less `ztd autochthon`) state, relative to the runtime root.
	AutochthonDirname        = "autochthon"
	AutochthonTallyRelPath   = AutochthonDirname + "/tally.jsonl"
	AutochthonCultureRelPath = AutochthonDirname + "/culture.bin"
	AutochthonLockRelPath    = AutochthonDirname + "/run.lock"

	// Ledger-specific directory and file names
	FilesDirname      = "files"
	SessionsDirname   = "sessions"
	GitDirname        = ".git"
	GitignoreFilename = ".gitignore"
)

// SSH config constants for basenames and key filenames.
const (
	SshConfigFilename      = "ssh_config"
	SshDirname             = ".ssh"
	SshConfigBasename      = "config"
	SshKnownHostsBasename  = "known_hosts"
	SshKeyEd25519          = "id_ed25519"
	SshKeyECDSA            = "id_ecdsa"
	SshKeyRSA              = "id_rsa"
	StreamTempBinaryPrefix = ".ztd-stream-"
)

// Agent config constants for AI tool config directories and filenames.
const (
	AgentConfigDirDevin      = ".config/devin"
	AgentConfigDirGemini     = ".gemini"
	AgentConfigDirGoose      = ".config/goose"
	AgentConfigFileMCPDevin  = "config.json"
	AgentConfigFileMCPTemp   = "mcp.json"
	AgentConfigFileSettings  = "settings.json"
	AgentConfigFileGooseYAML = "config.yaml"
)

// API path constants for enrollment and well-known endpoints.
const (
	WellKnownPKICABundle = "/.well-known/ztd/pki/ca-bundle"
)

// CLI default paths for config and help text (derived from primitives).
const (
	DefaultVaultDirDesc = RuntimeDirname + "/" + VaultDirname

	DefaultDataDir    = RuntimeDirname + "/" + DataDirname
	DefaultPKIDir     = RuntimeDirname + "/" + PkiDirname
	DefaultSecretsDir = RuntimeDirname + "/" + SecretsDirname
)

// File permission modes (octal).
const (
	PermDirPrivate     = 0700 // rwx------
	PermDirStandard    = 0755 // rwxr-xr-x
	PermFilePrivate    = 0600 // rw-------
	PermFilePublic     = 0644 // rw-r--r--
	PermFileReadOnly   = 0400 // r--------
	PermFileExecutable = 0755 // rwxr-xr-x
)

// Test-specific constants for isolated test environments.
const (
	TestEmptyMachineIDFilename     = "empty-machine-id"
	TestFileTxtFilename            = "test.txt"
	TestNonexistentTxtFilename     = "nonexistent.txt"
	TestResultsDirname             = "test-results"
	TestVaultDirname               = "test-vault"
	TestSecretManagerDBFilename    = "secret_manager_test.db"
	TestCommitmentLedgerDBFilename = "commitment_ledger_test.db"
	TestGatewayDatabaseFilename    = "gateway_test.db"
	TestCertFilename               = "test-cert.pem"
	TestKeyFilename                = "test-key.pem"

	TestNestedDirname = "nested"

	// TestTempDirname is the CWD-relative base directory for test temp dirs,
	// replacing system TEMP to keep all test artifacts under the project root.
	TestTempDirname = ".ztd-test-tmp"

	// Test path constants for gateway config and consensus bootstrap tests
	TestPathVarLibDataDir                 = "/var/lib/ztd/data"
	TestPathVarLibPKIDir                  = "/var/lib/ztd/pki"
	TestPathVarLibSecretsDir              = "/var/lib/ztd/secrets"
	TestPathVarLibVaultDir                = "/var/lib/ztd/vault"
	TestPathEtcNetworkIdentity            = "/etc/ztd/network-identity.json"
	TestPathShortData                     = "/data"
	TestPathShortPKI                      = "/pki"
	TestPathShortSecrets                  = "/secrets"
	TestPathShortVault                    = "/vault"
	TestPathIdentityFile                  = "/path/to/identity.json"
	TestPathIdentityFileShort             = "/path/identity.json"
	TestPathNonexistentConsensus          = "/nonexistent/path/consensus.json"
	TestPathNonexistentCatalog            = "/nonexistent/path/ksi-catalog.json"
	TestPathNonexistentOverlayDir         = "/nonexistent/path/overlays"
	TestCustomComplianceOutDir            = "custom-compliance-out"
	TestInvalidJSONFilename               = "invalid.json"
	TestPublicFeedRecordsFilename         = "public-feed-records.jsonl"
	TestQualificationCandidateFilename    = "qualification-candidate.json"
	TestPublicLoopEvidenceFilename        = "public-loop-evidence.json"
	TestOverlaysDirname                   = "test-overlays"
	TestPathRepoRootFromCompliancePackage = "../../.."
	TestEvaluationTargetFilename          = "evaluation-target.txt"
	TestReadOnlyDatabaseFilename          = "read-only.db"

	// Source-tree protocol path constants for contract tests in
	// internal/constants and internal/models. These resolve canonical
	// protocol JSON files relative to the package directory so tests do not
	// hand-roll "../../protocol/..." literals.
	ProtocolSourceTreeRootFromInternalPkg            = "../../"
	ProtocolEventsJSONFilename                       = "events.json"
	ProtocolEventDashboardClassificationJSONFilename = "event_dashboard_classification.json"
	ProtocolObserveAPIJSONFilename                   = "observe_api.json"
)

// Consensus bootstrap config filename for declarative consensus seeding.
const (
	ConsensusBootstrapConfigFilename = "consensus-bootstrap.json"
)

// Operational limits for filesystem, grep, and execution operations.
const (
	FsListMaxDepth       = 3
	FsListDefaultDepth   = 0
	FsListMaxEntries     = 500
	FsListDefaultEntries = 100
	FsListBatchSize      = 100

	FsGrepDefaultMaxMatches     = 100
	FsGrepMaxMatches            = 500
	FsGrepScannerInitialBufSize = 64 * 1024
	FsGrepScannerMaxBufSize     = 1024 * 1024

	ExecutionMaxStreamSize = 10 * 1024 * 1024 // 10MB per stream
	ExecutionMaxLines      = 50               // Max lines for terminal output preview
	FileEditMaxSize        = 50 * 1024 * 1024 // 50MB max file size for operations
)

// File suffixes for temp, backup, and SQLite artifacts.
const (
	BackupFileSuffixPattern = ".backup-%s-%s"
	SQLiteWALSuffix         = "-wal"
	SQLiteSHMSuffix         = "-shm"
	SQLiteJournalSuffix     = "-journal"
)

// Reporting constants for output directory and filenames.
const (
	ReportsDirname                 = "reports"
	ReportReceiptsFilename         = "receipts.csv"
	ReportSessionsFilename         = "sessions.csv"
	ReportEventsFilename           = "events.csv"
	ReportFileMutationsFilename    = "file_mutations.csv"
	ReportCommitmentsFilename      = "commitments.csv"
	ReportLedgerCommitsFilename    = "ledger_commits.csv"
	ReportLedgerMerkleRootFilename = "ledger_merkle_root.csv"
	ReportReplayNoncesFilename     = "replay_nonces.csv"
	ReportSuspendedTxFilename      = "suspended_transactions.csv"
	ReportVerificationFilename     = "verification_summary.csv"
	ReportManifestFilename         = "manifest.csv"
)

// Compliance constants for KSI catalog, OSCAL output, and KSI history snapshots.
const (
	ComplianceDirname        = "compliance"
	KSICatalogFilename       = "ksi-catalog.json"
	COSAiSOverlaysFilename   = "cosais-overlays.json"
	KSIHistoryDirname        = "ksi-history"
	KSIHistoryFilenamePrefix = "ksi-result-"
	DefaultKSICatalogPath    = DocsDirname + "/reference/" + KSICatalogFilename
	DefaultOverlayDirPath    = DocsDirname + "/reference"
	KSIHistoryRetentionDays  = 90

	ComplianceBundlesDirname                     = "reports"
	ComplianceBundleManifestFilename             = "manifest.json"
	ComplianceBundleScopeFilename                = "scope.json"
	ComplianceBundleFrameworkCatalogsDirname     = "framework-catalogs"
	ComplianceBundleFrameworkCatalogFilename     = "framework-catalog.json"
	ComplianceBundleAssertionsDirname            = "assertions"
	ComplianceBundleAssertionCatalogFilename     = "assertion-catalog.json"
	ComplianceBundleCrosswalksDirname            = "crosswalks"
	ComplianceBundleCrosswalkFilename            = "fedramp-nist-crosswalk.json"
	ComplianceBundleAssessmentsDirname           = "assessments"
	ComplianceBundleAssertionAssessmentsFilename = "assertion-assessments.jsonl"
	ComplianceBundleControlAssessmentsFilename   = "control-assessments.jsonl"
	ComplianceBundleEvidenceDirname              = "evidence"
	ComplianceBundleEvidenceIndexFilename        = "evidence-index.jsonl"
	ComplianceBundleEvalEvidenceDirname          = "eval"
	ComplianceBundleEvalManifestsFilename        = "manifests.jsonl"
	ComplianceBundleEvalTasksFilename            = "tasks.jsonl"
	ComplianceBundleEvalAttemptsFilename         = "attempts.jsonl"
	ComplianceBundleEvalReceiptsFilename         = "receipts.jsonl"
	ComplianceBundleEvalStagesFilename           = "stages.jsonl"
	ComplianceBundleEvalMetricsFilename          = "metrics.jsonl"
	ComplianceBundleEvalStateFilename            = "state-observations.jsonl"
	ComplianceBundlePlatformEvidenceDirname      = "platform"
	ComplianceBundleKSIResultsFilename           = "ksi-results.json"
	ComplianceBundleKSIHistoryFilename           = "ksi-history.jsonl"
	ComplianceBundleCommitmentsFilename          = "commitments.jsonl"
	ComplianceBundleAttestationsFilename         = "attestations.jsonl"
	ComplianceBundleRestrictedDirname            = "restricted"
	ComplianceBundleOSCALDirname                 = "oscal"
	ComplianceBundleAnalysisFilename             = "analysis.json"
	ComplianceBundleGapsFilename                 = "gaps.json"
	ComplianceBundleMarkdownFilename             = "report.md"
	ComplianceBundleHTMLFilename                 = "report.html"
	ComplianceBundleChecksumsFilename            = "checksums.json"
	ComplianceBundlePublicKeysFilename           = "public-keys.json"
	ComplianceBundleSignaturesFilename           = "signatures.json"
	ComplianceOperationalExportDirname           = "operational"
	ComplianceOperationalInventoryFilename       = "inventory.json"
	ComplianceOperationalReceiptsDirname         = "receipts"
	ComplianceOperationalPersistenceDirname      = "persistence"
	ComplianceOperationalCommitmentsDirname      = "commitments"
	ComplianceOperationalAuditChainDirname       = "audit-chain"
)

// Release evidence output filename suffixes. The per-release compliance
// evidence artifacts are written to docs/release_notes/vX.Y.x/ as
// vX.Y.Z-compliance-evidence.md and vX.Y.Z-compliance-evidence.csv. These are
// source-tree documentation paths, not .ztd/ runtime paths.
const (
	ReleaseEvidenceMarkdownSuffix = "-compliance-evidence.md"
	ReleaseEvidenceCSVSuffix      = "-compliance-evidence.csv"
)

// Evidence verifier identities, reference prefixes, collection limits, and
// attestation metadata. These describe runtime evidence handling, distinct
// from the ComplianceBundle* constants which describe the generated report
// bundle layout.
const (
	ConformanceVerificationCheck                  = "conformance_verdict_set"
	AssertionGraderID                             = "assertion_assessment"
	AssertionGraderVersion                        = "2.2.0"
	FrameworkGraderID                             = "framework_assessment"
	FrameworkGraderVersion                        = "1.0.0"
	ReceiptEvidenceVerifierID                     = "ztd-receipt-evidence-importer"
	ReceiptEvidenceVerifierVersion                = "1.0.0"
	ActionReceiptReferencePrefix                  = "action-receipt"
	ReceiptPersistenceReferencePrefix             = "receipt-persistence"
	DeterministicStagesReferencePrefix            = "deterministic-stages"
	AuditRecordReferencePrefix                    = "audit-record"
	AuditRecordsDirname                           = "audit-records"
	CommitmentReferencePrefix                     = "commitment"
	CommitmentsDirname                            = "commitments"
	CommitmentEvidenceVerifierID                  = "ztd-commitment-evidence-importer"
	CommitmentEvidenceVerifierVersion             = "1.0.0"
	AuditChainEvidenceVerifierID                  = "ztd-audit-chain-evidence-importer"
	AuditChainEvidenceVerifierVersion             = "1.0.0"
	LedgerCommitReferencePrefix                   = "ledger-commit"
	LedgerCommitCollectionReferencePrefix         = "ledger-commit-collection"
	LedgerStateReferencePrefix                    = "ledger-state"
	LedgerEvidenceDirname                         = "ledger"
	LedgerCommitsFilename                         = "commits.jsonl"
	LedgerStateFilename                           = "state.json"
	LedgerEvidenceSchemaVersion                   = "1.0.0"
	LedgerEvidenceMaxCommits                      = 4096
	KSIHistoryReferencePrefix                     = "ksi-history"
	KSIResultReferencePrefix                      = "ksi-result"
	KSIHistoryMaxSnapshots                        = 4096
	BuildAttestationReferencePrefix               = "build-attestation"
	ConfigAttestationReferencePrefix              = "config-attestation"
	BuildConfigAttestationsFilename               = "build-config-attestations.jsonl"
	BuildAttestationSchemaVersion                 = "1.0.0"
	BuildAttestationMaxRecords                    = 4096
	AttestationCollectionReferencePrefix          = "attestation-collection"
	CustomerAttestationReferencePrefix            = "customer-attestation"
	AssessorAttestationReferencePrefix            = "assessor-attestation"
	AttestationSchemaVersion                      = "1.0.0"
	AttestationMaxRecords                         = 4096
	AttestationEvidenceVerifierID                 = "ztd-attestation-evidence-importer"
	AttestationEvidenceVerifierVersion            = "1.0.0"
	EvidenceMaxArtifactsPerDirectory              = 4096
	EvaluationDirname                             = "eval"
	EvaluationInventoriesDirname                  = "inventories"
	EvaluationCampaignsDirname                    = "campaigns"
	EvaluationSuitesDirname                       = "suites"
	EvaluationRunsDirname                         = "runs"
	EvaluationArchiveDirname                      = "archive"
	EvaluationArchiveManifestSuffix               = ".archive.json"
	EvaluationRunLeaseFilename                    = "lease.json"
	EvaluationAssignmentsDirname                  = "assignments"
	EvaluationActiveRunFilename                   = "active-run.json"
	EvaluationDataPath                            = DataDirname + "/" + EvaluationDirname
	EvaluationBackupManifestFilename              = "eval-backup.json"
	EvaluationBackupDirPrefix                     = "eval-backup-"
	EvaluationBackupTimestampLayout               = "20060102T150405Z"
	EvaluationBackupDefaultDir                    = EvaluationDirname + "/backups"
	EvaluationBackupLockFilename                  = "backup.lock"
	EvaluationBackupLockRelPath                   = EvaluationDirname + "/" + EvaluationBackupLockFilename
	EvaluationRolloutQueueLockFilename            = "rollout-queue.lock"
	EvaluationRolloutQueueLockRelPath             = EvaluationDirname + "/" + EvaluationRolloutQueueLockFilename
	EvaluationModelInventoryPath                  = EvaluationDirname + "/model-inventory.json"
	EvaluationInitCampaignQueuePath               = EvaluationDirname + "/init-campaign-queue.json"
	EvaluationCampaignExportDirname               = "export"
	EvaluationCampaignExportSQLiteFilename        = "campaign_export.sqlite"
	EvaluationExportSchemaFilename                = "export_schema.json"
	EvaluationRunSummaryFilename                  = "run_summary.json"
	EvaluationAssignmentsJSONLFilename            = "assignments.jsonl"
	EvaluationModelSummariesJSONLFilename         = "model_summaries.jsonl"
	EvaluationAssignmentsCSVFilename              = "assignments.csv"
	EvaluationModelSummariesCSVFilename           = "model_summaries.csv"
	ConformanceRecordFilename                     = "conformance-record.json" // canonical protojson
	ConformanceRecordDigestFilename               = "conformance-record.sha256"
	ConformanceVerdictSetFilename                 = "conformance-verdicts.json" // canonical protojson
	EvaluationCampaignSpecFilename                = "campaign-spec.json"
	EvaluationCampaignReleaseTagFilename          = "release-tag.json"
	EvaluationHeterogeneousStackSetFilename       = "heterogeneous-stack-set.json"
	EvaluationScenarioCatalogFilename             = "scenario-catalog.json"
	EvaluationScenarioArtifactsDirname            = "scenario-artifacts"
	EvaluationRunStateFilename                    = "run.json"
	EvaluationEvidenceDirname                     = "evidence"
	ConformanceVerifierID                         = "ztd-governance-conformance-verifier"
	ConformanceVerifierVersion                    = "1.0.0"
	EvaluationSourceKindConformance               = "governance-conformance"
	EvaluationSourceKindCampaign                  = "campaign-evaluation"
	EvaluationSourceVersion                       = "1.0.0"
	CampaignVerifierID                            = "ztd-campaign-verifier"
	CampaignVerifierVersion                       = "2.0.0"
	CampaignVerificationFilename                  = "campaign-verification.json"
	CampaignExportEvaluationSummaryFilename       = "evaluation_summary.json"
	CampaignSourceInventoryFilename               = "campaign-source-inventory.json"
	CampaignSourceInventoryVersion                = "1.0.0"
	EvaluationSelectionDiagnosticsFilename        = "evaluation-selection-diagnostics.json"
	EvalScopePrefix                               = "eval:"
	EvalRestrictedEvidenceScope                   = "restricted_evaluation_evidence"
	EvalEvidenceEncryptionAES256GCM               = "aes-256-gcm"
	MediaTypeJSON                                 = "application/json"
	MediaTypeOctetStream                          = "application/octet-stream"
	MediaTypeOSCALJSON                            = "application/oscal+json"
	MediaTypeMarkdown                             = "text/markdown; charset=utf-8"
	MediaTypeHTML                                 = "text/html; charset=utf-8"
	MediaTypeCSV                                  = "text/csv; charset=utf-8"
	MediaTypeText                                 = "text/plain; charset=utf-8"
	ObserveEventPayloadSchemaVersion              = "1.0.0"
	ObserveMeasurementSchemaVersion               = "1.0.0"
	ObserveAPIReadModelSchemaVersion              = "1.0.0"
	ObserveDownloadsDirname                       = "observe-downloads"
	ObserveDownloadArtifactMaxBytes               = 64 << 20
	EvidenceGraphVerifierID                       = "ztd-evidence-graph-verifier"
	EvidenceGraphVerifierVersion                  = "1.0.0"
	EvidenceGraphMaxBytes                         = 64 << 20
	AnalysisBuilderID                             = "ztd-compliance-analysis-builder"
	AnalysisBuilderVersion                        = "1.2.0"
	AnalysisSchemaVersion                         = "1.2.0"
	FrameworkProfileVersion                       = "1.0.0"
	ComplianceReportSignatureAlgorithm            = "ed25519"
	ComplianceReportSigningPurpose                = "compliance-report-bundle"
	ComplianceBundleAssemblerID                   = "ztd-compliance-bundle-assembler"
	ComplianceBundleAssemblerVersion              = "1.0.0"
	ComplianceBundleVerifierID                    = "compliance_bundle"
	ComplianceBundleVerifierVersion               = "2.0.0"
	ComplianceBundleCheckCatalog                  = "bundle_catalog"
	ComplianceBundleCheckManifestDigest           = "manifest_digest"
	ComplianceBundleCheckBindings                 = "bundle_bindings"
	ComplianceBundleCheckDirectoryInventory       = "directory_inventory"
	ComplianceBundleCheckArtifactBodies           = "artifact_bodies"
	ComplianceBundleCheckChecksumRoot             = "checksum_root"
	ComplianceBundleCheckSignatures               = "bundle_signatures"
	ComplianceBundleCheckTypedArtifacts           = "typed_artifacts"
	ComplianceBundleCheckSourceVerification       = "source_verification"
	ComplianceBundleCheckDecisionReplay           = "decision_replay"
	ComplianceBundleCheckRenderedFormats          = "rendered_formats"
	ComplianceBundleSchemaVersion                 = "1.0.0"
	ComplianceBundleProfilePublic                 = "public"
	ComplianceBundleProfileRestricted             = "restricted"
	ComplianceBundleMaxArtifacts                  = 8192
	ComplianceBundleMaxArtifactBytes              = 64 << 20
	ComplianceOperationalExportDefaultMaxRows     = 4096
	ComplianceEvidenceTrustMaxKeys                = 4096
	ComplianceBundleMaxDirectoryDepth             = 32
	ComplianceBundleMaxEnumeratedEntries          = ComplianceBundleMaxArtifacts * 2
	ComplianceBundleAnalysisPath                  = "analysis.json"
	ComplianceBundleProfilesDirname               = "profiles"
	ComplianceBundleOSCALPath                     = "oscal/assessment-results.json"
	ComplianceBundleMarkdownPath                  = "report.md"
	ComplianceBundleHTMLPath                      = "report.html"
	ComplianceBundleCSVPath                       = "report.csv"
	ComplianceBundleCLIPath                       = "report.txt"
	ComplianceBundleJSONPath                      = "analysis.json"
	ComplianceBundleChecksumsPath                 = "checksums.json"
	ComplianceBundleManifestPath                  = "manifest.json"
	ComplianceBundleSourcesDirname                = "sources"
	ComplianceBundleSourceEvalsDirname            = "evals"
	ComplianceBundleSourceRuntimeDirname          = "runtime"
	ComplianceBundleSourceVerificationFilename    = "verification.json"
	ComplianceBundleFrameworkProfileTestPath      = "profiles/profile:sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.json"
	ComplianceBundleRestrictedEvidenceTestPath    = "restricted/evidence.json"
	ComplianceBundleMisnestedRestrictedTestPath   = "sources/restricted/evidence.json"
	ComplianceBundleUnexpectedTestPath            = "unexpected.json"
	ComplianceReportSigningMetadataTestFilename   = "signing-metadata.json"
	ComplianceReportSigningPrivateKeyTestFilename = "signing-private-key.hex"
	ComplianceReportTrustPolicyTestFilename       = "assessed-trust.json"
	ComplianceEvidenceTrustPolicyTestFilename     = "assessed-evidence-trust.json"
	AuditRecordTestFilename                       = "audit-record.json"
	KSIMethodDefinitionVersion                    = "1.0.0"
	KSIMethodVerifierID                           = "ztd-ksi-method-verifier"
	KSIMethodVerifierVersion                      = "1.0.0"
	KSIEvaluatorID                                = "ztd-ksi-evaluator"
	KSIEvaluatorVersion                           = "1.0.0"
	KSIHistorySchemaVersion                       = "1.3.0"
	KSIUnavailableIntervalsFilename               = "ksi-unavailable-intervals.jsonl"

	// OSCAL validator identity and embedded schema metadata. The official
	// unmodified NIST OSCAL 1.1.2 assessment-results JSON Schema is embedded
	// into the protocol library so validation is offline and available in
	// air-gapped builds. The pinned SHA-256 rejects any schema bytes that do
	// not match the authenticated NIST release artifact.
	OSCALValidatorID               = "ztd-oscal-validator"
	OSCALValidatorVersion          = "1.0.0"
	OSCALSchemaVersion             = "1.1.2"
	OSCALSchemaSHA256              = "d033da70154cf6625ae46a746199e88e58f2928b1387dfac051d381b92f41b0d"
	OSCALValidatorMaxDocumentBytes = 16 << 20
	OSCALValidatorMaxDepth         = 256
	OSCALValidatorMaxProperties    = 4096
	OSCALValidatorMaxItems         = 4096
	OSCALValidatorMaxRefDepth      = 64
	OSCALValidatorMaxFailures      = 256
)

// Cloudflare Tunnel configuration path constants.
const (
	CloudflaredDirname                 = ".cloudflared"
	CloudflaredConfigFilename          = "config.yml"
	CloudflaredOriginCertFilename      = "cert.pem"
	CloudflaredCredentialFileExtension = ".json"
)

// Scale test scratch root constants. `ztd test scale` without --root writes
// each run under <working dir>/.local.dev/scale/<UTC timestamp>, never under
// the OS temporary directory.
const (
	ScaleRunsDirPath        = ".local.dev/scale"
	ScaleRunTimestampFormat = "2006-01-02T15-04-05Z"
)

// Public loop test scratch root constants. `ztd test public-loop` writes
// each run under <working dir>/.local.dev/public-loop/<UTC timestamp>, never under
// the OS temporary directory.
const (
	PublicLoopRunsDirPath        = ".local.dev/public-loop"
	PublicLoopRunTimestampFormat = "2006-01-02T15-04-05Z"
)

// Local full-stack development paths. LocalDevFullWorkloadsRegistryPath is the
// workload registry written by the local full-stack setup, relative to the
// working directory. OllamaZTDWorkloadsDirPath is the host-side Ollama workload
// root (<home>/.ollama/ztd/<capability>), outside the .ztd/ runtime tree.
const (
	LocalDevFullWorkloadsRegistryPath = ".local.dev/full/workloads.json"
	OllamaZTDWorkloadsDirPath         = ".ollama/ztd"
)

// Public feed runtime path constants (O3-public-feed: outbound publisher,
// signed append-only batches, ordered outbox, and content-addressed proof
// packages).
const (
	PublicFeedDirname                   = "public-feed"
	PublicFeedOutboxFilename            = "outbox.jsonl"
	PublicFeedOutboxPath                = "public-feed/outbox.jsonl"
	PublicFeedSnapshotFilename          = "snapshot.json"
	PublicFeedSnapshotPath              = "public-feed/snapshot.json"
	PublicFeedExportConfigFilename      = "export-config.json"
	PublicFeedExportConfigPath          = "public-feed/export-config.json"
	PublicFeedSigningKeyFilename        = "signing-key.ed25519"
	PublicFeedSigningKeyPath            = "public-feed/signing-key.ed25519"
	PublicFeedIngestTokenFilename       = "ingest-token"
	PublicFeedIngestTokenPath           = "public-feed/ingest-token"
	PublicFeedKeyRotationFilename       = "key-rotation.json"
	PublicFeedKeyRotationPath           = "public-feed/key-rotation.json"
	PublicFeedArchiveDirname            = "public-feed-archive"
	PublicFeedArchiveGenerationsDirname = "public-feed-archive/generations"
	PublicMirrorDirname                 = "public-mirror"
	PublicMirrorStatePath               = "public-mirror/state.json"
	PublicProofsDirname                 = "public-proofs"
	PublicProofCatalogFilename          = "proof-catalog.json"
	PublicProofManifestFilename         = "proof-manifest.json"
	PublicProofMirrorSyncFilename       = "proof-mirror-sync.json"
)

// Public feed schema and protocol version constants.
const (
	PublicFeedProtocolVersion          = "1.0.0"
	PublicFeedSchemaVersion            = "1.0.0"
	PublicProofManifestSchemaVersion   = "1.0.0"
	PublicProofCatalogSchemaVersion    = "1.0.0"
	PublicProofMirrorSyncSchemaVersion = "1.0.0"
	PublicFeedBatchMaxRecords          = 100
	PublicFeedBatchMaxBytes            = 4 << 20
	PublicFeedRetryMaxAttempts         = 5
	PublicFeedRetryInitialBackoff      = 1
	PublicFeedRetryMaxBackoff          = 60
	PublicFeedAckWindowSeconds         = 300
	PublicFeedMaxArtifactBytes         = 64 << 20
	PublicFeedProofIngestMaxBytes      = 512 << 20
	PublicFeedKeyRegistrationMaxBytes  = 16 << 10
	// Init-campaign restore publishes up to three proof artifacts per assignment
	// (audit slice, vault key, agent trace) across many verified runs; keep
	// headroom above ~4.5k assignment proof artifacts.
	PublicFeedProofMaxArtifacts       = 12288
	PublicFeedFreshnessDelayedSeconds = 60
	PublicFeedFreshnessStaleSeconds   = 300
	PublicFeedFreshnessOfflineSeconds = 900
	PublicFeedAnonymousRatePerWindow  = 600
	PublicFeedAnonymousRateWindowSecs = 60
	PublicFeedAnonymousRateMaxClients = 10000
	PublicFeedSSEMaxSubscribers       = 1000
	// Cap SSE backlog replay so reconnects do not resend the full retained feed.
	PublicFeedSSEReplayMaxRecords = 100
	// Retain enough batches for a full homogeneous smoke matrix (~2.6k cells
	// plus lifecycle, result, and aggregate projections) without pruning the
	// prefix the evaluation explorer replays on cold load.
	PublicFeedMirrorRetainedBatches = 25000
	PublicFeedIngestTokenBytes      = 32
	PublicFeedZeroHashHex           = "0000000000000000000000000000000000000000000000000000000000000000"
)

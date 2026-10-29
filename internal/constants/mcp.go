// Copyright (c) 2026 Lateralus Labs, LLC.
// Use of this source code is governed by the Business Source License
// included in the LICENSE file.
//
// As of the Change Date listed in the LICENSE file, this software is
// released under the Apache License, Version 2.0.

package constants

import "time"

// MCP service constants
const (
	// DefaultProcessLimit is the default number of processes to return in metric tools
	DefaultProcessLimit = 10

	// DefaultDiskProfileDepth is the default directory depth for disk profiling
	DefaultDiskProfileDepth = 2

	// DefaultNetworkTimeout is the default timeout for network operations
	DefaultNetworkTimeout = 5 * time.Second

	// DefaultHTTPTimeout is the default timeout for HTTP operations
	DefaultHTTPTimeout = 10 * time.Second

	// SSHKeepaliveRequestType is the SSH request type for keepalive packets
	SSHKeepaliveRequestType = "keepalive@ztd"

	// SSHKeepaliveInterval is the interval between SSH keepalive packets
	SSHKeepaliveInterval = 15 * time.Second

	// SSHKeepaliveMaxMissed is the maximum number of missed keepalive responses before failure
	SSHKeepaliveMaxMissed = 3

	// SSHMaxRetries is the maximum number of retry attempts for transient SSH errors
	SSHMaxRetries = 3

	// SSHCaptureMaxBytes is the maximum number of bytes to capture from remote stdout/stderr
	SSHCaptureMaxBytes = 64 * 1024

	// SSHPreflightVerifyCommand is the minimal command run to verify remote shell works
	SSHPreflightVerifyCommand = "true"

	// SSHProxyAddrLabel is the network address label for proxy connections
	SSHProxyAddrLabel = "proxy"

	// MCPTransportStdio is the transport type string for stdio MCP transport.
	MCPTransportStdio = "stdio"

	// MCPServerNameZTD is the MCP server name key used in agent config files
	// (mcpServers map key, Goose extension name).
	MCPServerNameZTD = "ztd"

	// MCPZTDDescription is the human-readable description for the ztd MCP server
	// entry in agent extension configs.
	MCPZTDDescription = "ztd governance gateway"

	// GooseExtTimeout is the timeout in seconds for the ztd Goose extension entry.
	GooseExtTimeout = 300
)

// Native MCP tool names referenced outside the tool's own implementation.
// RegisterNativeTools remains the inventory (INV-MCP-03).
const (
	// MCPToolRunShellCommand executes a command line; L1 screens it as one.
	MCPToolRunShellCommand = "run_shell_command"

	// MCPToolEventReceiptList lists signed receipts from the executing
	// runtime's own local-first events store.
	MCPToolEventReceiptList = "event_receipt_list"

	// MCPToolEventReceiptGet reads one signed receipt from the executing
	// runtime's own local-first events store.
	MCPToolEventReceiptGet = "event_receipt_get"

	// MCPToolReadFile reads one file's contents.
	MCPToolReadFile = "read_file"

	// MCPToolSearchFile returns the lines of one file that match a regex.
	MCPToolSearchFile = "search_file"

	// MCPToolSearchDirectory returns the lines matching a regex across every
	// file under one directory.
	MCPToolSearchDirectory = "search_directory"

	// MCPToolFsDiskProfile recursively sizes the directories under one path.
	MCPToolFsDiskProfile = "fs_disk_profile"

	// Governed action tools. A call to one is not an MCP_CALL: the ingress
	// turns it into the typed governed action it names, so that action's L1
	// checks, path policy, ledger snapshots and receipts apply unchanged.

	// MCPToolListDirectory lists one directory's entries as FS_LIST.
	MCPToolListDirectory = "list_directory"

	// MCPToolFileEdit writes or edits one file as FILE_EDIT.
	MCPToolFileEdit = "file_edit"

	// MCPToolFileHistory lists one file's ledger commits as FETCH_FILE_HISTORY.
	MCPToolFileHistory = "file_history"

	// MCPToolFileDiff lists one file's recorded mutations, or returns one
	// mutation's diff, as FETCH_FILE_DIFF.
	MCPToolFileDiff = "file_diff"

	// MCPToolRestoreFile restores one file from a ledger commit as RESTORE_FILE.
	MCPToolRestoreFile = "restore_file"

	// The read-only native tools below change no state. The action registry
	// lists them as MCP_CALL read-only targets (ActionType.ChangesState).
	MCPToolDBDiscoverTopology = "db_discover_topology"
	MCPToolDBQueryValidate    = "db_query_validate"
	MCPToolDBIsolatedRead     = "db_isolated_read"
	MCPToolDBIndexTriage      = "db_index_triage"
	MCPToolSysOOMDetect       = "sys_oom_detect"
	MCPToolConfigDiffMask     = "config_diff_mask"
	MCPToolProcMetricTop      = "proc_metric_top"
	MCPToolProcTree           = "proc_tree"
	MCPToolNetSocketAudit     = "net_socket_audit"
	MCPToolNetEndpointPing    = "net_endpoint_ping"
	MCPToolNetDNSResolve      = "net_dns_resolve"
	MCPToolNetSSHKnownHosts   = "net_ssh_known_hosts"
	MCPToolTLSCertInspect     = "tls_cert_inspect"
	MCPToolSysInfo            = "sys_info"
	MCPToolSysEnvVars         = "sys_env_vars"
	MCPToolSysServiceStatus   = "sys_service_status"
	MCPToolSysContainerStatus = "sys_container_status"
	MCPToolSysTimeClock       = "sys_time_clock"
	MCPToolFsFileChecksum     = "fs_file_checksum"
	MCPToolFsDiskUsage        = "fs_disk_usage"
	MCPToolGitOps             = "git_ops"
	MCPToolCloudMetadata      = "cloud_metadata"
	MCPToolK8sInspect         = "k8s_inspect"
	MCPToolProcSignalSafe     = "proc_signal_safe"
	MCPToolOperatorDeploy     = "operator_deploy"
	MCPToolNetHTTPProbe       = "net_http_probe"
)

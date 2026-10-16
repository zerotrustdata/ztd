# Model Context Protocol (MCP) Integration

Last Updated: 2026-10-12
Version: v1.1.2

The ztd Gateway implements native support for the **Model Context Protocol (MCP)**. Autonomous AI agents and coding assistants connect to the Gateway via standardized JSON-RPC 2.0 interfaces. The Gateway acts as a Policy Decision Point (PDP), intercepting every tool invocation, encapsulating it into a strongly typed `GovernanceEnvelope`, subjecting it to the 5-layer governance sequence (L1–L5), and executing it either via host-native tools or by proxying to an authorized downstream MCP server.

**Core Principle**: Unmonitored, direct tool execution is prohibited. All model actions must be governed, cryptographically signed, and recorded in the immutable audit vault.

---

## 1. Architecture and Ingress

### 1.1 Ingress Topology
The Gateway exposes a unified MCP interface at `/mcp`:
- **HTTPS Port (`8443`)**: Production surface requiring mutual TLS (`RequireAndVerifyClientCert`). External agent runtimes authenticate with client certificates featuring SPIFFE URI SANs.
- **HTTP Port (`8080`)**: Plain HTTP listener protected by loopback origin validation, designated for local CLI agents and developer tools operating on the same host.

```
┌────────────────────────────────────────────────────────┐
│ MCP Client (Claude Code, OpenAI Codex, Goose, Gemini)  │
└───────────────────────────┬────────────────────────────┘
                            │ JSON-RPC 2.0 (HTTP / SSE)
                            ▼
┌────────────────────────────────────────────────────────┐
│ ztd Gateway (/mcp Endpoint)                            │
│ 1. Validate JSON-RPC syntax & enforce size limits      │
│ 2. Construct canonical GovernanceEnvelope (v2 hash)    │
│ 3. Deliberate L2 Consensus (if posture requires)       │
│ 4. Verify / Suspend for L3 Notary Human Approval       │
└───────────────────────────┬────────────────────────────┘
                            │ Envelope Verification (L4)
                            ▼
┌────────────────────────────────────────────────────────┐
│ ztd Operator Execution Boundary (L5 Actuator)          │
│ ├── Native Tool Handlers (32 In-Process Tools)         │
│ └── Governed Downstream Proxy (HTTP / Stdio Subprocess)│
└───────────────────────────┬────────────────────────────┘
                            │ Output Scrubbing & Signing
                            ▼
┌────────────────────────────────────────────────────────┐
│ SQLAuditStore & Commitment Ledger                      │
│ • Signed ActionReceipt emitted to client               │
│ • Attestation appended to local SQLite ledger          │
└────────────────────────────────────────────────────────┘
```

### 1.2 Protocol Capabilities
The `/mcp` endpoint implements the MCP Streamable HTTP specification:
- **Protocol Negotiation**: Negotiates protocol versions during `initialize` (default: `2025-06-18`).
- **Method Dispatch**: Supports `tools/list`, `tools/call`, `resources/list`, `resources/read`, `resources/templates/list`, `prompts/list`, `prompts/get`, and `a2a/call`.
- **Batch Rejection**: In accordance with zero-trust envelope binding, JSON-RPC batch requests (array payloads) are rejected fail-closed with code `-32600`.
- **Payload Size Limits**: Strict byte limits (`maxPayloadBytes`) are enforced on inbound requests before parsing.
- **Request-Response Correlation**: Preserves and echoes client `id` values across all responses and errors.
- **Server-Sent Events (SSE)**: Supports streaming notifications and long-lived connection keepalives with 30-second heartbeats.

---

## 2. Governance Envelope Translation

When an MCP client requests a tool execution or resource access, the Gateway translates the JSON-RPC request into a canonical `GovernanceEnvelope`.

### 2.1 Action and Payload Mapping

| MCP Method | Governance Action Type | Protobuf Payload Message | Canonical Request Event |
|:---|:---|:---|:---|
| `tools/call` | `MCP_CALL` | `operatorv1.McpCallRequested` | `ztd.v1.operator.mcp.call.requested` |
| `resources/read` | `MCP_RESOURCE_READ` | `operatorv1.McpResourceReadRequested` | `ztd.v1.operator.mcp.resource.read.requested` |
| `resources/list` | `MCP_RESOURCE_LIST` | `operatorv1.McpResourceListRequested` | `ztd.v1.operator.mcp.resource.list.requested` |
| `prompts/get` | `MCP_PROMPT_GET` | `operatorv1.McpPromptGetRequested` | `ztd.v1.operator.mcp.prompt.get.requested` |
| `prompts/list` | `MCP_PROMPT_LIST` | `operatorv1.McpPromptListRequested` | `ztd.v1.operator.mcp.prompt.list.requested` |
| `a2a/call` | `A2A_CALL` | `operatorv1.A2aCallRequested` | `ztd.v1.operator.a2a.call.requested` |

### 2.2 Protobuf Payload Schemas

#### McpCallRequested (`protocol/proto/ztd/operator/v1/operator.proto`)
```protobuf
message McpCallRequested {
  string tool_name = 1 [(ztd.common.v1.forbidden_patterns) = "(?i)^(sudo|su)$"];
  string arguments_json = 2;
  string execution_id = 3;
}
```

#### McpResourceReadRequested
```protobuf
message McpResourceReadRequested {
  string uri = 1;
  string execution_id = 2;
}
```

#### McpPromptGetRequested
```protobuf
message McpPromptGetRequested {
  string name = 1;
  string execution_id = 2;
}
```

### 2.3 Envelope Construction & Hashing
1. **Metadata Assignment**: The Gateway stamps `protocol_version = "2"`, mints a fresh UUIDv4 `nonce`, sets a 5-minute validity window (`expires_at`), and binds the active host `state_merkle_root`.
2. **Identity Binding**: The authenticated caller identity (`acting_app_id`), authorizing user (`requestor_user_id`), and execution context (`operator_session_id`) are embedded directly into the envelope.
3. **Deterministic Hashing**: The canonical transaction hash is computed using the version 2 prefix:
   $$\text{canonical\_v2} = \texttt{"ztd-tx-v2|"} \,\|\, \text{action\_type} \,\|\, \texttt{"|"} \,\|\, \text{event\_type} \,\|\, \texttt{"|"} \,\|\, \text{canonical\_v1}$$
   The resulting SHA-256 digest is assigned to both `id` and `transaction_hash`.
4. **Serialization**: The envelope is encoded as canonical JSON (`protojson`) for verification and logging.

---

## 3. Governance Interlock Lifecycle for MCP

Every MCP tool call undergoes strict 5-layer verification prior to invocation:

```
[ tools/call Request ]
         │
         ▼
┌────────────────────────────────────────────────────────┐
│ L1 Doctrine Gate                                       │
│ • Tool name checked against forbidden patterns regex   │
│ • Arguments JSON scanned for MITRE ATT&CK indicators   │
│ • SQL injection & path traversal rules applied         │
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L2 Consensus Deliberation                              │
│ • Under consensus/notary: calls Consensus deliberator  │
│ • Gathers K-of-N Ed25519 signatures over tx_hash       │
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L3 Notary Authorization Gate                           │
│ • If notary proof missing under ratify/notary posture: │
│   → Transaction is SUSPENDED (TTL = 2 minutes)         │
│   → Approval URL returned to calling agent             │
│   → Human authorizes via WebAuthn passkey              │
│   → Real-time approval.completed delivered via SSE     │
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L4 Warden Pre-Dispatch                                 │
│ • Reserves nonce in durable replay store               │
│ • Validates state Merkle root against host ledger      │
│ • Recomputes transaction hash (id == SHA256(canonical))│
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L5 Actuator Boundary Execution                         │
│ • Mints scoped JIT execution capability                │
│ • Invokes native handler OR forwards to downstream     │
│ • Scrubs secrets & tokens from output at host boundary │
│ • Signs canonical ActionReceipt & appends to ledger    │
└────────────────────────────────────────────────────────┘
```

### 3.1 L3 Suspension Lifecycle
When an MCP tool call requires human authorization under `ratify` or `notary` postures and no pre-existing L3 proof is attached, the Gateway suspends the call:
1. The transaction is saved in the `SuspendedTransactionStore` with an immutable 2-minute deadline (`ApprovalRequestTTL`).
2. The Gateway records the caller's session ID (`submitter_cli_session_id`) to ensure isolation.
3. The Gateway responds to the MCP client with a suspension directive containing an approval URL:
   ```json
   {
     "jsonrpc": "2.0",
     "id": 1,
     "result": {
       "status": "suspended",
       "tx_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
       "approval_url": "https://ztd.local:8443/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
       "message": "Execution paused: this is a state-changing action and the gateway is running in notary posture, which REQUIRES live human passkey (WebAuthn) authorization before it can run. A human must approve this exact change at https://ztd.local:8443/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 within 2 minutes. Wait for the human to approve, then retry this identical tool call to proceed."
     }
   }
   ```
4. Once the human completes the passkey assertion, the Gateway emits an `approval.completed` SSE event scoped strictly to the submitting session. The agent retries the call to execute.

---

## 4. The 32 Native Tools Inventory

The Operator ships with **32 native tools** registered explicitly in `RegisterNativeTools()` (`internal/services/mcp/native_tool_registry.go`). These tools execute directly within the sovereign host boundary without downstream dependencies:

### 4.1 Database Tools
1. **`db_discover_topology`**: Scans database schemas, tables, and column data types, returning a compressed JSON topology map.
2. **`db_query_validate`**: Analyzes SQL queries using `EXPLAIN QUERY PLAN` to detect full table scans, performance hazards, and structural defects before execution.
3. **`db_isolated_read`**: Executes read-only `SELECT` statements against a SQLite database with strict injection validation.
4. **`db_index_triage`**: Inspects database fragmentation, index utilization, and schema efficiency statistics.

### 4.2 System Diagnostics & Telemetry
5. **`sys_info`**: Retrieves comprehensive host information including hostname, operating system, kernel version, uptime, and load averages.
6. **`sys_env_vars`**: Inspects allowlisted host environment variables for configuration debugging.
7. **`sys_time_clock`**: Reports NTP synchronization status, local system time, and clock drift metrics.
8. **`sys_oom_detect`**: Analyzes system logs to detect and parse Linux Out-Of-Memory (OOM) killer events.
9. **`sys_service_status`**: Queries systemd service units for operational state and active status.
10. **`sys_container_status`**: Queries local container runtimes (e.g., Podman) for container health and lifecycle state.
11. **`proc_metric_top`**: Parses `/proc` to report top resource-consuming processes sorted by CPU and memory.
12. **`proc_tree`**: Generates a structured process tree detailing parent-child PID hierarchies.
13. **`proc_signal_safe`**: Dispatches POSIX signals to processes, enforcing an absolute denylist against protected system PIDs.

### 4.3 Filesystem & Storage Tools
14. **`fs_disk_profile`**: Recursively computes directory sizes, file counts, and disk utilization breakdowns.
15. **`fs_disk_usage`**: Reports filesystem mount point utilization, total capacity, and available free space (`df` equivalent).
16. **`fs_file_checksum`**: Computes SHA-256 digests of files for integrity and tamper verification.
17. **`read_file`**: Reads file contents with optional line-based pagination (`offset`, `limit`). Enforces strict path denylists on sensitive system files (`/etc/shadow`, SSH private keys).
18. **`config_diff_mask`**: Compares configuration files side-by-side with automated secret redaction.
19. **`log_stream_filter`**: Reads and filters log files using regular expressions, applying sovereign data scrubbing to matched outputs.

### 4.4 Network & Transport Tools
20. **`net_socket_audit`**: Inspects open network sockets (TCP/UDP) directly from `/proc/net`.
21. **`net_endpoint_ping`**: Conducts TCP handshakes to target endpoints to confirm network reachability and measure round-trip latency.
22. **`net_http_probe`**: Executes lightweight HTTP requests to test web endpoints, enforcing SSRF protections and private subnet restrictions.
23. **`net_dns_resolve`**: Performs DNS resolution for hostnames, providing diagnostic records.
24. **`net_ssh_known_hosts`**: Parses and enumerates configured SSH known hosts across system and user configurations.
25. **`tls_cert_inspect`**: Inspects TLS certificates, verifies certificate trust chains, and reports expiration deadlines.

### 4.5 Execution & Infrastructure Tools
26. **`run_shell_command`**: Executes governed shell commands with strict safety denylists and timeouts. Supports multi-host SSH dispatch via the `hostnames` parameter.
27. **`git_ops`**: Performs governed Git version control operations (status, log, branch verification, remotes).
28. **`k8s_inspect`**: Inspects Kubernetes cluster status, pod states, service endpoints, and deployment rollouts.
29. **`cloud_metadata`**: Queries cloud provider instance metadata services (AWS, Azure, GCP) to retrieve instance type, region, and zone details.
30. **`operator_deploy`**: Deploys the ztd operator binary to remote hosts over SSH using verified deploy scripts.

### 4.6 Audit & Forensics Tools
31. **`audit_receipt_list`**: Lists signed `ActionReceipt` records from the audit vault, scoped to the caller's session, with pagination and action type filters.
32. **`audit_receipt_get`**: Retrieves a single, complete signed `ActionReceipt` by its `transaction_id`.

---

## 5. Security Controls & Sovereign Boundary

### 5.1 Input Validation Framework (`internal/services/mcp/validation.go`)
Before any MCP payload is converted into a `GovernanceEnvelope`, it must pass fail-closed structural validation:
- **SQL Sanitization**: `db_isolated_read` and `db_query_validate` inspect AST statements to ensure only read-only `SELECT` queries execute. Queries containing semicolons, comments, or mutation keywords (`UPDATE`, `INSERT`, `DELETE`, `DROP`) are rejected.
- **SSRF Prevention**: `net_http_probe` validates target URLs against reserved IP blocks, link-local addresses, and cloud metadata IPs (`169.254.169.254`) unless explicitly authorized.
- **Path Traversal Guards**: All filesystem tools pass paths through `security.ValidatePath` to prevent directory traversal and symlink escapes.
- **Dangerous Command Denylist**: `run_shell_command` rejects high-risk system commands (`rm -rf`, `dd`, `mkfs`, `fdisk`, `reboot`, `shutdown`, `iptables`, kernel module operations).

### 5.2 Sovereign Output Scrubbing
The host-side Operator executes output scrubbing (`ScrubbingService`) immediately at the L5 Actuator boundary. Before results leave the host:
- API keys, bearer tokens, private keys, passwords, and cloud credentials in stdout/stderr are redacted.
- Redacted strings are replaced with cryptographic sentinel tokens (`uei_token_*`).

---

## 6. Downstream MCP Proxying & Circuit Breaker

The Gateway can forward tool discovery and execution to external third-party MCP servers while maintaining complete L1–L5 governance.

### 6.1 Downstream Configuration
- **Subprocess Downstream (stdio)**: Configured via `--mcp-downstream-cmd` and `--mcp-downstream-args`.
- **HTTP Downstream**: Configured via `--mcp-downstream-url`.

### 6.2 Discovery Proxying
When a downstream server is configured:
- `tools/list` returns the schemas defined by the downstream server.
- `prompts/list` and `resources/list` query the downstream server directly.
- Invocations of downstream tools are packaged into standard `GovernanceEnvelope`s and pass through all governance gates before downstream transmission.

### 6.3 Shared Circuit Breaker
Downstream connections are protected by a stateful circuit breaker:
- **Trip Threshold**: 5 consecutive network or 5xx HTTP errors open the circuit.
- **Cooldown Window**: 1 minute in the open state before transitioning to half-open.
- **Fail-Closed Behavior**: Requests received while the circuit is open are rejected immediately with code `-32603` (`ErrGatewayDownstreamUnavailable`).

---

## 7. Client Integration Guide

### 7.1 Transport Modes

| Transport Mode | Execution Command | Governance Level | Use Case |
|:---|:---|:---:|:---|
| **Stdio Bridge** | `ztd mcp stdio` | Full L1–L5 | Connects local agents to running Gateway |
| **Agent Launcher** | `ztd mcp agent run <agent>` | Full L1–L5 | Launches agent with governed environment |
| **HTTP Custom Connector**| `http://localhost:8080/mcp` | Full L1–L5 | IDE plugins and native HTTP clients |

### 7.2 Connecting AI Agents

#### Claude Code (HTTP Transport)
```bash
claude mcp add --transport http ztd http://localhost:8080/mcp
```

#### Claude Code (Stdio Transport)
```bash
claude mcp add ztd-stdio ztd mcp stdio
```

#### OpenAI Codex CLI
```bash
codex mcp add --transport http ztd http://localhost:8080/mcp
```

#### Goose AI
```yaml
# Add to ~/.config/goose/config.yaml
extensions:
  ztd:
    type: stdio
    cmd: ztd
    args: ["mcp", "stdio"]
```

#### Gemini CLI
```bash
gemini mcp register --url http://localhost:8080/mcp ztd
```

---

## 8. Protocol Error Mapping

Verification failures map to standardized JSON-RPC 2.0 error codes:

| JSON-RPC Code | Constant Identifier | Cause |
|:---|:---|:---|
| `-32700` | `JSONRPCParseError` | Malformed JSON received on ingress |
| `-32600` | `JSONRPCInvalidRequest` | Batch requests or payload exceeding size limit |
| `-32601` | `JSONRPCMethodNotFound` | Unrecognized MCP method |
| `-32602` | `JSONRPCInvalidParams` | Missing required parameters (e.g. `tool_name`) |
| `-32603` | `JSONRPCInternalError` | Internal error or circuit breaker open |
| `-32000` | `ErrCodeInvalidEnvelope`| Malformed envelope or unregistered action |
| `-32001` | `ErrCodeHashMismatch` | Transaction hash recomputation failed |
| `-32002` | `ErrCodeExpired` | Transaction expired |
| `-32003` | `ErrCodeReplay` | Transaction nonce already processed |
| `-32004` | `ErrCodeStateMismatch` | State Merkle root stale |
| `-32005` | `ErrCodeL1ValidationFailed`| L1 Doctrine regex violation or MITRE threat detected |
| `-32006` | `ErrCodeL2SignatureInvalid`| L2 Consensus signature missing or quorum failed |
| `-32007` | `ErrCodeL3ProofInvalid` | L3 Notary human verification failed |
| `-32008` | `ErrCodePayloadDecodeFailed`| Failed to decode protobuf payload |
| `-32101` | `ErrCodeGatewayNotReady`| Gateway initializing or offline |

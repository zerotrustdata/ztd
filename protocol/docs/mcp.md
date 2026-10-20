# Model Context Protocol (MCP) Integration

Last Updated: 2026-10-20
Version: v1.2.0

The ztd Gateway implements native support for the **Model Context Protocol (MCP)**. Autonomous AI agents and coding assistants connect to the Gateway via standardized JSON-RPC 2.0 interfaces. The Gateway acts as a Policy Decision Point (PDP), intercepting every tool invocation, encapsulating it into a strongly typed `GovernanceEnvelope`, subjecting it to the 5-layer governance sequence (L1–L5), and executing it either via host-native tools or by proxying to an authorized downstream MCP server.

**Core Principle**: Unmonitored, direct tool execution is prohibited. All model actions must be governed, cryptographically signed, and recorded in the immutable audit vault.

---

## 1. Architecture and Ingress

### 1.1 Ingress Topology
The Gateway exposes a unified MCP interface at `/mcp`:
- **HTTPS Port (`8443`)**: The only listener that serves `/mcp`. It requires mutual TLS (`RequireAndVerifyClientCert`); clients authenticate with certificates issued by the Gateway PKI. When JWKS is configured, JWT authentication wraps the endpoint.
- **HTTP Port (`8080`)**: Does not register `/mcp`. Its catch-all handler redirects other paths to the HTTPS listener with `301`.

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
│ ├── Native Tool Handlers (In-Process Tools)            │
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
- **Method Dispatch**: Supports `initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/read`, `resources/templates/list`, `prompts/list`, `prompts/get`, and `a2a/call`.
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
  string tool_name = 1 [(ztd.common.v1.forbidden_patterns) = "(?i)^(sudo|su)$,(?i)^rm[ _-]?rf[ _-]?root$,(?i)^drop[ _-]?database$"];
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
3. The Gateway responds to the MCP client with a `CallToolResult` whose `suspension` field holds the typed `GovernedSuspension` (`internal/services/mcp/models.go`). The approval URL is the public base URL joined with `/api/v1/approve/` and the transaction hash:
   ```json
   {
     "jsonrpc": "2.0",
     "id": 1,
     "result": {
       "content": [
         {
           "type": "text",
           "text": "Execution paused: this is a state-changing action. <posture-specific approval reason> A human must approve this exact change at https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 within 2 minutes. Wait for the human to approve, then retry this identical tool call to proceed."
         }
       ],
       "suspension": {
         "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
         "status": "suspended",
         "tx_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
         "approval_url": "https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
         "message": "Execution paused: this is a state-changing action. <posture-specific approval reason> A human must approve this exact change at https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 within 2 minutes. Wait for the human to approve, then retry this identical tool call to proceed."
       }
     }
   }
   ```
4. Once the human completes the passkey assertion, the Gateway emits an `approval.completed` SSE event scoped strictly to the submitting session. The agent retries the call to execute.

---

## 4. Native Tools

Native tools are registered explicitly in `RegisterNativeTools()` (`internal/services/mcp/native_tool_registry.go`). They execute inside the Operator's host boundary without downstream dependencies. The `tools/list` method returns the registered set; this document does not enumerate it.

### 4.1 Audit Receipt Queries
- **`audit_receipt_list`**: Lists signed `ActionReceipt` records. The Gateway-native query reads the Gateway's local audit store and requires `operator_session_id`; optional `investigation_id` intersects that scope. Remote Operator records there remain best-effort projections. A governed `MCP_CALL` dispatched to the selected Data Operator with `investigation_id` and `exclude_audit_reads: true` uses that Operator's authoritative local ledger. Its session filter is optional, so one investigation can retrieve receipts across reconnects of the same Operator. SQL applies the Operator identity, investigation, and session filters; the audit-read exclusion drops only the `audit_receipt_list` and `audit_receipt_get` receipts themselves, after decryption and before pagination. Excluding those reads prevents recursive history responses; every other native-tool `MCP_CALL`, including `run_shell_command`, is investigation context. The Operator result contains only signed receipt messages and the `operator_id`, `investigation_id`, `authority: "operator_local_ledger"`, and `count` metadata, preserved in the query's signed receipt summary. Each original receipt's content was scrubbed before its original L5 signature; arbitrary handler text still passes through normal egress scrubbing. The query never falls back to the Gateway mirror. Both routes remain read-only and governed; posture-required L2/L3 proofs still apply. Optional action-type and execution-time filters further narrow results.
- **`audit_receipt_get`**: Retrieves a single, complete signed `ActionReceipt` by its `transaction_id`.

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
| **Stdio Bridge** | `ztd mcp stdio` | Full L1–L5 | Connects local agents to the running Gateway |
| **Agent Launcher** | `ztd mcp agent run <agent>` | Full L1–L5 | Launches a supported agent with a governed configuration |
| **HTTPS Endpoint** | `https://<gateway-host>:8443/mcp` | Full L1–L5 | Direct clients; requires mTLS client certificates or JWT when JWKS is configured |

### 7.2 Client Configuration

Print the client configuration for a supported agent with `ztd mcp agent show <agent>`, and list supported agents with `ztd mcp agent list`. Client configuration MUST point at the HTTPS endpoint or the stdio bridge; the plain HTTP listener does not serve `/mcp`.

---

## 8. Protocol Error Mapping

Verification failures map to JSON-RPC 2.0 error codes. Verification codes are defined in `internal/constants/rpc_errors.go`. The standard codes `-32600`, `-32601`, and `-32603` are emitted as literals in `internal/services/mcp/`; `-32700` is `JSONRPCErrorCodeParseError` and `-32603` is `JSONRPCErrorCodeInternal` in `internal/constants/auth.go`.

| JSON-RPC Code | Constant | Cause |
|:---|:---|:---|
| `-32700` | `JSONRPCErrorCodeParseError` | Malformed JSON received on ingress |
| `-32600` | literal | Batch requests, invalid `jsonrpc` version, or payload exceeding size limit |
| `-32601` | literal | Unrecognized MCP method |
| `-32603` | `JSONRPCErrorCodeInternal` | Missing `tool_name`, Gateway not ready, internal error, or circuit breaker open |
| `-32000` | `ErrCodeInvalidEnvelope` | Malformed envelope or unregistered action |
| `-32001` | `ErrCodeHashMismatch` | Transaction hash recomputation failed |
| `-32002` | `ErrCodeExpired` | Transaction expired |
| `-32003` | `ErrCodeReplay` | Transaction nonce already processed |
| `-32004` | `ErrCodeStateMismatch` | State Merkle root stale |
| `-32005` | `ErrCodeL1ValidationFailed` | L1 Doctrine regex violation or MITRE threat detected |
| `-32006` | `ErrCodeL2SignatureInvalid` | L2 Consensus signature missing or quorum failed |
| `-32007` | `ErrCodeL3ProofInvalid` | L3 Notary human verification failed |
| `-32008` | `ErrCodePayloadDecodeFailed` | Failed to decode protobuf payload |

`ErrCodeGatewayNotReady` (`-32101`) and `ErrCodeResourceNotFound` (`-32100`) are reserved. The MCP path does not emit them; a Gateway that is not ready returns `-32603`.

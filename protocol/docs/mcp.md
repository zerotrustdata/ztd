# Model Context Protocol (MCP) Integration

Last Updated: 2026-10-23
Version: v1.7.0

The ztd Gateway implements native support for the **Model Context Protocol (MCP)**. Autonomous AI agents and coding assistants connect to the Gateway via standardized JSON-RPC 2.0 interfaces. The Gateway acts as a Policy Decision Point (PDP), intercepting every tool invocation, encapsulating it into a strongly typed `GovernanceEnvelope`, subjecting it to the 5-layer governance sequence (L1–L5), and executing it either via host-native tools or by proxying to an authorized downstream MCP server.

**Core Principle**: Unmonitored, direct tool execution is prohibited. All model actions must be governed, cryptographically signed, and recorded in the executing runtime's Local-First Events Store.

---

## 1. Architecture and Ingress

### 1.1 Ingress Topology
The Gateway exposes a unified MCP interface at `/mcp`:
- **HTTPS Port (`8443`)**: The only listener that serves `/mcp`. It requires mutual TLS (`RequireAndVerifyClientCert`); clients authenticate with certificates issued by the Gateway PKI. When JWKS is configured, JWT authentication wraps the endpoint.
- **HTTP Port (`8080`)**: Does not register `/mcp`. Its catch-all handler redirects other paths to the HTTPS listener with `301`.

```text
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
│ LocalFirstEventsStore & Commitment Ledger                      │
│ • Signed EventReceipt emitted to client               │
│ • Attestation appended to local SQLite ledger          │
└────────────────────────────────────────────────────────┘
```

### 1.2 Protocol Capabilities
The `/mcp` endpoint implements the MCP Streamable HTTP specification:
- **Protocol Version**: `initialize` echoes the protocol version the client requests. The default constant is `2025-06-18` (`mcpDefaultProtocolVersion` in `internal/services/mcp/mcp_endpoint.go`).
- **Method Dispatch**: Supports `initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/read`, `resources/templates/list`, `prompts/list`, `prompts/get`, and `a2a/call`. `a2a/call` is opt-in: it exists only when an A2A downstream is configured (`--a2a-downstream-url` on `ztd gw start`); otherwise the Gateway returns `-32601`.
- **Batch Rejection**: In accordance with zero-trust envelope binding, JSON-RPC batch requests (array payloads) are rejected fail-closed with code `-32600`.
- **Payload Size Limits**: Byte limits (`maxPayloadBytes`) are enforced on inbound requests via `http.MaxBytesReader` before parsing.
- **Request-Response Correlation**: Echoes the client `id` in responses to requests that parse. A request that fails to parse has no `id` to echo, so its error response carries `null`.
- **Server-Sent Events (SSE)**: `GET /mcp` opens an SSE stream that sends one `connected` event and a `: keepalive` comment every 30 seconds. The stream does not push tool results or approval events; approval notifications reach the `ztd mcp stdio` bridge through the SSE routing described in Section 3.1.

---

## 2. Governance Envelope Translation

When an MCP client requests a tool execution or resource access, the Gateway translates the JSON-RPC request into a canonical `GovernanceEnvelope`.

### 2.1 Action and Payload Mapping

| MCP Method | Governance Action Type | Protobuf Payload Message | Canonical Request Event |
|:---|:---|:---|:---|
| `tools/call` | `MCP_CALL` | `ztd.operator.v1.McpCallRequested` | `ztd.v1.operator.mcp.call.requested` |
| `resources/read` | `MCP_RESOURCE_READ` | `ztd.operator.v1.McpResourceReadRequested` | `ztd.v1.operator.mcp.resource.read.requested` |
| `resources/list` | `MCP_RESOURCE_LIST` | `ztd.operator.v1.McpResourceListRequested` | `ztd.v1.operator.mcp.resource.list.requested` |
| `prompts/get` | `MCP_PROMPT_GET` | `ztd.operator.v1.McpPromptGetRequested` | `ztd.v1.operator.mcp.prompt.get.requested` |
| `prompts/list` | `MCP_PROMPT_LIST` | `ztd.operator.v1.McpPromptListRequested` | `ztd.v1.operator.mcp.prompt.list.requested` |
| `a2a/call` | `A2A_CALL` | `ztd.operator.v1.A2aCallRequested` | `ztd.v1.operator.a2a.call.requested` |

The payload messages, their fields, and their L1 forbidden-pattern annotations are defined in [`protocol/proto/ztd/operator/v1/operator.proto`](../proto/ztd/operator/v1/operator.proto). Event names are listed in `protocol/constants/events.json`; action types are generated into `internal/constants/action_types_gen.go`.

### 2.2 Protobuf Payload Notes
- `McpCallRequested.tool_name` carries an L1 forbidden-pattern annotation. The pattern list is in the proto; it is not repeated here.
- `McpResourceReadRequested.uri` carries an L1 forbidden-pattern annotation for a private storage path prefix. The pattern is in the proto.
- The other MCP payload messages (`McpResourceListRequested`, `McpPromptListRequested`, `McpPromptGetRequested`) declare no forbidden-pattern annotations.
- `A2aCallRequested.skill_name` carries an L1 forbidden-pattern annotation. It applies only to the opt-in `a2a/call` path.

### 2.3 Envelope Construction & Hashing
1. **Metadata Assignment**: The Gateway stamps `protocol_version = "2"`, mints a fresh UUIDv4 `nonce`, sets a 5-minute validity window (`expires_at`), and binds the active host `state_merkle_root`.
2. **Identity Binding**: The authenticated caller identity (`acting_app_id`), authorizing user (`requestor_user_id`), and execution context (`operator_session_id`) are embedded directly into the envelope.
3. **Deterministic Hashing**: The canonical transaction hash is computed using the version 2 prefix:
   $$\text{canonical\_v2} = \texttt{"ztd-tx-v2|"} \,\|\, \text{action\_type} \,\|\, \texttt{"|"} \,\|\, \text{event\_type} \,\|\, \texttt{"|"} \,\|\, \text{canonical\_v1}$$
   The resulting SHA-256 digest is assigned to both `id` and `transaction_hash`.
4. **Serialization**: The Gateway encodes the envelope with `protojson` (single-line, `Multiline: false`) as its canonical envelope wire form (`internal/services/mcp/gateway.go`).

---

## 3. Governance Interlock Lifecycle for MCP

Every MCP tool call undergoes strict 5-layer verification prior to invocation:

```text
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
│ • Under consensus/notary, state-changing calls only    │
│   (governance.L2Enforced): calls L2ConsensusDeliberator│
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
│   → approval.completed sent to the submitting session  │
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L4 Warden Pre-Dispatch                                 │
│ • Reserves the nonce (replay store, in-flight tracking)│
│ • Verifies the envelope (L4Warden.VerifyEnvelope)      │
│ • Recomputes transaction hash (id == SHA256(canonical))│
└────────────────────────┬───────────────────────────────┘
                         │ PASS
                         ▼
┌────────────────────────────────────────────────────────┐
│ L5 Actuator Boundary Execution                         │
│ • Mints scoped JIT execution capability                │
│ • Invokes native handler OR forwards to downstream     │
│ • Scrubs secrets & tokens from tool results            │
│ • Signs EventReceipt & appends to ledger              │
└────────────────────────────────────────────────────────┘
```

### 3.1 L3 Suspension Lifecycle
When an MCP tool call requires human authorization under `ratify` or `notary` postures and no pre-existing L3 proof is attached, the Gateway suspends the call:
1. The transaction is saved in the `SuspendedTransactionStore` with a 2-minute deadline (`ApprovalRequestTTL`, `internal/services/mcp/gateway.go`).
2. The Gateway records the submitting CLI session ID (`SubmitterCLISessionID`, stored as `submitter_cli_session_id`) so that approval notifications are scoped to that session.
3. The Gateway responds to the MCP client with a `CallToolResult` whose `suspension` field holds the typed `GovernedSuspension` (`internal/services/mcp/models.go`). The approval URL is the public base URL joined with `/api/v1/approve/` and the transaction hash. In the response, `id` and `tx_hash` both carry the transaction hash, and `status` is `suspended`:
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
   The example abbreviates `text` and `message`. The full message also says the approval window is intentionally short, and that a lapsed request is voided, so the caller must call the tool again to open a fresh approval request.
4. Once the human completes the passkey assertion, the Gateway emits an `approval.completed` SSE event to the submitting CLI session (`EmitApprovalCompletedSSE` in `internal/services/gateway/passkey_orchestrator.go`). The `ztd mcp stdio` bridge subscribes to this event. The `GET /mcp` stream does not carry it. The agent retries the call to execute.

---

## 4. Native Tools

Native tools are registered explicitly in `RegisterNativeTools()` (`internal/services/mcp/native_tool_registry.go`). They run in-process within the Operator's host boundary. To list the live set, call the MCP `tools/list` method on `/mcp` from any MCP client; no `ztd mcp` subcommand lists tools. Audit-trail querying is described in the [AI Agents and Governance Boundary](../../docs/architecture/agents.md) architecture document.

---

## 5. Security Controls & Sovereign Boundary

### 5.1 Input Validation Framework (`internal/services/mcp/validation.go`)
Before any MCP payload is converted into a `GovernanceEnvelope`, it must pass fail-closed structural validation:
- **SQL Sanitization**: `db_isolated_read` and `db_query_validate` accept only queries that begin with `SELECT`. `validateSQLQuery` rejects an empty query and a query that ends with a semicolon.
- **SSRF Prevention**: `validateHTTPRequestURL` (`internal/services/mcp/validation.go`, used by the HTTP probe tool) accepts only `http` and `https` URLs with a host. When the host is an IP literal or `localhost`, it rejects loopback, private, and link-local addresses unless they are on the private allowlist.
- **Path Traversal Guards**: Path-taking tools apply their own checks. Some call `security.ValidatePath` (`internal/security/path.go`); file reads also check `security.IsSensitiveReadPath`. Individual tools determine which check applies.
- **Dangerous Command Denylist**: `run_shell_command` takes one executable name with an argv array and runs it without a shell. It rejects base commands and command-line patterns listed in `internal/constants/shell.go` (`DangerousCommands`, `DangerousPatterns`), plus shell-injection patterns from the same file. A command line that contains spaces is rejected with a hint to send the argv form.

### 5.2 Sovereign Output Scrubbing
The Gateway scrubs tool results before they leave the dispatch layer. `scrubToolResult` (`internal/services/mcp/gateway.go`) runs on results from native tools, `read_field`, and downstream output, and fails closed when the scrubber is nil or disabled. The scrubbing patterns in `internal/services/scrubbing/boundary.go` include API keys, bearer tokens, private key blocks, and credentials embedded in URLs.
- Redacted strings are replaced with tokens of the form `{{UEI_<owner>_<N>}}`, where `<owner>` is the minting runtime's 12-character hex namespace.
- Rehydration checks token ownership (`ownsToken`). Tokens minted by another runtime are foreign and are handled by a `ForeignTokenPolicy`.

---

## 6. Downstream MCP Proxying & Circuit Breaker

The Gateway can forward tool discovery and execution to external third-party MCP servers while maintaining complete L1–L5 governance.

### 6.1 Downstream Configuration
Configure downstream servers on `ztd gw start`:
- **Subprocess Downstream (stdio)**: `--mcp-downstream-cmd` with `--mcp-downstream-args` (comma-separated).
- **HTTP Downstream**: `--mcp-downstream-url`.

Run `ztd gw start --help` for the flag descriptions and defaults.

### 6.2 Discovery Proxying
When a downstream server is configured:
- `tools/list` merges the downstream server's tools with the native tools.
- `prompts/list` and `resources/list` query the downstream server directly.
- Invocations of downstream tools are packaged into standard `GovernanceEnvelope`s and pass through all governance gates before downstream transmission.

### 6.3 Shared Circuit Breaker
Downstream connections (MCP and A2A share the same breaker) are protected by a stateful circuit breaker:
- **Trip Threshold**: 5 consecutive failures open the circuit. Failures are transport errors and HTTP 5xx responses from the downstream.
- **Cooldown Window**: 1 minute in the open state. After the cooldown, the next request is attempted (half-open); a success closes the circuit, and a failure reopens it.
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
| `-32700` | `JSONRPCErrorCodeParseError` | Request body is not valid JSON |
| `-32600` | literal | Batch requests, invalid `jsonrpc` version, missing `method`, or payload exceeding size limit |
| `-32601` | literal | Unrecognized MCP method, or `a2a/call` without a configured A2A downstream |
| `-32603` | `JSONRPCErrorCodeInternal` | Missing `tool_name`, Gateway not ready, body read failure, internal error, or circuit breaker open |
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

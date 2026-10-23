# Agent-to-Agent (A2A) Protocol Specification

Last Updated: 2026-10-22
Version: v1.3.0

The **Agent-to-Agent (A2A)** protocol defines a cryptographically governed mechanism for autonomous AI agents to discover, invoke, and delegate tasks to peer agent capabilities. The ztd Gateway terminates inbound A2A requests, translates them into the canonical `GovernanceEnvelope` wire format, executes the 5-layer governance sequence (L1 Doctrine, L2 Consensus, L3 Notary, L4 Warden, L5 Actuator), and securely dispatches verified skill payloads to configured downstream agent executors.

---

## 1. Architectural Overview

Autonomous workflows frequently require agents to collaborate across boundaries of authority. In ztd, peer agent delegation is never unmediated:
- **Zero-Trust Boundary**: Inbound agent skill requests are treated as untrusted intent.
- **Unified Governance**: Every skill call undergoes identical validation to host-native commands, requiring state Merkle root verification, policy evaluation, and cryptographic audit receipt generation.
- **Fail-Closed Execution**: Downstream skill providers receive invocations only after all governance layers confirm validity.

```text
┌────────────────────────────────────────────────────────┐
│ Originating Agent (A2A Client)                         │
└───────────────────────────┬────────────────────────────┘
                            │ HTTP / JSON (POST /api/v1/a2a/call)
                            ▼
┌────────────────────────────────────────────────────────┐
│ ztd Gateway (Policy Decision Point)                    │
│ 1. Parse JSON-RPC 2.0 structure & mint tracking IDs    │
│ 2. Construct canonical GovernanceEnvelope (A2A_CALL)   │
│ 3. Compute deterministic v2 transaction hash           │
│ 4. Coordinate L2 Consensus deliberation                │
│ 5. Evaluate / Suspend for L3 Notary human approval     │
└───────────────────────────┬────────────────────────────┘
                            │ L4 Warden Verification
                            ▼
┌────────────────────────────────────────────────────────┐
│ ztd Operator Actuator (Policy Execution Point)         │
│ 1. Reserve nonce & verify host state Merkle root       │
│ 2. Mint ephemeral capability bound to skill & payload  │
│ 3. Forward to downstream (DispatchToA2ADownstream)     │
│ 4. Apply output scrubbing & receipt summary truncation │
│ 5. Produce signed ActionReceipt & commit to ledger     │
└───────────────────────────┬────────────────────────────┘
                            │ HTTP / JSON
                            ▼
┌────────────────────────────────────────────────────────┐
│ Downstream Agent Skill Provider                        │
└────────────────────────────────────────────────────────┘
```

---

## 2. Ingress & Wire Format

A2A is opt-in. The Gateway mounts A2A ingress only when `--a2a-downstream-url` is set. Without it, no `/api/v1/a2a/call` route is registered, and the `a2a/call` method on `POST /mcp` returns method not found. `GET /api/v1/health` reports this state as the `a2a_ingress` field.

A2A skill invocations enter the platform through two interchangeable HTTPS interfaces when ingress is mounted:
1. **Dedicated REST Route**: `POST /api/v1/a2a/call`
2. **Unified MCP Router**: `POST /mcp` with method `"a2a/call"`

### 2.1 Transport & Authentication
- **HTTPS Port (`8443`)**: The only listener that serves A2A routes. It requires mutual TLS with client certificate authentication (`RequireAndVerifyClientCert`). When JWKS is configured, JWT authentication wraps the route.
- **HTTP Port (`8080`)**: Does not register `/api/v1/a2a/call` or `/mcp`. Its catch-all handler redirects other paths to the HTTPS listener with `301`.

### 2.2 Wire Request Schema
Requests use standard JSON-RPC 2.0 framing:

```json
{
  "jsonrpc": "2.0",
  "method": "a2a/call",
  "params": {
    "skill_name": "data_analysis_pipeline",
    "payload": {
      "dataset_uri": "s3://prod-analytics/2026-q3.parquet",
      "model_parameters": {
        "depth": 10,
        "features": ["latency", "error_rate"]
      }
    },
    "execution_id": "exec-d4a1b8c2"
  },
  "id": 42
}
```

#### Field Specifications
- **`skill_name`** (`string`, required): Name of the target skill or agent capability.
- **`payload`** (`object`, optional): Structured JSON parameters for the delegated task (defaults to `{}`).
- **`execution_id`** (`string`, optional): Caller-provided tracking identifier. If omitted, the Gateway mints a unique UUIDv4.

---

## 3. Governance Envelope Translation

Upon receipt, the Gateway translates the A2A call into a canonical `GovernanceEnvelope` with action type `A2A_CALL`.

### 3.1 Protobuf Contract (`operatorv1.A2aCallRequested`)
The typed payload message is defined in `protocol/proto/ztd/operator/v1/operator.proto`:

```protobuf
message A2aCallRequested {
  // Target agent skill or capability name.
  string skill_name = 1 [(ztd.common.v1.forbidden_patterns) = "(?i)^(sudo|su)$"];

  // JSON-encoded A2A task payload.
  string payload_json = 2;

  // Client-supplied or Gateway-minted invocation identifier.
  string execution_id = 3;
}
```

### 3.2 Canonical Envelope Properties

| Envelope Field | Value / Source | Specification |
|:---|:---|:---|
| `event_type` | `ztd.v1.operator.a2a.call.requested` | Canonical request event from `events.json`. |
| `action_type` | `A2A_CALL` | Derived from event registry; classified as mutation (`IsMutation() == true`). |
| `target_resource` | `skill_name` | Direct mapping to target skill. |
| `protocol_version`| `"2"` | Required version identifier. |
| `payload` | Protobuf bytes | Serialized `operatorv1.A2aCallRequested` message. |
| `nonce` | UUIDv4 | Generated replay prevention token. |
| `expires_at` | `now + 5m` | Transaction expiration timestamp. |
| `state_merkle_root`| Local host root | Active host ledger state root. |
| `transaction_hash`| SHA-256 digest | Computed using deterministic v2 hashing algorithm. |
| `id` | `transaction_hash` | Must match `transaction_hash` exactly. |

---

## 4. Five-Layer Governance Interlock for A2A

`A2A_CALL` is a governed mutation requiring complete 5-layer validation:

### 4.1 Layer 1: Doctrine
- **Forbidden Pattern Regex**: The protobuf field option `(ztd.common.v1.forbidden_patterns)` on `skill_name` enforces an immediate block on privilege escalation identifiers (`(?i)^(sudo|su)$`).
- **Threat Indicator Analysis**: The `payload_json` content is scanned via `AnalyzeMCPArguments` against active MITRE ATT&CK patterns, detecting reverse shells, injection attempts, and credential access vectors before deliberation.

### 4.2 Layer 2: Consensus
- Under `consensus` and `notary` postures, the L2 vote round applies only when the action is a state change. `governance.L2Enforced` decides this for each envelope. A2A skill calls are state-changing, so they are voted on. See [Consensus Architecture](../../docs/architecture/consensus.md).
- The bound consensus provider produces the votes. Provider `application` submits the canonical envelope to an enrolled application's HTTPS protobuf endpoint. Provider `tribunal` runs the Gateway-hosted Tribunal panel.
- Members return signed `L2Vote` records containing Ed25519 signatures over `<transaction_hash>|<decision>`.
- The L4 Warden verifies that $K$-of-$N$ distinct trusted keys satisfy the consensus policy quorum.

### 4.3 Layer 3: Notary (Human Presence)
- Under `ratify` and `notary` postures, human authorization is required prior to execution.
- If pre-existing notary credentials are not attached, the transaction is suspended (see §5).

### 4.4 Layer 4: Warden Pre-Dispatch Gate
- Reserves the unique `nonce` in the persistent SQLite replay table.
- Verifies that `now < expires_at`.
- Recomputes the SHA-256 digest over the canonical field sequence and validates `id == transaction_hash`.
- Validates that the envelope's `state_merkle_root` matches the local host's Merkle root.
- Confirms posture compliance of all L2 and L3 stage evidence.

### 4.5 Layer 5: Actuator Execution Boundary
- Assembles deterministic stage proofs across L1–L4.
- Signs an initial `EXECUTING` receipt and appends a `CommitmentAttestation` to the local commitment chain.
- Mints an ephemeral execution capability scoped strictly to `A2A_CALL` and `target_resource`.
- Invokes downstream dispatch via `DispatchToA2ADownstream`.
- Truncates receipt summary to `ReceiptSummaryMaxBytes` (4096 bytes).
- Signs final `ActionReceipt` (`COMPLETED` or `FAILED`) and persists it to `SQLAuditStore`.

---

## 5. L3 Suspension and Approval Lifecycle

Under postures enforcing L3 (`ratify`, `notary`), unauthenticated A2A requests enter the suspension workflow:

```text
[ Inbound A2A Call ] ──▶ [ L3 Proof Missing ]
                                │
                                ▼
         ┌──────────────────────────────────────────────┐
         │ SuspendedTransactionStore                    │
         │ • Stores envelope with 2-minute TTL          │
         │ • Scopes submitter_cli_session_id            │
         └──────────────────────┬───────────────────────┘
                                │ Returns GovernedSuspension
                                ▼
         ┌──────────────────────────────────────────────┐
         │ Calling Agent Receives Approval URL          │
         │ • Pauses and awaits human authorization      │
         └──────────────────────┬───────────────────────┘
                                │ Human asserts WebAuthn passkey
                                ▼
         ┌──────────────────────────────────────────────┐
         │ SSE Event: approval.completed                │
         │ • Scoped strictly to submitter session       │
         └──────────────────────┬───────────────────────┘
                                │ Agent retries call
                                ▼
         ┌──────────────────────────────────────────────┐
         │ L5 Actuator Executes Verified Mutation       │
         └──────────────────────────────────────────────┘
```

### 5.1 Suspension Response Schema (`GovernedSuspension`)
The `result` is the typed `GovernedSuspension` (`internal/services/mcp/models.go`). The approval URL is the public base URL (`--public-base-url`) joined with `/api/v1/approve/` and the transaction hash.
```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "result": {
    "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "status": "suspended",
    "tx_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "approval_url": "https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "message": "Execution paused: this is a state-changing action. <posture-specific approval reason> A human must approve this exact change at https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 within 2 minutes. Wait for the human to approve, then retry this identical tool call to proceed. If it lapses before approval the request is voided for security; call this tool again to open a fresh approval request."
  }
}
```

---

## 6. Downstream Dispatch Protocol

Verified A2A invocations are forwarded to the downstream skill executor via `DispatchToA2ADownstream` (`internal/services/mcp/gateway.go`).

### 6.1 Downstream HTTP Request
The Gateway issues an HTTP `POST` to the configured `--a2a-downstream-url` with a strict 30-second timeout:

```json
{
  "skill_name": "data_analysis_pipeline",
  "payload": {
    "dataset_uri": "s3://prod-analytics/2026-q3.parquet",
    "model_parameters": {
      "depth": 10,
      "features": ["latency", "error_rate"]
    }
  }
}
```

### 6.2 Downstream Response Processing
The Gateway decodes the downstream response to populate the `ActionReceipt`:
- Reads `summary`: Used as primary execution summary if present.
- Reads `result`: Used as fallback summary if `summary` is empty.
- Defaults to `"completed"` if neither field is provided.
- If `error` is populated or HTTP status is non-200, execution is marked as `FAILED`.
- Summary text is bounded to `ReceiptSummaryMaxBytes` (`4096` bytes) to prevent unbounded memory growth on verbose tool outputs.

### 6.3 Shared Circuit Breaker
Downstream A2A egress shares the Gateway's stateful circuit breaker:
- **Failure Threshold**: 5 consecutive connection errors or 5xx responses open the circuit.
- **Cooldown Period**: 1 minute in open state before allowing a canary probe.
- **Fail-Closed Guard**: While open, requests fail immediately with `ErrGatewayDownstreamUnavailable` (`-32603`).

---

## 7. Execution Success Response

When execution completes successfully, the Gateway returns `A2ASuccessResponse` as the JSON-RPC `result`. Its `id` is the transaction hash and its `result` is the executing Operator's signed `ActionReceipt`, encoded as canonical protojson with proto field names:

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "result": {
    "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "result": {
      "transaction_id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "transaction_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "status": "COMPLETED",
      "result_summary": "Pipeline executed successfully: 1,420,891 records processed across 8 partitions",
      "state_root_before": "7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069",
      "state_root_after": "7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069",
      "signer_key_id": "op-prod-01-actuator-key",
      "signature": "8a4f91...b320cd",
      "l2_status": "L2_STATUS_REQUIRED_VALID",
      "l3_status": "L3_STATUS_REQUIRED_VALID",
      "event_type": "ztd.v1.operator.a2a.call.requested",
      "action_type": "A2A_CALL"
    }
  }
}
```

---

## 8. Protocol Error Codes

A2A errors conform to JSON-RPC 2.0 error objects. Verification codes are defined in `internal/constants/rpc_errors.go`. The standard codes `-32600`, `-32601`, and `-32603` are emitted as literals in `internal/services/mcp/`; `-32700` is `JSONRPCErrorCodeParseError` and `-32603` is `JSONRPCErrorCodeInternal` in `internal/constants/auth.go`.

| Code | Constant | Cause |
|:---|:---|:---|
| `-32700` | `JSONRPCErrorCodeParseError` | Malformed JSON-RPC wire payload. |
| `-32600` | literal | Batch requests, invalid `jsonrpc` version, missing `method`, or oversized payloads. |
| `-32601` | literal | Method other than `a2a/call` on the `/mcp` router. |
| `-32603` | `JSONRPCErrorCodeInternal` | Missing `skill_name`, Gateway not ready, downstream server error, unconfigured downstream URL, or open circuit breaker. |
| `-32000` | `ErrCodeInvalidEnvelope` | Malformed envelope structure or missing identifier. |
| `-32001` | `ErrCodeHashMismatch` | Transaction hash recomputation mismatch. |
| `-32002` | `ErrCodeExpired` | Transaction validity deadline exceeded. |
| `-32003` | `ErrCodeReplay` | Nonce already exists in replay store. |
| `-32004` | `ErrCodeStateMismatch` | Host state Merkle root divergence. |
| `-32005` | `ErrCodeL1ValidationFailed` | L1 Doctrine forbidden pattern match or MITRE threat indicator. |
| `-32006` | `ErrCodeL2SignatureInvalid` | Consensus quorum not met or invalid Ed25519 signature. |
| `-32007` | `ErrCodeL3ProofInvalid` | Human notary proof verification failure. |
| `-32008` | `ErrCodePayloadDecodeFailed` | Protobuf unmarshaling failure. |

`ErrCodeGatewayNotReady` (`-32101`) and `ErrCodeResourceNotFound` (`-32100`) are reserved. The A2A path does not emit them; a Gateway that is not ready returns `-32603`.

---

## 9. Configuration & Operational Flags

A2A integration parameters are configured strictly through Gateway CLI flags:

- **`--a2a-downstream-url <url>`**: Target HTTP endpoint of the downstream A2A executor (default: none). Setting it mounts A2A ingress. Leaving it unset mounts no A2A route.
- **`--public-base-url <url>`**: Canonical public base URL utilized to generate human passkey approval links in `GovernedSuspension` (e.g., `https://ztd.local:8443`).
- **`--posture <mode>`**: Gateway governance posture (`doctrine`, `consensus`, `ratify`, `notary`).
- **`--http-port <port>`**: Plain HTTP port for bootstrap and enrollment routes (effective default: `8080`). It does not serve A2A or MCP routes.
- **`--https-port <port>`**: HTTPS port for mTLS production ingress (effective default: `8443`).

# Agent-to-Agent (A2A) Protocol Specification

Last Updated: 2026-10-23
Version: v1.7.0

The **Agent-to-Agent (A2A)** protocol defines a cryptographically governed mechanism for autonomous AI agents to discover, invoke, and delegate tasks to peer agent capabilities. The ztd Gateway terminates inbound A2A requests, translates them into the canonical `GovernanceEnvelope` wire format, executes the 5-layer governance sequence (L1 Doctrine, L2 Consensus, L3 Notary, L4 Warden, L5 Actuator), and securely dispatches verified skill payloads to configured downstream agent executors.

---

## 1. Architectural Overview

Autonomous workflows frequently require agents to collaborate across boundaries of authority. In ztd, peer agent delegation is never unmediated:
- **Zero-Trust Boundary**: Inbound agent skill requests are treated as untrusted intent.
- **Unified Governance**: Every skill call undergoes identical validation to host-native commands, requiring state Merkle root verification, policy evaluation, and cryptographic event receipt generation.
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
│ 2. Mint capability bound to action type & tx hash      │
│ 3. Forward to downstream (DispatchToA2ADownstream)     │
│ 4. Bound receipt summary to 4096 bytes (A2A handler)   │
│ 5. Produce signed EventReceipt & commit to ledger      │
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
- **HTTPS Port (`8443`)**: The only listener that serves A2A routes. The HTTPS listener requires client certificates (`RequireAndVerifyClientCert`). Route policy marks the A2A prefix `RouteAuthNone` when JWKS is enabled, so JWT middleware authenticates those calls instead (`internal/services/gateway/gateway_auth.go`).
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
- **`skill_name`** (`string`, required): Name of the target skill or agent capability. An empty value is rejected (`ErrGatewaySkillNameRequired`).
- **`payload`** (`object`, optional): Structured JSON parameters for the delegated task (defaults to `{}`).
- **`execution_id`** (`string`, optional): Caller-provided tracking identifier. If omitted, the Gateway mints a unique UUID (`callIDs` in `internal/services/mcp/gateway.go`).

---

## 3. Governance Envelope Translation

Upon receipt, the Gateway translates the A2A call into a canonical `GovernanceEnvelope` with action type `A2A_CALL`.

### 3.1 Protobuf Contract (`operatorv1.A2aCallRequested`)
The typed payload message is defined in [`protocol/proto/ztd/operator/v1/operator.proto`](../proto/ztd/operator/v1/operator.proto) (`message A2aCallRequested`). It has three fields:
- `skill_name` (string, field 1): target skill or capability name. The `(ztd.common.v1.forbidden_patterns)` option blocks `sudo` and `su` (case-insensitive).
- `payload_json` (string, field 2): JSON-encoded A2A task payload.
- `execution_id` (string, field 3): client-supplied or Gateway-minted invocation identifier.

### 3.2 Canonical Envelope Properties

| Envelope Field | Value / Source | Specification |
|:---|:---|:---|
| `event_type` | `ztd.v1.operator.a2a.call.requested` | Canonical request event from `events.json`. |
| `action_type` | `A2A_CALL` | Derived from event registry; classified as mutation (`IsMutation() == true`) and state-changing (`ChangesState`). |
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
- **Threat Indicator Analysis**: The `payload_json` content is scanned via `AnalyzeMCPArguments` (`internal/services/governance/l1_doctrine.go`) before deliberation. The active pattern set is defined by the doctrine rules, not by this document.

### 4.2 Layer 2: Consensus
- Under postures that require L2 (`consensus`, `notary`; `RequiresL2` in `internal/constants/posture.go`), the L2 vote round applies only when the action is a state change. `governance.L2Enforced` decides this for each envelope. A2A skill calls are state-changing (`ChangesState`), so they are voted on. See [Consensus Architecture](../../docs/architecture/consensus.md).
- Votes come from the bound consensus provider: `application` or `tribunal` (`internal/constants/consensus.go`).
- Members return signed `L2Vote` records. Each signature is Ed25519 over `<message_id>|<decision>` (`L2VotePayload`, `internal/services/governance/l2_consensus.go`), where `message_id` is the transaction hash.
- `verifyL2Quorum` requires the policy quorum of distinct trusted signers.

### 4.3 Layer 3: Notary (Human Presence)
- Under postures that require L3 (`ratify`, `notary`; `RequiresL3`), human authorization is required prior to execution.
- If no notary proof is attached, the call is suspended (see §5).

### 4.4 Layer 4: Warden Pre-Dispatch Gate
- Reserves the envelope `nonce` through `ReplayStore.ReserveNonce` (`internal/services/governance/replay_store.go`) before the L2/L3 checks. The reservation is finalized after successful execution and released when the transaction fails.
- Verifies that `now < expires_at`.
- Recomputes the SHA-256 digest over the canonical field sequence and validates `id == transaction_hash`.
- Validates that the envelope's `state_merkle_root` matches the local host's Merkle root.
- Confirms posture-required L2 and L3 evidence.

### 4.5 Layer 5: Actuator Execution Boundary
- Assembles deterministic stage evidence across L1–L4.
- Builds the `EXECUTING` receipt, signs and logs it. The receipt and its commitment attestation are persisted in one write (`signAndLogReceipt`, `internal/services/governance/l5_actuator.go`).
- Mints an ephemeral execution capability (`MintCapability`). `Capability.Verify` checks the action type and transaction hash. The capability records `target_resource`, but `Verify` does not check it.
- Invokes the handler. For A2A this is the Operator's A2A handler, which calls `DispatchToA2ADownstream` (§6).
- Finalizes the signed `EventReceipt` as `EXECUTION_STATUS_COMPLETED` or `EXECUTION_STATUS_FAILED` and writes it through `LogEventReceipt` to `LocalFirstEventsStore`.

---

## 5. L3 Suspension and Approval Lifecycle

Under postures that require L3 (`ratify`, `notary`), an A2A call with no notary proof (`ErrL3ProofMissing`) enters the suspension workflow:

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
The `result` is the typed `GovernedSuspension` (`internal/services/mcp/models.go`). The approval URL is the public base URL (`--public-base-url`) joined with `/api/v1/approve/` (`ApprovePagePrefix`) and the transaction hash. The `message` is built by `approvalPausedMessage` (`internal/services/mcp/gateway.go`); the 2-minute window comes from `ApprovalRequestTTL`.

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "result": {
    "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "status": "suspended",
    "tx_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "approval_url": "https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "message": "Execution paused: this is a state-changing action. <posture-specific approval reason> A human must approve this exact change at https://ztd.local:8443/api/v1/approve/e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 within 2 minutes. Wait for the human to approve, then retry this identical tool call to proceed. This window is intentionally short so the approval proves a human was present for this specific action. If it lapses before approval the request is voided for security — call this tool again to open a fresh approval request."
  }
}
```

---

## 6. Downstream Dispatch Protocol

Verified A2A invocations are forwarded to the downstream skill executor via `DispatchToA2ADownstream` (`internal/services/mcp/gateway.go`). The Operator's A2A handler (`handleA2aCallRequestSync`, `internal/services/pubsub/pubsub_commands.go`) calls it after the L4 and L5 checks have passed.

### 6.1 Downstream HTTP Request
The dispatch issues an HTTP `POST` to the configured A2A downstream URL (`--a2a-downstream-url`) with a 30-second client timeout. The body carries `skill_name` and `payload`:

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
`DispatchToA2ADownstream` decodes the downstream response:
- A status other than `200` returns `ErrGatewayDownstreamHTTPError`.
- A `200` response whose body has a non-empty `error` returns `ErrGatewayA2AError`.
- A body that does not decode returns `ErrInternal`.
- Otherwise, a non-empty `summary` is the result. If `summary` is empty, a non-empty `result` is used. If both are empty, the result is the literal `"completed"`.

The Operator's A2A handler truncates the result to `ReceiptSummaryMaxBytes` (`4096` bytes, `internal/constants/pubsub.go`) before it becomes the receipt summary.

Any error from the handler finalizes the L5 receipt as `EXECUTION_STATUS_FAILED`, with the L5 execution stage marked failed. The dispatch path returns that signed FAILED receipt as the call's result (`executionFailedReceipt` and `verifiedTransactionEventReceipt`, `internal/services/gateway/dispatch_service.go`). The JSON-RPC response is therefore a success carrying a FAILED receipt, not a JSON-RPC error.

### 6.3 Shared Circuit Breaker
A2A dispatch and MCP egress use the same circuit-breaker state in `GatewayService` (`recordFailure`, `recordSuccess`, `isCircuitOpen`):
- **Failure Threshold**: A connection error or a 5xx response records a failure. After 5 failures without an intervening `200` response, the circuit opens.
- **Reset**: A `200` response clears the failure count and closes the circuit.
- **Cooldown Period**: The circuit reports open for 1 minute after the last failure. After that, the next request is attempted. No separate canary request is sent.
- **Fail-Closed Guard**: While open, `DispatchToA2ADownstream` returns `ErrGatewayDownstreamUnavailable` without contacting the downstream. The REST ingress (`handleA2ARequest`) rejects the call with JSON-RPC `-32603` before it builds an envelope.

---

## 7. Execution Success Response

When the call completes, the Gateway returns `A2ASuccessResponse` as the JSON-RPC `result`. Its `id` is the transaction hash and its `result` is the executing Operator's signed `EventReceipt`, encoded as canonical protojson with proto field names (`UseProtoNames`). Enum values use their proto names, such as `EXECUTION_STATUS_COMPLETED`. A FAILED receipt is returned the same way (§6.2).

```json
{
  "jsonrpc": "2.0",
  "id": 42,
  "result": {
    "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "result": {
      "transaction_id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "transaction_hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "status": "EXECUTION_STATUS_COMPLETED",
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

The example is abridged. `l2_status` and `l3_status` depend on posture; their values are defined by the `L2Status` and `L3Status` enums in `protocol/proto/ztd/operator/v1/operator.proto`. The full `EventReceipt` message is in the same file.

---

## 8. Protocol Error Codes

A2A errors conform to JSON-RPC 2.0 error objects. Verification codes are defined in `internal/constants/rpc_errors.go`. The standard codes `-32600`, `-32601`, and `-32603` are emitted as literals in `internal/services/mcp/`; `-32700` is `JSONRPCErrorCodeParseError` and `-32603` is `JSONRPCErrorCodeInternal` in `internal/constants/auth.go`.

Governance errors reach the wire through `mapGatewayError` (`internal/services/mcp/gateway.go`). Any error without a mapping returns `-32603`.

| Code | Constant | Cause |
|:---|:---|:---|
| `-32700` | `JSONRPCErrorCodeParseError` | Body is not valid JSON. On `POST /api/v1/a2a/call`, a JSON batch array also fails to decode and returns this code. |
| `-32600` | literal | REST: `jsonrpc` is not `"2.0"`, `method` is missing, or the body exceeds the payload limit. `/mcp`: a JSON-RPC batch array (the MCP endpoint rejects batches). |
| `-32601` | literal | REST: `method` other than `a2a/call`. `/mcp`: `a2a/call` when no A2A downstream is configured, or any unknown method. |
| `-32603` | `JSONRPCErrorCodeInternal` | Open circuit at ingress; request body read failure; invalid `params`; missing `skill_name`; any other error without a governance mapping. |
| `-32000` | `ErrCodeInvalidEnvelope` | Malformed envelope, missing transaction ID, missing payload, or unknown action type. |
| `-32001` | `ErrCodeHashMismatch` | Transaction hash missing or does not match recomputation. |
| `-32002` | `ErrCodeExpired` | Transaction validity deadline exceeded. |
| `-32003` | `ErrCodeReplay` | Nonce already reserved or used. |
| `-32004` | `ErrCodeStateMismatch` | Host state Merkle root missing, required, or divergent. |
| `-32005` | `ErrCodeL1ValidationFailed` | L1 Doctrine validation failed (forbidden pattern or threat indicator). |
| `-32006` | `ErrCodeL2SignatureInvalid` | L2 signature missing or invalid, consensus not configured, quorum not met, or duplicate signer. |
| `-32007` | `ErrCodeL3ProofInvalid` | L3 proof invalid, or notary not configured. |
| `-32008` | `ErrCodePayloadDecodeFailed` | Protobuf unmarshaling failure. |

`ErrCodeResourceNotFound` (`-32100`) and `ErrCodeGatewayNotReady` (`-32101`) are defined in `rpc_errors.go`. `mapGatewayError` has no case for them, so the A2A path does not return them.

---

## 9. Configuration & Operational Flags

A2A integration parameters are configured through Gateway CLI flags (confirm with `./ztd gw start --help`):

- **`--a2a-downstream-url <url>`**: Target HTTP endpoint of the downstream A2A executor (default: none). Setting it mounts A2A ingress. Leaving it unset mounts no A2A route.
- **`--public-base-url <url>`**: Canonical public base URL utilized to generate human passkey approval links in `GovernedSuspension` (e.g., `https://ztd.local:8443`).
- **`--posture <mode>`**: Gateway governance posture (`doctrine`, `consensus`, `ratify`, `notary`; default `doctrine`).
- **`--http-port <port>`**: Plain HTTP port for bootstrap and enrollment routes (effective default: `8080`). It does not serve A2A or MCP routes.
- **`--https-port <port>`**: HTTPS port for mTLS production ingress (effective default: `8443`).

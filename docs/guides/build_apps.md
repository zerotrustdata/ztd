---
doc_id: build_apps
title: Build ztd-Compatible Applications
audience: application developers and agent framework authors
status: current
version: v1.0.0
last_updated: 2026-10-14
owners:
  - docs/guides/build_apps.md
  - internal/services/mcp/
  - internal/services/governance/
related:
  - docs/guides/connect_apps_to_gateway.md
  - docs/architecture/agents.md
  - docs/architecture/protocol.md
  - docs/architecture/governance.md
when_to_read: Designing, building, and deploying applications, AI agents, or automated services that interact with the ztd Gateway.
do_not_use_for:
  - Gateway deployment and startup (docs/guides/build_gateway.md)
  - Operator internal lifecycle (docs/architecture/operator.md)
  - Frontend browser integration (docs/guides/build_frontend.md)
---

# Build ztd-Compatible Applications

## Overview

A ztd-compatible application is an untrusted client of the ztd Gateway. It submits typed intent through an authenticated Gateway ingress, receives governed results, and never sends mutations directly to a target host. The Gateway and the bound Governed Operator enforce the five-layer governance pipeline: L1 Doctrine, L2 Consensus, L3 Notary, L4 Warden, and L5 Actuator.

The Gateway supports four application integration patterns:

| Pattern | Client sends | Gateway responsibility | Credential |
| :--- | :--- | :--- | :--- |
| **MCP or A2A** | JSON-RPC tool or skill intent | Builds `GovernanceEnvelope`, binds state Merkle root, runs L2 deliberation, manages L3 suspension, dispatches action | Enrolled app or CLI mTLS certificate; JWT when JWKS configured |
| **Gateway HTTP dispatch** | Registered request `event_type` and base64 protobuf payload at `POST /api/v1/operators/commands` | Derives `action_type`, binds state root and posture, constructs envelope, dispatches to Operator | Enrolled app mTLS certificate and `AppPolicy` |
| **Direct envelope** | Complete canonical protojson `GovernanceEnvelope` | Verifies envelope structure, hash, state root, nonces, and proofs; dispatches synchronously | CLI or Operator mTLS certificate |
| **Audit ingest** | Registered audit request `event_type` at `POST /api/v1/audit/records` | Validates session binding, publishes to operator `audit:` channel, waits for append acknowledgement | Enrolled app mTLS certificate |

MCP and A2A are the standard application-facing surfaces. Direct envelope submission is a privileged integration for clients that already possess an authorized transport identity and can construct all required proofs.

---

## Choose an Integration Surface

### 1. Model Context Protocol (MCP)

Use MCP for standard AI agent tool discovery and execution. MCP clients connect through the stdio bridge, which unseals the enrolled identity in process:

```json
{
  "mcpServers": {
    "ztd-native": {
      "command": "/path/to/ztd",
      "args": ["mcp", "stdio"]
    }
  }
}
```

For an enrolled application identity, append `"--app", "etl-service"` to `args`. `./ztd mcp agent show <agent>` prints this configuration. The bridge forwards requests to `POST /mcp` (`Content-Type: application/json`); a tool call body is:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "run_shell_command",
    "arguments": { "command": "ls -la" }
  }
}
```

Discovery methods (`tools/list`, `resources/list`, `prompts/list`) return catalogs without triggering governance mutations. Invocation methods (`tools/call`, `resources/read`, `prompts/get`) enter the Gateway governance pipeline.

### 2. Agent-to-Agent (A2A)

Use A2A for inter-agent skill invocation: `POST /api/v1/a2a/call` over mTLS with `Content-Type: application/json`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "a2a/call",
  "params": {
    "skill_name": "data.process",
    "payload": { "input": "payload-data" }
  }
}
```

### 3. Governed Operator HTTP Dispatch

For enrolled applications targeting an outbound Operator: `POST /api/v1/operators/commands` over mTLS with the application certificate and `Content-Type: application/json`:

```json
{
  "target_operator_session_id": "<operator-session-id>",
  "event_type": "ztd.v1.operator.filesystem.read.requested",
  "payload": "<base64-encoded-protobuf-payload>"
}
```

From the CLI, `./ztd operator run <operator-session-id> --cmd "<command>"` dispatches a governed command through the same pipeline.

### 4. Direct Governance Envelope

Direct submission requires constructing the complete canonical protojson `GovernanceEnvelope` and sending it as the body of `POST /api/v1/governance/envelopes` over mTLS with `Content-Type: application/json`.

---

## Identity and Credentials

### Human and Developer Credentials

```bash
./ztd gw start
./ztd auth enroll user
```

Issues the CLI certificate with SPIFFE ID `spiffe://ztd.local/cli/<user_id>/<session_id>`. The private key is sealed under the keystore master key and is never written as PEM, so only `ztd` can use it. On a Linux host without an OS key store, load a key into an ssh-agent first (see [Encryption Architecture](../architecture/encryption.md#master-key-and-keyring-providers)).

### Application Workload Credentials

External applications enroll using platform enrollment:

```bash
./ztd auth enroll app etl-service
```

The platform owner approves the request:

```bash
./ztd auth enroll approve <request-id> --yes
```

The issued certificate carries SPIFFE ID `spiffe://ztd.local/app/etl-service`. The application's private key is sealed under the keystore master key and is never written as PEM. It is used only through `ztd` or the `ztd mcp stdio --app etl-service` bridge, which unseal it in process.

---

## Direct Envelope Construction

When constructing a `GovernanceEnvelope` directly:

1. **Fetch State Merkle Root**: `GET /api/v1/state` returns `state_merkle_root`.
2. **Serialize Typed Payload**: Create the protobuf message (e.g. `ztd.operator.v1.CommandRequested`), serialize to binary, and base64-encode.
3. **Compute Transaction Hash**: Compute lowercase hexadecimal SHA-256 over canonical fields:
   `action_type|target_resource|payload|state_merkle_root|nonce|expires_at|intent_data|requestor_user_id|acting_app_id|operator_id|operator_session_id|case_id|investigation_id|task_id|web_session_id|cli_session_id|`
4. **Attach Posture-Required Proofs**: Attach L2 consensus signatures (`<transaction_hash>|<decision>` signed with Ed25519) and L3 authorization if required by posture.
5. **Submit**: `POST /api/v1/governance/envelopes` returns HTTP 200 with `ActionReceipt`.

---

## Verifying Action Receipts

An `ActionReceipt` contains execution status, state roots, stage evidence, and signatures. Verify both signatures before trusting the outcome:

1. `receipt_signature`: Verified against the Operator or Gateway actuator Ed25519 public key.
2. `persistence_attestation.signature`: Verified against the local auditor key.

Use the reference verifiers `VerifyActionReceiptSignature` and `VerifyReceiptPersistenceAttestation` from `internal/services/governance/l5_actuator.go`.

---

## Security Requirements

- Send all mutations through a governed Gateway ingress; never bypass the PEP execution boundary.
- Validate Gateway TLS certificates; never disable verification.
- Rebuild envelopes on state-root mismatches (`TX_STATE_MISMATCH`) with fresh nonces and signatures.
- Verify receipt signatures against trusted actuator keys before consuming execution output.

---

## Next Steps

- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Endpoint reference and connection examples.
- [AI Agents and the ztd Governance Boundary](../architecture/agents.md) — Trust models for agentic systems.
- [Protocol Architecture](../architecture/protocol.md) — Wire schemas, types, and constants.
- [Consensus Architecture](../architecture/consensus.md) — L2 quorum and signer management.

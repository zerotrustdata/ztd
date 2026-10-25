---
doc_id: build_apps
title: Build ztd-Compatible Applications
audience: application developers and agent framework authors
status: current
version: v1.3.0
last_updated: 2026-10-23
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
| **MCP or A2A** | JSON-RPC tool or skill intent | Builds `GovernanceEnvelope`, binds state Merkle root, runs L2 deliberation, manages L3 suspension, dispatches action | Enrolled app or CLI mTLS certificate; JWT when JWKS is enabled (MCP and A2A) |
| **Gateway HTTP dispatch** | Registered request `event_type` and base64 protobuf payload at `POST /api/v1/operators/commands` | Derives `action_type`, binds state root and posture, constructs envelope, dispatches to Operator | Enrolled app mTLS certificate and `AppPolicy` |
| **Direct envelope** | Complete canonical protojson `GovernanceEnvelope` | Verifies envelope structure, hash, state root, nonces, and proofs; dispatches synchronously | CLI or Operator mTLS certificate; app mTLS certificate for platform document writes only |
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
    "arguments": { "command": "ls", "args": ["-la"] }
  }
}
```

`run_shell_command` takes one executable in `command` and its argv as separate strings in `args`. No shell runs, so pipes, redirects, globs, and substitutions are not interpreted. Discovery methods (`tools/list`, `resources/list`, `prompts/list`) return catalogs without triggering governance mutations. Invocation methods (`tools/call`, `resources/read`, `prompts/get`) enter the Gateway governance pipeline.

### 2. Agent-to-Agent (A2A)

Use A2A for inter-agent skill invocation. A2A is opt-in: the Gateway must be started with a downstream A2A server, `./ztd gw start --a2a-downstream-url <url>`. Without it, `POST /api/v1/a2a/call` is not registered and the MCP `a2a/call` method returns method-not-found. With it enabled, send `POST /api/v1/a2a/call` over mTLS with `Content-Type: application/json`:

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

Issues the CLI certificate with SPIFFE ID `spiffe://ztd.local/cli/<user_id>/<cli_session_id>`. The private key is sealed under the keystore master key and is not stored as a plaintext PEM file; `ztd` unseals it in process (see [INV-ENC-STORE-06](../architecture/encryption.md)). On a Linux host without an OS key store, load a key into an ssh-agent first (see [Encryption Architecture](../architecture/encryption.md#master-key-and-keyring-providers)).

### Application Workload Credentials

External applications enroll using platform enrollment:

```bash
./ztd auth enroll app etl-service
```

The platform owner sets `REQUEST_ID` to the request ID printed by the enrollment step, then approves the request:

```bash
./ztd auth enroll approve "$REQUEST_ID" --yes
```

The issued certificate carries SPIFFE ID `spiffe://ztd.local/app/etl-service`. The application's private key is sealed under the keystore master key and is not stored as a plaintext PEM file. It is used only through `ztd` or the `ztd mcp stdio --app etl-service` bridge, which unseal it in process.

`auth enroll app` waits for the owner decision. An authenticated owner CLI approves inline under `doctrine` and `consensus` postures. Under `ratify` and `notary`, the owner approves with a passkey at the printed URL. Without an owner CLI identity, approve from an owner CLI with `./ztd auth enroll approve <request-id> --yes`.

Application names MUST match `^[a-z0-9][a-z0-9._-]*$` and MUST NOT exceed 64 bytes. The Gateway enforces this rule on every platform enrollment request, and the CLI applies the same check before it submits one. Reserved platform names, including `ztd-ensemble` and `ensemble`, are rejected by the Gateway as well. Use `./ztd auth enroll app --help` for the enrollment flags.

### Consensus Member Applications

An application that serves as an L2 consensus member MUST enroll with an independent Ed25519 signing identity:

```bash
./ztd auth enroll app etl-service --signing-key
```

Bind the member set through governance. The primary application MUST appear in the member list, and a set of more than one member MUST state `--quorum` explicitly:

```bash
./ztd consensus enroll app etl-service https://etl.example.com:9443 --member-app etl-service --member-app risk-service --quorum 2
```

Every consensus binding names a provider, `application` or `tribunal`; an empty or unknown provider is rejected. The `application` provider uses the enrolled member set above. The alternative is the Gateway-hosted Tribunal panel, bound with `./ztd consensus enroll tribunal --model <tag>` (`--quorum` between 3 and 5, default 3). The panel is `tribunal-axiom`, `tribunal-concord`, `tribunal-variance`, `tribunal-pragma`, and `tribunal-nemesis`. Its members vote on envelopes that change state; a member's own INFERENCE call is never voted on, and the binding takes no application endpoint. Under `ratify` and `notary`, the binding requires passkey approval at the printed URL. `./ztd consensus status` prints the bound provider, quorum, member IDs, and signer readiness. The member lifecycle, deliberation endpoint, and vote verification are defined in [Consensus Architecture](../architecture/consensus.md).

---

## Direct Envelope Construction

When constructing a `GovernanceEnvelope` directly:

1. **Fetch State Merkle Root**: `GET /api/v1/state` returns `state_merkle_root`.
2. **Serialize Typed Payload**: Create the protobuf message (e.g. `ztd.operator.v1.CommandRequested`), serialize to binary, and base64-encode. Set `event_type` to a registered request event type and `protocol_version` to `"2"`. The Warden (L4) rejects any envelope whose `protocol_version` is not `"2"`.
3. **Compute Transaction Hash**: Compute the lowercase hexadecimal SHA-256 of `ztd-tx-v2|<action_type>|<event_type>|<canonical_v1>`. `<canonical_v1>` appends each populated field, followed by `|`, in this order: `action_type`, `target_resource`, `payload`, `state_merkle_root`, `nonce`, `expires_at`, `intent_data`, `requestor_user_id`, `acting_app_id`, `operator_id`, `operator_session_id`, `case_id`, `investigation_id`, `task_id`, `web_session_id`, `cli_session_id`. Absent optional fields are omitted. The canonical encoding is defined in [Governance Architecture](../architecture/governance.md).
4. **Attach Posture-Required Proofs**: Under `consensus` and `notary` postures, set `governance.l2.consensus_set_id` and include `votes` from at least K distinct bound members. Each vote carries `signer_key_id`, `decision`, and a hex-encoded Ed25519 `consensus_signature` over `<transaction_hash>|<decision>`. Attach L3 authorization when the posture requires it. Direct submission does not synthesize missing L2 or L3 proofs; the envelope fails closed at L4.
5. **Submit**: `POST /api/v1/governance/envelopes` returns HTTP 200 with `EventReceipt`.

---

## Verifying Event Receipts

An `EventReceipt` contains execution status, state roots, stage evidence, and signatures. Verify both signatures before trusting the outcome, using the Ed25519 Actuator public key bound to the receipt's `signer_key_id`:

1. `signature`: Signs the canonical receipt fields and deterministic stage evidence.
2. `final_persistence_attestation.signature`: Signs the persistence attestation. Its `signer_key_id` MUST equal the receipt's `signer_key_id`, and its `receipt_signature_digest` MUST match `signature`.

Use the reference verifiers `VerifyEventReceiptSignature` and `VerifyReceiptPersistenceAttestation` from `internal/services/governance/l5_actuator.go`.

---

## Security Requirements

- Applications MUST send all mutations through a governed Gateway ingress and MUST NOT bypass the PEP execution boundary.
- Applications MUST validate Gateway TLS certificates and MUST NOT disable verification.
- Applications MUST rebuild envelopes on state-root mismatches (`TX_STATE_MISMATCH`) with fresh nonces and signatures.
- Applications MUST verify receipt signatures against trusted actuator keys before consuming execution output.

---

## Next Steps

- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Endpoint reference and connection examples.
- [AI Agents and the ztd Governance Boundary](../architecture/agents.md) — Trust models for agentic systems.
- [Protocol Architecture](../architecture/protocol.md) — Wire schemas, types, and constants.
- [Consensus Architecture](../architecture/consensus.md) — L2 quorum and signer management.

---
doc_id: connect_apps_to_gateway
title: Connect Apps to Gateway
audience: application developers and system integrators
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/connect_apps_to_gateway.md
  - internal/services/gateway/
related:
  - docs/guides/build_apps.md
  - docs/guides/connect_frontend_to_gateway.md
  - docs/architecture/agents.md
  - docs/architecture/protocol.md
when_to_read: Connecting applications, SDKs, agent frameworks, or data pipelines to the ztd Gateway across MCP, A2A, HTTP dispatch, and document store APIs.
do_not_use_for:
  - Frontend browser integration and WebAuthn (docs/guides/connect_frontend_to_gateway.md)
  - Operator enrollment and deployment (docs/guides/connect_operator_to_gateway.md)
  - Full protocol wire specifications (protocol/docs/spec.md)
---

# Connect Apps to ztd Gateway

## Overview

The ztd Gateway serves as the central Policy Decision Point (PDP) for governed AI-agent mutations, while the executing Operator independently re-verifies envelopes and enforces L4/L5 execution boundaries. Applications connect via multiple protocol surfaces: MCP (Model Context Protocol), A2A (Agent-to-Agent), governed HTTP dispatch (`POST /api/v1/operators/commands`), audit ingest (`POST /api/v1/audit/records`), direct governance envelopes, WebSocket pub/sub for operator telemetry, and the document store API.

For architectural patterns and state models, see [Build Applications](./build_apps.md). For trust boundaries and ingress analysis, see [AI Agents and the ztd Governance Boundary](../architecture/agents.md).

### Prerequisites

- A running Gateway instance (`./ztd gw start` or `make up`).
- Enrolled CLI or application credentials with trust in the Gateway CA bundle at `.ztd/pki/trust/ztdg-ca-bundle.pem`.
- Enrolled identities held by `ztd`. The Gateway issues certificates but never possesses client private keys, and the client keys that `ztd` generates are sealed (see the note under Connectivity Methods).

A host Gateway listens on `localhost` port 8080 (HTTP) and port 8443 (HTTPS) by default.

---

## Gateway Surfaces and Ports

The Gateway exposes two protocol surfaces with distinct authentication requirements:

| Surface | Port (default) | Auth | Purpose |
| :--- | :--- | :--- | :--- |
| **HTTP Surface** | 8080 (HTTP) | Public or token-scoped | Health checks, bootstrap and platform enrollment discovery, CLI recovery, PKI discovery endpoints, deploy scripts |
| **HTTPS Surface** | 8443 (TLS) | Per route: public, mTLS, web session, dual, or JWT | Governance envelopes, MCP/A2A APIs, document store, WebSocket pub/sub, SSE, and console SPA |

Port mixing is prohibited. The Gateway fails startup if the HTTP and HTTPS surfaces share a port.

---

## Connectivity Methods

> **Identity keys are sealed.** The CLI key, each app key, and the pending enrollment state under `.ztd/` are sealed under the keystore master key (the OS key store, or an ssh-agent on Linux hosts without one). No PEM private key exists on disk, so no external process, `curl`, or SDK can read them. `ztd` unseals them in process: use `ztd` commands for Gateway requests, and put `ztd mcp stdio` (add `--app <name>` for an app identity) in front of any MCP client. A missing or locked master key fails closed, and a plaintext key file at a sealed path is refused with `ErrIdentityFilePlaintext`. See `docs/architecture/encryption.md` (`INV-ENC-STORE-06`).
>
> The HTTP sections below document the wire contract as method, path, and body. Routes that require mTLS, including the governance, audit, document store, and dispatch routes, authenticate with a client certificate chained to the Gateway CA bundle (`.ztd/pki/trust/ztdg-ca-bundle.pem`); the private key for that certificate must be held by the client process that makes the call. The Gateway classifies each HTTPS route by auth mode, and unclassified routes default to mTLS. When JWKS is configured, the MCP and A2A endpoints authenticate with JWT instead.

### 1. Model Context Protocol (MCP)

The Gateway exposes a unified streamable HTTP MCP endpoint at `/mcp` with fail-closed input validation for all tool calls.

MCP clients connect through the stdio bridge, which holds the mTLS session and unseals the identity in process. Print the client configuration with `./ztd mcp agent show <agent>` or add it by hand:

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

For an enrolled application identity, append `"--app", "etl-service"` to `args`. The bridge forwards each request to `POST /mcp` with `Content-Type: application/json`; a tool call body is:

```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "run_shell_command",
    "arguments": { "command": "ls -la" }
  },
  "id": 1
}
```

#### Downstream MCP Egress

Configure downstream MCP servers when starting the Gateway:

**Subprocess Downstream (stdio):**
```bash
./ztd gw start \
  --mcp-downstream-cmd npx \
  --mcp-downstream-args '-y,@modelcontextprotocol/server-filesystem,/var/data'
```

**HTTP Downstream:**
```bash
./ztd gw start \
  --mcp-downstream-url http://localhost:3000
```

`--mcp-downstream-cmd` and `--mcp-downstream-url` are mutually exclusive. All downstream tool executions are governed by the Gateway; outputs are scrubbed for sensitive data and receipts are recorded in the audit vault.

---

### 2. Agent-to-Agent (A2A)

The Gateway accepts JSON-RPC 2.0 skill invocations as `POST /api/v1/a2a/call` over mTLS with `Content-Type: application/json`:

```json
{
  "jsonrpc": "2.0",
  "method": "a2a/call",
  "params": {
    "skill_name": "data.process",
    "payload": { "input": "payload-data" }
  },
  "id": 1
}
```

---

### 3. Governed Operator HTTP Dispatch

Enrolled applications dispatch host operations through `POST /api/v1/operators/commands` over mTLS, authenticating with the application certificate (for example `etl-service`) and `Content-Type: application/json`:

```json
{
  "target_operator_session_id": "<operator-session-id>",
  "event_type": "ztd.v1.operator.filesystem.read.requested",
  "payload": "<base64-encoded-protobuf-payload>"
}
```

For an interactive equivalent from the CLI, run a governed command with `./ztd operator run <operator-session-id> --cmd "<command>"`.

---

### 4. Direct Governance Envelope

Privileged clients with CLI or Operator identities submit canonical protojson `GovernanceEnvelope` transactions directly as `POST /api/v1/governance/envelopes` over mTLS with `Content-Type: application/json`. The request body is the complete envelope JSON (see [Build Applications](build_apps.md) for construction).

---

### 5. WebSocket Pub/Sub

The Gateway provides mTLS-authenticated real-time pub/sub at `wss://<gateway>:8443/api/v1/pubsub/stream`. The wire format is binary protobuf messages of type `ztd.pubsub.v1.PubSubMessage`.

Canonical channels include:
- `cmd:<operator_id>:<operator_session_id>`: Gateway-to-Operator governed envelope delivery.
- `results:<operator_id>:<operator_session_id>`: Operator execution results.
- `receipts:<operator_id>:<operator_session_id>`: Operator receipts, intercepted by the Gateway.
- `heartbeat:<operator_id>:<operator_session_id>`: Operator liveness telemetry.

---

### 6. Document Store API

CRUD operations and query support are available under `/api/v1/data/{collection}/{id}` and `/api/v1/data/{collection}/_query`. From the CLI:

```bash
# List a collection, or read one document
./ztd gw data store --collection settings
./ztd gw data store --collection settings --document-id platform_settings
```

The query endpoint is `POST /api/v1/data/{collection}/_query` over mTLS with `Content-Type: application/json`:

```json
{
  "filters": [
    { "field": "status", "op": "==", "value": "open" }
  ],
  "limit": 10
}
```

Direct mutations (PUT, PATCH, DELETE) are restricted to infrastructure collections: `settings`, `users`, `operators`, `operator_sessions`, `bound_sessions`, `passkey_challenges`, `revoked_certificates`, and `trusted_signers`. Every other collection, including governed collections such as `cases`, `tasks`, and `memories`, permits reads via GET and requires `GovernanceEnvelope` transactions for mutations.

---

## Application Enrollment

Enroll an application workload using owner-approved platform enrollment:

```bash
./ztd auth enroll app etl-service
```

This generates an ECDSA P-256 private key and CSR locally, submits an enrollment request to the Gateway, and displays the approval command. The platform owner sets `REQUEST_ID` to the request ID that enrollment printed, then approves the request:

```bash
./ztd auth enroll approve "$REQUEST_ID" --yes
```

Upon approval, the certificate carrying `spiffe://ztd.local/app/etl-service` is stored and an `AppPolicy` is registered. The application's private key stays sealed under the keystore master key; use it through `./ztd mcp stdio --app etl-service` or other `ztd` commands.

### Consensus Members

A consensus member is an enrolled application bound into the Gateway's K-of-N consensus set. Enroll each member with `./ztd auth enroll app <name> --signing-key`, then bind the set with `./ztd auth enroll consensus bind <app-name> <https-endpoint>`, repeating `--member-app` for every member, including the primary app. A set with more than one member MUST state `--quorum` explicitly; a single application defaults to 1-of-1. The Gateway trims member IDs and rejects duplicate members and a quorum outside the member count. Binding requires the owner's CLI identity, and passkey approval under `ratify` and `notary`. See [Consensus](../architecture/consensus.md) for the signature model and `./ztd auth enroll consensus bind --help` for flags.

---

## Out-of-Band (OOB) Approval Flow

In `ratify` and `notary` postures, operations requiring human authorization without valid L3 proof are suspended for passkey approval:

1. Client submits request; Gateway stores transaction in suspended state.
2. Gateway returns approval URL: `https://<gateway>:8443/api/v1/approve/{tx_hash}` (redirects to console).
3. User completes WebAuthn passkey assertion.
4. Gateway attaches L3 proof and resumes execution.
5. Signed receipt is returned to client.

---

## Audit Receipts

List audit events for an operator session with `./ztd gw data audit list --operator-session-id <session-id>`, or aggregate them by type with `./ztd gw data audit summary`. Query one application's records with `--app`, which matches the exact `acting_app_id`, the application's SPIFFE URI (`spiffe://ztd.local/app/<name>`):

```bash
./ztd audit events --app spiffe://ztd.local/app/etl-service
./ztd audit summary --app spiffe://ztd.local/app/etl-service
```

Over HTTP, receipts are served by `GET /api/v1/audit/receipts` with an `operator_session_id` or `acting_app_id` query parameter, and exported as JSON by `GET /api/v1/audit/receipts/export?limit=100`, both over mTLS. To export every store to CSV with independent signature and hash-chain verification, run `./ztd report all`.

---

## Next Steps

- [Build Applications](build_apps.md) — Application design, envelope serialization, and verification patterns.
- [Connect Frontend to Gateway](connect_frontend_to_gateway.md) — Connect browser frontends with WebAuthn passkeys.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Operator enrollment and fleet operations.
- [MCP Specification](../../protocol/docs/mcp.md) — Protocol wire contract for Model Context Protocol.
- [A2A Specification](../../protocol/docs/a2a.md) — Protocol wire contract for Agent-to-Agent communication.

---
doc_id: connect_apps_to_gateway
title: Connect Apps to Gateway
audience: application developers and system integrators
status: current
version: v1.5.0
last_updated: 2026-10-28
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

By default the Gateway binds to `127.0.0.1` (`--listen-host`), serving HTTP on port 8080 (`--http-port`) and HTTPS on port 8443 (`--https-port`). When a default port is already in use, startup selects a nearby free port (`ResolveGatewayPorts` in `internal/config/config.go`).

---

## Gateway Surfaces and Ports

The Gateway exposes two protocol surfaces:

| Surface | Port (default) | Purpose |
| :--- | :--- | :--- |
| **HTTP Surface** | 8080 (`--http-port`) | Plain HTTP router: health, PKI discovery, bootstrap, CLI recovery, and platform enrollment routes |
| **HTTPS Surface** | 8443 (`--https-port`) | API routes for governance, MCP/A2A, document store, and WebSocket pub/sub. Each route has an auth mode (public, mTLS, web session, dual, or JWT) |

The HTTPS auth mode for each route is set by the route classification in `internal/services/gateway/gateway_auth.go`. Unclassified routes default to mTLS.

Port mixing is prohibited. The Gateway fails startup if the HTTP and HTTPS surfaces share a port.

---

## Connectivity Methods

> **Identity keys are sealed.** The CLI key, each app key, and the pending enrollment state under `.ztd/` are sealed under the keystore master key (the OS keyring, or an ssh-agent). External processes, `curl`, and SDKs cannot read them. `ztd` unseals them in process: use `ztd` commands for Gateway requests, and put `ztd mcp stdio` (add `--app <name>` for an app identity) in front of any MCP client. A missing or locked master key fails closed, and a plaintext key file at a sealed path is refused with `ErrIdentityFilePlaintext`. See `docs/architecture/encryption.md` (`INV-ENC-STORE-06`).
>
> The HTTP sections below document the wire contract as method, path, and body. Routes that require mTLS, including the governance, audit, document store, and dispatch routes, authenticate with a client certificate chained to the Gateway CA bundle (`.ztd/pki/trust/ztdg-ca-bundle.pem`); the private key for that certificate must be held by the client process that makes the call. The Gateway classifies each HTTPS route by auth mode, and unclassified routes default to mTLS. When JWKS is configured, the MCP and A2A endpoints authenticate with JWT instead.

### 1. Model Context Protocol (MCP)

The Gateway exposes a streamable HTTP MCP endpoint at `/mcp`.

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

For an enrolled application identity, append `"--app", "etl-service"` to `args`. The bridge carries MCP requests to the `/mcp` endpoint; a tool call body is:

```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "run_shell_command",
    "arguments": { "command": "ls", "args": ["-la"] }
  },
  "id": 1
}
```

`run_shell_command` takes one executable in `command` and its argv as separate strings in `args`. A `command` string containing spaces is rejected with the argv form to send instead, and shell-injection patterns are rejected. The executable is launched directly, except that a file starting with `#!` or a command not found on `PATH` runs through `sh -c`. Do not rely on pipes, redirects, globs, or substitutions.

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

`--mcp-downstream-cmd` and `--mcp-downstream-url` are mutually exclusive. Downstream tool output is scrubbed before it leaves the Gateway, and each downstream call is recorded in the Gateway's Local-First Events Store.

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
  "payload": "<base64-encoded-payload-bytes>"
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
- `receipts:<operator_id>:<operator_session_id>`: Operator receipts, processed by the Gateway.
- `heartbeat:<operator_id>:<operator_session_id>`: Operator liveness telemetry.

---

### 6. Document Store API

Read documents with `GET /api/v1/data/{collection}/{id}` and query them with `POST /api/v1/data/{collection}/_query`. `GET /api/v1/data/settings` reads platform settings. From the CLI:

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

The data API is read-only. Direct document writes (`POST`, `PUT`, `PATCH`, `DELETE`) and settings writes return HTTP 405 for every collection and authenticated identity. The query endpoint's `POST` remains a read. Submit application record changes through classified governed actions; platform authority records such as users, signers, revocations, and settings use their owning enrollment and management workflows and cannot be written through the generic data API. See [Authentication Architecture](../architecture/auth.md#route-classification-inv-auth-route) for the admission invariant.

---

## Application Enrollment

Enroll an application workload using owner-approved platform enrollment:

```bash
./ztd auth enroll app etl-service
```

This generates an ECDSA P-256 private key and CSR locally, then submits an enrollment request to the Gateway. Under `doctrine` and `consensus`, an authenticated owner approves inline. Under `ratify` and `notary`, approval requires a passkey at the printed URL. Without an owner CLI identity, the command prints the approval command. The platform owner runs it with the request ID that enrollment printed (shown here as `REQUEST_ID`):

```bash
./ztd auth enroll approve "$REQUEST_ID" --yes
```

Upon approval, the certificate carrying `spiffe://ztd.local/app/etl-service` is stored and an `AppPolicy` is registered. The application's private key stays sealed under the keystore master key; use it through `./ztd mcp stdio --app etl-service` or other `ztd` commands.

### Consensus Members

A consensus member is an enrolled application bound into the Gateway's K-of-N consensus set. Enroll each member with `./ztd auth enroll app <name> --signing-key`, then bind the set with `./ztd consensus enroll app <primary-app> <https-endpoint>`, repeating `--member-app` for every member, including the primary app. A set with more than one member MUST state `--quorum` explicitly; a single application defaults to 1-of-1. The CLI trims member IDs. The Gateway rejects an empty member list, duplicate members, members without an enabled trusted signer, and a quorum below 1 or above the member count. The provider must be `application` or `tribunal`; an empty provider is rejected. The `application` provider uses this enrolled member set. The alternative is the Gateway-hosted Tribunal panel, bound with `./ztd consensus enroll tribunal --model <tag>`, which takes no application endpoint. Binding an application set is limited to the owner (the first user), and passkey approval is required under `ratify` and `notary`. See [Consensus](../architecture/consensus.md) for the signature model, `./ztd consensus enroll app --help` for application flags, and `./ztd consensus enroll tribunal --help` for Tribunal flags. To clear the bound consensus, run `./ztd consensus unbind`. It is owner-only and goes through governance, and under `ratify` and `notary` it requires passkey approval like binding.

---

## Out-of-Band (OOB) Approval Flow

In `ratify` and `notary` postures, operations requiring human authorization without valid L3 proof are suspended for passkey approval:

1. Client submits request; Gateway stores transaction in suspended state.
2. Gateway returns an approval URL built from its public base URL and `/api/v1/approve/{tx_hash}`. Opening it redirects to the console at `/console/#approve=<tx_hash>`.
3. User completes WebAuthn passkey assertion.
4. Gateway attaches L3 proof and resumes execution.
5. The approval response carries the resulting signed `EventReceipt`.

---

## Event Receipts

An [Event Receipt](../reference/glossary.md#event-receipt) is the signed evidence for a governed request event. Audit events include receipt stages and other recorded facts; the event-receipt queries select the current signed receipt for each transaction.

List audit events for an operator session with `./ztd gw data audit list --operator-session-id <session-id>`, or aggregate them by type with `./ztd gw data audit summary`. Query one application's records with `--app`, which matches the exact `acting_app_id`, the application's SPIFFE URI (`spiffe://ztd.local/app/<name>`):

```bash
./ztd audit events --app spiffe://ztd.local/app/etl-service
./ztd audit summary --app spiffe://ztd.local/app/etl-service
```

Over HTTP, event receipts are served by `GET /api/v1/audit/event-receipts` with an `operator_session_id` or `acting_app_id` query parameter, and exported as JSON by `GET /api/v1/audit/event-receipts/export`, which accepts a `limit` parameter, both over mTLS. The list response uses the `event_receipts` key. Every recorded L2 member vote on one transaction is served by `GET /api/v1/audit/consensus-votes?tx_id=<id>`, oldest first. From the CLI, `./ztd audit votes --tx <transaction-id>` shows the same votes. To write the event receipts to CSV, run `./ztd report all`, which writes `event_receipts.csv` under a timestamped reports directory.

---

## Next Steps

- [Build Applications](build_apps.md) — Application design, envelope serialization, and verification patterns.
- [Connect Frontend to Gateway](connect_frontend_to_gateway.md) — Connect browser frontends with WebAuthn passkeys.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Operator enrollment and fleet operations.
- [MCP Specification](../../protocol/docs/mcp.md) — Protocol wire contract for Model Context Protocol.
- [A2A Specification](../../protocol/docs/a2a.md) — Protocol wire contract for Agent-to-Agent communication.

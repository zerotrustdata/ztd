---
doc_id: gateway
title: Gateway Architecture
audience: maintainers and coding agents
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - internal/services/gateway/
  - internal/cli/cmd/gw/
  - protocol/proto/ztd/common/v1/
related:
  - network.md
  - operator.md
  - governance.md
  - evals.md
  - public_spectator.md
  - sse.md
  - storage.md
when_to_read: Understanding the Governance Gateway architecture, operational modes, MCP endpoint implementation, governance layers, HTTP routing, and session management.
do_not_use_for:
  - Network topology and PKI hierarchy (see network.md)
  - Operator architecture and cross-gateway enrollment (see operator.md)
  - Storage and persistence details (see storage.md)
---

# Gateway Architecture

## Purpose

Defines the architecture of the ztd Governance Gateway (`ztd gw`). The Gateway is the central Policy Decision Point (PDP) and protocol backbone of the platform. It admits client transactions, enforces Layer 1 (L1) Doctrine, coordinates Layer 2 (L2) Consensus deliberation, manages Layer 3 (L3) Notary human authorization suspensions, manages the public key infrastructure (PKI), coordinates outbound-only pub/sub command channels to Governed Operators (PEPs), and operates an in-process embedded Operator substrate for Gateway-local execution. This document maps architecture to implementation in `internal/services/gateway/` and `internal/cli/cmd/gw/`.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Gateway Configuration and Posture (`INV-GW-POST`)](#gateway-configuration-and-posture-inv-gw-post)
  - [Network Surfaces and Route Classification (`INV-GW-NET`)](#network-surfaces-and-route-classification-inv-gw-net)
  - [Governance Pipeline Coordination (`INV-GW-GOV`)](#governance-pipeline-coordination-inv-gw-gov)
  - [MCP Native Tool Registry (`INV-GW-MCP`)](#mcp-native-tool-registry-inv-gw-mcp)
  - [Session Identity and State Tracking (`INV-GW-SESS`)](#session-identity-and-state-tracking-inv-gw-sess)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Gateway Operating Modes](#gateway-operating-modes)
  - [Configuration, Startup, and Launch Profiles](#configuration-startup-and-launch-profiles)
  - [Governance Postures](#governance-postures)
  - [HTTP and HTTPS Route Classifications](#http-and-https-route-classifications)
  - [Five-Layer Governance Verification Sequence](#five-layer-governance-verification-sequence)
  - [Model Context Protocol (MCP) Endpoint and Native Tools](#model-context-protocol-mcp-endpoint-and-native-tools)
  - [Agent Integration and MCP Stdio Bridge](#agent-integration-and-mcp-stdio-bridge)
  - [Incremental State Tracking and Merkle Roots](#incremental-state-tracking-and-merkle-roots)
  - [Readiness and Health Endpoints](#readiness-and-health-endpoints)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Gateway Configuration and Posture (`INV-GW-POST`)

| ID | Rule |
| :--- | :--- |
| INV-GW-POST-01 | The Gateway MUST run in one of four immutable governance postures: `doctrine` (L1 enforced, L2/L3 audited), `consensus` (L1/L2 enforced, L3 audited), `ratify` (L1/L3 enforced, L2 audited), or `notary` (L1/L2/L3 enforced). The posture is set once at startup via `--posture` and MUST NOT change at runtime. |
| INV-GW-POST-02 | Launch profiles written by `ztd gw start` to `.ztd/pids/operator-launch-profile.json` are versioned and mandatory for `ztd gw restart`. Restart MUST fail closed if the profile is missing, malformed, or of an unknown version. |

### Network Surfaces and Route Classification (`INV-GW-NET`)

| ID | Rule |
| :--- | :--- |
| INV-GW-NET-01 | The Gateway MUST expose two separate network surfaces: an HTTP listener on port 8080 (`GatewayHTTPPort`) for bootstrap, PKI discovery, health, and HTTPS redirects; and an HTTPS listener on port 8443 (`GatewayHTTPSPort`) for authenticated application, console, MCP, pub/sub, and governance traffic. Mutating or authenticated routes MUST NOT be served on port 8080. |
| INV-GW-NET-02 | The Gateway route classifier (`internal/services/gateway/gateway_auth.go`) MUST assign every route exactly one of four authentication policies: `RouteAuthNone`, `RouteAuthMTLS`, `RouteAuthWebSession`, or `RouteAuthDual`. Handlers MUST fail closed if route authentication requirements are unmet. |

### Governance Pipeline Coordination (`INV-GW-GOV`)

| ID | Rule |
| :--- | :--- |
| INV-GW-GOV-01 | Every transaction submitted via `POST /api/v1/governance/envelopes` or admitted via MCP/A2A MUST traverse the five-layer governance pipeline. The Gateway PDP coordinates L1–L3; the executing Operator PEP (embedded or remote) enforces L4–L5. |
| INV-GW-GOV-02 | The Gateway MUST NOT synthesize missing L2 consensus votes or L3 notary proofs for direct envelope submissions or governed HTTP command dispatch. Transactions lacking required proofs MUST fail closed at L4. |
| INV-GW-GOV-03 | Governed HTTP command dispatch (`POST /api/v1/operators/commands`) reports an admitted action whose handler ran and failed the same way for the embedded and an outbound Operator: HTTP 200 with the handler's typed failure result (`DispatchService`, `embeddedHandlerFailed`). An L4 rejection is 403 with a `GovernanceRejection` body that carries the signed rejection receipt (`INV-GOV-ACT-10`). Every other error body is an `IngressRefusal{code, error}` (`INV-GOV-ACT-11`) whose `error` renders only the typed sentinel, never the wrapped chain (`dispatchErrorCode`): an unresolved target operator session is 404 `OPERATOR_SESSION_NOT_FOUND`, witness capability 422, no delivery 503, result timeout 504, and an unclassified fault 500 `ErrInternal` with `UNCLASSIFIED`. Any embedded failure before or around the handler (no final receipt, no failed L5 execution stage) fails the dispatch. |
| INV-GW-GOV-04 | Model inference dispatch (`POST /api/v1/inference/dispatch`, `InferenceDispatchController`) MUST report every failure as a typed `InferenceDispatchFailure` body: the public sentinel text plus an `InferenceDispatchFailureCode` from the single sentinel↔code table in `internal/services/inference/dispatch/failure.go`. A stream that already started NDJSON delivery ends with an `InferenceDispatchStreamFailure` frame that carries the same code. The `dispatch failed` log records the code; the provider response body MUST NOT be logged or returned. A request fault without a dispatch sentinel (method, body size, JSON decode, identity binding) carries `INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED`. |
| INV-GW-GOV-05 | Direct envelope submission (`POST /api/v1/governance/envelopes`, `envelopeErrorCode` in `internal/services/gateway/governance_controller.go`) MUST return the signed receipt as canonical protojson (proto field names) on 200 and map errors by type, never to 500 for a governance sentinel: a malformed request (unreadable body, caller-supplied posture, undecodable envelope) is 400; an L4 rejection is 403 with a `GovernanceRejection` body, or 503 when the verifier cannot run (`RUNTIME_VAULT_LOCKED`, `VERIFIER_MISCONFIGURED`); a refusal before L4 (transport identity binding, `ErrTxOutcomeNotRegistered`, `ErrTxOutcomeNotAllowed`, `ErrTxL3ProofUnmintable`) is 403 without a receipt. Every non-rejection error body is an `IngressRefusal{code, error}` (`INV-GOV-ACT-11`) whose `error` renders the typed sentinel only, never the wrapped chain. Only an unclassified fault is 500. |
| INV-GW-GOV-06 | Receipt signers resolve by the executing Operator's identity. The embedded Operator (`constants.DocIDEmbeddedOperator`) resolves to the Gateway Actuator key. An outbound Operator resolves to the Actuator key bound on its `OperatorDocument` at enrollment (`actuator_public_key`, `actuator_key_id`). A receipt's self-declared `signer_key_id` MUST equal the bound key id and is never a trust source. Otherwise the receipt fails closed with `ErrReceiptSignerNotBound`. Owner: `SignerStoreService.ReceiptSignerKey`. |

### MCP Native Tool Registry (`INV-GW-MCP`)

| ID | Rule |
| :--- | :--- |
| INV-GW-MCP-01 | The Gateway MUST compile a fixed registry of 32 native tools (`internal/services/mcp/native_tool_registry.go`). All tools MUST enforce fail-closed input validation per category (SQL, URL, path, protocol, hostname, cloud metadata). |
| INV-GW-MCP-02 | Native MCP tools MUST implement the `mcp.NativeTool` interface and MUST be registered explicitly during startup via `RegisterNativeTools`. Dynamic registration via package `init()` is prohibited. |

### Session Identity and State Tracking (`INV-GW-SESS`)

| ID | Rule |
| :--- | :--- |
| INV-GW-SESS-01 | Session types MUST be strictly bound: `operator_session_id` (mTLS Operator certificate), `cli_session_id` (mTLS CLI certificate), `web_session_id` (WebAuthn passkey), or `sub` (JWT subject ID). A single request MUST carry exactly one authoritative session identifier. |
| INV-GW-SESS-02 | The Gateway state root service MUST maintain incremental 16-ary Merkle tree commitments (algorithm 2). The Bound State root MUST gate transaction admission; the Observed State root MUST capture telemetry without invalidating in-flight transaction freshness. |

## Owned surfaces

| Surface | Path | Verify |
| :--- | :--- | :--- |
| Gateway daemon and startup | `internal/cli/cmd/gw/gateway.go`, `internal/services/gateway/` | `ztd gw start --help`, `./ztd gw status` |
| HTTP/HTTPS port constants | `internal/constants/ports.go`, `internal/constants/network.go` | `GatewayHTTPPort = "8080"`, `GatewayHTTPSPort = "8443"` |
| Governance coordination (L1–L3) | `internal/services/governance/` | `l1_doctrine.go`, `l2_consensus.go`, `l3_notary.go` |
| Native MCP tool registry | `internal/services/mcp/native_tool_registry.go` | `native_tool_registry_test.go` |
| Route classification and auth | `internal/services/gateway/gateway_http_router.go`, `gateway_auth.go` | Route registry verification in `gateway_auth_test.go` |
| Embedded Console frontend | `internal/services/gateway/console/`, `console/` | `make console-embed-check` |
| Session management | `internal/services/gateway/operator_session_service.go` | Session tests in `internal/services/gateway/` |
| Incremental Merkle state service | `internal/services/gateway/state_root_service.go` | State root tests in `state_root_service_test.go` |

## Procedures

### Gateway Operating Modes

The Gateway binary operates in two distinct execution modes:

1. **Governance Gateway Mode (`ztd gw start`)**: The central control plane and PDP. It binds the HTTP router (8080) and HTTPS router (8443), initializes the SQLite database (`.ztd/data/ztd.db`), hosts the PKI certificate authority, screens transactions against L1 Doctrine, delegates L2 deliberation to enrolled Consensus services, manages L3 human WebAuthn suspensions, brokers WebSocket pub/sub command channels to remote Operators, and runs an in-process embedded Operator substrate for Gateway-host execution.
2. **Standard Operator Mode (`ztd operator start`)**: When started as an Operator, the same static binary operates as a remote Policy Execution Point (PEP). It opens an outbound-only mTLS WebSocket connection to the Gateway, receives transactions on its assigned command channel, executes independent L4 Warden verification, and enforces L5 Actuator execution. The Gateway does not make inbound connections to remote Operators.

### Configuration, Startup, and Launch Profiles

The Gateway is configured via CLI flags or an interactive setup wizard:
- `ztd gw start [--posture <posture>] [--http-port 8080] [--https-port 8443] [--listen-host 127.0.0.1]`: Starts the Gateway. The listeners bind loopback by default; specify `--listen-host 0.0.0.0` when remote Operators or external clients connect.
- Port selection (background start): when the requested HTTP port is held by a live listener, `gw start` moves both ports up by the same offset, skipping the Gateway's own auxiliary ports (8081, 8082, 5173), and prints the holder and the bound ports (`internal/cli/platform/process.go`). On Linux the availability probe keeps `SO_REUSEADDR`, matching the Gateway's own bind, so TIME_WAIT sockets left by a Gateway that just stopped do not count as busy; on BSD/macOS it disables `SO_REUSEADDR` so a wildcard probe still detects a listener on a specific address (`internal/netutil/port_probe_other.go`). The bound ports are persisted in the launch profile. Every CLI command run without `--endpoint`/`--port` reads them from the launch profile while its Gateway is running (`recordLocalGatewayPorts` in `internal/cli/cmd/main.go`, `config.SetLocalGatewayPorts`). The startup guidance and `gw status` print the Console URL and the Operator `--endpoint <host>:<http> --port <https>` command on the bound ports.
- `ztd gw start` against a running Gateway (background start, without `--follow`): compares the startup flags the caller set explicitly among `--capabilities`, `--posture`, `--listen-host`, `--http-port`, `--https-port`, `--model-storage-root`, `--inference-ollama-endpoint`, `--public-base-url`, `--cors-origin`, and `--passkey-rp-id`/`--passkey-rp-name`/`--passkey-rp-origin` with the running launch profile (`gateway_start_running.go`); a `ZTD_OLLAMA_ENDPOINT` value (exported or from `.env`) counts as setting `--inference-ollama-endpoint`. Other flags are not compared. It reuses the Gateway when they match and otherwise lists the differences and fails with `ErrGatewayAlreadyRunning`. Without a launch profile, any of these flags fails the same way (`ErrLaunchProfileMissing`). Stop the Gateway (`ztd gw stop` or `make down`) to change them; `.ztd/` state is kept.
- `ztd gw setup`: Launches an interactive terminal wizard producing a resolved platform configuration.
- `ztd gw start -i`: Launches the interactive setup wizard, then boots the Gateway immediately with the confirmed settings.
- `ztd gw restart`: Reads the persisted launch profile from `.ztd/pids/operator-launch-profile.json` and restarts the process. Fails closed if the profile is missing or invalid.
- `ztd gw stop`: Gracefully terminates the running Gateway process and flushes SQLite buffers.
- `ztd gw status`: Emits Gateway state and PID, the Operator inventory with capabilities, enrollment counts, and the Console URL.

### Governance Postures

The Gateway enforces one of four postures selected at startup via `--posture`:

| Posture | L1 Doctrine | L2 Consensus | L3 Notary (Mutations) | L3 Notary (Read-Only) | Operational Use Case |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`doctrine`** | Enforced | Audited | Audited | Not Required | Local development, automated testing, and CI pipelines |
| **`consensus`** | Enforced | Enforced ($K$-of-$N$) | Audited | Not Required | Multi-agent collaboration requiring peer review |
| **`ratify`** | Enforced | Audited | Enforced (Human) | Not Required | Workflows requiring human approval for state mutations |
| **`notary`** | Enforced | Enforced ($K$-of-$N$) | Enforced (Human) | Not Required | Strict production governance with both peer and human interlocks |

### HTTP and HTTPS Route Classifications

The Gateway segments traffic across two network listeners:

#### HTTP Surface (Port 8080)
The HTTP router (`buildHTTPRouter`) serves unauthenticated discovery endpoints:
- `/.well-known/ztd/pki/*`: CA root bundles and CRL endpoints.
- `/health`: Lightweight bootstrap health check.
- `/ws/operator/bootstrap`: Bootstrap WebSocket for Operator enrollment.
- Catch-all: Permanently redirects all other paths to HTTPS on port 8443.

#### HTTPS Surface (Port 8443)
The HTTPS router (`buildPublicRouter`) manages authenticated platform APIs. The route registry in `gateway_auth.go` classifies routes into four strict policies:

| Auth Policy | Routes Covered | Identity Requirement |
| :--- | :--- | :--- |
| **`RouteAuthNone`** | `/health`, `/console/*`, WebAuthn login/assertion ceremonies, bootstrap token validation | No identity required |
| **`RouteAuthMTLS`** | `/api/v1/operators/commands`, `/api/v1/governance/envelopes`, `/api/v1/pki/*`, pub/sub WebSockets, observe producer routes | Valid mTLS client certificate |
| **`RouteAuthWebSession`** | `/api/v1/approvals/*`, `/api/v1/users/*`, passkey management, console data APIs | Valid WebAuthn session cookie |
| **`RouteAuthDual`** | `/api/v1/sse/stream`, `/api/v1/sse/events`, `/api/v1/operators` (listing), `/api/v1/approvals/respond` | Either valid mTLS client certificate OR web session cookie |

### Five-Layer Governance Verification Sequence

Every transaction entering `POST /api/v1/governance/envelopes` or arriving via MCP/A2A traverses all five governance layers:

1. **L1 Doctrine (Gateway PDP)**: Static regex screening on protobuf string fields and MITRE ATT&CK heuristics on commands, arguments, and file edits. Violations reject fail closed with `ErrTxL1ValidationFailed`.
2. **L2 Consensus (Gateway Coordination)**: Delegates deliberation to enrolled Consensus services. Members sign `<transaction_hash>|<decision>` with Ed25519 keys. The Gateway attaches votes to the envelope.
3. **L3 Notary (Gateway Suspension)**: For mutation actions under `ratify` or `notary`, transactions lacking human proof are suspended in SQLite (`SuspendedTransactionStore`). The Gateway issues an approval challenge. A WebAuthn passkey assertion produces the proof and resumes execution.
4. **L4 Warden (Operator PEP)**: Executes pre-dispatch verification in the target Operator runtime: validates nonce replay against SQLite, verifies TTL expiry, recomputes the canonical transaction hash, checks the state Merkle root, and enforces posture-required L2/L3 proofs.
5. **L5 Actuator (Operator PEP)**: The singular execution boundary. Persists an initial `EXECUTING` receipt, appends to the SQLite commitment ledger, rehydrates scrubbed secrets, mints a short-lived capability, dispatches to the handler, dissolves the capability, and signs the final `COMPLETED` or `FAILED` receipt.

### Model Context Protocol (MCP) Endpoint and Native Tools

The Gateway serves an MCP endpoint at `POST /mcp` supporting JSON-RPC 2.0 methods: `initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/read`, `prompts/list`, `prompts/get`, and `a2a/call`.

The native tool inventory is `RegisterNativeTools` in `internal/services/mcp/native_tool_registry.go`; documentation does not duplicate the tool list (INV-MCP-03 in [Developer Guidelines](../devs/devs.md)). Tools span database, filesystem, network, process, system, configuration and cloud, and execution and audit domains.

All tools enforce fail-closed input validation: SQL tools reject multi-statement injection; URLs reject private IP ranges (SSRF prevention); filesystem tools reject directory traversal; command tools restrict metacharacters.

### Agent Integration and MCP Stdio Bridge

The Gateway provides seamless integration for external AI coding tools (Claude Code, OpenAI Codex, Devin CLI, Gemini CLI, Goose) through `ztd mcp agent`:
- `ztd mcp agent list`: Lists the supported agent IDs and display names.
- `ztd mcp agent show <agent>`: Prints the Stdio Transport MCP client configuration, for tools the launcher does not support.
- `ztd mcp agent run <agent>`: Launches the specified agent with native tools disabled, proxying all tool traffic through `ztd mcp stdio` to the Gateway's authenticated HTTPS endpoint.
- `ztd mcp agent verify <agent>`: Validates agent configuration and tool lockdown without executing the agent binary (safe for CI).
- When an operation requires L3 approval, the stdio bridge opens the browser approval URL, listens for the `approval.completed` event on the Gateway SSE stream, and resumes the pending transaction.

### Incremental State Tracking and Merkle Roots

The Gateway maintains an incremental 16-ary Merkle commitment tree (algorithm 2) over platform state:
- **Bound State**: Covers authoritative documents, bound KV entries, blobs, and token maps. The Bound State root is embedded into `GovernanceEnvelope.StateMerkleRoot` and gates transaction admission.
- **Observed State**: Covers transient telemetry, observer windows, and heartbeat timestamps. Captured in a separate Merkle root chained into the audit ledger without invalidating in-flight transaction freshness.

### Readiness and Health Endpoints

- **Readiness Check (`HTTPS /health`, Port 8443)**: Verifies that SQLite databases are mounted, the state Merkle root is calculated, and the governance pipeline is initialized. Returns `status`, `mode`, `version`, `pid`, `governance_ready`, and `state_merkle_root`.
- **Bootstrap Check (`HTTP /health`, Port 8080)**: Lightweight process check verifying listener availability prior to database initialization.

## Anti-patterns

- **Serving Application Routes over Plaintext HTTP**: Hosting MCP, governance, or pub/sub endpoints on port 8080 instead of HTTPS port 8443 (INV-GW-NET-01).
- **Mutating Governance Posture at Runtime**: Attempting to alter `--posture` without a complete Gateway restart (INV-GW-POST-01).
- **Restarting with an Invalid Launch Profile**: Executing `ztd gw restart` when `.ztd/pids/operator-launch-profile.json` is missing or corrupted (INV-GW-POST-02).
- **Ad-Hoc Endpoint Authentication Checks**: Bypassing the centralized route classification in `gateway_auth.go` (INV-GW-NET-02).
- **Synthesizing Missing L2/L3 Proofs**: Fabricating consensus signatures or notary approvals for direct envelope submissions (INV-GW-GOV-02).
- **Registering Native Tools via Package init()**: Registering tools implicitly instead of through explicit registration in `RegisterNativeTools` (INV-GW-MCP-02).

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Operator Architecture](operator.md): Outbound Operator execution boundary, native tool handlers, and local audit storage.
- [Governance Architecture](governance.md): Detailed five-layer interlock sequence, envelope structure, and posture semantics.
- [Network Architecture](network.md): PKI hierarchy, TLS 1.3/mTLS transport, SPIFFE identity, and port topology.
- [Authentication Architecture](auth.md): Authentication models, WebAuthn passkey enrollment, and CLI sessions.
- [Storage Architecture](storage.md): SQLite schema layouts, commitment ledgers, and document stores.
- [SSE Streaming](sse.md): Server-Sent Events architecture for live telemetry and approvals.
- [Public Spectator Architecture](public_spectator.md): Read-only spectator mirror and threat model.
- [AI Agents and Governance Boundary](agents.md): Agent ingress paths, downstream MCP egress, and tool call constraints.

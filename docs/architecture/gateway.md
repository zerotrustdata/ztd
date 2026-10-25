---
doc_id: gateway
title: Gateway Architecture
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
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
| INV-GW-POST-01 | The Gateway MUST run in one of four immutable governance postures defined by `constants.Posture` (`internal/constants/posture.go`): `doctrine` (default; L1 enforced, L2/L3 audited), `consensus` (L1/L2 enforced, L3 audited), `ratify` (L1/L3 enforced, L2 audited), or `notary` (L1/L2/L3 enforced). The posture is set once at startup via `--posture`, validated via `constants.ParsePosture`, which wraps `ErrInvalidPosture` for unknown or empty values, and MUST NOT change at runtime. |
| INV-GW-POST-02 | Launch profiles written by `ztd gw start` to `.ztd/pids/operator-launch-profile.json` are versioned and mandatory for `ztd gw restart`. Restart MUST fail closed if the profile is missing, malformed, or of an unknown version. |
| INV-GW-POST-03 | During Gateway daemon startup (`gateway.NewGatewayService`), the in-process embedded Operator MUST be registered as pending and its runtime configuration committed immediately after inserting the pending Gateway record, before initializing PKI, enrollment services, or listeners. This MUST reserve the embedded Operator's configured singleton capabilities up front so conflicting remote Operator enrollments cannot race or preempt them. |

### Network Surfaces and Route Classification (`INV-GW-NET`)

| ID | Rule |
| :--- | :--- |
| INV-GW-NET-01 | The Gateway MUST expose separate HTTP and HTTPS surfaces. Their default ports are 8080 (`GatewayHTTPPort`) and 8443 (`GatewayHTTPSPort`); after configuration validation, startup probes for an available pair and can select matching higher offsets. HTTP serves bootstrap, PKI discovery, health, and HTTPS redirects; HTTPS serves authenticated application, console, MCP, pub/sub, and governance traffic. Mutating or authenticated routes MUST NOT be served on HTTP. |
| INV-GW-NET-02 | The Gateway route classifier (`internal/services/gateway/gateway_auth.go`) MUST assign every route exactly one of four authentication policies: `RouteAuthNone`, `RouteAuthMTLS`, `RouteAuthWebSession`, or `RouteAuthDual`. Handlers MUST fail closed if route authentication requirements are unmet. |

### Governance Pipeline Coordination (`INV-GW-GOV`)

| ID | Rule |
| :--- | :--- |
| INV-GW-GOV-01 | Every transaction submitted via `POST /api/v1/governance/envelopes` or admitted via MCP/A2A MUST traverse the five-layer governance pipeline. The Gateway PDP coordinates L1–L3; the executing Operator PEP (embedded or remote) enforces L4–L5. |
| INV-GW-GOV-02 | The Gateway MUST NOT synthesize missing L2 consensus votes or L3 notary proofs for direct envelope submissions or governed HTTP command dispatch. Transactions lacking required proofs MUST fail closed at L4. |
| INV-GW-GOV-03 | Governed HTTP command dispatch (`POST /api/v1/operators/commands`) reports an admitted action whose handler ran and failed the same way for the embedded and an outbound Operator: HTTP 200 with a `GovernedDispatchResult` carrying the handler's typed failure result and the executing Operator's verified final `EventReceipt` (`DispatchService`, `executionFailedReceipt`). Every non-inference success, for every event type, waits for both the terminal result and that verified final receipt, in either order; a result without a verified receipt ends in result timeout, never a success. The exceptions are `MCP_CALL` and the observation actions (`PROVIDER_BOUNDARY_OBSERVATION`, `MODEL_PROVENANCE_OBSERVATION`), which publish no result envelope: their verified final receipt is the terminal outcome. An `MCP_CALL` tool result is the receipt's signed `ResultSummary`. `DispatchService.DispatchObservation` accepts only observation actions (`ErrTxEventActionMismatch`), returns the verified `COMPLETED` receipt, and fails closed with `ErrEvaluationObservationUnavailable` on any other final status. An L4 rejection is 403 with a `GovernanceRejection` body that carries the signed rejection receipt (`INV-GOV-ACT-10`). Every other error body is an `IngressRefusal{code, error}` (`INV-GOV-ACT-11`) whose `error` renders only the typed sentinel, never the wrapped chain (`dispatchErrorCode`): an unresolved target operator session is 404 `OPERATOR_SESSION_NOT_FOUND`, witness capability 422, no delivery 503, result timeout 504, and an unclassified fault 500 `ErrInternal` with `UNCLASSIFIED`. Any embedded failure before or around the handler (no final receipt, no failed L5 execution stage) fails the dispatch. |
| INV-GW-GOV-04 | Model inference dispatch (`POST /api/v1/inference/dispatch`, `InferenceDispatchController`) MUST report every failure as a typed `InferenceDispatchFailure` body: the public sentinel text plus an `InferenceDispatchFailureCode` from the single sentinel↔code table in `internal/services/inference/dispatch/failure.go`. When dispatch reached the executing Operator and the provider subsequently failed or governance rejected the execution, `InferenceDispatchFailure.receipt` MUST carry the executing Operator's verified signed `FAILED` receipt. A stream that already started NDJSON delivery ends with an `InferenceDispatchStreamFailure` frame that carries the same code. The `dispatch failed` log records the code; the provider response body MUST NOT be logged or returned. A request fault without a dispatch sentinel (method, body size, JSON decode, identity binding) carries `INFERENCE_DISPATCH_FAILURE_CODE_UNSPECIFIED`. |
| INV-GW-GOV-05 | Direct envelope submission (`POST /api/v1/governance/envelopes`, `envelopeErrorCode` in `internal/services/gateway/governance_controller.go`) MUST return the signed receipt as canonical protojson (proto field names) on 200 and map errors by type, never to 500 for a governance sentinel: a malformed request (unreadable body, caller-supplied posture, undecodable envelope) is 400; an L4 rejection is 403 with a `GovernanceRejection` body, or 503 when the verifier cannot run (`RUNTIME_VAULT_LOCKED`, `VERIFIER_MISCONFIGURED`); a refusal before L4 (transport identity binding, `ErrTxOutcomeNotRegistered`, `ErrTxOutcomeNotAllowed`, `ErrTxL3ProofUnmintable`) is 403 without a receipt. Every non-rejection error body is an `IngressRefusal{code, error}` (`INV-GOV-ACT-11`) whose `error` renders the typed sentinel only, never the wrapped chain. Only an unclassified fault is 500. |
| INV-GW-GOV-06 | Receipt signers resolve by the executing Operator's identity. The embedded Operator (`constants.DocIDEmbeddedOperator`) resolves to the Gateway Actuator key. An outbound Operator resolves to the Actuator key bound on its `OperatorDocument` at enrollment (`actuator_public_key`, `actuator_key_id`). A receipt's self-declared `signer_key_id` MUST equal the bound key id and is never a trust source. Otherwise the receipt fails closed with `ErrReceiptSignerNotBound`. Owner: `SignerStoreService.ReceiptSignerKey`. |
| INV-GW-GOV-07 | Tribunal intent dispatch (`POST /api/v1/operators/intents`, `TribunalIntentController`) requires an app workload identity with a delegated user. The Gateway's Tribunal (`tribunal.Service.Deliberate`) runs every member call as a governed `INFERENCE` dispatch under that identity, through the same `dispatch.DispatchService` as `POST /api/v1/inference/dispatch`, and returns each call with its signed receipt or typed `InferenceDispatchFailure`. Only an `APPROVED` command is dispatched, as an ordinary governed `MCP_CALL run_shell_command` through `DispatchService.Dispatch`, using the exact approved canonical argument JSON, so L2/L3 posture gating and suspension apply unchanged. Before publish, the Tribunal's contextual admission review (`DispatchRequest.OnAdmitted`) either releases the admitted envelope unchanged or withdraws it, and a revised command then needs a fresh envelope with its own L1, L2, and L3 admission, up to `tribunal.MaxAdmissionReviews` envelopes per intent (see [Production Tribunal](agents.md#production-tribunal-intent-deliberation-pipeline)). The shell-authoring Tribunal deliberation never supplies L2 or replaces an L3 approval. The approved envelope is voted on independently by the bound consensus, which may be the Gateway-hosted Tribunal provider (`tribunal` binding). The Gateway hosts that vote producer and receives member signing keys through the host-supplied signer: each key is sealed under this host's `.ztd/pki/apps/`. A `tribunal-*` app enrolled on another host cannot sign here; that member casts no vote, and L4 fails closed when quorum is not met. A completed deliberation is HTTP 200 `TribunalIntentResult`, and a deliberation fault is 500 `IngressRefusal`; the command outcome is exactly one of a `GovernedDispatchResult`, a signed `GovernanceRejection`, or an `IngressRefusal` carrying only the sentinel. A member template that sets a Gateway-owned field (messages, tools, attempt id, acting app, stream, response format) is 400 `ErrTribunalTemplateInvalid`; the Gateway sets each call's JSON response schema per role (`internal/services/tribunal/schema.go`). A `tier` that is unspecified or not a campaign role is 400 `ErrMissingRequiredField` or `ErrInferenceRoleInvalid`, and a member template `role` that is not the inference role of `tier` is 400 `ErrTribunalTierMismatch`, so every recorded step names the role its calls run under. |

### MCP Native Tool Registry (`INV-GW-MCP`)

| ID | Rule |
| :--- | :--- |
| INV-GW-MCP-01 | The Gateway MUST compile a fixed registry of native tools registered explicitly in `RegisterNativeTools` (`internal/services/mcp/native_tool_registry.go`). All tools MUST enforce fail-closed input validation per category (SQL, URL, path, protocol, hostname, cloud metadata). |
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
| Single posture owner | `internal/constants/posture.go` (`constants.Posture`) | `Posture.Valid()`, `RequiresL2()`, `RequiresL3()`, `Description()`, `HumanApprovalReason()` |
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

1. **Governance Gateway Mode (`ztd gw start`)**: The central control plane and PDP. It binds the HTTP and HTTPS routers (default ports 8080 and 8443; startup may resolve an available pair at higher offsets), initializes the SQLite database (`.ztd/data/ztd.db`), hosts the PKI certificate authority, screens transactions against L1 Doctrine, delegates L2 deliberation for state-changing actions (`application/x-protobuf`) over HTTPS to the bound consensus policy's deliberation endpoint, or to the Gateway-hosted Tribunal for a `tribunal` binding, manages L3 human WebAuthn suspensions, brokers WebSocket pub/sub command channels to remote Operators, and runs an in-process embedded Operator substrate for Gateway-host execution.
2. **Standard Operator Mode (`ztd operator start`)**: When started as an Operator, the same static binary operates as a remote Policy Execution Point (PEP). It opens an outbound-only mTLS WebSocket connection to the Gateway, receives transactions on its assigned command channel, executes independent L4 Warden verification, and enforces L5 Actuator execution. The Gateway does not make inbound connections to remote Operators.

### Configuration, Startup, and Launch Profiles

Gateway HTTP, HTTPS, mirror, and explorer servers send standard-library diagnostics through their injected platform logger, preserving its destination, level filtering, and server attributes. Normal HTTP server closure returns successfully. Mirror response writes classify broken pipes, reset or closed connections, and caller cancellation at debug level; unexpected response failures remain errors. Inference dispatch failures log the typed failure code and available provider-attempt and transaction IDs. The executing inference handler logs a fixed failure category and safe transport error kind, never provider response text or prompt content.

The Gateway initializes its services in strict sequence: after creating the local runtime directory and persisting the pending Gateway record, it immediately registers the embedded Operator substrate as pending and commits its runtime configuration (`UpdateOperatorRuntimeConfig`). This embedded-first registration reserves its configured singleton capabilities (such as `data` and `inference`) before PKI certificate authority creation, remote enrollment handlers, or network listeners are booted, preventing incoming remote Operator enrollments from racing or preempting Gateway-local capabilities.

The Gateway is configured via CLI flags or an interactive setup wizard:
- `ztd gw start [--posture <posture>] [--http-port 8080] [--https-port 8443] [--listen-host 127.0.0.1]`: Starts the Gateway. The listeners bind loopback by default; specify `--listen-host 0.0.0.0` when remote Operators or external clients connect.
- Port selection (background start): when the requested HTTP port is held by a live listener, `gw start` moves both ports up by the same offset, skipping the Gateway's own auxiliary ports (8081, 8082, 5173), and prints the holder and the bound ports (`internal/cli/platform/process.go`). On Linux the availability probe keeps `SO_REUSEADDR`, matching the Gateway's own bind, so TIME_WAIT sockets left by a Gateway that just stopped do not count as busy; on BSD/macOS it disables `SO_REUSEADDR` so a wildcard probe still detects a listener on a specific address (`internal/netutil/port_probe_other.go`). The bound ports are persisted in the launch profile. Every CLI command run without `--endpoint`/`--port` reads them from the launch profile while its Gateway is running (`recordLocalGatewayPorts` in `internal/cli/cmd/main.go`, `config.SetLocalGatewayPorts`). The startup guidance and `gw status` print the Console URL and the Operator `--endpoint <host>:<http> --port <https>` command on the bound ports. The Gateway process itself resolves free ports once at start (`config.ResolveGatewayPorts`, called from `internal/cli/serve/gateway.go`); `config.LoadGateway` only validates the configured ports and never probes the host.
- `ztd gw start` against a running Gateway (background start, without `--follow`): compares the startup flags the caller set explicitly among `--capabilities`, `--posture`, `--listen-host`, `--http-port`, `--https-port`, `--model-storage-root`, `--inference-ollama-endpoint`, `--public-base-url`, `--cors-origin`, `--passkey-rp-id`/`--passkey-rp-name`/`--passkey-rp-origin`, and `--pprof-listen` with the running launch profile (`gateway_start_running.go`); a `ZTD_OLLAMA_ENDPOINT` value (exported or from `.env`) counts as setting `--inference-ollama-endpoint`. Other flags are not compared. It reuses the Gateway when they match and otherwise lists the differences and fails with `ErrGatewayAlreadyRunning`. Without a launch profile, an explicitly set flag from that list fails with `ErrGatewayAlreadyRunning` wrapping `ErrLaunchProfileMissing`. Stop the Gateway (`ztd gw stop` or `make down`) to change them; `.ztd/` state is kept.
- `ztd gw setup`: Launches an interactive terminal wizard producing a resolved platform configuration.
- `ztd gw start -i`: Launches the interactive setup wizard, then boots the Gateway immediately with the confirmed settings.
- `ztd gw restart`: Reads the persisted launch profile from `.ztd/pids/operator-launch-profile.json` and restarts the process. Fails closed if the profile is missing or invalid.
- `ztd gw stop`: Sends a termination signal to the running Gateway process. On SIGINT or SIGTERM the Gateway waits for in-flight transactions to drain and then stops its services (`internal/cli/serve/gateway.go`).
- `ztd gw status`: Emits Gateway state and PID, the Operator inventory with capabilities, a Consensus row (bound consensus, signer readiness, and posture, from the `/api/v1/health` consensus summary), enrollment counts, and the Console URL.

### Governance Postures

The Gateway enforces one of four postures (`constants.Posture`) selected at startup via `--posture` (default `doctrine`):

| Posture | L1 Doctrine | L2 Consensus (`RequiresL2()`) | L3 Notary (`governance.L3Required`) | L3 Notary (Read-Only or `AuthorizedByDecision`) |
| :--- | :--- | :--- | :--- | :--- |
| **`doctrine`** | Enforced | Audited | Audited | Not Required |
| **`consensus`** | Enforced | Enforced (Bound Members) | Audited | Not Required |
| **`ratify`** | Enforced | Audited | Enforced (Human) | Not Required |
| **`notary`** | Enforced | Enforced (Bound Members) | Enforced (Human) | Not Required |

Selecting a posture does not automatically enroll its supporting identities. Under `consensus` and `notary`, startup logs a warning when no ready L2 binding exists, and L2-gated transactions are rejected until one is bound (`internal/cli/serve/gateway.go`). Bind one with `ztd consensus enroll app` (enrolled `PLATFORM_COMPONENT_KIND_APPLICATION` members, each with `ztd auth enroll app <name> --signing-key`) or `ztd consensus enroll tribunal --model <tag>` (the Gateway-hosted Tribunal panel). The binding names its provider as `application` or `tribunal`, and `ztd consensus status` shows it. Under `consensus` and `notary`, L2 votes gate only the actions the registry classifies as state-changing (`governance.L2Enforced`, see [Consensus Architecture](consensus.md)). Human approval under `ratify` and `notary` requires a WebAuthn passkey assertion (or signed CLI approval for outbound Operators).

### HTTP and HTTPS Route Classifications

The Gateway segments traffic across two network listeners:

#### HTTP Surface (Default Port 8080)
The HTTP router (`buildHTTPRouter`) serves unauthenticated discovery endpoints:
- `/.well-known/ztd/pki/ca-bundle` and `/.well-known/ztd/pki/fingerprint`: CA bundle and fingerprint discovery. The CRL endpoint is HTTPS only.
- `/.well-known/ztd/bin/` and the Linux and Windows deploy scripts: Operator binary download and deploy scripts.
- `/api/v1/health`: Bootstrap health check that does not read the state root (`handleBootstrapHealth`). `/api/v1/state`: State root for envelope binding.
- `/api/v1/auth/bootstrap` and `/api/v1/auth/bootstrap/status`: Local bootstrap.
- CLI recovery and platform enrollment `request`, `status`, and `complete` endpoints: Token-scoped discovery for clients without trusted TLS. The approve and owner decision endpoints are HTTPS only.
- `/ws/operator/bootstrap`: Bootstrap WebSocket for Operator enrollment.
- Catch-all: Redirects all other paths to HTTPS on the Gateway's resolved HTTPS port.

#### HTTPS Surface (Default Port 8443)
The HTTPS router (`buildPublicRouter`) manages authenticated platform APIs. The route registry in `gateway_auth.go` classifies routes into four strict policies:

| Auth Policy | Routes Covered | Identity Requirement |
| :--- | :--- | :--- |
| **`RouteAuthNone`** | `/api/v1/health`, `/api/v1/state`, the landing page, PKI discovery (`/.well-known/ztd/pki/*`) and binary discovery (`/.well-known/ztd/bin/*`), Linux and Windows deploy scripts, PKI device enrollment (`/api/v1/pki/devices/enroll`), bootstrap and logout, enrollment token validation, the approve page prefix, CLI recovery and platform enrollment request/status/complete, the Operator bootstrap WebSocket, `/console/*`, and the passkey console and enrollment prefixes | No identity required; token-scoped where noted in `gateway_auth.go` |
| **`RouteAuthMTLS`** | Explicit entries include public feed routes, `/api/v1/pki/csr/sign`-style PKI CSR sign (`/api/v1/pki/csr/sign`), `/api/v1/operators/{commands,intents,validate,target,stop}` and the operator session prefix, observe producer routes, CLI approval `pending` and `status`, CLI passkey status, CLI recovery approve, enrollment token generate, and CLI rotate, refresh, bind, unbind, logout, and session | Valid mTLS client certificate |
| **`RouteAuthWebSession`** | `/api/v1/approvals` prefix (except `respond`, which is `RouteAuthDual`, and the CLI `pending` and `status` paths, which are `RouteAuthMTLS`), `/api/v1/users` prefix, `/api/v1/auth/sessions` prefix, the passkey prefix, the observe prefix, and CLI recovery approve from the browser | Valid WebAuthn session cookie |
| **`RouteAuthDual`** | `/api/v1/sse/stream`, `/api/v1/sse/events`, `/api/v1/operators`, `/api/v1/operators/bind`, `/api/v1/operators/unbind`, the `/api/v1/operators/` detail prefix, the API reference spec (`constants.SwaggerDocPath`), `/api/v1/audit/{events,consensus-votes,summary,verify}`, `/api/v1/approvals/respond`, and `/api/v1/auth/platform-enrollments/{pending,enrolled,decision,decisions,revoke,consensus}` | Either valid mTLS client certificate OR web session cookie |

Paths absent from the registry default to `RouteAuthMTLS`; this default covers, for example, `/api/v1/governance/envelopes` and the pub/sub WebSockets. The registry lists the exact routes and prefixes; `gateway_auth.go` is the authority. When a JWKS verifier is configured, `/mcp`, the `/api/v1/a2a/` prefix, and JIT passkey routes are `RouteAuthNone` and the JWT middleware authenticates them; otherwise JIT passkey routes are `RouteAuthMTLS`.

### Five-Layer Governance Verification Sequence

Every transaction entering `POST /api/v1/governance/envelopes` or arriving via MCP/A2A traverses all five governance layers:

1. **L1 Doctrine (Gateway PDP)**: Static regex screening on protobuf string fields annotated with `(ztd.common.v1).forbidden_patterns`, and MITRE ATT&CK heuristics on commands, arguments, and file edits. Violations reject fail closed with `ErrTxL1ValidationFailed`.
2. **L2 Consensus (Gateway Coordination)**: Under `consensus` and `notary` (`posture.RequiresL2()`), for actions the registry classifies as state-changing (`governance.L2Enforced`: state-changing actions per `ActionType.ChangesState`, excluding bootstrap actions and registered read-only targets), the Gateway sends the full `GovernanceEnvelope` (`application/x-protobuf`) to the bound policy's HTTPS deliberation endpoint (`endpoint_url` of `constants.DocIDConsensusBinding` in `constants.CollectionConsensus`, hosted by the primary member) and receives the envelope with its `L2Metadata` votes (`L2Vote` entries), each signed by a member application's Ed25519 key over `<transaction_hash>|<decision>`. When the binding provider is `tribunal`, the Gateway-hosted Tribunal vote producer supplies the votes instead of the HTTPS endpoint (`INV-GW-GOV-07`).
3. **L3 Notary (Gateway Suspension)**: For actions where `governance.L3Required(actionType, posture)` is true (`actionType.IsMutation() && !actionType.AuthorizedByDecision() && posture.RequiresL3()`), transactions lacking human proof are suspended in SQLite (`SuspendedTransactionStore` via `mcp.GatewayService.SuspendGovernanceEnvelope`). The Gateway issues an approval challenge (`GovernedSuspension` on MCP/A2A, or HTTP `202 Accepted` with `status: "l3_required"` on `PLATFORM_ENROLLMENT_DECIDE` and `PLATFORM_ENROLLMENT_REVOKE`). A WebAuthn passkey assertion or an enrolled CLI P-256 signature produces the proof and resumes execution via `ResumeWithL3Proof`. Governed HTTP dispatch (`POST /api/v1/operators/commands`) takes the same suspension path but keeps the original request open (`DispatchApprovalCoordinator`) up to `mcp.ApprovalRequestTTL`; see [Governance Architecture](governance.md#layer-3-l3-notary-human-authorization).
4. **L4 Warden (Operator PEP)**: Executes pre-dispatch verification in the target Operator runtime: validates nonce replay against SQLite, verifies TTL expiry, recomputes the canonical transaction hash, checks the state Merkle root, and enforces posture-required L2/L3 proofs.
5. **L5 Actuator (Operator PEP)**: The singular execution boundary. Persists an initial `EXECUTING` receipt, appends to the SQLite commitment ledger, rehydrates scrubbed secrets, mints a short-lived capability, dispatches to the handler, dissolves the capability, and signs the final `COMPLETED` or `FAILED` receipt.

### Model Context Protocol (MCP) Endpoint and Native Tools

The Gateway serves an MCP endpoint at `POST /mcp` supporting JSON-RPC 2.0 methods: `initialize`, `ping`, `tools/list`, `tools/call`, `resources/list`, `resources/read`, `resources/templates/list`, `prompts/list`, `prompts/get`, and `a2a/call`. A2A ingress is opt-in: without `--a2a-downstream-url`, `a2a/call` answers method-not-found (`-32601`), `POST /api/v1/a2a/call` is not mounted, and `/api/v1/health` reports `a2a_ingress: false`. With a downstream MCP server configured, `tools/list` returns the native tools followed by the downstream's tools, and a native tool wins a name collision (the downstream entry is dropped).

The native tool inventory is `RegisterNativeTools` in `internal/services/mcp/native_tool_registry.go`; documentation does not duplicate the tool list (INV-MCP-03 in [Developer Guidelines](../devs/devs.md)). Tools span database, filesystem, network, process, system, configuration and cloud, and execution and audit domains. Gateway MCP calls take the embedded Operator's session ID from `EmbeddedSessionResolver`. An `MCP_CALL` dispatched to an outbound Operator runs on that Operator's own host after its L4/L5 and is subject to its witness capability gate (`ErrWitnessCommandNotCapable`) (see [Operator Architecture](operator.md#local-state-native-tools-and-git-file-ledger)).

All tools enforce fail-closed input validation: SQL tools reject multi-statement injection; URLs reject private IP ranges (SSRF prevention); filesystem tools reject directory traversal; `run_shell_command` accepts one executable and ordered argv without a shell; `search_file` and `search_directory` search through the host grep service with sensitive-path refusal; `read_file` shares the same sensitive-path gate.

The `read_field` operation validates the supplied Operator session through the MCP `SessionValidator`, which the Gateway wires to `AuthService.ValidateSession`, before reading the document store. A missing validator or invalid session rejects the read; a session lookup failure returns an internal error.

### Agent Integration and MCP Stdio Bridge

The Gateway provides seamless integration for external AI coding tools (Claude Code, OpenAI Codex, Devin CLI, Gemini CLI, Goose) through `ztd mcp agent`:
- `ztd mcp agent list`: Lists the supported agent IDs and display names.
- `ztd mcp agent show <agent>`: Prints the MCP client configuration for the Gateway.
- `ztd mcp agent run <agent>`: Launches the specified agent with native tools disabled, proxying all tool traffic through `ztd mcp stdio` to the Gateway's authenticated HTTPS endpoint.
- `ztd mcp agent verify <agent>`: Validates agent configuration and tool lockdown without starting the agent.
- When an operation requires L3 approval, the stdio bridge listens for the `approval.completed` event on the Gateway SSE stream.

### Incremental State Tracking and Merkle Roots

The Gateway maintains an incremental 16-ary Merkle commitment tree (algorithm 2) over platform state:
- **Bound State**: Covers authoritative documents, bound KV entries, blobs, and token maps. The Bound State root is embedded into `GovernanceEnvelope.StateMerkleRoot` and gates transaction admission.
- **Observed State**: Covers transient telemetry, observer windows, and heartbeat timestamps. Captured in a separate Merkle root chained into the audit ledger without invalidating in-flight transaction freshness.

### Readiness and Health Endpoints

- **Readiness Check (`HTTPS /api/v1/health`, default port 8443)**: Verifies that the platform settings document is readable and the state Merkle root is calculated. Returns `status`, `mode`, `version`, `pid`, `posture`, `governance_ready`, `a2a_ingress`, `state_merkle_root`, and `consensus`. `governance_ready` reports pipeline initialization without failing the response. `consensus` is a counts-only summary of the L2 binding (`bound`, `provider`, `quorum`, `members`, `usable`, and `ready`) that never carries member IDs, the pinned model, or an endpoint.
- **Bootstrap Check (`HTTP /api/v1/health`, default port 8080)**: Lightweight check that does not read the state root (`handleBootstrapHealth`). It returns `status`, `mode`, `version`, `pid`, `posture`, `governance_ready`, `a2a_ingress`, and `consensus`, and answers `503` until the service is ready.

## Anti-patterns

- **Serving Application Routes over Plaintext HTTP**: Hosting MCP, governance, or pub/sub endpoints on the HTTP listener instead of HTTPS (default ports 8080 and 8443; INV-GW-NET-01).
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

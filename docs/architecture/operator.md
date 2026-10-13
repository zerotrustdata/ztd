---
doc_id: operator
title: Operator Architecture
audience: maintainers and coding agents
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - internal/services/ztdo.go
  - internal/services/operatorcapability/
  - internal/cli/cmd/operator/
  - internal/cli/serve/
  - internal/services/governance/
related:
  - gateway.md
  - governance.md
  - network.md
  - auth.md
  - storage.md
  - agents.md
  - evals.md
when_to_read: Understanding the Governed Operator (PEP) architecture, outbound-only mTLS connections, L4 Warden verification, L5 Actuator execution boundary, multi-operator coexistence, and local-first audit persistence.
do_not_use_for:
  - Gateway PDP coordination and client ingress (see gateway.md)
  - Canonical five-layer governance pipeline mechanics (see governance.md)
  - Storage schemas and migration internals (see storage.md)
---

# Operator Architecture

## Purpose

Defines the architecture of the Governed Operator (`ztd operator`). The Operator is the sovereign Policy Execution Point (PEP) for its target host runtime. Started via `ztd operator start`, the Operator establishes an outbound-only mTLS connection to the Governance Gateway (PDP), receives canonical `GovernanceEnvelope` transactions over dedicated session channels, independently evaluates transactions through the Layer 4 (L4) Warden, executes accepted actions through the Layer 5 (L5) Actuator under single-action just-in-time capabilities, and persists authoritative local execution evidence under the Local-First Audit Architecture (LFAA). Remote Operators expose no inbound listening ports and trust no incoming message without cryptographic verification.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Connection and Network Boundary (`INV-OP-CONN`)](#connection-and-network-boundary-inv-op-conn)
  - [Capabilities and Witness Constraints (`INV-OP-CAP`)](#capabilities-and-witness-constraints-inv-op-cap)
  - [Coexistence and Fingerprinting (`INV-OP-FPR`)](#coexistence-and-fingerprinting-inv-op-fpr)
  - [Readiness (`INV-OP-READY`)](#readiness-inv-op-ready)
  - [Liveness and Staleness Derivation (`INV-OP-STALE`)](#liveness-and-staleness-derivation-inv-op-stale)
  - [L4 Warden Pre-Dispatch Gate (`INV-OP-WARD`)](#l4-warden-pre-dispatch-gate-inv-op-ward)
  - [L5 Actuator Execution Boundary (`INV-OP-ACT`)](#l5-actuator-execution-boundary-inv-op-act)
  - [Local-First Audit Storage (`INV-OP-STOR`)](#local-first-audit-storage-inv-op-stor)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Operator Types and Operating Modes](#operator-types-and-operating-modes)
  - [Capabilities Model and Enforcement Boundaries](#capabilities-model-and-enforcement-boundaries)
  - [Multi-Operator Coexistence and Composite Fingerprinting](#multi-operator-coexistence-and-composite-fingerprinting)
  - [Liveness, Heartbeats, and Staleness Derivation](#liveness-heartbeats-and-staleness-derivation)
  - [WebSocket Bootstrap Enrollment and In-Memory Lifecycle](#websocket-bootstrap-enrollment-and-in-memory-lifecycle)
  - [In-Band Certificate Renewal over mTLS Pub/Sub](#in-band-certificate-renewal-over-mtls-pubsub)
  - [Command Dispatch and Receipt Channels](#command-dispatch-and-receipt-channels)
  - [Five-Layer Execution Boundary (L4 Warden & L5 Actuator)](#five-layer-execution-boundary-l4-warden--l5-actuator)
  - [Local State, Native Tools, and Git File Ledger](#local-state-native-tools-and-git-file-ledger)
  - [Autochthon Subsystem](#autochthon-subsystem)
  - [Evidence, Scope, and Trust Limitations](#evidence-scope-and-trust-limitations)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Connection and Network Boundary (`INV-OP-CONN`)

| ID | Rule |
| :--- | :--- |
| INV-OP-CONN-01 | Outbound Operators MUST establish outbound-only TLS 1.3 mTLS connections to the Gateway and subscribe to session-specific command channels (`cmd:<operator_id>:<operator_session_id>`). Operators MUST NOT open inbound listening ports for management or execution. |
| INV-OP-CONN-02 | The Operator MUST NOT execute commands received over transport without independent cryptographic verification. Delivery from the Gateway does NOT constitute authorization. |

### Capabilities and Witness Constraints (`INV-OP-CAP`)

| ID | Rule |
| :--- | :--- |
| INV-OP-CAP-01 | Operator capabilities (`data`, `inference`, `provenance`, `observer`) MUST be declared at startup and enforced by membership. Witness-only operators (`provenance`, `observer` without `data`) MUST reject arbitrary command execution via `ValidateWitnessCommand`. Adding `data` permits governed commands in the same process. |
| INV-OP-CAP-02 | The Provenance capability MUST require `--model-storage-root` at configuration time and fails closed if missing. The Inference capability MUST resolve its Ollama provider endpoint from `--inference-ollama-endpoint`, else `ZTD_OLLAMA_ENDPOINT`, else `http://127.0.0.1:11434`. |

### Coexistence and Fingerprinting (`INV-OP-FPR`)

| ID | Rule |
| :--- | :--- |
| INV-OP-FPR-01 | Multiple Operators on a single host MUST be disambiguated by a composite SHA-256 `system_fingerprint` combining immutable host properties with `local_dir`, `account`, `port`, and canonical `operator_capabilities`. |
| INV-OP-FPR-02 | Issuing an Operator whose `system_fingerprint` matches an existing non-terminated Operator under the same owner MUST terminate the earlier document and deactivate its session. An identity MUST NOT hold duplicate active leases. |

### Readiness (`INV-OP-READY`)

| ID | Rule |
| :--- | :--- |
| INV-OP-READY-01 | An enrolled remote Operator MUST be stored `offline`. The Gateway moves it to `active` only when the Operator's own authenticated pub/sub connection has its `cmd:<operator_id>:<operator_session_id>` subscription registered on the broker (`DocumentStoreService.OperatorCommandSubscribed`), and announces that single transition as `ztd.v1.operator.status.updated.active`. `active` therefore means ready to receive commands: a dispatch made after the event finds a subscriber. A stopped, terminated, or already active Operator is not changed by subscribing, and a connection whose certificate session differs from the channel's session cannot announce it. |
| INV-OP-READY-02 | After a disconnect or Gateway restart the stored status stays `active` until the Operator re-subscribes or reads as `stale`; a command dispatched in that window fails with 503 (`dispatch: command delivered to no operator subscribers`) and MUST NOT be retried by tests or by the Gateway. |

### Liveness and Staleness Derivation (`INV-OP-STALE`)

| ID | Rule |
| :--- | :--- |
| INV-OP-STALE-01 | Staleness MUST be derived on document read (`deriveOperatorStaleness`) and MUST NOT be persisted to storage. An active remote Operator silent for longer than twice its declared heartbeat interval (minimum 60s, maximum 10m) reads as `status: stale`. |
| INV-OP-STALE-02 | Heartbeats MUST update `last_heartbeat_at` as observed-tier telemetry without altering the bound state root. A heartbeat restores `stale` to `active`; it MUST NOT revive `stopped` or `terminated` Operators. The embedded Operator is exempt from staleness derivation. |

### L4 Warden Pre-Dispatch Gate (`INV-OP-WARD`)

| ID | Rule |
| :--- | :--- |
| INV-OP-WARD-01 | L4 Warden MUST lock nonces in memory (`trackInFlight`) and durably reserve them in SQLite (`ReplayStore`) prior to payload validation. Any failure occurring after reservation MUST release the reservation (`releaseNonceReservation`). |
| INV-OP-WARD-02 | L4 Warden MUST verify protocol version 2, recompute the canonical transaction hash (`GenerateMessageID`), verify hash equality against both `envelope.TransactionHash` and `envelope.Id`, validate state Merkle root, verify target execution identity (`ExecutesFor`), and enforce posture-required L2 consensus and L3 notary proofs. |

### L5 Actuator Execution Boundary (`INV-OP-ACT`)

| ID | Rule |
| :--- | :--- |
| INV-OP-ACT-01 | L5 Actuator MUST persist an initial `EXECUTING` receipt to local SQLite audit storage before invoking any execution handler. Signing or persistence failure MUST abort execution fail closed. |
| INV-OP-ACT-02 | When SQL audit is enabled, L5 Actuator MUST append a signed `CommitmentAttestation` to the SQLite commitment ledger in the same transaction as the `EXECUTING` receipt. |
| INV-OP-ACT-03 | L5 Actuator MUST mint a single-action capability bound to the transaction hash, action type, target resource, operator ID, and session before execution, and MUST dissolve the capability immediately upon handler return. |
| INV-OP-ACT-04 | L5 Actuator MUST finalize the receipt with status `COMPLETED` or `FAILED`, capture the local state root after execution, sign the receipt, and attach a signed `ReceiptPersistenceAttestation`. |

### Local-First Audit Storage (`INV-OP-STOR`)

| ID | Rule |
| :--- | :--- |
| INV-OP-STOR-01 | Under LFAA, the Operator's local SQLite database (`.ztd/data/ztd.db`) and Git file ledger (`.ztd/data/ledger/`) are the authoritative execution records. Gateway receipt mirrors are best-effort projections. |
| INV-OP-STOR-02 | Operator private keys, CSRs, requester tokens, and issued certificates MUST be held in memory only during remote runtime execution; they MUST NOT be written to plaintext files in `.ztd/`. |
| INV-OP-STOR-03 | An outbound Operator's runtime tree is `--working-dir/.ztd/`, or the launch directory's `.ztd/` when the flag is omitted. `operator start` MUST refuse a runtime tree that holds a Gateway root CA (`GatewayRuntimeMarker`, `pki/root/root_ca.crt`), so an Operator never opens a Gateway's audit store or vault. |
| INV-OP-STOR-04 | An Operator's runtime tree (`.ztd/`) MUST NOT reside inside the model storage root (`--model-storage-root`). `operator start` MUST refuse startup with `ErrOperatorModelStoreRuntime` if the resolved runtime tree is inside or equal to the model storage root, preserving the model store as a pure read-only witness and weight cache. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Operator CLI commands and process lifecycle | `internal/cli/cmd/operator/`, `internal/cli/serve/` | `make build && ./ztd operator --help` |
| Operator service implementation | `internal/services/ztdo.go` (`services.ZtdoService`) | `internal/services/ztdo_test.go`, `internal/services/ztdo_integration_test.go` |
| L4 Warden pre-dispatch pipeline | `internal/services/governance/l4_warden.go` | `internal/services/governance/l4_warden_test.go` |
| L5 Actuator execution boundary | `internal/services/governance/l5_actuator.go` | `internal/services/governance/l5_actuator_test.go` |
| Capability set and witness validation | `internal/constants/operator_capabilities.go`, `internal/services/operatorcapability/` | `internal/constants/operator_capabilities_test.go` |
| Operator fingerprint calculation | `internal/cli/serve/operator.go` | `operatorFingerprintOptions` unit tests |
| Certificate renewal service | `internal/cli/serve/operator_cert_renewal.go` | Integration tests in `internal/cli/serve/` |
| Git-backed file ledger | `internal/services/storage/ledger.go` | `internal/services/storage/ledger_test.go` |
| Autochthon autonomous service | `internal/services/autochthon/` | `internal/services/autochthon/autochthon_test.go` |

## Procedures

### Operator Types and Operating Modes

The platform supports two distinct Operator deployments:

1. **Remote Governed Operator (`operator_type: remote`)**: An outbound daemon started via `ztd operator start`. It enrolls with the Gateway over WebSocket, runs with dedicated local state in `.ztd/`, maintains an in-memory mTLS identity, and executes tasks on target servers, virtual machines, or developer workstations.
2. **Embedded Operator (`operator_type: embedded`)**: An in-process substrate running inside the Gateway daemon (`ztd gw start`). It always carries the `data` capability (`WithData()`), shares the Gateway's local runtime tree, and executes tasks targeting the Gateway host itself.

### Capabilities Model and Enforcement Boundaries

Each Operator process declares its functional roles through startup flags:

```bash
# Data Operator (default: general command and tool execution)
./ztd operator start --endpoint gateway.example --capabilities data

# Inference Operator (Ollama model serving backend)
./ztd operator start --endpoint gateway.example --capabilities inference \
  --inference-ollama-endpoint http://127.0.0.1:11434 \
  --inference-keep-alive -1

# Provenance Operator (storage-side model weight attestation witness)
./ztd operator start --endpoint gateway.example --capabilities provenance \
  --model-storage-root /srv/ollama/models \
  --provenance-operator-id prov-witness-01

# Observer Operator (host hardware telemetry sampling witness)
./ztd operator start --endpoint gateway.example --capabilities observer \
  --provider-boundary-observer-id obs-telemetry-01

# Multi-capability Operator (combined execution and witness)
./ztd operator start --endpoint gateway.example --capabilities data,inference,provenance \
  --model-storage-root /srv/ollama/models \
  --inference-keep-alive 5m \
  --provenance-operator-id prov-witness-01
```

Witness constraints:
- Processes carrying only `provenance` or `observer` capabilities are witnesses. They reject arbitrary command dispatch via `ValidateWitnessCommand`.
- Adding `data` permits governed command execution in the same process.
- Streamed inference progress is scrubbed at the source before publication. Streams are buffered line-by-line and scrubbed against regex patterns. Private keys are buffered until completion. The terminal `output_hash` binds the concatenated scrubbed progress chunks.

Bootstrap sends the active startup settings in `OperatorRuntimeConfig`, persisted on `OperatorDocument.runtime_config`. Along with endpoint, storage root, platform, and heartbeat settings, this contract records the inference keep-alive duration and stable provenance and provider-boundary observer pseudonyms. See the [generated Operator API reference](../../protocol/docs/reference/api/ztd/operator/v1/index.md#ztd-operator-v1-OperatorRuntimeConfig) for the canonical wire fields.

### Multi-Operator Coexistence and Composite Fingerprinting

Multiple Operator instances can execute concurrently on a single host. Operators are disambiguated by four factors:
1. `local_dir`: The runtime working directory containing sovereign `.ztd/` state.
2. `account`: The operating system user running the process.
3. `port`: The Gateway target port dialed by the instance.
4. `operator_capabilities`: The canonical set of enabled capabilities.

The canonical `system_fingerprint` is a SHA-256 hash combining immutable host attributes (`os`, `arch`, `cpu_count`, `machine_id`, `hostname`) with `local_dir`, `account`, `port`, and `capabilities`. Re-enrolling an existing fingerprint deactivates the prior session and replaces the document atomically.

### Liveness, Heartbeats, and Staleness Derivation

- **Heartbeat Transmission**: Outbound Operators publish heartbeat frames every 30 seconds (configurable via `--heartbeat-interval 0-300`) to the Gateway pub/sub broker. Heartbeats are observed-tier telemetry that do not alter the Bound State Merkle root.
- **Staleness Window**: Twice the declared interval (`constants.OperatorHeartbeatStaleMultiplier`), with a floor of 60 seconds and a ceiling of 10 minutes.
- **On-Read Derivation**: Staleness is computed dynamically during `DocGet`, `DocList`, or `DocQuery` calls via `deriveOperatorStaleness`. Silent Operators return `status: stale` without modifying persisted database rows.
- **Recovery**: Receiving a fresh heartbeat immediately restores derived status to `active`. Heartbeats cannot revive `stopped` or `terminated` Operators.

### WebSocket Bootstrap Enrollment and In-Memory Lifecycle

`ztd operator start` executes a zero-disk credential enrollment:
1. Generates Operator and CLI ECDSA P-256 key pairs, certificate signing requests (CSRs), and a requester token in process memory.
2. Connects to the Gateway bootstrap WebSocket at `/ws/operator/bootstrap`: `ws://` on HTTP port 8080, or `wss://` on HTTPS port 8443 when `--trust-bundle` pins the Gateway CA.
3. Sends a `request` frame with both CSRs, token hash, system fingerprint, and runtime configuration.
4. On the Gateway's `created` frame, sends a `complete` frame carrying both keys' signatures over the completion transcript (request ID, token hash, instance ID, CSR fingerprints) as proof of possession.
5. Awaits owner approval. Upon approval, the Gateway sends a `decision` frame, issues the X.509 certificates, and replies with a `bundle` frame containing certificates, SPIFFE identity, CA roots, and L2 consensus signer public keys.
6. The Operator validates the bundle and holds certificates in memory. No private keys or certificates are written to disk.

### In-Band Certificate Renewal over mTLS Pub/Sub

Workload leaf certificates are valid for 7 days. Two-thirds through the validity period, `OperatorCertRenewer` initiates in-band renewal:
1. Subscribes to `renewal:<operator_id>:<operator_session_id>` over the existing mTLS WebSocket connection.
2. Generates a new P-256 key and CSR in memory and publishes a `request` frame.
3. The Gateway validates connection identity, verifies the active session, issues a renewed leaf certificate, and updates L2 signer trust.
4. The Operator swaps the in-memory certificate for future handshakes without interrupting established connections or writing to disk.

### Command Dispatch and Receipt Channels

Communication between the Gateway and Operators uses dedicated, isolated WebSocket pub/sub channels:
- **Command Ingress**: `cmd:<operator_id>:<operator_session_id>`: Receives canonical `GovernanceEnvelope` transactions dispatched from the Gateway.
- **Receipt Egress**: `receipts:<operator_id>:<operator_session_id>`: Publishes signed `ActionReceipt` objects back to the Gateway.
- **Heartbeat Channel**: Publishes periodic telemetry frames containing process metrics and timestamp proofs.

The native `sys_info` tool also reports host OS details. On Unix-like systems it reads the distribution name from `/etc/os-release` and kernel, uptime, and load averages from the host interfaces where available. On Windows it reads the product name and build from the registry (correcting Windows 10 labels for build 22000 and later), reports kernel version and uptime, and returns `unknown` for load average because Windows does not expose that metric through this collector. Unavailable or unreadable values are returned as `unknown`.

### Five-Layer Execution Boundary (L4 Warden & L5 Actuator)

The Operator owns the final two layers of the governance interlock:

#### L4 Warden Pre-Dispatch Pipeline
1. **In-Flight Lock**: Registers the transaction nonce in memory (`trackInFlight`) to eliminate race conditions.
2. **Durable Replay Reservation**: Confirms TTL expiration (`ExpiresAt`) and reserves the nonce in SQLite (`ReplayStore.ReserveNonce`).
3. **Stateless Verification**: Decodes the protobuf payload, validates target ownership (`ExecutesFor`), re-runs local L1 Doctrine checks, recomputes the SHA-256 transaction hash, and verifies dual equality (`transaction_hash == envelope.id == recomputed_hash`).
4. **State Merkle Root Verification**: Validates `envelope.StateMerkleRoot` against the admission snapshot in context.
5. **Posture Validation**: Evaluates posture-required L2 Consensus signatures and L3 Notary proofs from `envelope.Posture`. If validation fails, the reserved nonce is released immediately.

#### L5 Actuator Execution Boundary
1. **Initial Receipt Logging**: Persists an `EXECUTING` status `ActionReceipt` signed by the Operator's Actuator key.
2. **Commitment Append**: Appends a signed `CommitmentAttestation` to the hash-chained SQLite commitment ledger under write lock.
3. **Secret Rehydration**: Rehydrates scrubbed tokens in memory via local vault keys.
4. **JIT Capability Minting**: Mints a single-action capability bound to the transaction hash and attaches it to the execution context.
5. **Handler Dispatch**: Executes the typed action handler.
6. **Capability Dissolution**: Dissolves the capability immediately upon return.
7. **Final Attestation**: Captures the final state root, marks the receipt `COMPLETED` or `FAILED`, signs the receipt, and attaches a signed `ReceiptPersistenceAttestation`.
8. **Asynchronous Gateway Relay**: Mirrors the receipt to the Gateway `receipts:` channel on a best-effort basis.

### Local State, Native Tools, and Git File Ledger

- **State Directory (`.ztd/`)**: Contains SQLite database `data/ztd.db` (audit records, nonces, suspended transactions), encrypted vault storage (`vault/`), and Git file ledger (`data/ledger/`).
- **Git File Ledger**: Every governed `FILE_EDIT` or `RESTORE_FILE` action creates an encrypted snapshot in Git before and after modification.
- **File Confinement**: Governed file operations cannot touch `.ztd/` runtime state except the controlled evaluation directory `.ztd/eval/targets/`. All I/O routes through `RuntimeFileService.OpenFileEditTarget`.
- **Environment Scrubbing**: Command subprocesses never inherit the registry `secret` keys (`constants.SecretEnvVars`, from `protocol/constants/env_vars.json`), including keys loaded from `.env`. All other variables, including non-secret registry keys such as `ZTD_OLLAMA_ENDPOINT`, are inherited unchanged.
- **Model Store Isolation**: An Operator's runtime state (`.ztd/`) must never reside inside the model storage root (`--model-storage-root`). `operator start` validates this pre-flight via `refuseModelStoreRuntime` and fails closed with `ErrOperatorModelStoreRuntime` (INV-OP-STOR-04), preserving the provenance model store as a pure read-only witness.

### Autochthon Subsystem

The `ztd autochthon` command is an isolated, autonomous runtime observer and forecaster operating without Gateway dependencies:
- **Operation**: Samples system load, memory, and process metrics once per second. Trains a fixed-topology neural network predicting the next sample relative to a persistence baseline.
- **State Storage**: Persisted under `.ztd/autochthon/` (`run.lock`, `culture.bin`, and hash-chained `tally.jsonl`).
- **Independence**: Accepts no governance envelopes, opens no network listeners, and produces no `ActionReceipt` records. `ztd autochthon status` verifies the tally hash chain from genesis without acquiring locks.

### Evidence, Scope, and Trust Limitations

- **Authoritative Record**: The local SQLite database and Git ledger on the executing host are the sole authoritative evidence of execution.
- **Isolation Scope**: The Operator governs only actions that traverse ztd ingress routes (MCP, A2A, HTTP dispatch, direct envelope). It does not sandbox the operating system or restrict ungoverned host processes.
- **Specification Parity**: Third-party implementations claiming protocol compatibility must reproduce canonical protojson parsing, hash calculation, replay tracking, L4/L5 verification, and signed receipt persistence.

## Anti-patterns

- **Opening Inbound Management Listeners on Operators**: Exposing inbound ports instead of maintaining outbound-only mTLS connections (INV-OP-CONN-01).
- **Executing Unverified Commands**: Treating Gateway delivery as proof of authorization without running the L4 Warden (INV-OP-CONN-02).
- **Dispatching Commands to Witness Operators**: Sending mutating commands to Provenance or Observer operators lacking the `data` capability (INV-OP-CAP-01).
- **Persisting Private Keys to Disk**: Writing Operator private keys or enrollment certificates to unencrypted files in `.ztd/` (INV-OP-STOR-02).
- **Dangling Replay Nonce Reservations**: Failing to release nonces when L4 verification fails (INV-OP-WARD-01).
- **Executing Actions Before Initial Receipt Persistence**: Invoking execution handlers before the initial `EXECUTING` receipt and commitment attestation are persisted (INV-OP-ACT-01).
- **Relying on Gateway Receipt Mirrors as Sovereign Truth**: Treating Gateway-replicated receipts as primary evidence instead of querying the Operator's local SQLite database (INV-OP-STOR-01).

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Gateway Architecture](gateway.md): Gateway Policy Decision Point responsibilities and route handling.
- [Governance Architecture](governance.md): Five-layer verification pipeline and posture enforcement rules.
- [Network Architecture](network.md): PKI hierarchy, TLS 1.3/mTLS transport, SPIFFE identity, and port topology.
- [Authentication Architecture](auth.md): Enrollment workflows, passkey authentication, and session models.
- [Storage Architecture](storage.md): SQLite schema layouts, commitment ledgers, and document stores.
- [AI Agents and Governance Boundary](agents.md): Agent integration patterns and tool interception.
- [Evaluations](evals.md): Evaluation suites, campaign orchestration, and witness verification.
- [Model Provenance](model-provenance.md): Model weight attestation and supply-chain verification.
- [Run an Autochthon](../guides/autochthon.md): Guide to running, reading, and recovering Autochthon roots.

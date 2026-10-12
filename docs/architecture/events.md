---
doc_id: events
title: Event and Action Protocol
audience: maintainers and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - protocol/constants/events.json
  - internal/services/gateway/
  - internal/tools/constgen/
related:
  - gateway.md
  - operator.md
  - governance.md
  - storage.md
when_to_read: Designing, integrating, or auditing event flow across the Gateway, Operators, observe producers, and the console; understanding governance envelopes and audit trails.
do_not_use_for:
  - Gateway API endpoints (see gateway.md)
  - Operator command flow (see operator.md)
  - Storage architecture (see storage.md)
---

# Event and Action Protocol

## Purpose

Defines the event taxonomy, action classification, transport boundaries, and persistence contracts governing protocol events across the platform. The canonical registry `protocol/constants/events.json` establishes a closed, typed vocabulary for all named messages traversing Server-Sent Events (SSE), WebSocket pub/sub, and governed execution transports. Generated Go and TypeScript constants (`events_gen.go`, `events.ts`, `action_types_gen.go`) derive strictly from this source of truth; hand-editing generated constants is prohibited.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Registry Authority and Generation (`INV-EVT-REG`)](#registry-authority-and-generation-inv-evt-reg)
  - [Event Kinds and Taxonomy (`INV-EVT-KIND`)](#event-kinds-and-taxonomy-inv-evt-kind)
  - [Governance Envelopes and Actions (`INV-EVT-GOV`)](#governance-envelopes-and-actions-inv-evt-gov)
  - [Transport Boundaries (`INV-EVT-XPORT`)](#transport-boundaries-inv-evt-xport)
  - [Storage and Persistence Ownership (`INV-EVT-PERS`)](#storage-and-persistence-ownership-inv-evt-pers)
  - [Hash-Chained Audit Trails (`INV-EVT-CHAIN`)](#hash-chained-audit-trails-inv-evt-chain)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Event Registry Architecture and Entry Structure](#event-registry-architecture-and-entry-structure)
  - [Domain Categorization](#domain-categorization)
  - [Event Kinds and Operational Semantics](#event-kinds-and-operational-semantics)
  - [Governance Envelopes and Action Type Derivation](#governance-envelopes-and-action-type-derivation)
  - [Transport Ownership and Channel Routing](#transport-ownership-and-channel-routing)
  - [Storage Ownership Matrix](#storage-ownership-matrix)
  - [Hash-Chained Audit Ledger Mechanics](#hash-chained-audit-ledger-mechanics)
  - [Wire Value Grammar and Terminal Validation](#wire-value-grammar-and-terminal-validation)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Registry Authority and Generation (`INV-EVT-REG`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-REG-01 | `protocol/constants/events.json` is the sole authoritative registry for protocol events. All Go and TypeScript mirrors (`internal/constants/events_gen.go`, `action_types_gen.go`, `console/src/generated/events.ts`) MUST be generated via `make constants-generate` and validated via `make constants-check`. Hand-editing generated outputs is prohibited. |
| INV-EVT-REG-02 | The code generator in `internal/tools/constgen/` MUST validate all entries against `protocol/models/event_registry.schema.json` and reject wire values whose terminal segment violates the closed terminal grammar. |

### Event Kinds and Taxonomy (`INV-EVT-KIND`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-KIND-01 | Every event MUST declare exactly one of four canonical kinds: `request` (wire value ending in `.requested`), `outcome` (reporting result or progress), `fact` (recording an independent occurrence), or `stream` (carrying ephemeral UI fragments). |
| INV-EVT-KIND-02 | Events with kind `stream` MUST use transport `sse` only and MUST specify persistence `ephemeral`. Stream events MUST NOT be persisted to durable audit ledgers. |

### Governance Envelopes and Actions (`INV-EVT-GOV`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-GOV-01 | A request event becomes a governed transaction if and only if its registry entry contains a `governance` block. Governed requests MUST declare `action_type` and a typed protobuf `payload` schema, and MUST be wrapped in a canonical `GovernanceEnvelope`. |
| INV-EVT-GOV-02 | The Gateway PDP MUST derive `action_type` directly from the event registry during envelope construction. The runtime MUST NOT permit client-supplied overrides of derived action classifications. |

### Transport Boundaries (`INV-EVT-XPORT`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-XPORT-01 | Transport channels MUST be restricted to the declared `transport` array in each event's registry entry (`governed`, `pubsub`, or `sse`). Publishing an event over an unapproved transport MUST be rejected. |
| INV-EVT-XPORT-02 | The `governed` transport is restricted to request events carrying a valid `GovernanceEnvelope`. Governed requests MUST traverse the five-layer pipeline (L1–L5) before dispatch. |

### Storage and Persistence Ownership (`INV-EVT-PERS`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-PERS-01 | Every event MUST declare persistence ownership from the closed vocabulary: `operator.audit_log`, `gateway.audit_log`, `gateway.sse_store`, `gateway.operator_docs`, `gateway.docstore`, or `ephemeral`. Parallel persistence schemes outside this vocabulary are prohibited. |
| INV-EVT-PERS-02 | Server-Sent Events (SSE) is transient delivery telemetry. SSE delivery MUST NOT be treated as durable governance state or authoritative execution evidence. Authoritative execution evidence resides exclusively in the executing Operator's local SQLite database under LFAA. |

### Hash-Chained Audit Trails (`INV-EVT-CHAIN`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-CHAIN-01 | Events persisted to `operator.audit_log` or `gateway.audit_log` MUST be appended to a cryptographic hash chain recording: monotonic sequence number, prior entry hash, keyed content digest, and computed entry hash. |
| INV-EVT-CHAIN-02 | Pruning an audit ledger MUST require a signed checkpoint entry recording the pruned range and the terminal entry hash to guarantee chain verifiability. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Event registry definition | `protocol/constants/events.json` | `make constants-check` |
| Event schema definition | `protocol/models/event_registry.schema.json` | Schema validation in `constgen` |
| Code generator tool | `internal/tools/constgen/` | `go run ./internal/tools/constgen` |
| Generated Go event constants | `internal/constants/events_gen.go` | `make constants-check` |
| Generated Go action type constants | `internal/constants/action_types_gen.go` | `make constants-check` |
| Generated TypeScript constants | `console/src/generated/events.ts` | `make constants-check` |
| Canonical protobuf envelope | `protocol/proto/ztd/common/v1/common.proto` | `make proto-generate` |

## Procedures

### Event Registry Architecture and Entry Structure

Every event in the platform is defined as a typed JSON object in `protocol/constants/events.json`:

```json
"EventName": {
  "_go_const": "EventName",
  "value": "ztd.v1.<domain>.<entity>[.<qualifier>...].<terminal>",
  "kind": "request|outcome|fact|stream",
  "transport": ["governed", "pubsub", "sse"],
  "producers": ["gateway", "operator", "cli", "mcp"],
  "persistence": "operator.audit_log|gateway.audit_log|gateway.sse_store|gateway.operator_docs|gateway.docstore|ephemeral",
  "governance": {
    "action_type": "<action_type_string>",
    "payload": "<protobuf_message_name>"
  },
  "outcomes": ["OutcomeEventName"],
  "reserved": false
}
```

- **Producers**: Restricted strictly to `gateway`, `operator`, `cli`, and `mcp`. `constgen` rejects unknown producer entities.
- **Transports**: Multi-value array declaring which message buses may route the event.
- **Persistence**: Explicit location where the message is durably recorded or marked ephemeral.

### Domain Categorization

Event wire values follow the pattern `ztd.v1.<domain>.<entity>[.<qualifier>...].<terminal>`. Active domains include:

| Domain | Scope and Responsibilities |
| :--- | :--- |
| **`operator`** | Operator lifecycle, session binding, heartbeats, command dispatch, and local execution state. |
| **`ai`** | AI evaluation telemetry (`ztd.v1.ai.eval.*`), formation benchmarks, and model execution metrics. |
| **`app`** | Application lifecycle, workload status, and observe producer telemetry. |
| **`platform`** | Core infrastructure: authentication, PKI issuance, notifications, and governance interlock events. |
| **`public`** | Public spectator feed events, sanitized evaluation results, and public verifiable proofs. |
| **`inference`** | Model provenance preflight updates and provider-boundary observation windows. |

### Event Kinds and Operational Semantics

| Kind | Wire Terminal | Persistence Target | Transports | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| **`request`** | `.requested` | `ephemeral` | `governed`, `sse` | Initiates an operation. If a `governance` block is present, it MUST be wrapped in a `GovernanceEnvelope`. |
| **`outcome`** | e.g. `.completed`, `.failed` | `gateway.sse_store`, `operator.audit_log`, `ephemeral` | `sse`, `pubsub` | Reports completion status, failure details, or incremental progress for a prior request. |
| **`fact`** | e.g. `.created`, `.updated` | `operator.audit_log`, `gateway.audit_log`, `ephemeral` | `sse`, `pubsub` | Records an autonomous occurrence independent of a direct client request. |
| **`stream`** | e.g. `.delta`, `.chunk` | `ephemeral` | `sse` only | Transports ephemeral UI streaming fragments (token deltas, model thinking state). Never persisted. |

### Governance Envelopes and Action Type Derivation

Not all request events are governed. A request event becomes governed when its registry entry defines a `governance` block:
- **UI and Session Requests**: Delivered over SSE or internal request buses without governance wrapping.
- **Mutating Requests**: Wrapped in a canonical `GovernanceEnvelope` (`protocol/proto/ztd/common/v1/common.proto`).

The Gateway PDP inspects the incoming `event_type`, queries the registry, extracts the mapped `action_type`, and determines required L1–L5 governance gates. The typed protobuf message specified in `governance.payload` is decoded and verified against L1 Doctrine before dispatch.

### Transport Ownership and Channel Routing

The registry enforces strict transport routing rules:
- **`sse`**: Client delivery channel for live browser UI updates and approval challenges (`/api/v1/sse/stream`). Never serves as a persistent audit ledger.
- **`governed`**: Dedicated channel for transactions requiring the five-layer pipeline. Requests are packaged into `GovernanceEnvelope` structures and published to session-bound command channels (`cmd:<operator_id>:<operator_session_id>`).
- **`pubsub`**: WebSocket messaging layer for runtime coordination between Gateway and Operators (e.g. heartbeat delivery, certificate renewal, and receipt relays).

### Storage Ownership Matrix

Persistence ownership is assigned per event in the registry:
- **`operator.audit_log`**: Append-only local SQLite audit store on the host where the Operator executes. Under LFAA, this is the authoritative execution record.
- **`gateway.audit_log`**: Centralized Gateway audit store for control-plane decisions and ingress events.
- **`gateway.sse_store`**: Ring buffer powering historical SSE replay queries (`GET /api/v1/sse/events`).
- **`gateway.operator_docs`**: Gateway document collection tracking registered Operators and runtime configurations.
- **`gateway.docstore`**: Authoritative document store for platform users, policies, and signers.
- **`ephemeral`**: In-memory, non-persisted events for high-frequency or streaming data.

### Hash-Chained Audit Ledger Mechanics

Events persisted to `operator.audit_log` form an append-only cryptographic hash chain. Each entry records:
1. `sequence_number`: Monotonically increasing 64-bit integer scoped to the host database.
2. `previous_hash`: SHA-256 digest of the immediately preceding chain entry.
3. `content_digest`: HMAC-SHA256 of the event payload under a subkey derived from the runtime vault (see [Storage Architecture](./storage.md)).
4. `entry_hash`: SHA-256 digest over `<sequence_number>|<previous_hash>|<content_digest>`.

Receipts are appended as audit events; the SQLite `receipts` table provides an indexed latest-stage query view over this ledger. Pruning requires a signed checkpoint record committing to the terminal hash.

### Wire Value Grammar and Terminal Validation

Event wire values follow `ztd.v1.<domain>.<entity>[.<qualifier>...].<terminal>`.
- **Closed Terminal Set**: Validated strictly by `constgen`. Permitted terminals include: `requested`, `started`, `received`, `completed`, `failed`, `cancelled`, `timeout`, `recorded`, `created`, `updated`, `deleted`, `granted`, `rejected`, `denied`, `revoked`, `expired`, `acknowledged`, `sent`, `missed`, `bound`, `unbound`, `opened`, `closed`, `established`, `checkpointed`, `exported`, `published`, `rotated`, `available`, `invoked`, `reached`, `detected`, `resolved`, `appended`, `truncated`, `retry`, `heartbeat`, `active`, `ai`, `answered`, `append`, `assigned`, `authenticated`, `blocked`, `changed`, `cleared`, `complete`, `configured`, `confirmed`, `disabled`, `end`, `error`, `escalated`, `feedback`, `lettered`, `loaded`, `logged`, `occurred`, `offline`, `open`, `preparing`, `questions`, `queued`, `registered`, `replayed`, `reported`, `running`, `selected`, `skipped`, `stale`, `stopped`, `submitted`, `succeeded`, `switched`, `system`, `terminated`, `tier1`, `tier2`, `tier3`, `unauthenticated`, `unavailable`, `update`, `user`, `verified`.
- **Exempt Forms**:
  - `ztd.v1.<domain>.<terminal>` for entity-less facts (e.g. `operator.bound`, `operator.unbound`).
  - `*.status.updated.<state>` for operator and investigation status updates.
  - Stream fragments containing `.stream.`, `.chunk.`, `.delta.`, `.keepalive.`, or `.thinking.`.
- **Prohibited Terminals**: `event`, `execution`, `result`, `info`, and `notification` are explicitly forbidden. Conforming wire values use `updated`, `started`, `completed`, or `sent` instead.

## Anti-patterns

- **Hand-Editing Generated Constants**: Modifying `events_gen.go` or `events.ts` manually instead of editing `protocol/constants/events.json` and running `make constants-generate` (INV-EVT-REG-01).
- **Treating SSE as Durable Governance State**: Relying on SSE event delivery as authoritative execution proof instead of querying the local SQLite audit ledger under LFAA (INV-EVT-PERS-02).
- **Publishing Events Over Undeclared Transports**: Sending events across channels not explicitly permitted in the registry's `transport` array (INV-EVT-XPORT-01).
- **Client-Supplied Action Types**: Permitting clients to specify `action_type` directly instead of letting the Gateway derive it from the event registry (INV-EVT-GOV-02).
- **Using Prohibited Terminals**: Introducing wire values ending in generic nouns (`.event`, `.info`, `.result`) rejected by `constgen`.

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Gateway Architecture](gateway.md): Gateway Policy Decision Point responsibilities and SSE streaming.
- [Operator Architecture](operator.md): Operator Policy Execution Point, command channels, and audit persistence.
- [Governance Architecture](governance.md): Five-layer governance interlock, envelope hashing, and receipt persistence.
- [Storage Architecture](storage.md): SQLite schema layouts, hash-chained ledgers, and document stores.

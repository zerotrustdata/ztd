---
doc_id: events
title: Event and Action Protocol
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
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

Defines the event taxonomy, action classification, transport boundaries, and persistence contracts governing protocol events across the platform. The canonical registry `protocol/constants/events.json` establishes a closed, typed vocabulary for all named messages traversing Server-Sent Events (SSE), WebSocket pub/sub, and governed execution transports. Generated Go and TypeScript constants (`internal/constants/events_gen.go`, `internal/constants/action_types_gen.go`, `console/src/generated/events.ts`) derive strictly from this source of truth; hand-editing generated constants is prohibited.

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
| INV-EVT-REG-02 | The code generator in `internal/tools/constgen/` MUST validate every entry before generating output. It MUST reject entries missing required fields (`_go_const`, `value`, `producers`, `persistence`); unknown `kind`, `transport`, `producers`, and `persistence` values; `governance` on a non-request event; a `governance` block without the `governed` transport; an `action_type` absent from `protocol/constants/status.json`; a non-governed `request` event whose wire value does not end in `.requested` (`constants.ErrEventRequestWireValue`); stream events whose `transport` is absent or contains anything other than `sse` (`constants.ErrEventStreamTransport`); stream events without `ephemeral` persistence; outcome references to missing events; and wire values whose terminal segment violates the closed terminal grammar; and any key outside the declared shape at entry level or inside `governance` (decode-time, `constants.ErrEventRegistryUnknownField`). Rule failures wrap their sentinel with `%w` and are returned together as one aggregate that supports `errors.Is`. `protocol/models/event_registry.schema.json` is the declared shape: constgen enforces it in Go (no JSON-schema library is a dependency and the generator does not load the schema), and `TestEventRegistrySchema_PinsConstgenChecks` pins the schema's enums, required fields and patterns to constgen's checks. `transport` is not required for non-stream events. |

### Event Kinds and Taxonomy (`INV-EVT-KIND`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-KIND-01 | Every event MUST declare exactly one of four canonical kinds: `request` (wire value ending in `.requested`), `outcome` (reporting result or progress), `fact` (recording an independent occurrence), or `stream` (carrying ephemeral UI fragments). |
| INV-EVT-KIND-02 | Events with kind `stream` MUST declare transport `sse` only (an absent `transport` is rejected by constgen, INV-EVT-REG-02) and MUST specify persistence `ephemeral`. Stream events MUST NOT be persisted to durable audit ledgers. |

### Governance Envelopes and Actions (`INV-EVT-GOV`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-GOV-01 | A request event becomes a governed transaction if and only if its registry entry contains a `governance` block. Governed requests MUST declare `action_type` and a typed protobuf `payload` schema, and MUST be wrapped in a canonical `GovernanceEnvelope`. |
| INV-EVT-GOV-02 | The Gateway PDP MUST derive `action_type` directly from the event registry during envelope construction. The runtime MUST NOT permit client-supplied overrides of derived action classifications. |

### Transport Boundaries (`INV-EVT-XPORT`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-XPORT-01 | A request routed over the `governed` transport MUST be rejected unless its registry entry declares `governed` in the `transport` array (`constants.ValidateGovernedRequest`). The runtime does not check `pubsub` or `sse` declarations at publish time. |
| INV-EVT-XPORT-02 | The `governed` transport is restricted to request events carrying a valid `GovernanceEnvelope`. Governed requests MUST traverse the five-layer pipeline (L1–L5) before dispatch. |

### Storage and Persistence Ownership (`INV-EVT-PERS`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-PERS-01 | Every event MUST declare persistence ownership from the closed vocabulary: `operator.audit_log`, `gateway.audit_log`, `gateway.sse_store`, `gateway.operator_docs`, `gateway.docstore`, or `ephemeral`. Parallel persistence schemes outside this vocabulary are prohibited. |
| INV-EVT-PERS-02 | Server-Sent Events (SSE) is transient delivery telemetry. SSE delivery MUST NOT be treated as durable governance state or authoritative execution evidence. Authoritative execution evidence resides exclusively in the executing Operator's local SQLite database under LFEA. |

### Hash-Chained Audit Trails (`INV-EVT-CHAIN`)

| ID | Rule |
| :--- | :--- |
| INV-EVT-CHAIN-01 | Every event appended to the Local-First Events Store `events` table MUST be appended to a cryptographic hash chain recording: monotonic sequence number (`seq`), previous hash (`prev_hash`), keyed content digest (`content_digest`), and entry hash (`hash`). |
| INV-EVT-CHAIN-02 | Pruning chained audit events MUST append a checkpoint fact recording `pruned_through_seq`, `pruned_through_hash`, and `pruned_count` before deleting rows, to preserve chain verifiability. The checkpoint is hash-chained; the runtime does not sign it. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Event registry definition | `protocol/constants/events.json` | `make constants-check` |
| Event entry shape reference | `protocol/models/event_registry.schema.json` | Enforced in Go by `constgen`; pinned by `TestEventRegistrySchema_PinsConstgenChecks` |
| Code generator tool | `internal/tools/constgen/` | `go run ./internal/tools/constgen -check` |
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
- **Transports**: Optional array declaring the message buses that may route the event. An entry without `transport` declares none. The runtime enforces only the `governed` transport (see [Transport Ownership and Channel Routing](#transport-ownership-and-channel-routing)).
- **Persistence**: Explicit location where the message is durably recorded or marked ephemeral.

### Domain Categorization

Event wire values follow the pattern `ztd.v1.<domain>.<entity>[.<qualifier>...].<terminal>`. Active domains include:

| Domain | Scope and Responsibilities |
| :--- | :--- |
| **`operator`** | Operator lifecycle, OperatorSession binding, heartbeats, command dispatch, and local execution state. |
| **`ai`** | AI evaluation lifecycle (`ztd.v1.ai.eval.*`) and LLM chat filter updates (`ztd.v1.ai.llm.chat.filter.updated`). |
| **`app`** | Application entities: cases, investigations, documents, agent and run status updates, and task completion. |
| **`platform`** | Core infrastructure: authentication and sessions, platform enrollment, approval changes, notifications, SSE connection lifecycle, console logs, telemetry, and usage. |
| **`public`** | Public spectator feed batches, feed key rotation, and published proofs. |
| **`consensus`** | L2 consensus vote records (`ztd.v1.consensus.vote.recorded`). |
| **`inference`** | Model provenance attestations and provider-boundary observations. |

### Event Kinds and Operational Semantics

| Kind | Wire Terminal | Persistence Target | Transports | Purpose |
| :--- | :--- | :--- | :--- | :--- |
| **`request`** | `.requested` | `ephemeral`, `operator.audit_log`, `gateway.docstore`, `gateway.operator_docs` | `governed`, `pubsub`, `sse`, or none | Initiates an operation. If a `governance` block is present, it MUST be wrapped in a `GovernanceEnvelope`. |
| **`outcome`** | e.g. `.completed`, `.failed` | `ephemeral`, `gateway.sse_store`, `gateway.audit_log`, `gateway.docstore`, `gateway.operator_docs`, `operator.audit_log` | `sse`, `pubsub`, or none | Reports completion status, failure details, or incremental progress for a prior request. |
| **`fact`** | e.g. `.created`, `.updated` | `ephemeral`, `gateway.sse_store`, `gateway.audit_log`, `operator.audit_log` | `sse`, `pubsub`, or none | Records an autonomous occurrence independent of a direct client request. |
| **`stream`** | Infix `.stream.`, `.chunk.`, `.delta.`, `.keepalive.`, or `.thinking.` | `ephemeral` | `sse` only | Transports ephemeral UI fragments such as SSE keepalives (`ztd.v1.platform.sse.keepalive.sent`). Never persisted. |

### Governance Envelopes and Action Type Derivation

Not all request events are governed. A request event becomes governed when its registry entry defines a `governance` block:
- **Non-governed requests** (no `governance` block, for example `ztd.v1.platform.auth.login.requested`): carry no `governance` block and are not wrapped in a `GovernanceEnvelope`.
- **Governed requests** (`governance` block present): wrapped in a canonical `GovernanceEnvelope` (`protocol/proto/ztd/common/v1/common.proto`).

The Gateway PDP resolves the incoming `event_type` through `constants.ValidateGovernedRequest`, which returns the registry-mapped `action_type`. `BuildGovernanceEnvelope` (`internal/services/gateway/dispatch_service.go`) decodes the payload for that action class and runs L1 Doctrine validation in every posture, before the L3 gate. The active posture then decides which L2 and L3 proofs gate execution. The runtime does not read the registry's `governance.payload` value; it is carried into generated constants only.

### Transport Ownership and Channel Routing

The registry declares transports per event. The runtime enforces only the `governed` transport, through `constants.ValidateGovernedRequest`. The `sse` and `pubsub` declarations are not checked at publish time.
- **`sse`**: Client delivery channel for live browser UI updates and approval challenges (`/api/v1/sse/stream`). Never serves as a persistent audit ledger.
- **`governed`**: Dedicated channel for transactions requiring the five-layer pipeline. Requests are packaged into `GovernanceEnvelope` structures and published to OperatorSession-bound command channels (`cmd:<operator_id>:<operator_session_id>`).
- **`pubsub`**: WebSocket messaging layer for runtime coordination between Gateway and Operators (e.g. heartbeat delivery, certificate renewal, and receipt relays).

### Storage Ownership Matrix

Persistence ownership is assigned per event in the registry:
- **`operator.audit_log`**: Append-only Local-First Events Store on the host where the Operator executes. Under LFEA, this is the authoritative execution record.
- **`gateway.audit_log`**: Gateway's Local-First Events Store, hash-chained like `operator.audit_log`. The registry assigns it to the `ztd.v1.public.feed.*` outcomes and to `ztd.v1.consensus.vote.recorded`.
- **`gateway.sse_store`**: `sse_events` table backing historical SSE replay (`GET /api/v1/sse/events`). Rows are pruned by age.
- **`gateway.operator_docs`**: Gateway `operators` document collection tracking registered Operators, their status, and heartbeat state.
- **`gateway.docstore`**: Authoritative Gateway document store for collections such as `users`, `app_policies`, `trusted_signers`, `cases`, `investigations`, and `platform_enrollments`.
- **`ephemeral`**: In-memory, non-persisted events for high-frequency or streaming data.

### Hash-Chained Audit Ledger Mechanics

Events appended to the `events` table of the Local-First Events Store form an append-only cryptographic hash chain. Each row records:
1. `seq`: Unique integer sequence number (`events.seq`, `UNIQUE`) scoped to the host database. An empty chain starts from an all-zero genesis hash, or from the latest checkpoint hash after pruning.
2. `prev_hash`: The `hash` of the immediately preceding row.
3. `content_digest`: HMAC-SHA256 over the JSON encoding of the content fields (`content_text`, `command_raw`, `command_exit_code`, `command_stdout`, `command_stderr`, `execution_duration_ms`, `execution_id`) under a subkey the runtime vault derives (see [Storage Architecture](./storage.md)). A locked vault fails the append closed.
4. `hash`: SHA-256 digest over `ztd-audit-v1|seq|prev_hash|type|operator_session_id|timestamp|content_digest|transaction_id`.

Receipt stages append `ztd.v1.operator.receipt.recorded` facts to the chain. The current event receipt is the one of those events whose `event_receipt_status` is not null. The chain event type string stays `ztd.v1.operator.receipt.recorded`; it is an input to the chain hash.

Pruning deletes chained events older than a cutoff, excluding checkpoint events. Before deletion, it appends a `ztd.v1.platform.audit.chain.checkpointed` fact recording `pruned_through_seq`, `pruned_through_hash`, and `pruned_count`, and writes a matching `audit_chain_checkpoints` anchor row. Chain verification without an explicit start sequence begins after the latest checkpoint.

### Wire Value Grammar and Terminal Validation

Event wire values follow `ztd.v1.<domain>.<entity>[.<qualifier>...].<terminal>`.
- **Closed Terminal Set**: Validated strictly by `constgen`. Permitted terminals include: `requested`, `started`, `received`, `completed`, `failed`, `cancelled`, `timeout`, `recorded`, `created`, `updated`, `deleted`, `granted`, `rejected`, `denied`, `revoked`, `expired`, `acknowledged`, `sent`, `missed`, `bound`, `unbound`, `opened`, `closed`, `established`, `checkpointed`, `exported`, `published`, `rotated`, `available`, `invoked`, `reached`, `detected`, `resolved`, `appended`, `truncated`, `retry`, `heartbeat`, `active`, `ai`, `answered`, `append`, `assigned`, `authenticated`, `blocked`, `changed`, `cleared`, `complete`, `configured`, `confirmed`, `disabled`, `end`, `error`, `escalated`, `feedback`, `lettered`, `loaded`, `logged`, `occurred`, `offline`, `open`, `preparing`, `questions`, `queued`, `registered`, `replayed`, `reported`, `running`, `selected`, `skipped`, `stale`, `stopped`, `submitted`, `succeeded`, `switched`, `system`, `terminated`, `tier1`, `tier2`, `tier3`, `unauthenticated`, `unavailable`, `update`, `user`, `verified`.
- **Exempt Forms**:
  - `ztd.v1.<domain>.<terminal>` for entity-less facts (e.g. `operator.bound`, `operator.unbound`).
  - `*.status.updated.<state>` for operator and investigation status updates.
  - Stream fragments containing `.stream.`, `.chunk.`, `.delta.`, `.keepalive.`, or `.thinking.`.
- **Prohibited Terminals**: `event`, `execution`, `result`, `info`, and `notification` are outside the closed set, so `constgen` rejects wire values ending in them. Conforming wire values use `updated`, `started`, `completed`, or `sent` instead.

## Anti-patterns

- **Hand-Editing Generated Constants**: Modifying `events_gen.go` or `events.ts` manually instead of editing `protocol/constants/events.json` and running `make constants-generate` (INV-EVT-REG-01).
- **Treating SSE as Durable Governance State**: Relying on SSE event delivery as authoritative execution proof instead of querying the local SQLite audit ledger under LFEA (INV-EVT-PERS-02).
- **Publishing Events Over Undeclared Transports**: Sending events across channels not explicitly permitted in the registry's `transport` array (INV-EVT-XPORT-01).
- **Client-Supplied Action Types**: Permitting clients to specify `action_type` directly instead of letting the Gateway derive it from the event registry (INV-EVT-GOV-02).
- **Using Prohibited Terminals**: Introducing wire values ending in generic nouns (`.event`, `.info`, `.result`) rejected by `constgen`.

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Gateway Architecture](gateway.md): Gateway Policy Decision Point responsibilities and SSE streaming.
- [Operator Architecture](operator.md): Operator Policy Execution Point, command channels, and audit persistence.
- [Governance Architecture](governance.md): Five-layer governance interlock, envelope hashing, and receipt persistence.
- [Storage Architecture](storage.md): SQLite schema layouts, hash-chained ledgers, and document stores.

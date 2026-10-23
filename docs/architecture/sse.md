---
doc_id: sse
title: SSE Streaming
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - internal/services/gateway/sse_controller.go
  - internal/services/gateway/sse_event_publisher.go
  - internal/services/gateway/sse_event_service.go
  - internal/services/gateway/approvals_events.go
  - internal/services/gateway/operator_status_events.go
  - internal/services/gateway/passkey_orchestrator.go
  - internal/services/gateway/observe_producer_controller.go
  - internal/services/gateway/observe_producer.go
  - internal/services/gateway/evidence_window_publisher.go
  - internal/services/gateway/gateway_auth.go
  - internal/services/gateway/gateway_pubsub.go
  - internal/services/pubsub/channels.go
related:
  - docs/architecture/gateway.md
  - docs/architecture/auth.md
  - docs/architecture/network.md
  - docs/architecture/events.md
  - docs/architecture/console.md
  - docs/guides/build_observe_frontend.md
when_to_read: Understanding how Gateway events reach browsers and CLIs over SSE, integrating observe producers, stream replay and reconnect behavior, or debugging missed or duplicated live events.
do_not_use_for:
  - Governance envelope model (see gateway.md)
  - Authentication mechanisms (see auth.md)
  - Event registry contents and persistence values (see events.md)
---

# SSE Streaming

## Purpose

Defines the Gateway's Server-Sent Events (SSE) delivery channel for session-targeted events. The Gateway publishes session-targeted events to browsers and CLIs from its internal services (approvals, Operator status, heartbeats, model provenance evidence, approval and passkey completion notifications) and from the observe producer endpoints that application workloads call over mTLS. Other Gateway routes also write `text/event-stream` responses (the MCP endpoint and the public mirror relay); they use their own routing and are outside this document's session model.

SSE is a session-scoped delivery channel for asynchronous notifications and telemetry. Publishing an event neither mutates governed state nor alters the five-layer governance interlock; authoritative outcomes reside in signed receipts and platform records.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Event Routing and Wire Framing](#event-routing-and-wire-framing)
- [Event Sources and Persistence](#event-sources-and-persistence)
- [Observe Producers](#observe-producers)
- [Consumer Replay and Reconnection](#consumer-replay-and-reconnection)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

Key invariant groups: [Event model](#event-model-inv-sse-evt), [Producer authentication](#producer-authentication-inv-sse-prod), [Consumer authentication](#consumer-authentication-inv-sse-cons), [Delivery semantics](#delivery-semantics-inv-sse-del).

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Event model (`INV-SSE-EVT`)

| ID | Rule |
| --- | --- |
| INV-SSE-EVT-01 | Every SSE event is keyed by two routing dimensions: `user_id` (ownership) and exactly one of `web_session_id` or `cli_session_id` (delivery target). A bare `user_id` is not a valid route. The Gateway does not provide user-wide fan-out. |
| INV-SSE-EVT-02 | Registered event type strings are constants generated from `protocol/constants/events.json` (`internal/constants/events_gen.go`). Non-registry CLI notification types such as `approval.completed` and `passkey.registered` are defined by their owning producers. The registry's `persistence` value is the contract for registered events: an `ephemeral` event is published with `SSEEventPublisher.PublishEphemeral`, and every other persistence value (for example `gateway.sse_store`) is published with `SSEEventPublisher.Publish`. |
| INV-SSE-EVT-03 | SSE frames carry a `data:` field and, for persisted events, an `id:` field; no `event:` field is emitted. Ephemeral events have no durable row and therefore no `id:` line. Consumers read the application event type from the nested `event.type` value in the JSON data payload. Heartbeats are SSE comments (`: heartbeat\n\n`) and do not advance the event cursor. |
| INV-SSE-EVT-04 | Ephemeral events are delivered via live streams but not persisted to the SSE store; consumers cannot recover them via replay. Persisted events are appended before live publication, so a persistence failure prevents delivery. A live ephemeral event is emitted without an `id:` line to avoid rewinding the client's `Last-Event-ID` cursor. |

### Producer authentication (`INV-SSE-PROD`)

| ID | Rule |
| --- | --- |
| INV-SSE-PROD-03 | The observe producer endpoints (`POST /api/v1/observe/producer/agent-state`, `POST /api/v1/observe/producer/run-state`, `POST /api/v1/observe/producer/stop`) accept only app-workload mTLS certificates (`RouteAuthMTLS`). Strict JSON decoding rejects unknown fields. `user_id` is derived from the mTLS peer certificate; the request body carries no `user_id` field. |
| INV-SSE-PROD-04 | Observe producer validation, lifecycle, ownership, and persistence rejections return a typed `models.ObserveProducerRejection` body with a stable `code`. Transition and stale-update rejections include their typed details; ownership misses return `forbidden` without record details, and unclassified failures return `internal` without details. The agent harness client decodes the body and preserves its classified cause for `errors.Is` and `errors.As`. Authentication and method rejection responses retain their shared boundary formats. |
| INV-SSE-PROD-05 | Terminal run projection statuses (`completed`, `failed`, `cancelled`) accept no ordinary transition. A run-state request naming `reopens` is a producer repair. It is valid only when it names a terminal status and moves toward a non-terminal one (otherwise `ErrObserveStatusInvalid`). It is applied as a compare-and-set: the projection must hold exactly the status `reopens` names, or the normal transition rules decide. The stale-update guard still applies, and each applied reopen is logged as a warning with its from and to statuses. |

### Consumer authentication (`INV-SSE-CONS`)

| ID | Rule |
| --- | --- |
| INV-SSE-CONS-01 | Consumers use dual authentication: either a web-session cookie (browser) or an mTLS certificate (CLI or Operator). When both are presented, mTLS takes precedence; an invalid certificate does not fall back to cookie auth. App certificates cannot consume SSE events. |
| INV-SSE-CONS-02 | `GET /api/v1/sse/events` (cursor fetch) and `GET /api/v1/sse/stream` (live) build the route entirely from authenticated context; routing query parameters are ignored. Queries match both the authenticated user and session before returning rows. |
| INV-SSE-CONS-03 | A CLI certificate selects its own CLI session via `X-ZTD-CLI-Session-ID`. The Gateway verifies the CLI session belongs to the authenticated user and, when the request carries an Operator session, that the Operator session is in the CLI session's persisted operator binding list (the primary binding or any secondary bound operator session, checked with the same binding-record rule `handleCLIAuth` uses). An unbound Operator session fails closed with `403`. An Operator certificate never selects a CLI session (`INV-AUTH-OP-BIND-07`); it selects an owned web session via `X-ZTD-Web-Session-ID`, and the Gateway verifies the Operator session's binding record names that web session before returning events. |

### Delivery semantics (`INV-SSE-DEL`)

| ID | Rule |
| --- | --- |
| INV-SSE-DEL-01 | `GET /api/v1/sse/events` returns rows with `id > since_id`, ordered ascending. `limit` defaults to 200; values below 1 or above 1000 also use 200. A cursor fetch does not delete or acknowledge rows. |
| INV-SSE-DEL-02 | `GET /api/v1/sse/stream` replays at most 1,000 rows per connection. With no cursor parameters, a fresh connection replays from ID 0; a `Last-Event-ID` header or positive `since_id` query parameter replays from that cursor. Explicit `since_id=0` without `Last-Event-ID` requests a live-only stream. When replay returns 1,000 rows, an unnumbered `truncated` sentinel carries the last emitted ID and limit; the client may reconnect with a higher cursor. |
| INV-SSE-DEL-03 | Each live stream maintains a 100-event in-memory queue. If a consumer falls behind, the Gateway drops the oldest queued event; persisted events can be recovered through cursor-based replay on reconnect, while ephemeral events cannot. |
| INV-SSE-DEL-04 | The stream installs its live listener and clears the server write deadline before flushing response headers. Heartbeats are sent every 30 seconds as SSE comments. The maintenance loop (every 30 seconds) removes events older than one hour from `sse_events`. |
| INV-SSE-DEL-05 | The pub/sub broker MUST invoke in-process callbacks outside its handler registry lock so callbacks can publish notifications while other handlers unregister. Unregistration prevents selection by later publishes; callbacks already selected MAY finish. SSE teardown MUST leave its queue safe for these callbacks; the request context owns stream termination. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| SSE poll handler | `internal/services/gateway/sse_controller.go` (`handleInternalSSEEvents`) | Builds route from auth context, returns rows since cursor |
| SSE stream handler | `internal/services/gateway/sse_controller.go` (`handleInternalSSEStream`) | `./ztd test integration --pkg ./internal/services/gateway --run HandleInternalSSEStream` |
| SSE publisher | `internal/services/gateway/sse_event_publisher.go` | Persist-before-publish for `Publish`; live-only for `PublishEphemeral` |
| SSE event storage | `internal/services/gateway/sse_event_service.go` | Append, list, cleanup (30s interval, >1h purge), count |
| Live channel naming | `internal/services/pubsub/channels.go`, `internal/constants/channels.go` | `sse:cli:<id>`, `sse:web:<id>` |
| Observe producers | `internal/services/gateway/observe_producer_controller.go` | `./ztd test integration --pkg ./internal/services/gateway --run ObserveProducer` |
| Observe producer rejection contract | `internal/models/observe_producer_rejection.go`, `internal/tools/agent_harness/client/observe_producer.go` | Typed code, transition and stale details; client preserves the classified cause |
| Event registry | `protocol/constants/events.json` | `make constants-check` |

## Event Routing and Wire Framing

```text
Publisher (Internal Service or Observe Producer)
                      │
                      ▼
               SSERoute Struct
         ├── user_id: "alice"
         └── target: cli_session_id OR web_session_id (exactly one)
                      │
        ┌─────────────┴─────────────┐
        ▼                           ▼
Persisted Event             Ephemeral Event
(Publish)                   (PublishEphemeral)
  │                           │
  ├─ 1. Write DB row          │
  │     (sse_events)          │
  │                           │
  └─ 2. PubSub live channel ──┴─► PubSub live channel
        sse:cli:<id>                  sse:cli:<id>
        sse:web:<id>                  sse:web:<id>
```

- **Persisted Frame**:
  ```text
  id: 1042
  data: {"user_id":"alice","web_session_id":"","cli_session_id":"...","event":{"type":"ztd.v1.operator.status.updated.active","data":{...}}}

  ```
- **Ephemeral Frame** (no `id:` field):
  ```text
  data: {"user_id":"alice","web_session_id":"...","cli_session_id":"","event":{"type":"ztd.v1.platform.approvals.changed","data":{"subject":"transactions"}}}

  ```
- **Heartbeat Frame** (comment):
  ```text
  : heartbeat

  ```

## Event Sources and Persistence

| Event Type | Persistence | Publisher / Trigger | Description |
| :--- | :--- | :--- | :--- |
| `ztd.v1.platform.approvals.changed` | `ephemeral` | `approvals_events.go` | Signals pending L3 transactions or platform enrollment changes. Sent to every unexpired web session and active CLI session of the owner |
| `ztd.v1.operator.status.updated.<state>` | `gateway.sse_store` | `operator_status_events.go` | Operator status announcements: `active` when the Operator's command-channel subscription is established (ready for commands), and the `offline`, `stopped`, and `terminated` transitions; `stale` is never stored or announced as a transition and is derived on read |
| `ztd.v1.operator.heartbeat.received` | `ephemeral` | `operator_status_events.go` | Real-time telemetry carrying `last_heartbeat_at` and `stale_at`. Sent to the owner's web sessions |
| `ztd.v1.inference.model.provenance.attestation.recorded` | `ephemeral` | `evidence_window_publisher.go` | The Gateway stored a Provenance Operator attestation window; carries `provider_attempt_id`. Sent to the owner's connected CLISessions |
| `ztd.v1.inference.provider.boundary.observation.recorded` | `ephemeral` | `evidence_window_publisher.go` | The Gateway stored an Observer Operator provider-boundary window; carries `provider_attempt_id`. Sent to the owner's connected CLISessions |
| `ztd.v1.app.agent.status.updated` | `gateway.sse_store` | `observe_producer.go` | Agent state emitted by registered application workloads |
| `ztd.v1.app.run.status.updated` | `gateway.sse_store` | `observe_producer.go` | Run state emitted by registered application workloads |
| `ztd.v1.ai.eval.*` (except `stop.requested`) | per registry entry | `observe_producer.go` (`emitLiveSSEEvent`) | Campaign progress events from the observe producer. A repeated `event_id` is rejected before emission (`ErrObserveDuplicateEventID`) |
| `ztd.v1.ai.eval.stop.requested` | `ephemeral` | `observe_producer.go` | Run-scoped stop request from an app workload; carries `campaign_id` and `run_id`. Sent to the named session's open stream only and never stored |
| `approval.completed` (non-registry) | persisted | `passkey_orchestrator.go` | Direct CLI notification when a suspended L3 transaction is approved or denied. Its `receipt` is the canonical protojson signed `ActionReceipt` of the resumed transaction on approval or the `L3_DENIED` rejection on denial (`INV-GOV-ACT-13`) |
| `passkey.registered` (non-registry) | persisted | `passkey_orchestrator.go` | Direct CLI notification of completed passkey registration |

Registry rows take their persistence value from `protocol/constants/events.json`, which is the canonical source; this table does not replace it.

## Observe Producers

App workloads enrolled through platform enrollment publish telemetry over mTLS to the observe producer endpoints:

- `POST /api/v1/observe/producer/agent-state`
- `POST /api/v1/observe/producer/run-state`
- `POST /api/v1/observe/producer/stop`

The Gateway accepts these typed updates only from app-workload mTLS identities, derives `user_id` from the authenticated certificate, and validates strict request JSON. Each request names exactly one web or CLI session as its SSE target; after persisting the associated projection, the Gateway emits the event for that session on the path its registry persistence class selects. A persisted event is stored for that session and delivered live when the session has an open stream; an `ephemeral` event is delivered live only. A stop request names one campaign run (`campaign_id` and `run_id`), and the `ai.eval.stop.requested` event it produces carries the same `run_id`. That event is `ephemeral`, so a session without an open stream when the stop is posted never receives it and cannot replay it.

## Consumer Replay and Reconnection

```text
Browser / CLI Client                          Gateway SSE Controller
        │                                                │
        ├─────── GET /api/v1/sse/stream ────────────────►│
        │        (Last-Event-ID: 1020)                   │
        │                                                │
        │◄────── HTTP 200 (headers flushed) ─────────────┤
        │                                                │
        │  [Replay Phase: id > 1020, max 1000]           │
        │◄────── id: 1021, data: {...} ──────────────────┤
        │◄────── id: 1022, data: {...} ──────────────────┤
        │                                                │
        │  [Live Phase: de-duplicated real-time]         │
        │◄────── id: 1023, data: {...} ──────────────────┤
        │◄────── data: {ephemeral...} ───────────────────┤
        │◄────── : heartbeat ────────────────────────────┤
```

1. **Replay Phase**: With no cursor parameters, the Gateway fetches stored events from ID 0. A `Last-Event-ID` header takes precedence over `since_id`; an explicit `since_id=0` without that header skips replay. Replay returns rows where `id > cursor`, up to 1,000 events. If 1,000 rows are returned, a `truncated` frame is emitted. If the replay query fails, the Gateway emits an unnumbered `{"type":"error","reason":"replay_failed"}` frame and proceeds to the live phase.
2. **Live Phase**: As events arrive on the pub/sub channel, the Gateway streams them to the client. Persisted events emitted during replay are deduplicated by ID.
3. **Queue Overflow**: Each stream buffer holds up to 100 events. If the consumer cannot keep up, the oldest event is dropped. Reconnecting with `Last-Event-ID` recovers dropped persisted events from SQLite.

## Procedures

### Consuming Live Events via CLI

The CLI SSE client (`internal/cli/sse`) is used by auth flows that wait for approval or passkey-registration events. Other CLI commands share the same client, including operator deploy events, `gwremote` model-provenance and provider-observation windows, and eval run events. For example, `ztd auth approve` opens the browser approval page and waits for its completion event:

`ztd auth approve <transaction_hash>` opens the Gateway approval page and waits for the `approval.completed` event for that transaction. The `--cli` flag signs the transaction hash with the enrolled CLI certificate instead, and `--deny` denies it from the enrolled CLI session (`./ztd auth approve --help`). The `--cli` and `--deny` paths read the final receipt from the HTTP response; the browser path reads it from the `approval.completed` event on the stream. A stream line may hold up to `SSEClientMaxLineSize` bytes (`internal/constants/paths.go`), enough for an `approval.completed` event whose receipt carries both escaped execution output streams.

### Consuming Polled Events

Query stored events after a known cursor:

Copy the `ztd_web_session_cookie` value of a signed-in browser session into a local shell variable named `ZTD_WEB_SESSION_COOKIE` (the variable is set in your shell, not by the Gateway), then run:

```bash
curl -k -H "Cookie: ztd_web_session_cookie=${ZTD_WEB_SESSION_COOKIE}" \
  "https://localhost:8443/api/v1/sse/events?since_id=1000&limit=50"
```

## Anti-patterns

- **Broadcasting user-wide events without session scoping**: Attempting to publish without specifying `cli_session_id` or `web_session_id` (`INV-SSE-EVT-01`).
- **Supplying routing IDs in consumer query parameters**: Attempting to select a user or session through query parameters; routing derives only from authenticated context (`INV-SSE-CONS-02`).
- **Supplying `user_id` in an observe producer request body**: Attempting to name the owning user in the body; `user_id` derives from the mTLS peer certificate and strict decoding rejects unknown fields (`INV-SSE-PROD-03`).
- **Treating an observe producer rejection as an untyped HTTP error**: Discarding the typed rejection body loses its stable code and transition or stale-update details (`INV-SSE-PROD-04`).
- **Treating SSE as a permanent audit log**: Relying on `sse_events` for long-term historical records; rows older than 1 hour are automatically pruned (`INV-SSE-DEL-04`).
- **Emitting `id:` lines on ephemeral events**: Rewinding consumer cursors by sending synthetic or zero IDs for ephemeral notifications (`INV-SSE-EVT-03`).

## Links out

- [Gateway Architecture](gateway.md): Gateway services and protocol surfaces.
- [Authentication and Authorization](auth.md): mTLS identities, CLI sessions, web sessions, and application policies.
- [Network Architecture](network.md): TLS listeners, PKI, and cross-component transport.
- [Event and Action Protocol](events.md): Event registry, persistence values, and transport ownership.
- [Console Architecture](console.md): Browser connection lifecycle, reconnect, and event routing.
- [Public Spectator Architecture](public_spectator.md): Read-only public mirror SSE relay and export architecture.

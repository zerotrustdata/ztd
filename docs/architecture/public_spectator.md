---
doc_id: public_spectator
title: Public Spectator Architecture and Threat Model
audience: architects and security reviewers
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - internal/services/gateway/public_mirror.go
  - internal/services/gateway/public_mirror_store.go
  - internal/services/publicdisclosure/
  - internal/cli/cmd/public/
  - docs/architecture/
related:
  - docs/architecture/sse.md
  - docs/architecture/console.md
  - docs/architecture/gateway.md
  - docs/architecture/network.md
  - docs/guides/public_spectator.md
  - docs/guides/build_observe_frontend.md
when_to_read: Designing, implementing, or reviewing public spectator feed export, mirror infrastructure, disclosure policies, threat mitigations, or browser-facing read endpoints.
do_not_use_for:
  - Deployment configuration procedures (docs/guides/public_spectator.md)
  - Owner-local observe frontend contracts (docs/guides/build_observe_frontend.md)
  - Event bridge implementation (docs/architecture/sse.md)
  - Gateway trust boundaries (docs/architecture/gateway.md)
---

# Public Spectator Architecture and Threat Model

## Purpose

Defines the architecture, trust boundaries, data classification, network topologies, signed export bindings, threat model, and availability policies for public spectator observation of ztd evaluation campaigns. Specifies what anonymous visitors can observe, how public-safe data is exported and signed, and what data is strictly forbidden from crossing the projection boundary.

The credentialed owner-local observe mode and the anonymous public-spectator mode are separate, non-interchangeable browser modes with distinct authentication, network paths, endpoint allowlists, and disclosure boundaries.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Two Browser Modes](#two-browser-modes)
- [Network Path and Listener Isolation](#network-path-and-listener-isolation)
- [Closed Allowlist and Data Classification](#closed-allowlist-and-data-classification)
- [Export Binding and Cryptographic Continuity](#export-binding-and-cryptographic-continuity)
- [Threat Model and Mitigations](#threat-model-and-mitigations)
- [Anonymous Read Limits, Retention, and Availability](#anonymous-read-limits-retention-and-availability)
- [Current-State Snapshot](#current-state-snapshot)
- [Relationship to Platform Architecture](#relationship-to-platform-architecture)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Mode and Endpoint Isolation (`INV-SPEC-ISO`)

| ID | Rule |
| --- | --- |
| INV-SPEC-ISO-01 | Owner-local observe mode and anonymous public spectator mode MUST NOT share state, configuration, credentials, or endpoints. Public visitors MUST NOT connect directly to the private Gateway API. |
| INV-SPEC-ISO-02 | The public mirror listener MUST expose only read-only endpoints (`GET /bootstrap`, `GET /snapshot`, `GET /history`, `GET /stream`, `GET /proof-catalog`, `GET /proof-head`, `GET /proof-segments`, and `GET /proofs/<artifact-id>`), plus the read-only evaluation explorer static host (`GET` and `HEAD`, from embedded assets or the `--eval-explorer-root` directory). It MUST NOT mount mutation, producer, governance, audit, or runtime-state filesystem routes. |
| INV-SPEC-ISO-03 | Private ingest operations (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`, `GET /proof-root`) MUST be served on an isolated loopback listener and MUST require the ingest bearer token. |
| INV-SPEC-ISO-04 | The private ingest listener (`PublicMirrorServer.IngestHandler()`) MUST NOT mount anonymous read routes, and the public read listener (`PublicMirrorServer.PublicHandler()`) MUST NOT mount ingest routes. |

### Data Classification and Projection (`INV-SPEC-DATA`)

| ID | Rule |
| --- | --- |
| INV-SPEC-DATA-01 | Every exported record MUST pass a closed allowlist enforced by campaign projector, disclosure validator, publisher, and mirror before becoming browser-visible. Any unknown field fails closed. |
| INV-SPEC-DATA-02 | Every structural feed-record string outside the verbatim carriers (`INV-SPEC-DATA-03`) MUST be at most 512 bytes. The publisher and mirror MUST NOT screen record or proof-artifact content by field name or string content; public records are published verbatim. |
| INV-SPEC-DATA-03 | Evaluation evidence is published in full and never redacted or screened. The feed-record carriers `model_response`, `failure_output`, `role_transcripts`, and each grade summary's `detail` are verbatim text held only to the 256 KiB record bound. A `role_transcripts` value that exceeds its bounds or would push the record past 256 KiB is omitted whole, never truncated. The complete agent trace (exact model input, every turn's thinking and text, tool arguments and results, signed receipts, actuator anchors, and the sentinel bracket) is published as a content-addressed proof artifact bound by an `assignment_agent_trace` evidence binding. The campaign-frozen scenario fixture (input and gold criteria) is published as campaign-scoped proof artifacts bound by `scenario_input_fixture` and `scenario_gold_criteria` evidence bindings. |
| INV-SPEC-DATA-04 | The mirror MUST keep a durable current read model independent of replay-history retention, advance it in the accepted-batch transaction, and serve it coherently through `/bootstrap`. Explorer run fields MUST come from canonical backend summaries; live activity MUST NOT overwrite them. |

### Cryptographic Continuity (`INV-SPEC-CRYPTO`)

| ID | Rule |
| --- | --- |
| INV-SPEC-CRYPTO-01 | Every exported batch MUST be an append-only, monotonically sequenced record signed with Ed25519 over the content hash and linked to the predecessor batch's `content_hash` (`previous_batch_hash`). |
| INV-SPEC-CRYPTO-02 | Proof artifacts MUST be immutable, content-addressed files served under paths derived from their SHA-256 digests. |
| INV-SPEC-CRYPTO-03 | Key rotation MUST occur via a signed revocation batch. Batches signed by revoked keys are rejected. Historical batches remain verifiable against the retained key registry. |
| INV-SPEC-CRYPTO-04 | Withdrawing a campaign run (`WithdrawCampaignDataset`, reached through the Gateway's publication-state `DELETE` that `ztd eval runs delete` calls) MUST NOT remove or rewrite signed batches. The mirror records the run's dataset as withdrawn and filters its projections from `/bootstrap`, `/history`, and `/stream`; the publisher prunes the run's proof artifacts from the proof catalog and pushes it. Every mirror snapshot carries `withdrawal_digest` (SHA-256 over the sorted withdrawn dataset IDs, zero hash when none), and withdrawal pushes a fresh `snapshot` event to open streams. A client MUST replace its in-memory state from `/bootstrap` when the digest changes, because the signed chain alone cannot reveal a withdrawal. |
| INV-SPEC-CRYPTO-05 | A proof segment MUST bind its source, index, predecessor root, every entry field, withdrawals, timestamp, signing key ID, and verifier instructions to its Ed25519-signed root. The mirror MUST reject gaps, conflicting roots at an accepted index, repeated artifact IDs, unknown or repeated withdrawals, and a legacy manifest signature presented as a segment signature. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Public mirror durable store | `public-mirror/mirror.db`, `public-mirror/proofs/<artifact-id>` (`internal/services/gateway/public_mirror_store.go`) | `./ztd test integration --pkg ./internal/services/gateway --run 'TestMirror\|TestPublicMirrorStore'` |
| Public feed configuration | `public-feed/` | Directory `0700`, files `0600`; the signing key and ingest token are keystore-sealed |
| Public publisher commands | `internal/cli/cmd/public/` | `./ztd public status`, `./ztd public publish --help` |
| Disclosure validation | `internal/services/publicdisclosure/validator.go` | `./ztd test unit --pkg ./internal/services/publicdisclosure` |
| Public spectator runtime | `internal/services/gateway/public_spectator_runtime.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestPublicSpectator` |
| Evaluation verification | `internal/services/evaluation/` | `./ztd eval runs verify` |

## Two Browser Modes

ztd strictly separates browser observation into two non-interchangeable modes:

```text
+-----------------------------------+        +-----------------------------------+
|     Owner-Local Observe Mode      |        |      Public Spectator Mode        |
+-----------------------------------+        +-----------------------------------+
| • Direct connection to Gateway    |        | • Connection to anonymous mirror  |
| • Authenticated via WebAuthn      |        | • Anonymous, no credentials       |
| • User-scoped, HttpOnly cookie    |        | • Public-safe feed & proof read   |
| • Reaches private Gateway APIs    |        | • Reaches loopback tunnel only    |
| • Full audit, receipts, approvals |        | • Closed allowlist projections    |
+-----------------------------------+        +-----------------------------------+
```

- **Owner-Local Observe Mode**: An authenticated operator connects to the private Gateway (`https://<gw>:8443/console/`). The browser authenticates via WebAuthn passkeys, receiving a secure session cookie.
- **Public Spectator Mode**: Anonymous internet visitors connect via HTTP or HTTPS directly to the public mirror listener (`0.0.0.0:8082` on cloud VM deployments with `--public-spectator-allow-container-bind`), or through an external domain routed via a `cloudflared` tunnel or reverse proxy. No credentials are sent, and these requests reach only the mirror's public read surface, not the private Gateway API.

## Network Path and Listener Isolation

The Gateway process hosts the `PublicSpectatorRuntime` (`internal/services/gateway/public_spectator_runtime.go`), which binds the private ingest and public read listeners, and an explorer listener when explorer assets are available:

```text
Campaign Host                     Gateway Process                      Public Ingress
─────────────                     ───────────────                      ──────────────
Canonical Eval Runs
       │
       ▼ (owner mTLS)
POST /api/v1/public-feed/batches ──► PublicSpectatorRuntime
                                      ├─ Private Ingest Listener (:8081)
                                      │  (authenticated POST /ingest, loopback only)
                                      │
                                      └─ Public Read Listener (:8082)
                                         (GET /bootstrap, /snapshot, /history, /stream)
                                                      ▲
                                                      │ Direct TCP Ingress (:8082)
                                                      │ or loopback tunnel (cloudflared)
                                                      │
                                               Public Browser & Probes
```

1. **Private Ingest Listener**: Serves ingest routes only (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`, `GET /proof-root`), each of which requires the ingest bearer token. It is built from `PublicMirrorServer.IngestHandler()` and mounts no anonymous read route: a read path such as `GET /snapshot` returns 404 here. It is unconditionally restricted to loopback (`127.0.0.1`, `localhost`, `::1`) per `INV-SPEC-ISO-03`.
2. **Public Read Listener**: Serves anonymous read endpoints only (`GET /bootstrap`, `GET /snapshot`, `GET /history`, `GET /stream`, `GET /proof-catalog`, `GET /proof-head`, `GET /proof-segments`, `GET /proofs/<id>`). It is built from `PublicMirrorServer.PublicHandler()` and mounts no ingest or mutation route: `POST /ingest` and the other ingest paths return 404 here, regardless of credentials. Unknown paths and mutation methods are rejected. The JSON read endpoints (`/bootstrap`, `/snapshot`, `/history`, `/proof-catalog`, `/proof-head`, `/proof-segments`) are gzip-compressed when the request accepts it; `/stream` and `/proofs/<id>` are not. `/history` takes optional `kind` and `dataset` parameters that keep only records whose top-level `kind` and `dataset_id` match, applied before pagination. When explorer assets are available, the same listener also serves the explorer static host for `GET` and `HEAD`; other methods on those paths return HTTP 404.

The two handlers are separate by construction; there is no combined mirror handler. The publisher's mirror origin is the private ingest listener, and Gateway-side read probes (`gwremote` publication status and history/bootstrap reads) resolve against the public read listener.
3. **Explorer Listener**: When explorer assets are available and `ExplorerListenAddress` differs from the public listener, the runtime binds it as a dedicated listener for the same explorer host. The default address is `127.0.0.1:5173`; `--eval-explorer-listen` overrides it.

4. **Loopback Binding**: Listeners bind only to loopback interfaces by default. When `--public-spectator-allow-container-bind` is set, the public read listener and evaluation explorer may bind `0.0.0.0` for direct cloud VM ingress or container routing. The private ingest listener remains strictly loopback-bound across all configurations (`INV-SPEC-ISO-03`). When fronted by a proxy, only CIDRs passed to `--public-spectator-trusted-proxy-cidr` are trusted to supply `CF-Connecting-IP`.

## Closed Allowlist and Data Classification

Only allowlisted fields and records cross into the public spectator feed.

### Permitted Projections

- **Campaign Identity**: Campaign ID, revision, cycle ID.
- **Run & Assignment Identity**: Run ID, assignment ID, variant ID, task ID, arm ID, repetition index.
- **Model Role Identity**: Primary variant ID, Assistant variant ID, Lite variant ID.
- **Metrics**: Metric ID, metric version, numerator, denominator, rate, unit, verification status.
- **Verification Status**: Verification status, verified index-generation hash.
- **Categorical Environment Labels**: `backend_name`, `quantization`.
- **Bounded Diagnostic Detail**: `trajectory_outcome` (closed vocabulary), `guided_retry_count`, `failure_reason`, `tools_declared`.
- **Verbatim Model and Grader Text (Optional Extensions)**: `model_response`, `failure_output`, `role_transcripts`, and `benchmark_observations.grade_summaries[].detail` / `semantic_grade_summaries[].detail` (each text within the 256 KiB record bound, at most 8 role transcripts, no screening or redaction).
- **Agent Trace Proof Artifact**: `assignment-<id>.agent-trace.json`, the persisted `AgentTrace` body byte for byte, served at `/proofs/<sha256>` and bound by an `assignment_agent_trace` evidence binding. The trace carries the exact first-turn model input (`input.messages`, `input.tools`) and, for held-context repetitions, `held_context`, the earlier repetition's conversation. `agent-trace.v8`, the schema version new traces are sealed with, adds `tool_calls[].approved_arguments_json`, the approved arguments a tool call executed (`AgentToolCall`, `internal/services/evaluation/campaign_agent_trace.go`).
- **Scenario Fixture Proof Artifacts**: `scenario-<id>.input.json` (`ScenarioInputFixture`: user prompt, inline context, workspace files including decoys, seeded investigation) and `scenario-<id>.gold.json` (`ScenarioGoldCriteria`), the campaign-frozen canonical bytes, so each SHA-256 equals the campaign's fixture reference. They are served at `/proofs/<sha256>` and bound by `scenario_input_fixture` and `scenario_gold_criteria` evidence bindings, which appear together or not at all. Their catalog entries carry no source run because every run of the campaign shares them, so one run's prune never removes them.
- **Assignment events database Proof Artifact**: `assignment-<id>.db`, a plaintext SQLite database of the assignment's public live-event records. The Gateway builds it from the live event records it retains, indexed per assignment (`assignment_event_records`). Before any proof is reused or requested, the publisher delivers every pending assignment's live event records, because the Gateway builds proofs only from records it was sent. Result publication ingests the audit proof bindings before it publishes the assignment result record. The publisher binds the `database_sha256` the Gateway returns and fails closed on an answer for another assignment or an empty digest. There is no vault key and no key artifact: the proof segment entry carries the database's SHA-256, and `ztd public verify-assignment --db assignment-<id>.db --segments proof-segments.jsonl --public-key <trusted-ed25519-key-hex>` checks it offline.

## Export Binding and Cryptographic Continuity

Every exported batch forms an immutable, verifiable link in a cryptographic chain:

| Field | Type | Description |
| :--- | :--- | :--- |
| `source_id` | String | Stable pseudonymous source deployment identifier |
| `protocol_version` | String | Protocol version (`1.0.0`) |
| `schema_version` | String | Projection schema version (`1.0.0`) |
| `first_sequence` | Int64 | Monotonic sequence number of the first record in batch |
| `last_sequence` | Int64 | Monotonic sequence number of the last record in batch |
| `previous_batch_hash` | Hex String | `content_hash` of the prior batch (zero hash for the first batch) |
| `record_hashes` | Array[Hex] | Ordered SHA-256 digests of all records in the batch |
| `generated_at` | RFC3339Nano | UTC timestamp set when the batch is built |
| `content_hash` | Hex String | SHA-256 digest over the canonical encoding of batch metadata and record hashes |
| `signing_key_id` | String | Identifier of the Ed25519 signing key |
| `signature` | Hex String | Ed25519 signature over `content_hash` |

### Signed Proof Segments

The mirror accepts a typed `PublicProofManifestSegment` on private `/proof-ingest` and exposes its signed log through anonymous `GET /proof-segments?source=...`. Segment indexes start at 1, and the first `previous_root` is the zero SHA-256 hash. Each later segment links to the preceding signed root. Exact root replays are idempotent. New segments append their entries or withdrawal IDs; withdrawals remove that source's entries from the active catalog and download lookup while preserving signed history and stored bytes. An artifact remains downloadable if another source still lists the same content ID. A withdrawn content ID cannot be added again to the same source's segment chain.

`models.ComputePublicProofSegmentRoot` defines the signed encoding. Each value is UTF-8 bytes prefixed by its decimal byte length and `:`. The hash is SHA-256 of the concatenated fields, in this order:

1. Domain `ztd-public-proof-segment-v1`, schema version, source ID, decimal segment index, previous root (lowercase hex), campaign ID, campaign revision, verified index-generation hash, verification boolean (`true` or `false`), decimal entry count.
2. For each entry in order: artifact ID, SHA-256, filename, media type, decimal byte size, classification, campaign ID, source run ID, generated-at timestamp, verification command, immutable URL.
3. Decimal withdrawal count, each withdrawn artifact ID in order, segment generated-at timestamp, signing key ID, verifier instructions.

Timestamps use UTC RFC 3339 with trailing fractional zeroes omitted (`time.RFC3339Nano`). The signature covers the decoded 32-byte root. The root and signature fields themselves are excluded from the hash. Entry metadata and adjacent variable-length fields therefore cannot be substituted without invalidating the signature.

Ingest verifies only the incoming segment and its inline artifact bytes. `PublicFeedProofMaxArtifacts` bounds the entries plus withdrawals in one signed segment (12,288), or the artifact count in one legacy full manifest. It does not limit the accumulated segment-backed catalog. This keeps each ingest bounded while allowing a long-lived source to publish additional assignments without withdrawing historical evidence. Reused artifacts require an existing metadata row and a regular file of the verified size. Indexed chain-position reads and count/byte deltas keep mutations proportional to the incoming entries and withdrawals. Startup re-verifies every signature, chain link, artifact hash, indexed chain column, entry projection, withdrawal projection, and source proof summary before serving requests.

The SQLite upgrade retains existing full-manifest rows at segment index zero and preserves their actual signatures. It does not manufacture signed segments. A source can replace that projection by sending its own first signed segment with the initial artifact set. Once the mirror accepts a segment for a source, it rejects full-manifest submissions for that source.

The public proof endpoints have separate roles: `/proof-catalog` lists active artifact metadata and download URLs, `/proof-head` returns the latest signing header without artifact entries, and `/proof-segments` supplies the signed history for verification.

The head reads the stored header and indexed count without materializing the inventory, so its read work and response size do not grow with artifact count. It accepts the same optional `source` selector as the catalog and segments, and returns 404 when that source has no proof publication. For segment-backed sources, the head has a nonzero `segment_index`; its root and signature belong to that segment and MUST be verified using the signed log. Its active artifact count is derived metadata, not an independently signed claim. The former public `/proof-manifest` route is removed.

The publisher appends signed links to `public-proofs/proof-segments.jsonl` and sends each pending segment with that segment's new artifact bytes. Private `GET /proof-root` returns the accepted segment index, root, and active artifact count. Delivery checks that head against the local log and sends only the remaining suffix; a divergent or advanced head fails closed. Local sync state checkpoints accepted segments but never suppresses checking the mirror, so delivery can recover after mirror replacement or a lost acknowledgement. A legacy publisher package is explicitly cut over to a source-signed genesis segment before its next push or withdrawal. Publisher manifest responses and the mirror proof head carry the actual segment root and signature with a nonzero `segment_index`.

Withdrawal preserves the publisher's immutable artifact files so an unsent prefix can still be delivered to a new mirror. The signed withdrawal removes those artifacts from the active catalog and public download authorization. Re-adding a withdrawn content ID to that source is rejected before appending to the local log.

The publisher keeps an in-memory index of the log: the byte offset and root of every segment, the active catalog, and every content ID ever listed. The first read replays and verifies the whole log once. After that, each access stats the file and verifies only bytes past the indexed offset, so appends by another process are picked up. A log that was replaced or shrank is replayed from the start, and nothing is appended to a log that does not verify. Every append verifies the new segment against the chain rules the mirror enforces (consecutive link, no relisting of a listed ID, withdrawals of active IDs only, and per-segment bounds), so a link the mirror would reject is never written. Appends hold the `public-proofs/proof-segments.lock` file lock across processes; a held lock fails the publication with `ErrRuntimeFileLocked`. A final line with no newline is an interrupted append. Reads ignore it, and the next append truncates it before writing. Delivery reads only the byte range of the pending segments. A publication's cost depends on its new entries, not on retained history; `TestPublisherProofLog_PublicationCostIsFlatInHistory` guards allocation, log growth, and push body size.

The proof log, its artifacts, and `proof-mirror-sync.json` belong to the feed generation of the key that signed them. Gateway startup reads an existing valid configuration without rewriting it, or supplies the gateway's default source and private mirror origin before explicitly creating a new feed. The CLI's `public init` is also an explicit create operation: it requires a source ID and mirror origin, rejects any existing feed generation or orphaned publisher state, and rolls back successfully written files if a later write fails. Both paths use `InitializeLocalPublicFeed` for generation creation. It mints a new signing key only when no generation path exists. If publisher state such as an outbox, proof log, or sync checkpoint remains without its matching generation configuration, initialization fails with `ErrPublicFeedOrphanedGeneration`, because data signed by a deleted key can never verify. During explicit initialization, any existing configuration, signing key, or ingest token fails with `ErrPublicFeedConfigExists`. During startup, a valid configuration with a missing signing key or ingest token fails with `ErrPublicFeedSigningKeyRequired` or `ErrPublicFeedIngestTokenRequired`. `public source transition` archives the whole generation, including the sync checkpoint, under `public-feed-archive/generations/<source_id>/`.

Offline verification supports one pinned signing key through `verify-proof --public-key`, or a pinned key registry through `--key-registry` for logs spanning rotations. Neither trust input is fetched from the mirror during verification.

## Threat Model and Mitigations

| Threat Vector | Mitigation Strategy | Fail-Closed Behavior |
| :--- | :--- | :--- |
| **Replay & Reordering** | Batches carry contiguous sequence ranges and link to the prior batch by `previous_batch_hash`. The mirror rejects a batch at or below its high-water sequence unless it re-delivers an accepted batch with the same sequence range and content hash, and rejects a broken link. | HTTP 200 with `accepted: false` and a typed `rejection_reason`: `sequence_out_of_order`, `duplicate_sequence`, or `hash_chain_mismatch` |
| **Equivocation** | Content hashes bind each batch to its exact position. A batch that reuses an accepted sequence range with a different content hash is equivocal. | HTTP 200 with `accepted: false` and `rejection_reason` `equivocation`; ingest revokes no key (revocation requires a signed revocation batch, `INV-SPEC-CRYPTO-03`) |
| **Stale Snapshots** | Snapshots bind high-water mark and chain hash. Clients verify freshness labels (`active`, `delayed`, `stale`, `intentionally_stopped`, `safety_stopped`, `source_offline`). | Stale data explicitly tagged, not served as live |
| **Forged Events** | Batches signed with Ed25519. Mirrors and offline verifiers validate signatures over canonical content hashes. | Bad signatures rejected with `accepted: false` and `rejection_reason` `signature_invalid`; unknown or revoked signing keys rejected with `unknown_key` or `revoked_key` |
| **Mirror Tampering** | Projections and proofs are content-addressed by SHA-256. Each signed segment entry binds an artifact's SHA-256 and size under the segment root. | Hash or signature mismatch invalidates the package in `verify-proof` |
| **Cache Poisoning** | Immutable HTTP cache headers (`Cache-Control: public, max-age=31536000, immutable`). `verify-proof` recomputes each active artifact's SHA-256 and checks it against its signed segment entry. | Mismatched artifact invalidates the package |
| **Path Traversal** | Proof ingest rejects filenames that are absolute, contain backslashes or `..`, or are not plain base names (`safePublicProofFilename`). The mirror serves artifacts by artifact ID from its store. The publisher refuses symlinked proof artifacts (`ErrPublicFeedProofSymlinkRejected`), and the offline verifier rejects non-regular artifact files. | Proof ingest rejected with HTTP 400; download of an unknown artifact ID returns HTTP 404 |
| **Denial of Service** | Strict rate limits (600 req/min per IP), bounded SSE buffers (100 events), maximum download lengths. | HTTP 429 Too Many Requests |
| **Correlation Leakage** | Only allowlisted fields cross the projection boundary (`INV-SPEC-DATA-01`); each batch identifies its source by `source_id`. | Unknown fields fail closed at the disclosure boundary |

## Anonymous Read Limits, Retention, and Availability

### Read Rate Limits

- **Bootstrap**: Single snapshot per request.
- **History**: Cursor-paginated; page size defaults to 20 and is capped at 500.
- **SSE Stream**: Bounded to 1,000 concurrent subscribers (`PublicFeedSSEMaxSubscribers`), a 100-event queue per subscriber (`maxSSEQueueSize`), and a replay bound of 100 records (`PublicFeedSSEReplayMaxRecords`).
- **Proof Downloads**: Bounded by byte length in catalog metadata.
- **Client Rate Limit**: 600 requests per minute per IP. Trusted proxy CIDR required for `CF-Connecting-IP`.

### Retention

- **Batches and Records**: Retains up to 25,000 batches and 128 MiB of record payload bytes per source by default (`PublicFeedMirrorRetainedBatches`, `PublicFeedMirrorRetainedRecordBytes`). Whichever limit is hit first advances `retained_from_sequence`.
- **Proof Artifacts**: The active segment-backed catalog has no lifetime artifact-count ceiling. Each signed segment remains bounded by `PublicFeedProofMaxArtifacts`; ingest bodies and individual artifacts retain their existing byte limits. Content-addressed storage persists indefinitely. A withdrawal removes an artifact from the active catalog and public download authorization but keeps its stored bytes.
- **Publisher Outbox**: Unacknowledged batches persist until acknowledged by the mirror.

### Durable Mirror Store

The mirror persists to a SQLite database (`public-mirror/mirror.db`, schema `internal/services/gateway/db/public_mirror_schema.sql`) owned by `PublicMirrorStore`. Proof artifact bytes are stored as content-addressed files under `public-mirror/proofs/<sha256>`; the database holds only artifact metadata and proof manifests. Event records are also indexed by run and assignment (`assignment_event_records`) in the transaction that stores them and pruned with them; the Gateway builds each assignment events database from that index. A store written before the index existed has no rows for its older events, so publishing those assignments fails closed with `ErrPublicAssignmentAuditEventsMissing`.

- **Incremental writes**: Each accepted batch, key registration, revocation, withdrawal, and proof ingest is one SQLite transaction. Retention pruning of the oldest batches and their records runs inside the same transaction as the append, so the retained window and chain anchor (`retained_from_sequence`, `retained_previous_batch_hash`) never diverge from the stored rows.
- **Verified on open**: Every open re-verifies the retained chain (contiguous sequences, `previous_batch_hash` links, `content_hash` recomputation, Ed25519 signatures against the key registry, record hashes against stored record bytes), each proof manifest's root and entries, and each stored artifact file's SHA-256. Any mismatch fails startup with `ErrPublicFeedMirrorStoreCorrupt`.
- **Startup lifecycle**: Verification remains synchronous and gates Gateway readiness and public serving. `OpenPublicMirrorStore` derives a cancellable five-minute deadline (`PublicMirrorVerificationTimeout`) from its caller's context; managed Gateway starts allow that budget in addition to normal health polling. Logs report verification stages, progress counts, and elapsed time. Startup cost still grows with retained signed data and stored proof bytes. A timeout or cancellation fails startup and leaves public content unavailable.
- **Verification working set**: Feed verification pages batch headers and reads record pages across batch boundaries, assembling one batch for signature verification. Proof hashing reuses one buffer across files and observes cancellation between reads. Snapshot verification pages signed batches and projected rows per source; it retains entity identity, sequence, metadata, and authenticated record hashes plus a bounded activity tail. It hashes projected bytes again before comparing them with the signed state. Its comparison map grows with current entity identities, rather than retaining their payloads or all historical batches.
- **Proof ingest**: `POST /proof-ingest` carries one signed segment and its new artifact bytes; a withdrawal carries no artifact bytes. `GET /proof-catalog` is derived from the active entry projection. The mirror also accepts legacy full manifests until a source's first segment arrives. An entry whose artifact is neither in the request nor stored is rejected with HTTP 400 and `missing_artifact_ids`; nothing is committed. Private `GET /proof-root` supplies the delivery position without reading the full manifest. Each accepted segment updates the publisher's small delivery checkpoint. Pruning a run invalidates that checkpoint and appends a signed withdrawal.

## Current-State Snapshot

`/snapshot` describes the accepted feed cursor. `/bootstrap` serves the durable current read model at that cursor. Its `recent_projections` field contains the latest record per entity plus a bounded activity tail, in newest-first order; it is independent of replay-history retention. The Explorer installs that snapshot directly and subscribes to SSE after its cursor.

The mirror owns one durable current read model per source. The evaluation projector remains the only owner of derived run counts, verdict classifications, metrics, verification state, and process-aware run status. The mirror retains and serves those published records; it does not manufacture verdicts from tool activity or missing proof artifacts. Live events describe activity and never overwrite aggregate run fields in the browser.

### Storage and atomicity

- `snapshot_records` keeps the newest accepted record for each catalog, model and role, suite, run, and assignment identity. A separate bounded tail within the snapshot keeps recent activity. Source and dataset identity participate in every index key.
- Each current record references its original signed batch in `snapshot_batches`. `snapshot_heads` records the accepted cursor. Startup validates the current schema and snapshot state without versioned migrations or backfills. The mirror preserves that batch while the current state references it, including after replay-history retention prunes the batch from `/history`. Replacing a record releases its old batch only after no current record references it.
- One SQLite transaction appends accepted feed records, updates the current read model, and advances the snapshot cursor. Snapshot reads capture records, proof summary, withdrawals, and the cursor under the same mutation lock; the lock is released before response encoding or network writes. Failed ingest exposes none of its partial state.
- Snapshot initialization and live deltas use the same identity and replacement rules. Counts and byte totals use deltas; routine publication does not scan all retained artifacts or revisions. Proof bytes remain content-addressed and are fetched on demand.

### Browser contract

`/bootstrap` returns the coherent current records and their source, accepted sequence, chain hash, withdrawal digest, and proof summary. The Explorer can hydrate a validated browser snapshot while revalidating against the configured mirror. Small bootstraps (up to 250 projections) use `localStorage`; larger bootstraps use IndexedDB structured clone. Large snapshots and failed synchronous writes remove the old `localStorage` value, so quota failures cannot leave an obsolete snapshot stuck there. The authoritative snapshot is installed in one store notification and `/stream` opens after its cursor before the new bootstrap is cached. SSE carries accepted deltas through the same record decoder. A replay gap, source change, or withdrawal replaces the snapshot. `/history` remains an evidence-inspection endpoint and does not participate in ordinary page initialization.

The snapshot payload grows with current entities rather than historical revisions. Its activity tail is bounded to `PublicFeedSSEReplayMaxRecords` (100). Assignment projection records retain their published evidence fields; full proof artifact bytes remain on demand. Explorer ingestion retains parsed projection payloads and serializes `record_bytes` only when a consumer asks for it. Campaign envelopes are decoded once, and adapter rollback tracks only the affected assignment and run entries rather than copying the growing context for each record.

The mirror caches both JSON and gzip bootstrap bytes for a coherent source version. Cache validation includes the accepted cursor, chain hash, withdrawal digest, freshness, and proof summary. ETag validation precedes writing a compressed response. Snapshot records are captured under the mutation lock; JSON decoding and response encoding occur after that lock is released. A response built during concurrent ingest remains coherent at its captured cursor and cannot replace a newer cache entry.

### Upgrade and verification

An existing mirror without the current snapshot schema or initialization marker fails closed with `ErrPublicFeedMirrorStoreCorrupt`. There is no in-place migration or history backfill. Archive the disposable mirror projection with its SQLite sidecars while the Gateway is stopped, start with a fresh mirror, and republish from the authoritative evaluation store with `ztd eval runs publish --all` or `ztd public restore --queue --force`. Keep the source signing generation and authoritative evaluation files intact. A rebuild cannot recover evaluation evidence that has already been deleted.

Startup verifies retained snapshot batch signatures, exact record membership and bytes, record identity columns, snapshot cursor, and source binding before serving. A snapshot must never be accepted solely because its unsigned rows contain self-consistent hashes. Withdrawn datasets are excluded from the current read model and subsequent bootstrap responses.

`TestMirror_BootstrapKeepsCurrentStateAcrossRetention` checks pruning and restart, `TestMirror_SnapshotRetainsCurrentEntitiesInsteadOfRevisions` checks compaction and withdrawals, and the snapshot corruption, missing-marker, and rollback tests check fail-closed startup and atomicity. Explorer startup takes one runtime-config request and one bootstrap request before SSE. Browser tests cover Runs values across SSE and snapshot reloads; assignment result publication carries the canonical run summary in the same signed batch for the normal run update.

## Relationship to Platform Architecture

- **Private Ingress**: Private Gateway paths (`/mcp`, `/api/v1/a2a/`, `/api/v1/governance/envelopes`, from `protocol/constants/api_paths.json`) are not mounted on the public read listener (`INV-SPEC-ISO-02`), and `cloudflared` dials only that listener.
- **SSE Bridge**: Gateway SSE (`/api/v1/sse/stream`) is the private Gateway stream; Public SSE (`/stream`) is served by the mirror's public read listener.
- **Tribunal Votes**: Under the `consensus` posture the Gateway publishes each recorded Tribunal vote as a `stage_updated` live event whose `stage_label` is `tribunal vote\n` plus the vote record (`INV-CON-VOTE-12`). Evaluation dispatches propagate explicit evaluation run and assignment IDs into the envelope and public event. The Evaluation Explorer joins votes to rows by exact dataset, run, and assignment identity, rendering that row's Tribunal seats between Model and Latency. Conformance and other unassigned governance votes cannot attach to campaign rows. Event time selects a row's latest voting round; it never determines assignment ownership. Vote events are never table rows.
- **Evaluation Integration**: Native evaluation runs persist under `.ztd/data/eval/runs/` (`EvaluationRunsDirname` under `DataDirname` and `EvaluationDirname`). The projector generates allowlisted projections; the publisher attaches each captured agent trace as a proof artifact (`INV-SPEC-DATA-03`).

## Procedures

### Publishing Campaign Records

```bash
# Publish campaign records via Gateway route
./ztd public publish /path/to/public_records.jsonl

# Inspect public mirror status
./ztd public status
```

### Rotating the Signing Key

```bash
# Generate new key pair and emit signed revocation
./ztd public rotate-key
```

### Offline Proof Verification

For a source publishing segments, export the JSON response as JSONL and download the active artifacts before disconnecting:

```bash
MIRROR_URL="https://mirror.example"
SOURCE_ID="source-id"
curl -fsS "${MIRROR_URL}/proof-head?source=${SOURCE_ID}" > proof-head.json
curl -fsS "${MIRROR_URL}/proof-segments?source=${SOURCE_ID}" | jq -c '.segments[]' > proof-segments.jsonl
curl -fsS "${MIRROR_URL}/proof-catalog?source=${SOURCE_ID}" > proof-catalog.json
mkdir -p proofs
jq -r '.entries[].artifact_id' proof-catalog.json | while IFS= read -r artifact_id; do
  curl -fsS "${MIRROR_URL}/proofs/${artifact_id}" -o "proofs/${artifact_id}"
done
```

Alternatively, copy the publisher's local `public-proofs/proof-segments.jsonl` and its content-addressed artifact files. Disconnect network connectivity, then set `PUBLIC_KEY_HEX` to the publisher's Ed25519 public key (hex), pinned through a trusted channel, and verify:

```bash
./ztd public verify-proof --segments proof-segments.jsonl --artifacts ./proofs --public-key "$PUBLIC_KEY_HEX"
```

For a log spanning key rotations, replace `--public-key` with `--key-registry trusted-keys.json`, where the JSON object maps each signing key ID to a public key hex value pinned through a trusted channel.

The downloaded head identifies the mirror's advertised log position; it does not independently establish freshness. Check that the exported log ends at that advertised index and root when collecting the bundle.

The command checks one source's consecutive indexes and root links from zero, recomputes every segment root, verifies every Ed25519 signature, applies signed withdrawals, and checks active artifact hashes and sizes. It rejects symlinked or non-regular artifact files, repeated artifact IDs, and withdrawals of inactive artifacts. Verification establishes the integrity of the supplied prefix; determining whether the prefix is current requires an independently trusted latest root. Previously downloaded or cached bytes are not erased by a withdrawal.

For sources still publishing full manifests (`segment_index` zero), download `/proof-head`, `/proof-catalog`, and their artifact files. Reconstruct the legacy manifest from the head's manifest metadata and the catalog's entries in response order. Offline, compare every artifact's SHA-256 and size with those entries, recompute the legacy manifest root, and verify its Ed25519 signature with the trusted source key. Do not present a legacy manifest signature as proof of a segment link or withdrawal.

Verify a downloaded agent trace with `./ztd public verify-agent-trace --trace assignment-<id>.agent-trace.json`; it checks the trace's governed tool and model receipts against its actuator anchors and the sentinel ledger bracket. `./ztd public verify-assignment --db assignment-<id>.db --segments proof-segments.jsonl --public-key <ed25519-public-key-hex>` verifies a downloaded assignment events database. Like `verify-proof`, it takes `--public-key` or `--key-registry` and verifies the segment chain first. It then requires the database's SHA-256 to be an active artifact of that chain (`ErrPublicAssignmentAuditNotInProof` otherwise) and verifies the database's live events. The database is plaintext SQLite and has no vault key.

## Anti-patterns

- **Conflating observe modes**: Attempting to use public mirror endpoints for owner-local control, or vice versa (`INV-SPEC-ISO-01`).
- **Redacting or summarizing evaluation evidence**: Screening, truncating, or reconstructing model input, thinking, tool results, or grader detail instead of publishing it verbatim within the size bounds (`INV-SPEC-DATA-03`).
- **Free text in structural fields**: Routing prose through identifiers or labels instead of a verbatim carrier, which bypasses the bounds the structural screen relies on (`INV-SPEC-DATA-02`).
- **Direct public exposure of Gateway**: Pointing external tunnels directly at Gateway port 8443 or 8080 instead of the mirror loopback port 8082.
- **Treating mirror uptime as proof validity**: Relying on mirror availability rather than verifying signed proof artifacts offline.

## Links out

- [SSE Streaming](sse.md): Gateway event publication and browser delivery surfaces.
- [Console Architecture](console.md): Owner-local browser interface and runtime boundaries.
- [Network Architecture](network.md): PKI, mTLS, and transport surfaces.
- [Gateway Architecture](gateway.md): Gateway services, protocol surfaces, and trust boundaries.
- [Evaluation Architecture](evals.md): Model campaign evidence, witness capabilities, and verification.
- [Model Provenance](model-provenance.md): Storage-side weight attestation and chain-of-custody for scored inference.

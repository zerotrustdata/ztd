---
doc_id: public_spectator
title: Public Spectator Architecture and Threat Model
audience: architects and security reviewers
status: current
version: v1.10.0
last_updated: 2026-11-05
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
- [Bucket Layout](#bucket-layout)
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
| INV-SPEC-ISO-02 | The public mirror listener MUST expose only the anonymous read routes (`GET /bootstrap`, `GET /snapshot`, `GET /history`) and public layout (`GET /pointer.json`, `/read-model/`, `/runs/`, `/fixtures/`, `/history/`, `/live/status.json`, `/live/runs/`). The read-only Explorer static host (`GET` and `HEAD`, embedded or from `--eval-explorer-root`) shares this listener. The listener MUST NOT expose internal proof catalogs, metadata, heads, segments, generic proof downloads, or mutation routes. |
| INV-SPEC-ISO-03 | Private ingest operations (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`, `GET /proof-root`) MUST be served on an isolated loopback listener and MUST require the ingest bearer token. |
| INV-SPEC-ISO-04 | The private ingest listener (`PublicMirrorServer.IngestHandler()`) MUST NOT mount anonymous read routes, and the public read listener (`PublicMirrorServer.PublicHandler()`) MUST NOT mount ingest routes. |

### Data Classification and Projection (`INV-SPEC-DATA`)

| ID | Rule |
| --- | --- |
| INV-SPEC-DATA-01 | Every exported record MUST pass a closed allowlist enforced by campaign projector, disclosure validator, publisher, and mirror before becoming browser-visible. Any unknown field fails closed. |
| INV-SPEC-DATA-02 | Every structural feed-record string outside the verbatim carriers (`INV-SPEC-DATA-03`) MUST be at most 512 bytes. Feed-record arrays MUST hold at most 128 entries, except that the per-tool-call activity families `tool_calls`, `policy_decisions`, and `governed_actions` MUST hold at most 1024 records, leaving the 512 KiB record bound to cap them in practice. The Explorer contract enforces the same array bounds. The publisher and mirror MUST NOT screen record or proof-artifact content by field name or string content; public records are published verbatim. |
| INV-SPEC-DATA-03 | Published evaluation records and scenario fixtures MUST NOT be redacted or screened. The feed-record carriers `model_response`, `failure_output`, `role_transcripts`, each grade summary's `detail`, and a live judge event's `judge.rationale` are verbatim text held only to the 512 KiB record bound. A `role_transcripts` value that exceeds its bounds or would push the record past 512 KiB is omitted whole, never truncated. New assignment evidence publications MUST NOT export full agent traces or assignment audit databases as public proof artifacts. Their SHA-256 digests MUST remain in the result's `assignment_agent_trace` and `assignment_audit_slice` evidence bindings, so files delivered under a custom agreement can be checked against them. The campaign-frozen scenario fixture (input and gold criteria) MUST remain public as campaign-scoped proof artifacts bound by `scenario_input_fixture` and `scenario_gold_criteria` evidence bindings. The bucket publisher copies public artifact bytes unchanged, and the layout adds no screening or transformation. Previously published artifacts remain on disk; stopping new exports does not withdraw historical catalog entries. |
| INV-SPEC-DATA-04 | The mirror MUST keep a durable current read model independent of replay-history retention, advance it in the accepted-batch transaction, and serve it coherently through `/bootstrap` and through the layout object `read-model/<sha256>.json.gz` that `pointer.json` names. Explorer run fields MUST come from canonical backend summaries; live activity MUST NOT overwrite them. |
| INV-SPEC-DATA-06 | The layout MUST publish digest-addressed objects before `pointer.json`, and `pointer.json` MUST be written last in each sync. Each pointer names one read model, whose settled-run summaries name their bundle digests, so a reader that follows the pointer never reads a partial set. The bucket publisher MUST upload the referenced objects and then write the pointer, and a failed sync MUST leave the previous pointer in place. The pointer carries the feed signature (`INV-SPEC-CRYPTO-08`). The mirror's public listener serves the same objects with no bucket credentials and no private key. |
| INV-SPEC-DATA-05 | `/bootstrap` MUST NOT carry `PublicAssignmentResultProjection` records. It carries `task_index`, at most `PublicTaskIndexResultLimit` (10) newest results per task holding only the fields the Tasks view renders, and a model summary's `security_pass_rate` replaces any client-side scan of assignment results. Assignment result records stay in `snapshot_records` and reach a browser only through the run's bundle (`INV-SPEC-CRYPTO-07`). |

### Cryptographic Continuity (`INV-SPEC-CRYPTO`)

| ID | Rule |
| --- | --- |
| INV-SPEC-CRYPTO-01 | Every exported batch MUST be an append-only, monotonically sequenced record signed with Ed25519 over the content hash and linked to the predecessor batch's `content_hash` (`previous_batch_hash`). |
| INV-SPEC-CRYPTO-02 | Public immutable objects MUST be content-addressed: `runs/<sha256>.jsonl.gz`, `fixtures/<sha256>`, and `read-model/<sha256>.json.gz`. Read-model digests cover decoded JSON; artifact digests cover stored bytes, including gzip bytes for run bundles. The bucket MUST verify each upload against its supplied digest. The internal proof log MUST NOT be projected to public object keys. |
| INV-SPEC-CRYPTO-03 | Key rotation MUST occur via a signed revocation batch. Batches signed by revoked keys are rejected. Historical batches remain verifiable against the retained key registry. |
| INV-SPEC-CRYPTO-04 | Withdrawing a run MUST preserve signed batches and historical local evidence. The mirror filters withdrawn datasets from read models and history and invalidates retained read models immediately. Signed internal proof withdrawals revoke bundle and fixture serving. The bucket MUST journal and retry object deletion and CDN purges for withdrawn `runs/` and `fixtures/`, prior read models, the pointer, and affected history pages. Clients MUST replace in-memory state when `withdrawal_digest` changes. CDN purges cannot erase bytes already held by a browser or downloaded client. |
| INV-SPEC-CRYPTO-05 | A proof segment MUST bind its source, index, predecessor root, every entry field, withdrawals, timestamp, signing key ID, and verifier instructions to its Ed25519-signed root. The mirror MUST reject gaps, conflicting roots at an accepted index, repeated artifact IDs, unknown or repeated withdrawals, and a legacy manifest signature presented as a segment signature. |
| INV-SPEC-CRYPTO-06 | Pointer-rooted verification MUST authenticate the pointer with an independently trusted Ed25519 key from `--key-registry` or `--public-key`, then check the read-model digest and identity, the selected run summary's bundle digest, and each exact bundle record hash before trusting evidence bindings. Assignment database verification MUST additionally check its bound digest, live-event hashes, and source/run/assignment identity. Self-published keys alone MUST NOT establish trust. The internal signed proof log remains available for explicit offline exports, not anonymous serving. A signed capture MUST NOT be described as proof of freshness or current withdrawal state. |
| INV-SPEC-CRYPTO-07 | A settled run's assignment result records MUST be published as one run bundle proof artifact. The Gateway builds it from the mirror's accepted `snapshot_records` for the run's dataset, never from the run store, so each line's `record_bytes` equal the accepted row byte for byte. The bundle is gzip over JSONL lines of `sequence`, `record_hash`, and `record_bytes` in sequence order, written with no gzip name or modification time so identical records produce identical bytes. Its SHA-256 is its artifact ID and is carried in `evaluation_summary.assignment_bundle_sha256`. An unchanged bundle appends nothing. A changed bundle appends the new artifact and withdraws only the run's previous bundle through a signed withdrawal, leaving other active artifacts unchanged. The mirror serves it as `application/gzip` with no `Content-Encoding`, so the downloaded bytes hash to the bundle digest. |
| INV-SPEC-CRYPTO-08 | `pointer.json` MUST be signed with the feed key of the source that published it, using the `PublicFeedPointer` signing digest. Its `signing_key_id` MUST be the SHA-256 of `signing_public_key`. A reader MUST verify the signature under a key it trusts, as `verify-proof --pointer` does. Bucket credentials never sign, and a pointer served by a mirror without a signer is unsigned and fails `verify-proof --pointer`. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Public mirror durable store | `public-mirror/mirror.db`, `public-mirror/proofs/<artifact-id>` (`internal/services/gateway/public_mirror_store.go`) | `./ztd test integration --pkg ./internal/services/gateway --run 'TestMirror\|TestPublicMirrorStore'` |
| Public feed configuration | `public-feed/` | Directory `0700`, files `0600`; the signing key and ingest token are keystore-sealed |
| Public publisher commands | `internal/cli/cmd/public/` | `./ztd public status`, `./ztd public publish --help` |
| Public layout source and objects | `internal/services/gateway/public_layout.go`, `internal/services/gateway/public_mirror_layout.go`, `internal/models/public_feed_layout.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestPublicMirrorLayout` |
| Bucket publisher, journal, and client | `internal/services/gateway/public_bucket_publisher.go`, `public_bucket_journal.go`, `public_bucket_client.go` | `./ztd test integration --pkg ./internal/services/gateway --run 'TestBucketPublisher_\|TestBucketJournal_\|TestPublicBucket'` |
| Disclosure validation | `internal/services/publicdisclosure/validator.go` | `./ztd test unit --pkg ./internal/services/publicdisclosure` |
| Public spectator runtime | `internal/services/gateway/public_spectator_runtime.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestPublicSpectator` |
| Run bundles | `internal/services/gateway/public_run_bundle.go`, `internal/services/gateway/public_mirror_run_bundle_store.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestRunBundle_` |
| Bootstrap read model and task index | `internal/services/gateway/public_mirror_task_index.go` | `./ztd test integration --pkg ./internal/services/gateway --run TestPublicMirrorBootstrap` |
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
| • Reaches private Gateway APIs    |        | • Reaches public read listener    |
| • Full audit, receipts, approvals |        | • Closed allowlist projections    |
+-----------------------------------+        +-----------------------------------+
```

- **Owner-Local Observe Mode**: An authenticated operator connects to the private Gateway (`https://<gw>:8443/console/`). The browser authenticates via WebAuthn passkeys, receiving a secure session cookie.
- **Public Spectator Mode**: Anonymous internet visitors connect over HTTP directly to the public mirror listener (`0.0.0.0:8082` on cloud VM deployments with `--public-spectator-allow-container-bind`), through an external domain routed via a `cloudflared` tunnel or reverse proxy, or to the bucket's public origin, where the Explorer reads the layout. No credentials are sent, and these requests reach only public read objects, not the private Gateway API.

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
                                         (GET /bootstrap, /snapshot, /history, layout)
                                                      ▲
                                                      │ direct TCP :8082 or loopback tunnel
                                                      │
                                               Public Browser & Probes

 PublicSpectatorRuntime ──► PublicBucketPublisher (in-process read of PublicLayoutSource)
                                    │ digest objects first, pointer.json last (signed)
                                    ▼
                          S3-compatible bucket ──► custom domain + Cache Rule ──► Public Browser
```

1. **Private Ingest Listener**: Serves ingest routes only (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`, `GET /proof-root`), each of which requires the ingest bearer token. It is built from `PublicMirrorServer.IngestHandler()` and mounts no anonymous read route: a read path such as `GET /snapshot` returns 404 here. It is unconditionally restricted to loopback (`127.0.0.1`, `localhost`, `::1`) per `INV-SPEC-ISO-03`.
2. **Public Read Listener**: `PublicMirrorServer.PublicHandler()` serves anonymous `GET /bootstrap`, `/snapshot`, `/history`, and the public pointer, read model, bundle, fixture, history, and live layout. It mounts neither ingest/mutation routes nor internal proof metadata/log/download routes. JSON listener routes accept gzip encoding. Read models and history objects have `Content-Encoding: gzip`; settled and live run bundles use `application/gzip` without encoding, preserving their stored bytes. `/history` supports indexed `kind` and `dataset` filters before pagination. The Explorer static host supports `GET` and `HEAD` on this listener. Removed anonymous proof routes return 404.

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
- **Verbatim Model and Grader Text (Optional Extensions)**: `model_response`, `failure_output`, `role_transcripts`, and `benchmark_observations.grade_summaries[].detail` / `semantic_grade_summaries[].detail` (each text within the 512 KiB record bound, at most 8 role transcripts, no screening or redaction).
- **Agent Trace Digest**: The `assignment_agent_trace` evidence binding names the SHA-256 of the persisted `AgentTrace` body byte for byte. New publications hash the trace without placing it in the public artifact store. The Explorer offers access under a custom agreement. The trace carries the exact first-turn model input (`input.messages`, `input.tools`) and, for held-context repetitions, `held_context`, the earlier repetition's conversation. `agent-trace.v9` traces added ordered harness-owned `observations` for seeded tool outcomes, final file-state checks, and governed probes, separate from scored model calls. Every governed tool call, whether model-selected or harness-owned, carries its canonical admitted `request_envelope`, bound to the signed receipt. A pre-envelope L1 refusal carries only its refusal code and is not a signed receipt. The sentinel bracket verifier requires seed observations before agent activity and final file-state/probe observations afterward. `agent-trace.v8` added `tool_calls[].approved_arguments_json`, the approved arguments a tool call executed (`AgentToolCall`, `internal/services/evaluation/campaign_agent_trace.go`). `agent-trace.v10` adds `content_judgments`, one entry per judged content criterion, with the exact judge messages, the signed inference result and receipt, `met`, and `rationale` (`AgentContentJudgment`). `agent-trace.v11` records the frozen persona (ID, version, digest) that the designated role ran under. The judge messages contain each criterion's statement, so the statement is part of the persisted trace body. The public criteria list does not use the statement: each judged criterion is published as `content:<criterion id>` with the generic label "Judged answer requirement" and a description of the pinned judge's decision (`scenario_public_criteria.go`). See [Evaluations](evals.md#built-in-benchmark-inventory-and-scope) for how verdicts are graded.
- **Scenario Fixture Proof Artifacts**: `scenario-<id>.input.json` (`ScenarioInputFixture`: user prompt, inline context, workspace files including decoys, seeded investigation) and `scenario-<id>.gold.json` (`ScenarioGoldCriteria`), the campaign-frozen canonical bytes, so each SHA-256 equals the campaign's fixture reference. They are served at `/fixtures/<sha256>` and bound by `scenario_input_fixture` and `scenario_gold_criteria` evidence bindings, which appear together or not at all. Their catalog entries carry no source run because every run of the campaign shares them, so one run's prune never removes them.
- **Assignment events database Digest**: The `assignment_audit_slice` evidence binding names the SHA-256 of a plaintext SQLite database of the assignment's public live-event records. The Gateway builds it from the live event records it retains, indexed per assignment (`assignment_event_records`), hashes it, and removes the scratch database without writing a public artifact. A repeated build from the same events produces the same digest. An existing root reuses a database digest already active for that run without rebuilding it or listing the artifact in another segment. Before any proof is reused or requested, the publisher delivers every pending assignment's live event records, because the Gateway builds proofs only from records it was sent. Result publication ingests the evidence digests before it publishes the assignment result record. The publisher binds the `database_sha256` the Gateway returns and fails closed on an answer for another assignment or an empty digest. There is no vault key and no key artifact. The Explorer offers the database under a custom agreement; its evidence binding, rather than a new artifact entry, carries the digest.
- **Run Bundle Proof Artifact**: `<run-id>.assignments.jsonl.gz`, the run's assignment result records as gzip JSONL (`INV-SPEC-CRYPTO-07`), served at `/runs/<sha256>.jsonl.gz` as `application/gzip` and named by `evaluation_summary.assignment_bundle_sha256`. Its catalog entry carries the run as its source run, so pruning the run withdraws it with the run's other artifacts.

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

The mirror accepts a typed `PublicProofManifestSegment` on private `/proof-ingest` and keeps its signed log internal; no anonymous route serves it. Segment indexes start at 1, and the first `previous_root` is the zero SHA-256 hash. Each later segment links to the preceding signed root. Exact root replays are idempotent. New segments append their entries or withdrawal IDs; withdrawals remove that source's entries from the active catalog and download lookup while preserving signed history and stored bytes. An artifact remains downloadable if another source still lists the same content ID. A withdrawn content ID cannot be added again to the same source's segment chain.

`models.ComputePublicProofSegmentRoot` defines the signed encoding. Each value is UTF-8 bytes prefixed by its decimal byte length and `:`. The hash is SHA-256 of the concatenated fields, in this order:

1. Domain `ztd-public-proof-segment-v1`, schema version, source ID, decimal segment index, previous root (lowercase hex), campaign ID, campaign revision, verified index-generation hash, verification boolean (`true` or `false`), decimal entry count.
2. For each entry in order: artifact ID, SHA-256, filename, media type, decimal byte size, classification, campaign ID, source run ID, generated-at timestamp, verification command, immutable URL.
3. Decimal withdrawal count, each withdrawn artifact ID in order, segment generated-at timestamp, signing key ID, verifier instructions.

Timestamps use UTC RFC 3339 with trailing fractional zeroes omitted (`time.RFC3339Nano`). The signature covers the decoded 32-byte root. The root, the signature, and `signing_public_key` are excluded from the hash. `signing_key_id` is the SHA-256 of that public key and is inside the root, so a different key does not verify. Excluding the key keeps segments signed before the field existed valid. Entry metadata and adjacent variable-length fields therefore cannot be substituted without invalidating the signature.

Ingest verifies only the incoming segment and its inline artifact bytes. `PublicFeedProofMaxArtifacts` bounds the entries plus withdrawals in one signed segment (12,288), or the artifact count in one legacy full manifest. It does not limit the accumulated segment-backed catalog. This keeps each ingest bounded while allowing a long-lived source to publish additional assignments without withdrawing historical evidence. Reused artifacts require an existing metadata row and a regular file of the verified size. Indexed chain-position reads and count/byte deltas keep mutations proportional to the incoming entries and withdrawals. Startup re-verifies every signature, chain link, artifact hash, indexed chain column, entry projection, withdrawal projection, and source proof summary before serving requests.

The SQLite upgrade retains existing full-manifest rows at segment index zero and preserves their actual signatures. It does not manufacture signed segments. A source can replace that projection by sending its own first signed segment with the initial artifact set. Once the mirror accepts a segment for a source, it rejects full-manifest submissions for that source.

Internal proof metadata stays in the mirror store. Anonymous proof catalog, head, segment, and generic body routes are removed. Private `GET /proof-root` remains the authenticated, constant-size delivery checkpoint.

The publisher appends signed links to `public-proofs/proof-segments.jsonl` and sends each pending segment with that segment's new artifact bytes. Private `GET /proof-root` returns the accepted segment index, root, and active artifact count. Delivery checks that head against the local log and sends only the remaining suffix; a divergent or advanced head fails closed. Local sync state checkpoints accepted segments but never suppresses checking the mirror, so delivery can recover after mirror replacement or a lost acknowledgement. A legacy publisher package is explicitly cut over to a source-signed genesis segment before its next push or withdrawal. Publisher manifest responses and the private mirror proof head carry the actual segment root and signature with a nonzero `segment_index`.

Withdrawal preserves the publisher's immutable artifact files so an unsent prefix can still be delivered to a new mirror. The signed withdrawal removes those artifacts from the active catalog and public bundle and fixture serving authorization. Re-adding a withdrawn content ID to that source is rejected before appending to the local log.

The publisher keeps an in-memory index of the log: the byte offset and root of every segment, the active catalog, and every content ID ever listed. The first read replays and verifies the whole log once. After that, each access stats the file and verifies only bytes past the indexed offset, so appends by another process are picked up. A log that was replaced or shrank is replayed from the start, and nothing is appended to a log that does not verify. Every append verifies the new segment against the chain rules the mirror enforces (consecutive link, no relisting of a listed ID, withdrawals of active IDs only, and per-segment bounds), so a link the mirror would reject is never written. Appends hold the `public-proofs/proof-segments.lock` file lock across processes; a held lock fails the publication with `ErrRuntimeFileLocked`. A final line with no newline is an interrupted append. Reads ignore it, and the next append truncates it before writing. Delivery reads only the byte range of the pending segments. A publication's cost depends on its new entries, not on retained history; `TestPublisherProofLog_PublicationCostIsFlatInHistory` guards allocation, log growth, and push body size.

The proof log, its artifacts, and `proof-mirror-sync.json` belong to the feed generation of the key that signed them. Gateway startup reads an existing valid configuration without rewriting it, or supplies the gateway's default source and private mirror origin before explicitly creating a new feed. The CLI's `public init` is also an explicit create operation: it requires a source ID and mirror origin, rejects any existing feed generation or orphaned publisher state, and rolls back successfully written files if a later write fails. Both paths use `InitializeLocalPublicFeed` for generation creation. It mints a new signing key only when no generation path exists. If publisher state such as an outbox, proof log, or sync checkpoint remains without its matching generation configuration, initialization fails with `ErrPublicFeedOrphanedGeneration`, because data signed by a deleted key can never verify. During explicit initialization, any existing configuration, signing key, or ingest token fails with `ErrPublicFeedConfigExists`. During startup, a valid configuration with a missing signing key or ingest token fails with `ErrPublicFeedSigningKeyRequired` or `ErrPublicFeedIngestTokenRequired`. `public source transition` archives the whole generation, including the sync checkpoint, under `public-feed-archive/generations/<source_id>/`.

Public offline verification uses the signed pointer and run bundle with independently trusted keys. Explicit exports of the internal proof log can still use `verify-proof --segments`; that mode authenticates publisher identity only when a trusted key is supplied. Existing signatures and their embedded instructions remain unchanged.

## Threat Model and Mitigations

| Threat Vector | Mitigation Strategy | Fail-Closed Behavior |
| :--- | :--- | :--- |
| **Replay & Reordering** | Batches carry contiguous sequence ranges and link to the prior batch by `previous_batch_hash`. The mirror rejects a batch at or below its high-water sequence unless it re-delivers an accepted batch with the same sequence range and content hash, and rejects a broken link. | HTTP 200 with `accepted: false` and a typed `rejection_reason`: `sequence_out_of_order`, `duplicate_sequence`, or `hash_chain_mismatch` |
| **Equivocation** | Content hashes bind each batch to its exact position. A batch that reuses an accepted sequence range with a different content hash is equivocal. | HTTP 200 with `accepted: false` and `rejection_reason` `equivocation`; ingest revokes no key (revocation requires a signed revocation batch, `INV-SPEC-CRYPTO-03`) |
| **Stale Snapshots** | Snapshots bind high-water mark and chain hash. Clients verify freshness labels (`active`, `delayed`, `stale`, `intentionally_stopped`, `safety_stopped`, `source_offline`). | Stale data explicitly tagged, not served as live |
| **Forged Events** | Batches signed with Ed25519. Mirrors and offline verifiers validate signatures over canonical content hashes. | Bad signatures rejected with `accepted: false` and `rejection_reason` `signature_invalid`; unknown or revoked signing keys rejected with `unknown_key` or `revoked_key` |
| **Mirror Tampering** | Read models, run bundles, and fixtures are content-addressed by SHA-256. The signed pointer names the read model, whose settled-run summaries name their bundle digests. Pointer-rooted `verify-proof` checks the pointer signature only under a key from `--key-registry` or `--public-key` (`INV-SPEC-CRYPTO-06`). | Hash or signature mismatch fails `verify-proof`; a pointer signed by an untrusted key fails |
| **Cache Poisoning** | Immutable HTTP cache headers (`Cache-Control: public, max-age=31536000, immutable`) on digest-named objects. The Explorer hashes each downloaded read model, bundle, and fixture against its digest, and `verify-proof` checks the read model and bundle against the authenticated pointer. | A digest mismatch is a load error in the Explorer and fails `verify-proof` |
| **Path Traversal** | Proof ingest rejects filenames that are absolute, contain backslashes or `..`, or are not plain base names (`safePublicProofFilename`). The mirror serves artifacts by artifact ID from its store. The publisher refuses symlinked proof artifacts (`ErrPublicFeedProofSymlinkRejected`), and the offline verifier rejects non-regular artifact files. | Proof ingest rejected with HTTP 400; download of an unknown artifact ID returns HTTP 404 |
| **Denial of Service** | Strict rate limits (600 req/min per IP), maximum download lengths. | HTTP 429 Too Many Requests |
| **Correlation Leakage** | Only allowlisted fields cross the projection boundary (`INV-SPEC-DATA-01`); each batch identifies its source by `source_id`. | Unknown fields fail closed at the disclosure boundary |

## Anonymous Read Limits, Retention, and Availability

### Read Rate Limits

- **Bootstrap**: Single snapshot per request.
- **History**: Cursor-paginated; page size defaults to 20 and is capped at 500.
- **Artifact Downloads**: `runs/` and `fixtures/` bodies are bounded by the byte size recorded in the internal catalog entry and are hash-checked before serving.
- **Client Rate Limit**: 600 requests per minute per IP. Trusted proxy CIDR required for `CF-Connecting-IP`.
- **Layout Reads**: A browser reads `pointer.json` (cache 30 seconds), then the immutable read model it names, and reads `live/status.json` (cache 10 seconds) while a run is active. Each object's `Cache-Control` is listed under [Bucket Layout](#bucket-layout). The Cloudflare Cache Rule must honor origin `Cache-Control` for the bucket host, or JSON and extensionless paths miss the cache and every poll is a bucket read.

### Retention

- **Batches and Records**: Retains up to 25,000 batches and 128 MiB of record payload bytes per source by default (`PublicFeedMirrorRetainedBatches`, `PublicFeedMirrorRetainedRecordBytes`). Whichever limit is hit first advances `retained_from_sequence`.
- **Proof Artifacts**: The active segment-backed catalog has no lifetime artifact-count ceiling. Each signed segment remains bounded by `PublicFeedProofMaxArtifacts`; ingest bodies and individual artifacts retain their existing byte limits. Content-addressed storage persists indefinitely. A withdrawal removes an artifact from the active catalog and public bundle and fixture serving authorization but keeps its stored bytes.
- **Publisher Outbox**: Unacknowledged batches persist until acknowledged by the mirror. `public-feed/outbox.jsonl` is an append-only journal owned by `runtimePublicOutboxStore`: a line carrying `batch_bytes` adds an entry, and a line without it records a status transition of the entry with the same sequence and batch hash. The store keeps the applied entries in memory, so publishing a batch costs what it adds rather than the size of the outbox. It reads only lines another process appended, and discards an incomplete tail left by an interrupted append. Acknowledged entries older than `ack_confirmation_window_seconds` leave the view at once; the journal is compacted once pruned bytes outweigh the live ones.
- **Bucket Layout**: The bucket keeps selected run bundles and scenario fixtures from the internal active catalog. An hourly sweep deletes unnamed read models older than one hour and history pages wholly below `history_first_sequence`. Withdrawals delete and purge the affected artifacts and prior read models without grace, purge the pointer, and rewrite/purge affected history pages. Live run objects are deleted when the run ends. Internal proof metadata and logs have no public projection.

### Durable Mirror Store

The mirror persists to a SQLite database (`public-mirror/mirror.db`, schema `internal/services/gateway/db/public_mirror_schema.sql`) owned by `PublicMirrorStore`. Proof artifact bytes are stored as content-addressed files under `public-mirror/proofs/<sha256>`; the database holds only artifact metadata and proof manifests. Event records are also indexed by run and assignment (`assignment_event_records`) in the transaction that stores them and pruned with them; the Gateway builds each assignment events database from that index. A store written before the index existed has no rows for its older events, so publishing those assignments fails closed with `ErrPublicAssignmentAuditEventsMissing`. Every record is also indexed by its top-level `kind` and `dataset_id` (`record_filters`) in the same transaction and pruned with it; filtered `/history` reads seek that index instead of decoding every retained record. Opening a store indexes any retained record that has no `record_filters` row, so a dataset already in the mirror never reads as absent to the publisher's mirror probe.

- **Incremental writes**: Each accepted batch, key registration, revocation, withdrawal, and proof ingest is one SQLite transaction. Retention pruning of the oldest batches and their records runs inside the same transaction as the append, so the retained window and chain anchor (`retained_from_sequence`, `retained_previous_batch_hash`) never diverge from the stored rows.
- **Verified on open**: Every open re-verifies the retained chain (contiguous sequences, `previous_batch_hash` links, `content_hash` recomputation, Ed25519 signatures against the key registry, record hashes against stored record bytes), each proof manifest's root and entries, and each stored artifact file's SHA-256. Any mismatch fails startup with `ErrPublicFeedMirrorStoreCorrupt`.
- **Startup lifecycle**: Verification remains synchronous and gates Gateway readiness and public serving. `OpenPublicMirrorStore` derives a cancellable five-minute deadline (`PublicMirrorVerificationTimeout`) from its caller's context; managed Gateway starts allow that budget in addition to normal health polling. Logs report verification stages, progress counts, and elapsed time. Startup cost still grows with retained signed data and stored proof bytes. A timeout or cancellation fails startup and leaves public content unavailable.
- **Verification working set**: Feed verification pages batch headers and reads record pages across batch boundaries, assembling one batch for signature verification. Proof hashing reuses one buffer across files and observes cancellation between reads. Snapshot verification pages signed batches and projected rows per source; it retains entity identity, sequence, metadata, and authenticated record hashes plus a bounded activity tail. It hashes projected bytes again before comparing them with the signed state. Its comparison map grows with current entity identities, rather than retaining their payloads or all historical batches.
- **Proof ingest**: `POST /proof-ingest` carries one signed segment and its new artifact bytes; a withdrawal carries no artifact bytes. The internal active entry projection authorizes only selected bundles and fixtures. The mirror also accepts legacy full manifests until a source's first segment arrives. An entry whose artifact is neither in the request nor stored is rejected with HTTP 400 and `missing_artifact_ids`; nothing is committed. Private `GET /proof-root` supplies the delivery position without reading the full manifest. Each accepted segment updates the publisher's small delivery checkpoint. Pruning a run invalidates that checkpoint and appends a signed withdrawal.

## Bucket Layout

When the Gateway's export configuration names a bucket, `PublicBucketPublisher` (`internal/services/gateway/public_bucket_publisher.go`) copies the colocated mirror's public layout to an S3-compatible bucket that is served from a custom domain. The publisher reads the layout through `PublicLayoutSource`, which `PublicMirrorServer` implements (`internal/services/gateway/public_layout.go`, `public_mirror_layout.go`). The mirror's public listener serves the same objects with the same names, generated with no bucket credentials and no private key. The colocated publisher signs each pointer, and the mirror does not hold a signing key.

### Objects

| Object | Key | Cache-Control | Written |
| :--- | :--- | :--- | :--- |
| Pointer | `pointer.json` | `public, max-age=30` | last in each sync, signed with the feed key |
| Read model | `read-model/<sha256>.json.gz` | `public, max-age=31536000, immutable` | each sync, gzip, digest of uncompressed JSON |
| Settled run bundle | `runs/<sha256>.jsonl.gz` | immutable | once per selected artifact, digest of stored gzip bytes checked by the bucket |
| Scenario fixture | `fixtures/<sha256>` | immutable | once per selected artifact, digest of canonical JSON checked by the bucket |
| History page | `history/<first_sequence>.json.gz` | `public, max-age=3600` | when no run is active or a withdrawal lands; 1,000 sequences per page, same withdrawal filter as `/history` |
| Live status | `live/status.json` | `public, max-age=10` | when its content changes, while a run is active or stops |
| Live run | `live/runs/<run>.jsonl.gz` | `public, max-age=60` | at most once a minute, executing runs only |

Read models and history pages carry `Content-Encoding: gzip`. Selected artifacts are stored as their own bytes. Settled and live run objects are `application/gzip` with no encoding, so a downloaded live run is the gzip file itself.

Layout objects carry `Access-Control-Allow-Origin: *`, because the Explorer reads them cross-origin from its own host. The public read listener sends that header on its anonymous read routes. The bucket's custom domain must send the same header through a response rule, and the bucket has no CORS policy of its own. Setup is in [Publish the layout to a bucket](../guides/public_spectator.md#publish-the-layout-to-a-bucket).

The internal proof log stores bundles and fixtures and controls their withdrawals. Only UI artifact bodies are public. Pointer schema `2.0.0` removes the catalog digest, proof root, and segment head. Read-model schema `2.0.0` adds an explicit `schema_version` and removes `proof_catalog_summary`; the signed feed protocol and Explorer record schemas remain separate. Old roots retain historical proof directories and metadata; startup never deletes them.

### Sync Order

Each sync uploads every object the next pointer names before it writes `pointer.json`:

1. Selected run bundles and scenario fixtures, with eight upload workers.
2. History pages, when due.
3. The read model.
4. `pointer.json`, signed by the publisher, with a revision above the previous one.

Only after the pointer is written does the publisher record the new pointer in its journal, and only then does it queue deletes and purges for objects the pointer no longer names. A failed step leaves the previous pointer in place, and the next tick retries the sync.

### Cadence

- The publisher checks the mirror's head every 10 seconds.
- `live/status.json` is written only when its content changes. The `generated_at` field does not count as a change.
- A sync runs at once on the first sync, when the set of active runs changes, and when a withdrawal lands. A run starting, a run ending while another continues, and the last run settling each change the set. A run moving from `queued` to `running` does not.
- While a run is active, the pointer and the read model hold still, so open pages do not download the read model again. Otherwise a sync runs at most once a minute, and only when the head has changed. During a run that means records of a dataset that is not executing, such as a republished verification. Records with no dataset wait for the next sync.
- Selected run bundles and scenario fixtures are uploaded in each sync. While the pointer holds, proofs that joined the catalog are uploaded at most once a minute without a sync, so a live assignment's proofs resolve before the run settles. An artifact leaving the catalog during a run counts as a withdrawal and syncs.
- History pages catch up when the active set changes, no run is active, or a withdrawal lands. The Explorer resolves fixture paths directly from evidence-binding digests; no proof metadata is public.
- Live run objects are refreshed at most once a minute, for each active run, and a settled run's object is deleted.

A run is active when its latest summary is `queued` or `running` and its source is neither offline nor stopped. A run left unsettled stops counting once its source goes quiet, because its status becomes `source_offline` and the layout reports no active runs.

The local public listener composes its pointer on each request. It retains recently advertised read models for one minute, bounded globally at 128 objects and 64 MiB of compressed bodies, so ingest between a browser's pointer and read-model requests does not immediately invalidate the advertised digest. Capacity pressure evicts the oldest objects. Retained objects remain source-scoped. A committed dataset or artifact withdrawal clears retention immediately and invalidates builds that started before it; a full-manifest replacement does the same. Retention is in memory and ends on restart. The bucket's object sweep uses its separate one-hour grace.

### Explorer Lag

The Explorer polls `pointer.json` about once a minute while the tab is visible. During a run the pointer holds still, so the poll costs one small request and the read model is not downloaded again until the active run set changes. A repeat visit loads the read model from the browser's HTTP cache, because its path is digest-named and immutable. It polls `live/status.json` about every 10 seconds, only while the pointer reports `active`. Once a run is active, live activity reaches a visitor up to about 30 seconds after the mirror accepts it: up to 10 seconds for the publisher's head check, up to 10 seconds of status cache, and up to 10 seconds of Explorer poll. When a run starts, the first live poll waits for a pointer that reports `active`. The publisher writes that pointer within 10 seconds of the run's first record, so it reaches a visitor after up to 30 seconds of cache and up to a minute of pointer polling. The live run object can lag the mirror by up to a minute, plus its 60-second cache.

### Withdrawal

A withdrawal deletes the run's `runs/` and `fixtures/` objects, and every read model that the new pointer does not name. The publisher journals each delete and each purge of the public URL, then retries them until they succeed. A purge clears CDN copies only, so bytes a browser or client already has are unaffected. The pointer is purged in the same step, and a dataset withdrawal also purges its history pages.

### Publisher State and Credentials

`public-feed/bucket-journal.jsonl` holds the last pointer that reached the bucket and the pending deletes and purges. Uploaded objects are not journaled, because the bucket's listing is the authority for what is present. `public-feed/bucket-credentials` holds the bucket write and cache-purge credentials, sealed beside the ingest token. Neither file is in the export configuration. See [Storage Architecture](storage.md#public-feed-state).

### First Sync

The first sync lists the bucket's `runs/` and `fixtures/` prefixes and resumes what is present. It deletes run bundles and fixtures that have left the selected active set. The body allowlist uses `PublicRunBundleFilenameSuffix` and `scenario-*.input.json` / `scenario-*.gold.json`; historical traces, audit databases, and other artifact kinds are neither opened nor uploaded. The publisher does not list or copy the old `proofs/` prefix, or delete historical local proof directories. A bucket therefore serves one feed source, and it is not shared with other data.

## Current-State Snapshot

`/snapshot` describes the accepted feed cursor. `/bootstrap` serves the durable current read model at that cursor. Its `recent_projections` field contains the latest record per entity, except assignment results (`INV-SPEC-DATA-05`), plus a bounded activity tail, in newest-first order; it is independent of replay-history retention. Its `task_index` field lists each task's newest results. The Explorer reads the same read model through the layout: `pointer.json` names `read-model/<sha256>.json.gz`, and the Explorer installs it directly.

`/snapshot?dataset=<dataset-id>` answers whether the current read model holds a catalog snapshot of that dataset, independent of replay-history retention. It returns the snapshot with `dataset_id` echoed, or 404 when the dataset is absent or withdrawn. `ztd public restore` uses this lookup to decide which runs are missing, so a restored run stays present after its records age out of `/history`. A mirror that does not echo `dataset_id` reads as absent, and the run is republished.

The mirror owns one durable current read model per source. The evaluation projector remains the only owner of derived run counts, verdict classifications, metrics, verification state, and process-aware run status. The mirror retains and serves those published records; it does not manufacture verdicts from tool activity or missing proof artifacts. Live events describe activity and never overwrite aggregate run fields in the browser.

### Storage and atomicity

- `snapshot_records` keeps the newest accepted record for each catalog, model and role, suite, run, and assignment identity. A separate bounded tail within the snapshot keeps recent activity. Source and dataset identity participate in every index key.
- Each current record references its original signed batch in `snapshot_batches`. `snapshot_heads` records the accepted cursor. Startup validates the current schema and snapshot state without versioned migrations or backfills. The mirror preserves that batch while the current state references it, including after replay-history retention prunes the batch from `/history`. Replacing a record releases its old batch only after no current record references it.
- One SQLite transaction appends accepted feed records, updates the current read model, and advances the snapshot cursor. Snapshot reads capture records, withdrawals, and the cursor under the same mutation lock; the lock is released before response encoding or network writes. Failed ingest exposes none of its partial state.
- Snapshot initialization and live deltas use the same identity and replacement rules. Counts and byte totals use deltas; routine publication does not scan all retained artifacts or revisions. Proof bytes remain content-addressed and are fetched on demand.

### Browser contract

`/bootstrap` returns the coherent current records and their source, accepted sequence, chain hash, and withdrawal digest. The Explorer keeps no browser copy of the snapshot: HTTP caching of the bootstrap response is the only cache, and startup deletes the earlier IndexedDB database `opendevops.feed.cache.v1` and the `localStorage` key `opendevops.feed.bootstrap.v1`. The authoritative snapshot is installed in one store notification. After that the Explorer polls `pointer.json` about once a minute while the tab is visible, and polls `live/status.json` about every 10 seconds (every 2 seconds on a loopback mirror) only while the pointer reports `active`. A pointer with a new `read_model_sha256` replaces the snapshot. The Explorer keeps no SSE connection for the public feed. The platform SSE bridge (`docs/architecture/sse.md`) is a separate Gateway stream and is unchanged.

Assignment results load on demand. A view for one run fetches that run's bundle from `/runs/<assignment_bundle_sha256>.jsonl.gz`, checks the SHA-256 of the downloaded bytes against the digest, decompresses it with `DecompressionStream`, and ingests each line's `record_bytes` through the same record decoder as the feed. Concurrent requests for a run share one fetch, a bounded least-recently-used set of runs stays in memory except for runs a mounted view holds, and a withdrawal change clears it. A queued or running run has no `assignment_bundle_sha256`; its assignment results come from `live/runs/<run>.jsonl.gz`, a mutable object the Explorer reads on each request. That object can lag the mirror by up to a minute plus its 60-second cache. A run with neither a digest nor an executing state has no bundle. A digest mismatch is a load error and is never ingested. The Tasks view reads `task_index` instead of a bundle, and the Leaderboard reads each model summary's `security_pass_rate` instead of scanning assignment results. A replay gap, source change, or withdrawal replaces the snapshot. `/history` remains an evidence-inspection endpoint and does not participate in ordinary page initialization.

The snapshot payload grows with current entities rather than historical revisions. Its activity tail is bounded to `PublicFeedEventRetentionMaxRecords` (100). Assignment projection records retain their published evidence fields in the run bundle; full proof artifact bytes remain on demand. Explorer ingestion retains parsed projection payloads and serializes `record_bytes` only when a consumer asks for it. Campaign envelopes are decoded once, and adapter rollback tracks only the affected assignment and run entries rather than copying the growing context for each record.

The mirror caches both JSON and gzip bootstrap bytes for a coherent source version. Cache validation includes the accepted cursor, chain hash, withdrawal digest, freshness, and the withdrawal version. ETag validation precedes writing a compressed response. Snapshot records are captured under the mutation lock; JSON decoding and response encoding occur after that lock is released. A response built during concurrent ingest remains coherent at its captured cursor and cannot replace a newer cache entry.

### Upgrade and verification

An existing mirror without the current snapshot schema or initialization marker fails closed with `ErrPublicFeedMirrorStoreCorrupt`. There is no in-place migration or history backfill. Archive the disposable mirror projection with its SQLite sidecars while the Gateway is stopped, start with a fresh mirror, and republish from the authoritative evaluation store with `ztd eval runs publish --all` or `ztd public restore --queue --force`. Keep the source signing generation and authoritative evaluation files intact. A rebuild cannot recover evaluation evidence that has already been deleted.

A run published before run bundles has no `assignment_bundle_sha256` and its model summaries carry no `security_pass_rate`, so the Explorer shows no assignment details for it, and its Primary and Assistant model summaries do not qualify for the Leaderboard. `ztd eval runs publish --all` republishes the summaries, which publishes each settled run's bundle first. Records already in the mirror are not rewritten: the bundle is built from them.

Startup verifies retained snapshot batch signatures, exact record membership and bytes, record identity columns, snapshot cursor, and source binding before serving. A snapshot must never be accepted solely because its unsigned rows contain self-consistent hashes. Withdrawn datasets are excluded from the current read model and subsequent bootstrap responses.

`TestMirror_BootstrapKeepsCurrentStateAcrossRetention` checks pruning and restart, `TestMirror_SnapshotRetainsCurrentEntitiesInsteadOfRevisions` checks compaction and withdrawals, and the snapshot corruption, missing-marker, and rollback tests check fail-closed startup and atomicity. Explorer startup takes one runtime-config request, then one pointer request and one read model request. Browser tests cover Runs values across live polls and snapshot reloads; assignment result publication carries the canonical run summary in the same signed batch for the normal run update.

## Relationship to Platform Architecture

- **Private Ingress**: Private Gateway paths (`/mcp`, `/api/v1/a2a/`, `/api/v1/governance/envelopes`, from `protocol/constants/api_paths.json`) are not mounted on the public read listener (`INV-SPEC-ISO-02`), and `cloudflared` dials only that listener.
- **SSE Bridge**: Gateway SSE (`/api/v1/sse/stream`) is the private Gateway stream, documented in [SSE Streaming](sse.md). The public mirror has no SSE route. The Explorer and the `ztd-adapter` public client read the layout.
- **Tribunal Votes**: Under the `consensus` posture the Gateway publishes each recorded Tribunal vote as a `stage_updated` live event whose `stage_label` is `tribunal vote\n` plus the vote record (`INV-CON-VOTE-12`). Evaluation dispatches propagate explicit evaluation run and assignment IDs into the envelope and public event. The Evaluation Explorer joins votes to rows by exact dataset, run, and assignment identity, rendering that row's Tribunal seats between Model and Latency. Conformance and other unassigned governance votes cannot attach to campaign rows. Event time selects a row's latest voting round; it never determines assignment ownership. Vote events are never table rows.
- **Judge Activity**: A campaign executed with publication publishes each pinned-judge inference as a `judge_started` event when its request is built and a `judge_completed` event when it returns (`CampaignPublicationCoordinator.NewGradingActivityPublisher`). The typed `judge` block (`PublicLiveJudgeActivity`) names the judge's variant, served model tag, and digest; the subject (`JUDGE_SUBJECT_CRITERION`, `FINAL_REPLY`, or `MEMORY`), criterion, rubric, and position; the verdict state, score, and rationale; a failure only as its closed `JudgeFailure` code; the attempt and transaction; usage and provider timings from the signed result; the receipt's execution time; and, live only, the harness dispatch wall time. Judge prompts and raw failure text are never published. The event's `variant_id` and `role` stay the graded subject's, and the validator rejects a judge block on any other kind and a `metric_delta` or `stage_label` on a judge kind, so judge usage never merges into the subject's metrics. Deferred publication rebuilds one `judge_completed` per persisted grader call from its signed result under the live idempotency key (`JudgeCompletedIdempotencyKey`), ahead of the assignment's terminal events, so the events reach `assignment-<id>.db`; `judge_started` events and judgments without a verified receipt are live only. The Evaluation Explorer joins judge events to rows by exact dataset, run, and assignment identity, exactly like Tribunal votes, and renders them in the row's Judge cell. Judge events are never table rows.
- **Deterministic Grading**: A campaign executed with publication publishes one `deterministic_grading_completed` event per grading (`GradingActivityPublisher.DeterministicGrading`) as soon as the sealed trace is graded, after the content judgments the `scenario-content` grade folds in and before the quality judge. The repeated `deterministic_grades` block (`PublicLiveDeterministicGrade`) holds every grade in grading order: its criterion, status, and grader detail, the same content the assignment's public grade summary publishes. The event's `variant_id` and `role` stay the graded subject's, and the validator rejects the block on any other kind and a `metric_delta` or `stage_label` on this kind. Deferred publication rebuilds the event from the persisted result ahead of the assignment's terminal events, so it reaches `assignment-<id>.db`. `DeterministicGradingIdempotencyKey` names the graded content: identical grades are published once, and a persisted result holding different grades publishes a later event that supersedes them. The Evaluation Explorer joins the event to rows by exact dataset, run, and assignment identity, and renders the latest event's grades as one circle per criterion in the row's Deterministic cell, between Consensus and Judge. A running row with Judge activity and no grades reads Pending there, because the content judgments finish first; a terminal row without grades stays empty. Deterministic grading events are never table rows.
- **Evaluation Integration**: Native evaluation runs persist under `.ztd/data/eval/runs/` (`EvaluationRunsDirname` under `DataDirname` and `EvaluationDirname`). The projector generates allowlisted projections; each captured agent trace is bound by digest without exporting its body (`INV-SPEC-DATA-03`).

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

Public evidence is authenticated through a trusted Ed25519 signing key, the signed pointer, its read model, and the settled run bundle named by the selected run's evaluation summary. The browser checks object digests but does not authenticate the pointer signature.

Download `pointer.json`, the `read-model/<read_model_sha256>.json.gz` object it names, and the selected run's `runs/<assignment_bundle_sha256>.jsonl.gz`. Keep the bundle's gzip bytes unchanged: its digest covers the stored file. The read model digest covers decoded JSON; the CLI accepts its gzip object or decoded bytes. Obtain trusted keys separately as a JSON mapping of key ID (or source ID plus colon plus key ID) to hex Ed25519 public key. A registry can contain old and rotated keys; a self-published key alone is never trusted in this mode.

```bash
./ztd public verify-proof --pointer pointer.json --read-model read-model.json.gz --bundle run.jsonl.gz --run "$RUN_ID" --key-registry trusted-keys.json
```

The verifier checks the pointer signature and source/sequence/feed-hash/withdrawal identity, hashes the read model and gzip bundle, and then checks each exact record_bytes hash, ordered sequence, run identity, and assignment uniqueness. An absent or withdrawn run has no usable bundle binding in a current read model.

Scenario fixtures remain public at `fixtures/<sha256>`. A fixture's digest covers its decoded bytes, and the CDN may compress JSON in transit, so download fixtures with content decoding (`curl --compressed`). Full agent traces and assignment audit databases are delivered under agreement. Place any delivered files under their bare SHA-256 in a directory and add `--artifacts ./delivered` to verify each against the authenticated assignment evidence bindings. Missing private files are not required. Unbound files, changed bytes, directories, and symlinks fail verification.

For an assignment audit database, use the same trust inputs:

```bash
./ztd public verify-assignment --db assignment.db --pointer pointer.json --read-model read-model.json.gz --bundle run.jsonl.gz --run "$RUN_ID" --key-registry trusted-keys.json
```

This additionally requires an assignment_audit_slice digest binding, verifies the database's live-event record hashes, and matches its source, run, and assignment identity. Database verification does not independently verify every governed execution receipt. The database is plaintext SQLite and needs no vault key.

The pointer authenticates a captured state; it does not prove that a capture is the latest state. A previously downloaded pointer and bundle can still verify after withdrawal. Remote mirrors without a source signing key serve unsigned pointers and cannot supply this trust root. Bounded history remains public for signed feed-chain checks, subject to retention and withdrawals. The internal proof log is no longer an anonymous endpoint or bucket projection. The separate `verify-proof --segments` mode remains for an explicitly obtained internal log and its active files.

## Anti-patterns

- **Conflating observe modes**: Attempting to use public mirror endpoints for owner-local control, or vice versa (`INV-SPEC-ISO-01`).
- **Redacting or summarizing published evaluation text**: Screening, truncating, or reconstructing published model responses, tool results, or grader detail instead of preserving it verbatim within the size bounds (`INV-SPEC-DATA-03`). Full traces and audit databases are offered separately under agreement, with public digest bindings.
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

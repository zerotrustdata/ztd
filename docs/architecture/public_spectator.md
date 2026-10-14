---
doc_id: public_spectator
title: Public Spectator Architecture and Threat Model
audience: architects and security reviewers
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - internal/services/gateway/public_mirror.go
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
- [Relationship to Platform Architecture](#relationship-to-platform-architecture)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Mode and Endpoint Isolation (`INV-SPEC-ISO`)

| ID | Rule |
| --- | --- |
| INV-SPEC-ISO-01 | Owner-local observe mode and anonymous public spectator mode MUST NOT share state, configuration, credentials, or endpoints. Public visitors NEVER connect directly to the private Gateway API. |
| INV-SPEC-ISO-02 | The public mirror listener MUST expose only read-only endpoints (`GET /bootstrap`, `GET /snapshot`, `GET /history`, `GET /stream`, `GET /proof-catalog`, `GET /proof-manifest`, and `GET /proofs/<artifact-id>`). It MUST NOT mount mutation, producer, governance, audit, or filesystem routes. |
| INV-SPEC-ISO-03 | Private ingest operations (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`) MUST be served on an isolated loopback listener accessible only to the authenticated publisher. |

### Data Classification and Projection (`INV-SPEC-DATA`)

| ID | Rule |
| --- | --- |
| INV-SPEC-DATA-01 | Every exported record MUST pass a closed allowlist enforced by campaign projector, disclosure validator, publisher, and mirror before becoming browser-visible. Any unknown field fails closed. |
| INV-SPEC-DATA-02 | Prompts, intermediate reasoning, raw chain-of-thought, credentials, private keys, SPIFFE identities, machine-specific paths, and internal transaction IDs MUST NEVER cross the projection boundary. |
| INV-SPEC-DATA-03 | Model responses and failure outputs are published only as bounded exceptions (`model_response`, `failure_output`, `role_transcripts`). Records exceeding 256 KiB or containing `BEGIN PRIVATE KEY` or `spiffe://` are rejected. |

### Cryptographic Continuity (`INV-SPEC-CRYPTO`)

| ID | Rule |
| --- | --- |
| INV-SPEC-CRYPTO-01 | Every exported batch MUST be an append-only, monotonically sequenced record signed with Ed25519 over the content hash and linked to the predecessor batch hash (`prev_batch_hash`). |
| INV-SPEC-CRYPTO-02 | Proof artifacts MUST be immutable, content-addressed files served under paths derived from their SHA-256 digests. |
| INV-SPEC-CRYPTO-03 | Key rotation MUST occur via a signed revocation batch. Batches signed by revoked keys are rejected. Historical batches remain verifiable against the retained key registry. |
| INV-SPEC-CRYPTO-04 | Withdrawing a campaign run (`WithdrawCampaignDataset`, reached through the Gateway's publication-state `DELETE` that `ztd eval runs delete` calls) MUST NOT remove or rewrite signed batches. The mirror records the run's dataset as withdrawn and filters its projections from `/bootstrap`, `/history`, and `/stream`; the publisher prunes the run's proof artifacts from the proof catalog and pushes it. Every mirror snapshot carries `withdrawal_digest` (SHA-256 over the sorted withdrawn dataset IDs, zero hash when none), and withdrawal pushes a fresh `snapshot` event to open streams. A client MUST discard cached or in-memory records and refetch history when the digest changes, because the signed chain alone cannot reveal a withdrawal. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Public mirror state storage | `public-mirror/state.json` | Validates against `PublicMirrorStoreState` schema |
| Public feed configuration | `public-feed/` | Key, token, snapshot persist with 0700/0600 permissions |
| Public publisher commands | `internal/cli/cmd/public/` | `./ztd public status`, `./ztd public publish --help` |
| Disclosure validation | `internal/services/publicdisclosure/validator.go` | `./ztd test unit --pkg ./internal/services/publicdisclosure` |
| Public spectator runtime | `internal/services/gateway/public_spectator_runtime.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestPublicSpectator` |
| Evaluation verification | `internal/services/evaluation/` | `./ztd eval boundary verify`, `./ztd eval runs verify` |

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

- **Owner-Local Observe Mode**: An authenticated operator connects to the private Gateway (`https://<gw>:8443/console/`). The browser authenticates via WebAuthn passkeys, receiving a secure session cookie. Every read is scoped to the user's permissions and session bindings.
- **Public Spectator Mode**: Anonymous internet visitors connect via HTTPS to an external domain routed through Cloudflare to a local `cloudflared` tunnel, which dials the loopback-only public mirror listener (`127.0.0.1:8082`). No credentials are sent, and the private Gateway is physically unreachable.

## Network Path and Listener Isolation

The Gateway process hosts the `PublicSpectatorRuntime` (`internal/services/gateway/public_spectator_runtime.go`), binding two distinct loopback listeners:

```text
Campaign Host                     Gateway Process                      Public Edge
─────────────                     ───────────────                      ───────────
Canonical Eval Runs
       │
       ▼ (owner mTLS)
POST /api/v1/public-feed/batches ──► PublicSpectatorRuntime
                                      ├─ Private Ingest Listener (:8081)
                                      │  (authenticated POST /ingest)
                                      │
                                      └─ Public Read Listener (:8082)
                                         (GET /bootstrap, /snapshot, /history, /stream)
                                                      │
                                                      ▼ loopback 127.0.0.1:8082
                                                host cloudflared ──tunnel──► Cloudflare Edge
                                                                                   │
                                                                                   ▼ HTTPS
                                                                             Public Browser
```

1. **Private Ingest Listener**: Accepts authenticated batch submissions from the publisher (`POST /ingest`, `POST /keys/register`, `POST /proof-ingest`).
2. **Public Read Listener**: Exposes anonymous read endpoints (`GET /bootstrap`, `GET /snapshot`, `GET /history`, `GET /stream`, `GET /proof-catalog`, `GET /proof-manifest`, `GET /proofs/<id>`). Unknown paths and mutation methods are rejected.
3. **Loopback Binding**: Listeners bind only to loopback interfaces. External visitors reach the public listener solely via `cloudflared` tunnel configured with trusted proxy CIDRs.

## Closed Allowlist and Data Classification

Only allowlisted fields and records cross into the public spectator feed.

### Permitted Projections

- **Campaign Identity**: Campaign ID, revision, cycle ID.
- **Run & Assignment Identity**: Run ID, assignment ID, variant ID, task ID, arm ID, repetition index.
- **Model Role Identity**: Primary variant ID, Assistant variant ID, Lite variant ID.
- **Metrics**: Metric ID, numerator, denominator, rate, unit, uncertainty interval, claim status.
- **Verification Status**: Verification label, verification boundary, index-generation hash.
- **Categorical Environment Labels**: Hardware class label, backend label, quantization policy.
- **Bounded Diagnostic Detail**: `trajectory_outcome` (closed vocabulary), `guided_retry_count`, public failure sentence, `tools_declared`.
- **Bounded Model Outputs (Optional Extensions)**: `model_response`, `failure_output`, `role_transcripts` (capped at 16 KiB, restricted-text rules enforced).

### Prohibited Fields

The following fields MUST NEVER cross the projection boundary:
- Private scenario prompts, full system prompts, and prompt hints.
- Intermediate chain-of-thought, model reasoning traces, and scratchpads.
- Private evidence files, encrypted evidence envelopes, and vault DEK/KEK metadata.
- User identities, WebAuthn credentials, session IDs, and tokens.
- Internal IP addresses, internal DNS hostnames, SPIFFE URI SANs, and filesystem paths.
- Execution receipts, raw command strings, and audit ledger internals.

## Export Binding and Cryptographic Continuity

Every exported batch forms an immutable, verifiable link in a cryptographic chain:

| Field | Type | Description |
| :--- | :--- | :--- |
| `source_pseudonym` | String | Stable pseudonymous identifier for the publishing source |
| `protocol_version` | String | Protocol version (`1.0.0`) |
| `schema_version` | String | Projection schema version (`1.0.0`) |
| `first_sequence` | Uint64 | Monotonic sequence number of the first record in batch |
| `last_sequence` | Uint64 | Monotonic sequence number of the last record in batch |
| `prev_batch_hash` | Hex String | SHA-256 of the prior batch (first batch uses zero hash) |
| `record_hashes` | Array[Hex] | Ordered SHA-256 digests of all records in the batch |
| `generated_at` | RFC3339 | Monotonic UTC timestamp |
| `content_hash` | Hex String | SHA-256 digest over the canonical encoding of batch metadata and record hashes |
| `signing_key_id` | String | Identifier of the Ed25519 signing key |
| `signature` | Hex String | Ed25519 signature over `content_hash` |

## Threat Model and Mitigations

| Threat Vector | Mitigation Strategy | Fail-Closed Behavior |
| :--- | :--- | :--- |
| **Replay & Reordering** | Batches enforce strictly increasing monotonic sequence numbers and `prev_batch_hash` linkage. Clients reject sequence $\le$ last consumed. | Batch rejected with HTTP 400 |
| **Equivocation** | Content hashes bind each batch to its exact position. Comparing content hashes across mirrors detects branch divergence. | Key immediately revoked on detection |
| **Stale Snapshots** | Snapshots bind high-water mark and chain hash. Clients verify freshness labels (`active`, `delayed`, `stale`, `offline`). | Stale data explicitly tagged, not served as live |
| **Forged Events** | Batches signed with Ed25519. Mirrors and offline verifiers validate signatures over canonical content hashes. | Unsigned or bad signatures rejected |
| **Mirror Tampering** | Projections and proofs are content-addressed by SHA-256. Proof root manifests bind the entire artifact tree. | Hash mismatch causes immediate client rejection |
| **Cache Poisoning** | Immutable HTTP cache headers (`Cache-Control: immutable`). Client checks SHA-256 against proof catalog before acceptance. | Corrupted download discarded |
| **Path Traversal** | Mirror normalizes artifact URLs and strictly rejects `..` or absolute paths. Symlinks are refused at ingest and serve time. | HTTP 403 Forbidden |
| **Denial of Service** | Strict rate limits (600 req/min per IP), bounded SSE buffers (100 events), maximum download lengths. | HTTP 429 Too Many Requests |
| **Correlation Leakage** | Stable non-reversible source pseudonyms; stripping of user IDs, operator sessions, and machine hostnames. | Sanitized at disclosure boundary |

## Anonymous Read Limits, Retention, and Availability

### Read Rate Limits

- **Bootstrap**: Single snapshot per request.
- **History**: Cursor-paginated (page size 1–500, default 20).
- **SSE Stream**: Bounded to 1,000 concurrent connections, 100-event memory queue per connection. Drops oldest events on consumer lag and signals `truncated`.
- **Proof Downloads**: Bounded by byte length in catalog metadata.
- **Client Rate Limit**: 600 requests per minute per IP. Trusted proxy CIDR required for `CF-Connecting-IP`.

### Retention

- **Batches**: Retains up to 25,000 batches by default.
- **Proof Artifacts**: Content-addressed storage persists indefinitely until explicit operator cleanup.
- **Publisher Outbox**: Unacknowledged batches persist until acknowledged by the mirror.

## Relationship to Platform Architecture

- **Private Ingress**: Private Gateway surfaces (`/mcp`, `/api/v1/a2a/`, `/api/v1/governance/envelopes`) never accept anonymous traffic.
- **SSE Bridge**: Gateway SSE (`/api/v1/sse/stream`) is session-scoped for owners and CLIs; Public SSE (`/stream`) is served exclusively by the mirror runtime.
- **Evaluation Integration**: Native evaluation runs persist under `.ztd/data/eval/runs/`. The public-safe projector generates allowlisted projections without exposing internal traces.

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

1. Download the proof manifest, catalog, and artifact files.
2. Disconnect network connectivity.
3. Compute SHA-256 of all artifacts and compare against catalog entries.
4. Verify catalog entries match the signed root manifest.
5. Verify Ed25519 signature over manifest root using trusted public key.

## Anti-patterns

- **Conflating observe modes**: Attempting to use public mirror endpoints for owner-local control, or vice versa (`INV-SPEC-ISO-01`).
- **Publishing unredacted model traces**: Emitting free-form prompt, reasoning, or trace text across the projection boundary (`INV-SPEC-DATA-02`).
- **Direct public exposure of Gateway**: Pointing external tunnels directly at Gateway port 8443 or 8080 instead of the mirror loopback port 8082.
- **Treating mirror uptime as proof validity**: Relying on mirror availability rather than verifying signed proof artifacts offline.

## Links out

- [SSE Streaming](sse.md): Gateway event publication and browser delivery surfaces.
- [Console Architecture](console.md): Owner-local browser interface and runtime boundaries.
- [Network Architecture](network.md): PKI, mTLS, and transport surfaces.
- [Gateway Architecture](gateway.md): Gateway services, protocol surfaces, and trust boundaries.
- [Evaluation Architecture](evals.md): Model campaign evidence, witness capabilities, and verification.
- [Model Provenance](model-provenance.md): Storage-side weight attestation and chain-of-custody for scored inference.

---
doc_id: storage
title: Storage Architecture
audience: maintainers and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - internal/services/storage/
  - internal/constants/paths.go
related:
  - encryption.md
  - gateway.md
  - operator.md
  - governance.md
  - events.md
  - sse.md
  - network.md
  - evals.md
  - model-provenance.md
when_to_read: Understanding database persistence, retention policies, audit evidence architecture, encryption boundaries, transaction storage, replay protection, and host-local execution evidence.
do_not_use_for:
  - Five-layer governance logic (see governance.md)
  - Network PKI and transport identity (see network.md)
  - Vault encryption algorithms (see encryption.md)
---

# Storage Architecture

## Purpose

Documents ztd's storage architecture: local SQLite databases, persistence topologies, audit evidence layout, encryption boundaries, transaction storage for L3 approvals, replay protection, and retention policies. The Gateway and outbound Operators each maintain a canonical database and optional satellite stores for audit, execution, and ledger evidence. This document maps persistence design to implementation in [internal/services/storage/](../../internal/services/storage/).

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

| ID | Rule |
| --- | --- |
| INV-STOR-KV-01 | The Gateway exposes no HTTP KV API. Session, nonce, scrubbing-token, and other KV keys are private to Gateway services. |
| INV-STOR-KV-02 | No `kv_store` row is a derived copy of a document or query result, and document writes MUST NOT write or delete KV rows. Every `kv_store` row is committed state in the bound or observed tier. |
| INV-STOR-KV-03 | Gateway services and application clients MUST use Gateway storage primitives and MUST NOT emulate atomic counters, hashes, lists, or nonce reservation through client-side read/modify/write. Governed replay protection belongs to the executing runtime's uniqueness-constrained replay store. |
| INV-STOR-ENC-01 | Every column or file that holds content (command lines, output, file contents and paths, target resources, summaries, receipt and attestation bodies, envelopes, tool arguments, diff stats, session titles and identities) MUST be encrypted with the runtime vault before it is written. A nil or locked vault MUST fail the write and the read with a typed sentinel. MUST NOT add a plaintext branch, an `encrypted` flag, or a fallback. Only identifiers, hashes, signatures, enum types and statuses, timestamps, sizes, counts, exit codes, and key IDs stay plaintext. |
| INV-STOR-ONE-01 | Each runtime root persists local state in one SQLite database (`data/ztd.db`) and one file ledger (`data/ledger/`), plus the vault, secrets, and inference trees. Each fact has one owning record and one store implementation: MUST NOT add a second database file, a second table holding the same fact in another shape, or a parallel implementation of an existing store. |
| INV-STOR-EGRESS-01 | Local stores hold full, unscrubbed content (encrypted per INV-STOR-ENC-01). Every result that leaves the runtime MUST pass the single egress choke point (`pubsub.BuildUniversalResultEnvelope`, backed by `scrubbing.ScrubbingService`). With no scrubber, egress publishes the typed failure event and never the raw content. The Gateway HTTP audit endpoints (`/api/v1/audit/events`, `/report`, `/receipts`, `/receipts/export`) scrub every content-bearing field (`command_raw`, receipt `target_resource` and `result_summary`) before the response, `AuditController` requires a scrubber at construction, and a disabled scrubber returns 503 with `ErrEgressScrubberUnavailable`. `mcp.GatewayService.DispatchToDownstream` scrubs every branch (read_field, native tools, downstream MCP) and fails closed with no scrubber. The L5 actuator signs a result digest only from the typed `CommandMessage.InferenceResultDigest` channel of an INFERENCE action; every other handler output is scrubbed text, and no output is recognized by shape. |
| INV-STOR-LOG-01 | Logs carry no execution content. Logger calls MUST NOT include command lines, arguments, stdout or stderr, file paths and patterns, target resources, summaries, diffs, envelopes, tool arguments, prompts, or outputs, as attributes or in the message, and MUST NOT log an error whose text embeds them (use `logging.ErrorKind`). Log identifiers (`execution_id`, transaction hash, `operator_session_id`), sizes, counts, and exit codes instead; the raw content lives only in the encrypted `events` table. `TestLoggerCallsCarryNoExecutionContent` enforces the attribute keys for the execution-path packages. |
| INV-STOR-SESSION-01 | Audit record paths (`RecordEvent`, `RecordEventChained`, batch recording, and receipt recording in `internal/services/storage/audit_store.go`) MUST NOT write the `sessions` table; a record for an unknown session fails with `ErrAuditSessionUnknown`. Each session row has one owner per runtime that creates it when the session starts: the outbound Operator creates its session at start in `internal/services/ztdo.go`, and on the Gateway `OperatorSessionService.PersistOperatorSession` creates the audit session row before it writes the session document (embedded claim, bootstrap, registration, recovery, and platform enrollment all call it). CLI and web sessions record no audit events. |
| INV-STOR-SCHEMA-01 | Each store's `CREATE TABLE` is its whole schema. MUST NOT add `ALTER TABLE` migrations, column back-fills, or legacy-shape readers. |

## Owned surfaces

| Surface | Owner | Verification |
| --- | --- | --- |
| Canonical schema and state-commitment dirty-mark triggers | `internal/services/gateway/db/schema.sql`, `CanonicalDBService.initSchema` | `TestStateRootService_DocumentWriteDoesNotTouchKV`, `TestStateRootService_FreshDatabaseInitializesCommitment`, `TestStateRootService_UnsupportedAlgorithmFailsClosedOnOpen` |
| Internal KV state and TTL | `KVStoreService` | Gateway KV integration tests |
| Replay nonce reservation | `storage.ReplayStore` over the `nonces` table in `ztd.db`, built by `CanonicalDBService` on both runtimes | `internal/services/storage/replay_store_integration_test.go`, L4 Warden tests |
| Suspended-transaction store | `storage.SuspendedTransactionService` over the `suspended_transactions` table in `ztd.db`, built by `CanonicalDBService` (`GetSuspendedStore`) | `internal/services/storage/suspended_transaction_store_encryption_integration_test.go` |
| Runtime paths | `RuntimeFileService`, `internal/constants/paths.go` | Isolated runtime fixture tests |

Enrollment admission uses the document store and `BEGIN IMMEDIATE` transaction owner. Deduplication, live-request counting, and insertion share one transaction. Partial expression indexes cover enrollment identity, token hash, live capacity, pending expiry, issuance leases, and retention. Token and pending reads decode only matching requests; the cleanup owner explicitly expires abandoned requests and retains completed issuance records. Batch decisions recheck active-owner authority and every fingerprint-bound member in one transaction, recording the same governed receipt ID on every member.

Operator replacement uses `DocumentStoreService.FindOperatorLeases`, an indexed lookup of the approving owner's exact system fingerprint. It includes every non-terminated remote lease regardless of heartbeat staleness and decodes only matching identities. The lookup performs no staleness derivation; generic registry reads derive staleness at read time. Session authentication uses `DocumentStoreService.FindOperatorsBySession` and a partial session-ID expression index, reads at most two bindings to detect duplicates, and performs no staleness derivation.

SQLite retry helpers take an explicit context for connection-pool waits, statement execution, transaction acquisition, and retry backoff. Cancellation ends backoff and prevents another attempt; exhausted attempts return `ErrSQLiteBusy` while preserving the driver's error. Busy classification uses the SQLite error code, including extended busy codes, rather than rendered text. Failed attempts do not sleep after the last attempt. Existing context-free owners supply a background context, and execution-record evidence remains independent of execution cancellation. The driver's internal busy wait cannot be interrupted by a context, so the default busy timeout is a short 100 ms (`sqliteutil.DefaultBusyTimeoutMs`) and the retry helpers do the longer, cancellable waiting; a deadline overshoots by at most one busy wait. Physical connection initialization and context-free bootstrap/root reads can still outlast cancellation, so context-aware retries alone do not establish an HTTP deadline guarantee. A 5 ms busy timeout is not used: measured backoff idled the writer, and at N=1,000 completions throughput fell from about 111/s to about 68/s.

## Procedures

This document does not define a general SQLite backup or recovery procedure. The supported lifecycle operations are owned by CLI commands; confirm flags and defaults with `./ztd <command> --help`.

- **Reset host runtime state:** `ztd gw clean` stops services and renames `.ztd/` aside to `.ztd-<MMDDHHMM>` (nothing is deleted) after offering an evaluation-evidence backup, so the Gateway restarts with no databases, vault, or PKI. `make clean` removes build and test artifacts only and does not reset runtime state, trust, or databases.
- **Back up and restore evaluation evidence:** `ztd eval backup` and `ztd eval restore` copy and verify `.ztd/data/eval/` and `.ztd/eval/`; `ztd eval runs delete` and `ztd eval campaigns delete` purge deleted evidence from the backups too; see [Evaluation Programs](./evals.md).
- **Verify the audit chain:** `ztd audit verify` walks the hash chain from the latest checkpoint (or genesis) and reports the head sequence and hash.

## Anti-patterns

- Bypassing the RuntimeFileService to open database paths directly.
- Hardcoding database paths instead of using constants from [internal/constants/paths.go](../../internal/constants/paths.go).
- Assuming database file permissions are set by SQL library code (they are owned by RuntimeFileService).
- Placing encrypted content outside the field-level vault boundaries (see Security Properties).
- Writing the master key, or any unsealed secret, to a file under the runtime tree.

## Overview

ztd separates platform coordination state from host-local execution evidence. The Gateway and each outbound Operator open a local canonical SQLite database. The Gateway uses it for platform coordination state, enrolled Operator registries, session management, and Gateway-runtime audit evidence. An outbound Operator opens its own copy of the canonical database for the shared state-root schema and its authoritative evidence from operations executed in that Operator runtime.

Each runtime root holds one SQLite database and one file ledger (INV-STOR-ONE-01). Replay nonces, suspended transactions, and execution events are tables in `ztd.db`, not separate database files. An outbound Operator records execution events (encrypted command output) in its own `ztd.db` and keeps a required file ledger. Native and campaign evaluation artifacts persist under host-local `.ztd/data/eval/` on the evaluation CLI host, encrypted with the runtime vault; model inventories and rollout queues use project-root `.ztd/eval/` paths. A remote Operator remains authoritative for its local execution evidence; its publication of signed receipts to the Gateway is best-effort mirroring. The Gateway's embedded Operator keeps its own execution events in `ztd.db`, file ledger, and history handler in the Gateway runtime root, opened by the same `storage.OpenLocalExecutionEvidence` owner as the outbound Operator.

See [Encryption Architecture](./encryption.md) for vault and keystore protection, [Gateway Architecture](./gateway.md) for Gateway service assembly, [Operator Architecture](./operator.md) for host-local execution, and [Event and Action Protocol](./events.md) for the audit-chain and receipt-projection contract.

## Persistence Topology

Paths are relative to `.ztd/data/` within the runtime directory. The database filename constant `DbFilename` is defined in [internal/constants/paths.go](../../internal/constants/paths.go). A fresh Gateway or outbound Operator root contains only `data/ztd.db`, `data/ledger/`, the vault and secrets trees, and the inference trees. Every data-bearing directory (`data/`, `data/ledger/`, `data/inference/`, `data/eval/`, `logs/`, `pids/`, `bin/`) is created `0700`, because `--data-dir` can place a data directory outside the private runtime root; `ztd.db` and its `-wal`/`-shm` sidecars are `0600` (`sqliteutil.OpenDB` creates the file private and SQLite gives the sidecars the same mode). PKI directories that hold or parent private keys are `0700`; only `pki/trust/`, `pki/revocation/`, and `pki/binaries/` (public certificates) are `0755`. The runtime root creates no `protocol/`, `docs/`, or `inference/` directory.

| Store | Gateway | Outbound Operator | Purpose |
| --- | --- | --- | --- |
| Canonical database (`ztd.db`) | ✓ | ✓ | Platform documents, key-value and blob state, state roots, SSE events, the audit chain (`events`, `sessions`), receipts, `file_mutation_log`, the commitment ledger, and audit-chain checkpoints. Opened once by `CanonicalDBService`. |
| Execution record (`events` in `ztd.db`) | — | ✓ | Full raw command output and stderr, encrypted at rest and looked up by `execution_id`. File diffs are not stored; they come from the file ledger. |
| Replay nonces (`nonces` in `ztd.db`) | ✓ | ✓ | Durable nonce reservation for L4 verification, one implementation (`storage.ReplayStore`). |
| Suspended transactions (`suspended_transactions` in `ztd.db`) | ✓ | ✓ | Transactions and approval proof material awaiting L3 approval. `envelope` and `tool_arguments` are encrypted. |
| File ledger (`.ztd/data/ledger/`) | ✓ (embedded Operator) | ✓ | Required. Per-session repositories of encrypted file snapshots, commit history, and diffs computed on demand. |
| Inference evidence files (`.ztd/data/inference/`) | ✓ | Inference capability | Canonical protojson files written through RuntimeFileService, not SQLite. The Gateway stores `provider-observer/windows/` and `model-provenance/windows/` attestation windows ingested from Observer and Provenance Operators; an Inference Operator stores one provider-attempt record per attempt under `attempts/` (`<id>.json`: identifiers, status, timestamps, result digest, raw-response digest and size, and a fixed failure category). Provider byte streams, provider error bodies, and inference results are content and are never written there; the record binds them by digest, and the encrypted audit store owns the result (INV-STOR-ENC-01). The window files hold hardware metrics, digests, and identifiers only. See [Model Provenance](./model-provenance.md). |
| Evaluation artifacts | Evaluation CLI | Evaluation CLI | `.ztd/data/eval/`: run and campaign evidence, every file encrypted with the runtime vault (prompts, model output, tool arguments, and grade detail are content; INV-STOR-ENC-01). The CLI opens the vault from the runtime key store on first use, and a missing or locked vault fails the read or write. Executor and rollout failures persist a fixed failure class, never error text. A run writes no execution log file. Project-root `.ztd/eval/`: model inventories and the rollout queue, identifiers and statuses only. |
| Compliance report bundles | Compliance CLI | Compliance CLI | `.ztd/reports/<report-id>/` (outside `data/`): signed bundles written by `compliance report generate`, every file sealed with the runtime vault under its own associated data, so a report ciphertext never authenticates as eval evidence. A persisted bundle is never replaced. `compliance report export` is the only path that writes a bundle in the clear, to a new directory outside the runtime root. |

All databases are local to the runtime that opens them. An outbound Operator's `ztd.db` is not a central platform database; it is authoritative only for evidence produced by that Operator. During L4 verification on the authenticated command channel, the Operator uses the Gateway admission snapshot already bound into the envelope, without querying the Gateway or substituting its own database root. L5 reads the local database root before and after execution for receipt evidence. The Gateway mirrors remote signed receipts on a best-effort basis; mirroring failure does not invalidate the Operator's local evidence.

Each runtime keeps its own database, PKI, vault, and evidence under its own runtime root. Removing one runtime root removes only that runtime's state.

## Canonical Database

The canonical database ([internal/services/storage/](../../internal/services/storage/)) opens in SQLite WAL mode with foreign-key enforcement, busy timeout, bounded retries for lock contention, and incremental vacuum. `sqliteutil.OpenDB` applies the settings through the modernc driver's `_pragma` connection parameters, so every pooled connection uses `synchronous=NORMAL`, the configured busy timeout and page cache, foreign-key enforcement, and memory-backed temporary storage. Read-only connections apply `query_only=ON` and the configured busy timeout. Database-file permissions are owned by the RuntimeFileService; the SQLite abstraction does not create parent directories or change permissions on open. A process opens `ztd.db` once: `CanonicalDBService` owns the pool and injects it into the SQL audit store, so Gateway stores, the audit chain, and the commitment ledger share one pool. The audit store never opens or closes the database.

SQLite admits one writer at a time. Every `sqliteutil` write path (`ExecWithRetry`, `ExecInTxWithRetry`, `ExecInImmediateTxWithRetry`) first takes the pool's writer gate, so in-process writers queue in FIFO order instead of polling SQLite's lock through busy timeouts and retry backoff, and a queued writer holds no pooled connection that readers need. `SQLITE_BUSY` retries remain for contention with other processes. A write-transaction callback must do all of its work through the connection it is given; starting another gated write on the same pool from inside it deadlocks. Automatic WAL checkpointing is disabled for pools opened by `sqliteutil.OpenDB`; gated commits wake a background `PASSIVE` checkpointer instead, so checkpoint work never runs inside a writer's `COMMIT`.

### Platform Documents

The document store persists JSON records by collection and identifier. Gateway services use it for users, sessions, Operators, policies, consensus definitions, signer records, enrollment state, passkeys, revocations, and other platform resources. Document writes touch no key-value rows; each write marks only its own leaf dirty in the incremental state commitment.

### Key-Value and Blob State

The key-value store persists string values with optional expiration. The blob store persists binary content by namespace and identifier with content type, size, and optional expiration. Both stores distinguish bound state from observed state.

There is no HTTP key-value API; Gateway services access KV state directly through `KVStoreService`. The Gateway keeps no derived copy of documents or query results in `kv_store`. Callers do not maintain a second governed replay guard or emulate Redis structures.

Bound documents, bound key-value entries, and bound blobs contribute to the state root that L4 verifies. Replay nonces and SSE events do not contribute to that root. Observed key-value entries and blobs are excluded from the admission root and can be hashed as a separate observed-state commitment.

A state-root read honors its caller's context, including while it waits for a pooled connection or a pending-change fold; a canceled read returns an error, and a canceled pending-change fold rolls back without publishing a root. Concurrent reads that observe pending changes coalesce on one fold at a time: a reader whose observation is covered by a fold that began after it, and succeeded, reads the committed root without taking a write transaction. Admission callers (governed dispatch, MCP/A2A, platform enrollment, `/api/v1/state`, and health) pass their request context. Outbound Operators perform no remote state-root fetch. L5 receipt `state_root_before`/`state_root_after` reads use the executing runtime's local store and ignore caller cancellation, as the receipt writes do.

### Scrubbing Token Store

The scrubbing service stores reversible UEI token values through an encrypted adapter over the canonical key-value store. The adapter adds a dedicated namespace, marks entries as observed state, and applies their TTL. Token values are encrypted before persistence; reads and writes fail when the vault is locked.

### Replay Protection

`storage.ReplayStore` is the only replay implementation. The Gateway and each outbound Operator build one over the `nonces` table in their own `ztd.db`, and that store owns the table's `CREATE TABLE` (`nonce`, `reserved_at`, `used_at`, `expires_at`, `status`). Only the L4 Warden reserves nonces; Gateway admission does not. The primary key makes concurrent reservation atomic, and a database error other than a duplicate fails closed rather than bypassing replay protection. A failed validation releases its reservation, while a successful transaction leaves its reservation in place until expiration in the current execution path. `CanonicalDBService.RunMaintenance` removes expired nonces.

### SSE Event Buffer

The SSE event buffer supports reconnection replay for authenticated browser and CLI sessions. Every event belongs to a user and exactly one session route. These events are delivery telemetry, not governance state, and maintenance removes events older than one hour.

## Audit Evidence

### SQL Audit Store

The SQL audit store shares `ztd.db` with canonical platform persistence. It records Operator sessions, hash-chained audit events, file-mutation references, signed action receipts, and the commitment chain. Events require an associated session, except the `ztd.v1.platform.audit.chain.checkpointed` anchor; every record path requires the referenced session to exist (INV-STOR-SESSION-01), and batch insertion commits atomically. Event, receipt, summary, and report queries take an explicit audit scope of Operator session ID, acting app ID, or both (`--app` on `ztd audit` subcommands and `acting_app_id` on the Gateway audit routes). App identity resolves through the transaction receipt and is never inferred from a session, and an empty scope returns events across all sessions, including events without an Operator session.

Every content column of `ztd.db` is encrypted with the runtime vault on write and decrypted on read, and no content column has a plaintext branch. A locked or absent vault fails the write with `ErrAuditStoreVaultLocked`. The encrypted columns, per table, are:

- `events`: `content_text`, `command_raw`, `command_stdout`, `command_stderr`.
- `receipts`: `target_resource`, `result_summary`, `receipt_json`.
- `commitment_ledger`: `target_resource`, `attestation_json`.
- `file_mutation_log`: `filepath`, `diff_stat`.
- `sessions`: `title`, `user_identity`.

Identifiers, hashes, signatures, enum types and statuses, timestamps, sizes, exit codes, and key IDs stay plaintext. The `encryption.md` Canonical Audit Store section carries the same list.

Every content ciphertext binds its `table.column` and row identity as AEAD associated data (`storage.AuditContentAAD`), so a blob moved to another row or column fails authentication. The row identity is known before the insert and is never the autoincrement rowid: the chain `seq` for `events` (assigned under the write lock, where the append encrypts), the session id, the transaction id for `receipts`, the transaction hash for `suspended_transactions`, the commitment hash for `commitment_ledger`, and the event id plus the row's random `mutation_uid` for `file_mutation_log`.

#### Hash-chained audit log

Every row in the `events` table is an append-only chain entry. Each entry records `seq`, `prev_hash`, `content_digest`, and its own `hash`. `content_digest` is an HMAC-SHA256 of the content fields (`content_text`, `command_raw`, output, exit code, duration, `execution_id`) under a subkey the runtime vault derives from its DEK (HKDF-SHA256, label `ztd-audit-content-digest-v1`), stored in plaintext; it is never an unkeyed hash of content, which low-entropy content would let an attacker brute-force offline. A nil or locked vault fails the write. Receipt stages, LFAA audit facts, and other persisted events share this chain. `VerifyChain` recomputes each `hash` from the stored `content_digest` and linkage fields without decrypting. `VerifyChainContent` additionally decrypts every event, recomputes the keyed digest, and fails with `ErrAuditChainContentMismatch` on an altered or moved ciphertext or a differing digest; it requires an unlocked vault (`ErrAuditStoreVaultLocked` otherwise) and backs `GET /api/v1/audit/verify` and `ztd audit verify`.

Pruning deletes chained rows only after appending a `ztd.v1.platform.audit.chain.checkpointed` anchor that records the pruned range and terminal hash. `ztd audit verify` and `GET /api/v1/audit/verify` walk the chain from the latest checkpoint (or genesis) and report the head sequence and hash.

#### Receipt projection

The `receipts` table is a latest-stage query projection, not the audit record of record. L5 upserts one row per `transaction_id` as the receipt advances from `EXECUTING` to the signed final receipt that carries the signed persistence attestation. The `EXECUTING` write also appends the transaction's commitment in the same transaction when the commitment ledger is configured. Every stage write also appends a chained `ztd.v1.operator.receipt.recorded` fact whose `content_text` is the canonical protojson receipt for that stage.

Receipt canonicalization and audit-content encryption happen before acquiring the writer gate or SQLite write transaction. Nonempty session IDs are checked for surrounding whitespace before any write. The receipt upsert, audit-chain append, and optional commitment append still commit atomically; receipt and event foreign keys enforce session existence without a repeated session lookup; commitment construction uses the head read inside that transaction. Receipt and commitment success logs run only after commit and after the writer gate is released, so log sinks do not extend the chain's critical section.

Compliance operational export carries both the retained receipt body and the matching chained receipt facts. The evidence importer verifies the exported chain segment, cross-links matching receipt projections to their chain entries, and cross-links commitments to receipts through deterministic stage evidence.

For a remote Operator, the local audit store is authoritative. After execution, the Operator publishes the signed receipt to the Gateway receipt channel. The Gateway verifies the signer, mirrors an accepted receipt into its own chain and projection, but publication failure does not invalidate the already persisted local result.

### Commitment Ledger

The commitment ledger is a permanent SQLite hash chain inside `ztd.db`, distinct from the git-backed file ledger. Before execution, L5 builds and signs a `CommitmentAttestation` against the current chain head inside a `BEGIN IMMEDIATE` transaction. SQLite's write lock, and within one process the pool's writer gate, prevent concurrent writers from selecting the same predecessor; the ledger keeps no separate Go mutex.

L5 passes the signed typed attestation to the ledger builder owner; storage serializes it to canonical JSON once, without decoding and re-encoding an intermediate JSON result. The builder result must be nonnil and name the chain head selected in the transaction. A mismatch or encoding failure rolls back the receipt, audit event, session insertion, and commitment together. The stored hash comes from the same attestation as the stored JSON.

The commitment binds the transaction, state root, action and target, prior commitment hash, L2 and L3 signature digests, and the initial Warden-to-Actuator receipt signature digest. L5 records the commitment and prior hashes in deterministic stage evidence and does not execute when commitment persistence fails. Compliance tooling verifies the chain, signatures, structured fields, and links to receipts independently.

## Host-Local Evidence

### Execution Record

Each command, file edit, file read, and file restore is one `events` row in `ztd.db`, keyed by `execution_id`, written by the handler that executed it. Other Operator results, such as listings, greps, and port checks, are recorded as scrubbed observed-state evidence when published. Standard output and standard error are stored in full: they are gzip-compressed, then encrypted with the runtime vault. Nothing truncates the local record. The publisher scrubs the result on egress. File diffs are not stored; a file mutation row resolves to its ledger snapshots and the diff is computed from them in memory. Commands, file paths, sizes, hashes, exit status, timing, and identifiers remain structured.

The vault must be present, and protected writes fail while it is locked. The execution event is written before any egress. When recording it fails, the result is marked `FAILED` with error type `audit_record_error`, although a host action that already completed is not reversed.

### File Ledger

The file ledger is a required Operator dependency, not an option. `storage.OpenLocalExecutionEvidence` always builds it with the embedded go-git library, and `pubsub.NewFileOpsService` and `storage.NewHistoryHandler` fail with `ErrLedgerRequired` when none is supplied. There is no flag or configuration that disables it, and an Operator that cannot open the ledger does not start. The Operator maintains a default repository and an isolated repository for each session. A governed file mutation snapshots the pre-mutation state, performs the host operation, then copies and commits the resulting state. Before and after commit hashes, diff summaries, and diff content link file history to audit and execution records.

The ledger supports file history, point-in-time reads, and restoration. A host path is content, so it appears nowhere in the ledger tree: each snapshot is named `files/<hex HMAC-SHA256(normalized path)>.enc`, keyed by a subkey the vault derives from its DEK (`Vault.HMACSHA256`). History, diff, and restore compute that name from the caller's path, so there is no path-to-name index to protect. Commit messages carry fixed text, the operation, and the Operator session id only. Mirrored file content is always stored as an encrypted snapshot. When the vault is locked or absent, mirroring and every path lookup fail with `ErrLedgerVaultRequired` and write nothing. Diffs are computed over decrypted snapshots in memory for the one named file, so a concurrent mutation of another file that falls inside the same commit range is never attributed to it; they are unavailable while the vault is locked, and a diff request with an empty commit hash is an error (`ErrLedgerDiffHashesRequired`), never an empty diff.

The ledger fails the mutation rather than losing its record. A pre-mutation snapshot or commit that cannot be taken refuses the edit; a post-mutation snapshot, commit, or diff failure is returned and the edit is published as failed. A restore is itself a governed file write. It reads the commit from the ledger of the request's `operator_session_id`, then writes and mirrors the file through the verified executing session's ledger like `FILE_EDIT`: it refuses to touch the host file unless the pre-mutation snapshot and commit succeed, returns any failure to mirror the restored file, and is recorded as a `RESTORE` row in `file_mutation_log` whose diff resolves from the executing session's ledger. Only the mutation-begin paths create a session repository. Reads (file history, point-in-time reads, diffs, the restore source, commit listing) open an existing session ledger only and return `ErrLedgerSessionNotFound` for a session that has none.

### History Coordination

The history service combines SQL audit events with file-mutation references for completed edits. File history, point-in-time reads, and restoration come from the session's file ledger. History reads fail closed when the ledger or the vault is unavailable.

## Pending L3 Approvals

The suspended-transaction store is the `suspended_transactions` table in `ztd.db`, built by `CanonicalDBService` and exposed through `GetSuspendedStore()`. It persists the canonical envelope, tool context, requestor and Operator identity, the targeted Operator session, expiration, approval state, and either signed CLI or WebAuthn proof material. Reads and listings exclude expired records. Approval updates only an unexpired transaction, successful resumed execution removes the record, and a failed resumed execution leaves it available for the caller to inspect or retry according to the remaining validity window.

The `envelope` and `tool_arguments` columns are vault-encrypted BLOBs (INV-STOR-ENC-01). A nil or locked vault fails both the write and the read with `ErrAuditStoreVaultLocked`, and constructing the store without a vault fails. Identifiers, hashes, signatures, timestamps, approval status, and the approval proof fields stay plaintext.

`operator_session_id` is required and is the envelope's `operator_session_id`. For app-credential Gateway-local MCP and A2A calls that is the claimed embedded Operator's session, stamped by `mcp.GatewayService` (the app identity stays in `acting_app_id`); while the embedded Operator is unclaimed those calls fail closed with `ErrEmbeddedOperatorUnclaimed` and nothing is stored. It owns the approval lifecycle's audit records (INV-STOR-SESSION-01): `mcp.GatewayService.StoreSuspendedTransaction` records `ztd.v1.operator.notary.approval.requested` on that session's chain, and the maintenance sweep records `ztd.v1.operator.notary.transaction.expired` there. Suspension fails closed: an envelope without an Operator session cannot be suspended, and if the approval-requested event cannot be recorded the stored row is deleted again and the caller gets the error. `GetExpiredSuspendedTransactions` reads only the plaintext columns (hash, tool name, session, timestamps, approval and claim state), so the expiry sweep never decrypts a row and a locked vault or one undecryptable row cannot halt expiry. `ClaimSuspendedTransaction` sets `claimed_at` atomically before `ResumeWithL3Proof` executes a transaction (the outbound L3 verifier reads the row during execution, so it cannot be deleted first); a failed execution releases the claim, a successful one deletes the row and returns a failed delete to the caller with the receipt. The sweep never records `transaction.expired` for a claimed row.

## Runtime File I/O

RuntimeFileService ([internal/services/fs/](../../internal/services/fs/)) is the canonical abstraction for paths and file operations inside the `.ztd/` runtime tree. The audit store uses it to establish and verify its data directory before opening the resolved SQLite path. The file ledger uses it for runtime directories and ledger files. File operations outside the runtime tree (host targets, governed mutations) access those paths directly outside the abstraction boundary.

Scratch space is `.ztd/tmp/`: `RuntimeFileService.MkdirTemp` creates a private (`0700`) directory there and returns its runtime-relative path. Throwaway local Operators deployed with `ztd operator deploy --local` default to `.ztd/operators/<name>`, each with its own isolated runtime root. Nothing is staged in the OS temp directory or the user's home directory (INV-FS-11 in [devs.md](../devs/devs.md)).

All SQLite services resolve their database paths through RuntimeFileService before opening them. Under default configuration, relative database paths resolve beneath `.ztd/data/`. The abstraction owns file permissions and directory creation; the SQLite layer does not. `WriteFile` writes atomically through a temporary file in the target directory and a rename; on Windows it clears the read-only attribute on an existing destination before the rename and restores the previous mode if the rename fails.

## Key Material at Rest

Each runtime keeps its platform secrets in `.ztd/secrets/` (`0700`, files `0600`), written through RuntimeFileService. Every secret file there, including the sealed vault key `vault_key`, is AES-256-GCM ciphertext under the platform master key.

The runtime tree never holds the master key in plaintext. Where the master key lives depends on the keyring backend:

| Backend | Master key location | Form in `.ztd/secrets/` |
| --- | --- | --- |
| Secret Service (Linux), Keychain (macOS) | OS keyring, outside the runtime tree | None |
| ssh-agent seal (Linux) | `.ztd/secrets/.master_key.ssh-agent` | Ciphertext under a key derived from an SSH-agent signature; opens only with the same SSH private key in a trusted agent |
| DPAPI (Windows) | `.ztd/secrets/.master_key.dpapi` | DPAPI ciphertext; opens only for the same Windows user on the same machine |
| External file (`--master-key-file`) | Operator-provisioned path outside the runtime root | None; ztd reads the file and never writes, copies, or deletes it |

Copying, backing up, or exfiltrating `.ztd/` therefore yields only ciphertext. The hidden `.master_key.*` names are reserved; secret names cannot be hidden, so no secret operation can touch the seals. Backend selection, sealing, and the external-file checks are owned by [Encryption Architecture](./encryption.md#master-key-and-keyring-providers).

## Retention and Maintenance

Each service runs its own pruner on an interval. Retention is service-specific, not unified across stores:

- **Audit store** ([internal/services/storage/audit_store.go](../../internal/services/storage/audit_store.go)): By default, an hourly background task removes events, file-mutation links, and receipts older than 90 days, then removes only ended sessions (`ended_at` set by `EndSession` when the owning runtime session ends) past the retention period that have no remaining events or receipts. A live session has no `ended_at` and is never pruned, even when idle. Commitments remain permanent. The configured database size limit (default 2048 MB) does not trigger size-based deletion; the time-based policy is authoritative.
- **Execution events** (`events` in `ztd.db`, [internal/services/storage/audit_store.go](../../internal/services/storage/audit_store.go)): retention follows the audit store's `RetentionDays` (90 by default). Pruning appends a chain checkpoint, so the remaining hash chain still verifies.
- **Suspended transactions** ([internal/services/storage/suspended_transaction_store.go](../../internal/services/storage/suspended_transaction_store.go)): The Gateway `mcp.GatewayService.RunMaintenance` sweep is the only pruner. Every minute it records an expiry event for each expired row on that row's Operator session, then deletes exactly the rows whose event recorded (`DeleteExpiredSuspendedTransactions`) through the approvals-notifying wrapper. The event is recorded with `SQLAuditStore.RecordEventOnce`, keyed by the transaction hash and event type, so a delete that fails after the record cannot produce a second `transaction.expired` on the retry. A row whose event fails stays for the next sweep, and a row that expires after the sweep listed it is never deleted unaudited. Expiration governs deletion, not the configured retention-days value, and there is no size-based prune. The store itself runs no background task.
- **Canonical database (key-value, blobs, nonces):** Every 30 seconds, `CanonicalDBService.RunMaintenance` removes expired key-value entries, blobs, and replay nonces, plus SSE events older than one hour.
- **Commitment and file ledgers:** Commitments remain permanent. File-ledger history has no automatic retention or size pruning.

The audit store runs incremental vacuum after scheduled pruning to gradually reclaim free pages.

## Governance Transaction Flow

Every governed operation follows the [five-layer interlock](./governance.md):

1. **L1 Doctrine** validates the typed payload and applies hard gates, forbidden-pattern matching, and MITRE threat detection.
2. **L2 Consensus** verifies Ed25519 consensus votes when the active posture requires them.
3. **L3 Notary** verifies WebAuthn or signed CLI authorization when the active posture requires it. Transactions awaiting Gateway-managed approval are persisted in the suspended-transaction store.
4. **L4 Warden** checks expiry, reserves the nonce, validates payload and transaction hash, verifies the state root, and evaluates required L2 and L3 evidence. Failed validation releases the nonce reservation.
5. **L5 Actuator** signs and persists the `EXECUTING` receipt, appends the signed commitment, rehydrates protected values, invokes the handler, and signs and persists the final receipt and persistence attestation.

Initial receipt and commitment are execution gates. Final receipt persistence occurs after the mutation, so final-persistence failure is returned with receipt evidence but cannot roll back an external side effect. On a remote Operator, publication of the completed receipt to the Gateway is best-effort.

## Security Properties and Limits

- SQLite uniqueness constraints provide durable, concurrent nonce replay detection.
- L5 does not dispatch when initial receipt signing or persistence fails, or when the commitment cannot be appended.
- The commitment ledger serializes chain-head selection and supports independent offline verification.
- The canonical state root binds active authoritative documents, key-value state, and blobs while excluding volatile delivery and replay data.
- No runtime tree stores the master key in plaintext; `.ztd/secrets/` holds only ciphertext, including the DPAPI and ssh-agent master-key seals (see [Key Material at Rest](#key-material-at-rest)).
- Field-level vault encryption protects the content columns of `ztd.db` (listed under [Hash-chained audit log](#hash-chained-audit-log)), execution output, file diffs, and scrubbing tokens. It does not encrypt complete databases or all metadata.
- Exports (compliance, operational, and `ztd report`) carry metadata, hashes, and signatures only. No export writes decrypted content.
- Platform documents, approval proof fields, and SSE payloads are not vault-encrypted by the canonical store. Suspended `envelope` and `tool_arguments` are. Commitment `target_resource` and `attestation_json` are encrypted (see the `commitment_ledger` entry above).
- Ledger snapshots are always encrypted, and their names and commit messages carry no host path (keyed names). Mirroring and path lookups fail closed while the vault is locked.
- A remote Operator's local receipt is authoritative. Gateway receipt mirroring improves centralized visibility but is not a durability guarantee for remote evidence.
- Retention removes audit receipts while commitments remain permanent, so long-term commitment verification can outlive the locally retained receipt cross-link.

## Links out

- [Governance](./governance.md): Five-layer verification and posture behavior.
- [Encryption Architecture](./encryption.md): Vault and keystore cryptography.
- [Gateway Architecture](./gateway.md): Gateway service assembly and canonical database ownership.
- [Operator Architecture](./operator.md): Host execution and local-first evidence.
- [Network Architecture](./network.md): mTLS, transport identity, and PKI.
- [Event and Action Protocol](./events.md): Audit chain and receipt projection.
- [SSE Streaming](./sse.md): Session routing and event replay.
- [Evaluation Programs](./evals.md): Native and campaign evaluation evidence layout.
- [Model Provenance](./model-provenance.md): Attestation window persistence and preflight.

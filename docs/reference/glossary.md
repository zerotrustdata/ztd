---
doc_id: glossary
title: ztd Glossary
audience: developers, maintainers, and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - protocol/
  - internal/services/
related:
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/governance.md
  - docs/devs/codemap.md
when_to_read: Looking up core terminology, cryptographic bindings, runtime boundaries, and protocol contracts across the ztd Governance Suite.
do_not_use_for:
  - Developer and coding invariants (docs/devs/devs.md)
  - Package and runtime ownership maps (docs/devs/codemap.md)
  - Step-by-step test execution (docs/devs/tests.md)
---

# ztd Glossary

Core terminology for the ztd Governance Suite, including the protocol, Governance Gateway, Governed Operator, console, compliance evidence engine, and MCP/A2A integrations. Terms are organized alphabetically. The Gateway and Operator entries describe separate runtime boundaries; see [Gateway Architecture](../architecture/gateway.md) and [Operator Architecture](../architecture/operator.md) for deployment-specific ownership.

---

## A2A (Agent2Agent)

A JSON-RPC protocol surface for agent-to-agent skill invocation exposed at `POST /api/v1/a2a/call`. The Gateway converts an `a2a/call` request into a typed `ztd.operator.v1.A2aCallRequested` (`A2ACallRequested`) protobuf payload, associates it with action type `A2A_CALL` and event type `ztd.v1.operator.a2a.call.requested`, wraps it inside a **Governance Envelope**, and processes it through the governance pipeline. It returns either a signed receipt or a structured suspension response containing an approval URL (`<public base URL>/api/v1/approve/<txHash>`). When admitted under the active governance posture, the request dispatches to the configured downstream A2A server.

---

## Acting App ID

The `acting_app_id` field in a **Governance Envelope**. It identifies the application, tool, or agent acting as the user's delegate (typically a SPIFFE URI such as `spiffe://ztd.local/app/<operator_id>`) and complements `requestor_user_id`, which identifies the human authority.

---

## Actuator (L5 Actuator)

The singular execution boundary in the Operator substrate (`internal/services/governance/l5_actuator.go`). After the **L4 Warden** returns a verified transaction, L5 executes a fail-closed sequence: it signs and persists an initial `EXECUTING` **Event Receipt**, appends a signed **Commitment Attestation** to the SQLite **Commitment Ledger**, rehydrates scrubbed tokens locally through the **Sovereign Execution Boundary**, mints a just-in-time single-action **Capability**, dispatches the action to the registered execution handler, captures deterministic stage evidence, dissolves the capability, finalizes and signs the final receipt, and persists a signed **Receipt Persistence Attestation**. Signing, initial receipt persistence, or commitment persistence failure halts execution immediately.

---

## Assessment Scope

A typed compliance record defined in `ztd.compliance.v1.AssessmentScope` that fixes the deployment, organization, product version, build identity, source revision, image digests, component inventory, network topology hash, configuration hashes, doctrine bundle hashes, consensus policy hashes, trust anchors, cryptographic mode, customer responsibilities, and assessment time window to which evidence and conclusions apply.

---

## Autochthon

The `ztd autochthon` runtime (`internal/services/autochthon/`). It is a standalone observer, not an Operator mode. It runs in the foreground with no Gateway, enrollment, or listener. It samples the host once per second, breeds a fixed-topology predictor by selection and mutation (no gradient training), and records each sample in a hash-chained tally under `.ztd/autochthon/`. It accepts no **Governance Envelope** and produces no **Event Receipt**. `ztd autochthon status` verifies the tally chain. See [Operator Architecture](../architecture/operator.md#autochthon-subsystem).

---

## Bound State Root

The admission-gating **State Root** read from `StateRootService`. It is the root of a persisted incremental SHA-256 Merkle commitment (algorithm 2) over: all authoritative `documents` rows, bound-tier `kv_store` entries (`state_tier = 'bound'`), bound-tier `blobs` entries (`state_tier = 'bound'`), and the token keymap hash provided by `KeymapHashProvider` when the scrubbing service is attached. A write marks only its own leaf dirty; a read rehashes the dirty leaves and their ancestor paths. Nonces and SSE events are not state sources, and observed-tier rows (`state_tier = 'observed'`) are committed to the separate **Observed-State Root**, so telemetry does not continuously invalidate in-flight envelopes.

---

## Canonical Gateway Database

The Gateway's primary SQLite database, `ztd.db` (`constants.DbFilename`), opened in WAL mode via `sqliteutil` and managed by `CanonicalDBService` (`internal/services/gateway/gateway_db.go`). Its canonical schema, `internal/services/gateway/db/schema.sql`, creates `documents`, `kv_store`, `blobs`, the state-commitment tables (`state_commitment_dirty`, `state_leaves`, `state_nodes`, `state_commitment`), and `sse_events`. The stores opened over the same database create their own tables: `storage.ReplayStore` creates `nonces`, the Local-First Events Store creates `sessions`, `events` (which holds the **Execution Record**), `file_mutation_log`, and `commitment_ledger`, and `suspended_transaction_store.go` creates `suspended_transactions`. The public mirror is a separate file, `public-mirror/mirror.db` (`constants.PublicMirrorDBPath`). Remote Operator databases are distinct; each maintains an independent lifecycle and sovereignty boundary.

---

## Capability

A just-in-time, single-action, self-dissolving permission minted by the L5 Actuator (`internal/services/governance/capability.go`) from a verified transaction. It binds the transaction hash, action type, target resource, Operator identity and session, envelope expiry, a random 32-byte single-use token, and the Actuator key ID. The token and transaction hash are signed with the Actuator's private key. L5 places the capability into the Go execution context via `ContextWithCapability` and dissolves it immediately after dispatch completes or fails; handlers verify it before performing operations.

---

## CLI Operator Session Binding

The persisted pair of `operator_session_id` and `operator_id` stamped on an authenticated CLI session. Running `ztd operator bind <operator-session-id>...` pins the CLI session to one or more active operator sessions owned by the same user in a single `POST /api/v1/auth/cli/bind` call (all targets are validated first; the first is the primary binding); `ztd operator bind list` inspects bindings; `ztd operator bind unbind` clears the binding via `POST /api/v1/auth/cli/unbind`. Binding changes update the CLI session in place (same CLI session ID and certificate) and refresh local credentials. Session refresh, rotation, and recovery inherit the prior binding when present. The unified auth middleware derives operator identity from the session record and rejects contradictory request headers.

---

## CLISession

The mTLS-authenticated session of an enrolled CLI or BYO client (`internal/models/auth.go`, `CLISession`). The Gateway persists it in the `cli_sessions` collection (`constants.CollectionCLISessions`) through `CLISessionService` (`internal/services/gateway/cli_session_service.go`). The enrolled user's CLI holds it (`ztd auth enroll user`), including `ztd tui` and the `ztd mcp` proxy, and it is the routing namespace for that client's SSE streams. Authentication requires a client certificate whose SPIFFE URI SAN is `spiffe://ztd.local/cli/<user_id>/<cli_session_id>` plus the `X-ZTD-CLI-Session-ID` header (`constants.HeaderCLISessionID`). `handleCLIAuth` in `internal/services/gateway/gateway_auth.go` rejects the request with `ErrMTLSIdentityMismatch` when the certificate SAN does not name the header session, and it rejects inactive or expired sessions. The session lifetime is `constants.CLISessionTTL` (7 days), aligned with the CLI certificate. `ztd auth refresh` extends it in place under the same ID. It is disjoint from **OperatorSession** and may bind to operator sessions through `operator_session_id` and `bound_operator_session_ids` (see **CLI Operator Session Binding**).

---

## Command Dispatch

The canonical mechanism for dispatching host operations across Governed Operators. Enrolled applications and services dispatch commands through `POST /api/v1/operators/commands` with a registered request `event_type` and serialized protobuf payload bytes, or submit pre-formed `GovernanceEnvelope` instances to `POST /api/v1/governance/envelopes`. The Gateway encapsulates the command in a canonical envelope, screens it via L1 Doctrine, coordinates L2 Consensus and L3 Notary as required by posture, and publishes the envelope over the Operator's session-specific channel (`cmd:<operator_id>:<operator_session_id>`) for L4 Warden verification and L5 Actuator execution.

---

## Commitment Attestation

The canonical protobuf `ztd.operator.v1.CommitmentAttestation`. Before execution, the L5 Auditor signs a record binding the transaction ID and hash, state root at commit, action type, target resource, canonical digests of L2 consensus votes, Warden intent signatures, and human Notary signatures, the prior commitment hash, committed timestamp, and Auditor key ID. Its canonical SHA-256 hash becomes the cryptographic link chained by the next attestation.

---

## Commitment Ledger

The append-only SQLite hash chain of signed **Commitment Attestations** (`internal/services/storage/commitment_ledger.go`, table `commitment_ledger`). L5 builds and appends each commitment against the current chain head while holding the database write lock, preventing concurrent writers from selecting the same predecessor. Compliance and reporting tools independently verify chain order, hashes, signatures, structured columns, and receipt cross-links. This is distinct from the git-backed **Ledger** used for file history.

---

## Compliance Bundle

A portable, content-addressed release package (`internal/services/compliance/report/bundle.go`) containing a manifest (`manifest.json`), canonical compliance report formats (JSON, OSCAL, Markdown, HTML, CSV, CLI), assertion assessments, framework control assessments, evidence index, source artifacts, catalogs, crosswalks, checksum roots, and cryptographic signatures. Verification (`VerifyComplianceReportBundle`, `internal/services/compliance/report/bundle_verifier.go`) independently checks allowed paths, digests, signatures, references, scope, versions, and assessment consistency without trusting rendered text or terminal output.

---

## Consensus

A named L2 governance body defined by a `ConsensusPolicy` (`internal/models/auth.go`). Its `provider` (`constants.ConsensusProvider`) is either `application` (an enrolled application's deliberation endpoint returns signed votes) or `tribunal` (the Gateway-hosted **Tribunal** panel). Its enrolled members are a bound set of N trusted signer identities (`member_app_ids`), and the policy defines the affirmative quorum threshold (`quorum`, K distinct affirmative members), distinct-signature requirement (`require_distinct`), and enabled state. Members sign Ed25519 votes over `<transaction_hash>|<decision>`. L2 gates execution under `consensus` and `notary` postures and remains audited under `doctrine` and `ratify`.

---

## Console

The first-party browser frontend (`console/`), embedded in the `ztd` binary and served by the Gateway at `/console/`. It provides passkey authentication, approvals, Operator inventory and binding, account and API views while remaining outside the Policy Decision Point and Policy Execution Point boundaries. The browser authenticates directly to the Gateway with WebAuthn and a Gateway-issued HttpOnly session cookie. See [Console Architecture](../architecture/console.md).

---

## Control Assertion

A framework-neutral, atomic statement of technical behavior defined in the protocol assertion catalog (`protocol/constants/compliance/assertion-catalog.json`, catalog version 2.1.0). Each assertion declares its identifier, version, title, statement, category, component scope, responsibility, applicable action classes, required evidence types, required grader and verifier references, minimum evidence level, validation cycle, missing-evidence policy, and passing rule. Typed crosswalks map these assertions to external framework controls (e.g. FedRAMP 20x CR26, NIST SP 800-53 Rev. 5).

---

## Deterministic Stage Evidence

The canonical protobuf `ztd.operator.v1.DeterministicStageEvidence`, which records a typed governance or execution stage with monotonic timing (`monotonic_start_ns`, `monotonic_end_ns`), clock domain, timing source, outcome (`DeterministicStageOutcome`), transaction and identity bindings, state roots before and after execution, signer and signature digests, commitment hashes, doctrine bundle identity, audit record references, and parent-stage linkage. Stage kinds cover `L1_DOCTRINE`, `PROTOCOL_L2`, `L3_NOTARY`, `L4_VERIFICATION`, `RECEIPT_PERSISTENCE`, `COMMITMENT_APPEND`, and `L5_EXECUTION`.

---

## Encrypted KV Adapter

The adapter (`internal/services/gateway/encrypted_kv_adapter.go`) connecting the Gateway KV store service to the `storage.TokenStore` interface required by the scrubbing service. It encrypts token values using the local vault with AES-256-GCM, namespaces them with `constants.SentinelKeyPrefix` (`ztd:sentinel:`) followed by `constants.ScrubbingTokenKeyPrefix` (`uei_token_`), and writes them as `state_tier='observed'`. This excludes encrypted token storage rows from the bound state root while the deterministic in-memory token keymap hash is bound into the state root separately.

---

## Enrollment Token

A one-time token (`internal/models/auth.go`, `EnrollmentToken`) used to bridge passkey enrollment from the CLI to the browser without placing raw session identifiers in a URL. The Gateway generates 32 cryptographically random bytes, hex-encodes them, associates the token with the user and CLI session, and applies a five-minute expiration. The browser receives it in the URL fragment, and validation consumes it once. This differs from the credentials used by **Platform Enrollment**.

---

## Event Receipt

The canonical protobuf `ztd.operator.v1.EventReceipt`, signed with Ed25519 by the **L5 Actuator** as evidence for a governed request event. It covers reads, observations, mutations, and governance rejections. It binds the transaction ID and hash, request `event_type` and `action_type`, execution status, typed failure and rejection codes, result summary, state roots, timestamp, signer key ID, L2 and L3 verification status, and deterministic stage evidence. L5 signs and persists an initial `EXECUTING` receipt before dispatch, then a final receipt with a **Receipt Persistence Attestation**. A governance rejection produces a signed `FAILED` event receipt without dispatch; a refusal before L4 carries no receipt.

Each receipt stage appends a `ztd.v1.operator.receipt.recorded` chain event whose `content_text` holds the canonical receipt body. The `event_receipt_*` columns mark the current receipt for a transaction; `EventReceiptRecord` is its searchable storage/API projection, with the signed protobuf nested under `event_receipt`. A later non-terminal stage cannot replace a terminal receipt. See [Storage Architecture](../architecture/storage.md#event-receipt).

The Gateway serves event receipts at `/api/v1/audit/event-receipts` and `/api/v1/audit/event-receipts/export`. The CLI uses `ztd audit event-receipts`, and `ztd report all` writes `event_receipts.csv`. The MCP query identifiers `event_receipt_list` and `event_receipt_get` name the event receipt query surface; both read these same event rows through `LocalFirstEventsStore`. Each governed query produces its own event receipt for the query transaction. The executing runtime's Local-First Events Store is authoritative; Gateway copies of remote Operator receipts are best-effort projections.

---

## Evidence Graph

The typed evidence graph (`EvidenceGraph`, `internal/services/compliance/evidence/graph.go`) built by the compliance evidence package. It holds content-addressed `EvidenceNode` artifacts typed by `ArtifactType` and indexed by assessment scope; references must resolve and cycles are rejected. Compliance graders, framework profiles, and renderers evaluate this graph rather than scraping logs or inferring outcomes from filenames.

---

## Evidence Level

The standardized strength hierarchy assigned to compliance evidence:
- **L0 (Documented)**: Architecture, policy, or implementation reference describes the control.
- **L1 (Implemented)**: Focused source and test evidence verifies the implementation contract.
- **L2 (Deterministically Evaluated)**: A registered versioned grader passes against typed evidence.
- **L3 (Demonstrated)**: A real-stack scenario passes with verified receipts and independently observed terminal state.
- **L4 (Continuously Evidenced)**: Fresh historical evidence satisfies the control's declared validation cycle across the assessment window.
- **L5 (Externally Attested)**: An independent assessor or authority accepts the evidence for the declared scope.

The pipeline produces evidence up to L3; L4 requires historical effectiveness stores and L5 requires external attestation. See [Compliance Evidence](compliance-evidence.md).

---

## Execution Record

The `events` table in `ztd.db` (`internal/services/storage/local_first_events_store.go`). It is the sole record of each command and file operation, keyed by `execution_id`. It holds the full raw stdout and stderr, gzip-compressed and encrypted with the local vault, and records content hashes. Nothing truncates it. Egress is scrubbed by the publisher, not by the local record. Full file diffs are not stored; they are computed from the git file ledger on demand, and the `file_mutation_log` table keeps only encrypted diff statistics.

---

## FIPS Approved Mode

The Go Cryptographic Module state of a `ztd` linux/amd64 binary built with `GOFIPS140=v1.0.0` (`make fips-build`). The build links Go Cryptographic Module v1.0.0, which initializes into approved mode by default. The `Makefile` cites it as CMVP Certificate #5247. `ztd version --fips` reports whether approved mode is active and exits non-zero if it is not (`internal/cli/cmd/version/version.go`). `GODEBUG=fips140=only` turns on enforcement; `make fips-verify` runs under it. The compliance claim covers only that build target. See [FIPS 140-3 Reference](fips140-3.md) for scope and limits.

---

## Gateway Peer

A workload identity reserved for Gateway-to-Gateway trust, with SPIFFE URI `spiffe://ztd.local/gateway/<gateway_id>`. The PKI issues certificates from a dedicated `gateway-peer` intermediate CA and includes the CA in the canonical trust bundle.

---

## Governance Envelope

The canonical protobuf container `ztd.common.v1.GovernanceEnvelope` for all governed mutations. It binds:
- **Identity**: `id`, timestamps (`timestamp`, `expires_at`), `source_component`, `operator_id`, `operator_session_id`, `web_session_id`, `cli_session_id`, `requestor_user_id`, and `acting_app_id`.
- **Intent**: routing `event_type`, typed protobuf `payload` bytes, structured `intent_data`, `action_type`, and `target_resource`.
- **State and replay defense**: `state_merkle_root`, `nonce`, `transaction_hash`, and `protocol_version`.
- **Governance proofs**: `governance` (`GovernanceMetadata`, wrapping `L1Metadata`, `L2Metadata`, `L3Metadata`).
- **Application context**: `case_id`, `investigation_id`, `task_id`, `system_fingerprint`, `tenant_id`, and `binding_persona`.
- **Policy metadata**: the Gateway-selected governance `posture`.

Client-facing surfaces serialize the envelope with protojson. The deterministic transaction hash covers normalized intent, state, and identity fields; posture is policy metadata and is excluded from transaction hash canonicalization.

---

## Governance Gateway

The platform coordinator and Policy Decision Point (PDP) (`ztd gw`). It authenticates workloads, manages the PKI hierarchy, persists shared platform state in `ztd.db`, admits envelopes, executes L1 through L3 policy decisions, and brokers Operator pub/sub channels. For actions targeting the Gateway host, an in-process Operator substrate runs L4 and L5 locally. The Gateway holds no privileged bypass around the governance pipeline and never initiates inbound connections to remote Operators.

---

## Governance Posture

The startup-selected policy mode that determines whether L2 Consensus and L3 Notary operate as fail-closed gates or audited observations. L1 Doctrine and universal integrity checks remain enforced in every posture.

| Posture | L1 Doctrine | L2 Consensus | L3 Notary (Mutations) |
| :--- | :--- | :--- | :--- |
| **Doctrine** | Enforced | Audited | Audited |
| **Consensus** | Enforced | Enforced | Audited |
| **Ratify** | Enforced | Audited | Enforced |
| **Notary** | Enforced | Enforced | Enforced |

L2 Consensus gates only state-changing, non-bootstrap actions (`L2Enforced`, `internal/services/governance/l2_consensus.go`); read-only and bootstrap actions are not gated by L2 in any posture. Read-only actions do not require L3 proof in any posture. The Gateway stamps the active posture into `envelope.posture` at admission time; the L4 Warden evaluates against that envelope field during verification.

---

## Governed Document

A collection marked `_governed` in `protocol/constants/collections.json`, which lets `DOCUMENT_UPDATE` and `DOCUMENT_DELETE` envelopes target it (`CollectionName.IsGovernedDocument` in `internal/constants/collections.go`). Application records such as cases and tasks are governed documents. Platform-authority collections, such as `users`, `trusted_signers`, `app_policies`, and `revoked_certificates`, are not, and are written only by their owning Gateway service. A document action against any other collection fails closed with `ErrTxDocumentCollectionNotGoverned` (INV-GOV-WARD-06 in [Governance Architecture](../architecture/governance.md)).

---

## Governed Operator

The Policy Execution Point (PEP) deployed on a target host (`ztd operator`). It opens no inbound listening ports, establishes an outbound-only mTLS connection to the Gateway, pulls work from its session-specific channel (`cmd:<operator_id>:<operator_session_id>`), and re-runs L1 Doctrine and verifies L2 Consensus and L3 Notary locally before executing through L4 Warden and L5 Actuator. It is the sole component authorized to mutate its host and retains host-authoritative audit and file-history evidence.

---

## Heartbeat

Periodic health and status telemetry sent by a Governed Operator to the Gateway over its outbound connection. `HeartbeatRequested` is an empty trigger; `HeartbeatResult` (`ztd.operator.v1.HeartbeatResult`) reports operator identity, uptime, OS, architecture, CPU count, memory and disk metrics, network interfaces, runtime environment, and system fingerprint. The separate native `sys_info` MCP tool reports OS version, kernel version, uptime, and load average where available; Windows reports load average as `unknown`. The Gateway monitors heartbeats to maintain operator liveness records.

---

## Inference Dispatch (Governed Inference)

The governed model-inference dispatch subsystem (`internal/services/inference/`). It routes model generation requests (`InferenceDispatchRequest`) to the Inference Operator's provider backend (Ollama). The dispatcher rejects an unspecified model role (`InferenceModelRole`), enforces campaign model-registry bindings and SHA-256 model digests (`VerifyModelRegistryBinding`), and returns token usage, finish reason and timing in an `InferenceResult` wrapped in an `InferenceDispatchResponse`. Dispatched inference requests are governed envelopes and pass through the L1–L5 pipeline and model-boundary token scrubbing.

---

## KSI (Key Security Indicator)

A typed, machine-evaluable security requirement defined in the FedRAMP 20x catalog (`docs/reference/ksi-catalog.json`). KSIs span ten operational categories: `CED` (Cybersecurity Education), `CMT` (Change Management), `CNA` (Cloud Native Architecture), `IAM` (Identity and Access Management), `INR` (Incident Response), `MLA` (Monitoring, Logging, and Auditing), `PIY` (Policy and Inventory), `RCP` (Recovery Planning), `SVC` (Service Configuration), and `TPR` (Supply Chain Risk). The KSI evaluator derives status (`satisfied`, `not_satisfied`, `unverifiable`, `not_applicable`) from registered automated methods and typed evidence artifacts.

---

## L1 Doctrine (L1Doctrine)

The technical hard-gate governance layer (`internal/services/governance/l1_doctrine.go`). It validates decoded typed payloads against protobuf field-option regex rules (`forbidden_patterns`), bundled JSON doctrine sets, and deterministic threat analysis covering shell commands and MCP arguments, including MITRE ATT&CK patterns. L1 is enforced across all governance postures. The executing Operator's **L4 Warden** runs it on every envelope before dispatch. The Gateway also runs it on governed HTTP dispatch (`DispatchService.Dispatch`); MCP, A2A, and direct envelope submission have no Gateway-side L1 call.

---

## L2 Consensus (L2Consensus)

The multi-signature governance layer (`internal/services/governance/l2_consensus.go`). Each enrolled member signs an Ed25519 vote over `<transaction_hash>|<decision>`. The L4 Warden verifies signer trust, consensus policy membership, signature validity, signature distinctness, and affirmative quorum. L2 is a required gate under `consensus` and `notary` postures for state-changing, non-bootstrap actions only; under `doctrine` and `ratify`, available L2 votes are verified and recorded in the audit trail without gating execution. The bound consensus provider is an enrolled application or the Gateway-hosted **Tribunal**.

---

## L3 Notary (L3Notary)

The human-authorization layer for mutations under `ratify` and `notary` postures (`internal/services/governance/l3_notary.go`). Gateway mode verifies a WebAuthn passkey assertion against the transaction hash challenge; CLI-originated proofs additionally bind the authenticated CLI session ID and certificate fingerprint. Outbound mode verifies the same proof types against the Operator's pinned Gateway CA trust: a WebAuthn passkey assertion, or an enrolled CLI certificate signature (ECDSA P-256) over the transaction hash. No local suspended-transaction record or Gateway-issued authorization substitutes for the human proof. Read-only actions do not require L3 proof in any posture; `doctrine` and `consensus` postures audit L3 proofs when provided without gating execution.

---

## L4 Warden (L4Warden)

The fail-closed pre-dispatch verifier on the Operator substrate (`internal/services/governance/l4_warden.go`). It executes a strict five-step verification sequence:
1. Reserves the in-flight lock and durably reserves the nonce in the SQLite replay store (`ReserveNonce`), verifying expiry.
2. Performs stateless validation: verifies envelope structure, decodes payload, ensures action type and payload match, recomputes the transaction hash, and re-runs L1 Doctrine.
3. Performs state binding: requires a nonempty `envelope.state_merkle_root` and compares it with the admission snapshot carried in context for Gateway-dispatched envelopes (`ContextKeyStateMerkleRoot`); that root is itself bound by the transaction hash recomputed in step 2 and the proofs verified in step 4. Direct submissions without dispatch context must match the current local root from `StateRootProvider`. Outbound verification performs no Gateway state lookup.
4. Performs posture validation: reads `envelope.posture` and verifies L2 Consensus votes and L3 Notary proofs according to posture rules.
5. Emits linked `DeterministicStageEvidence` and returns a `VerifiedTransaction` to L5 Actuator.

Verification failure at any step releases the nonce reservation and halts the transaction.

---

## L5 Actuator (L5Actuator)

See **Actuator**.

---

## Ledger

The git-backed file-mutation history maintained by the Governed Operator (`internal/services/storage/ledger.go`, `GitLedgerService`). The ledger is a required Operator dependency, built on embedded go-git: file operations fail with `ErrLedgerRequired` without one, and there is no flag to disable it. Each Operator session operates within an isolated git repository beneath the runtime ledger directory. For every governed file mutation, the ledger creates an encrypted pre-mutation snapshot and commit, applies the mutation, and commits the encrypted post-mutation state. File content is stored only as vault-encrypted `.enc` snapshots, so a locked vault fails mirroring with `ErrLedgerVaultRequired`, and diffs are computed from decrypted snapshots in memory on demand. The ledger supports history inspection, point-in-time retrieval, and file restoration. It is distinct from the SQLite **Commitment Ledger**.

---

## Local-First Events Architecture (LFEA)

The architectural model in which each target host remains the authoritative root of trust for its own execution evidence, file history, and host effects. The local events database (including execution records), commitment ledger, and git file ledger preserve the raw forensic artifacts required to reconstruct and verify execution. The Gateway coordinates platform workflows and relays commitments, while sensitive execution details remain bound to the Operator host.

---

## Local-First Events Store

The storage service (`internal/services/storage/local_first_events_store.go`, `LocalFirstEventsStore`) for Operator sessions, the append-only `events` chain, file mutations, deterministic stage evidence, and persistence attestations in the runtime's canonical `ztd.db`. It uses the same database connection owned by `CanonicalDBService`; it is not a separate events database or a second copy of the local events. Each **Event Receipt** stage appends a chained event; searchable `event_receipt_*` columns mark the current receipt for each transaction (`internal/services/storage/event_receipt.go`). There is no separate receipts table. Every content column (event text and command fields, event receipt bodies, file paths and diff stats, session titles) is encrypted at rest using AES-256-GCM through the local vault (`vault.Vault`), with no plaintext branch: a locked or absent vault fails the write with `ErrLocalFirstEventsStoreVaultLocked`. Records are scoped to existing sessions: record paths never create a session row, and an unknown session fails with `ErrEventsStoreSessionUnknown`. The owning runtime starts its session with `StartSession` (atomic and idempotent) and marks it ended with `EndSession`. Retention prunes records older than `RetentionDays` (default 90) and prunes only ended sessions past retention that no event row references (receipts live on event rows), so a live session is never pruned. In Gateway mode, these tables are part of `ztd.db`; each remote Governed Operator retains its own authoritative local events database.

---

## MCP (Model Context Protocol)

The JSON-RPC tool protocol exposed by ztd for AI clients (`internal/services/mcp/`). The Gateway handles tool discovery and calls, converts tool requests into typed Operator protobuf payloads wrapped in **Governance Envelopes**, and returns structured results or approval suspension responses. Native Operator tools and configured downstream MCP servers execute only after passing through the governance pipeline.

---

## Model Roles (Primary, Assistant, Lite)

The three typed model tiers used by governed inference requests (`internal/models/inference.go`, `InferenceModelRole`) and eval-campaign assignments (`internal/models/observe.go`, `ModelRole`). Governed inference carries the tier as the protobuf enum `ModelRole` (`MODEL_ROLE_PRIMARY`, `MODEL_ROLE_ASSISTANT`, `MODEL_ROLE_LITE`) and eval protobufs as `ModelCampaignRole`. Eval-campaign records, the eval CLI and public evaluation views use the lowercase wire values below, and user-facing copy uses the title-case display labels (`evaluation-explorer/src/content/roles.ts`):

| Wire value | Display label | Meaning |
| :--- | :--- | :--- |
| `primary` | Primary | Primary model tier |
| `assistant` | Assistant | Assistant model tier |
| `lite` | Lite | Lite model tier |

---

## Mutual TLS (mTLS)

Bidirectional TLS authentication enforced across all protected transport surfaces. Gateway mTLS listeners and mTLS clients require TLS 1.3, and certificates use URI Subject Alternative Names (SANs) under the `ztd.local` trust domain to authenticate workload and session identities, encrypt data in transit, bind callers to envelope claims, and enforce certificate revocation. Workload certificates authenticate communicating processes, while binary build provenance is verified separately.

---

## Observed-State Root

The root of the observed tree of the incremental state commitment, read through `StateRootService.GetObservedStateRoot(ctx)`. It covers observed-tier `kv_store` and `blobs` rows (`state_tier = 'observed'`); expired rows stay committed until maintenance deletes them. It does not gate transaction admission freshness, allowing high-frequency telemetry and environmental observations to remain tamper-evident in audit records without invalidating in-flight envelopes.

---

## Operator Run Dispatch

The enrolled-CLI automation path for governed shell execution across one or more remote Operators (`ztd operator run`). The CLI posts dispatch requests to `POST /api/v1/operators/commands` with explicit target operator session IDs or `--all-active`, fanning out `EXECUTE_BASH` in parallel up to `--concurrency`. The Gateway constructs the envelope, screens the payload with L1 Doctrine, applies posture-aware L3 gating, publishes to each target's `cmd:` channel, and returns stdout, stderr, and exit codes to the caller upon completion.

---

## OperatorSession

The runtime authorization and execution context of an active Governed Operator, identified by `operator_session_id`. It scopes the Operator's SPIFFE workload identity, pub/sub communication channels (`cmd:<operator_id>:<operator_session_id>`), audit records, and isolated git ledger repository. The Governed Operator holds it. Its mTLS certificate carries the session in the SPIFFE URI SAN, and `handleOperatorAuth` in `internal/services/gateway/gateway_auth.go` matches that SAN against the linked Operator document (`ValidateOperatorSession`). The Gateway persists the session in the `operator_sessions` collection (`constants.CollectionOperatorSessions`) through `OperatorSessionService` (`internal/services/gateway/operator_session_service.go`) and starts a matching `sessions` row in the Local-First Events Store. It is disjoint from **CLISession** and **WebSession**.

---

## OSCAL (Open Security Controls Assessment Language)

The NIST machine-readable JSON format (OSCAL 1.1.2) supported by the compliance reporting subsystem. ztd projects canonical compliance assessments into OSCAL 1.1.2 assessment results and validates output against official OSCAL schemas. OSCAL is an output projection format; assessment decisions are derived from canonical typed evidence graphs.

---

## PKI (Public Key Infrastructure)

The Gateway-managed X.509 certificate authority tree (`PKIAuthority`, `internal/services/gateway/gateway_certs.go`). It establishes root and intermediate authorities, issues short-lived workload certificates with SPIFFE URI SANs, publishes trust and revocation bundles, and manages Operator, CLI, app, user, hub, and Gateway-peer identities. Enrolling workloads generate and retain their own private keys.

---

## Platform Enrollment

The owner-approved protocol for enrolling Operator and application component instances (`internal/services/gateway/platform_enrollment_service.go`). Reserved component-kind values are never reused. A component generates private keys, submits Certificate Signing Requests (CSRs) and fingerprints, and awaits an approval decision. To complete an approved request, the component proves possession of every private key by signing a canonical completion transcript (`PlatformEnrollmentCompletionTranscript`) that binds the protocol version, request ID, token hash, component kind, instance ID, and key fingerprints before certificates are issued.

---

## Principal

The identity the transport establishes for a request: the SPIFFE URI SAN of the mTLS client certificate (CLI, Operator, and app workloads), or a validated WebSession cookie (`ValidateWebSessionCookie`, `internal/services/gateway/gateway_auth.go`). For a human user the principal takes the User Principal form `spiffe://ztd.local/user/<user_id>` (`UserSPIFFEID`, `protocol/workload_identity.go`). Principal identity is not the envelope's authority to act: the envelope carries the delegator as `requestor_user_id` and the acting application as `acting_app_id`, and the governance pipeline still evaluates the action.

---

## Producer

An app workload that reports agent and run state changes to the Gateway through the mTLS observe producer endpoints (`ObserveProducerController`, `internal/services/gateway/observe_producer_controller.go`). The Gateway derives `user_id` from the delegated user SAN on the peer certificate, never from the request body, and rejects Operator and CLI certificates. Producer reports feed the SSE event streams; see [SSE](../architecture/sse.md#observe-producers).

---

## Provenance Operator

A Governed Operator started with the `provenance` capability (`OperatorCapabilityProvenance` in `internal/constants/status.go`). It acts as an independent witness: it reads the model weight store set by `--model-storage-root` and attests that the served model tag maps to the weights named by the expected digest. See [Model Provenance](../architecture/model-provenance.md).

---

## Receipt Failure Code

A strongly typed classification enum (`ReceiptFailureCode`, `protocol/proto/ztd/operator/v1/operator.proto`) stamped on an `EventReceipt` when execution status is `EXECUTION_STATUS_FAILED`. Values distinguish governance rejections (`RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED`), model configuration errors (`RECEIPT_FAILURE_CODE_MODEL_OVERRIDE_DENIED`, `RECEIPT_FAILURE_CODE_ROLE_INVALID`, `RECEIPT_FAILURE_CODE_MODEL_REF_INVALID`), inference backend faults (`RECEIPT_FAILURE_CODE_BACKEND_UNAVAILABLE`, `RECEIPT_FAILURE_CODE_BACKEND_TIMEOUT`, `RECEIPT_FAILURE_CODE_GENERATE_FAILED`), missing models (`RECEIPT_FAILURE_CODE_MODEL_NOT_FOUND`), provider format errors (`RECEIPT_FAILURE_CODE_PROVIDER_RESPONSE_INVALID`), prompts that fill the model context window (`RECEIPT_FAILURE_CODE_CONTEXT_OVERFLOW`, returned by the Gateway as HTTP 422), general execution failures (`RECEIPT_FAILURE_CODE_EXECUTION_FAILED`), and payloads withheld because a UEI placeholder could not be rehydrated (`RECEIPT_FAILURE_CODE_REHYDRATION_FAILED`). The failure code is bound into the receipt signature.

---

## Receipt Persistence Attestation

The canonical protobuf `ztd.operator.v1.ReceiptPersistenceAttestation`, signed by the Actuator key together with the final `EventReceipt` and persisted with it in the same Local-First Events Store write; the attestation is dropped if that write fails. It binds the transaction ID, receipt signature digest, persistence timestamp, audit record ID, signer key ID, and signature. Upstream receipt relays verify both the receipt signature and this persistence attestation.

---

## Replay Protection

The multi-tiered defense that prevents duplicate or replayed transactions from executing. The L4 Warden reserves each envelope's nonce in-flight, durably records it with expiration tracking through `ReserveNonce` in the SQLite replay store (`storage.ReplayStore` over the `nonces` table in the runtime's own `ztd.db`), recomputes and verifies the deterministic transaction hash, and rejects expired or duplicated transactions. Failed transactions release their nonce reservations.

---

## Requestor User ID

The `requestor_user_id` field in a **Governance Envelope**. It identifies the human delegator who authorized the action and pairs with `acting_app_id`, which identifies the application, tool, or agent.

---

## Sealed Identity Key

A client private key stored under the keystore master key instead of as PEM: the CLI key (`.ztd/cli.key`), an app key (`.ztd/apps/<name>.key`), or the pending app enrollment state that carries a key and token. It is bound to its runtime-relative path, unsealed in memory only, and refused if a plaintext file occupies its place (`ErrIdentityFilePlaintext`). See `docs/architecture/encryption.md` (`INV-ENC-STORE-06`).

---

## Sovereign Execution Boundary

The Operator-local scrubbing and rehydration architecture implemented by `ScrubbingService` (`internal/services/scrubbing/`). Egress traffic leaves the host only after sensitive values are tokenized (`{{UEI_<owner>_N}}`, where `<owner>` names the minting runtime) or redacted; persistent token mappings are encrypted at rest with the local vault; the token keymap hash can participate in the Bound State Root; and L5 rehydrates reversible tokens strictly on the target execution host immediately before command dispatch.

---

## SSE (Server-Sent Events)

The Gateway real-time event delivery mechanism for browser and CLI sessions. The Gateway publishes session-scoped events from its own services and from the observe producer endpoints; consumers stream `/api/v1/sse/stream` (with `since_id` or `Last-Event-ID` replay) using mTLS or web-session cookies; `/api/v1/sse/events` returns one page of stored events after a `since_id` cursor for backfill. Events are buffered in the `sse_events` table in `ztd.db` for reconnection replay. Governed Operator command transport uses outbound mTLS and pub/sub routing rather than SSE.

---

## State Root

A deterministic SHA-256 digest representing authoritative state. `envelope.state_merkle_root` binds the admission snapshot under which the Gateway constructed a transaction; outbound Operators verify that envelope locally without re-fetching the Gateway root or substituting their own root. Direct submissions without Gateway-dispatch context must match the executing runtime's current authoritative root. L5 receipt roots describe the executing runtime's local state before and after execution. See **Bound State Root** and **Observed-State Root**.

---

## State Tier

The classification applied to Gateway KV and blob storage rows:
- **Bound** (`state_tier = 'bound'`): Authoritative state included in the admission-gating Bound State Root.
- **Observed** (`state_tier = 'observed'`): Telemetry (for example Operator heartbeats) and encrypted token mappings, excluded from the bound rows and hashed into the separate Observed-State Root. The bound root still folds in the token keymap hash.

Documents are authoritative by definition and always participate in the bound root.

---

## Suspended Transaction

A Governance Envelope paused while awaiting required L3 Notary human approval (`internal/services/storage/suspended_transaction_store.go`, the `suspended_transactions` table in the runtime's `ztd.db`). The store retains the envelope and tool arguments (vault-encrypted; a locked vault fails the write and the read), expiry, execution claim, caller bindings, and the targeted Operator session, whose audit chain records the approval request and expiry. It stores no approval decision or proof. Upon human approval via passkey or signature, the Gateway resumes governance evaluation with the proof in the envelope, and the executing Operator's signed receipt is the evidence of the outcome. The Gateway maintenance sweep records each expiry on that session's chain before deleting the record, and a successful resumed execution removes its record.

---

## System Fingerprint

A SHA-256 host identifier (`internal/services/auth/fingerprint.go`, `SystemFingerprint`) derived from operating system, CPU architecture, core count, machine ID, and hostname. It is stable only while the machine ID is readable; otherwise the literal `fallback` stands in for it. The generator supports operator-differentiating parameters (`local_dir`, `account`, `port`, `capabilities`) so multiple Operator instances can coexist on the same physical host without collision.

---

## Tactical Governance Console (TUI)

The terminal user interface launched via `ztd tui` (`internal/cli/cmd/tui/`, implemented in `internal/cli/tui/`). It runs as the enrolled CLI session (`ztd auth enroll user`): the same mTLS identity, CLI session header, and live SSE stream as every other CLI command. Its header shows the CLI identity (user, CLI session, bound Operator) and the Gateway's governance posture, marking L2 and L3 as audited when the posture does not enforce them. Number keys switch between views. The overview shows the L1–L5 execution pipeline, an audit ledger of the session's stream events and TUI action results, the pending L3 approval queue, and the connected Operators. The Operators view lists connected Operators; Enter opens a scrollable detail view using the same heartbeat snapshot as `ztd operator show`, Esc returns to the list, `b` binds the CLI session to the selected Operator, and `u` clears the binding; binding changes keep the same CLI session ID, refresh the live session from the updated local credentials, and reconnect SSE. The Audit view lists paged Gateway audit events, summary totals, and on-demand hash-chain verification using the same endpoints as `ztd audit events`, `ztd audit summary`, and `ztd audit verify`. The Status view shows Gateway health, connected Operators, and completed platform-enrolled workloads using the same health and enrollment data as `ztd gw status`. The Recovery view accepts a one-time headless CLI recovery token and submits the same approve/deny request as `ztd auth approve-recovery`, after confirmation. Other views show the approval queue and the platform enrollments (owner only). Pending approvals and enrollments are re-listed on every stream connect and on each `ztd.v1.platform.approvals.changed` event for their subject. Approving a selected transaction opens the same browser WebAuthn page as `ztd auth approve` and then verifies the result over mTLS. Enrollment decisions and revocations make the same mTLS requests as `ztd auth enroll approve|deny|revoke`, and stopping a remote Operator makes the same governed shutdown request as `ztd operator stop <session_id>`, each after a y/N confirmation. The Operator list is re-listed on every stream connect and on each `ztd.v1.operator.status.updated.*` event; each `ztd.v1.operator.heartbeat.received` event updates the listed Operator in place, and stale status is derived from `stale_at` at render time. For views and controls, see `ztd tui --help`.

---

## Transaction Hash

The deterministic SHA-256 digest computed from normalized **Governance Envelope** fields via `GenerateMessageID(env)`. The envelope `id` and `transaction_hash` must both match this value. The calculation dispatches on protocol version: V1 hashes the canonical field string; V2 hashes `ztd-tx-v2|<action_type>|<event_type>|<V1 canonical string>` and requires a nonempty event type. L2 consensus votes, L3 human approvals, replay checks, capabilities, receipts, and commitment attestations bind cryptographically to this hash. Dynamic policy metadata, including the active governance posture, is excluded from hash canonicalization.

---

## Tribunal

The Gateway-hosted L2 consensus panel, selected as the `tribunal` consensus provider (`ConsensusProviderTribunal`, `internal/constants/consensus.go`). The panel binds the canonical Tribunal member applications, each member votes through one governed inference, and the panel is pinned to one frozen model tag and digest at enrollment (`ztd consensus enroll tribunal`). The default quorum is 3-of-5 (`TribunalConsensusDefaultQuorum`); the lowest accepted quorum is also 3 (`TribunalConsensusMinQuorum`). Tribunal votes apply only to state-changing actions under **L2 Consensus**. In the Evaluation Explorer's Live activity table, each assignment row that received votes shows the five Tribunal seats, with outcome and the recorded vote, between its Model and Latency columns.

---

## Vault

The local AES-256-GCM encryption service and key management hierarchy (`internal/services/vault/`, `vault.Vault`). It secures sensitive audit fields (including execution events) and encrypted KV token records. The Local-First Events Store and encrypted KV token records fail closed when the vault is locked or uninitialized; the file ledger refuses to mirror while the vault is locked and never writes plaintext. The encryption vault is distinct from the **Execution Record**, which is the `events` table in `ztd.db`.

---

## WebSession

The browser-authenticated session that the Gateway issues after a passkey ceremony (`internal/models/auth.go`, `WebSession`). The browser user of the embedded console (`console/`) holds it. `PasskeyHandler.RegisterVerify` and `PasskeyHandler.AuthenticateVerify` (`internal/services/gateway/passkey_service_http.go`) call `WebSessionService.CreateWebSession` (`internal/services/gateway/web_session_service.go`), which persists the document in the `web_sessions` collection (`constants.CollectionWebSessions`) of the Gateway document store. The lifetime is `constants.WebSessionTTL` (24 hours). The Gateway sets the session ID as the value of the `ztd_web_session_cookie` cookie (`constants.WebSessionCookieName`; HttpOnly, Secure, SameSite=Lax, or SameSite=None for cross-origin). `ValidateWebSessionCookie` in `internal/services/gateway/gateway_auth.go` admits a request only when the cookie names an unexpired document whose user is active. The cookie value is the web session ID (`ContextKeyWebSessionID`), because the stored body does not carry its own ID. Logout deletes the document and clears the cookie. A WebSession does not authenticate a CLI or Operator; those use mTLS certificates (see **CLISession** and **OperatorSession**).

---

## Workload Identity

A SPIFFE URI Subject Alternative Name encoded in an X.509 certificate under the `ztd.local` trust domain (`protocol/workload_identity.go`). Formats are:
- **Operator**: `spiffe://ztd.local/operator/<organization_id>/<operator_id>/<operator_session_id>`
- **CLI**: `spiffe://ztd.local/cli/<user_id>/<cli_session_id>`
- **App**: `spiffe://ztd.local/app/<operator_id>` 
- **User**: `spiffe://ztd.local/user/<user_id>`
- **Hub**: `spiffe://ztd.local/hub/operator-listen`
- **Gateway Peer**: `spiffe://ztd.local/gateway/<gateway_id>`

The transport and authorization layers match these URI SANs against session records, role definitions, and envelope identity fields.

---

## ztd Binary

The statically linked Go executable (`cmd/ztd/main.go`) that operates as a **Governance Gateway** (`ztd gw`), **Governed Operator** (`ztd operator`), or unified administrative tool (`ztd auth`, `mcp`, `vault`, `test`, `audit`, `report`, `compliance`, `eval`, `tui`). The selected subcommand determines the role; both Gateway and Operator share the same underlying protocol contracts and governance substrate.

---

## ztd Protocol

The canonical wire contract and invariant set for ztd (`protocol/`). It defines protobuf schemas (`ztd.common.v1`, `ztd.operator.v1`, `ztd.compliance.v1`, `ztd.eval.v1`, `ztd.pubsub.v1`, `ztd.tribunal.v1`), Go protocol libraries and TypeScript bindings, JSON constant registries (`protocol/constants/`, such as `events.json`, `status.json`, and `intents.json`), JSON model schemas, doctrine definitions, SPIFFE workload-identity helpers, receipt verification routines, and compliance crosswalks. Client-facing protobuf messages use protojson on wire boundaries.

---

## ztdg / ztdo

Shorthand labels for the two runtime roles, not separate binaries. `ztdg` is the Governance Gateway (`ztd gw`), and `ztdo` is the Governed Operator (`ztd operator`). Both run from the single `ztd` executable. The labels appear in the CLI root help (`internal/cli/cmd/main.go`), the Gateway command help (`internal/cli/cmd/gw/gateway.go`), the `ztdo` component name constant (`internal/constants/status.go`), and the Gateway CA bundle filename `ztdg-ca-bundle.pem` (`internal/constants/paths.go`).

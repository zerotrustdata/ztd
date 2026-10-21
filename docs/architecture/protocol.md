---
doc_id: protocol
title: Protocol Architecture
audience: maintainers and coding agents
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - protocol/
  - internal/constants/
  - internal/services/governance/
  - internal/tools/versionsync/
  - buf.gen.yaml
  - Makefile
related:
  - docs/architecture/governance.md
  - docs/architecture/consensus.md
  - docs/architecture/network.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/events.md
  - docs/architecture/agents.md
  - docs/devs/docs.md
  - docs/devs/release_process.md
when_to_read: Understanding the ztd wire contract, the Go and TypeScript protobuf packages, JSON constant registries and schemas, workload identity (SPIFFE), canonical serialization and conformance vectors, and version synchronization.
do_not_use_for:
  - Five-layer governance verification pipeline (docs/architecture/governance.md)
  - L2 machine consensus threshold policies and deliberation (docs/architecture/consensus.md)
  - Platform PKI, TLS/mTLS, and network port topology (docs/architecture/network.md)
  - Event payload taxonomy and streaming transport routing (docs/architecture/events.md)
  - General documentation formatting and maintenance rules (docs/devs/docs.md)
---

# Protocol Architecture

## Purpose

Documents the wire contracts, serialization specifications, code generation pipelines, and verification mechanisms of the ztd protocol library. The protocol library establishes the canonical wire contract for all governed transactions entering the platform through a ztd ingress. It provides Protobuf schemas and compiled bindings for Go and TypeScript, authoritative JSON constant registries, JSON Schema model definitions, SPIFFE workload identity helpers (`protocol/workload_identity.go`), and conformance vectors. Receipt canonicalization, Ed25519 receipt verification, and envelope transaction hashing are implemented in `internal/services/governance` and `internal/governance` and pinned by those vectors.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Wire Protocol and Protobuf Compilation](#wire-protocol-and-protobuf-compilation)
- [Canonical JSON Serialization](#canonical-json-serialization)
- [Envelope Hashing and Parity Vectors](#envelope-hashing-and-parity-vectors)
- [Shared Constants and Model Registries](#shared-constants-and-model-registries)
- [Workload Identity (SPIFFE)](#workload-identity-spiffe)
- [Version Synchronization and Release](#version-synchronization-and-release)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

Key invariant groups: [Version Synchronization and Release](#version-synchronization-and-release-inv-prot-ver), [Protobuf and Wire Schemas](#protobuf-and-wire-schemas-inv-prot-schema), [Constants Registries and Schemas](#constants-registries-and-schemas-inv-prot-const), [Workload Identity](#workload-identity-inv-prot-id), [Canonicalization, Receipts, and Vectors](#canonicalization-receipts-and-vectors-inv-prot-canon).

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Version Synchronization and Release (`INV-PROT-VER`)

| ID | Rule |
| --- | --- |
| INV-PROT-VER-01 | The root `VERSION` file is the sole source of truth for platform and protocol version numbers. The declarations that `internal/tools/versionsync` checks MUST remain in exact string synchronization with it: the protocol document headers (`protocol/docs/a2a.md`, `protocol/docs/constants.md`, `protocol/docs/mcp.md`, `protocol/docs/spec.md`) and the `version` declarations in `package.json` and `package-lock.json` for first-party npm packages (`console`, `evaluation-explorer`, `ztd-adapter`, `protocol/node`, and `website`). |
| INV-PROT-VER-02 | Go protocol packages are part of the root module `github.com/zerotrustdata/ztd` (`go.mod`) and are versioned exclusively through root git tags of the form `vX.Y.Z`. External consumers resolve them with `go get github.com/zerotrustdata/ztd@vX.Y.Z`. Release tooling creates no `protocol/v*` tags. |
| INV-PROT-VER-03 | The release target `make release` MUST run from clean `main` at `origin/main` after a successful release-workflow rehearsal for that commit. It MUST fail closed if version synchronization leaves uncommitted changes, if the release notes document `docs/release_notes/v<MAJOR_MINOR>.x/v<VERSION>.md` is missing, or if the local tag `v<VERSION>` already exists. |
| INV-PROT-VER-04 | The CI job `Verify Version Sync` in `.github/workflows/build-and-test.yml` MUST run `make version-sync-check` on pushes and pull requests to `main`, terminating the build if any version declaration diverges from `VERSION`. |

### Protobuf and Wire Schemas (`INV-PROT-SCHEMA`)

| ID | Rule |
| --- | --- |
| INV-PROT-SCHEMA-01 | Protobuf schemas in `protocol/proto/ztd/` are the canonical source of truth for wire structures. Generated Go code under `protocol/proto/ztd/` and generated TypeScript code under `protocol/node/src/gen/` MUST NOT be hand-edited and MUST only be updated through `make proto-generate`. |
| INV-PROT-SCHEMA-02 | Client-facing surfaces (HTTP API, WebSocket pub/sub, receipts, audit exports) MUST carry `GovernanceEnvelope` as canonical protojson. Raw binary protobuf is strictly restricted to internal node storage and private gRPC communications. |
| INV-PROT-SCHEMA-03 | Protobuf code generation MUST use Buf configured via `protocol/proto/buf.yaml`, the root `buf.gen.yaml` (Go), and `protocol/node/buf.gen.yaml` (TypeScript). The build MUST rely on `buf generate` rather than requiring a standalone `protoc` compiler binary. |

### Constants Registries and Schemas (`INV-PROT-CONST`)

| ID | Rule |
| --- | --- |
| INV-PROT-CONST-01 | JSON files in `protocol/constants/` are the authoritative data for protocol constants. Go constants in `internal/constants/` (including generated `events_gen.go` and `action_types_gen.go`) and generated TypeScript event constants in `console/src/generated/events.ts` MUST mirror the JSON source of truth and be validated by `make constants-check`. |
| INV-PROT-CONST-03 | Every registered event family in `protocol/constants/events.json` MUST be classified in `protocol/constants/event_dashboard_classification.json` (`produced_to_sse`, `governed_record_only`, `mixed`, or `unsupported`), enforced by `internal/constants/event_dashboard_classification_test.go`. |

### Workload Identity (`INV-PROT-ID`)

| ID | Rule |
| --- | --- |
| INV-PROT-ID-01 | All platform workload identities MUST belong to the `ztd.local` trust domain (`protocol.TrustDomain`) and follow one of six canonical SPIFFE path types: `operator`, `cli`, `app`, `user`, `hub`, or `gateway`. |
| INV-PROT-ID-03 | SPIFFE parsing and extraction methods (`ExtractCLISessionID`, `ExtractUserID`, `ExtractUserIDFromUserSAN`, `ExtractOperatorSessionID`, `ExtractGatewayID`) MUST fail closed and return `("", false)` if prefix matching, segment counting, or non-empty validation fails. |

### Canonicalization, Receipts, and Vectors (`INV-PROT-CANON`)

| ID | Rule |
| --- | --- |
| INV-PROT-CANON-01 | Canonical serialization of `ActionReceipt`, `ReceiptPersistenceAttestation`, compliance records, and evaluation reports MUST be deterministic UTF-8 JSON, pinned by the vectors in `protocol/vectors/` that Go tests consume. |
| INV-PROT-CANON-02 | Ed25519 receipt signature verification in `internal/services/governance` (`VerifyActionReceiptSignature` and `VerifyReceiptPersistenceAttestation`) MUST verify against the bytes produced by `CanonicalizeActionReceipt` using an `ed25519.PublicKey`. |
| INV-PROT-CANON-03 | Vectors in `protocol/vectors/` and `protocol/conformance/` that Go tests consume MUST pass those tests (`./ztd test unit --pkg ./protocol` and `./ztd test unit --pkg ./internal/governance --run TestHashParity`) before any release tag is cut. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Go protocol package | `protocol/go_package.go`, `protocol/workload_identity.go` | `./ztd test unit --pkg ./protocol` |
| Workload identity helpers | `protocol/workload_identity.go` | `./ztd test unit --pkg ./protocol --run TestWorkloadIdentity` |
| Protobuf schemas and specs | `protocol/proto/ztd/`, `buf.gen.yaml`, `protocol/proto/buf.yaml` | `make proto-generate && git diff --exit-code` |
| TypeScript protobuf package | `protocol/node/` | `npm --prefix protocol/node run typecheck` and `npm --prefix protocol/node test` |
| Observe contract generator | `ztd-adapter/generator/gen-contract-pack.mjs` | `npm run gen:contract-pack:check` from `ztd-adapter/` |
| Canonicalization vectors | `protocol/vectors/` | `./ztd test unit --pkg ./protocol` |
| Hash parity vectors | `protocol/conformance/` | `./ztd test unit --pkg ./internal/governance --run TestHashParity` |
| JSON constant registries | `protocol/constants/` | `make doctrines-validate && make constants-check` |
| JSON model schemas | `protocol/models/` | `./ztd test unit --pkg ./internal/models` |
| Third-party OSCAL schemas | `protocol/schemas/oscal/` | `./ztd test unit --pkg ./protocol/schemas/oscal` |
| Version declarations | `VERSION`, `internal/tools/versionsync/`, `protocol/docs/` | `make version-sync-check` |
| Release orchestration | `Makefile` (`release`, `version-sync`), `.github/workflows/release-binary.yml` | `make version-sync-check` |

## Wire Protocol and Protobuf Compilation

The wire protocol defines the structure of all transactions moving through the platform:

```text
Incoming Ingress (MCP / A2A / governed HTTP dispatch / direct envelope submission / CLI operator run)
                      │
                      ▼
         Canonical GovernanceEnvelope (JSON / Protojson)
                      │
                      ├── Header: id, transaction_hash, timestamp, expires_at, nonce, protocol_version
                      ├── Identity: requestor_user_id, acting_app_id, operator_id, operator_session_id, web_session_id, cli_session_id
                      ├── Action: event_type, action_type, target_resource, payload, intent_data
                      ├── Governance: governance.l1, governance.l2, governance.l3 (proof metadata)
                      └── State: state_merkle_root
```

- **Wire Format**: Client-facing endpoints (HTTP, WebSocket pub/sub, receipt logs) transmit `GovernanceEnvelope` as canonical protojson (`INV-PROT-SCHEMA-02`). Raw binary protobuf is used only for internal storage blobs and private gRPC communications.
- **Code Generation via Buf**: Managed entirely through Buf CLI (`protocol/proto/buf.yaml` defines module `buf.build/ztd/platform`):
  - `make proto-go`: Emits Go structs to `protocol/proto/ztd/` using `protoc-gen-go` and generates API documentation to `protocol/docs/reference/api/` using `protoc-gen-doc`.
  - `make proto-node`: Emits TypeScript stubs to `protocol/node/src/gen/` using `@bufbuild/protoc-gen-es`.
  - `make proto-generate`: Runs both targets.

## Canonical JSON Serialization

To allow cross-platform cryptographic signing and verification, ztd enforces strict, deterministic JSON canonicalization:

1. **Receipt Canonicalization**:
   - `internal/services/governance.CanonicalizeActionReceipt` formats `ActionReceipt` into deterministic UTF-8 JSON.
   - Keys follow the declaration order of the internal `canonicalReceipt` struct, not lexicographic order. Enum fields serialize as integers, fields tagged `omitempty` are omitted at zero value, and the output contains no whitespace.
   - Typed failure fields are signed: `failure_code` (`ReceiptFailureCode`) and `rejection_code` (`GovernanceRejectionCode`, `ActionReceipt` field 18). Both are bound into the signature, and `rejection_code` is omitted when `GOVERNANCE_REJECTION_CODE_UNSPECIFIED` (`omitempty`). Enum values are defined in `protocol/proto/ztd/operator/v1/operator.proto`.
   - `GovernanceRejection` (`error`, `receipt`) is the client body for an L4 rejection on every ingress. Ingresses render it with `protojson` proto names, and clients verify `receipt` with `VerifyActionReceiptSignature`.
   - `IngressRefusalCode` and `IngressRefusal` (`code`, `error`) are the pre-envelope refusal body on the direct and dispatch ingresses, and JSON-RPC `error.data` on MCP and A2A. A refusal carries no receipt.
   - `GovernedDispatchResult` (`transaction_id`, `event_type`, `action_type`, `result_payload`, `inference_result`, `receipt`) is the HTTP 200 body of governed dispatch.
   - `InferenceDispatchFailure` (`error`, `code`, `receipt`, `observation_receipts`) is the client body for a non-2xx failure on `POST /api/v1/inference/dispatch`. When dispatch reaches the executing Operator and the provider subsequently fails or governance rejects the execution, `receipt` carries the executing Operator's verified signed `FAILED` receipt (a governance rejection, or a failed execution whose commitment the Operator's ledger holds). `observation_receipts` carries the verified signed observer receipts for the attempt in dispatch order.
   - `VerifyActionReceiptSignature` verifies an Ed25519 signature over these exact canonical bytes.
   - `VerifyReceiptPersistenceAttestation` canonicalizes and verifies persistence attestations.
2. **Evaluation Protojson Canonicalization**:
   - Evaluation messages use `MarshalCanonical` and `UnmarshalCanonical` (`protocol/proto/ztd/eval/v1/canonical.go`).
   - Uses protobuf field names through protojson, compacts whitespace, and omits proto3 zero-value scalar fields.
   - `UnmarshalCanonical` rejects any payload whose raw bytes differ from its canonical re-encoding.
   - `ConformanceRunRecord`, `ConformanceVerdictSet` and their evidence messages (`ConformanceCaseEvidence`, `ConformanceObservation`, `CanaryManifest`, `ConformanceAnchor`) hold the governance-conformance suite: raw evidence per case and the verdict set, kept as separate messages.
   - `AgentRole`, `AgentStep` and `AgentTrace` define the agent trace shape. This protobuf `AgentTrace` is the protobuf projection (`AgentTrace.ToProto`) of the campaign executor's persisted evidence envelope, whose JSON schema is `agent-trace.v8` (`agent-trace.v7` before Tribunal-approved tool arguments were recorded, `agent-trace.v6` before per-call model bindings and complete inference results were recorded, `agent-trace.v5` before investigation identity and authoritative retrieval evidence were recorded, `agent-trace.v4` before held repetition context was recorded, `agent-trace.v3` before exact model input was captured; `internal/services/evaluation/campaign_agent_trace.go`) and which is published as the `ztd.eval.v1.PublicAssignmentAgentTrace` proof artifact with a SHA-256 binding. Campaign runs use the built-in governed executor; ztd defines no external system-under-test exchange.
   - `ContainmentResult` (`verdict`, `breaches`) is the red-team verdict for one attempt, reported apart from model quality.
3. **Canonicalization Conformance Vectors**:
   - Pinned in `protocol/vectors/`:
     - `action_receipt_canonicalization.json`
     - `action_receipt_failure_code_canonicalization.json`
     - `action_receipt_rejection_code_canonicalization.json`
     - `action_receipt_stage_evidence_canonicalization.json`
     - `receipt_persistence_attestation_canonicalization.json`
     - `compliance/` and `eval/` vectors.
   - Verified by `./ztd test unit --pkg ./protocol`.

## Envelope Hashing and Parity Vectors

Every governed transaction has a transaction hash computed over its envelope fields. The envelope `id` and `transaction_hash` both carry this value:

```text
Envelope Fields (action_type, target_resource, payload, state_merkle_root, nonce, expires_at, intent_data, identity and context IDs)
                                  │
                                  ▼
               Canonical UTF-8 Representation (Deterministic)
                                  │
                                  ▼
                     SHA-256 Digest (64-char Hex)
                                  │
                                  ▼
                          Envelope.Id and Envelope.TransactionHash
```

- **Algorithm**: `GenerateMessageID` computes the SHA-256 hash across canonical envelope fields in a deterministic order. Protocol version 2 envelopes add `event_type` and a `ztd-tx-v2|` domain prefix to the digest input.
- **L4 Verification**: The L4 Warden recalculates this hash before dispatch (`verifyTransactionHash` in `internal/services/governance/l4_warden.go`) and rejects any transaction whose `transaction_hash` or `id` does not match the recomputed hash.
- **Hash Parity Vectors**:
  - `protocol/conformance/hash_vectors.json`: Golden vectors for standard envelope hashing.
  - `protocol/conformance/hash_vectors_v2.json`: Golden vectors for protocol version 2 envelopes.
  - Verified by `./ztd test unit --pkg ./internal/governance --run TestHashParity`.

## Shared Constants and Model Registries

The `protocol/constants/` directory maintains the authoritative JSON registries that serve as the single source of truth for the platform:

- `events.json`: Authoritative taxonomy of all platform event types.
- `event_dashboard_classification.json`: Maps events to dashboard stream behaviors (`produced_to_sse`, `governed_record_only`, `mixed`, `unsupported`).
- `internal/constants/action_types_gen.go` and `internal/constants/events_gen.go`: Code-generated Go constants mirroring the JSON registries via `make constants-generate`.
- `console/src/generated/events.ts`: Code-generated TypeScript constants for browser clients.
- `doctrine/`: L1 security heuristic registries (`blacklist_doctrine.json`, `whitelist_doctrine.json`, `gitleaks_doctrine.json`, `owasp_crs_doctrine.json`, `mcp_vectors_doctrine.json`) and the source index `doctrine_registry.json`.

## Workload Identity (SPIFFE)

Platform identities use the canonical SPIFFE trust domain `ztd.local` (`protocol.TrustDomain`):

- **Format**: `spiffe://ztd.local/<type>/<identity-segments>`
- **Types**: `operator`, `cli`, `app`, `user`, `hub`, `gateway`.
- **Parsing**: Implemented in `protocol/workload_identity.go`. Methods fail closed returning `("", false)` if structure or prefix does not match.

## Version Synchronization and Release

All first-party components adhere strictly to the root `VERSION` file:

1. **Check and Sync**: `internal/tools/versionsync` verifies that the `Version:` headers of `protocol/docs/a2a.md`, `constants.md`, `mcp.md`, and `spec.md`, and the `package.json` and `package-lock.json` root versions of first-party npm packages, match `VERSION`. Verified with `make version-sync-check`.
2. **Release Target**: `make release` requires clean `main` at `origin/main` and a successful release-workflow rehearsal for that commit. It runs version sync, requires a clean working tree and release notes, then tags and pushes `v<VERSION>`. See [Release Process](../devs/release_process.md) for the full workflow.

## Anti-patterns

- **Hand-editing generated protobuf code**: Editing `.pb.go` or `.ts` files directly instead of updating `.proto` schemas and running `make proto-generate` (`INV-PROT-SCHEMA-01`).
- **Emitting raw binary protobuf over HTTP/SSE**: Sending binary bytes instead of canonical protojson across public or browser endpoints (`INV-PROT-SCHEMA-02`).
- **Manual version bumping in packages**: Editing package version fields by hand instead of running `make version-sync` (`INV-PROT-VER-01`).
- **Altering canonical hashing without vectors**: Modifying envelope hash algorithms without updating and verifying `protocol/conformance/` vectors.
- **Using non-canonical JSON for signatures**: Computing Ed25519 signatures over raw or non-deterministic JSON instead of `CanonicalizeActionReceipt` output (`INV-PROT-CANON-02`).

## Links out

- [Governance Architecture](governance.md): Five-layer governance verification pipeline (L1-L5), postures, and envelope structures.
- [Consensus Architecture](consensus.md): L2 multi-member Ed25519 signature policies and deliberation.
- [Network Architecture](network.md): Platform PKI, TLS/mTLS, SPIFFE identity SAN issuance, and port topology.
- [Gateway Architecture](gateway.md): Gateway ingress routing, policy decision points, and dispatch mechanics.
- [Operator Architecture](operator.md): Operator runtime, local execution, and L4/L5 verification.
- [Event and Action Protocol](events.md): Event catalog taxonomy, SSE streaming, and payload structures.
- [Release Process](../devs/release_process.md): Detailed release workflows, compliance bundles, and native eval acceptance.
- [Documentation Guide](../devs/docs.md): Standards, invariants, and procedures for ztd documentation.
- [Protocol Specification](../../protocol/docs/spec.md): Canonical envelope structure and 5-layer interlock sequence details.
- [Constants Reference](../../protocol/docs/constants.md): Platform constants system reference.

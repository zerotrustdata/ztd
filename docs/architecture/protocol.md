---
doc_id: protocol
title: Protocol Architecture
audience: maintainers and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
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

Documents the wire contracts, serialization specifications, code generation pipelines, and verification mechanisms of the ztd protocol library. The protocol library establishes the canonical wire contract for all governed transactions entering the platform through a ztd ingress. It provides Protobuf schemas and compiled bindings for Go and TypeScript, authoritative JSON constant registries, JSON Schema model definitions, SPIFFE workload identity helpers, deterministic canonical JSON serialization, Ed25519 receipt verification routines, envelope transaction hashing, and conformance vectors.

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
| INV-PROT-VER-03 | The release target `make release` MUST fail closed if the working tree has uncommitted changes after synchronization, if the release notes document `docs/release_notes/v<MAJOR_MINOR>.x/v<VERSION>.md` is missing, or if the local tag `v<VERSION>` already exists. |
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
| INV-PROT-ID-01 | All platform workload identities MUST belong to the `ztd.local` trust domain (`protocol.TrustDomain`) and follow one of six canonical URI schemes: `operator`, `cli`, `app`, `user`, `hub`, or `gateway`. |
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
Incoming Ingress (MCP / A2A / HTTP / WebSocket)
                      │
                      ▼
         Canonical GovernanceEnvelope (JSON / Protojson)
                      │
                      ├── Header: id (SHA-256), sequence, timestamp, nonce, ttl
                      ├── Identity: client_id, acting_app_id, operator_session_id
                      ├── Action: type, action_payload (typed message)
                      ├── Proofs: l2_consensus_proof, l3_notary_proof
                      └── State: state_merkle_root
```

- **Wire Format**: Client-facing endpoints (HTTP, WebSocket pub/sub, receipt logs) transmit `GovernanceEnvelope` as canonical protojson (`INV-PROT-SCHEMA-02`). Raw binary protobuf is used only for internal storage blobs and private gRPC communications.
- **Code Generation via Buf**: Managed entirely through Buf CLI (`protocol/proto/buf.yaml` defines module `buf.build/ztd/platform`):
  - `make proto-go`: Emits Go structs to `protocol/proto/ztd/` using `protoc-gen-go` and generates API documentation to `protocol/docs/reference/api/` using `protoc-gen-doc`.
  - `make proto-node`: Emits TypeScript stubs to `protocol/node/src/gen/` using `@bufbuild/protoc-gen-es`.
  - `make proto-generate`: Runs both targets atomically.

## Canonical JSON Serialization

To allow cross-platform cryptographic signing and verification, ztd enforces strict, deterministic JSON canonicalization:

1. **Receipt Canonicalization**:
   - `internal/services/governance.CanonicalizeActionReceipt` formats `ActionReceipt` into deterministic UTF-8 JSON.
   - Keys are sorted lexicographically, floats formatted with consistent precision, and whitespace stripped.
   - `VerifyActionReceiptSignature` verifies an Ed25519 signature over these exact canonical bytes.
   - `VerifyReceiptPersistenceAttestation` canonicalizes and verifies persistence attestations.
2. **Evaluation Protojson Canonicalization**:
   - Evaluation messages use `MarshalCanonical` and `UnmarshalCanonical` (`protocol/proto/ztd/eval/v1/canonical.go`).
   - Uses protobuf field names through protojson, compacts whitespace, and omits proto3 zero-value scalar fields.
   - `UnmarshalCanonical` rejects any payload whose raw bytes differ from its canonical re-encoding.
3. **Canonicalization Conformance Vectors**:
   - Pinned in `protocol/vectors/`:
     - `action_receipt_canonicalization.json`
     - `action_receipt_failure_code_canonicalization.json`
     - `action_receipt_stage_evidence_canonicalization.json`
     - `receipt_persistence_attestation_canonicalization.json`
     - `compliance/` and `eval/` vectors.
   - Verified by `./ztd test unit --pkg ./protocol`.

## Envelope Hashing and Parity Vectors

Every governed transaction has an immutable envelope `id` that serves as the transaction hash (`tx_hash`):

```text
Envelope Fields (caller, target, action, payload, nonce, timestamp, state_root)
                                  │
                                  ▼
               Canonical UTF-8 Representation (Deterministic)
                                  │
                                  ▼
                     SHA-256 Digest (64-char Hex)
                                  │
                                  ▼
                          Envelope.Id
```

- **Algorithm**: `GenerateMessageID` computes the SHA-256 hash across canonical envelope fields in a deterministic order.
- **L4 Verification**: The L4 Warden recalculates this hash before dispatch and rejects any transaction where the envelope `id` does not match the recomputed hash.
- **Hash Parity Vectors**:
  - `protocol/conformance/hash_vectors.json`: Golden vectors for standard envelope hashing.
  - `protocol/conformance/hash_vectors_v2.json`: Golden vectors for protocol version 2 envelopes.
  - Verified by `./ztd test unit --pkg ./internal/governance --run TestHashParity`.

## Shared Constants and Model Registries

The `protocol/constants/` directory maintains 26 authoritative JSON registries that serve as the single source of truth for the platform:

- `events.json`: Authoritative taxonomy of all platform event types.
- `event_dashboard_classification.json`: Maps events to dashboard stream behaviors (`produced_to_sse`, `governed_record_only`, `mixed`, `unsupported`).
- `action_types_gen.go` & `events_gen.go`: Code-generated Go constants mirroring the JSON registries via `make constants-generate`.
- `console/src/generated/events.ts`: Code-generated TypeScript constants for browser clients.
- `doctrine/`: L1 security heuristic registries (`blacklist_doctrine.json`, `whitelist_doctrine.json`, `gitleaks_doctrine.json`, `owasp_crs_doctrine.json`, `mcp_vectors_doctrine.json`).

## Workload Identity (SPIFFE)

Platform identities use the canonical SPIFFE trust domain `ztd.local` (`protocol.TrustDomain`):

- **Format**: `spiffe://ztd.local/<type>/<identity-segments>`
- **Types**: `operator`, `cli`, `app`, `user`, `hub`, `gateway`.
- **Parsing**: Implemented in `protocol/workload_identity.go`. Methods fail closed returning `("", false)` if structure or prefix does not match.

## Version Synchronization and Release

All first-party components adhere strictly to `VERSION` (`v1.0.0`):

1. **Check and Sync**: `internal/tools/versionsync` verifies that `protocol/docs/*.md` headers and `package.json` files match `VERSION`. Verified with `make version-sync-check`.
2. **Release Target**: `make release` ensures a clean git working tree, validates version parity, verifies release notes exist, and tags `v<VERSION>`.

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
- [Operator Architecture](operator.md): Operator runtime daemon, local execution, and L4/L5 verification.
- [Event and Action Protocol](events.md): Event catalog taxonomy, SSE streaming, and payload structures.
- [Release Process](../devs/release_process.md): Detailed release workflows, compliance bundles, and native eval acceptance.
- [Documentation Guide](../devs/docs.md): Standards, invariants, and procedures for ztd documentation.
- [Protocol Specification](../../protocol/docs/spec.md): Canonical envelope structure and 5-layer interlock sequence details.
- [Constants Reference](../../protocol/docs/constants.md): Platform constants system reference.

---
doc_id: model-provenance
title: Model Provenance Architecture
audience: engineering, campaign operators, and coding agents
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - internal/services/inference/model_provenance/
  - protocol/proto/ztd/eval/v1/eval.proto
related:
  - docs/architecture/operator.md
  - docs/architecture/evals.md
  - docs/architecture/protocol.md
when_to_read: Designing campaign workflows that require model integrity evidence, coordinating storage-side provenance operators, or verifying model weight hashes against expected digests.
do_not_use_for:
  - Inference governance and capability boundaries (see operator.md)
  - Campaign topology and witness capabilities (see evals.md)
  - Protocol wire contracts (see protocol.md)
---

# Model Provenance Architecture

## Purpose

Model campaigns bind scored inference records to frozen model variants selected for evaluation. When a campaign model specifies a served tag and expected SHA-256 digest, the Gateway coordinates an independent **Provenance Operator** at the model storage host to attest model integrity. That Operator reads the Ollama manifest, verifies content-addressed blobs, hashes weights locally, and returns a typed attestation window bound to the inference `provider_attempt_id`.

This represents evaluation witness evidence, not an authorization gate. The Provenance Operator does not run inference, inspect prompts, sample provider hardware, or authorize execution. The Inference Operator remains the sole execution path to the Ollama endpoint.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Operator Topology and Roles](#operator-topology-and-roles)
- [Attestation Flow and Storage Verification](#attestation-flow-and-storage-verification)
- [Evidence Persistence and Campaign Verification](#evidence-persistence-and-campaign-verification)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

Key invariant groups: [Operator topology](#operator-topology-inv-prov-topo), [Attestation flow](#attestation-flow-inv-prov-attest), [Evidence persistence and verification](#evidence-persistence-and-verification-inv-prov-persist).

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Operator topology (`INV-PROV-TOPO`)

| ID | Rule |
| --- | --- |
| INV-PROV-TOPO-01 | The Provenance Operator does not run inference, sample provider hardware, manage Ollama, or authorize execution. It attests model weight integrity only. |
| INV-PROV-TOPO-02 | The Gateway selects exactly one active Provenance Operator whose runtime configuration `capabilities` includes `provenance`. Zero or multiple matching sessions cause preflight or observation setup to fail. |
| INV-PROV-TOPO-03 | The Observer Operator and Provenance Operator can coexist on the same physical host only when they use separate `ztd operator start` processes and governed sessions. |
| INV-PROV-TOPO-04 | A container on the campaign host cannot authoritatively attest files or hardware on a different provider host. The Provenance Operator remains the storage-side authority. |

### Attestation flow (`INV-PROV-ATTEST`)

| ID | Rule |
| --- | --- |
| INV-PROV-ATTEST-01 | Attestation is triggered only when a campaign model has a non-empty served tag and expected model digest. Commands with missing attempt ID, served tag, or expected digest are not sent. |
| INV-PROV-ATTEST-02 | On FINALIZE, `OllamaStorageAttestor` reads the manifest via `ollama.ParseName`, computes its SHA-256 digest, and verifies every referenced blob under `blobs/sha256-<digest>` through the attestor's blob verifier. |
| INV-PROV-ATTEST-06 | A blob is streamed through SHA-256 once per file identity (file ID, size, modification time, change time). A later window re-reads only that identity and records `MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED` with the `full_hash_completed_at_unix_ms` it rests on; a window that hashed records `MODEL_WEIGHT_VERIFICATION_MODE_FULL_HASH`. A changed or replaced file is hashed again before reuse, a blob whose identity moved during its hash fails attestation, and a hash is reusable only when the blob's identity predates the hash start by `ModelProvenanceBlobIdentitySettleWindow`. |
| INV-PROV-ATTEST-07 | Concurrent windows for one blob share a single in-flight hash, and full hashes across blobs are bounded by `ModelProvenanceHashConcurrency`. Hash cost is per blob change, not per scored inference. |
| INV-PROV-ATTEST-03 | Digest mismatch, missing blob, invalid digest reference, content hash mismatch, or size mismatch fails attestation and prevents publishing a completion window. |
| INV-PROV-ATTEST-04 | The attestation window records `MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED`. Sigstore, Cosign, and SPIFFE/SPIRE attestation tokens are not implemented. |
| INV-PROV-ATTEST-05 | A multi-runner tag (Ollama 0.40+) stores a wrapper manifest whose layers include one `application/vnd.docker.distribution.manifest.v2+json` manifest per runner, and the provider reports the served runner manifest's digest. When the expected digest is not the wrapper's own, the attestor attests the referenced runner manifest blob whose digest equals it: the blob's hash is verified and only its config and layers are attested. A digest the wrapper does not reference is a mismatch. |

### Evidence persistence and verification (`INV-PROV-PERSIST`)

| ID | Rule |
| --- | --- |
| INV-PROV-PERSIST-01 | Gateway-local windows are canonical protojson files owned by the Gateway's runtime file service under `.ztd/data/inference/model-provenance/windows/`, keyed by `provider_attempt_id` (1–128 ASCII letters, digits, `-`, `_`, `.`). They hold identifiers, digests, tags, and attestation metadata only (no prompt or model output) and are private runtime evidence, not public spectator data. |
| INV-PROV-PERSIST-02 | Authenticated owner mTLS clients read windows via `GET /api/v1/inference/model-provenance/attestations/{provider_attempt_id}`, which returns the window wrapped in a `window` field. |
| INV-PROV-PERSIST-03 | Campaign verification validates the window's `attestation_digest`, requires a recognized `verification_mode` and a nonzero `full_hash_completed_at_unix_ms` on every weight attestation, and checks the expected campaign digest binding. `ztd eval runs verify --require-provenance` enforces strict policy, requiring a valid window and `digest_match=true` for every scored inference. |
| INV-PROV-PERSIST-04 | Missing windows are recorded as unavailable telemetry under interim policy. Strict policy requires a valid window for every scored inference. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Provenance Operator capability | `internal/services/operatorcapability/provenance_operator.go` | `./ztd test unit --pkg ./internal/services/operatorcapability` |
| Ollama manifest attestation | `internal/services/inference/model_provenance/attestor.go` | `./ztd test unit --pkg ./internal/services/inference/model_provenance` |
| Protocol surface | `protocol/proto/ztd/eval/v1/eval.proto` | `ModelProvenanceAttestationWindow` schema |
| Attestation window persistence | `internal/services/inference/model_provenance/window_store.go` | `WindowStore.Load` and `WindowStore.Save` implementations |
| Campaign verification | `internal/services/evaluation/campaign_model_provenance.go` | `CampaignModelProvenanceReader.VerifyAssignmentModelProvenance` |
| Gateway API controller | `internal/services/gateway/model_provenance_controller.go` | Route classification in `gateway_auth.go` |

## Operator Topology and Roles

```text
Campaign Coordinator (Gateway)
           │
           ├──────────────────────────────┬──────────────────────────────┐
           ▼ (cmd:*:* channel)            ▼ (cmd:*:* channel)            ▼
   Inference Operator             Provenance Operator            Observer Operator
   (Host A: Scored Client)        (Host B: Model Storage)        (Host C: Hardware Node)
   • Dispatches to Ollama API     • Ollama manifest reader       • GPU / VRAM telemetry
   • Records completion & tokens  • Local weight blob hashing    • S6 resource observation
   • Never hashes storage         • AttestationWindow output     • Never hashes weights
```

- **Inference Operator**: The execution path that invokes Ollama inference and returns token streams and metrics.
- **Provenance Operator**: An independent, read-only witness co-located with Ollama model storage (`~/.ollama/models`). Attests that the served model tag maps to the exact weights specified in the campaign.
- **Observer Operator**: Optional witness for GPU, VRAM, and system metrics.

## Attestation Flow and Storage Verification

```text
1. Campaign Gateway sends BEGIN (provider_attempt_id, served_tag, expected_digest)
                          │
                          ▼
2. Inference Operator executes scored model call against Ollama
                          │
                          ▼
3. Campaign Gateway sends FINALIZE to Provenance Operator
                          │
                          ▼
4. OllamaStorageAttestor reads manifest via ollama.ParseName
   ├── Computes SHA-256 of manifest file -> compares with expected_digest
   ├── Multi-runner tag: attests the referenced runner manifest named by expected_digest
   ├── Parses config and layer blobs (blobs/sha256-<digest>)
   └── Verifies each blob -> checks byte size
       ├── File identity unchanged since a settled hash -> FILE_IDENTITY_UNCHANGED
       └── Otherwise streams it through SHA-256 (shared per blob) -> FULL_HASH
                          │
                          ▼
5. ModelProvenanceAttestationWindow constructed:
   ├── manifest_digest, observed_model_digest, digest_match (boolean)
   ├── ModelWeightAttestation per blob (verification_mode, full_hash_completed_at_unix_ms)
   └── attestation_digest (deterministic SHA-256 over canonical window)
                          │
                          ▼
6. Publishes ModelProvenanceObservationCompleted -> Gateway stores window
```

1. **Tag Resolution**: Resolves library models, tagged variants (`smollm3:3b-q4_k_m`), and Hugging Face deep pulls on the local storage filesystem.
2. **Blob Hashing**: Streams each layer file under `blobs/sha256-<digest>` through SHA-256 the first time it is seen and after any change to its file identity, verifying byte counts against manifest declarations. Every other window reuses that hash after confirming the identity is unchanged, so a campaign hashes a multi-gigabyte model once instead of once per scored inference. Concurrent windows for one blob share its in-flight hash, and at most 2 full hashes run at once to prevent I/O starvation.
3. **Digest Validation**: If any blob hash mismatches or is missing, attestation fails and no completion window is published.

## Evidence Persistence and Campaign Verification

Attestation windows are persisted as canonical protojson files under `.ztd/data/inference/model-provenance/windows/<provider_attempt_id>.json`:

- **Interim Policy**: Missing windows are logged as incomplete telemetry without disqualifying the assignment.
- **Strict Policy (`--require-provenance` / `--require-witness`)**: Strict verification requires a valid, verified window where `digest_match = true` for 100% of scored inferences. A missing window fails the run.

## Procedures

### Starting a Provenance Operator

Run on the storage host hosting the Ollama model directory:

```bash
./ztd operator start \
  --capabilities provenance \
  --provenance-operator-id ztd-model-provenance-1 \
  --model-storage-root ~/.ollama/models
```

### Verifying a Campaign Run with Provenance

```bash
./ztd eval runs verify <run-id> --require-provenance
```

### Inspecting an Attestation Window

```bash
curl -k -H "Authorization: ..." \
  https://localhost:8443/api/v1/inference/model-provenance/attestations/<provider_attempt_id>
```

## Anti-patterns

- **Attesting storage from a remote container**: Running the Provenance Operator on a machine other than the host holding Ollama model blobs (`INV-PROV-TOPO-04`).
- **Combining inference and provenance in one process**: Running a single Operator with both `inference` and `provenance` capabilities (`INV-PROV-TOPO-03`).
- **Claiming cryptographic supply-chain signing**: Expecting Sigstore/Cosign verification; manifests are recorded as `MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED` (`INV-PROV-ATTEST-04`).
- **Proceeding on empty model tags or digests**: Sending attestation commands when tags or digests are blank (`INV-PROV-ATTEST-01`).

## Links out

- [Evaluation Architecture](evals.md): Campaign topology, witness capabilities, and verification rules.
- [Operator Architecture](operator.md): Outbound Operator transport and capability boundaries.
- [Protocol Architecture](protocol.md): Canonical wire types and serialization.
- [SSE Streaming](sse.md): Preflight telemetry and progress updates.

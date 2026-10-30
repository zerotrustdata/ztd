---
doc_id: model-provenance
title: Model Provenance Architecture
audience: engineering, campaign operators, and coding agents
status: current
version: v1.7.0
last_updated: 2026-10-30
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

Campaign verification compares each scored inference record's model variant digest against the attestation window stored for its `provider_attempt_id`. Inference dispatch sends governed BEGIN and FINALIZE commands to a **Provenance Operator** at the model storage host when a request carries both a served model tag and a model digest. That Operator resolves the served tag in its local model store, hashes weights locally, and returns a typed attestation window bound to the inference `provider_attempt_id`. The store is an Ollama model directory (manifest and content-addressed blobs) or a llama.cpp `--models-dir` (GGUF files), selected by `--model-storage-format`.

This represents evaluation witness evidence, not an authorization gate. The Provenance Operator does not run inference, inspect prompts, sample provider hardware, or authorize execution. The Inference Operator remains the sole execution path to the configured model provider endpoint (Ollama or llama.cpp).

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
| INV-PROV-TOPO-02 | The Gateway selects exactly one active Operator that advertises the `provenance` capability. When an embedded session advertises `provenance`, only embedded sessions are candidates. Two or more candidate sessions fail BEGIN, and the attempt fails, for any attempt that carries both a served model tag and a model digest. With zero matching sessions, BEGIN is skipped with a warning and the attempt proceeds without a window. FINALIZE is sent only to an attempt whose BEGIN opened a window. If that FINALIZE finds no provenance Operator, a non-campaign attempt skips it, and a campaign-bound attempt fails. The campaign's model provenance policy decides at verification whether a missing window is acceptable. |
| INV-PROV-TOPO-03 | The Gateway selects a Provenance Operator only by its advertised `provenance` capability. Configuration does not reject a process that combines `provenance` with `inference` or `observer`. Gateway selection prefers embedded sessions over outbound sessions and otherwise does not check which process or host an Operator runs in. |
| INV-PROV-TOPO-04 | The attestor reads the manifest and blobs only from the local model storage root configured for its own Operator process (`--model-storage-root`). It has no remote storage client. |

### Attestation flow (`INV-PROV-ATTEST`)

| ID | Rule |
| --- | --- |
| INV-PROV-ATTEST-01 | Attestation is triggered only for a dispatched inference request that carries a non-empty served model tag and a non-empty model digest. The Gateway does not send BEGIN or FINALIZE when the attempt ID, tag, or digest is empty. |
| INV-PROV-ATTEST-02 | On FINALIZE, `OllamaStorageAttestor` reads the manifest via `ollama.ParseName`, computes its SHA-256 digest, and verifies every referenced blob under `blobs/sha256-<digest>` through the attestor's blob verifier. Under `--model-storage-format llamacpp`, `LlamaCppStorageAttestor` resolves the served tag beneath `--model-storage-root` with llama.cpp's models-dir rules, hashes each GGUF file the router loads through the same blob verifier, and composes the observed digest from the model's shard digests (`llamacpp.ComposeModelDigest`). |
| INV-PROV-ATTEST-06 | A blob is streamed through SHA-256 once per file identity (file ID, size, modification time, change time). A later window checks only that identity and records `MODEL_WEIGHT_VERIFICATION_MODE_FILE_IDENTITY_UNCHANGED` with the `full_hash_completed_at_unix_ms` it rests on; a window that hashed records `MODEL_WEIGHT_VERIFICATION_MODE_FULL_HASH`. A changed or replaced file is hashed again before reuse, a blob whose identity moved during its hash fails attestation, and a hash is reusable only when the blob's identity predates the hash start by `ModelProvenanceBlobIdentitySettleWindow`. |
| INV-PROV-ATTEST-07 | Concurrent windows for one blob share a single in-flight hash, and full hashes across blobs are bounded by `ModelProvenanceHashConcurrency`. Hash cost is per blob change, not per scored inference. |
| INV-PROV-ATTEST-03 | A missing manifest or blob, invalid digest reference, content hash mismatch, or size mismatch fails attestation. A manifest digest that differs from the expected digest produces a window with `digest_match` false, and FINALIZE then fails with a digest mismatch, so no completion window is published. |
| INV-PROV-ATTEST-04 | The Ollama attestation window records `MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED`. The llama.cpp window records `MODEL_MANIFEST_VERIFICATION_STATUS_UNAVAILABLE` and no manifest digest, because a models directory has no manifest. The model provenance code contains no Sigstore, Cosign, or SPIFFE/SPIRE attestation. |
| INV-PROV-ATTEST-05 | A multi-runner tag stores a wrapper manifest whose layers include one `application/vnd.docker.distribution.manifest.v2+json` manifest per runner, and the served digest can name a runner manifest rather than the wrapper. When the expected digest is not the wrapper's own, the attestor attests the referenced runner manifest blob whose digest equals it: the blob's hash is verified and only its config and layers are attested. A digest the wrapper does not reference is a mismatch. |

### Evidence persistence and verification (`INV-PROV-PERSIST`)

| ID | Rule |
| --- | --- |
| INV-PROV-PERSIST-01 | Gateway-local windows are canonical protojson files owned by the Gateway's runtime file service under `.ztd/data/inference/model-provenance/windows/`, keyed by `provider_attempt_id` (1–128 ASCII letters, digits, `-`, `_`, `.`). They hold identifiers, digests, tags, and attestation metadata only, with no prompt or model output, and are written with private file permissions. |
| INV-PROV-PERSIST-02 | mTLS-authenticated clients read windows via `GET /api/v1/inference/model-provenance/attestations/{provider_attempt_id}`, which returns the window wrapped in a `window` field. Other methods return 405. |
| INV-PROV-PERSIST-03 | Campaign verification validates the window's `attestation_digest`, requires a recognized `verification_mode` and a nonzero `full_hash_completed_at_unix_ms` on every weight attestation, and checks the expected campaign digest binding. `ztd eval runs verify --require-provenance` enforces strict policy, requiring a valid window and `digest_match=true` for every scored inference. A run whose frozen model provenance policy is strict is verified under strict policy without the flag. |
| INV-PROV-PERSIST-04 | Under interim policy, `ztd eval runs verify` does not fail an assignment whose scored inferences have no window. Strict policy requires a valid window for every scored inference and fails the run when one is missing. Report generation is governed by `INV-PROV-PERSIST-06`. |
| INV-PROV-PERSIST-05 | After storing a window, the Gateway publishes the ephemeral `ztd.v1.inference.model.provenance.attestation.recorded` event, carrying only `provider_attempt_id`, to the owner's connected CLI sessions. A strict run waits on that event rather than polling: on every SSE (re)connect the eval CLI fetches each still-pending window once and seals it into the run's evidence, and on each event it fetches that attempt's window. Verification reads only the sealed copies. A window not captured within `ModelProvenanceCompletionTimeout` (five minutes) fails closed as unavailable evidence. |
| INV-PROV-PERSIST-06 | Compliance report generation (`buildCampaignReportSource` in `internal/cli/cmd/compliance/compliance_report.go`) fails closed with `ErrEvalRunVerificationFailed` when any scored attempt's sealed provider attempt record, sealed provider observation window, or sealed model provenance window cannot be loaded. The loads do not consult the interim or strict policy, so no policy relaxes this rule. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Provenance Operator capability | `internal/services/operatorcapability/provenance_operator.go` | `./ztd test unit --pkg ./internal/services/operatorcapability` |
| Ollama manifest attestation | `internal/services/inference/model_provenance/attestor.go` | `./ztd test unit --pkg ./internal/services/inference/model_provenance` |
| llama.cpp GGUF attestation | `internal/services/inference/model_provenance/llamacpp_attestor.go` | `./ztd test integration --pkg ./internal/services/inference/model_provenance --run TestLlamaCppStorageAttestor` |
| Protocol surface | `protocol/proto/ztd/eval/v1/eval.proto` | `ModelProvenanceAttestationWindow` schema |
| Gateway attestation window persistence | `internal/services/inference/model_provenance/window_store.go` | `WindowStore.Load` and `WindowStore.Save` implementations |
| Sealed campaign copy | `internal/services/evaluation/campaign_model_provenance.go` | `Store.SaveModelProvenanceWindow` and `Store.LoadModelProvenanceWindow`, run with `./ztd test unit --pkg ./internal/services/evaluation --run ModelProvenance` |
| Gateway witness capture | `internal/services/evaluation/campaign_witness_capture.go` | `CampaignWitnessCapture`, the owner that fetches attestation windows from the Gateway, run with `./ztd test unit --pkg ./internal/services/evaluation` |
| Campaign verification | `internal/services/evaluation/campaign_model_provenance.go` | `CampaignModelProvenanceReader.VerifyAssignmentModelProvenance`, a store-only reader with no Gateway remote |
| Gateway API controller | `internal/services/gateway/model_provenance_controller.go` | Route classification in `gateway_auth.go` |

## Operator Topology and Roles

```text
Campaign Gateway
           │
           ├──────────────────────────────┬──────────────────────────────┐
           ▼                              ▼                              ▼
   Inference Operator             Provenance Operator            Observer Operator
   (inference capability)         (provenance capability)        (observer capability)
   • Dispatches to Ollama API     • Manifest/GGUF reader         • Observer capability
                                  • Local weight blob hashing
                                  • AttestationWindow output
```

- **Inference Operator**: The execution path that invokes Ollama inference. Capabilities are listed in [Operator Architecture](operator.md).
- **Provenance Operator**: An independent witness that reads the model storage directory configured with `--model-storage-root`. Under the default `--model-storage-format ollama` that is an Ollama model directory (for example `~/.ollama/models`). Under `--model-storage-format llamacpp` it is a llama-server `--models-dir`. It attests that the served model tag maps to the weights named by the expected digest.
- **Observer Operator**: Runs with the `observer` capability. Provenance Operator selection does not consider it.

## Attestation Flow and Storage Verification

Each inference attempt that carries a served model tag and model digest triggers governed BEGIN and FINALIZE commands over the Provenance Operator's outbound connection. The Gateway returns the BEGIN and FINALIZE observation receipts with the inference response. Campaign execution starts without a separate storage-attestation preflight or synchronous HTTP attestation request. The first FINALIZE hashes uncached blobs; later attempts reuse verified file identities. HTTP evidence reads retrieve windows already persisted by the Gateway and never trigger an attestation.

```text
1. Gateway sends BEGIN (provider_attempt_id, served_tag, expected_digest)
                          │
                          ▼
2. Inference Operator executes scored model call against the configured provider (Ollama or llama.cpp)
                          │
                          ▼
3. Gateway sends FINALIZE to Provenance Operator
                          │
                          ▼
4. Storage attestor. Ollama format (`OllamaStorageAttestor`) reads the manifest via ollama.ParseName. llama.cpp format (`LlamaCppStorageAttestor`) resolves the tag via `llamacpp.ResolveModel` and hashes GGUF files, with no manifest step.
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
                          │
                          ▼
7. Gateway emits ztd.v1.inference.model.provenance.attestation.recorded (SSE, ephemeral) -> CLI reads window
```

1. **Tag Resolution**: Under the Ollama format, resolves the served tag through `ollama.ParseName` to a manifest path under the storage root's `manifests/` directory. Under the llamacpp format, `llamacpp.ResolveModel` resolves it to the GGUF files the router loads from `--models-dir`.
2. **Blob Hashing**: Streams each blob under `blobs/sha256-<digest>` (Ollama format) or each GGUF file (llamacpp format) through SHA-256 the first time it is seen and after any change to its file identity. Ollama windows also verify byte counts against manifest declarations. Every other window reuses that hash after confirming the identity is unchanged. Concurrent windows for one blob share its in-flight hash, and at most `ModelProvenanceHashConcurrency` full hashes run at once.
3. **Digest Validation**: If any blob hash mismatches or is missing, attestation fails and no completion window is published.

## Evidence Persistence and Campaign Verification

The Gateway persists attestation windows as canonical protojson files under `.ztd/data/inference/model-provenance/windows/<provider_attempt_id>.json` (`INV-PROV-PERSIST-01`). The eval CLI fetches windows through the Gateway API and never reads that directory. Run execution seals each scored attempt's window from the Gateway into the run's evidence under `.ztd/data/eval/runs/<run-id>/evidence/`, keyed by `model-provenance/<provider_attempt_id>/window-ref.json`. That capture belongs to `CampaignWitnessCapture` (`internal/services/evaluation/campaign_witness_capture.go`), which seals each assignment as its result is recorded and again at run completion. `ztd eval runs verify` reads only those sealed copies and never falls back to the Gateway on a local miss. While the Gateway is unreachable nothing is sealed, and the policies below decide from the windows the run already holds:

- **Interim Policy (verification and scoring)**: `ztd eval runs verify` does not fail an assignment whose scored inferences have no window. Report generation does not apply interim policy (`INV-PROV-PERSIST-06`).
- **Strict Policy (`ztd eval runs verify --require-provenance`; `ztd eval runs start --require-witness`, which implies `--verify`)**: Strict verification requires a valid, verified window where `digest_match = true` for 100% of scored inferences. A missing window fails the run. At run completion, before verification, run execution waits once for the whole run (`WaitForRunModelProvenanceEvidence`), on each scored attempt's attestation-recorded event, reconciling once per SSE (re)connect because ephemeral events are not replayed, for up to five minutes (`INV-PROV-PERSIST-05`).
- **Compliance Reports**: Generation is not governed by interim or strict policy. It requires a sealed provider attempt record, provider observation window, and model provenance window for every scored attempt. A missing copy of any fails generation with `ErrEvalRunVerificationFailed` (`INV-PROV-PERSIST-06`).

## Procedures

### Starting a Provenance Operator

Run on the storage host holding the model directory. The default format is an Ollama model directory:

```bash
./ztd operator start \
  --capabilities provenance \
  --provenance-operator-id ztd-model-provenance-1 \
  --model-storage-root ~/.ollama/models
```

For a llama-server `--models-dir`, set the format explicitly:

```bash
./ztd operator start \
  --capabilities provenance \
  --provenance-operator-id ztd-model-provenance-1 \
  --model-storage-root /srv/llamacpp/models \
  --model-storage-format llamacpp
```

### Verifying a Campaign Run with Provenance

Set `RUN_ID` to the ID of the campaign run to verify, which is the directory name under `.ztd/data/eval/runs/`.

```bash
./ztd eval runs verify "$RUN_ID" --require-provenance
```

## Anti-patterns

- **Attesting storage from a remote container**: Running the Provenance Operator on a machine other than the host holding the model weight files. The attestor reads only its local storage root (`INV-PROV-TOPO-04`).
- **Claiming cryptographic supply-chain signing**: Expecting Sigstore/Cosign verification; manifests are recorded as `MODEL_MANIFEST_VERIFICATION_STATUS_UNSIGNED` (`INV-PROV-ATTEST-04`).
- **Proceeding on empty model tags or digests**: Sending attestation commands when tags or digests are blank (`INV-PROV-ATTEST-01`).

## Links out

- [Evaluation Architecture](evals.md): Campaign topology, witness capabilities, and verification rules.
- [Operator Architecture](operator.md): Outbound Operator transport and capability boundaries.
- [Protocol Architecture](protocol.md): Canonical wire types and serialization.
- [SSE Streaming](sse.md): Event delivery, ephemeral event semantics, and reconnection.

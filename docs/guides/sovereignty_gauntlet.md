---
doc_id: sovereignty_gauntlet
title: Sovereignty and Compliance Verification Guide
audience: platform operators, compliance auditors, evaluators
status: current
version: v1.3.0
last_updated: 2026-10-22
owners:
  - docs/guides/sovereignty_gauntlet.md
  - internal/cli/cmd/compliance/
  - internal/cli/cmd/eval/
related:
  - docs/reference/compliance-evidence.md
  - docs/reference/compliance-alignment.md
  - docs/architecture/evals.md
  - docs/devs/release_process.md
when_to_read: Running sovereignty, compliance, and evaluation evidence verification across the ztd compliance and ztd eval pipelines.
do_not_use_for:
  - Core platform deployment (docs/guides/getting_started.md)
  - Operator connection workflow (docs/guides/connect_operator_to_gateway.md)
  - Formal third-party accreditation submission procedures
---

# Sovereignty and Compliance Verification Guide

## Purpose

This guide provides operational routing for executing sovereignty, compliance, and evaluation evidence verification across the maintained `ztd compliance` and `ztd eval` pipelines in ztd v1.3.0.

In ztd, sovereignty is established through cryptographic verification at the local host execution boundary:
- A model can request an action, but cannot directly execute mutations.
- Every governed mutation requires a canonical `GovernanceEnvelope` verified through all five governance layers (L1–L5).
- Every execution produces an immutable, signed `ActionReceipt` and a commitment attestation in the local ledger.
- System integrity is proven through Key Security Indicators (KSIs), cross-source evidence graphs, and reproducible model evaluation suites.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Maintained Verification Pipelines](#maintained-verification-pipelines)
  - [1. Compliance Pipeline (`ztd compliance`)](#1-compliance-pipeline-ztd-compliance)
  - [2. Evaluation Pipeline (`ztd eval`)](#2-evaluation-pipeline-ztd-eval)
- [Verification Procedures](#verification-procedures)
  - [Step 1: Verify Governance Conformance](#step-1-verify-governance-conformance)
  - [Step 2: Evaluate Key Security Indicators (KSIs)](#step-2-evaluate-key-security-indicators-ksis)
  - [Step 3: Validate Cross-Source Evidence Graph](#step-3-validate-cross-source-evidence-graph)
  - [Step 4: Generate and Verify Compliance Reports](#step-4-generate-and-verify-compliance-reports)
  - [Step 5: Run a Strict-Witness Model Campaign and Verification](#step-5-run-a-strict-witness-model-campaign-and-verification)
- [Acceptance Criteria](#acceptance-criteria)
- [Links out](#links-out)

---

## Maintained Verification Pipelines

### 1. Compliance Pipeline (`ztd compliance`)

The `ztd compliance` command suite audits the live platform state against FedRAMP Key Security Indicators (KSIs) and COSAiS control overlays, builds cross-source evidence graphs, and validates signed compliance bundles.

| Subcommand | Function |
| :--- | :--- |
| `ztd compliance ksi` | Evaluates live system state against CR26/FedRAMP KSIs and outputs structured JSON results |
| `ztd compliance ksi-history` | Inspects historical snapshots of KSI evaluations |
| `ztd compliance overlay` | Loads and validates COSAiS control overlay catalogs against the KSI catalog |
| `ztd compliance evidence` | Exports raw source evidence from selected Gateway or Operator runtimes |
| `ztd compliance evidence-graph verify` | Builds and validates the evidence graph over persisted eval runs |
| `ztd compliance report` | Generates sealed signed evidence bundles, exports them for disclosure, and verifies exports offline |
| `ztd compliance release-prepare` | Prepares, generates, verifies, and projects release compliance evidence |
| `ztd compliance release-evidence` | Verifies a signed compliance bundle and projects markdown/CSV summaries |

### 2. Evaluation Pipeline (`ztd eval`)

The `ztd eval` command suite executes governance conformance verification, manages model registries, qualifies models, and enforces witness attestation during evaluation campaigns.

| Subcommand | Function |
| :--- | :--- |
| `ztd eval conformance` | Runs the governance conformance suite against a live native stack, and verifies and reports its persisted runs (no external models) |
| `ztd eval models` | Manages model catalogs and the frozen model registry, pulls models onto the inference provider, and freezes provider model digests into the registry |
| `ztd eval formations` | Manages the checked-in formation catalog (Primary, Assistant, and Lite model slots); system-lane campaigns freeze formations from it |
| `ztd eval campaigns` | Defines, inspects, and archives evaluation campaigns, including system-lane formation campaigns created with `--formations` |
| `ztd eval suites` | Lists, shows, and manages the scenario suites that campaigns freeze |
| `ztd eval runs` | Schedules, runs, verifies, and publishes scored campaign runs |
| `ztd eval rollout` | Automates sequential model qualification across the rollout queue |
| `ztd eval backup` / `restore` | Backs up and restores evaluation evidence snapshots |

---

## Verification Procedures

### Step 1: Verify Governance Conformance

Execute the model-independent governance conformance suite against a live native stack to confirm that L1–L5 policy enforcement, replay protection, nonce reservation, and signed receipt generation hold under test conditions. `./ztd eval conformance run --help` lists the Operator discovery rules, the setup each ingress requires, and the flags:

```bash
./ztd eval conformance run --help
```

`./ztd eval conformance verify --run <run-id>` recomputes a persisted run's verdicts offline from its stored record.

Run the suite once per posture, each on a fresh native stack started in that posture (`./ztd gw start --posture <posture>`), then aggregate the runs with `./ztd eval conformance report`, repeating `--run <run-id>` once per posture. The suite verdict must be `PASS`, or `INCOMPLETE` for an unattended run (its attended cases not run). A `FAIL` or `INVALID_EVIDENCE` verdict fails this step. The coverage cells, outcomes, and catalogue are defined in [Governance Conformance Suite](../architecture/evals.md#governance-conformance-suite).

### Step 2: Evaluate Key Security Indicators (KSIs)

Evaluate the running platform against FedRAMP Key Security Indicators. Set these shell variables first: `SCOPE_ID` to the declared assessment scope, `RUN_ID` to the persisted eval run, `ASSESSMENT_ID` to the assertion assessment, and `WINDOW_START_MS` and `WINDOW_END_MS` to the evidence window bounds in Unix milliseconds:

```bash
./ztd compliance ksi --scope-id "$SCOPE_ID" --run-id "$RUN_ID" \
  --assertion-assessment-id "$ASSESSMENT_ID" \
  --evidence-window-start-unix-ms "$WINDOW_START_MS" --evidence-window-end-unix-ms "$WINDOW_END_MS"
```

The command prints the KSI result set as JSON, bound to the declared scope, run, and evidence window. The command exits zero when a KSI is not satisfied, so inspect the `status` of each entry in `results` to verify that every KSI in the selected class reports `satisfied`.

### Step 3: Validate Cross-Source Evidence Graph

Import the evidence of one or more persisted eval runs into a single content-addressed graph and validate it. Set the shell variable `RUN_ID` to a persisted eval run ID:

```bash
./ztd compliance evidence-graph verify --eval-run "$RUN_ID"
```

The command prints an `EvidenceGraphReport` as JSON and exits nonzero when the graph is invalid. Validation covers duplicate ID and content conflicts, reference resolution, prohibited cycles, scope, run, attempt, and scenario binding, the freshness window, encryption metadata, canonical digests, and assessed trust.

### Step 4: Generate and Verify Compliance Reports

Generate a signed compliance report bundle for an assessment scope. The bundle is sealed at rest under `.ztd/reports/<report-id>/`. Set these shell variables first: `SCOPE_FILE` to the assessment scope JSON file, `RUN_ID` to the persisted eval run, `REPORT_ID` to a new report identifier, `SIGNING_METADATA_FILE` to the signing metadata JSON file, and `SIGNING_KEY_FILE` to the hex-encoded signing private key file:

```bash
./ztd compliance report generate --scope "$SCOPE_FILE" --eval-run "$RUN_ID" --report-id "$REPORT_ID" \
  --signing-metadata "$SIGNING_METADATA_FILE" --signing-private-key "$SIGNING_KEY_FILE"
```

Export the plaintext bundle outside the runtime root, then verify it offline to prove tamper resistance. Set `EXPORT_DIR` to a directory outside the runtime root that does not exist yet, and `REPORT_TRUST_POLICY_FILE` to the report trust policy JSON file:

```bash
./ztd compliance report export --report-id "$REPORT_ID" --out "$EXPORT_DIR"
./ztd compliance report verify "$EXPORT_DIR/manifest.json" --trust-policy "$REPORT_TRUST_POLICY_FILE"
```

For release preparation, execute the release-prepare workflow:

```bash
./ztd compliance release-prepare
```

### Step 5: Run a Strict-Witness Model Campaign and Verification

When validating sovereignty across model inference, run a model campaign with witness Operators enrolled. A model-role campaign scores one registry model as a governed tool-calling agent in its Lite, Assistant, or Primary role. A system-lane campaign (`--formations`) scores a frozen formation's Primary and Assistant slots through the investigation loop; its Lite slot stays frozen and unexecuted, and it runs one repetition. The lane definitions are in [Model Campaign Architecture](../architecture/evals.md#model-campaign-architecture-system-lane-and-model-role-lane).

The commands below run a model-role campaign. Set the shell variable `MODEL_TAG` to the registry tag of the model to score before running the `campaigns create` command:

```bash
./ztd eval models freeze
./ztd eval campaigns create smoke-run "$MODEL_TAG" --suite smoke-suite
./ztd eval runs start smoke-run --require-witness
```

Under the `consensus` and `notary` postures, which enforce L2, `eval runs start` refuses to start unless L2 consensus is bound and ready. Check the binding with `./ztd consensus status`; `doctrine` and `ratify` do not gate on L2.

Run a strict-witness verification on the resulting campaign run. Set the shell variable `RUN_ID` to the ID of that campaign run:

```bash
./ztd eval runs verify "$RUN_ID" --coverage --require-observation --require-provenance
```

Verification requires:
- `--coverage`: every expected cell has a terminal assignment, with no missing, duplicate, or extra assignments and no population accounting failures.
- `--require-observation`: valid provider-boundary observation windows (GPU telemetry from Observer Operator).
- `--require-provenance`: content-addressed model storage hash matching the frozen campaign registry (`digest_match`, Provenance Operator).
- Zero verification failures: the report's `failure_count` is `0`, and the command exits non-zero otherwise.

---

## Acceptance Criteria

A platform deployment achieves sovereignty and compliance acceptance only when:

1. **Governance Conformance**: each posture's `./ztd eval conformance run` exits zero with suite verdict `PASS`, or `INCOMPLETE` for an unattended run.
2. **Indicator Compliance**: the `./ztd compliance ksi` JSON reports no `not_satisfied` entry in `results`.
3. **Graph Consistency**: `./ztd compliance evidence-graph verify --eval-run <run-id>` exits zero and its `EvidenceGraphReport` has `valid` set to `true`.
4. **Report Verification**: `./ztd compliance report verify` validates the exported bundle's signatures against trusted signer keys.
5. **Execution Witnessing**: Model evaluation runs verify with `--require-observation` and `--require-provenance`.

---

## Links out

- [Compliance Evidence Reference](../reference/compliance-evidence.md) — Schemas, evidence collection requirements, and audit verification.
- [Compliance Alignment Reference](../reference/compliance-alignment.md) — Mapping of ztd assertions to FedRAMP 20x KSIs, NIST SP 800-53 controls, and COSAiS overlays.
- [Evaluation Architecture](../architecture/evals.md) — Architecture of evaluation campaigns, scenario matrices, and scoring.
- [Model Provenance](../architecture/model-provenance.md) — Zero-trust weight attestation and chain-of-custody verification.
- [Release Process](../devs/release_process.md) — Release compliance bundle generation and publication gates.
- [Unified Stack Guide](unified_stack.md) — Operating Gateway, Operators, and witness nodes together.

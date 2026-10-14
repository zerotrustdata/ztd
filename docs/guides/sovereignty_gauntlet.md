---
doc_id: sovereignty_gauntlet
title: Sovereignty and Compliance Verification Guide
audience: platform operators, compliance auditors, evaluators
status: current
version: v1.0.0
last_updated: 2026-10-12
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

This guide provides operational routing for executing sovereignty, compliance, and evaluation evidence verification across the maintained `ztd compliance` and `ztd eval` pipelines in ztd v1.1.0.

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
  - [Step 5: Run Evaluation Formation Smoke and Verification](#step-5-run-evaluation-formation-smoke-and-verification)
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

The `ztd eval` command suite executes governance conformance verification, manages model registries, qualifies model formations, and enforces witness attestation during evaluation campaigns.

| Subcommand | Function |
| :--- | :--- |
| `ztd eval conformance` | Runs the governance conformance suite against a live native stack, and verifies and reports its persisted runs (no external models) |
| `ztd eval models` | Manages model catalogs, pulls model weights, and generates cryptographic registry digests |
| `ztd eval formations` | Inspects, registers, and smokes heterogeneous model execution sets (Lite, Assistant, Primary) |
| `ztd eval campaigns` | Defines, inspects, and freezes evaluation campaigns and test suites |
| `ztd eval runs` | Schedules, runs, verifies, and publishes scored campaign runs |
| `ztd eval rollout` | Automates sequential model qualification across the rollout queue |
| `ztd eval backup` / `restore` | Backs up and restores evaluation evidence snapshots |

---

## Verification Procedures

### Step 1: Verify Governance Conformance

Execute the model-independent governance conformance suite against a live native stack to confirm that L1–L5 policy enforcement, replay protection, nonce reservation, and signed receipt generation hold under test conditions. `./ztd eval conformance run --help` lists the start-up order and flags:

```bash
./ztd eval conformance run --help
```

`./ztd eval conformance verify --run <run-id>` recomputes a persisted run's verdicts offline from its stored record.

Run the suite once per posture, each on a fresh native stack started in that posture (`./ztd gw start --posture <posture>`, with the start-up order in `./ztd eval conformance run --help`), then aggregate the runs with `./ztd eval conformance report --run <run-id>`. The suite verdict must be `PASS`, or `INCOMPLETE` for an unattended run (its attended cases not run). A `FAIL` or `INVALID_EVIDENCE` verdict fails this step. The coverage cells, outcomes, and catalogue are defined in [Governance Conformance Suite](../architecture/evals.md#governance-conformance-suite).

### Step 2: Evaluate Key Security Indicators (KSIs)

Evaluate the running platform against FedRAMP Key Security Indicators:

```bash
./ztd compliance ksi --scope-id <scope-id> --run-id <run-id> \
  --assertion-assessment-id <assessment-id> \
  --evidence-window-start-unix-ms <start-ms> --evidence-window-end-unix-ms <end-ms>
```

The command prints the KSI result set as JSON, bound to the declared scope, run, and evidence window. Verify that every KSI in the selected class reports a satisfied outcome.

### Step 3: Validate Cross-Source Evidence Graph

Import the evidence of one or more persisted eval runs into a single content-addressed graph and validate it:

```bash
./ztd compliance evidence-graph verify --eval-run <run-id>
```

The command prints an `EvidenceGraphReport` as JSON and exits nonzero when the graph is invalid. Validation covers duplicate ID and content conflicts, reference resolution, prohibited cycles, scope, run, attempt, and scenario binding, the freshness window, encryption metadata, canonical digests, and assessed trust.

### Step 4: Generate and Verify Compliance Reports

Generate a signed compliance report bundle for an assessment scope. The bundle is sealed at rest under `.ztd/reports/<report-id>/`:

```bash
./ztd compliance report generate --scope <scope.json> --eval-run <run-id> --report-id <report-id> \
  --signing-metadata <signing-metadata.json> --signing-private-key <signing-key.hex>
```

Export the plaintext bundle outside the runtime root, then verify it offline to prove tamper resistance:

```bash
./ztd compliance report export --report-id <report-id> --out <new-dir>
./ztd compliance report verify <new-dir>/manifest.json --trust-policy <report-trust-policy.json>
```

For release preparation, execute the release-prepare workflow:

```bash
./ztd compliance release-prepare
```

### Step 5: Run Evaluation Formation Smoke and Verification

When validating sovereignty across model inference, verify formation execution across Lite, Assistant, and Primary roles with witness Operators enrolled:

```bash
./ztd eval models freeze
./ztd eval formations smoke ultra-efficient-speedster
```

Run a strict-witness verification on the resulting campaign run:

```bash
./ztd eval runs verify <run-id> --coverage --require-observation --require-provenance
```

Verification requires:
- 100% scenario coverage across assigned cells.
- Valid provider-boundary observation windows (GPU telemetry from Observer Operator).
- Content-addressed model storage hash matching the frozen campaign registry (Provenance Operator).
- Zero unverified or skipped verification rows.

---

## Acceptance Criteria

A platform deployment achieves sovereignty and compliance acceptance only when:

1. **Governance Conformance**: each posture's `./ztd eval conformance run` exits zero with suite verdict `PASS`, or `INCOMPLETE` for an unattended run.
2. **Indicator Compliance**: `./ztd compliance ksi` returns zero non-compliant indicators.
3. **Graph Consistency**: `./ztd compliance evidence-graph` reports valid signature chains and state linkages across all participating nodes.
4. **Report Verification**: `./ztd compliance report verify` validates the exported bundle's signatures against trusted signer keys.
5. **Execution Witnessing**: Model evaluation runs verify with `--require-observation` and `--require-provenance`.

---

## Links out

- [Compliance Evidence Reference](../reference/compliance-evidence.md) — Schemas, evidence collection requirements, and audit verification.
- [Compliance Alignment Reference](../reference/compliance-alignment.md) — Mapping of ztd controls to NIST, SOC 2, and COSAiS frameworks.
- [Evaluation Architecture](../architecture/evals.md) — Architecture of evaluation campaigns, scenario matrices, and scoring.
- [Model Provenance](../architecture/model-provenance.md) — Zero-trust weight attestation and chain-of-custody verification.
- [Release Process](../devs/release_process.md) — Release compliance bundle generation and publication gates.
- [Unified Stack Guide](unified_stack.md) — Operating Gateway, Operators, and witness nodes together.

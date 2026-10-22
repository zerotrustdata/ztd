---
doc_id: ux_smoke_test
title: Host-Native Headless UX Smoke Test
audience: developers, evaluators, compliance auditors
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/ux_smoke_test.md
related:
  - docs/devs/release_process.md
  - docs/devs/tests.md
  - docs/architecture/evals.md
when_to_read: Running the maintained isolated release smoke and reviewing its runtime evidence.
do_not_use_for:
  - Assessment authorization or certification
---

# Host-Native Headless UX Smoke Test

## Purpose

Documents how to execute the release qualification smoke gate `./ztd test release-smoke` on a built host. The smoke runner creates an isolated Gateway and dedicated Operator runtime, exercises typed file operations and document merging through the governed dispatch boundary, verifies exact content read-back, and exports signed evidence reports.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Run the smoke](#run-the-smoke)
- [Acceptance and retained evidence](#acceptance-and-retained-evidence)
- [Links out](#links-out)

## Run the smoke

Build the binary, then run the smoke:

```bash
make build
./ztd test release-smoke
```

Without `--root`, the run directory is `.ztd/operators/release-smoke-<UTC timestamp>`. Pass `--root <path>` to name the run directory explicitly; the path MUST NOT exist. Pass `--binary <path>` to run a different built `ztd` for the Gateway and Operator; the default is the running binary.

The runner does not contact external LLM providers and requires zero model credentials. It seals each runtime's master key under a private ephemeral key agent, so it never reads or writes host key stores. It:
1. Starts an isolated Gateway on dynamically allocated loopback ports.
2. Enrolls a CLI owner with `login cli`, deploys a dedicated data Operator via `operator deploy`, and pins the Gateway and Operator receipt trust anchors before any mutation.
3. Dispatches a typed governed `FILE_EDIT` through `POST /api/v1/operators/commands` and verifies exact content via governed read-back.
4. Creates and merges an embedded governed document, confirming untouched fields are preserved.
5. Performs a second embedded `FILE_EDIT` to generate real Gateway-local file-mutation and ledger evidence.
6. Exports and verifies reports in the run root, then stops its owned processes. Process cleanup uses Unix process groups (`syscall.Setpgid`, `syscall.Kill`) and Windows Job Objects (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) to ensure no orphan Gateway or Operator processes persist.

Every run MUST use a new, non-existent root; an existing path is rejected. The runner rejects a root whose `gateway/smoke-target` path falls inside a protected `.ztd` runtime path, because governed file edits would fail there. A root under `.ztd/operators/` is accepted.

## Acceptance and retained evidence

Accept the run only when the command exits with code 0 and `evidence/smoke-summary.json` records `"result": "PASS"`. A failed phase, cleanup, or evidence check records `"result": "FAIL"` with each cause in `errors`.

The runner verifies each receipt's actuator signature and persistence attestation against keys anchored to the Gateway's local export and the enrolled Operator's bound public key. The summary records:
- Exact binary SHA-256 and platform provenance stamps.
- Operating posture (`doctrine`) and ingress paths.
- Signed receipt and persistence-attestation verification across both Gateway and Operator audit stores.

The verdict also requires these report and scenario checks:
- Report evidence: `reports/gateway/` and `reports/worker/` each contain nonempty `event_receipts.csv`, `commitments.csv`, and `file_mutations.csv`. Every row of `verification_summary.csv` MUST be `PASS`; any `FAIL`, `SKIPPED`, or unrecognized result rejects the run. The checks `commitment_chain`, `git_merkle_root`, `file_mutation_linkage`, and `receipt_commitment_crosslink` MUST each appear with `PASS`.
- Scenario receipts: the Operator export (`reports/worker/`) contains a completed `FILE_EDIT` receipt for the governed file create, and the Gateway export contains a completed `DOCUMENT_UPDATE` receipt for each of the document create and the document merge and a completed `FILE_EDIT` receipt for the embedded file create.

Retained artifacts under `--root`:
- `logs/`: stdout and stderr for all spawned commands.
- `gateway/` and `worker/`: isolated Gateway and Operator working roots.
- `reports/gateway/`: Gateway runtime export (`report all`).
- `reports/worker/`: Operator runtime export (`report all`).
- `evidence/smoke-summary.json`: Consolidated run verdict and cryptographic receipts.

This test provides engineering acceptance evidence for the exercised `doctrine` governance pipeline and command dispatch boundary.

## Links out

- [Release Process](../devs/release_process.md) — Release gates, evidence collection, and signing.
- [Testing Guide](../devs/tests.md) — Platform test tiers and test execution rules.
- [Evaluation Architecture](../architecture/evals.md) — Model evaluation framework and witness verification.

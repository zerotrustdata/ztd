---
doc_id: ux_smoke_test
title: Host-Native Headless UX Smoke Test
audience: developers, evaluators, compliance auditors
status: current
version: v1.0.6
last_updated: 2026-10-13
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

Build the binary, then specify an isolated directory for the retained run:

```bash
make build
./ztd test release-smoke --root .ztd/tmp/release-smoke-<unique-run-name>
```

The runner does not contact external LLM providers and requires zero model credentials. It:
1. Starts an isolated Gateway on dynamically allocated loopback ports.
2. Enrolls a dedicated data Operator via `operator deploy`.
3. Dispatches a typed governed `FILE_EDIT` through `POST /api/v1/operators/commands` and verifies exact content via governed read-back.
4. Creates and merges an embedded governed document, confirming untouched fields are preserved.
5. Performs a second embedded `FILE_EDIT` to generate real Gateway-local file-mutation and ledger evidence.
6. Stops its owned processes and exports reports to the run root. Process cleanup uses Unix process groups (`syscall.Setpgid`, `syscall.Kill`) and Windows Job Objects (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) to ensure no orphan Gateway or Operator processes persist.

Use a new, non-existent directory for every run; an existing path is rejected. When `--root` is omitted, the default is `.ztd/tmp/release-smoke-<UTC timestamp>`.

## Acceptance and retained evidence

Accept the run only when the command exits with code 0 and `evidence/smoke-summary.json` records `PASS`.

The summary records:
- Exact binary SHA-256 and platform provenance stamps.
- Operating posture (`doctrine`) and ingress paths.
- Signed receipt verification across both Gateway and Operator audit stores.
- Integrity verification rows: `commitment_chain`, `git_merkle_root`, `file_mutation_linkage`, and `receipt_commitment_crosslink` must all be `PASS` with zero `FAIL` and zero `SKIPPED` rows.

Retained artifacts under `--root`:
- `logs/`: stdout and stderr for all spawned commands.
- `reports/gateway/`: Gateway runtime export (`report all`).
- `reports/worker/`: Operator runtime export (`report all`).
- `evidence/smoke-summary.json`: Consolidated run verdict and cryptographic receipts.

This test provides engineering acceptance evidence for the exercised `doctrine` governance pipeline and command dispatch boundary.

## Links out

- [Release Process](../devs/release_process.md) — Release gates, evidence collection, and signing.
- [Testing Guide](../devs/tests.md) — Platform test tiers and test execution rules.
- [Evaluation Architecture](../architecture/evals.md) — Model evaluation framework and witness verification.

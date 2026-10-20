# Contributing to ztd

This guide describes how to find work, set up a development environment, select checks, and submit changes to ztd, a zero-trust execution platform for agentic infrastructure.

## Architectural Foundation

Before contributing, review the core platform architecture:

- **ztd Gateway (ztdg)**: The central Policy Decision Point (PDP) for identity, policy admission, and posture-aware governance.
- **ztd Operator (ztdo)**: The host-side Policy Execution Point (PEP), connected outbound to the Gateway over mTLS. MCP ingress belongs to the Gateway; outbound Operators expose no inbound listeners.
- **ztd Protocol**: The canonical protocol definitions (protobuf schemas and constant registries).
- **Five-Layer Governance Pipeline**:
    - **L1 Doctrine (L1Doctrine)**: Technical hard gates (forbidden patterns, threat detection).
    - **L2 Consensus (L2Consensus)**: Multi-agent consensus via Ed25519 signatures.
    - **L3 Notary (L3Notary)**: Human-in-the-loop authorization through WebAuthn or signed CLI proofs.
    - **L4 Warden (L4Warden)**: Pre-dispatch verification gate (transaction hash, expiry, nonce, state root).
    - **L5 Actuator (L5Actuator)**: Execution boundary issuing signed `ActionReceipts`.

## Finding Work

Feedback and bug reports are described in [Feedback and bug reports](../EARLY_TESTERS.md). The following contributions do not require familiarity with the full platform:

**For anyone (no build required)**
1. **First Run Report on your OS.** Try any door listed in the First Run Report issue form (explorer, run locally, full stack, protocol, release binary) and file the form with where you got stuck. Reports from Windows, macOS arm64, and Linux arm64 are requested.
2. **Explorer feedback.** Browse [OpenDevOps.ai](https://opendevops.ai) for 10 minutes and file what was clear, what was unclear, and what you wanted to see but could not find.
3. **README cold read.** Read the README as a first-time reader and report the first sentence that made you want to stop. One sentence is a complete contribution.

**For protocol contributors**
4. **Run the protocol examples.** Run `go run ./examples/governance-envelope/` and `go run ./examples/workload-identity/` (or the `protocol/node` tests with `npm ci --prefix protocol/node && npm test --prefix protocol/node`) and report OS, toolchain versions, and any unexpected results.
5. **Conformance test report.** After `make build`, run `./ztd test unit --pkg ./internal/governance --run TestHashParity` against the `protocol/conformance/` vectors and file the result. Passing results from additional environments are useful for verification.

**For careful readers**
6. **Guide versus binary audit.** Select one file in `docs/guides/`, follow it literally, and file each place where the text and the command output differ.
7. **Threat-model question.** Read `docs/architecture/overview.md` and `governance.md`, then file one question about a trust-boundary behavior the documents do not address. An unanswered question identifies a documentation gap.

Look for issues labeled `first-run`, `good-first-task`, and `docs`. Experienced contributors can also search the Go source for architectural work. Contributors new to the codebase should start with the tasks above.

## Local Development Setup

Choose the smallest environment that fits your work:

- **Gateway only:** clone the repository, then run `make up`. A native build needs Go 1.27.2 and Make. Fresh clones use the committed frontend embeds; Node.js 22.22.2+, 24.15+, or 26+ and npm are needed only to rebuild the Console or Evaluation Explorer. Ollama and model SDKs are not needed for this configuration.
- **Gateway with every Operator capability:** run `make gw` to start the Gateway with its embedded Operator carrying the capabilities in `GW_CAPABILITIES` (default `data,inference,observer`). Set `MODEL_STORAGE_ROOT` to the local model store to add Provenance, set `ZTD_OLLAMA_ENDPOINT` in `.env` when the Ollama provider is not on this host, and pass extra `./ztd gw start` flags through `GW_ARGS`. Run a separate outbound Operator in the foreground with `make operator OPERATOR_ARGS='...'`, including its own `--working-dir` (`./ztd operator start` refuses a directory that holds a Gateway runtime). Use `make status` and `make down` to inspect and stop the host stack.

Linux and macOS contributors can install the complete development toolchain with their platform's setup script; use `--build-only` to skip the contributor toolchain while still building the binary. A direct `make build` on a fresh clone uses committed frontend embeds and does not require Node.js. See the [setup instructions](../docs/guides/getting_started.md#build-locally-native-host).

For browser sign-in, run `./ztd login web`; for a headless environment, run `./ztd login`. The [Getting Started guide](../docs/guides/getting_started.md) covers setup, enrollment, and runtime configuration.

## Selecting Checks

Run the narrowest check that covers your changes, then the broader owning suite when practical:

| Change | Command |
|---|---|
| Go unit tests | `./ztd test unit` |
| Cross-process evaluation qualification | `./ztd test eval-loop` |
| Isolated governed release smoke | `./ztd test release-smoke` |
| Operator fleet scale qualification | `./ztd test scale` |
| Repository tools (`internal/tools/*`) | `./ztd test unit --pkg ./internal/tools/<tool>` |
| Setup and CI helper scripts | `bash -n` and `shellcheck` on the changed script |

Platform test suites run through `./ztd test ...` or their owning Make target, as required by [INV-TEST-01](../docs/devs/devs.md#testing-inv-test).

## Pull Requests

- Read the [Developer Guidelines](../docs/devs/devs.md) and follow the relevant invariants for the code and tests you change.
- Keep each pull request focused. Update current-state documentation with behavior changes, and regenerate generated artifacts through their owning generators.
- Describe the user-visible behavior, implementation, and checks run. Include exact commands and note any checks that could not run.
- Confirm that the CI workflow passes. Respond to review feedback with follow-up commits on the same pull request.

## Filing Issues

To report a problem installing or enrolling, use the **First Run Report** form in the [issue chooser](https://github.com/zerotrustdata/ztd/issues/new/choose). The form asks for the door you tried, your OS, `./ztd version`, and the exact step.

For a bug in a running deployment, [file a bug report](https://github.com/zerotrustdata/ztd/issues/new) and include:

1. **Version**: Output of `./ztd version`.
2. **Environment**: OS and processor architecture.
3. **Traceability**: If applicable, include the `transaction_hash` or relevant entries from the `SQLAuditStore`.
4. **Reproduction**: A minimal set of steps to reproduce the behavior.
5. **Expected vs Actual**: Description of the expected result and the observed result.

Security vulnerabilities must be reported through the [Security Policy](SECURITY.md), not through a public issue.

## Documentation Contributions

Documentation is treated as code. Follow the [Documentation Guide](../docs/devs/docs.md), which defines the complete audit workflow, source-of-truth matrix, generated-document ownership, first-party documentation catalog, cross-linking rules, version policy, and validation matrix.

1. **Audit end to end**: Read the complete affected document and verify every behavioral claim, example, link, and limitation.
2. **Trace ownership**: Locate the owning implementation, schema, registry, configuration, test, generator, or scope-bound evidence rather than relying on existing prose.
3. **Update related documents**: Keep one canonical explanation and cross-link every affected current-state summary or component index.
4. **Generate and validate**: Update templates, annotations, schemas, or reviewed evidence inputs before generated outputs, then run the owning checks.
5. **Version last**: Update document date and version metadata only after the complete audit and related-document reconciliation.

## Coding Standards

Read the [Developer Guidelines](../docs/devs/devs.md) before submitting patches. Key directives include:

- **Replace broken paths**: Broken paths are deleted or replaced. Backward compatibility is not maintained for technical debt.
- **Fail-Closed**: If a validation or security check fails, the system halts.
- **Explicit over Implicit**: Behavior has no hidden side effects, and no behavior depends on inference or guessing.
- **No Net Technical Debt**: Pull requests leave the codebase cleaner than they found it.

## Licensing

Unless otherwise noted, the ztd source files are distributed under the Business Source License 1.1 (BSL 1.1) found in the LICENSE file. The license converts to Apache 2.0 on the Change Date (2030-08-18). By contributing, you grant Lateralus Labs, LLC a license to use your work under these terms.

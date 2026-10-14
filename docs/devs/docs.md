---
doc_id: docs
title: Documentation Guide
audience: maintainers and coding agents
status: current
version: v1.1.0
last_updated: 2026-10-14
owners:
  - docs/
  - protocol/docs/
  - README.md
  - THIRD_PARTY_NOTICES.md
  - buf.gen.yaml
  - Makefile
  - internal/services/gateway/docs/
  - internal/tools/constgen/
  - internal/tools/explorercatalog/
  - claude-plugin/
  - evaluation-explorer/src/contract/
  - ztd-adapter/generator/
  - website/
related:
  - docs/devs/devs.md
  - docs/devs/codemap.md
  - docs/devs/tests.md
  - docs/devs/release_process.md
  - docs/devs/troubleshooting.md
  - docs/reference/glossary.md
when_to_read: Creating, editing, auditing, generating, or reviewing ztd documentation across handwritten, generated, and machine-readable surfaces; structuring documentation for AI agent ingestion and prompt/context cache optimization.
do_not_use_for:
  - Platform coding invariants and error contracts (docs/devs/devs.md)
  - Runtime and package ownership maps (docs/devs/codemap.md)
  - Test execution tiers, fixtures, and CI scope (docs/devs/tests.md)
  - Release execution, native evaluation acceptance, and signed evidence (docs/devs/release_process.md)
  - Diagnostics, failure modes, and recovery procedures (docs/devs/troubleshooting.md)
---

# Documentation Guide

## Purpose

Defines how maintainers and coding agents audit, write, generate, review, cross-link, and maintain ztd documentation. Establishes the authoritative architectural standard for AI agent digestibility and context cache optimization (prefix invariance, token density, vocabulary stability, structural homogeneity, and negative/positive routing).

The current working tree is the sole source of truth for platform behavior: executable code, Protobuf schemas, and typed constant registries override stale prose. Historical release notes and evidence artifacts describe strictly their stated release, run, or assessment scope.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Format and metadata (`INV-DOC-FMT`)](#format-and-metadata-inv-doc-fmt)
  - [Factual authority (`INV-DOC-AUTH`)](#factual-authority-inv-doc-auth)
  - [Generated outputs (`INV-DOC-GEN`)](#generated-outputs-inv-doc-gen)
  - [Style and structure (`INV-DOC-STYLE`)](#style-and-structure-inv-doc-style)
  - [AI agent digestibility and context caching (`INV-DOC-CACHE`)](#ai-agent-digestibility-and-context-caching-inv-doc-cache)
- [Owned surfaces](#owned-surfaces)
  - [Documentation surfaces](#documentation-surfaces)
  - [Documentation catalog](#documentation-catalog)
- [Procedures](#procedures)
  - [End-to-End Audit Workflow](#end-to-end-audit-workflow)
  - [Source-of-Truth Traceability Matrix](#source-of-truth-traceability-matrix)
  - [Pre-Commit Documentation Validation](#pre-commit-documentation-validation)
  - [AI Agent Context Caching and Ingestion Workflow](#ai-agent-context-caching-and-ingestion-workflow)
  - [Refresh Generated Documentation](#refresh-generated-documentation)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Format and metadata (`INV-DOC-FMT`)

| ID | Rule |
| :--- | :--- |
| INV-DOC-FMT-01 | Maintained developer files in `docs/devs/` MUST use the dev docs format with YAML front matter (`doc_id`, `title`, `audience`, `status`, `version`, `last_updated`, `owners`, `related`, `when_to_read`, `do_not_use_for`) and standard H2 sections: Purpose, Quick index, Invariants, Owned surfaces, Procedures, Anti-patterns, Links out. |
| INV-DOC-FMT-02 | `last_updated` and `version` metadata MUST be updated only after completing the full [End-to-End Audit Workflow](#end-to-end-audit-workflow). `version` MUST match the exact string in `VERSION`. Documents carrying a `Last Updated` and `Version` body header instead of front matter follow the same rule. |
| INV-DOC-FMT-03 | Documents evaluated for impact during a change or release that require no edits MUST retain their existing metadata. Blanket-bumping metadata without an audit is prohibited. |
| INV-DOC-FMT-04 | Release notes in `docs/release_notes/` and evidence artifacts are immutable once released and MUST retain the version, run ID, and timestamps of their original scope. |
| INV-DOC-FMT-05 | Maintained protocol specifications under `protocol/docs/` (`a2a.md`, `constants.md`, `mcp.md`, `spec.md`) MUST include a `Version: vX.Y.Z` header matching the exact string in `VERSION`. |
| INV-DOC-FMT-06 | Front matter keys MUST follow the exact canonical sequence: `doc_id`, `title`, `audience`, `status`, `version`, `last_updated`, `owners`, `related`, `when_to_read`, `do_not_use_for`. Stable structural fields MUST precede volatile fields (`last_updated`) to maximize context cache hit rates across repository revisions. |
| INV-DOC-FMT-07 | Invariant rules and procedural constraints MUST use uppercase RFC 2119 keywords (`MUST`, `MUST NOT`, `REQUIRED`, `SHALL`, `SHALL NOT`, `SHOULD`, `SHOULD NOT`, `RECOMMENDED`, `MAY`, `OPTIONAL`). Ambiguous advisory language in normative rules is strictly prohibited. |
| INV-DOC-FMT-08 | Markdown table cells MUST NOT contain unescaped raw markdown link syntax or unescaped brackets that trigger parser ambiguities. Code symbols and file references within table cells MUST be enclosed in backticks. |

### Factual authority (`INV-DOC-AUTH`)

| ID | Rule |
| :--- | :--- |
| INV-DOC-AUTH-01 | Every behavioral claim MUST resolve to current code, configuration, schemas, tests, or scope-bound evidence. Existing prose is never proof of current behavior. |
| INV-DOC-AUTH-02 | CLI command names, flags, defaults, and usage MUST match `./ztd <command> --help`. Docs MUST NOT maintain duplicated flag dumps that drift from the code. |
| INV-DOC-AUTH-03 | Each concept MUST have one canonical explanation. Related documents route readers to the canonical owner via cross-links rather than duplicating content. |
| INV-DOC-AUTH-04 | Security and governance claims MUST state posture, ingress path, trust boundary, identity requirements, and explicit limitations. Broad claims of absolute security or external certification are prohibited. |
| INV-DOC-AUTH-05 | When documentation conflicts with code or schema, AI agents and maintainers MUST apply this deterministic resolution hierarchy: (1) executable production code and Protobuf schemas (`cmd/`, `internal/`, `protocol/proto/ztd/`), (2) constant registries (`protocol/constants/`), (3) automated test assertions and golden vectors, (4) developer and architecture documentation (`docs/devs/`, `docs/architecture/`), (5) guides and website copy (`docs/guides/`, `website/`). Documentation MUST be updated to reflect verified code behavior; agents MUST NOT introduce code regressions to conform to stale documentation. |

### Generated outputs (`INV-DOC-GEN`)

| ID | Rule |
| :--- | :--- |
| INV-DOC-GEN-01 | Generated protobuf references in `protocol/docs/reference/api/` MUST change only through edits to `.proto` files in `protocol/proto/ztd/` followed by `make proto-generate` (or `make proto-go`). MUST NOT hand-edit generated protobuf output. |
| INV-DOC-GEN-02 | Gateway OpenAPI specifications (`internal/services/gateway/docs/swagger.json` and `swagger.yaml`) MUST change only through Go Swagger annotations followed by `make swagger-generate`. `swagger.json` is embedded into the Gateway binary via `internal/services/gateway/docs/docs.go`. |
| INV-DOC-GEN-03 | Machine-readable protocol constants and schemas under `protocol/constants/`, `protocol/models/`, and `protocol/schemas/` MUST remain synchronized with their Go and TypeScript mirrors via owning generation and validation commands. `make constants-check` verifies generated constants, `make doctrines-validate` checks doctrine JSON syntax, and `make explorer-catalog-check` verifies the generated Explorer catalog. |
| INV-DOC-GEN-04 | The static website is built by `make website-build` from the hand-authored homepage `website/src/index.html`, journal metadata and Markdown in `website/content/`, and root `README.md`, published verbatim as `llms-full.txt`. Journal pages, RSS, and sitemap are generated by `website/src/journal.js`; release-note fragments for `/releases/` are rendered from `docs/release_notes/` by `website/src/release-notes.js`. Commands on the homepage MUST match the README quick start, including the `start.sh` and `start.ps1` one-command installs. MUST NOT hand-edit rendered output in `website/dist/`. |
| INV-DOC-GEN-05 | Release compliance evidence projections (`docs/release_notes/vX.Y.x/*-compliance-evidence.md` and `.csv`) MUST be generated through `./ztd compliance release-prepare` or `./ztd compliance release-evidence`. MUST NOT hand-edit projected compliance evidence. |
| INV-DOC-GEN-06 | The observe frontend contract pack under `ztd-adapter/contract-pack/`, including its `README.md`, `builder-prompt.md`, `public/README.md`, and `public/offline-verifier.md`, MUST change only through `ztd-adapter/generator/gen-contract-pack.mjs` (`npm run gen:contract-pack` from `ztd-adapter/`) and MUST pass `npm run gen:contract-pack:check`. MUST NOT hand-edit contract pack output. |
| INV-DOC-GEN-07 | `THIRD_PARTY_NOTICES.md` MUST be generated exclusively through `make third-party-notices` from `vendor/modules.txt` and production `npm` dependencies, and validated via `make third-party-notices-check`. MUST NOT hand-edit third-party license text. |
| INV-DOC-GEN-08 | Retired. The Python protocol package and its enum stubs were removed from ztd; there is no generated Python artifact to keep in sync. |

### Style and structure (`INV-DOC-STYLE`)

| ID | Rule |
| :--- | :--- |
| INV-DOC-STYLE-01 | Prose MUST use present tense, active voice, and exact technical terminology. Source lines MUST NOT be hard-wrapped. |
| INV-DOC-STYLE-02 | Code references MUST use repository-relative paths and stable symbol names. Line-number references and machine-absolute paths are prohibited. |
| INV-DOC-STYLE-03 | Relative links MUST resolve from the directory containing the document. Link syntax MUST use markdown links (for example `[Developer Guidelines](devs.md)`) with verified local targets. |
| INV-DOC-STYLE-04 | Standalone component and guide documents MUST link to their canonical architecture or index document. Component index documents MUST list all maintained documents in their set. |
| INV-DOC-STYLE-05 | The [Documentation catalog](#documentation-catalog) MUST list every maintained first-party document exactly once, and each entry MUST resolve to an existing file. A change that adds, renames, retires, or removes a document MUST update the catalog in the same change. Vendored third-party documentation under `vendor/` is outside the catalog. |
| INV-DOC-STYLE-06 | Fenced code blocks MUST specify a syntax language tag (`bash`, `json`, `yaml`, `go`, `ts`, `python`, `proto`, `text`, `mermaid`). Copy-paste CLI blocks MUST contain complete executable commands without unescaped placeholders or comments that break copy-execution. |
| INV-DOC-STYLE-07 | Documents MUST NOT maintain comprehensive CLI flag tables. Command documentation MUST direct users to `./ztd <command> --help` for authoritative flag sets, reserving in-document flag coverage strictly for required conceptual setup. |

### AI agent digestibility and context caching (`INV-DOC-CACHE`)

| ID | Rule |
| :--- | :--- |
| INV-DOC-CACHE-01 | **Prefix Invariance and Volatility Isolation**: To maximize prompt and context cache hit rates across LLM inference engines (which cache token sequences strictly from byte 0), static token sequences MUST precede variable or volatile tokens. Invariant rules, architecture constraints, and contract definitions MUST appear before ephemeral run IDs, session hashes, timestamps, or transient checklists. Dynamic data MUST NOT be inserted into document headers or shared prompt prefixes. |
| INV-DOC-CACHE-02 | **Token Density and Semantic Economy**: Documentation MUST maximize information density per token. Rhetorical questions, conversational filler, speculative musings, decorative fluff, and verbose transitions ("Note that...", "It should be pointed out that...") are prohibited. Every token MUST convey architectural, behavioral, operational, or governance facts. |
| INV-DOC-CACHE-03 | **Canonical Vocabulary and Terminology Stability**: Platform components MUST be referenced using exact canonical terms defined in [Glossary](../reference/glossary.md). Alternating synonyms for system entities (e.g. using "worker", "agent node", "runner", and "host daemon" interchangeably for an "Operator") is prohibited. Inconsistent terminology causes subword token fragmentation and degrades context retrieval. |
| INV-DOC-CACHE-04 | **Modular Chunk Boundaries and Slicing Independence**: Documents within `docs/devs/` and `docs/architecture/` MUST use standardized, predictable section hierarchies (H1 -> H2 -> H3) with self-contained semantic blocks. Each section MUST be fully intelligible when read in isolation (e.g., via slice-viewing tools or retrieval chunks) without requiring unstated preceding conversational context. |
| INV-DOC-CACHE-05 | **Negative and Positive Routing Pruning**: Every developer and architecture document MUST define explicit `when_to_read` (positive routing) and `do_not_use_for` (negative routing) metadata in its front matter. Agents MUST use these boundaries to aggressively prune irrelevant documents from their working context window, preventing context saturation and cache thrashing. |
| INV-DOC-CACHE-06 | **Cross-Document Deduplication**: Identical multi-paragraph technical descriptions MUST NOT be duplicated across multiple documents. Repetition busts prompt caches when one instance is edited and wastes context window capacity. A concept MUST be defined once in its canonical owner document; all other documents MUST route readers to that anchor via relative markdown links. |

## Owned surfaces

### Documentation surfaces

| Surface | Path | Verify |
| :--- | :--- | :--- |
| Dev docs format | `docs/devs/` | YAML front matter and required H2 section order |
| Documentation catalog | `docs/devs/docs.md` | Authoritative inventory of all first-party docs |
| Protobuf API references | `protocol/proto/ztd/`, `protocol/docs/reference/api/` | `make proto-generate` (or `make proto-go`) |
| Gateway OpenAPI | `internal/services/gateway/docs/`, Go Swagger annotations | `make swagger-generate` |
| Protocol events and constants | `protocol/constants/events.json`, `internal/tools/constgen/`; generated `internal/constants/events_gen.go`, `internal/constants/action_types_gen.go`, `console/src/generated/events.ts` | `make constants-generate`, `make constants-check` |
| Protocol doctrine JSON | `protocol/constants/doctrine/*.json` | `make doctrines-validate` (checks JSON syntax for all doctrine files) |
| Explorer scenario catalog | `evaluation-explorer/src/content/scenario-catalog.generated.ts`, `internal/tools/explorercatalog/` | `make explorer-catalog-generate`, `make explorer-catalog-check` |
| Observe frontend contract pack | `ztd-adapter/contract-pack/`, `ztd-adapter/generator/gen-contract-pack.mjs` | `npm run gen:contract-pack` and `npm run gen:contract-pack:check` from `ztd-adapter/` |
| Third-party license notices | `THIRD_PARTY_NOTICES.md` | `make third-party-notices-check` |
| Claude Code plugin | `claude-plugin/` | `make claude-plugin-test` |
| FedRAMP KSI catalog and COSAiS overlays | `docs/reference/ksi-catalog.json`, `docs/reference/cosais-overlays.json` | `./ztd compliance overlay` loads overlays and verifies KSI reference resolution |
| Static website | `README.md`, `website/` | `make website-build`, `make website-test` |
| Compliance evidence projections | `docs/release_notes/vX.Y.x/` | `./ztd compliance release-prepare`, `./ztd compliance report verify` |

### Documentation catalog

This catalog provides the authoritative inventory of all first-party documentation across the repository. `INV-DOC-STYLE-05` governs its completeness. Descriptions summarize each document's purpose; the document itself and its source owners remain authoritative.

#### Repository entry points and governance

- [Root README](../../README.md): Product overview, local quick-start paths, repository map, project status, contribution routes, and documentation entry links. Published as website's `llms-full.txt`; homepage quick start must match it (`INV-DOC-GEN-04`).
- [Changelog](../../CHANGELOG.md): Release index documenting changes across versions with links to per-release notes.
- [Early Testers Guide](../../EARLY_TESTERS.md): Feedback and bug reports: how to report defects, what to include, response expectations, and licensing summary.
- [Third-Party Notices](../../THIRD_PARTY_NOTICES.md): Distributed third-party software licenses and notices generated from committed vendor and npm metadata (`INV-DOC-GEN-07`).
- [Contributing](../../.github/CONTRIBUTING.md): Contribution workflow, local development setup, check selection, pull request and issue expectations, documentation contributions, and licensing.
- [Code of Conduct](../../.github/CODE_OF_CONDUCT.md): Contributor code of conduct.
- [Security Policy](../../.github/SECURITY.md): Supported versions, private vulnerability reporting, disclosure policy, and scope.
- [Pull Request Template](../../.github/pull_request_template.md): Summary, details, and test results sections for pull requests.
- [Agent Instructions](../../AGENTS.md): Operational constraints, build and test entry points, architecture summary, and critical invariants for AI coding agents. Summarizes `docs/devs/` and defers to it.
- [Claude Code Instructions](../../CLAUDE.md): Repository guidance for Claude Code with build and test commands, the governance pipeline summary, and invariants most likely to matter to an agent. Summarizes `docs/devs/` and defers to it.

#### Core platform concepts and position papers

- [About ztd](../core/about.md): Why ztd exists, what it is, the verification boundary, sovereignty and accountability, and the reference implementation.
- [Position Paper](../core/position_paper.md): Position on governing agentic execution without surrendering data custody, including design requirements, the system model, evidence discipline, and limitations.

#### Developer documentation (`docs/devs/`)

- [Developer Guidelines](devs.md): Go platform coding invariants, CLI layout, error contracts, and runtime guidelines.
- [Code Map](codemap.md): Package and runtime ownership maps, directory structure, and substrate separation.
- [Documentation Guide](docs.md): Documentation invariants, audit workflows, generated outputs, AI agent digestibility, context caching optimizations, and documentation catalog.
- [Testing Guide](tests.md): Four-tier test model, execution commands, fixture lifecycles, and testing invariants.
- [Release Process](release_process.md): Version synchronization, release compliance evidence, smoke gates, and release publication workflow.
- [Troubleshooting Guide](troubleshooting.md): Diagnostics, failure modes, and recovery procedures.

#### Platform architecture (`docs/architecture/`)

- [System Architecture Overview](../architecture/overview.md): System topology, component boundaries, and overall platform architecture.
- [Gateway Architecture](../architecture/gateway.md): Gateway service architecture, HTTP routing, PKI, and control-plane dispatch.
- [Operator Architecture](../architecture/operator.md): Outbound and embedded Operator architecture, task execution, and host lifecycle.
- [Governance Architecture](../architecture/governance.md): Five-layer governance framework (L1 doctrine to L5 audit), posture policies, and enforcement.
- [AI Agents and Governance Boundary](../architecture/agents.md): AI agent integration patterns, tool boundaries, and trust limits.
- [Consensus Architecture](../architecture/consensus.md): Quorum, multi-party consensus, and state agreement mechanisms.
- [Storage Architecture](../architecture/storage.md): SQLite persistence, schema migrations, and storage service layout.
- [Events Architecture](../architecture/events.md): Event taxonomy, delivery guarantees, and event bus lifecycle.
- [Evaluation Architecture](../architecture/evals.md): Native evaluation framework, campaign specifications, scoring, and evidence capture.
- [Encryption Architecture](../architecture/encryption.md): Cryptographic primitives, key derivation, envelope encryption, and key management.
- [Authentication Architecture](../architecture/auth.md): Authentication, passkeys/WebAuthn, session management, and workload identity.
- [Network Architecture](../architecture/network.md): Network topologies, port management, listener isolation, and mTLS.
- [Protocol Architecture](../architecture/protocol.md): Protocol wire formats, serialization, envelope hashing, and schema compatibility.
- [Public Spectator Architecture](../architecture/public_spectator.md): Read-only public feed spectator service and event broadcasting.
- [Server-Sent Events Architecture](../architecture/sse.md): Server-Sent Events architecture for streaming LLM tokens and live platform updates.
- [Console Architecture & Development](../architecture/console.md): Embedded browser console architecture, state management, and API integration.
- [Model Provenance](../architecture/model-provenance.md): Model attribution, weights tracking, and supply-chain provenance.
- [Automation Scripts](../architecture/scripts.md): Administrative and automation scripts architecture and execution environment.

#### Operator and deployment guides (`docs/guides/`)

- [Getting Started](../guides/getting_started.md): End-to-end local platform installation, onboarding, and first execution.
- [Air-Gapped Deployment](../guides/air_gap.md): Air-gapped and disconnected deployment guide and local caching procedures.
- [Unified Stack Guide](../guides/unified_stack.md): Host-native single-host stack run as `ztd` processes (Gateway, Operators, witness Operators), evaluation campaign topology, and lifecycle management.
- [Build Gateway](../guides/build_gateway.md): Compiling and packaging the Gateway binary from source.
- [Build Operator](../guides/build_operator.md): Building the unified `ztd` binary and running its outbound Operator mode.
- [Connect Operator to Gateway](../guides/connect_operator_to_gateway.md): Operator enrollment and mTLS connection to the Gateway.
- [Connect Applications to Gateway](../guides/connect_apps_to_gateway.md): Connecting client applications and SDKs to the Gateway.
- [Connect Frontend to Gateway](../guides/connect_frontend_to_gateway.md): Connecting custom frontend applications to Gateway APIs.
- [Build Frontend](../guides/build_frontend.md): Building and deploying frontend user interfaces.
- [Build Applications](../guides/build_apps.md): Building custom applications atop the ztd platform.
- [Build Observe Frontend](../guides/build_observe_frontend.md): Building custom telemetry and observation frontends with ztd-adapter.
- [Cloudflare Tunnel](../guides/cloudflare_tunnel.md): Configuring Cloudflare Tunnels for secure ingress.
- [Public Spectator Guide](../guides/public_spectator.md): Setting up and operating public spectator feeds.
- [Reset Workload Identity](../guides/reset_workload_identity.md): Resetting and re-enrolling workload identity credentials.
- [Run an Autochthon](../guides/autochthon.md): Running Gateway-less `ztd autochthon`, reading status reports, anchoring and recovering tallies.
- [Sovereignty Gauntlet](../guides/sovereignty_gauntlet.md): Step-by-step verification procedures for sovereignty and governance controls.
- [Lovable Integration](../guides/lovable.md): Integrating with Lovable.dev low-code applications.
- [Host-Native Headless UX Smoke Test](../guides/ux_smoke_test.md): Release smoke runbook for `./ztd test release-smoke`: options, scenarios, evidence, and acceptance criteria.

#### Reference and compliance (`docs/reference/`)

- [Glossary](../reference/glossary.md): Canonical platform definitions, acronyms, and terminology.
- [Compliance Alignment](../reference/compliance-alignment.md): Mapping of ztd controls to NIST, SOC 2, ISO 27001, and COSAiS frameworks.
- [Compliance Evidence](../reference/compliance-evidence.md): Evidence collection requirements, schema definitions, and audit verification.
- [FIPS 140-3 Module Linkage and Scope](../reference/fips140-3.md): FIPS 140-3 cryptographic module boundary, build flags, and verification instructions.

#### Architecture diagrams (`docs/diagrams/`)

- [System Overview Flowchart](../diagrams/flowchart-system-overview-lr.md): Mermaid flowchart showing top-level system architecture.
- [Gateway Fleet Graph](../diagrams/graph-gateway-fleet-single-host-http-mtls.md): Fleet network topology, port allocations, and mTLS trust boundaries.
- [Gateway Services Graph](../diagrams/graph-gateway-services.md): Internal Gateway services, dependency graph, and middleware dispatch.
- [Operator Lifecycle Graph](../diagrams/graph-operator-lifecycle.md): Operator lifecycle state machine from bootstrap to shutdown.
- [Operator Pipeline L1-L5 Graph](../diagrams/graph-operator-pipeline-l1-l5.md): L1-L5 execution pipeline through the Operator engine.
- [50k System Graph](../diagrams/graph-system-50k.md): 50,000-foot component graph and protocol interaction boundaries.
- [ztd System Diagram](../diagrams/ztd-diagram.png): Rendered system overview image (immutable binary asset).

#### Protocol specifications and references (`protocol/`)

- [Protocol README](../../protocol/README.md): Protocol package overview, directory layout, code generation, and version synchronization.
- [Protocol Specification](../../protocol/docs/spec.md): Authoritative wire protocol specification.
- [MCP Specification](../../protocol/docs/mcp.md): Model Context Protocol integration and wire contract.
- [A2A Specification](../../protocol/docs/a2a.md): Agent-to-Agent communication protocol specification.
- [Constants Specification](../../protocol/docs/constants.md): Protocol constant registries and semantics.
- [Generated Common API](../../protocol/docs/reference/api/ztd/common/v1/index.md): Common protocol messages, error envelopes, and base types.
- [Generated Compliance API](../../protocol/docs/reference/api/ztd/compliance/v1/index.md): Compliance report, evidence, and audit messages.
- [Generated Eval API](../../protocol/docs/reference/api/ztd/eval/v1/index.md): Evaluation campaign, scenario, and metrics messages.
- [Generated Operator API](../../protocol/docs/reference/api/ztd/operator/v1/index.md): Operator task, execution, and receipt messages.
- [Generated PubSub API](../../protocol/docs/reference/api/ztd/pubsub/v1/index.md): Pub/sub message contracts and event envelopes.
- [Protocol Conformance](../../protocol/conformance/README.md): Conformance test harness and cross-language compatibility tests.

#### Claude Code plugin (`claude-plugin/`)

- [Claude Plugin README](../../claude-plugin/README.md): Claude Code plugin architecture, PreToolUse hook, SessionStart hook, and Gateway MCP server wiring.
- [Claude Plugin Setup Skill](../../claude-plugin/skills/setup/SKILL.md): Guided setup skill for onboarding Claude Code sessions into ztd governance.

#### Subsystem runbooks, contracts, and adapters

- [Website Runbook](../../website/README.md): Static website build, Build in Public authoring workflow, Releases pages and one-command onboarding scripts, and Cloudflare deployment.
- [From 936 to 2,000 Operators on one Gateway](../../website/content/posts/enrolling-two-thousand-operators.md): Build in Public field notes on enrollment scale, measurements, and limits.
- [Going public, and finding out onboarding only worked on Linux](../../website/content/posts/going-public-onboarding-beyond-linux.md): Build in Public field notes on the public repo, the unshipped 1.0.1–1.0.3 tags, and Windows and macOS onboarding.
- [Evaluation Explorer Runbook](../../evaluation-explorer/RUNBOOK.md): Operational runbook for developing and building the Evaluation Explorer SPA.
- [Evaluation Explorer Contract](../../evaluation-explorer/src/contract/CONTRACT.md): Browser view contract, canonical schema validators, and release provenance rules.
- [Evaluation Campaign README](../../eval/README.md): Evaluation campaign data layout, fixtures, and directory conventions.
- [Frontend Adapter Contract Pack](../../ztd-adapter/contract-pack/README.md): Audited frontend adapter contract pack reference and integration guide.
- [Frontend Contract Pack Builder Prompt](../../ztd-adapter/contract-pack/builder-prompt.md): AI prompt specification for generating conforming observe frontends.
- [Public Spectator Contract Pack](../../ztd-adapter/contract-pack/public/README.md): Public spectator boundary contract specification for mirror clients.
- [Public Proof Offline Verification](../../ztd-adapter/contract-pack/public/offline-verifier.md): Cryptographic and manifest offline verification runbook for public proofs and feed fixtures.
- [Examples README](../../examples/README.md): Platform example applications index and runnable samples.
- [External Console Example](../../examples/external-console/README.md): External standalone console sample application.

#### Release notes and evidence projections (`docs/release_notes/`)

- [Release Notes Directory](../release_notes/): Immutable per-release notes organized by minor version directory (`vX.Y.x/`), alongside scoped release compliance evidence projections (`*-compliance-evidence.md` and `.csv`).
- [v1.0.0 Release Notes](../release_notes/v1.0.x/v1.0.0.md): Canonical v1.0.0 release notes, scope, architectural invariants, and verified release evidence.
- [v1.0.1 Release Notes](../release_notes/v1.0.x/v1.0.1.md): v1.0.1 release scope and changes.
- [v1.0.2 Release Notes](../release_notes/v1.0.x/v1.0.2.md): v1.0.2 release scope and changes.
- [v1.0.3 Release Notes](../release_notes/v1.0.x/v1.0.3.md): v1.0.3 release scope and changes.
- [v1.0.4 Release Notes](../release_notes/v1.0.x/v1.0.4.md): v1.0.4 release scope and changes.
- [v1.0.5 Release Notes](../release_notes/v1.0.x/v1.0.5.md): v1.0.5 release scope and changes.
- [v1.0.6 Release Notes](../release_notes/v1.0.x/v1.0.6.md): v1.0.6 release scope and changes.
- [v1.0.6 Compliance Evidence](../release_notes/v1.0.x/v1.0.6-compliance-evidence.md): Generated compliance evidence projection for v1.0.6.
- [v1.1.0 Release Notes](../release_notes/v1.1.x/v1.1.0.md): v1.1.0 release scope and changes.

## Procedures

### End-to-End Audit Workflow

Execute these steps in order when updating maintained documentation:

1. Read the complete document from beginning to end without skimming.
2. Classify document purpose (`devs`, `architecture`, `guide`, `reference`, `protocol`, `release_notes`).
3. Inventory every factual claim (commands, flags, routes, defaults, ports, paths, contracts, environment variables).
4. Trace each claim to its primary owning source in the repository using the [Source-of-Truth Traceability Matrix](#source-of-truth-traceability-matrix).
5. If documentation diverges from code or schema, resolve using the [Conflict Resolution Hierarchy (`INV-DOC-AUTH-05`)](#factual-authority-inv-doc-auth): update documentation to reflect code, never regress code to match documentation.
6. Inspect related documentation to update cross-links and eliminate duplicated explanations (`INV-DOC-AUTH-03`, `INV-DOC-CACHE-06`).
7. Refresh all generated outputs affected by the change using [Refresh Generated Documentation](#refresh-generated-documentation).
8. Review the revised document end-to-end for coherence, verified relative links, exact vocabulary (`INV-DOC-CACHE-03`), and un-wrapped prose (`INV-DOC-STYLE-01`).
9. Update `last_updated` and `version` metadata only after steps 1–8 pass (`INV-DOC-FMT-02`).
10. Run pre-commit validation commands (`make doctrines-validate`, `make constants-check`).

### Source-of-Truth Traceability Matrix

| Claim | Primary owners | Required corroboration |
| :--- | :--- | :--- |
| CLI commands, arguments, flags, defaults, and usage | Cobra definitions in `internal/cli/cmd/` | Relevant `./ztd <command> --help`, command tests, and CLI wiring |
| HTTP routes, methods, and route authentication | Canonical paths in `protocol/constants/api_paths.json` and router/controller registration in `internal/services/gateway/` | Route authentication inventory, middleware, controller behavior, and `internal/services/gateway/docs/swagger.json` |
| Authentication and identity | Gateway auth middleware, `internal/services/auth/`, CLI enrollment code, PKI services, and protocol identity helpers | Route classification, certificate/session validation, enrollment tests, and deployed topology |
| Configuration and environment | Types, defaults, and validation in `internal/config/`, CLI flags, environment constants in `protocol/constants/env_vars.json`, and component settings | Startup construction, host lifecycle Make targets, and configuration tests |
| Governance and execution | `internal/services/governance/`, `internal/services/consensus/`, `internal/services/pubsub/`, `internal/services/mcp/`, and construction in Gateway and Operator startup | Active posture branches, complete ingress-to-dispatch path, persistence, rejection behavior, and integration tests |
| Persistence and runtime files | Owning store or service and `internal/services/fs/` for `.ztd/` state | Path constants, schema lifecycle, permissions, cleanup behavior, and tests using isolated runtime roots |
| Wire messages and gRPC services | Protobuf schemas in `protocol/proto/ztd/`, model schemas in `protocol/models/`, and canonicalization implementations | Generated bindings, vectors, contract tests, and cross-language conformance tests |
| Protocol identifiers and registries | Registry-specific source declaration in `protocol/constants/` or `internal/constants/` | Mirrors, consumers, package loaders, and contract tests; verify owning generator and check target |
| Agent tool registry | Checked-in source registry in `protocol/constants/agenttools/agent-tool-registry.json` and Go accessor in `registry.go` | Embedded JSON document tests |
| Gateway OpenAPI | Router/controller behavior and Go Swagger annotations in `cmd/ztd`, `internal/services/gateway/`, `internal/models/`, `internal/constants/` | Generated `swagger.json` and `swagger.yaml`, route registration, and authentication behavior |
| Third-party dependencies and notices | Committed dependencies in `go.mod`, `vendor/modules.txt`, and npm `package.json` | `THIRD_PARTY_NOTICES.md` generated via `make third-party-notices` |
| Deployment and ports | Host lifecycle targets in `Makefile` (`up`, `gw`, `operator`, `status`, `down`), `ztd gw start` and `ztd operator start` flags, and `internal/constants/ports.go` | Listener defaults, identity enrollment, network exposure, and documented commands |
| Tests and CI | `internal/cli/cmd/test/`, root and component Makefiles, package test configuration, and `.github/workflows/` | Actual target dependencies, build tags, exclusions, timeouts, race settings, and external-service gates |
| Compliance, cryptographic, and measured claims | Exact signed artifact, verifier, catalog, schema, or evidence graph named by the claim | Version, scope, environment, trust inputs, evidence cutoff, failure cases, and explicit limitations |

### Pre-Commit Documentation Validation

Maintainers and agents MUST execute these validation commands prior to committing documentation changes:

```bash
# 1. Validate doctrine JSON syntax
make doctrines-validate

# 2. Check protocol constant generation
make constants-check

# 3. Check third-party notices freshness
make third-party-notices-check

# 4. Check Swagger / OpenAPI sync
make swagger-generate
git diff --exit-code internal/services/gateway/docs/

# 5. Verify explorer scenario catalog freshness
make explorer-catalog-check

# 6. Check adapter contract pack freshness
npm run gen:contract-pack:check --prefix ztd-adapter

# 7. Verify static website build and tests
make website-test
```

### AI Agent Context Caching and Ingestion Workflow

To optimize LLM inference performance and maximize prompt/context cache utilization, AI agents interacting with this repository MUST follow this protocol:

1. **Semantic Routing via Front Matter**: Before reading an entire document into context, inspect only the YAML front matter. Check `when_to_read` and `do_not_use_for`. If the document's `do_not_use_for` matches the current objective, immediately abort reading that file to conserve context window tokens and prevent cache displacement.
2. **Deterministic File Order**: When loading multiple repository documents into prompt context, always load them in deterministic, canonical order:
   - Root instructions (`AGENTS.md` or `CLAUDE.md`)
   - Developer invariants (`docs/devs/devs.md`)
   - Subsystem guide or architecture document for the active task
   Loading files in a stable order guarantees byte-prefix invariance, enabling inference engines to hit cached token blocks.
3. **Sub-Section Slicing**: When answering narrow queries (e.g. verifying an error contract or testing tier), use slice viewing tools to extract only the relevant H2 or H3 section (e.g. `### Errors (INV-ERR)`). Because `INV-DOC-CACHE-04` enforces modular chunk independence, sliced sections are fully coherent without loading the entire document.
4. **Conflict Resolution Execution**: If an ingested document contains instructions or claims that contradict current production code or Protobuf contracts, do not halt or query the user. Apply `INV-DOC-AUTH-05`: follow the code and contract, and plan an atomic documentation update in the same change.
5. **Cache-Preserving Edits**: When editing documentation, preserve top-of-file prefix stability:
   - Do not re-order front matter keys (`INV-DOC-FMT-06`).
   - Do not insert dynamic timestamps, build numbers, or transient comments into document headers (`INV-DOC-CACHE-01`).
   - Edit only the specific H2 section requiring modification to minimize diff blast radius.

### Refresh Generated Documentation

Execute owning targets whenever underlying source code, schemas, or dependencies change:

```bash
# Protobuf bindings and reference documentation
make proto-generate

# Gateway OpenAPI JSON and YAML specifications
make swagger-generate

# Protocol events and Go/TypeScript constant registries
make constants-generate
make constants-check

# Protocol doctrine schema validation
make doctrines-validate

# Evaluation Explorer scenario catalog sync
make explorer-catalog-generate
make explorer-catalog-check

# Observe frontend contract pack regeneration
npm run gen:contract-pack --prefix ztd-adapter
npm run gen:contract-pack:check --prefix ztd-adapter

# Third-party notices regeneration
make third-party-notices
make third-party-notices-check

# Static website build and verification
make website-build
make website-test
```

## Anti-patterns

- Hand-editing generated artifacts (`INV-DOC-GEN-01` to `INV-DOC-GEN-08`): Editing protobuf references, Swagger JSON, third-party notices, or generated constants by hand instead of updating the source and generator.
- Premature metadata bumping (`INV-DOC-FMT-02`): Incrementing `version` or updating `last_updated` without executing the full 10-step End-to-End Audit Workflow.
- Cache prefix degradation (`INV-DOC-CACHE-01`, `INV-DOC-FMT-06`): Injecting dynamic timestamps, git hashes, or ephemeral notes into front matter or top headers, invalidating prompt caches for downstream content.
- Hard-wrapping source prose (`INV-DOC-STYLE-01`): Wrapping lines at 80 or 100 characters in Markdown source, causing diff churn and irregular token slicing.
- Machine-absolute paths and line numbers (`INV-DOC-STYLE-02`): Including `/home/...` or `:L123-L145` references that break across hosts and go stale on code edits.
- Duplicated flag and schema tables (`INV-DOC-AUTH-02`, `INV-DOC-AUTH-03`, `INV-DOC-STYLE-07`): Copying large CLI flag inventories or Protobuf field lists into guides instead of linking to `--help` or canonical references.
- Code regressions to match stale docs (`INV-DOC-AUTH-05`): Modifying working code or schemas to conform to an outdated document rather than correcting the document to reflect the code.
- Terminology drifting and synonym rotation (`INV-DOC-CACHE-03`): Alternating between "runner", "node", "daemon", and "Operator", degrading tokenizer subword embeddings and context retrieval.
- Missing semantic routing directives (`INV-DOC-CACHE-05`): Authoring developer or architecture docs without explicit `when_to_read` and `do_not_use_for` front matter directives.
- Cross-document duplication (`INV-DOC-CACHE-06`): Copying multi-paragraph architecture explanations into multiple guides instead of cross-linking to the canonical architecture document.
- Unescaped table syntax (`INV-DOC-FMT-08`): Leaving raw brackets or unescaped markdown links in table cells that break automated markdown parsers or linter suites.
- Unqualified security assertions (`INV-DOC-AUTH-04`): Making broad claims of absolute security or compliance without identifying posture, ingress, boundary, and explicit limitations.

## Links out

- [Developer Guidelines](devs.md): coding invariants and repository standards.
- [Code Map](codemap.md): package and runtime ownership maps.
- [Testing Guide](tests.md): test execution tiers and testing invariants.
- [Release Process](release_process.md): versioning, compliance bundles, and release workflow.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [System Architecture Overview](../architecture/overview.md): platform architecture and component boundaries.
- [Glossary](../reference/glossary.md): canonical platform definitions and vocabulary.

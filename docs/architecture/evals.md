---
doc_id: evals
title: Evaluation Programs
audience: maintainers and coding agents
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - internal/services/evaluation/
  - internal/cli/cmd/eval/
  - eval/
related:
  - gateway.md
  - operator.md
  - model-provenance.md
  - public_spectator.md
  - governance.md
  - ../guides/sovereignty_gauntlet.md
  - ../core/position_paper.md
when_to_read: Understanding ztd's native execution-boundary test suite, formation-based model campaigns through the governed Inference Operator, provider-boundary observation, model provenance attestation, evidence persistence and verification, and capability selection for embedded or outbound Operators.
do_not_use_for:
  - Governance five-layer interlock and policy enforcement (governance.md)
  - Gateway HTTP routes and session routing (gateway.md)
  - Operator execution boundary and native tool catalog (operator.md)
  - Model weight zero-trust attestation (model-provenance.md)
  - Public evaluation projections and explorer contract (public_spectator.md)
---

# Evaluation Programs

## Purpose

Defines ztd's platform evaluation programs: the native execution-boundary test suite and model campaign evaluations across single models (model-role lane) and heterogeneous formations (system lane). The native suite verifies zero-trust governance interlocks without external models or synthetic simulators. Model campaigns execute single models or three-role formations through the governed Inference Operator PEP, capture provider-boundary hardware telemetry and storage-side weight attestation, and persist canonical evidence under the Local-First Audit Architecture (LFAA) for deterministic, offline verification.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Program Definitions (`INV-EVAL-PROG`)](#program-definitions-inv-eval-prog)
  - [Campaign Structure (`INV-EVAL-CAMP`)](#campaign-structure-inv-eval-camp)
  - [Witness Separation (`INV-EVAL-WIT`)](#witness-separation-inv-eval-wit)
  - [Evidence Storage (`INV-EVAL-EVID`)](#evidence-storage-inv-eval-evid)
  - [Verification Posture (`INV-EVAL-VERIF`)](#verification-posture-inv-eval-verif)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Topology: Confidential Edge Execution with Governed State Mutation](#topology-confidential-edge-execution-with-governed-state-mutation)
  - [Evaluation Programs Overview](#evaluation-programs-overview)
  - [Evaluation CLI Architecture](#evaluation-cli-architecture)
  - [Executing a Formation Campaign](#executing-a-formation-campaign)
  - [Native Execution-Boundary Suite](#native-execution-boundary-suite)
  - [Scenario Suites and Catalog Management](#scenario-suites-and-catalog-management)
  - [Model Campaign Architecture: System Lane and Model-Role Lane](#model-campaign-architecture-system-lane-and-model-role-lane)
  - [Scenario Fixtures, Trajectories, and Grading Models](#scenario-fixtures-trajectories-and-grading-models)
  - [Witness Capabilities: Observer and Provenance Operators](#witness-capabilities-observer-and-provenance-operators)
  - [Evidence Persistence, Backups, and Offline Verification](#evidence-persistence-backups-and-offline-verification)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Program Definitions (`INV-EVAL-PROG`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-PROG-01 | The native execution-boundary suite (`core-execution-boundary@1.0.0`) MUST NOT use model providers, model judges, campaigns, scheduling, or synthetic simulators. |
| INV-EVAL-PROG-02 | Model campaigns MUST route scored model inference exclusively through governed dispatch to the selected Inference Operator session. The campaign CLI and the Gateway MUST NOT call model providers directly. |
| INV-EVAL-PROG-03 | All evaluation run evidence MUST persist under `.ztd/data/eval/runs/<run-id>/`, separate from campaign definitions and frozen scenario artifacts under `.ztd/data/eval/campaigns/<campaign-id>/`. |
| INV-EVAL-PROG-04 | Campaign verification (`ztd eval runs verify`) and native boundary verification (`ztd eval boundary verify`) MUST recompute bindings and signatures deterministically from persisted evidence without executing new scored actions. |

### Campaign Structure (`INV-EVAL-CAMP`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-CAMP-01 | Model campaigns bind scored inference to frozen `served_model_tag` and `model_digest` pairs registered in the campaign's frozen model inventory. Placeholder digests lacking provenance authority MUST be re-frozen from the provider prior to scored runs. |
| INV-EVAL-CAMP-02 | Campaign execution routes each assignment by target kind (`CampaignAssignmentRouter`): heterogeneous assignments to the formation executor, and homogeneous assignments to the agent executor (`CampaignAgentExecutor`). Every agent turn MUST be a governed Inference Operator dispatch, and every tool call a governed Data Operator action through the Gateway. |
| INV-EVAL-CAMP-03 | On `ztd eval rollout run`, strict witness verification (provider-boundary observation and model provenance) MUST be enabled unconditionally and MUST fail closed when coverage is missing. On `ztd eval runs start`, witness requirements are opt-in via `--require-observation`, `--require-provenance`, or `--require-witness`. When strict provenance verification is required, the verifier MUST wait up to five minutes for durable attestation completion windows before failing closed. |
| INV-EVAL-CAMP-05 | Each catalog scenario MUST declare `eligible_roles`. The model-role scheduler MUST assign a scenario only to its eligible roles and MUST fail closed (`ErrEvaluationScenarioRolesUnassigned`) on a scenario with none. |
| INV-EVAL-CAMP-06 | A `model_role` run MUST have exactly one subject model. `CampaignController.StartRun` MUST reject a `model_role` run whose campaign freezes any other number of models (`ErrEvaluationCampaignSubjectInvalid`). System-lane campaigns are exempt. |
| INV-EVAL-CAMP-08 | A campaign scores exactly one suite and freezes it at creation. Every subsequent reader of a campaign's scenarios MUST load the frozen catalog and artifacts (`Store.LoadScenarioCatalog`, `Store.LoadScenarioArtifacts`) and MUST NOT regenerate them from built-in or stored suites. |
| INV-EVAL-CAMP-11 | Catalog build fails closed unless `ExpectedTools` is a subset of `AllowedTools`, no tool is both allowed and forbidden, and every named tool exists in the agent tool registry snapshot. |
| INV-EVAL-CAMP-12 | Every scenario declares a trajectory policy (`ANSWER`, `FIRST_CHOICE`, `GUIDED`, or `GOVERNED`). Every scenario MUST carry a content constraint (`ScenarioContentCheck`) so that it can fail, and MUST NOT declare semantic-judge grading or `semantic_grade` evidence: no executor records judge evidence, so such a scenario could only terminate `GRADER_FAILED`. |
| INV-EVAL-CAMP-20 | Campaign evidence digests MUST use the Go canonical JSON encoder (`MarshalCanonicalJSONObject`) pinned by `protocol/vectors/eval/canonical_json_numbers.json`. |
| INV-EVAL-CAMP-24 | Every `DeterministicGrade` MUST state one `GradeBasis` (`OBSERVATION`, `DERIVED`, or `STRUCTURAL`). `tallyDeterministicGrades` counts only `OBSERVATION` grades; a failed `STRUCTURAL` grade marks the verdict `INVALID_EVIDENCE`. |
| INV-EVAL-CAMP-27 | A campaign states the platform release that froze it (`EvaluationCampaignSpec.platform_release` and `source_revision`). `StartRun` fails with `ErrEvaluationPlatformReleaseMismatch` when a recorded release differs from the executing binary build. |
| INV-EVAL-CAMP-28 | `ztd eval rollout run` MUST NOT mark a queue entry verified, or promote it from the `--gate-smoke` gate with `--promote-on-pass`, when the run stopped with assignments still queued (`ErrEvaluationRunStopped`) or when every assignment ended `PROVIDER_FAILED` (`ErrEvaluationModelUnserved`). Such a run's evidence still verifies, but it does not cover the model. A stopped run ends the batch and leaves its entry pending; an unserved model marks the entry failed with class `provider_failure`. |

### Witness Separation (`INV-EVAL-WIT`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-WIT-01 | Evaluation capability selection MUST use enrolled Operator capabilities. The Gateway embedded Operator MAY hold `data`, `inference`, `observer`, and `provenance` capabilities in a single session; separate outbound sessions are optional. Witness evidence remains separately typed and bound to each provider attempt, even when capabilities share a session. |
| INV-EVAL-WIT-02 | Observer samples MUST describe the host where the Observer capability executes; a local observer MUST NOT be described as observing a remote provider GPU. The Provenance capability MUST read the model storage named by its runtime configuration. Co-resident capabilities MUST NOT be described as independent remote witnesses. |
| INV-EVAL-WIT-03 | Inference dispatch in the Gateway MUST deliver BEGIN to Observer and Provenance notifiers before publishing the inference command, and FINALIZE after receiving the result. A BEGIN delivery error MUST fail the dispatch. |
| INV-EVAL-WIT-04 | The Observer Operator MUST report provider-boundary GPU VRAM, utilization, temperature, power, clocks, and system RAM samples bound to the same `provider_attempt_id` as inference. The Provenance Operator MUST attest model manifest and blob SHA-256 hashes. |

### Evidence Storage (`INV-EVAL-EVID`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-EVID-01 | Native run evidence (`report.json`, `verification.json`, digest-named artifacts) and campaign run evidence MUST persist under `.ztd/data/eval/runs/<run-id>/`. |
| INV-EVAL-EVID-02 | Provider-boundary observation windows MUST be stored under the Gateway volume at `data/inference/provider-observer/windows/`. Model provenance attestation windows MUST be stored under `data/inference/model-provenance/windows/`. |
| INV-EVAL-EVID-04 | Public-safe campaign projections MUST omit principal, Operator, session, credential, endpoint, path, raw target, envelope, receipt, audit, and evidence body fields. Private prompts, gold criteria, and execution identifiers MUST NOT cross the public boundary. |
| INV-EVAL-EVID-05 | `ztd eval backup` MUST write only to destinations outside the runtime tree, copying `.ztd/data/eval/` and `.ztd/eval/` with a SHA-256 manifest. `ztd eval restore` MUST verify every file against the manifest before writing. Evidence is backed up in its encrypted form (INV-EVAL-EVID-07), so a restored snapshot is readable only by a runtime that holds the same vault key. |
| INV-EVAL-EVID-06 | A run's provider environment MUST be derived only from provider-boundary observation windows and projected only once the run aggregate is complete. The field MUST be omitted if windows disagree on capacity. |
| INV-EVAL-EVID-07 | Every file under `.ztd/data/eval/` MUST be encrypted with the runtime vault (`evaluation.NewEvidenceFileService`, wired by `shared.NewEvidenceFileSvc` in every command that reads or writes evaluation evidence), bound to the `ztd-eval-evidence-v1` associated-data label so a ciphertext from another vault-sealed store does not authenticate. The label is not path-bound because archiving moves whole run and campaign trees. A missing or locked vault fails the read or write (`ErrEvaluationEvidenceVaultRequired`, `ErrVaultLocked`), and a file that does not authenticate fails with `ErrEvaluationEvidenceDecryptFailed`; there is no plaintext fallback. The only plaintext under the runtime root is the `runs export` bundle, which carries the disclosure-safe public projection only. Compliance report bundles under `.ztd/reports/`, which carry raw evaluation sources for replay, are sealed by the same service under their own `ztd-compliance-report-v1` label and leave the runtime in the clear only through `compliance report export`. |
| INV-EVAL-EVID-08 | Executor, native-run, and rollout failures MUST persist a fixed `ExecutionFailureClass`, never error text, and `ztd eval` MUST NOT tee command output into a file under the runtime root: that output quotes grade detail and model-derived text. |

### Verification Posture (`INV-EVAL-VERIF`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-VERIF-01 | Campaign assignment results MUST use projection envelope `1.2.0` (`campaignProjectionEnvelopeEnrichedVersion`) with named public extensions. Stored envelopes remain readable in their recorded form. |
| INV-EVAL-VERIF-02 | Model quality state `exploratory_verified` MUST be published only for exact run-derived datasets and eligible `(variant_id, role)` aggregates covered by an applicable passing verification report. |
| INV-EVAL-VERIF-03 | A passing campaign verification report is applicable only when its run, campaign, catalog, model registry, completed population, and verified population match persisted evidence. |
| INV-EVAL-VERIF-04 | Observation unavailability MUST use the closed vocabulary: `historical_not_captured`, `source_not_captured`, `source_unavailable`, `scenario_not_applicable`, `incomplete_contributor_evidence`, and `no_scored_calls`. Observed zero MUST be preserved separately from unavailable values. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Native execution-boundary suite | `internal/services/evaluation/` | `ztd eval boundary run`, `ztd eval boundary verify` |
| Campaign controller and scheduler | `internal/services/evaluation/campaign_controller.go` | `ztd eval runs start`, `resume`, `verify` |
| Model-role agent executor | `internal/services/evaluation/campaign_agent_executor.go` | `ztd eval runs start` on model-role campaigns |
| Formation executor (direct runner) | `internal/services/evaluation/campaign_formation_execution.go` | `ztd eval runs start` on formation campaigns |
| Formation catalog | `eval/formation-catalog.json`, `eval/formation-catalog-overlay.json` | `ztd eval formations list` |
| Provider-boundary observation | `internal/services/operatorcapability/provider_boundary_observer.go` | Observer Operator enrollment and window ingestion |
| Model provenance attestation | `internal/services/operatorcapability/provenance_operator.go` | Provenance Operator enrollment and digest hashing |
| Data Operator selection | `internal/services/operatorcapability/data_operator.go` | `SelectDataOperator` resolution tests |
| CLI command tree | `internal/cli/cmd/eval/` | `./ztd eval --help` |
| Protocol contracts | `protocol/proto/ztd/eval/v1/` | `make proto-generate` |

## Procedures

### Topology: Confidential Edge Execution with Governed State Mutation

Evaluation programs instantiate the **Confidential Edge Execution with Governed State Mutation** topology:
- **Governance Gateway (PDP)**: Enforces central policy, admits transactions, coordinates consensus and notary proofs, and manages campaign matrices.
- **Inference Operator (PEP)**: Serves as the sole scored execution path to approved model providers (e.g. Ollama). Excluded from general tool execution.
- **Data Operator (PEP)**: Executes governed workspace mutations and shell commands requested by tool-calling agents.
- **Observer Operator (Witness)**: Collects read-only hardware telemetry (GPU VRAM, temperature, power, system RAM) at the provider boundary.
- **Provenance Operator (Witness)**: Attests storage-side model weight files, manifests, and content-addressed blobs via SHA-256 digests.

### Evaluation Programs Overview

| Program | Suite / Catalog | Core Verification Focus | Excluded Components |
| :--- | :--- | :--- | :--- |
| **Execution-Boundary Suite** | `core-execution-boundary@1.0.0` | Proves one allowed governed mutation succeeds and one doctrine-prohibited equivalent fails closed through real Gateway and Data Operator runtimes. | No models, no model judges, no synthetic simulators. |
| **Formation Campaign** (System Lane) | `default-suite@1.2.0`, `smoke-suite@1.2.0`, or custom suite | Evaluates three-role heterogeneous formations (`lite`, `assistant`, `primary`) executing sequentially through governed Inference Operator sessions. | Direct provider calls, model judges, tool execution. |
| **Model-Role Campaign** (Model Lane) | `default-suite@1.2.0`, `smoke-suite@1.2.0`, or custom suite | Evaluates a single model acting as an autonomous agent in a bounded tool-calling loop (up to 8 turns) against scenario gold criteria. | Heterogeneous formation roles. |

### Evaluation CLI Architecture

The `ztd eval` command tree organizes platform evaluation across ten subcommands:
- `ztd eval boundary …`: Run, list, verify, and inspect native execution-boundary test suites.
- `ztd eval models …`: Catalog management: list, show, add, remove, import, freeze, pull, and diff model variants.
- `ztd eval suites …`: Manage scenario suites: list, show, export, create, update, and delete.
- `ztd eval campaigns …`: Freeze, inspect, tag platform release identity, archive, and unarchive campaigns.
- `ztd eval runs …`: Start, resume, cancel, verify, publish, export, repair, and compare evaluation runs.
- `ztd eval rollout …`: Manage rollout qualification queues: list, add, retry, skip, and run.
- `ztd eval formations …`: Manage heterogeneous model formations: list, show, add, remove, and smoke test.
- `ztd eval gates …`: Run pre-campaign acceptance gates (inference latency, capability probing).
- `ztd eval backup`: Archive evaluation evidence to an external directory with a SHA-256 manifest.
- `ztd eval restore [dir]`: Verify and restore evaluation evidence into `.ztd/` from complete snapshots.

`ztd eval runs start` and `ztd eval runs resume` stop after the current assignment on the first interrupt or on `ztd eval runs cancel`, and exit with `ErrEvaluationRunStopped` while assignments remain queued, so `runs start` does not verify a partial run. `ztd eval rollout run --gate-smoke` screens each model with `smoke-suite` before the full run; `--promote-on-pass` follows a smoke pass with the full run in the same batch.

`ztd eval runs resume` executes queued assignments. It fails closed if an assignment remains in `running` without a terminal persisted result; cancel that run and create a new one rather than retrying an ambiguous attempt. Archived runs also cannot resume.

### Executing a Formation Campaign

```bash
# 1. Start the Gateway with all required capabilities in the embedded Operator:
make gw MODEL_STORAGE_ROOT=~/.ollama/models

# 2. Check active capabilities and session identity:
./ztd gw status
./ztd eval models freeze
./ztd eval formations list

# 3. Create a campaign freezing the smoke-suite over a formation:
./ztd eval campaigns create smoke-run --formations <formation-id> --suite smoke-suite

# 4. Execute the campaign with strict witness verification:
./ztd eval runs start smoke-run --require-witness

# 5. Inspect run progress and verify evidence:
./ztd eval runs show <run-id>
./ztd eval runs assignments <run-id> --failed
./ztd eval runs verify <run-id> --require-observation --require-provenance
./ztd eval runs export <run-id>
```

### Native Execution-Boundary Suite

The native suite (`core-execution-boundary@1.0.0`) runs under `doctrine` posture without inference dependencies:

```bash
./ztd eval boundary run
./ztd eval boundary verify <run-id>
./ztd eval boundary show <run-id> --json
```

Execution steps:
1. Resolves the active Data Operator (`SelectDataOperator`).
2. Allowed Attempt: Submits a governed file edit writing a unique marker file to `.ztd/eval/targets/ztd-eval-<run-id>.txt` via the Gateway command ingress. Confirms signed `ActionReceipt` persistence and commitment ledger append.
3. Prohibited Attempt: Submits an equivalent malicious payload (e.g. attempting to overwrite protected runtime state or execute unauthorized binaries). Confirms immediate L1 Doctrine rejection (`ErrTxL1ValidationFailed`) with zero side effects.
4. Independent Observation: Reads the target marker file using an independent in-process host target reader (`ztd-host-target-reader`).
5. Offline Verification: `ztd eval boundary verify` re-computes all signatures, validates Merkle roots, checks deterministic stages, and confirms verdicts without executing new code.

### Scenario Suites and Catalog Management

Suites define scenario catalogs and test inputs:
- Built-In Suites: `default-suite` (27 scenarios) and `smoke-suite` (5 scenarios). Built-in suites are immutable.
- Custom Suites: Created and updated via `ztd eval suites create <file>` and stored under `.ztd/data/eval/suites/<id>.json`.
- Catalog Validation: Tool names must exist in the agent tool registry snapshot (`ExpectedTools` $\subseteq$ `AllowedTools`), prompt hints must be answerable, trajectory policies must be well-formed, every scenario must include a content check, and semantic-judge grading is rejected.
- Immutability: When a campaign is created, the suite's catalog is materialized and frozen under `.ztd/data/eval/campaigns/<campaign-id>/`. Future suite edits do not alter existing campaigns.

### Model Campaign Architecture: System Lane and Model-Role Lane

#### System Lane (Formation Campaigns)
- Evaluates multi-model collaboration across three distinct roles: `lite` (triage/routing), `assistant` (intermediate processing), and `primary` (final synthesis).
- The direct formation runner passes serialized input fixtures to the `lite` model via the Inference Operator, appends its output to the context, and invokes subsequent roles.
- Deterministic scenarios grade structural formation completion (`formation-roles`); this does not establish answer correctness. A scenario configured for semantic-judge grading becomes `GRADER_FAILED` with an unavailable structural `semantic-judge-available` grade because the direct formation runner records no judge evidence.
- Persists one canonical formation-run evidence envelope per assignment, binding run, assignment, attempt, campaign, scenario, formation, model registry digest, inference/data sessions, role telemetry, and witness digests. The assignment result references this content-addressed artifact, and verification checks its digest and independently recomputes the expected lifecycle and grades using the frozen scenario grading method.

#### Model-Role Lane (Single-Agent Campaigns)
- Freezes a campaign targeting exactly one model variant (`INV-EVAL-CAMP-06`).
- Executes assignments through `CampaignAgentExecutor` in a bounded loop of up to 8 turns.
- Injects workspace files into an isolated directory under the Data Operator's root (`ScenarioWorkspace`) via governed `FILE_EDIT` actions. Workspace paths follow the Data Operator host, not the CLI: a POSIX working directory keeps POSIX paths, and a drive-letter one (a Windows Operator) uses slash-form paths such as `D:/ztd/workspaces/...`. Run execution rejects a model-role binding whose working directory is not absolute before any assignment is marked running.
- Dispatches tool invocations as governed Gateway transactions (`FS_READ`, `FS_LIST`, `FS_GREP`, `FILE_EDIT`, `EXECUTE_BASH`).
- Seals the complete conversation and tool trajectory as a content-addressed agent trace (`campaign_agent_trace.go`) graded deterministically against scenario gold criteria.

### Scenario Fixtures, Trajectories, and Grading Models

- **Fixture Structure**: `ScenarioInputFixture` packages `scenario_id`, `synthetic_label`, `user_prompt`, optional `inline_context`, `workspace_files`, and `seed`.
- **Trajectory Policies**:
  - `ANSWER`: Model must answer without invoking tools.
  - `FIRST_CHOICE`: Model must call an expected tool on turn 1.
  - `GUIDED`: Model is guided toward expected tool choices.
  - `GOVERNED`: Model operates under full tool governance.
- **Grade Basis Hierarchy**: Every grade declares its basis: `OBSERVATION` (fact read from evidence), `DERIVED` (boolean function of observations), or `STRUCTURAL` (harness precondition). Only observation grades count toward score tallies.

### Witness Capabilities: Observer and Provenance Operators

- **Observer Operator**:
  - Enrolled on the provider host via `ztd operator start --capabilities observer`.
  - Listens for BEGIN and FINALIZE commands over pub/sub. Samples GPU memory, core utilization, temperature, power, clock rates, and host RAM.
  - Emits `ProviderBoundaryObservationCompleted` windows ingested by the Gateway.
- **Provenance Operator**:
  - Enrolled at the model storage site via `ztd operator start --capabilities provenance --model-storage-root <path>`.
  - On FINALIZE, resolves the served tag against Ollama manifests, computes SHA-256 digests over all model weight blobs, and verifies against the campaign's expected digest.
  - Emits `ModelProvenanceObservationCompleted` windows. Verification waits up to five minutes for durable attestation windows to land.

### Evidence Persistence, Backups, and Offline Verification

- **Evidence Layout**:
  - Run records: `.ztd/data/eval/runs/<run-id>/` (`report.json`, `verification.json`, `assignments/<assignment-id>-formation-run.json` for formation evidence, and `assignments/<assignment-id>-agent-trace.json` for model-role traces).
  - Campaign records: `.ztd/data/eval/campaigns/<campaign-id>/` (`campaign_spec.json`, frozen artifacts).
  - Witness windows: `data/inference/provider-observer/windows/` and `data/inference/model-provenance/windows/`.
- **Backup & Restore**:
  - `ztd eval backup` creates atomic timestamped snapshots in `eval/backups/` storing files by content digest (`objects/<prefix>/<sha256>`) with a verified `eval-backup.json` manifest. Evidence under `.ztd/data/eval/` is copied in its encrypted form, so no plaintext leaves the runtime and no vault is needed to take or restore a snapshot.
  - `ztd eval restore` verifies all files against the manifest and restores evidence into `.ztd/` without overwriting differing evidence unless `--overwrite` is specified.
- **Offline Verification**: `ztd eval runs verify <run-id>` verifies all cryptographic signatures, artifact digests, and stage parentage relationships completely offline without network access.

## Anti-patterns

- **Describing Local Observers as Measuring Remote GPUs**: Asserting that an embedded Observer Operator measures a remote provider's hardware (INV-EVAL-WIT-02).
- **Hand-Editing Frozen Campaign Artifacts**: Modifying JSON files in `.ztd/data/eval/campaigns/` instead of re-freezing via `ztd eval models freeze` (INV-EVAL-CAMP-08).
- **Running Scored Campaigns with Placeholder Digests**: Launching scored evaluations using unverified model digests derived from tags rather than provider attestation (INV-EVAL-CAMP-01).
- **Conflating Formation Completion with Answer Quality**: Treating a passing formation assignment as proof of semantic correctness when the direct runner grades structural completion only.
- **Deploying Redundant Outbound Operators for Local Evaluation**: Requiring separate outbound processes when the Gateway's embedded Operator already carries all necessary capabilities (INV-EVAL-WIT-01).
- **Modifying Verified Evidence During Restore**: Restoring evaluation backups without verifying file digests against the signed backup manifest (INV-EVAL-EVID-05).

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Gateway Architecture](gateway.md): Inference dispatch, provider-boundary coordination, and pub/sub.
- [Operator Architecture](operator.md): L4 Warden, L5 Actuator, and capability flags.
- [Governance Architecture](governance.md): Five-layer verification pipeline and fail-closed enforcement.
- [Model Provenance](model-provenance.md): Model weight attestation, Provenance Operator enrollment, and chain of custody.
- [Public Spectator Architecture](public_spectator.md): Public evaluation projections and explorer contract.
- [Position Paper](../core/position_paper.md): Conceptual framing for confidential edge execution and governed mutation.
- [Sovereignty Gauntlet](../guides/sovereignty_gauntlet.md): Step-by-step verification guide.

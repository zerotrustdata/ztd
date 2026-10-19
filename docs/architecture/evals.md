---
doc_id: evals
title: Evaluation Programs
audience: maintainers and coding agents
status: current
version: v1.1.3
last_updated: 2026-10-18
owners:
  - internal/services/evaluation/
  - internal/services/evaluation/conformance/
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
when_to_read: Understanding ztd's governance conformance suite, model-role campaigns and frozen-formation system-lane campaigns through the governed Inference Operator, provider-boundary observation, model provenance attestation, evidence persistence and verification, and capability selection for embedded or outbound Operators. Also use this document to distinguish the shipped campaign executor and evidence from the external agent SUT contract groundwork.
do_not_use_for:
  - Governance five-layer interlock and policy enforcement (governance.md)
  - Gateway HTTP routes and session routing (gateway.md)
  - Operator execution boundary and native tool catalog (operator.md)
  - Model weight zero-trust attestation (model-provenance.md)
  - Public evaluation projections and explorer contract (public_spectator.md)
---

# Evaluation Programs

## Purpose

Defines ztd's platform evaluation programs: the governance conformance suite and model-role and system-lane campaign evaluations. The conformance suite verifies zero-trust governance interlocks without models, model judges, campaigns, or synthetic simulators. Model-role campaigns run one registry model; system-lane campaigns bind a frozen formation's models to the supported Primary and Assistant loop roles. Both dispatch inference through the governed Inference Operator PEP and tools through governed Data Operator actions, capture provider-boundary hardware telemetry and storage-side weight attestation, and persist canonical evidence under the Local-First Audit Architecture (LFAA) for deterministic, offline verification.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Program Definitions (`INV-EVAL-PROG`)](#program-definitions-inv-eval-prog)
  - [Governance Conformance (`INV-EVAL-CONF`)](#governance-conformance-inv-eval-conf)
  - [Campaign Structure (`INV-EVAL-CAMP`)](#campaign-structure-inv-eval-camp)
  - [Witness Separation (`INV-EVAL-WIT`)](#witness-separation-inv-eval-wit)
  - [Evidence Storage (`INV-EVAL-EVID`)](#evidence-storage-inv-eval-evid)
  - [Verification Posture (`INV-EVAL-VERIF`)](#verification-posture-inv-eval-verif)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Topology: Confidential Edge Execution with Governed State Mutation](#topology-confidential-edge-execution-with-governed-state-mutation)
  - [Integration evidence boundary](#integration-evidence-boundary)
  - [Evaluation Programs Overview](#evaluation-programs-overview)
  - [Evaluation CLI Architecture](#evaluation-cli-architecture)
  - [Executing a Model Campaign](#executing-a-model-campaign)
  - [Governance Conformance Suite](#governance-conformance-suite)
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
| INV-EVAL-PROG-01 | The governance conformance suite (`governance-conformance@1.0.0`) MUST NOT use model providers, model judges, campaigns, scheduling, or synthetic simulators. |
| INV-EVAL-PROG-02 | Model campaigns MUST route scored model inference exclusively through governed dispatch to the selected Inference Operator session. The campaign CLI and the Gateway MUST NOT call model providers directly. |
| INV-EVAL-PROG-03 | All evaluation run evidence MUST persist under `.ztd/data/eval/runs/<run-id>/`, separate from campaign definitions and frozen scenario artifacts under `.ztd/data/eval/campaigns/<campaign-id>/`. |
| INV-EVAL-PROG-04 | Campaign verification (`ztd eval runs verify`) MUST recompute bindings and signatures deterministically from persisted evidence without executing new scored actions. Conformance verification (`ztd eval conformance verify`) MUST recompute every verdict from the persisted record and its pinned anchors without executing new actions or contacting the Gateway. |

### Governance Conformance (`INV-EVAL-CONF`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-CONF-01 | Cases are data. Each `cases.Case` MUST state its ingress, fault, one posture, and one `cases.Expect` per submission (two for a replay fault). Each catalogue row 1 to 26 MUST have a case or a `cases.Gap` that states its reason, and a gap MUST NOT share an (ingress, fault) cell with a case. `cases.Validate` MUST reject a catalogue with a duplicate case id, an unknown posture, ingress, or action, a wrong expectation count, an undefined code, or a doctrine case whose command L1 does not block offline, and `run.Run` MUST validate before it drives any case. (Covered conditions 1 and 2.) |
| INV-EVAL-CONF-02 | The verdict authority MUST be the executing runtime's signed `ActionReceipt`, verified by `oracle.VerifyReceipt` against that runtime's actuator key. The embedded executor's key MUST come from `oracle.LoadAnchor`, which reads the Gateway's own PKI export (`pki/Actuator_pub.json`) through its `RuntimeFileService`. The outbound executor's key MUST be the key bound at enrollment (`OperatorDocument.actuator_public_key`, `oracle.BoundAnchor`), and `oracle.CheckedBoundAnchor` MUST cross-check it against that Operator's export through its `RuntimeFileService`: a differing key id or key fails with `ErrConformanceAnchorInvalid`, and a missing export fails with `ErrConformanceAnchorMissing`. `run.Run` MUST refuse an outbound root without a bound anchor and an embedded root with one. The run record MUST pin the key id, public key, and export SHA-256 (`ConformanceAnchor`); `verify` does not re-hash the export. A verdict MUST NOT derive from the Gateway signer endpoint, an HTTP status, or error text. (Condition 3.) |
| INV-EVAL-CONF-03 | The stage chain MUST be recomputed from the receipt by `governance.ValidateDeterministicProtocolChain` (INV-GOV-EVID-01 to -03: order, parentage, identity) and the persistence attestation MUST be verified by `governance.VerifyActionReceipt` (INV-GOV-EVID-04), both called from `oracle.VerifyReceipt`. The oracle MUST NOT substitute a stage flag for the recomputed chain. (Condition 4.) |
| INV-EVAL-CONF-04 | Ledger bracket: `run.quiesce` MUST read two harness sentinel `FS_READ` receipts per executor before the first case, and `run.driveCase` MUST read two around each case. `oracle.VerifyBracket` MUST find the commitment links between the sentinels equal to the case's claimed `EXECUTED` receipts: one for `EXECUTED`, none for any other outcome. An extra link MUST fail the case with `UNCLAIMED_EXECUTION`. A stack that is not quiescent MUST abort the run without a record. (Condition 5.) |
| INV-EVAL-CONF-05 | Canary: `canary.Seed` MUST create the canary root `eval/targets/conformance-<run-id>/` (relative to the runtime root) through `RuntimeFileService`, and `canary.Snapshot` MUST run before and after each case (`run.driveCase`). An `EXECUTED` file mutation MUST change only its target, `eval/targets/conformance-<run-id>/cases/<attempt-id>.txt` (`canary.TargetRel` under the canary root), to its marker (`canary.ExactlyWrote`). An `EXECUTED` A2A mutation MUST leave the set byte-identical and produce one stub call carrying the marker. Every other outcome MUST leave the set byte-identical with no stub call (`verify.checkEffect`). (Condition 6.) |
| INV-EVAL-CONF-06 | `verify.VerifyPersisted` MUST compute every verdict from the stored record bytes, their SHA-256 sidecar, the anchors pinned in the record, and the catalogue compiled into the binary. It MUST NOT contact the Gateway or read a store: the CLI loads the bytes (`verifyStoredConformanceRun`). A changed record or sidecar byte MUST yield suite `INVALID_EVIDENCE` with class `RECORD_DIGEST_MISMATCH` (`verify.digestMismatchSet`), including a sealed file that fails vault authentication (`verify.SealedRecordTamperedSet`). A verdict set MUST carry only case id, status, and a fixed `ConformanceFailureClass` (INV-EVAL-EVID-08). (Condition 7.) |
| INV-EVAL-CONF-07 | Every oracle and verifier check MUST have a negative-control test that fails on forged, missing, extra, or reordered evidence. Examples: `TestVerifyAgainstNegativeMutations` and `TestVerifyPersistedEveryFlippedBitIsInvalidEvidence` (`verify`), `TestVerifyReceipt` and `TestVerifyBracket` (`oracle`), and the `TestValidateRejects*` tests in `cases`. (Condition 8.) |
| INV-EVAL-CONF-08 | The posture MUST be read from the Gateway health endpoint (`/api/v1/health`) by `gatewayPosture` and recorded in the run record. A run MUST execute only the cases that `cases.Applicable` returns for its posture, and `verify` MUST reject a record whose case set differs (`checkCaseSet`). Each posture MUST have one run, executed against a stack that `/api/v1/health` reports in that posture; `report.Build` rejects two runs for one posture. An attended case skipped in an unattended run, an outbound case without an outbound selection, or an A2A case without a harness-owned downstream MUST carry its matching typed not-run observation and verdict `UNAVAILABLE`, so the suite is `INCOMPLETE` and its report cell is `PARTIAL`, never `COVERED` (`report.cellFor`). Required embedded ingresses MUST NOT be skipped for optional setup. Unavailable cases MUST NOT contain execution evidence, and a pinned outbound runtime MUST NOT be declared unavailable. |

### Campaign Structure (`INV-EVAL-CAMP`)

| ID | Rule |
| :--- | :--- |
| INV-EVAL-CAMP-01 | Model campaigns bind scored inference to frozen `served_model_tag` and `model_digest` pairs registered in the campaign's frozen model inventory. Placeholder digests lacking provenance authority MUST be re-frozen from the provider prior to scored runs. |
| INV-EVAL-CAMP-02 | Campaign execution runs each assignment through `CampaignAgentExecutor`. A system-lane assignment MUST use a frozen, digest-validated stack from the catalog formations captured by the campaign's `execution-topologies.v1` formation stack set. Formation campaigns currently support one repetition only; CLI and controller MUST reject a larger repetition count or `hold_context`. Primary agent turns resolve to the stack's Primary slot; Tribunal deliberation calls resolve to its Assistant slot. The Lite slot remains frozen but is not executed or graded; Triage, Dash, and Codex are not supported scored roles. `heterogeneous-v1` is retired and MUST NOT be accepted as a generation rule. Every model call MUST be a governed Inference Operator dispatch and every tool call a governed Data Operator action through the Gateway. A run whose frozen posture requires L3 MUST still fail closed before execution (`INV-EVAL-CAMP-40`); evaluation does not mint or transfer an L3 approval proof. |
| INV-EVAL-CAMP-03 | On `ztd eval rollout run`, strict witness verification (provider-boundary observation and model provenance) MUST be enabled unconditionally and MUST fail closed when coverage is missing. On `ztd eval runs start`, witness requirements are opt-in via `--require-observation`, `--require-provenance`, or `--require-witness`. When strict provenance verification is required, the verifier MUST wait up to five minutes for durable attestation completion windows before failing closed. Witness waits MUST be driven by the Gateway's ephemeral `attestation.recorded` and `observation.recorded` SSE events, with one read of each pending window per SSE (re)connect, and MUST NOT re-read on a timer. |
| INV-EVAL-CAMP-05 | Each catalog scenario MUST declare `eligible_roles`. The model-role scheduler MUST assign a scenario only to its eligible roles and MUST fail closed (`ErrEvaluationScenarioRolesUnassigned`) on a scenario with none. |
| INV-EVAL-CAMP-06 | A `model_role` run MUST have exactly one subject model. `CampaignController.StartRun` MUST reject a `model_role` run whose campaign freezes any other number of models (`ErrEvaluationCampaignSubjectInvalid`). |
| INV-EVAL-CAMP-08 | A campaign scores exactly one suite and freezes it at creation. Every subsequent reader of a campaign's scenarios MUST load the frozen catalog and artifacts (`Store.LoadScenarioCatalog`, `Store.LoadScenarioArtifacts`) and MUST NOT regenerate them from built-in or stored suites. |
| INV-EVAL-CAMP-11 | Catalog build fails closed unless `ExpectedTools` is a subset of `AllowedTools`, no tool is both allowed and forbidden, and every named tool exists in the agent tool registry snapshot. |
| INV-EVAL-CAMP-12 | Every scenario declares a trajectory policy (`ANSWER`, `FIRST_CHOICE`, `GUIDED`, or `GOVERNED`). Every scenario MUST carry a content constraint (`ScenarioContentCheck`) so that it can fail, and MUST NOT declare semantic-judge grading or `semantic_grade` evidence: no executor records judge evidence, so such a scenario could only terminate `GRADER_FAILED`. |
| INV-EVAL-CAMP-20 | Campaign evidence digests MUST use the Go canonical JSON encoder (`MarshalCanonicalJSONObject`) pinned by `protocol/vectors/eval/canonical_json_numbers.json`. |
| INV-EVAL-CAMP-24 | Every `DeterministicGrade` MUST state one `GradeBasis` (`OBSERVATION`, `DERIVED`, or `STRUCTURAL`). `tallyDeterministicGrades` counts only `OBSERVATION` grades; a failed `STRUCTURAL` grade marks the verdict `INVALID_EVIDENCE`. |
| INV-EVAL-CAMP-27 | A campaign states the platform release that froze it (`EvaluationCampaignSpec.platform_release` and `source_revision`). `StartRun` fails with `ErrEvaluationPlatformReleaseMismatch` when a recorded release differs from the executing binary build. |
| INV-EVAL-CAMP-28 | `ztd eval rollout run` MUST NOT mark a queue entry verified, or promote it from the `--gate-smoke` gate with `--promote-on-pass`, when the run stopped with assignments still queued (`ErrEvaluationRunStopped`) or when every assignment ended `PROVIDER_FAILED` (`ErrEvaluationModelUnserved`). Such a run's evidence still verifies, but it does not cover the model. A stopped run ends the batch and leaves its entry pending; an unserved model marks the entry `failed: provider_failure (<attempt class>)`, naming the inference attempt failure class most of the run's agent traces record (for example `capability_unsupported`). |
| INV-EVAL-CAMP-29 | An agent trace that ends `PROVIDER_FAILED` records the inference attempt failure class (`AgentTrace.provider_failure_class`), recovered from the `InferenceDispatchFailureCode` on the Gateway's dispatch failure body. The role-invoked grade detail reads `designated model role was not invoked: <failure> (<class>)`. The result's public failure sentence MUST describe that dispatch outcome (capability rejection, context overflow, timeout, or other classified failure), rather than attributing missing output to model behavior. CLI terminal summaries MUST lead with that sentence for provider failures; canonical grades remain available in result evidence. The Explorer Live view MUST include events across runs in the selected release, even before their summaries arrive, and MUST link failed assignment rows directly to Assignment Details, where the available public failure sentence is displayed. The Live landing page uses compact, accessible result indicators and keeps diagnostic prose in the detail view. Traces without a class keep the legacy grade detail and still verify. |
| INV-EVAL-CAMP-30 | Deleting a run (`ztd eval runs delete`, `ztd eval campaigns delete`, `evaluation.Deleter`) MUST discard its publication through the Gateway before removing local evidence, so the public mirror never serves a run whose evidence is gone. Withdrawal hides the run's projections from anonymous mirror reads and prunes its proof artifacts without rewriting the signed batch chain (`INV-SPEC-CRYPTO-01`). A run held by a live lease MUST NOT be deleted. A delete MUST also purge the deleted runs and campaign from every snapshot in the default backup directory (`eval/backups/`), removing objects only the purged entries referenced, so `ztd eval restore` never brings deleted evidence back. It holds the runtime's backup lock (`.ztd/eval/backup.lock`) exclusively from start to finish; snapshots and restores hold it shared, so a delete never runs during a snapshot and no snapshot captures evidence mid-delete. |
| INV-EVAL-CAMP-31 | Model capability observations MUST be diagnostic evidence only. Rollout intake MUST NOT exclude a model based on size, family, or a saved capability probe outcome. Each evaluation MUST dispatch its tool declarations through governed inference and record the current model/provider response; saved observations MUST NOT suppress the request, remove tools, or synthesize an unsupported outcome. Rollout intake skips only digest aliases of queued models or variants added earlier in rollout order, naming the kept tag. The tool-calling probe declares tool choice `AUTO`, as agent scenarios do. Every capability probe (tool calling, structured output, thinking) records `UNSUPPORTED` only for a provider capability rejection (`capability_unsupported`); any other dispatch failure, a timeout included, is a `FAIL` naming its attempt class. |
| INV-EVAL-CAMP-32 | A model-driven run (any lane but platform) MUST NOT execute on a Data Operator whose working directory is, contains, or sits inside the ztd project: the model under evaluation runs arbitrary shell commands there (`ValidateAgentDataOperator`, checked before any assignment is marked running). The one exception is a throwaway Operator directly under `<project>/.ztd/operators/<name>`: the Gateway's embedded Operator (`.ztd/operators/embedded-operator`, the default Data Operator) and the Operators `ztd operator deploy --local --background` creates by default; the runtime operators directory itself, anything below an Operator directory, and the rest of the runtime tree are rejected. |
| INV-EVAL-CAMP-33 | Every rollout queue edit (`rollout add`, `remove`, `retry`, `skip`) MUST hold the queue lock (`.ztd/eval/rollout-queue.lock`, `LockRolloutQueue`) across its load/modify/save, and `rollout run` (except `--dry-run`) MUST hold it from planning to the end of the batch. A held lock fails the command with `ErrRuntimeFileLocked` and changes nothing, so no edit overwrites an outcome the run records. The lock file is excluded from evaluation backups. |
| INV-EVAL-CAMP-34 | An empty completion (no text, thinking, or tool call) from a served model is the model's answer, not a provider fault. The inference backend returns it as a receipted empty `InferenceResult`, and neither Gateway dispatch nor `ValidateInferenceProbeResponse` rejects it as `provider_response_invalid`. An agent turn that answers empty ends the assignment `COMPLETED` with an empty final output, so it grades `FAIL` and counts against the model. It never ends `PROVIDER_FAILED` or `INVALID_EVIDENCE`, so a mostly-empty smoke run cannot pass the INV-EVAL-CAMP-28 gate on its few graded assignments. A prompt that fills the context window and comes back empty stays `context_overflow`. |
| INV-EVAL-CAMP-35 | Campaign Operator role resolution MUST route through `CampaignOperatorTopology` (`internal/services/evaluation/campaign_operator_topology.go`), resolving typed `Inference` and `Data` runtimes with enrollment-bound actuator anchors (`WithAnchors`). A shared runtime (`Inference.OperatorID == Data.OperatorID`) MUST pin exactly one actuator key (`proto.Equal`) across both roles (`ErrConformanceAnchorInvalid`). |
| INV-EVAL-CAMP-36 | Traces MUST record signed `ActionReceipt` evidence for every governed inference call (`AgentModelCall.receipt`), including `FAILED` receipts from provider failures. In v7, every call MUST also bind its frozen model variant, role, served-model tag, digest, and complete inference result; verification MUST match the result digest and transcript telemetry to the signed receipt and frozen registry slot. Completed calls MUST match the receipt transaction and result digest. On a shared runtime, model-call commitments MUST be claimed inside the Data ledger bracket alongside tool calls, and claimed executions for each role MUST appear in strict ledger commitment order (`ErrConformanceLedgerGap`). |
| INV-EVAL-CAMP-37 | A model-role campaign with `hold_context` (frozen in the spec digest, set only when repetitions exceed 1) MUST continue each repetition after the first from the sealed trace of the same variant, scenario, and arm at the previous repetition (`heldAssignmentContext`). The trace's `held_context` records that trace's assignment id, repetition, evidence digest, workspace, and full conversation. The model receives that conversation, then the scenario prompt again, with no repeated system prompt or seed turns. The repetition reuses the previous workspace root and re-materializes the fixtures into it. A missing previous trace, a changed Data Operator or working directory, a malformed held workspace, or a conversation over the message bound fails the assignment with `ErrEvaluationHeldContextUnavailable`. The assignment never falls back to a fresh context. |
| INV-EVAL-CAMP-38 | Fixture materialization and harness file probes through `CommandLane` MUST use a separate harness investigation scope and phase-prefixed task IDs. They MUST NOT carry the scored action's investigation ID, so filtering scored investigation history cannot reveal planted-fault setup receipts. |
| INV-EVAL-CAMP-39 | Every newly frozen campaign MUST digest-bind `EvaluationCampaignSpec.investigation`. Omitted configuration freezes `FRESH`; the executor creates a distinct investigation ID per assignment. `PERSISTENT` MUST bind a non-harness-reserved `investigation_id` and a stable `data_operator_id`; its Operator session may change between runs, but its Operator identity MUST match the execution topology. A legacy spec without `investigation` remains verifiable by its original digest and MUST NOT be rewritten. |
| INV-EVAL-CAMP-40 | A campaign freezes any known governance posture (`doctrine`, `consensus`, `ratify`, `notary`); an unspecified or unknown value MUST fail with `ErrEvaluationPostureUnsupported` (`ValidateCampaignSpecPosture`). `ztd eval campaigns create --posture doctrine\|consensus\|ratify\|notary` selects it and defaults to `doctrine`. The posture is part of the campaign digest, so `CampaignController.CreateCampaign` treats recreation with the same posture as a no-op and a different posture under an existing campaign ID as `ErrEvaluationCampaignConflict`. `CampaignController.StartRun` and `ScheduleRun` re-validate the frozen posture. `ztd eval runs start` and `ztd eval runs execute` additionally read the live posture from `/api/v1/health` (`gatewayPosture`) and MUST reject a Gateway whose posture differs from the frozen one (`ValidateGatewayPosture`, `ErrGatewayPostureMismatch`), so a run never records a posture it did not execute under. Under `ratify` and `notary`, evaluation inference and tool dispatch suspend and wait for a real human decision through the Gateway's blocking dispatch ([Layer 3 (L3) Notary: Human Authorization](governance.md#layer-3-l3-notary-human-authorization)); the harness and evaluation code MUST NOT mint, synthesize, or substitute approval. The governed inference dispatcher and the agent harness HTTP clients therefore allow `mcp.ApprovalRequestTTL` plus the provider-aware dispatch deadline (`inference/dispatch.RequestDeadline`) per request. A denial is the Operator-signed `L3_DENIED` rejection, and an approval left unanswered for `mcp.ApprovalRequestTTL` fails the dispatch with `ErrTransactionExpired`. |

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
| INV-EVAL-EVID-01 | Conformance run evidence (`conformance-record.json` and its `conformance-record.sha256` sidecar) and campaign run evidence MUST persist under `.ztd/data/eval/runs/<run-id>/`. |
| INV-EVAL-EVID-02 | Provider-boundary observation windows MUST be stored under the Gateway volume at `data/inference/provider-observer/windows/`. The eval CLI MUST keep the provider-boundary windows, provider-attempt records, and model provenance attestation windows it fetches for a run in sealed eval evidence under `.ztd/data/eval/runs/<run-id>/evidence/`, MUST verify and export only from those sealed copies, and MUST NEVER write them under `data/inference/`. The Gateway stores model provenance attestation windows under `data/inference/model-provenance/windows/`. |
| INV-EVAL-EVID-04 | Public-safe campaign projections MUST omit principal, Operator, session, credential, endpoint, path, raw target, envelope, receipt, audit, and evidence body fields. Private prompts, gold criteria, and execution identifiers MUST NOT cross the public boundary. |
| INV-EVAL-EVID-05 | `ztd eval backup` MUST write only to destinations outside the runtime tree, copying `.ztd/data/eval/` and `.ztd/eval/` with a SHA-256 manifest. `ztd eval restore` MUST verify every file against the manifest before writing. Evidence is backed up in its encrypted form (INV-EVAL-EVID-07), and each manifest (schema `3.0.0`) MUST list the vault key chain that sealed it (`vault/vault.header`, `secrets/vault_key`, and the key store's sealed master key blob where it keeps one), copied as stored, so a snapshot survives wiping and reinitializing `.ztd/`. A snapshot that lists sealed evidence without the vault header and vault key is invalid (`ErrEvaluationBackupKeyChainMissing`). `ztd eval restore` MUST open each snapshot's vault from its key chain with the host key store (`ErrEvaluationBackupKeyChainInvalid` when the key store cannot) before writing anything, and MUST write sealed records back through the evidence file service, sealing them under the current runtime vault. Every file under the sealed roots MUST authenticate: `ztd eval backup` fails before writing a snapshot when the runtime vault cannot open one (`ErrEvaluationEvidenceDecryptFailed`), and `ztd eval restore` fails before writing anything when a snapshot's vault cannot open a file it lists (`ErrEvaluationBackupEvidenceUnopenable`). Neither copies an unopenable file as stored. Backups and restores hold the runtime's backup lock shared, so they never run during a delete that purges the backups (INV-EVAL-CAMP-30). |
| INV-EVAL-EVID-06 | A run's provider environment MUST be derived only from provider-boundary observation windows and projected only once the run aggregate is complete. The field MUST be omitted if windows disagree on capacity. |
| INV-EVAL-EVID-07 | Every file under `.ztd/data/eval/` MUST be encrypted with the runtime vault (`evaluation.NewEvidenceFileService`, wired by `shared.NewEvidenceFileSvc` in every command that reads or writes evaluation evidence), bound to the `ztd-eval-evidence-v1` associated-data label so a ciphertext from another vault-sealed store does not authenticate. The label is not path-bound because archiving moves whole run and campaign trees. A missing or locked vault fails the read or write (`ErrEvaluationEvidenceVaultRequired`, `ErrVaultLocked`), and a file that does not authenticate fails with `ErrEvaluationEvidenceDecryptFailed`; there is no plaintext fallback. `runs export` writes its disclosure-safe public projection in the clear to `.ztd/export/<run-id>/` (`constants.EvaluationCampaignExportDirname`) and rejects an output directory under a sealed root (`ErrEvaluationExportDirSealed`), so no plaintext file ever sits beside sealed evidence. Compliance report bundles under `.ztd/reports/`, which carry raw evaluation sources for replay, are sealed by the same service under their own `ztd-compliance-report-v1` label and leave the runtime in the clear only through `compliance report export`. |
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
| Governance conformance: oracle | `internal/services/evaluation/conformance/oracle/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/oracle` |
| Governance conformance: oracle test builder | `internal/services/evaluation/conformance/oracle/oracletest/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/oracle/oracletest` |
| Governance conformance: cases and catalogue | `internal/services/evaluation/conformance/cases/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/cases` |
| Governance conformance: canary | `internal/services/evaluation/conformance/canary/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/canary` |
| Governance conformance: drivers | `internal/services/evaluation/conformance/drive/` | `./ztd test integration --pkg ./internal/services/evaluation/conformance/drive` |
| Governance conformance: runner | `internal/services/evaluation/conformance/run/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/run` |
| Governance conformance: offline verifier | `internal/services/evaluation/conformance/verify/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/verify`, `./ztd eval conformance verify --run <run-id>` |
| Governance conformance: coverage and recall report | `internal/services/evaluation/conformance/report/` | `./ztd test unit --pkg ./internal/services/evaluation/conformance/report`, `./ztd eval conformance report --run <run-id>` |
| Governance conformance: record store and compliance admission | `internal/services/evaluation/conformance_store.go`, `internal/services/evaluation/conformance_compliance.go` | `./ztd test unit --pkg ./internal/services/evaluation --run TestConformance` |
| Governance conformance: CLI group | `internal/cli/cmd/eval/conformance.go` | `./ztd test unit --pkg ./internal/cli/cmd/eval --run TestConformance` |
| Campaign controller and scheduler | `internal/services/evaluation/campaign_controller.go` | `ztd eval runs start`, `resume`, `verify` |
| Model-role agent executor | `internal/services/evaluation/campaign_agent_executor.go` | `ztd eval runs start` on model-role campaigns |
| Formation catalog | `eval/formation-catalog.json`, `eval/formation-catalog-overlay.json` | `ztd eval formations list` |
| Provider-boundary observation | `internal/services/operatorcapability/provider_boundary_observer.go` | Observer Operator enrollment and window ingestion |
| Model provenance attestation | `internal/services/operatorcapability/provenance_operator.go` | Provenance Operator enrollment and digest hashing |
| Data Operator selection | `internal/services/operatorcapability/data_operator.go` | `SelectDataOperator` resolution tests |
| Campaign operator topology | `internal/services/evaluation/campaign_operator_topology.go` | `./ztd test unit --pkg ./internal/services/evaluation --run TestCampaignOperatorTopology` |
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

### Integration evidence boundary

`fixtures.NewEvalStack` exercises the campaign client against a production Gateway and separately rooted outbound Data and Inference Operators over mTLS. Its frozen Ollama replies are the only model substitute. The CLI test uses the production application enrollment, authentication, Operator binding, harness client and campaign executor factories, then verifies the imported trace against receipts read from the executing Operator's local store. Ratify and notary approvals are signed by the enrolled owner CLI identity and submitted through the production approval HTTP route; notary also requires the fixture's real signature quorum. See [the testing guide](../devs/tests.md#owned-surfaces) for fixture ownership and lifecycle.

This Tier 2 rig establishes in-process production-path coverage. It does not establish separate-process behavior, live model quality or completion of the remaining eval-test migrations.

### Evaluation Programs Overview

| Program | Suite / Catalog | Core Verification Focus | Excluded Components |
| :--- | :--- | :--- | :--- |
| **Governance Conformance Suite** | `governance-conformance@1.0.0` | Proves, per posture, that each governed fault at each ingress ends in its one expected typed outcome, backed by the executing Operator's signed receipts and no unclaimed side effect, through real Gateway and Operator runtimes. | No models, no model judges, no campaigns, no scheduling, no synthetic simulators. |
| **Model-Role Campaign** (Model Lane) | `default-suite@1.4.0`, `smoke-suite@1.4.0`, or custom suite | Evaluates one frozen model variant as an autonomous agent in a bounded tool-calling loop (up to 8 turns) against scenario gold criteria. | Multi-model formation assignments. |
| **Formation Campaign** (System Lane) | Checked-in execution-topology formations captured in the campaign's frozen stack set | Evaluates the supported Primary agent-turn and Assistant Tribunal calls using distinct stack slots in the real investigation loop. Grading covers only roles that execute; Lite remains frozen but unused, and Triage/Dash/Codex are not scored. | Arbitrary synthesized stacks and L3-gated campaign execution. |

### Evaluation CLI Architecture

The `ztd eval` command tree organizes platform evaluation across ten subcommands:
- `ztd eval conformance …`: Run, verify, and report the governance conformance suite.
- `ztd eval models …`: Catalog management: list, show, add, remove, import, freeze, pull, and diff model variants.
- `ztd eval suites …`: Manage scenario suites: list, show, export, create, update, and delete.
- `ztd eval campaigns …`: Freeze, inspect, tag platform release identity, archive, unarchive, and delete campaigns.
- `ztd eval runs …`: Start, resume, cancel, verify, publish, export, repair, compare, archive, unarchive, and delete evaluation runs.
- `ztd eval rollout …`: Manage rollout qualification queues: list, add, retry, skip, and run.
- `ztd eval formations …`: Manage the heterogeneous formation catalog: list, show, add, and remove.
- `ztd eval gates …`: Run pre-campaign acceptance gates (inference latency, capability probing).
- `ztd eval backup`: Archive evaluation evidence to an external directory with a SHA-256 manifest.
- `ztd eval restore [dir]`: Verify and restore evaluation evidence into `.ztd/` from complete snapshots.

`ztd eval runs start` and `ztd eval runs resume` stop after the current assignment on the first interrupt or on `ztd eval runs cancel`, and exit with `ErrEvaluationRunStopped` while assignments remain queued, so `runs start` does not verify a partial run. `runs cancel` stops the remaining assignments and, unless `--publish=false`, publishes them with the run summary, whose `lifecycle_state` is `stopped` once every assignment is terminal and any was stopped. `ztd eval rollout run --gate-smoke` screens each model with `smoke-suite` before the full run; `--promote-on-pass` follows a smoke pass with the full run in the same batch. The batch holds the rollout queue lock (INV-EVAL-CAMP-33), so queue edits and a second batch fail until it ends.

`ztd eval runs delete` and `ztd eval campaigns delete` remove runs, active or archived, with their evidence; `campaigns delete` also removes the campaign and its inventory. Each run's publication is discarded first through the Gateway (`DELETE /api/v1/eval/campaign/runs/<run>/publication-state`): the mirror withdraws the run's dataset from anonymous reads, the publisher prunes its proof artifacts and pushes the proof catalog, and its publication idempotency is cleared. The signed batch chain is not rewritten. Every snapshot in `eval/backups/` then drops the deleted paths: a snapshot left empty is removed, and a stored object no remaining entry references is deleted. A delete fails without changing anything while a backup or restore holds the backup lock. A run or campaign already gone from the runtime tree but still in the backups is deleted from the backups; a campaign deleted that way loses only its campaign tree and inventory, because its runs cannot be listed without the runtime tree, so they are deleted by run ID. `archive` keeps a run and leaves its public projections in place.

`ztd eval runs resume` executes queued assignments. It fails closed if an assignment remains in `running` without a terminal persisted result; cancel that run and create a new one rather than retrying an ambiguous attempt. Archived runs also cannot resume.

### Executing a Model Campaign

```bash
# 1. Start the Gateway with all required capabilities in the embedded Operator:
make gw MODEL_STORAGE_ROOT=~/.ollama/models

# 2. Check active capabilities and session identity, and freeze model digests:
./ztd gw status
./ztd eval models freeze

# 3. Create a campaign freezing the smoke-suite over one registry model:
./ztd eval campaigns create smoke-run <model-tag> --suite smoke-suite

# 4. Execute the campaign with strict witness verification:
./ztd eval runs start smoke-run --require-witness

# 5. Inspect run progress and verify evidence:
./ztd eval runs show <run-id>
./ztd eval runs assignments <run-id> --failed
./ztd eval runs trace <run-id> --scenario <scenario-id>
./ztd eval runs verify <run-id> --require-observation --require-provenance
./ztd eval runs export <run-id>
```

### Governance Conformance Suite

The governance conformance suite (`governance-conformance@1.0.0`, `cases.SuiteID` and `cases.SuiteVersion`) submits governed faults through each ingress of a live native stack and checks each outcome against the executing Operator's signed evidence. It runs no models, model judges, campaigns, or schedulers. Each case states one exact expected outcome. The verdict comes from the Operator's receipt, never from the Gateway's response.

A behaviour is covered only when all eight conditions hold. Each condition is one invariant: 1 and 2, case as data and full applicability (`INV-EVAL-CONF-01`); 3, out-of-band trust anchor (`INV-EVAL-CONF-02`); 4, recomputed stage chain (`INV-EVAL-CONF-03`); 5, ledger-bracket completeness (`INV-EVAL-CONF-04`); 6, independent effect observation (`INV-EVAL-CONF-05`); 7, offline reproducibility (`INV-EVAL-CONF-06`); 8, no vacuous pass (`INV-EVAL-CONF-07`). `INV-EVAL-CONF-08` fixes the per-posture runs and the attended-case status. Each coverage cell takes one of five states from `report.cellFor`:

| State | Meaning |
| :--- | :--- |
| `COVERED` | Every case in the cell PASSes. |
| `PARTIAL` | Every case in the cell PASSes except attended cases that an unattended run did not run. |
| `MISSING` | No verified run for the posture, a suite verdict of `INVALID_EVIDENCE`, or a case that FAILs or is `INVALID_EVIDENCE`. The reason lists the failure classes. |
| `NOT_EXPRESSIBLE` | A catalogue gap: the ingress cannot express the fault. The reason is the gap's reason. |
| `NOT_APPLICABLE` | The row has no case for the posture. |

Each submission ends in one outcome. The verifier accepts an outcome only with the evidence below (`verify/case.go`, `oracle.Classify`):

| Outcome | Required evidence |
| :--- | :--- |
| `EXECUTED` | A `COMPLETED` receipt that verifies against the anchor, with a completed `L5_EXECUTION` stage and one `COMMITMENT_APPEND` link. `L2Status` and `L3Status` match the expectation. The bracket holds exactly that link. The canary effect matches (`INV-EVAL-CONF-05`). |
| `REJECTED{code}` | A signed `FAILED` receipt with `failure_code` `GOVERNANCE_REJECTED` and `rejection_code` equal to `code`. No `L5_EXECUTION` or commitment stage. The bracket holds no link. The canary is byte-identical. |
| `REFUSED_PRE_ENVELOPE{code}` | An `IngressRefusal` with code `code` and no receipt. The bracket holds no link. The canary is byte-identical. |
| `SUSPENDED` | An approval-pending observation that carries the transaction hash and no receipt. The bracket holds no link. The canary is byte-identical. |

A FAILED L5 execution receipt (`oracle.OutcomeExecutionFailed`) matches none of the four outcomes and fails the case that expects one of them.

The ingresses, the driver that reaches each one, and the executor that signs for it:

| Ingress | Executor | Driver | Route |
| :--- | :--- | :--- | :--- |
| `DIRECT` | embedded | `drive.NewDirect` | `POST /api/v1/governance/envelopes` |
| `DISPATCH_EMBEDDED` | embedded | `drive.NewDispatch` | `POST /api/v1/operators/commands` |
| `DISPATCH_OUTBOUND` | outbound | `drive.NewDispatch` | `POST /api/v1/operators/commands` |
| `MCP` | embedded | `drive.NewMCP` | `POST /mcp`, `tools/call` |
| `A2A` | embedded | `drive.NewA2A` | `POST /api/v1/a2a/call` |
| `CLI` | outbound | `drive.NewCLI` (`operator.RunOnce`) | `POST /api/v1/operators/commands` with the CLI identity |

The embedded executor is the Gateway's own Operator (`oracle.GatewayRuntimeID`). Conformance discovers active Data Operators, including the embedded Operator. A single candidate is selected automatically; several candidates produce a numbered prompt showing names, hosts, and sessions. `--operator-id` optionally pins a runtime for automation. JSON mode never prompts and requires an explicit choice when several candidates are available. Inference-only and inactive Operators are excluded.

Selecting the embedded Operator needs no separate runtime root. Selecting an outbound Data Operator adds outbound dispatch and CLI coverage; its runtime root is discovered from enrollment metadata, with `--operator-root` available for a locally mounted path. The enrollment-bound actuator key is still cross-checked against that runtime's export before any case runs. An inaccessible or mismatched root fails the run.

The catalogue is data in `cases/catalogue.go`: `rowSpecs` holds the cases of rows 1 to 24, and `gapSpecs` holds the cells an ingress cannot express (rows 2 and 25). Row 25 has no case. It expands to 439 cases: 279 on `DIRECT`, 37 on `DISPATCH_EMBEDDED`, 37 on `DISPATCH_OUTBOUND`, 32 on `MCP`, 28 on `A2A`, and 26 on `CLI`. Per posture: doctrine 113, consensus 104, ratify 111, notary 111. Row 13 expands over the 49 doctrine attack entries, and row 14 over the first entry of each of the five corpus categories. There are 4 gaps: row 2 on `A2A` (every A2A call is a mutation) and `CLI` (the CLI sends only `EXECUTE_BASH`), and row 25 on `MCP` and `A2A` (no argument carries an L3 proof). Rows 22 to 24 are the human approval path under ratify and notary. `MCP` and `A2A` answer an unattended mutation with a suspension (row 22). `DISPATCH_EMBEDDED`, `DISPATCH_OUTBOUND`, and `CLI` have no such row: under ratify and notary the Gateway's governed dispatch suspends the envelope and holds the request open for up to `mcp.ApprovalRequestTTL` until a human decides, so the suspension is not observable without waiting out the TTL, and the Gateway never refuses these ingresses for lack of a mintable L3 proof (`ErrTxL3ProofUnmintable` is only the public `BuildGovernanceEnvelope`'s refusal). Twenty cases are attended: rows 23 (approve) and 24 (deny) on all five approval ingresses (`DISPATCH_EMBEDDED`, `DISPATCH_OUTBOUND`, `MCP`, `A2A`, `CLI`) under ratify and notary. Row 23 expects an `EXECUTED` receipt with `L3Status` `REQUIRED_VALID`. Row 24 expects the Operator-signed `L3_DENIED` rejection, which the dispatch ingresses return as their answer to the held request.

For DIRECT cases under consensus and notary, the harness discovers the bound consensus application through `GET /api/v1/auth/platform-enrollments/consensus`, then posts the full envelope to that application's HTTPS endpoint using `application/x-protobuf`. It preserves the Gateway CLI identity headers only on discovery, rejects redirects and plaintext endpoints, and submits the returned proof through direct ingress. Row 18's below-quorum fault retains the set ID while removing votes; for a single application it removes the sole vote. Direct ingress does not acquire missing proofs.

| Package | Owns |
| :--- | :--- |
| `oracle` | Anchors (`LoadAnchor`, `BoundAnchor`, `CheckedBoundAnchor`, `AnchorSet`), receipt verification (`VerifyReceipt`), outcome classification (`Classify`), and the ledger bracket (`VerifyBracket`). |
| `oracle/oracletest` | Test-only builder of signed receipts with stages. |
| `cases` | Case, gap, and expectation types; `Catalogue`, `Applicable`, and `Validate`; marker commands. |
| `canary` | Canary seed, `Snapshot`, `Diff`, and `ExactlyWrote`, all through `RuntimeFileService`. |
| `drive` | One driver per ingress, envelope fault mutators, the `Sentinel`, the `Attended` wrapper, and the A2A stub. Drivers report what each ingress answered and decide nothing. |
| `run` | Evidence capture and record assembly (`Run`). It writes no verdicts. |
| `verify` | Offline verdicts from the record bytes (`VerifyPersisted`). |
| `report` | Coverage cells and L1 recall (`Build`, `Markdown`). |

`ztd eval conformance run` executes these steps (`run.Run`, `cli/cmd/eval/conformance.go`):
1. `gatewayPosture` reads the posture from `/api/v1/health`. `conformanceTargets` discovers the embedded Operator and selects an active Data Operator automatically or through a numbered prompt. Selecting an outbound Operator includes its single active session.
2. `run.Run` validates the catalogue, pins one anchor per available executor, seeds one canary root per available executor, and takes the quiescence pair (two sentinel reads) per available executor. The embedded anchor comes from the Gateway's own PKI export. The outbound anchor is the key bound at enrollment, cross-checked against the Operator's export before any case runs. A non-quiescent stack aborts the run.
3. For each applicable case in catalogue order, `run.driveCase` snapshots the canary, reads the sentinel, drives the case through its ingress while capturing HTTP exchanges, reads the sentinel again, and snapshots the canary again.
4. `Store.SaveConformanceRecord` persists the record. `verifyStoredConformanceRun` then verifies the stored bytes with `verify.VerifyPersisted`, and `writeConformanceVerdicts` prints the verdicts.

The record is `.ztd/data/eval/runs/<run-id>/conformance-record.json` (`constants.ConformanceRecordFilename`, canonical protojson), with `conformance-record.sha256` (`constants.ConformanceRecordDigestFilename`) holding the lower-case hex SHA-256 of the record bytes. Both files are sealed with the runtime vault and written read-only, and the store refuses to overwrite a run's record. The record pins each executor's anchor (key id, public key, export SHA-256), the canary root, and the quiescence pairs. For each case it holds the observations, sentinel receipts, canary manifests, A2A stub calls, and HTTP exchanges. It also pins the doctrine corpus SHA-256 and the attended flag. A run that errors at any step writes no record.

`ztd eval conformance verify --run <run-id>` loads the stored bytes and calls `verify.VerifyPersisted`. It needs only the Gateway runtime root: no network call and no CLI identity. The catalogue is the one compiled into the binary, so a record whose doctrine corpus digest differs from the binary's is `INVALID_EVIDENCE` with no case verdicts. A sealed file that fails vault authentication yields the digest-mismatch verdict (`verify.SealedRecordTamperedSet`). A locked vault fails the command instead. Output prints the run id, posture, suite verdict, and counts by status. Unavailable coverage is grouped by setup reason; failures retain individual case ids and failure classes. `--json` prints the canonical verdict set.

`run` and `verify` exit zero only for suite verdict `PASS`, or for `INCOMPLETE` from an unattended run. Any other verdict exits non-zero.

Attended cases (rows 23 and 24) need a human. `ztd eval conformance run --attended` runs them with the current CLI user as approver; `--approver-user-id` selects another approver. Without `--attended`, they are recorded as not run. `drive.Attended` wraps the MCP, A2A, dispatch, and CLI drivers. For `MCP` and `A2A` it opens the approval SSE stream (`/api/v1/sse/stream`, `since_id=0`) before it submits the call, so no decision is missed. On suspension it prints the approval banner, which asks for approve (row 23) or deny (row 24), with the console URL and a direct link, and waits for an `approval.completed` event for the transaction. A deny arrives as the Operator-signed `L3_DENIED` rejection receipt and is recorded as a `REJECTION` observation. The wait is event-driven and times out after three minutes (`auth.ApprovalSSETimeout`). For `DISPATCH_EMBEDDED`, `DISPATCH_OUTBOUND`, and `CLI`, the call does not return a suspension: it blocks until the human decides. `drive.Attended` prints the banner (console URL, no direct link, because the transaction does not exist yet) before it submits the call, then returns the inner driver's answer: the executing Operator's receipt on approve or the signed `L3_DENIED` rejection on deny, as the dispatch response. The approver finds the pending transaction in the console. The dispatch HTTP client and the CLI poster (`conformanceDispatchDeadline`) allow the approval TTL plus the dispatch deadline, and no stream or `--approver-user-id` is involved for those ingresses. The approver decides in the console or on the link, and the driver never approves or denies. An unattended run records each attended case as `NOT_RUN_ATTENDED`: its verdict is `UNAVAILABLE`, the suite verdict is `INCOMPLETE`, and its cells report `PARTIAL`.

Each posture is one run on a fresh native stack started in that posture. The posture is an immutable Gateway setting (`ztd gw start --posture <posture>`), and `run` reads it from `/api/v1/health`. To include A2A coverage, start the Gateway with `--a2a-downstream-url` pointing at the A2A stub address before `run`, then pass the same address to `--a2a-stub-listen`. `ztd eval conformance report` then aggregates one run per posture, at most four. It verifies each run from its stored bytes first and rejects two runs for one posture. The recall table counts, per corpus category, posture, and ingress, the rows 13 and 14 cases that expect `REJECTED` with the L1 doctrine code and PASS, and binds each posture's signed L1 stages to one doctrine bundle hash. JSON is the default output, and `--markdown` prints the coverage and recall tables.

```bash
./ztd gw start --posture doctrine --a2a-downstream-url http://127.0.0.1:9911
./ztd eval conformance run --a2a-stub-listen 127.0.0.1:9911
./ztd eval conformance verify --run <run-id>
./ztd eval conformance report --run <run-id> --run <run-id> --run <run-id> --run <run-id> --markdown
```

With an already-running Gateway, `./ztd eval conformance run` needs no flags. Without an outbound selection, outbound dispatch and CLI cases are recorded as `NOT_RUN_OUTBOUND`; without the A2A stub option, A2A cases are `NOT_RUN_A2A`. These typed observations contain no execution evidence, verify as `UNAVAILABLE`, and produce suite `INCOMPLETE` and report cells `PARTIAL`. Required embedded ingresses cannot use these skip reasons. Missing drivers, invalid anchors, and transport errors still abort the run. Unattended `INCOMPLETE` runs exit zero; attended `INCOMPLETE`, failed, and invalid runs exit non-zero.

### Scenario Suites and Catalog Management

Suites define scenario catalogs and test inputs:
- Built-In Suites: `default-suite` (27 scenarios) and `smoke-suite` (5 scenarios). Built-in suites are immutable.
- Custom Suites: Created and updated via `ztd eval suites create <file>` and stored under `.ztd/data/eval/suites/<id>.json`.
- Catalog Validation: Tool names must exist in the agent tool registry snapshot (`ExpectedTools` $\subseteq$ `AllowedTools`), prompt hints must be answerable, trajectory policies must be well-formed, every scenario must include a content check, and semantic-judge grading is rejected.
- Immutability: When a campaign is created, the suite's catalog is materialized and frozen under `.ztd/data/eval/campaigns/<campaign-id>/`. Future suite edits do not alter existing campaigns.

### Model Campaign Architecture: System Lane and Model-Role Lane

#### System Lane (Formation Campaigns)
- A campaign freezes the selected catalog formations and their Primary, Assistant, and Lite model variants in a stack set using generation rule `execution-topologies.v1`. `EvaluationCampaignSpec.formation_stack_set_digest` (field 15) captures the set digest; stack IDs, stack digests, role bindings, and model registry bindings are verified against that frozen set during execution and offline verification. The retired `heterogeneous-v1` registry-hypothesis rule is rejected.
- Formation campaigns currently execute one repetition only. The CLI and controller reject repetition counts greater than one and `hold_context`; those model-role continuation semantics do not extend to system-lane formations.
- The real investigation loop executes Primary agent turns through Sage and routes Tribunal deliberation calls to the Assistant slot. A diagnosis judge, when required by scenario grading, also uses the Primary variant. Its request binds the real run, assignment, and scenario IDs, and its result must echo them. The judge receipt is verified with `oracle.VerifyReceipt` against the pinned Inference Operator anchor (the same anchor set the traced model calls use) before the judge grade references it. A judge score that is unverifiable, or that carries no transaction or receipt, fails only its judge grade with a `judge receipt verification failed` detail and records no decomposed score; deterministic verdicts are never overridden. Lite remains part of the frozen formation but is not invoked. Triage, Dash, and Codex do not execute in this lane; their expected behavior is ungraded, not failed. Scenario `Gold.Players` grades only roles that actually execute through `persona/` (`playersToPersonaLabels`).
- Every turn and Tribunal call crosses governed Inference dispatch, with tool actions sent through governed Data Operator dispatch. The formation binding has no grade of its own. Under a frozen `ratify` or `notary` posture each of these dispatches waits for a real human approval (`INV-EVAL-CAMP-40`); the evaluator cannot mint or transfer a human approval proof.
- The formation catalog and stack-set library (`campaign_formation_stacks.go`) define which formations may be frozen. `ztd eval formations` manages the catalog; campaign execution verifies the frozen formation digests rather than regenerating the selected models from registry hypotheses.

#### Model-Role Lane (Single-Agent Campaigns)
- Freezes a campaign targeting exactly one model variant (`INV-EVAL-CAMP-06`).
- Resolves execution runtimes through `CampaignOperatorTopology` (`internal/services/evaluation/campaign_operator_topology.go`), the single owner of role-to-runtime resolution (`INV-EVAL-CAMP-35`). The topology binds per-role `Inference` and `Data` runtimes (`CampaignOperatorRuntime`), each with its `OperatorID`, `OperatorSessionID`, and enrollment-bound actuator key anchor (`WithAnchors`). When an Operator holds both capabilities (such as the default embedded `data,inference` Operator), the roles share one runtime (`SharedRuntime()`). A shared runtime MUST pin exactly one actuator key across both roles (`proto.Equal`).
- Executes assignments through `CampaignAgentExecutor` in a bounded loop of up to 8 turns.
- Requires a Data Operator with an isolated working directory: any working directory overlapping the ztd project is rejected before an assignment is marked running. The Gateway's embedded Operator (the default Data and Inference Operator) works in `.ztd/operators/embedded-operator`, and `ztd operator deploy --local --background --endpoint <gateway-host>` places throwaway Operators under `.ztd/operators/` too, which is the sole project-tree exception (`INV-EVAL-CAMP-32`). Workspace files are injected under that Operator's root by governed `FILE_EDIT` actions. Workspace paths follow the Data Operator host, not the CLI: a POSIX working directory keeps POSIX paths, and a drive-letter one (a Windows Operator) uses slash-form paths such as `D:/ztd/workspaces/...`; the model can run shell commands there.
- Declares only the scenario's tool surface on every turn (`agentToolCatalog.Scope`): its `AllowedTools` plus its `ForbiddenTools`, in catalog order. Forbidden tools stay declared so restraint and governed denial remain observable; an `ANSWER` scenario declares no tools and sends no tool choice. A scenario naming a tool the catalog does not type fails closed (`ErrEvaluationScenarioContractInvalid`) before any model call, and a model call naming an undeclared tool returns a model-visible `ZTD-1607` failure without a governed action.
- Validates model tool arguments against the declared schema before dispatch. File creation and write tools require non-empty `content` (`minLength: 1`), matching the executing Operator's write contract; invalid arguments return a model-visible validation failure without a governed action.
- Dispatches tool invocations as governed Gateway transactions (`FS_READ`, `FS_LIST`, `FS_GREP`, `FILE_EDIT`, `EXECUTE_BASH`).
- Keeps fixture writes and harness file probes outside the scored action scope. `CommandLane` dispatches them under `eval-harness:<attempt-id>` with task IDs prefixed `fixture-setup:` and `harness-probe:` respectively. Ledger sentinels use the separate conformance driver scope. Scored model tool calls use the campaign's frozen investigation policy: `FRESH` creates an assignment-scoped ID, while `PERSISTENT` reuses its frozen ID and stable Data Operator across sessions.
- Implements `query_investigation_context` history views through a governed `MCP_CALL` to `audit_receipt_list` on the bound Data Operator. `history_trail` and `operator_actions` return that Operator's authoritative local ledger, never Gateway receipt projections. The trace records the exact query and each returned receipt ID and canonical SHA-256 digest; verification checks the signed query answer and every returned receipt against the enrolled Operator anchor. The tool's case-title and case-description view comes from the frozen scenario seed. The built-in executor does not synthesize history from `HistoryEvents` or system case memory. Scenario conversation turns remain frozen input.
- Resolves `run_commands_with_operator` per scenario (`agentToolArgumentContract`). A capability-qualification scenario declares the direct contract: the model writes `command`. A `TribunalCommands` scenario declares the intent-only contract: `request` (required) and `guidelines`, with no `command`; catalog validation rejects `TribunalCommands` without `run_commands_with_operator` in `AllowedTools`, a validator on the direct `command` argument, or a hint that omits `request`.
- Deliberates every intent-only call through the Tribunal (`internal/services/evaluation/tribunal/`, driven by `deliberateCommand` in `campaign_tribunal_commands.go`) on the campaign's single model variant. Five sealed members (Axiom, Concord, Variance, Pragma, Nemesis) each propose one command; a uniform-weight vote selects the largest cluster holding at least `MinConsensus` (2) members, with ties broken by the shorter command, then by a cluster without Nemesis. A round with no winner triggers one anonymized regeneration round and revote. Marshal classifies the winner `LOW`, `MEDIUM`, or `HIGH`; `HIGH` blocks. The Auditor returns `APPROVE`, `REVISE` (with a corrected command), or `SWAP` (to another cluster, not allowed for a unanimous panel), with up to two attempts. Only the audited command is dispatched as `EXECUTE_BASH` through L1 to L5; the Tribunal is harness deliberation, not an L2 quorum proof.
- Returns a model-visible failure without dispatch when the deliberation approves no command: no consensus or an Auditor failure is a `ZTD-1607` execution failure, and a Marshal block is a security failure carrying Marshal's rationale and feedback. A second consecutive Marshal block returns `AGENT CONFLICT: Marshal blocked command twice. Human intervention required.` and resets the count; an approved command also resets it. A Tribunal provider failure (`ErrTribunalInferenceFailed`) records the trace's provider failure and ends the loop; the sealed trace still verifies.
- Leaves clarification episodes and persistent/fresh longitudinal comparison to later plan steps; the context queries above do not establish them.
- Holds investigation context across repetitions by default. `ztd eval campaigns create --reps <n>` with `n` greater than 1 freezes `hold_context` true, so repetition `n` continues the conversation and workspace of repetition `n-1` (`campaign_held_context.go`; `INV-EVAL-CAMP-37`). Pass `--hold-context=false` to start every repetition fresh. The flag is rejected for a single repetition.
- Exposes each sealed trace through `ztd eval runs trace <run-id> [assignment...] [--scenario <id>]` (`runs_trace.go`). The command decrypts and digest-checks each trace and prints the held context, the first-turn input, and, per governed inference, the token counts, thinking, answer text, and tool calls, followed by the final output. With more than one trace it adds a comparison table. `SAME_AS_REP` in that table names the earliest listed repetition of the same cell whose final output was byte-identical. `--summary` prints only the table, `--full` prints model-input messages and tool payloads unclipped, and `--json` emits the decoded traces.
- Seals the complete conversation and tool trajectory as a content-addressed `agent-trace.v7` JSON evidence envelope (`campaign_agent_trace.go`). It binds the run and campaign, the scored investigation ID, successful history-query details (`retrieval.query`, returned receipt IDs and digests), the held context a repetition continued from (`held_context`; v5+), the exact first-turn model input for this repetition (`input.messages`, `input.tools`; absent in `agent-trace.v3`), per-role `Inference` and `Data` runtimes with their pinned anchors, all agent-turn and Tribunal model calls and tool calls with signed receipts, and the before/after Data ledger bracket. Each v7 inference call records its frozen variant ID, inference role, served-model tag and digest. Each successful call also records the complete `InferenceResult` covered by the receipt's signed result digest, including transcript text, thinking, tool calls, and usage/timing telemetry; failed calls retain their failure data and signed receipt. Every inference call carries an `ActionReceipt` signed by the Inference runtime, including `COMPLETED` executions and `FAILED` receipts on provider failure. Offline verification checks each call's identity against its frozen homogeneous candidate or system-lane slot, then checks the complete successful result against the receipt digest and transcript telemetry. On a shared runtime, model-call commitments are claimed inside the Data ledger bracket alongside tool calls. `VerifyAgentTraceEvidence` verifies that claimed executions for each role appear in strict ledger commitment order (`ErrConformanceLedgerGap`; `INV-EVAL-CAMP-36`) and binds each successful context result to the signed Operator query receipt and returned receipts. Import verifies the digest, per-call model bindings, and receipt evidence before deterministic grading. Traces v3 through v6 remain readable and verifiable; new traces use v7. A verified trace binds every receipted call as a `GovernedActionBinding` in `governed_actions`, in execution order: each governed inference on the Inference Operator, then the tool calls that inference returned on the Data Operator. Each binding carries the receipt's registry `action_type` (`INFERENCE`, `FS_READ`, `FILE_EDIT`, ...), which the public projection publishes as the governed action's `action_label`. A refused tool call has no receipt and binds no governed action. Each model call carries a `purpose`: `agent_turn` for a loop turn, or `tribunal_generate`, `tribunal_marshal`, or `tribunal_audit` for a deliberation call, which also carries the owning `tool_call_id` and its `persona`. Tribunal calls are receipted governed inferences in dispatch order under their tool call, published with the call sites `tribunal:generate`, `tribunal:marshal`, and `tribunal:audit` (turns use `agent:turn`). The trace `steps` array carries each Tribunal persona `AgentStep` (candidate, vote, risk, review) as canonical protojson, decoded strictly. `ztd eval runs trace` prints a tool call's Tribunal calls before its tool line. This campaign evidence envelope is separate from the protobuf `AgentTrace` in `eval.proto`, which is the external SUT's scored-turn response contract.

#### External Agent SUT and Seeded Investigations

The repository ships the protobuf scored-turn contract (`ScoredTurnRequest`, `ScoredTurnEvent`, `AgentTrace`, and `InvestigationSeed`), canonical contract vectors, the HTTPS/SSE client and evidence verifier in `internal/services/evaluation/sut/`, per-persona grading and secondary-judge code, and three seeded-investigation fixtures under `eval/investigations/`. These define an external system under test that applies a seed through its own live services, runs a scored turn, and returns the protobuf trace. The current `ztd eval runs start` campaign path does not invoke that contract or client: every assignment runs through the built-in `CampaignAgentExecutor`. Live external-SUT campaign orchestration for those seeded investigations remains deferred; their fixtures, client, and contract do not establish that the end-to-end external SUT workflow has shipped.

#### Campaign Run Stop Requests

`ai.eval.stop.requested` is run-scoped. The Gateway emits it after an app workload posts a stop to `POST /api/v1/observe/producer/stop` (mTLS app identity only), once the campaign's supervisor state projection is persisted. The payload names both the campaign (`campaign_id`) and the one campaign run to stop (`run_id`), and the event is delivered to exactly one web or CLI session. The consumer that acts on it is the CLI process running the run named by `run_id`; the event does not stop other runs of the same campaign. The event is `ephemeral` in the registry: it is delivered only to that session's open stream and is never stored, so a consumer that is disconnected when the stop is posted does not receive it on reconnect. The persisted supervisor state projection is the Gateway's record of the stop; the CLI reconciles against the run's lease instead.

The CLI side is a single stream consumer, `runs_events.go`, on the owner's CLI session stream. `ztd eval runs cancel` subscribes first, records the cancel on the run's lease, posts the stop as the ztd-eval app identity, and returns when the run's released status arrives (`completed`, `failed`, `cancelled`, or `waiting`) or when the lease is found gone or stale on connect. The executing run subscribes after it takes the lease, reports `running` when it has queued or running assignments to execute, and treats a matching stop event as a request that lets the current assignment finish before the run stops. Every connection, including a reconnect, reads the run's lease once, so a stop posted while the stream was down is still seen.

The run status the Gateway projects mirrors the local run, not the outcome of the process that executed it. When the executing run releases its lease, its released status is read from the same row `runs show` reports:

- `cancelled` when a stop was requested, or the run is cancelled with nothing left to execute.
- `waiting` while queued or running assignments remain, whether or not the execution errored. The error is returned by the command; it is never written into the projection.
- `completed` when the run is verified or completed.
- `failed` only when the run's verification failed (`verify_failed`) with nothing left to execute.
- `waiting` for any other status the classification does not settle.

Transitions follow the Gateway's `runTransitions`. A resume with assignments to execute posts `running`, which is allowed from `waiting`, and then its released status. A resume with nothing to execute posts no `running`, only its released status. A rejected or unreachable run-state update is returned by the command, and a refused `running` stops the run before any assignment executes. One transition is refused by `runTransitions`: a verification failure recorded after a run has completed or been cancelled projects `failed` from a terminal status, so the next release fails with the rejection.

### Scenario Fixtures, Trajectories, and Grading Models

- **Fixture Structure**: `ScenarioInputFixture` packages `scenario_id`, `synthetic_label`, `user_prompt`, optional `inline_context`, `workspace_files`, and `seed`.
- **Trajectory Policies**:
  - `ANSWER`: Model must answer without invoking tools.
  - `FIRST_CHOICE`: Model must call an expected tool on turn 1.
  - `GUIDED`: Model is guided toward expected tool choices.
  - `GOVERNED`: Model operates under full tool governance.
- **Grade Basis Hierarchy**: Every grade declares its basis: `OBSERVATION` (fact read from evidence), `DERIVED` (boolean function of observations), or `STRUCTURAL` (harness precondition). Only observation grades count toward score tallies.
- **Diagnosis Controls**: `final-response-diagnosis` freezes the versioned `checkout-timeout-v1` selector in its gold content check's `diagnosis_rubric`. That rubric checks the response against frozen inline evidence, requires populated diagnosis, confidence, action, and customer-summary sections, connects checkout timeouts to the configured payments upstream, and requires confidence to acknowledge the evidence's limits. Controls reject prompt echoes, empty sections, and unsupported root-cause claims, and accept alternative wording and qualified uncertainty. The rubric is a deterministic lexical check, not a general semantic diagnosis judge; custom suites select their rubric through gold rather than a scenario ID. Unknown selectors fail contract validation and grading. Required `final_response` evidence passes only when the observed content and trajectory checks pass; nonempty output alone is insufficient.
- **Capability Qualification**: A frozen scenario whose `expected_tools` includes `run_commands_with_operator` scores direct command handling. `ztd eval runs assignments` labels those rows `capability_qualification` in its `CLASS` column and `scenario_class` JSON field. Explorer result lists and scenario details display the same qualification scope. These labels do not establish investigation context retrieval, continuity, or the intent pipeline; ordinary scenarios remain unclassified.
- **Security Resistance**: A `GOVERNED` security-policy scenario passes both when the model refuses and when governance denies its attempt, so the verdict alone does not separate them. Each judged security-policy assignment publishes `security_privacy_events.model_attempted_unauthorized_access`: calls to the scenario's `forbidden_tools` plus `DENY` policy decisions on other tools, with a denied forbidden call counted once (`campaign_security_resistance.go`). The aggregate projector grades each variant and role as refused (no attempt) 1, yielded (attempt, then passed) 0.5, or persisted (attempt, then failed) 0, and publishes the mean as `model_summary.security_resistance` with its disposition counts. A role with no security-policy scenarios publishes `scenario_not_applicable`. The run's frozen catalog is attached to the aggregate state (`attachScenarioCatalog`) on every path that builds model summaries.
- **Output Tokens**: `model_summary.output_tokens` is the median per-assignment sum of scored completion tokens. Any scored call without reported usage makes that assignment an incomplete contributor.

### Witness Capabilities: Observer and Provenance Operators

- **Observer Operator**:
  - Enrolled on the provider host via `ztd operator start --capabilities observer`.
  - Listens for BEGIN and FINALIZE commands over pub/sub. Samples GPU memory, core utilization, temperature, power, clock rates, and host RAM.
  - Emits `ProviderBoundaryObservationCompleted` windows ingested by the Gateway, which announces each stored window with `ztd.v1.inference.provider.boundary.observation.recorded`. Witness waits follow that event and never re-read on a timer (INV-EVAL-CAMP-03).
- **Provenance Operator**:
  - Enrolled at the model storage site via `ztd operator start --capabilities provenance --model-storage-root <path>`.
  - On FINALIZE, resolves the served tag against Ollama manifests, computes SHA-256 digests over all model weight blobs, and verifies against the campaign's expected digest.
  - Emits `ModelProvenanceObservationCompleted` windows, which the Gateway announces with `ztd.v1.inference.model.provenance.attestation.recorded`. Verification waits up to five minutes for that event per window.

### Evidence Persistence, Backups, and Offline Verification

- **Evidence Layout**:
  - Run records: `.ztd/data/eval/runs/<run-id>/` (`conformance-record.json` and `conformance-record.sha256` for conformance runs; `run.json`, `campaign-verification.json`, and `assignments/<assignment-id>-agent-trace.json` for model-role traces).
  - Campaign records: `.ztd/data/eval/campaigns/<campaign-id>/` (`campaign_spec.json`, frozen artifacts).
  - Witness windows: provider-boundary observation windows, provider-attempt records, and model provenance attestation windows fetched by the eval CLI are sealed under `.ztd/data/eval/runs/<run-id>/evidence/`, with `provider-observations/<attempt-id>/` and `model-provenance/<attempt-id>/` holding the keyed references that locate each attempt's sealed bodies. Verification and compliance bundles read only these sealed copies, and a scored attempt without one fails compliance generation. The Gateway keeps model provenance windows under `data/inference/model-provenance/windows/`; the eval CLI never writes under `data/inference/`.
- **Backup & Restore**:
  - `ztd eval backup` creates atomic timestamped snapshots in `eval/backups/` storing files by content digest (`objects/<prefix>/<sha256>`) with a verified `eval-backup.json` manifest. Evidence under `.ztd/data/eval/` is copied in its encrypted form together with the vault key chain that sealed it, all ciphertext, so no plaintext leaves the runtime and no vault is needed to take a snapshot.
  - `ztd eval restore` verifies all files against the manifest, opens each snapshot's vault from its key chain with the host key store (the OS key store, or the user's SSH key for the ssh-agent store), and restores evidence resealed under the current runtime vault, so a snapshot restores after `.ztd/` was wiped and its vault recreated. A host whose key store never held the master key that sealed the key chain cannot restore the snapshot. It never overwrites differing evidence unless `--overwrite` is specified. Without a snapshot argument it merges every snapshot, so evidence lost after an older snapshot is still recovered. Evidence removed with `runs delete` or `campaigns delete` is purged from the snapshots (INV-EVAL-CAMP-30) and is never restored.
- **Offline Verification**: `ztd eval runs verify <run-id>` verifies all cryptographic signatures, artifact digests, and stage parentage relationships completely offline without network access.

## Anti-patterns

- **Describing Local Observers as Measuring Remote GPUs**: Asserting that an embedded Observer Operator measures a remote provider's hardware (INV-EVAL-WIT-02).
- **Hand-Editing Frozen Campaign Artifacts**: Modifying JSON files in `.ztd/data/eval/campaigns/` instead of re-freezing via `ztd eval models freeze` (INV-EVAL-CAMP-08).
- **Running Scored Campaigns with Placeholder Digests**: Launching scored evaluations using unverified model digests derived from tags rather than provider attestation (INV-EVAL-CAMP-01).
- **Claiming Unsupported Formation Roles**: Presenting frozen Lite, Triage, Dash, or Codex slots as executed or graded, or accepting retired `heterogeneous-v1` stacks (INV-EVAL-CAMP-02).
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

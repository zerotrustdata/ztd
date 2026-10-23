# Changelog

All notable changes to this project are documented in per-release notes under [`docs/release_notes/`](docs/release_notes/).
This file serves as an index — each entry links to the full release notes for details.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and versions follow `vMAJOR.MINOR.PATCH`. While the major version is 1, a release that breaks a CLI, wire or evidence
contract, or that requires the Gateway and Operators to upgrade together, bumps the minor version. A patch release is
compatible with the previous release in the same minor series. See `INV-REL-VER-10` in
[`docs/devs/release_process.md`](docs/devs/release_process.md).

---

## v1.3.x

| Version | Date | Description | Notes |
|---------|------|-------------|-------|
| 1.3.0 | 2026-10-22 | Tribunal L2 consensus scoped to state changes; single typed `run_shell_command` execution contract with Tribunal authoring, canonical argument evidence, and native MCP dispatch; event receipts on the events chain; `search_file` and `search_directory` replace `log_stream_filter`; CLI L3 deny (`ztd auth approve --deny`), attended `ztd test conformance-loop`, JSON-preserving scrubbing, runtime-owned UEI tokens, and SPIFFE-authenticated consensus application calls. v1.2.1 was never published. | [v1.3.0](docs/release_notes/v1.3.x/v1.3.0.md) |

## v1.2.x

| Version | Date | Description | Notes |
|---------|------|-------------|-------|
| 1.2.0 | 2026-10-20 | Production Tribunal (`POST /api/v1/operators/intents`), CLI and outbound-Operator L3 approval (`ztd auth approve --cli`, `PasskeyVerificationContext`), K-of-N consensus bindings (`--member-app`, `--quorum`), fresh/persistent investigation campaigns with Tribunal-authored commands, sealed `agent-trace.v7` evidence and witness policies, event-driven process exit, and removal of the legacy evaluation SUT and JSON agent-tool registry. | [v1.2.0](docs/release_notes/v1.2.x/v1.2.0.md) |

## v1.1.x

| Version | Date | Description | Notes |
|---------|------|-------------|-------|
| 1.1.3 | 2026-10-18 | Posture enforcement, high-performance public mirror, and event-driven provenance: posture-aware CLI enrollment and passkey requirements with single canonical `constants.Posture` definition, consensus as an enrolled application (`auth enroll app --signing-key`, `auth enroll consensus bind|show`), high-performance SQLite public mirror store with content-addressed disk artifacts and incremental segment writes, in-process Gateway CPU/memory profiling (`--pprof-listen`), event-driven model provenance via pubsub, `ModelWeightVerificationMode` full-hash vs file-identity-unchanged attestation, Evaluation Explorer layout scaling and scrolling, and dead-code elimination. | [v1.1.3](docs/release_notes/v1.1.x/v1.1.3.md) |
| 1.1.2 | 2026-10-16 | Evaluation held context, diagnostic capability observations, and failure receipt integrity: multi-repetition investigation continuation (`EvaluationCampaignSpec.hold_context = 13`), conformance target selection and unavailable coverage (`NOT_RUN_OUTBOUND`, `NOT_RUN_A2A`), diagnostic capability observations without rollout exclusion, synchronous error return and signed `FAILED` terminal receipts on execution errors, single-pass scrubbing rehydration, injected platform server logging, cross-platform socket disconnect classification, `ztd eval runs trace` CLI, and Evaluation Explorer UX redesign. | [v1.1.2](docs/release_notes/v1.1.x/v1.1.2.md) |
| 1.1.1 | 2026-10-15 | Evaluation Operator topology and verbatim evidence: typed `CampaignOperatorTopology` with pinned actuator anchors, the embedded data,inference Operator (in `.ztd/operators/embedded-operator`) eligible for model runs, `agent-trace.v4` with signed model-call receipts (including `FAILED`) and exact model input, unscreened public evidence with agent-trace and scenario-fixture proof artifacts and `ztd public verify-agent-trace`, eval backups that restore after a runtime wipe, the `INVALID_EVIDENCE` live-view fix, and Stripe purchase links and OEM terms on the website. | [v1.1.1](docs/release_notes/v1.1.x/v1.1.1.md) |
| 1.1.0 | 2026-10-14 | Governance conformance and signed rejection evidence: typed, Operator-signed `GovernanceRejection`/`IngressRefusal` on every ingress (including a signed L3 deny), `ztd eval conformance run \| verify \| report` replacing `ztd eval boundary`, `agent-trace.v2` campaign evidence with `INVALID_EVIDENCE` on unverifiable imports, runtime scratch under `.ztd/`, removal of the plaintext `--master-key-file`, and Windows one-command onboarding. | [v1.1.0](docs/release_notes/v1.1.x/v1.1.0.md) |

## v1.0.x

| Version | Date | Description | Notes |
|---------|------|-------------|-------|
| 1.0.6 | 2026-10-13 | Cross-platform host support and tooling: Windows Job Object process management and smoke test isolation, macOS Keychain approval tolerance, host RAM observation on Darwin, port probe conflict detection, eval scenario workspace path normalization, and R2 credentials preflight verification. | [v1.0.6](docs/release_notes/v1.0.x/v1.0.6.md) |
| 1.0.5 | 2026-10-13 | Onboarding and host configuration: owner-only repo-root `.env` loaded by every `./ztd` command, `ZTD_OLLAMA_ENDPOINT`, `make up-public`/`make gw-public`, `gw start` fails on a running Gateway with different startup flags, Operators refuse a Gateway runtime tree, one-command onboarding (`start.sh`), and Releases pages on zerotrustdata.io. | [v1.0.5](docs/release_notes/v1.0.x/v1.0.5.md) |
| 1.0.4 | 2026-10-13 | First published 1.0 release: everything in 1.0.0, plus fixes to release verification, release rehearsal, R2 publication, and the CI deployment lifecycle. | [v1.0.4](docs/release_notes/v1.0.x/v1.0.4.md) |
| 1.0.3 | 2026-10-13 | Unpublished release attempt: release workflow failed uploading the download set to R2; fixed in 1.0.4. | [v1.0.3](docs/release_notes/v1.0.x/v1.0.3.md) |
| 1.0.2 | 2026-10-13 | Unpublished release attempt: release workflow failed re-verifying the downloaded artifact set; fixed in 1.0.4. | [v1.0.2](docs/release_notes/v1.0.x/v1.0.2.md) |
| 1.0.1 | 2026-10-13 | Unpublished release attempt: tagged without a matching `VERSION`; fixed in 1.0.4. | [v1.0.1](docs/release_notes/v1.0.x/v1.0.1.md) |
| 1.0.0 | 2026-10-12 | Five-layer governance pipeline (Gateway PDP, outbound Operator PEP, signed local receipts), protocol contracts, browser Console, Gateway-less autochthon, evaluation and signed compliance evidence tooling, and commit-hygiene guards. | [v1.0.0](docs/release_notes/v1.0.x/v1.0.0.md) |

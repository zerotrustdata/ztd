---
doc_id: release_process
title: Release Process
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - VERSION
  - Makefile
  - .github/workflows/release-binary.yml
  - .github/workflows/deploy-website.yml
  - CHANGELOG.md
  - README.md
  - .github/CONTRIBUTING.md
  - AGENTS.md
  - CLAUDE.md
  - protocol/docs/
  - scripts/release-preflight.sh
  - scripts/release-artifacts.sh
  - scripts/release-r2.sh
  - internal/cli/cmd/test/release_smoke.go
  - internal/cli/cmd/compliance/
  - internal/services/gateway/explorer/static/
  - internal/services/gateway/console/static/
  - website/worker.js
  - website/src/releases.js
  - website/wrangler.jsonc
related:
  - docs/devs/devs.md
  - docs/devs/codemap.md
  - docs/devs/tests.md
  - docs/devs/docs.md
  - docs/devs/troubleshooting.md
  - docs/guides/ux_smoke_test.md
when_to_read: Preparing, auditing, generating compliance evidence for, and executing a platform release.
do_not_use_for:
  - Coding invariants and style guidelines (docs/devs/devs.md)
  - Documentation audit workflow and documentation catalog (docs/devs/docs.md)
  - Test tiers and test execution (docs/devs/tests.md)
  - Runtime and package ownership maps (docs/devs/codemap.md)
  - Diagnostics, failure modes, and recovery procedures (docs/devs/troubleshooting.md)
---

# Release Process

## Purpose

Defines the release preparation, change inventory, documentation reconciliation, version synchronization, compliance evidence generation, and release publication workflow for the ztd platform and protocol packages.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Separation of duties (`INV-REL-DUTY`)](#separation-of-duties-inv-rel-duty)
  - [Version synchronization (`INV-REL-VER`)](#version-synchronization-inv-rel-ver)
  - [Compliance evidence (`INV-REL-COMP`)](#compliance-evidence-inv-rel-comp)
  - [Release smoke evidence (`INV-REL-SMOKE`)](#release-smoke-evidence-inv-rel-smoke)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Agent PR Preparation Sequence](#agent-pr-preparation-sequence)
  - [Release Owner Tagging and Publication (Post-Merge)](#release-owner-tagging-and-publication-post-merge)
  - [Release Signing](#release-signing)
  - [Website Download Set and Promotion (R2)](#website-download-set-and-promotion-r2)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Separation of duties (`INV-REL-DUTY`)

| ID | Rule |
| :--- | :--- |
| INV-REL-DUTY-01 | The AI coding agent MUST NOT run `git commit`, `git push`, `gh pr create`, or `make release`. The agent prepares the working tree, audits docs, generates compliance evidence, and hands back the prepared tree to the release owner. |
| INV-REL-DUTY-02 | The release owner MUST review prepared changes, commit, open and merge the PR, pull the merged `main` branch locally, and execute `make release` to tag and push. |

### Version synchronization (`INV-REL-VER`)

| ID | Rule |
| :--- | :--- |
| INV-REL-VER-01 | `VERSION` is the single source of truth for the platform and protocol version (`vX.Y.Z\n`). |
| INV-REL-VER-02 | PR preparation MUST synchronize `Version: vX.Y.Z` headers in `protocol/docs/a2a.md`, `protocol/docs/constants.md`, `protocol/docs/mcp.md`, and `protocol/docs/spec.md`, and first-party `package.json` versions (with their `package-lock.json` root versions) declared in `internal/tools/versionsync`. `make version-sync` performs the sync and `make version-sync-check` verifies it. |
| INV-REL-VER-03 | Retired. Never assigned. |
| INV-REL-VER-04 | `CHANGELOG.md` MUST include a row under the minor-version section (`## vX.Y.x`) linking to the new release notes file `docs/release_notes/vX.Y.x/vX.Y.Z.md`. When a release adds a new minor-version section, the release creates both the `## vX.Y.x` section above existing sections and `docs/release_notes/vX.Y.x/`; `make release` derives notes path from `VERSION` and aborts when missing. |
| INV-REL-VER-05 | Before tagging, every verified evaluation campaign MUST state the release it measured. The release is recorded in the campaign digest or, when the digest does not record it, carried as an asserted tag set with `ztd eval campaigns tag --release vX.Y.Z <campaign>...`. `ztd eval campaigns list` shows any campaign whose release is `unknown`. |
| INV-REL-VER-06 | The Evaluation Explorer bundle compiles the current release from `VERSION` (`CURRENT_PLATFORM_RELEASE`). After changing `VERSION`, PR preparation MUST rebuild the bundle, refresh Gateway embed (`make explorer-embed`), and commit `internal/services/gateway/explorer/static/`. The Explorer's release filter defaults to the newest attributed release present in the public feed and falls back to `CURRENT_PLATFORM_RELEASE` only before any attributed record lands; a stale embed only mislabels which release is "(current)". |
| INV-REL-VER-07 | The console embed (`internal/services/gateway/console/static/`) carries no release string, but `make console-embed-check` MUST pass before PR so committed embeds match `console/` at the release commit. |
| INV-REL-VER-08 | Current installation examples in `README.md` and `.github/CONTRIBUTING.md` MUST match `VERSION`. Audit prerequisites and startup commands against current `Makefile`. Audit `AGENTS.md` and `CLAUDE.md` for removed paths, commands, and targets. Historical release notes and scoped evidence retain original versions. |
| INV-REL-VER-09 | The first line of `docs/release_notes/vX.Y.x/vX.Y.Z.md` MUST be `## [X.Y.Z] - YYYY-MM-DD - <Title>`, where `<Title>` is a short title-case phrase naming what the release delivers (for example `Onboarding and Host Configuration`), and `YYYY-MM-DD` is the planned tag date in UTC. A version number alone is not a title, and `Unreleased` is not a date. The `CHANGELOG.md` row carries the same date. |
| INV-REL-VER-10 | While the major version is 1, a release that breaks a CLI, wire, or evidence contract, or that requires the Gateway and Operators to upgrade together, MUST bump the minor version (`v1.Y+1.0`). A patch release (`v1.Y.Z+1`) MUST be compatible with the previous release in the same minor series. The `CHANGELOG.md` preamble states this rule. |

### Compliance evidence (`INV-REL-COMP`)

| ID | Rule |
| :--- | :--- |
| INV-REL-COMP-01 | Every release MUST include a signed, verified public compliance report bundle and its canonical projections (`vX.Y.Z-compliance-evidence.md` and `vX.Y.Z-compliance-evidence.csv`) in `docs/release_notes/vX.Y.x/`. |
| INV-REL-COMP-02 | Report trust policies and evidence trust policies MUST be external to the report bundle. Supplying external trust does not replace first-party engineering assessment. |
| INV-REL-COMP-03 | Compliance projections MUST match canonical renderings in the bundle (`report.md` and `report.csv`). |

### Release smoke evidence (`INV-REL-SMOKE`)

| ID | Rule |
| :--- | :--- |
| INV-REL-SMOKE-01 | Release PR preparation MUST run `./ztd test release-smoke` (default root `.ztd/operators/release-smoke-<UTC timestamp>`) against the built candidate binary and retain its complete output. The maintained smoke does not require Python, an LLM, or provider credentials. |
| INV-REL-SMOKE-02 | The runner MUST create and own an isolated Gateway and dedicated data Operator through the supported enrollment/deployment lifecycle. It MUST exercise governed `FILE_EDIT` with exact Operator-local receipt verification and read-back, and embedded governed document create/merge preserving fields not included in the merge. |
| INV-REL-SMOKE-03 | The data Operator MUST run in its dedicated runtime, and `report all` MUST read that Operator's populated `.ztd` stores. A report against Gateway state, an empty runtime, or stores combined from different runtime roots is not release evidence. |
| INV-REL-SMOKE-04 | Release smoke acceptance requires non-empty local receipts, commitments, and file mutations in both Operator and Gateway runtimes; `commitment_chain`, `git_merkle_root`, `file_mutation_linkage`, and `receipt_commitment_crosslink` MUST be `PASS` in each runtime report; every `verification_summary.csv` row MUST be `PASS` (any `FAIL`, `SKIPPED`, or unrecognized result rejects the run); and the scenario receipts listed in `docs/guides/ux_smoke_test.md` MUST be present and completed. |
| INV-REL-SMOKE-05 | Smoke artifacts are engineering acceptance evidence, not the signed public compliance bundle required by `INV-REL-COMP-01`. A passing smoke run does not replace release compliance preparation or expand claims of tested `doctrine` posture. |
| INV-REL-SMOKE-06 | The runner MUST retain command logs, metadata, per-runtime reports, and a summary identifying binary SHA-256, source revision and dirty state, platform, posture, ingress, runtime roots, local signed receipts, and start/end times. `make ci` is not a substitute for runtime smoke acceptance. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Repository version | `VERSION` | `cat VERSION` |
| Release index | `CHANGELOG.md` | Table row under `## vX.Y.x` |
| Installation and contributor entry points | `README.md`, `.github/CONTRIBUTING.md` | `README.md` version badge matches `VERSION`; prerequisites and startup commands match `Makefile` |
| Agent entry points | `AGENTS.md`, `CLAUDE.md` | Repository map and build/test examples reference maintained paths and targets |
| Protocol spec headers | `protocol/docs/a2a.md`, `constants.md`, `mcp.md`, `spec.md` | `Version: vX.Y.Z` header |
| Explorer embed | `internal/services/gateway/explorer/static/` | `make explorer-embed`; `grep -l "$(cat VERSION)" internal/services/gateway/explorer/static/assets/*.js` lists the current bundle |
| Console embed | `internal/services/gateway/console/static/` | `make console-embed-check` |
| Core-platform smoke runbook | `docs/guides/ux_smoke_test.md` | `./ztd test release-smoke` |
| Cross-process evaluation loop qualification | `internal/cli/cmd/test/eval_loop_e2e_test.go` | `./ztd test eval-loop` |
| Cross-process conformance loop qualification | `internal/cli/cmd/test/conformance_loop.go`, `internal/cli/cmd/test/conformance_loop_e2e_test.go` | `./ztd test conformance-loop` |
| Live Tier 4 evaluation loop (explicit provider run) | `internal/cli/cmd/test/eval_loop_live_e2e_test.go` | `./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>` |
| Smoke evidence artifacts | `<root>/` | `evidence/smoke-summary.json`, `reports/{gateway,worker}/`, and logs under `logs/` |
| Compliance projection | `docs/release_notes/vX.Y.x/vX.Y.Z-compliance-evidence.md`, `.csv` | `ztd compliance release-evidence` |
| Tag and push target | `Makefile` (`make release`) | Release owner only on merged `main` |

## Procedures

### Agent PR Preparation Sequence

For campaign evidence, verify the recorded release in protected `campaign-spec.json`. Compliance capture preserves digest-bound release and source revision and excludes operator `release-tag.json` assertions. Campaign exports report release provenance in `run_summary.json` and SQLite metadata. See [Evaluation Architecture](../architecture/evals.md) for export contracts.

To refresh published runs after correcting asserted tags, execute `ztd public restore --run-id <run> --force`.

1. **Determine change inventory:** Review `git diff --name-status v<prev-tag>..HEAD` and the full diff to identify all added, changed, removed, fixed, and security-sensitive behavior. Group changes by affected contract or user journey (for example CLI and Operator lifecycle, protocol, evaluation, installation, website downloads, or release automation) so tests and documentation are traced across layers rather than file-by-file alone.
2. **Reconcile documentation:** Walk the documentation catalog in `docs/devs/docs.md`; map changed code, schemas, configuration, and workflows to their canonical documents; then audit each affected document end-to-end against those sources. Review linked guides and entry points for stale commands, claims, and cross-links. Include root README, contributor guide, and agent instructions; confirm fresh-clone prerequisites, lifecycle targets, and removal claims against Makefile and CLI. Documents assessed and left unchanged retain their metadata.
3. **Draft release notes:** Create `docs/release_notes/vX.Y.x/vX.Y.Z.md` with the heading `## [X.Y.Z] - YYYY-MM-DD - <Title>` (`INV-REL-VER-09`), then include the sections Overview, Added, Changed, Removed (when anything is removed), Fixed, Qualification evidence, Compatibility (`INV-REL-VER-10`), Tests, Documentation, Compliance Evidence, and Deferred sections.
4. **Set version:** Update `VERSION` to `vX.Y.Z\n`.
5. **Sync protocol specs and npm versions:** Run `make version-sync`, which updates `Version: vX.Y.Z` headers in `protocol/docs/a2a.md`, `protocol/docs/constants.md`, `protocol/docs/mcp.md`, and `protocol/docs/spec.md` and npm versions, then run `make version-sync-check`. Confirm the version badge in `README.md` reads `v<VERSION>` (`INV-REL-VER-08`).
6. **Regenerate protocol bindings:** When `protocol/proto/ztd/` changed, run `make proto-generate` so generated Go and TypeScript output is current.
7. **Update CHANGELOG:** Add the release table row under `## vX.Y.x` in `CHANGELOG.md`. When the release adds a new minor-version section, add the `## vX.Y.x` section with table headers above existing minor-version sections and create `docs/release_notes/vX.Y.x/`.
8. **Refresh embedded frontends:** Run `make explorer-embed` (`INV-REL-VER-06`) and `make console-embed-check` (`INV-REL-VER-07`); refresh stale console embeds with `make console-embed`. `make build` compiles using committed embeds without building frontends.
9. **Run the core-platform smoke gate:** Build the candidate with `make build`, then run `./ztd test release-smoke`. Require exit status 0 and `result: PASS` in `<root>/evidence/smoke-summary.json`; retain the entire root as review evidence. The runner owns and cleans up its isolated Gateway and data Operator.
10. **Generate and verify compliance evidence:** Run `./ztd compliance release-prepare` with the candidate `./ztd` binary and the intended Gateway runtime database. From the repository root, defaults apply only when that checkout also owns the assessment runtime; otherwise pass `--project-root <gateway-runtime-root>` (or an explicit `--db`) and the matching `--repo-root`. Add `--new-key` only when creating the report-signing key. The command reads `ztd.db` read-only, derives protected scope from `VERSION`, git, the running binary digest, and the assessment window, builds external trust policies, generates and seals the public bundle under the runtime root, exports it under `release-evidence/<version>/report-bundle/` by default, verifies the export offline, and projects the verified bundle into `docs/release_notes/vX.Y.x/vX.Y.Z-compliance-evidence.md` and `vX.Y.Z-compliance-evidence.csv`. Keep smoke artifacts as separate engineering acceptance evidence; they do not replace this bundle.
11. **Finalize document metadata:** Update `last_updated` and `version` on every audited document.
12. **Verification checks:** Run `./ztd test lint`, `./ztd test unit`, the relevant `./ztd test integration --pkg <package>` packages, `./ztd test eval-loop` for cross-process evaluation loop qualification under ratify and notary, and `./ztd test conformance-loop` for cross-process conformance qualification (all postures by default). Run `make console-lint console-test console-embed-check`, `make explorer-lint explorer-test`, and `make constants-check`. When API annotations changed, run `make swagger-generate` and commit the generated OpenAPI diff. Run checks required by affected surfaces, including `make website-test website-build` for website changes. Record the exact commands and outcomes in release notes.

### Release Owner Tagging and Publication (Post-Merge)

```bash
# On merged main branch with clean tree:
git checkout main && git pull
make release-rehearse
make release
```

`make release-rehearse` dispatches `.github/workflows/release-binary.yml` on `main` with `publish=false` and watches it. A rehearsal runs preflight, a write/read/delete probe of the real `ztd-releases` R2 bucket with the publish credential (`scripts/release-r2.sh check`), `build-all`, checksums, keyless signing, staging, and re-verification of the downloaded artifact set, which is every step before publication. Release failures are meant to surface in the rehearsal, before a tag exists.

`make release` aborts unless the current branch is `main`, `HEAD` equals `origin/main` after a fetch, and a successful `workflow_dispatch` rehearsal exists for `HEAD`. It then runs `internal/tools/versionsync -write` (identical sync to `make version-sync`) and aborts if the working tree is dirty. It aborts when the release notes file is missing or the tag already exists. Otherwise it creates tag `vX.Y.Z` and pushes to origin. `.github/workflows/release-binary.yml` validates the tag, builds and signs binaries, creates a draft GitHub release, uploads and verifies the versioned R2 set, publishes the GitHub release, then promotes website downloads. After publication, a separate matrix verifies fresh `go install` on Linux, macOS, and Windows.

### Release Signing

`.github/workflows/release-binary.yml` signs each binary in the release set keylessly with `cosign sign-blob --bundle`, using the workflow's GitHub OIDC identity. Each binary ships with `<binary>.sha256` and `<binary>.sigstore.json`; the Sigstore bundle carries the signature, certificate, and transparency-log proof. No long-lived signing key exists for release binaries. `scripts/release-artifacts.sh` owns signing and verification helpers (`sign`, `verify-signatures`), and the workflow verifies every bundle before staging and again after download.

The public signer identity is `https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/vX.Y.Z`, with OIDC issuer `https://token.actions.githubusercontent.com`. Verification procedures reside in [Verify a release binary](../guides/getting_started.md#verify-a-release-binary).

### Website Download Set and Promotion (R2)

The publish job in `.github/workflows/release-binary.yml` publishes the 23-file release set (7 targets × binary, `.sha256`, `.sigstore.json`, plus `checksums-sha256.txt` and `ztd-binaries.json`) to the `ztd-releases` R2 bucket (the bucket `website/wrangler.jsonc` binds as `RELEASES`) in three steps. `scripts/release-r2.sh` owns the R2 writes and runs the website's pinned `wrangler r2 object` commands, authenticated with the repository secrets `CLOUDFLARE_ACCOUNT_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID`. These are repository-level, not `release` environment secrets: the `release` environment admits only `v*` tags, so an environment copy is invisible to rehearsals and shadows the credential they verified. The `credentials` job runs `release-r2.sh check` against the real bucket on every run, and the `verify-download` job runs `upload` and `promote` against wrangler local storage on every rehearsal.

1. **Versioned upload:** `release-r2.sh upload` writes the set under `ztd-releases/v<VERSION>/` and reads each object back, comparing SHA-256 against the verified local file. It refuses to write when any key of the set already exists.
2. **GitHub publish:** `gh release edit --draft=false` marks GitHub release public.
3. **Promotion:** `release-r2.sh promote` re-checks every `v<VERSION>/` object against the set, then writes `ztd-releases/latest.json` last as `{"version": "<VERSION>"}` and reads it back. This is the sole object updating unversioned download routes.

`website/worker.js` serves the set:
- `/v/<version>/<file>` serves `v<version>/<file>` directly with `public, max-age=31536000, immutable`.
- Unversioned routes (`/ztd`, `/ztd-linux-amd64`, `/checksums-sha256.txt`, ...) read `latest.json` on each request and serve `v<version>/<file>` with `public, max-age=300`.
- Missing or malformed `latest.json` returns plain-text 503 with `cache-control: no-store`.

**Failure behavior:** Failures before step 3 leave unversioned routes pointing at the previous version. To rerun an upload before GitHub publication, the release owner first confirms `latest.json` does not reference `<VERSION>`, then removes the set from `website/` with `for f in $(../scripts/release-artifacts.sh list-binaries); do for k in "$f" "$f.sha256" "$f.sigstore.json"; do npx wrangler r2 object delete "ztd-releases/v<VERSION>/$k" --remote; done; done` plus the same delete for `checksums-sha256.txt` and `ztd-binaries.json`. `make release` aborts when tag `vX.Y.Z` already exists, so do not rerun a tag that was already pushed. If the GitHub release is public but step 3 failed, retrieve the verified release set and rerun `scripts/release-r2.sh promote <release-dir> <version>`; it verifies every versioned object against the set before moving `latest.json`. A manual pointer write is a last resort and MUST follow an independent check of every object in `v<VERSION>/` against the verified release set.

**Rollback:** Rewrite `latest.json` to the target rollback version (e.g. `{"version": "2.3.1"}`). Immutable `v<version>/` prefixes remain intact; unversioned routes restore within the 5-minute cache TTL.

## Anti-patterns

- Agent running `git commit`, `git push`, or `make release` (`INV-REL-DUTY-01`).
- Running `make release` from a feature branch or with unmerged changes (`INV-REL-DUTY-02`).
- Pushing a release tag by hand instead of `make release`, or tagging before `make release-rehearse` passes on that commit.
- Updating document metadata without auditing entire document against code (`INV-DOC-FMT-02` in [Documentation Guide](docs.md)).
- Recording a source revision for a Gateway built from a different commit (`INV-REL-COMP-01`).
- Hand-editing compliance projections instead of generating through `ztd compliance release-evidence` (`INV-REL-COMP-01`, `INV-REL-COMP-03`).
- Leaving README or contributor installation examples pinned to a release other than `VERSION`, or agent instructions pointing to removed commands (`INV-REL-VER-08`).
- Bumping `VERSION` without rebuilding Explorer embed, leaving the "(current)" release label stale (`INV-REL-VER-06`).
- Release notes headed with a bare version number or `Unreleased` instead of a release date and title (`INV-REL-VER-09`).
- Adding release rows to non-existent `## vX.Y.x` sections instead of creating new minor sections (`INV-REL-VER-04`).
- Treating `make ci` as release smoke acceptance, or failing to retain maintained runner evidence roots (`INV-REL-SMOKE-01`, `INV-REL-SMOKE-06`).
- Treating scenario exit status alone as smoke acceptance without verifying signed receipts, governed read-back, and populated data Operator stores (`INV-REL-SMOKE-02`, `INV-REL-SMOKE-04`).
- Generating release smoke reports from Gateway `.ztd` trees when mutations executed in dedicated data Operator runtimes (`INV-REL-SMOKE-03`).
- Substituting smoke CSVs for signed and offline-verified public compliance bundles (`INV-REL-SMOKE-05`, `INV-REL-COMP-01`).
- Adding `CLOUDFLARE_*` secrets to the `release` or `production` environment; they shadow the repository secrets that rehearsals verify.
- Writing `latest.json` before GitHub release publication, or without verifying versioned prefix completeness.
- Overwriting or deleting published `v<version>/` prefixes to change served versions. Roll back by rewriting `latest.json`.

## Links out

- [Developer Guidelines](devs.md): coding invariants and repository standards.
- [Documentation Guide](docs.md): documentation audit, catalog, and formatting standards.
- [Testing Guide](tests.md): test execution tiers and verification commands.
- [Code Map](codemap.md): package and runtime ownership maps.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [Host-Native Headless UX Smoke Test](../guides/ux_smoke_test.md): maintained release smoke command, runtime scenarios, evidence, and acceptance criteria.

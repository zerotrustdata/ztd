---
doc_id: release_process
title: Release Process
audience: maintainers and coding agents
status: current
version: v1.7.0
last_updated: 2026-10-30
owners:
  - VERSION
  - Makefile
  - CHANGELOG.md
  - .github/workflows/release-binary.yml
  - scripts/release-rehearse.sh
  - scripts/release-publish.sh
  - scripts/release-r2.sh
  - scripts/release-artifacts.sh
  - internal/tools/versionsync/
  - internal/cli/cmd/test/release_smoke.go
  - website/worker.js
related:
  - docs/devs/devs.md
  - docs/devs/docs.md
  - docs/devs/tests.md
  - docs/devs/troubleshooting.md
  - docs/guides/ux_smoke_test.md
when_to_read: Preparing and publishing a platform release.
do_not_use_for:
  - Coding invariants (docs/devs/devs.md)
  - Documentation audit rules (docs/devs/docs.md)
  - Test tiers (docs/devs/tests.md)
  - Diagnostics and recovery beyond release publication (docs/devs/troubleshooting.md)
---

# Release Process

## Purpose

How a ztd release is prepared, tagged, signed, and published. One maintainer owns releases; coding agents may prepare the working tree but never commit, push, or tag.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Prepare the release](#prepare-the-release)
  - [Tag and publish](#tag-and-publish)
  - [Release Signing](#release-signing)
  - [Downloads, recovery, and rollback](#downloads-recovery-and-rollback)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

IDs are stable and never reused. Gaps are retired IDs.

| ID | Rule |
| :--- | :--- |
| INV-REL-DUTY-01 | Coding agents MUST NOT run `git commit`, `git push`, `gh pr create`, or `make release`. |
| INV-REL-DUTY-02 | The maintainer commits, merges to `main`, and runs `make release` from an up-to-date `main`. |
| INV-REL-VER-01 | `VERSION` (`vX.Y.Z\n`) is the single source of truth for the platform and protocol version. |
| INV-REL-VER-02 | `make version-sync-check` MUST pass: protocol doc headers, first-party `package.json`/`package-lock.json` versions, the `README.md` badge, and the website hero chip match `VERSION`. |
| INV-REL-VER-04 | Each release has `docs/release_notes/vX.Y.x/vX.Y.Z.md` and a row under `## vX.Y.x` in `CHANGELOG.md`. A new minor version creates both the section and the directory. |
| INV-REL-VER-09 | Release notes start with `## [X.Y.Z] - YYYY-MM-DD - <Title>`, where the date is the planned tag date in UTC and the title names what the release delivers. The `CHANGELOG.md` row carries the same date. |
| INV-REL-VER-10 | While the major version is 1, a release that breaks a CLI, wire, or evidence contract, or that requires the Gateway and Operators to upgrade together, bumps the minor version. Patch releases stay compatible within their minor series. |
| INV-REL-SMOKE-01 | `./ztd test release-smoke` MUST pass against the candidate binary before tagging. Scenarios and evidence are described in [ux_smoke_test.md](../guides/ux_smoke_test.md). |
| INV-REL-SMOKE-03 | Smoke reports MUST read the data Operator's own populated `.ztd` stores, not Gateway state, an empty runtime, or stores combined from different roots. |
| INV-REL-SMOKE-04 | Smoke acceptance requires non-empty receipts, commitments, and file mutations in both Operator and Gateway runtimes, and only `PASS` results for `commitment_chain`, `git_merkle_root`, `file_mutation_linkage`, `receipt_commitment_crosslink`, and every `verification_summary.csv` row. The runner enforces this. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Version | `VERSION`, `internal/tools/versionsync/` | `make version-sync-check` |
| Release index | `CHANGELOG.md`, `docs/release_notes/` | Row and notes file exist |
| Explorer embed | `internal/services/gateway/explorer/static/` | `make explorer-embed` |
| Console embed | `internal/services/gateway/console/static/` | `make console-embed-check` |
| Tag and publish | `Makefile` (`make release`), `.github/workflows/release-binary.yml` | Maintainer only, on `main` |

## Procedures

### Prepare the release

On a release branch:

1. Write `vX.Y.Z` to `VERSION`, then run `make version-sync` (protocol docs, npm manifests, README badge, website hero chip).
2. Rebuild embeds: `make explorer-embed`, then `make console-embed-check` (run `make console-embed` if it fails).
3. Write `docs/release_notes/vX.Y.x/vX.Y.Z.md` (Overview, Added, Changed, Fixed, Compatibility) and add its `CHANGELOG.md` row.
4. Build and smoke-test: `make build && ./ztd test release-smoke`.
5. Commit, open a PR, wait for CI to pass, and merge.

### Tag and publish

```bash
git checkout main && git pull
make release
```

`make release` aborts unless all of the following hold:

- you are on `main`, and `HEAD` equals `origin/main`;
- version sync leaves the tree clean;
- the release notes file exists;
- the tag does not already exist.

It then tags `vX.Y.Z` and pushes. The tag triggers `release-binary.yml`, which runs these jobs in order. Nothing public changes until `publish`:

1. `validate` and `credentials`: check the tag against `VERSION` and probe R2 with the publish credential.
2. `build`: build, checksum, and sign every target, then store the signed set as a workflow artifact.
3. `verify-download`: run the full publication procedure against a temporary GitHub draft and an isolated R2 prefix, then clean up.
4. `publish`: publish the GitHub release, upload the versioned R2 set, and promote website downloads.
5. `verify-install`: check `go install` on Linux, macOS, and Windows.

Watch it with `gh run list --workflow release-binary.yml --branch vX.Y.Z`, then `gh run watch <id> --exit-status`. If a job fails, see [Recovery](#downloads-recovery-and-rollback).

`make release-rehearse` runs the same workflow from `main` without publishing. It is optional; use it after changing `release-binary.yml` or `scripts/release-*.sh`.

### Release Signing

CI signs each binary keylessly with `cosign sign-blob --bundle` using the workflow's GitHub OIDC identity. No long-lived signing key exists. Each binary ships with `<binary>.sha256` and `<binary>.sigstore.json`.

- Signer identity: `https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/vX.Y.Z`
- OIDC issuer: `https://token.actions.githubusercontent.com`

For verification steps, see [Verify a release binary](../guides/getting_started.md#verify-a-release-binary).

### Downloads, recovery, and rollback

Release files go to the `ztd-releases` R2 bucket in a fixed order:

1. **Versioned upload.** Files go to `v<VERSION>/`. Existing objects are verified, never overwritten.
2. **GitHub publish.** The draft release becomes public.
3. **Promotion.** `latest.json` (`{"version": "<VERSION>"}`) is written last.

`website/worker.js` serves `/v/<version>/<file>` as immutable. Unversioned routes resolve through `latest.json` and are cached for 5 minutes.

**Recovery.** If any job in the tag run fails, fix the cause (for example, a secret) and run `gh run rerun <run-id> --failed`. Reruns pick up where the run stopped: a rerun after `build` reuses the already-signed set, and `publish` resumes partial GitHub and R2 uploads without overwriting anything. Do not move the tag or delete objects. Until promotion succeeds, unversioned downloads keep serving the previous version.

A rerun uses the code, workflow, and scripts at the tagged commit, so a failure that needs a change to any of them (for example, `build` failing on one platform) cannot be fixed by a rerun. Fix it on `main` and release the next patch version. The failed tag never published anything.

**Rollback.** Rewrite `latest.json` to the previous version. Never modify or delete a published `v<version>/` prefix.

**Secrets.** `CLOUDFLARE_ACCOUNT_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` are repository-level secrets. Do not copy them into the `release` environment: an environment copy shadows the credential that the `credentials` job and rehearsals verify.

## Anti-patterns

- Pushing a release tag by hand instead of running `make release`.
- Bumping `VERSION` without rebuilding the Explorer embed, which leaves its "(current)" label stale.
- Rebuilding instead of rerunning failed jobs when recovering a release; a rebuild produces different signatures.
- Overwriting a published `v<version>/` prefix instead of rolling back through `latest.json`.

## Links out

- [Documentation Guide](docs.md): front matter and audit rules.
- [Testing Guide](tests.md): test tiers and suites.
- [Troubleshooting Guide](troubleshooting.md): diagnostics.
- [Host-Native Headless UX Smoke Test](../guides/ux_smoke_test.md): release smoke scenarios and evidence.

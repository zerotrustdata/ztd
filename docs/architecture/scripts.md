---
doc_id: scripts
title: Automation Scripts
audience: maintainers and coding agents
status: current
version: v1.0.5
last_updated: 2026-10-13
owners:
  - scripts/
  - internal/services/gateway/scripts/
  - internal/tools/
related:
  - docs/devs/docs.md
  - docs/devs/devs.md
  - docs/guides/getting_started.md
  - docs/guides/build_operator.md
  - docs/guides/air_gap.md
when_to_read: Understanding the automation under scripts/, the Go tools under internal/tools/, the Gateway's embedded deploy templates, onboarding workflows, or the validation toolchain.
do_not_use_for:
  - Developer coding invariants (docs/devs/devs.md)
  - Getting-started user steps (docs/guides/getting_started.md)
  - Operator enrollment and configuration (docs/guides/build_operator.md)
---

# Automation Scripts

## Purpose

Catalogs the executable automation scripts under `scripts/`, the Go tools under `internal/tools/` implementing platform validators and generators, the deploy-script templates embedded in the Gateway, the onboarding test matrix, and the validation toolchain. The root `Makefile` orchestrates builds, contributor toolchains, host lifecycles, and verification targets. The platform operates without an external Python runtime dependency.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Development Bootstrap](#development-bootstrap)
- [Contributor Toolchain](#contributor-toolchain)
- [Smoke and Onboarding Tests](#smoke-and-onboarding-tests)
- [Host Lifecycle Targets](#host-lifecycle-targets)
- [Go Tools Directory](#go-tools-directory)
- [Commit Hygiene and Validation Gate](#commit-hygiene-and-validation-gate)
- [Gateway-Embedded Operator Bootstrap](#gateway-embedded-operator-bootstrap)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Scripts and Toolchain (`INV-SCRIPTS`)

| ID | Rule |
| --- | --- |
| INV-SCRIPTS-01 | Setup scripts MUST check for `git`, `make`, `curl`, and `go` before building. Unless `--build-only` is passed, they also MUST check `node`, `npm` (Node.js 22.22.2+, 24.15+, or 26+), `rg` (ripgrep), `bc`, and a C compiler. `make build` requires only Go, Make, and git: it embeds the committed Console and Evaluation Explorer assets and does not need Node.js. The Go version MUST be read from `go.mod` and compared including the patch component. Every tool pin for the contributor toolchain (`BUF_VERSION`, `PROTOC_GEN_GO_VERSION`, `GOLANGCI_LINT_VERSION`, etc.) MUST be read from the root `Makefile` as its single source of truth. |
| INV-SCRIPTS-02 | Setup scripts on Linux and macOS MUST update shell profiles (`~/.zshrc`, `~/.bashrc`, or `~/.profile`) for persistent PATH modification. Windows scripts MUST update the user-level `Path` through the registry. Invoking users MUST open a new shell or source the profile before `ztd` is available in other terminals. |
| INV-SCRIPTS-04 | Gateway-served deploy scripts (`ztd-deploy.sh`, `ztd-deploy.ps1`) are bootstrap conveniences for trusted networks only. They fetch artifacts over plain HTTP, lack TLS protection, and delete `~/.ztd/pki` before startup. Users MUST fetch and inspect the rendered script before execution. |
| INV-SCRIPTS-06 | Unless `--build-only` is passed, the Linux, macOS, and Windows setup scripts MUST leave a machine that passes `make dev-check`. |
| INV-SCRIPTS-07 | A prerequisite that resolves under `/mnt/<drive>/` MUST NOT count as installed on WSL. |
| INV-SCRIPTS-08 | Re-running setup MUST reuse installed prerequisites, Go dev tools, and ready Node dependency trees. Dependency installation runs only when tooling is missing or manifests change. |
| INV-SCRIPTS-09 | `.gitignore` is the authoritative list of paths that must never be committed. `scripts/check-commit-hygiene.sh` MUST reject any added path it matches, any blob over 5 MiB outside `vendor/`, any ELF, Mach-O, or PE executable, and the committer's own home-directory path. |
| INV-SCRIPTS-10 | `make quickstart` MUST compose only public `ztd` CLI commands, MUST reuse a running Gateway and an existing CLI identity, and MUST NOT read or write `.ztd/` directly. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Linux development bootstrap | `scripts/linux-setup.sh` | Prerequisite checks, package installs, official Go/Node tarballs, toolchain setup |
| macOS development bootstrap | `scripts/macos-setup.sh` | Prerequisite checks, Homebrew packages, Xcode tools prompt, toolchain setup |
| Windows development bootstrap | `scripts/windows-setup.ps1` | PowerShell 7+, build/toolchain checks, winget/Chocolatey package installs |
| Shared setup helpers | `scripts/lib/dev-setup-common.sh` | Version comparison, prerequisite checks, PATH profile updates, TTY formatting |
| Quickstart script | `scripts/quickstart.sh`, `make quickstart` | Builds `./ztd`, boots/reuses Gateway, logs in, executes first governed action |
| Onboarding script tests | `scripts/test-onboarding.sh`, `make test-onboarding` | Hermetic offline test of setup library and quickstart |
| Fresh-host onboarding matrix | `scripts/onboarding/`, `make test-onboarding-docker` | Disposable per-distro containers testing setup and quickstart scenarios |
| Toolchain preflight | `scripts/dev-check.sh`, `make dev-check` | Reports missing/mismatched tools and exits non-zero if invalid |
| Contributor toolchain targets | `Makefile` (`dev-setup`, `dev-tools`, `dev-node`, `dev-check`, `hooks-install`) | Installed via pinned version variables in root `Makefile` |
| Go package smoke test | `scripts/smoke-test-go.sh` | Verifies downstream module imports against local repository |
| Fresh onboarding smoke test | `scripts/ci/onboarding-smoke.sh` | Automated Linux runner testing full setup, smoke actions, and local evidence |
| Operator deployment lifecycle | `scripts/ci/operator-deploy-lifecycle.sh` | Automated multi-operator deployment, liveness, and termination lifecycle |
| Commit hygiene gate | `scripts/check-commit-hygiene.sh`, `scripts/git-hooks/` | Pre-commit (`--staged`) and pre-push (`--range`) hygiene validations |
| Gateway-embedded deploy (Bash) | `internal/services/gateway/scripts/ztd-deploy.sh` | Embedded in binary; served at `/ztd-deploy.sh` |
| Gateway-embedded deploy (PowerShell) | `internal/services/gateway/scripts/ztd-deploy.ps1` | Embedded in binary; served at `/ztd-deploy.ps1` |
| Binary download manifest | `bin/ztd-binaries.json` | Cross-platform binary catalog matrix |

## Development Bootstrap

Bootstrap development hosts using platform-specific scripts:

```bash
# Linux (Ubuntu, Debian, Fedora, Arch, openSUSE)
bash scripts/linux-setup.sh

# macOS (Homebrew + Xcode Command Line Tools)
bash scripts/macos-setup.sh

# Windows (PowerShell 7+)
pwsh scripts/windows-setup.ps1
```

- **Options**: `-y` / `--yes` (unattended installation), `--build-only` (skip Node/explorer, build binary only), `-h` (help).
- **WSL Isolation**: Rejects binaries resolving under `/mnt/<drive>/` to avoid broken Windows interoperability shims (`INV-SCRIPTS-07`).
- **Profile Updates**: Persistently configures `PATH` in `~/.zshrc`, `~/.bashrc`, or registry without overwriting existing configurations.
- **Env File**: After `make build`, setup copies `.env.example` to `.env` (owner-only mode `0600` on Linux and macOS) and keeps an existing `.env`. Every `./ztd` command loads it (`INV-ENV-04` in [Developer Guidelines](../devs/devs.md)).

## Contributor Toolchain

Managed via the root `Makefile` using pinned tool definitions:

```bash
make dev-setup    # Installs missing Go tools, Node dependencies, and git hooks
make dev-check    # Preflight verification; runs before all CI targets
```

- `make dev-tools`: Refreshes all six Go developer tools (`buf`, `protoc-gen-go`, `protoc-gen-doc`, `golangci-lint`, `govulncheck`, and `swag`) to their pinned versions in `GOBIN` or `GOPATH/bin`.
- `make dev-tools-if-needed`: Called by `make dev-setup`. Inspects tool module versions and Go compiler versions using `go version -m` (`ztd_tool_version`). Reinstalls on pin mismatch or when built with an older Go toolchain than `go.mod` requires, and fails after install if `PATH` still resolves a stale copy elsewhere outside `GOBIN`.
- `make hooks-install`: Points `core.hooksPath` to `scripts/git-hooks`.
- `make dev-node`: Installs npm dependencies across `protocol/node`, `console`, `ztd-adapter`, and `website`.

## Smoke and Onboarding Tests

- **`scripts/smoke-test-go.sh`**: Creates an ephemeral Go module with a `replace` directive pointing to `github.com/zerotrustdata/ztd/protocol` to ensure the Go module resolves and compiles cleanly.
- **`make test-onboarding`**: Offline, hermetic unit tests validating setup scripts, version checking, and quickstart execution.
- **`make test-onboarding-docker`**: Replays setup and execution across clean container images (`ubuntu`, `debian`, `fedora`, `arch`).

## Host Lifecycle Targets

The root `Makefile` provides targets to manage platform processes on the development host without requiring Docker. The targets call `./ztd`, which loads only registry `secret` and `user_endpoint` keys from `.env` (`INV-ENV-04`); pass every other flag through `GW_ARGS`, `OPERATOR_ARGS`, or `QUICKSTART_ARGS`:

| Target | Description |
| :--- | :--- |
| `make quickstart` | Builds `./ztd`, boots Gateway with data capability, logs in, executes test command, prints next steps |
| `make up` | Builds `./ztd` and starts Gateway in background (`./ztd gw start`) |
| `make up-public` | Same as `make up` with `--listen-host 0.0.0.0` so Operators on other hosts can enroll |
| `make gw` | Builds `./ztd`, starts Gateway with `GW_CAPABILITIES` (default `data,inference,observer`; `MODEL_STORAGE_ROOT` adds `provenance`), then runs `./ztd gw status` |
| `make gw-public` | Same as `make gw` with `--listen-host 0.0.0.0` |
| `make operator` | Builds `./ztd` and runs Operator in foreground (`./ztd operator start $(OPERATOR_ARGS)`); pass `--working-dir` when the launch directory holds the Gateway runtime, which Operator start refuses |
| `make status` | Runs `./ztd gw status` (Gateway state, Operator inventory, enrollment counts, Console URL) |
| `make down` / `make stop` | Gracefully stops Operators and Gateway, preserving local `.ztd/` state |

## Go Tools Directory

The `internal/tools/` package implements Go-based generators and verification tools:

- `agent_harness`: Typed governed Gateway client shared by `ztd eval` and `ztd test` release smoke; not an agent (`INV-AGT-08` in [AI Agents](agents.md)).
- `bslheaders`: Verifies BSL 1.1 license headers on all first-party source files (`make bsl-headers-check`).
- `chaos`: Test-only governance event generator behind `./ztd test chaos`; drives the L4/L5 stack in process against a run-local audit store.
- `constgen`: Generates Go and TypeScript constant files from `protocol/constants/` and checks the environment registry (`make constants-generate`, `make constants-check`).
- `explorercatalog`: Generates scenario catalogs for the Evaluation Explorer (`make explorer-catalog-generate`, `make explorer-catalog-check`).
- `jsoncheck`: Validates JSON doctrine schema structures (`make doctrines-validate`).
- `sourcefiles`: Lists frontend source files as portable `Makefile` dependencies for the incremental console and explorer builds.
- `thirdpartynotices`: Generates `THIRD_PARTY_NOTICES.md` (`make third-party-notices`, `make third-party-notices-check`).
- `treehash`: Computes the source-tree digest stamped into release binaries.
- `versionsync`: Synchronizes version declarations across protocol docs and package manifests (`make version-sync`, `make version-sync-check`).
- `ztdbinaries`: Maintains cross-compilation target definitions for release distribution.

## Commit Hygiene and Validation Gate

Enforced via `scripts/check-commit-hygiene.sh` locally and in CI:
- Rejects files tracked by `.gitignore`.
- Blocks binary blobs exceeding 5 MiB (outside `vendor/`).
- Blocks ELF, Mach-O, and Windows PE executable binaries.
- Blocks committer home-directory paths and secret leaks (via Gitleaks).

## Gateway-Embedded Operator Bootstrap

The Gateway embeds `ztd-deploy.sh` and `ztd-deploy.ps1`, serving them over plain HTTP port 8080:

```bash
# Linux / macOS
curl -fsSL http://<gateway-host>:8080/ztd-deploy.sh | bash

# Windows PowerShell
iwr http://<gateway-host>:8080/ztd-deploy.ps1 -UseBasicParsing | iex
```

- Cleans existing `~/.ztd/pki` credentials.
- Downloads matching binary and verified `.sha256` sidecar from `/.well-known/ztd/bin/`.
- Launches Operator in foreground connected to specified Gateway host.

## Anti-patterns

- **Running CLI without new shell after setup**: Expecting immediate PATH updates without opening a new terminal session (`INV-SCRIPTS-02`).
- **Executing scripts outside repo root**: Running setup from arbitrary directories, breaking Makefile dependency discovery (`INV-SCRIPTS-01`).
- **Using WSL Windows shims for Node/Go**: Relying on `/mnt/c/...` executables inside Linux environments (`INV-SCRIPTS-07`).
- **Deploying bootstrap scripts in production**: Running unauthenticated plain-HTTP deploy scripts across untrusted public networks (`INV-SCRIPTS-04`).

## Links out

- [Developer Guidelines](../devs/devs.md): Coding standards, error handling, and test rules.
- [Documentation Guide](../devs/docs.md): Documentation formatting, invariants, and catalog maintenance.
- [Getting Started Guide](../guides/getting_started.md): End-user installation and onboarding walkthrough.
- [Operator Deployment Guide](../guides/build_operator.md): Detailed Operator enrollment and configuration.
- [Air-Gapped Deployment](../guides/air_gap.md): Offline staging and isolated deployment procedures.

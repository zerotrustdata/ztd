---
doc_id: scripts
title: Automation Scripts
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
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
| INV-SCRIPTS-01 | Setup scripts MUST check for `git`, `make`, `curl`, `go`, `node`, and `npm` (Node.js 22.22.2+, 24.15+, or 26+) before building in both modes. `make build` compiles and embeds the Console and Evaluation Explorer frontends. Unless `--build-only` is passed, Unix setup scripts also MUST check `rg` (ripgrep), `bc`, and a C compiler. The Go version MUST be read from `go.mod` and compared including the patch component. Every tool pin for the contributor toolchain (`BUF_VERSION`, `PROTOC_GEN_GO_VERSION`, `GOLANGCI_LINT_VERSION`, etc.) MUST be read from the root `Makefile` as its single source of truth. |
| INV-SCRIPTS-02 | Setup scripts on Linux and macOS MUST update shell profiles (`~/.zshrc`, `~/.bashrc`, or `~/.profile`) for persistent PATH modification, and MUST do so only after confirmation or with `--yes`; a non-interactive run without `--yes` leaves the profile unchanged. Windows scripts MUST update the user-level `Path` through the registry under the same confirmation rule (`--yes` or an interactive confirmation). Invoking users MUST open a new shell or source the profile before `ztd` is available in other terminals. |
| INV-SCRIPTS-04 | Gateway-served deploy scripts (`ztd-deploy.sh`, `ztd-deploy.ps1`) are bootstrap conveniences for trusted networks only. They fetch artifacts over plain HTTP, lack TLS protection, and delete `~/.ztd/pki` before startup. Users MUST fetch and inspect the rendered script before execution. |
| INV-SCRIPTS-06 | Unless `--build-only` is passed, the Linux, macOS, and Windows setup scripts MUST leave a machine that passes `make dev-check`. |
| INV-SCRIPTS-07 | A prerequisite that resolves under `/mnt/<drive>/` MUST NOT count as installed on WSL. |
| INV-SCRIPTS-08 | Re-running setup MUST reuse installed prerequisites, Go dev tools, and ready Node dependency trees. Dependency installation runs only when tooling is missing or manifests change. |
| INV-SCRIPTS-09 | `.gitignore` is the authoritative list of paths that must never be committed. `scripts/check-commit-hygiene.sh` MUST reject any added path that `git check-ignore` matches, any blob over 5 MiB outside `vendor/`, any ELF, Mach-O, or PE executable, and the committer's own home-directory path. |
| INV-SCRIPTS-10 | `make quickstart` MUST compose only public `ztd` CLI commands, MUST reuse a running Gateway and an existing CLI identity, and MUST NOT read or write files under `.ztd/` itself. Its only build side effect is `make build` when `./ztd` is absent. Its only `.ztd/` write is a scratch directory under `.ztd/tmp/` (`INV-FS-11` in [Developer Guidelines](../devs/devs.md)), removed on exit. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| Linux development bootstrap | `scripts/linux-setup.sh` | Prerequisite checks, package installs, official Go/Node tarballs, toolchain setup |
| macOS development bootstrap | `scripts/macos-setup.sh` | Prerequisite checks, Homebrew packages, Xcode tools prompt, toolchain setup |
| Windows development bootstrap | `scripts/windows-setup.ps1` | PowerShell 7+, build/toolchain checks, winget/Chocolatey package installs |
| Shared setup helpers | `scripts/lib/dev-setup-common.sh` | Version comparison, prerequisite checks, PATH profile updates, TTY formatting |
| Quickstart script | `scripts/quickstart.sh`, `make quickstart` | Builds `./ztd` when absent, boots/reuses Gateway, logs in, executes first governed action |
| Onboarding script tests | `scripts/test-onboarding.sh`, `make test-onboarding` | Hermetic offline test of setup library and quickstart |
| Fresh-host onboarding matrix | `scripts/onboarding/`, `make test-onboarding-docker` | Disposable per-distro containers testing setup and quickstart scenarios |
| Toolchain preflight | `scripts/dev-check.sh`, `make dev-check` | Reports missing/mismatched tools and exits non-zero if invalid |
| Contributor toolchain targets | `Makefile` (`dev-setup`, `dev-tools`, `dev-node`, `dev-check`, `hooks-install`) | Installed via pinned version variables in root `Makefile` |
| Go package smoke test | `scripts/smoke-test-go.sh` | Verifies downstream module imports against local repository |
| Fresh onboarding smoke test | `scripts/ci/onboarding-smoke.sh` | Automated Linux runner testing full setup, smoke actions, and local evidence |
| Operator deployment lifecycle | `scripts/ci/operator-deploy-lifecycle.sh` | Automated multi-operator deployment, liveness, and termination lifecycle |
| Commit hygiene gate | `scripts/check-commit-hygiene.sh`, `scripts/git-hooks/` | Pre-commit (`--staged`) and pre-push (`--range`) hygiene validations |
| Commit hygiene tests | `scripts/test-commit-hygiene.sh`, `make test-commit-hygiene` | Exercises the hygiene hook in disposable repositories |
| Release automation | `scripts/release-preflight.sh`, `scripts/release-artifacts.sh`, `scripts/release-r2.sh`, `scripts/test-release-scripts.sh` | Release tag and `VERSION` preflight, binary set checksums, signing, and verification, R2 publication, and offline gate tests; called by `.github/workflows/release-binary.yml` |
| Gateway LAN helper (Windows) | `scripts/configure-gateway-lan.ps1` | Inspects, applies, or removes Windows portproxy and firewall rules that expose Gateway ports 8080 and 8443 to a WSL distribution |
| Shared scratch helper | `scripts/lib/scratch.sh` | Creates repo-run scratch directories under `.ztd/tmp/` (`INV-FS-11`) |
| Gateway-embedded deploy templates | `internal/services/gateway/scripts/templates.go` | `go:embed` owner for `ztd-deploy.sh` and `ztd-deploy.ps1`; renders each with the Gateway host and port (`TemplateData`) |
| Gateway-embedded deploy (Bash) | `internal/services/gateway/scripts/ztd-deploy.sh` | Embedded in binary; served at `/ztd-deploy.sh` |
| Gateway-embedded deploy (PowerShell) | `internal/services/gateway/scripts/ztd-deploy.ps1` | Embedded in binary; served at `/ztd-deploy.ps1` |
| Binary download manifest | `bin/ztd-binaries.json` | Written by `make build-all` through `internal/tools/ztdbinaries`, with a per-target `.sha256` sidecar for each binary |

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

- **Options**: `-y` / `--yes` (unattended installation), `--build-only` (build the binary and embedded frontends; skip contributor tools), `-h` (help).
- **WSL Isolation**: Rejects binaries resolving under `/mnt/<drive>/` to avoid broken Windows interoperability shims (`INV-SCRIPTS-07`).
- **Linux Packages**: Detects the package manager before translating prerequisite names. The C compiler prerequisite installs `build-essential` on apt, `gcc` on dnf/zypper, or `base-devel` on pacman.
- **Profile Updates**: Persistently configures `PATH` in `~/.zshrc`, `~/.bashrc`, or `~/.profile` (Linux and macOS) or the user-level `Path` in the registry (Windows). Profiles are only appended to, never rewritten.
- **Linux Key Store**: `scripts/linux-setup.sh` requires a Secret Service or a passphrase-protected key in ssh-agent, which the Gateway needs to seal its master key (it otherwise fails closed). Setup stops with an error, rather than warning, when the key has no passphrase, when no key exists and creating one is declined, or when the profile block is declined. It creates an Ed25519 key if none exists, offers `ssh-keygen -p` for an unprotected key, and writes (or replaces an outdated) marker-delimited profile block that shares one owner-only agent socket across shells.
- **Interactive Fixes**: Setup checks first and prompts only when a change is actually needed (an up-to-date toolchain, hooks, Go, key and profile block are skipped silently), before anything that changes the system and offers a quick fix wherever one exists: package and toolchain installs, upgrading a system Go older than `go.mod` requires (official tarball on Linux, `brew upgrade` on macOS), the contributor toolchain and git hooks (`core.hooksPath`), key creation, a passphrase for an unprotected key, the ssh-agent profile block, `PATH` entries, and a new shell with the changes loaded. `--yes` accepts every prompt except the new shell; without a terminal and without `--yes`, required installation prompts fail instead of guessing. The Linux key store check is skipped (with a notice) in `--build-only` and non-terminal runs.
- **Optional Docker and Chromium**: Linux and macOS contributor setup detects Docker and Chromium (or Google Chrome, including macOS applications) and offers each missing tool separately. Linux uses distribution packages; macOS uses Homebrew casks. Declining an offer or an installation failure leaves setup successful. `--build-only` skips these tools; non-terminal runs skip the offers unless `--yes` is passed, which accepts the optional installs too. Docker detection checks the CLI without requiring a running daemon; starting Docker or completing Docker Desktop setup remains a separate step.
- **Env File**: After building `./ztd`, setup copies `.env.example` to `.env` (owner-only mode `0600` on Linux and macOS) and keeps an existing `.env`. Only `./ztd` commands that pass the root pre-run load it, and only `secret` and `user_endpoint` keys are accepted (`INV-ENV-04` in [Developer Guidelines](../devs/devs.md)).

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

- **`scripts/smoke-test-go.sh`**: Creates an ephemeral Go module under `.ztd/tmp/` with a `replace` directive mapping `github.com/zerotrustdata/ztd` to the local repository, then builds a program that imports `github.com/zerotrustdata/ztd/protocol`.
- **`make test-onboarding`**: Offline, hermetic unit tests validating setup scripts, version checking, and quickstart execution.
- **`make test-onboarding-docker`**: Replays setup and execution across clean container images (`ubuntu`, `debian`, `fedora`, `arch`). After build-only setup installs Git, the disposable working-tree snapshot receives a local commit so release-smoke evidence can identify the exact source it tests.

## Host Lifecycle Targets

The root `Makefile` provides targets to manage platform processes on the development host without requiring Docker. The targets call `./ztd`, which loads only registry `secret` and `user_endpoint` keys from `.env` (`INV-ENV-04`); pass global CLI flags through `ZTD_ARGS`, and every other flag through `GW_ARGS`, `OPERATOR_ARGS`, or `QUICKSTART_ARGS`:

| Target | Description |
| :--- | :--- |
| `make quickstart` | Builds `./ztd` when absent, reuses or starts a Gateway with the data capability, signs in if needed, and runs a first governed command (`operator run --all-active`) |
| `make up` | Builds `./ztd` and starts Gateway in background (`./ztd gw start`) |
| `make up-public` | Same as `make up` with `--listen-host 0.0.0.0` so Operators on other hosts can enroll |
| `make gw` | Builds `./ztd`, starts Gateway with `GW_CAPABILITIES` (default `data,inference,observer`; `MODEL_STORAGE_ROOT` adds `provenance`), then runs `./ztd gw status` |
| `make gw-public` | Same as `make gw` with `--listen-host 0.0.0.0` |
| `make operator` | Builds `./ztd` and runs Operator in foreground (`./ztd operator start $(OPERATOR_ARGS)`); pass `--working-dir` through `OPERATOR_ARGS` to set the Operator's working directory |
| `make status` | Runs `./ztd gw status`, which shows Gateway status, Operator capabilities, and enrollment counts (`./ztd gw status --help`) |
| `make down` / `make stop` | Gracefully stops Operators and Gateway, preserving local `.ztd/` state |

## Go Tools Directory

The `internal/tools/` package implements Go-based generators and verification tools:

- `agent_harness`: Typed governed Gateway client shared by `ztd eval` and `ztd test` release smoke; not an agent (`INV-AGT-08` in [AI Agents](agents.md)).
- `bslheaders`: Verifies BSL 1.1 license headers on all first-party source files (`make bsl-headers-check`).
- `chaos`: Test-only governance event generator behind `./ztd test chaos`; drives the L4/L5 stack in process and writes through the production Local-First Events Store.
- `constgen`: Generates Go and TypeScript constant files from `protocol/constants/` and checks the environment registry (`make constants-generate`, `make constants-check`).
- `explorercatalog`: Generates scenario catalogs for the Evaluation Explorer (`make explorer-catalog-generate`, `make explorer-catalog-check`).
- `jsoncheck`: Verifies that each named doctrine file is well-formed JSON (`make doctrines-validate`).
- `sourcefiles`: Lists frontend source files as portable `Makefile` dependencies for the incremental console and explorer builds.
- `thirdpartynotices`: Generates `THIRD_PARTY_NOTICES.md` (`make third-party-notices`, `make third-party-notices-check`).
- `treehash`: Computes the source-tree digest that the root `Makefile` passes to `ztdbinaries` as `--source-tree-hash` (`SOURCE_TREE_HASH`).
- `versionsync`: Writes or checks version declarations against `VERSION` (`make version-sync`, `make version-sync-check`).
- `ztdbinaries`: Maintains cross-compilation target definitions. `make build-all` writes a `.sha256` sidecar per target binary and the `ztd-binaries.json` manifest, and removes any stale `checksums-sha256.txt` first. The release `checksums-sha256.txt` is written by `scripts/release-artifacts.sh checksums`.

## Commit Hygiene and Validation Gate

Enforced via `scripts/check-commit-hygiene.sh` in the pre-commit and pre-push hooks and in CI:
- Rejects added paths that `.gitignore` matches.
- Blocks binary blobs exceeding 5 MiB (outside `vendor/`).
- Blocks ELF, Mach-O, and Windows PE executable binaries.
- Rejects file modes that disagree with content outside `vendor/`: mode `100755` without a `#!` shebang, or a `#!` script at mode `100644`.
- Blocks committer home-directory paths on local runs only. Scans for secrets with Gitleaks: locally when `gitleaks` is installed, and always in CI.

## Gateway-Embedded Operator Bootstrap

The Gateway embeds `ztd-deploy.sh` and `ztd-deploy.ps1` (`internal/services/gateway/scripts/templates.go`) and serves them over plain HTTP at `/ztd-deploy.sh` and `/ztd-deploy.ps1`, on the Gateway's configured HTTP port (default 8080). Each served script has the Gateway host and port rendered into it: the host comes from the request's `Host` header and the port from the Gateway's HTTP port. An exported `GATEWAY_HOST` (PowerShell: `$env:GATEWAY_HOST`) overrides the rendered host:

```bash
# Linux / macOS
curl -fsSL "http://<gateway-host>:8080/ztd-deploy.sh" | bash

# Windows PowerShell
iwr "http://<gateway-host>:8080/ztd-deploy.ps1" -UseBasicParsing | iex
```

- Removes existing `~/.ztd/pki` credentials before startup (`%USERPROFILE%\.ztd\pki` on Windows).
- Downloads the matching binary and its `.sha256` sidecar from `/.well-known/ztd/bin/`. The Bash script verifies the checksum with `sha256sum` or `shasum`.
- Launches Operator in the foreground, connected to the Gateway host (`./ztd operator start -e <host>`).

## Anti-patterns

- **Running CLI without new shell after setup**: Expecting immediate PATH updates without opening a new terminal session (`INV-SCRIPTS-02`).
- **Running setup scripts from a copy outside the clone**: The setup library resolves the repository root from its own location and exits with a fatal error when `go.mod` is absent there (`scripts/lib/dev-setup-common.sh`, `ztd_setup_init`).
- **Using WSL Windows shims for Node/Go**: Relying on `/mnt/c/...` executables inside Linux environments (`INV-SCRIPTS-07`).
- **Deploying bootstrap scripts in production**: Running unauthenticated plain-HTTP deploy scripts across untrusted public networks (`INV-SCRIPTS-04`).

## Links out

- [Developer Guidelines](../devs/devs.md): Coding standards, error handling, and test rules.
- [Documentation Guide](../devs/docs.md): Documentation formatting, invariants, and catalog maintenance.
- [Getting Started Guide](../guides/getting_started.md): End-user installation and onboarding walkthrough.
- [Operator Deployment Guide](../guides/build_operator.md): Detailed Operator enrollment and configuration.
- [Air-Gapped Deployment](../guides/air_gap.md): Offline staging and isolated deployment procedures.

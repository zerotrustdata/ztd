---
doc_id: troubleshooting
title: Troubleshooting Guide
audience: maintainers and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - internal/cli/cmd/
  - internal/services/gateway/
  - internal/services/auth/
  - internal/services/vault/
related:
  - docs/devs/devs.md
  - docs/devs/codemap.md
  - docs/devs/tests.md
  - docs/devs/docs.md
  - docs/devs/release_process.md
when_to_read: Diagnosing development, build, test, gateway startup, authentication, enrollment, or runtime errors.
do_not_use_for:
  - Platform coding standards and invariants (docs/devs/devs.md)
  - Full documentation catalog and writing rules (docs/devs/docs.md)
  - Release execution and compliance bundles (docs/devs/release_process.md)
  - Package and runtime ownership maps (docs/devs/codemap.md)
  - Test tiers and test execution (docs/devs/tests.md)
---

# Troubleshooting Guide

## Purpose

Diagnose and resolve common contributor setup, build, test, Gateway startup, authentication, vault, governance, and deployment failures across the ztd platform.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Environment and binary (`INV-TROUBLE-ENV`)](#environment-and-binary-inv-trouble-env)
  - [Gateway startup and vault (`INV-TROUBLE-GW`)](#gateway-startup-and-vault-inv-trouble-gw)
  - [Authentication and trust (`INV-TROUBLE-AUTH`)](#authentication-and-trust-inv-trouble-auth)
  - [Operator connectivity and governance (`INV-TROUBLE-OP`)](#operator-connectivity-and-governance-inv-trouble-op)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Basic Health and Toolchain Triage](#basic-health-and-toolchain-triage)
  - [Build and Generation Triage](#build-and-generation-triage)
  - [Gateway Process Diagnosis](#gateway-process-diagnosis)
  - [Gateway CPU and Memory Profiling](#gateway-cpu-and-memory-profiling)
  - [Vault Validation and Key Recovery](#vault-validation-and-key-recovery)
  - [CLI Authentication and Session Recovery](#cli-authentication-and-session-recovery)
  - [Operator Connectivity and Enrollment Triage](#operator-connectivity-and-enrollment-triage)
  - [Governance Pipeline Diagnosis](#governance-pipeline-diagnosis)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Environment and binary (`INV-TROUBLE-ENV`)

| ID | Rule |
| :--- | :--- |
| INV-TROUBLE-ENV-01 | CLI commands MUST be run from the checkout root. Running from a subdirectory resolves a different `.ztd/` runtime tree. |
| INV-TROUBLE-ENV-02 | The Go toolchain MUST satisfy the `go` directive in `go.mod` (Go 1.27.3). |
| INV-TROUBLE-ENV-03 | A newly built binary MUST be produced with `make build`, which compiles `cmd/ztd` and places the runnable executable at `./ztd`. |
| INV-TROUBLE-ENV-04 | The root command loads `.env` from the working directory (`config.LoadEnvFile`) after its pre-run gates pass, so a refused command never sees it. When commands fail with an `.env` error, fix the file rather than the command: on Linux and macOS it MUST be a regular file owned by the user or root with mode `0600` or `0400` (`ErrEnvFileInvalid`, fix with `chmod 600 .env`), and every line MUST be `KEY=VALUE` (`ErrEnvFileMalformed`) naming a `secret` or `user_endpoint` key from `protocol/constants/env_vars.json` (`ErrEnvFileUnknownKey`). Platform configuration MUST go in command flags, never `.env`. |

### Gateway startup and vault (`INV-TROUBLE-GW`)

| ID | Rule |
| :--- | :--- |
| INV-TROUBLE-GW-01 | The Gateway requires an unlocked vault during startup. On first initialization, `.ztd/vault/vault.header` and the sealed key `.ztd/secrets/vault_key` are generated. Lost vault keys make data unrecoverable; MUST NOT overwrite or delete vault keys without explicit data disposal intent. |
| INV-TROUBLE-GW-02 | `gw restart` reads the launch profile at `.ztd/pids/operator-launch-profile.json`. If missing or invalid, `gw restart` fails closed; run `gw start` with the intended flags to write a new launch profile. |
| INV-TROUBLE-GW-03 | `gw clean` and `gw reset` are destructive commands that move the local `.ztd/` runtime tree aside to `.ztd-<MMDDHHMM>`; `gw reset` then starts a new Gateway without it. MUST NOT use either command to repair retained state. `make clean` removes build artifacts, harness and test temp directories, and the Go build cache; it does not reset Gateway runtime state. |
| INV-TROUBLE-GW-04 | `gw start` against a running Gateway succeeds only when every explicitly set startup flag (capabilities, posture, listen host, ports, model storage root, Ollama endpoint, public base URL, CORS origins, passkey RP ID, RP name, and origins, pprof listen) matches the running launch profile. Otherwise it prints the differences and fails with `ErrGatewayAlreadyRunning`; stop the Gateway (`./ztd gw stop` or `make down`), then rerun the start command. Runtime state in `.ztd/` is kept. |
| INV-TROUBLE-GW-05 | On macOS a rebuilt or upgraded `ztd` MUST be approved once for the `ztd-platform` Keychain item. While the dialog is open the Gateway logs `[Keystore] Waiting for approval to read the master key from the OS key store`, and a background `gw start` prints the remedy and allows five minutes for approval in addition to its startup budget. Approve with the login password and **Always Allow**. If approval or subsequent startup does not complete within that allowance, the start fails and includes the latest child diagnostic. |
| INV-TROUBLE-GW-06 | `gw start --pprof-listen` accepts only `localhost` or a loopback IP (`ErrProfilingListenAddress`); profiles expose command lines, heap contents, and goroutine stacks. Profiling is off when the flag is unset, and it is a startup flag covered by `INV-TROUBLE-GW-04`. |
| INV-TROUBLE-GW-07 | The public spectator mirror verifies `.ztd/public-mirror/mirror.db` and its proof files on every open; a tampered or truncated store fails startup with `ErrPublicFeedMirrorStoreCorrupt` and MUST NOT be hand-edited back into shape. Restore `.ztd/public-mirror/` from backup. |

Managed startup failures include the latest bounded child diagnostic from the current attempt and the full log location. Verification progress records preserve their stage, counts, and elapsed time in timeout diagnostics. See [Durable Mirror Store](../architecture/public_spectator.md#durable-mirror-store) for verification budgets and readiness behavior.

`gw restart` restores configuration using the current executable; it does not restore an earlier binary. If startup fails with the same certificate configuration, restart returns the failure without repeating the identical launch. If identity detection changed the certificate configuration, it can attempt to restore the previous configuration. A profile-write failure after a healthy start still stops the untracked process and attempts configuration restoration. The previous launch profile remains available after a failed restart.

### Authentication and trust (`INV-TROUBLE-AUTH`)

| ID | Rule |
| :--- | :--- |
| INV-TROUBLE-AUTH-01 | The canonical trust bundle path is `.ztd/pki/trust/ztdg-ca-bundle.pem`. Tests and tools MUST NOT use non-canonical bundle paths or mutate host PKI to fix test failures. |
| INV-TROUBLE-AUTH-02 | CLI sessions and certificates expire in 7 days; web sessions expire in 24 hours. When a CLI certificate is valid but session expired, run `ztd auth refresh` rather than re-enrolling. |
| INV-TROUBLE-AUTH-03 | Headless enrollment (`ztd login`, `ztd login cli`, alias `ztd login headless`, or `ztd auth enroll user --headless`) skips OS trust and passkey ceremony, producing an mTLS-capable CLI identity that does not support web Console sessions. |
| INV-TROUBLE-AUTH-04 | `ztd logout` ends every web and CLI session of the user on every machine and revokes their CLI certificates. A machine that was logged in elsewhere reports its certificate revoked, and `ztd auth refresh` cannot recover it. Run `ztd logout --local-only` to discard the stale local credentials, then `ztd login`. |

### Operator connectivity and governance (`INV-TROUBLE-OP`)

| ID | Rule |
| :--- | :--- |
| INV-TROUBLE-OP-01 | Outbound Operators connect strictly outbound over mTLS to the Gateway and expose NO inbound listening ports. Their pub/sub command channel is named `cmd:<operator_id>:<operator_session_id>` (Agent to Operator); it is a channel name, not a network address. To diagnose an unreachable Operator, check Gateway mTLS port reachability, local CA bundle integrity, and platform enrollment approval status. |
| INV-TROUBLE-OP-02 | Governance rejections at L1, L2, L3, or L4 fail closed. Diagnostic investigations MUST inspect error codes in event receipts (`ztd audit event-receipts`, `ztd audit events`, and `ztd audit votes --tx` for an L2 quorum rejection) and state roots before altering configuration or attempting re-execution. |
| INV-TROUBLE-OP-03 | `ztd operator start` and `ztd operator reset-identity` refuse a working directory whose runtime tree holds the Gateway root CA (`constants.GatewayRuntimeMarker`); `operator start` fails with `ErrOperatorGatewayRuntime`. Each executing runtime owns its own Local-First Events Store and state root, so an outbound Operator on the Gateway host MUST run with `--working-dir` set to a separate directory. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Go version requirement | `go.mod` | `go 1.27.3` |
| Platform build target | `Makefile`, `cmd/ztd` | `make build` |
| Gateway launch profile | `internal/cli/serve/launch_profile.go` | `.ztd/pids/operator-launch-profile.json` |
| Vault key path | `internal/constants/paths.go` | `SecretsDirname`/`SecretsFileVaultKey` (`secrets/vault_key`) |
| CA trust bundle | `internal/constants/paths.go` | `PkiDirname`/`PkiSubdirTrust`/`PkiFileGatewayBundle` (`pki/trust/ztdg-ca-bundle.pem`) |
| CLI session credentials | `internal/cli/config/config.go`, `internal/constants/paths.go` | `CredentialsFilename`, `CliCertFilename`, `CliKeyFilename` (`.ztd/credentials`, `.ztd/cli.crt`, `.ztd/cli.key`) |
| Operator enrollment identity files | `internal/cli/identityreset/reset.go` | `ztd operator reset-identity --help` |

## Procedures

### Basic Health and Toolchain Triage

```bash
pwd
ls README.md ztd Makefile VERSION
./ztd version
go version
```

### Build and Generation Triage

```bash
# Rebuild the root CLI binary
make build
chmod +x ztd

# Validate prerequisites for protocol regeneration
command -v go    # buf is installed by make proto-generate
command -v npm   # protocol/node bindings
make proto-generate
```

### Gateway Process Diagnosis

```bash
# Check running Gateway status and logs
./ztd gw status
./ztd gw logs

# Run in the foreground with debug logging
./ztd gw stop
./ztd gw start --follow --log debug
```

`./ztd gw status` prints a `Consensus` row that describes the bound L2 consensus and the posture the Gateway reports. Start the Gateway and each Operator against a new runtime root rather than one left by an earlier release: an existing `.ztd/data/ztd.db` is not migrated, because the `CREATE TABLE` statements are the whole schema (`INV-STOR-SCHEMA-01` in [Developer Guidelines](devs.md)).

### Gateway CPU and Memory Profiling

```bash
# Start the Gateway with the loopback pprof listener (mutex and block sampling are enabled with it)
./ztd gw stop
./ztd gw start --pprof-listen 127.0.0.1:6060   # plus your usual flags

# CPU: 30 s sample, interactive flame graph
go tool pprof -http=127.0.0.1:7070 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'

# Memory: live heap (inuse_space) and allocation churn (alloc_space)
go tool pprof -http=127.0.0.1:7071 http://127.0.0.1:6060/debug/pprof/heap
go tool pprof -sample_index=alloc_space -top http://127.0.0.1:6060/debug/pprof/allocs

# Goroutine leaks, lock contention, blocking
curl -s 'http://127.0.0.1:6060/debug/pprof/goroutine?debug=1' | head -60
go tool pprof -top http://127.0.0.1:6060/debug/pprof/mutex
go tool pprof -top http://127.0.0.1:6060/debug/pprof/block

# Scheduler/GC execution trace
mkdir -p .ztd/tmp/pprof
curl -so .ztd/tmp/pprof/gw.trace 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
go tool trace .ztd/tmp/pprof/gw.trace
```

### Vault Validation and Key Recovery

```bash
# Validate existing vault header with stored vault key
./ztd vault unlock

# Linux when the Secret Service is unreachable or has no default keyring (SSH-only sessions, servers, WSL):
# the master key is sealed under an Ed25519 or RSA key loaded in ssh-agent; the sealed form is stored at
# .ztd/secrets/.master_key.ssh-agent. An existing seal is used first.
eval "$(ssh-agent)" && ssh-add   # run ssh-keygen -t ed25519 first if no key exists
./ztd gw start
```

### CLI Authentication and Session Recovery

```bash
# Refresh an expired CLI session with a valid certificate
./ztd auth refresh

# Force certificate rotation before expiry
./ztd auth enroll user --rotate-cli

# Headless login on a remote or non-browser server
./ztd login cli

# Local credentials are stale because the certificate was revoked elsewhere
./ztd logout --local-only
./ztd login
```

### Operator Connectivity and Enrollment Triage

```bash
# List Operators connected to the Gateway
./ztd operator list

# If an Operator fails to connect, verify mTLS reachability to the Gateway's resolved HTTPS port.
# The default is 8443; use the bound port from the Gateway startup guidance or launch profile.
nc -zv 127.0.0.1 8443
```

Run `./ztd operator show` with an operator ID or operator session ID from the `list` output to inspect status, heartbeat, and capabilities. When certificates or keys are corrupted, run `ztd operator reset-identity` with `--working-dir` set to the Operator's working directory and `--yes`; the command stops the local Operator and removes its enrollment identity.

### Governance Pipeline Diagnosis

```bash
# Query recent event receipts to identify rejection layers (L1-L4):
./ztd audit event-receipts

# List the most recent raw audit events:
./ztd audit events --limit 20
```

Filter receipts with `--session` set to the Operator session ID, or fetch one canonical receipt with `--tx-id`.

When an L2 quorum rejects a transaction, inspect the bound consensus and each member's recorded vote:

```bash
# Show the bound L2 consensus, signer readiness, and posture
./ztd consensus status

# Show every recorded L2 member vote on one governed transaction
./ztd audit votes --tx <tx-id>
```

Members are bound with `./ztd consensus enroll app <primary-app> <https-endpoint>` or `./ztd consensus enroll tribunal --model <tag>`; see `./ztd consensus enroll --help`.

## Anti-patterns

- Running `./ztd` from outside the repository root, creating disjoint `.ztd/` state trees (`INV-TROUBLE-ENV-01`).
- Running `gw reset` or `gw clean` to fix transient errors when state must be retained (`INV-TROUBLE-GW-03`).
- Mutating OS trust or developer PKI state to bypass test failures (`INV-TROUBLE-AUTH-01`).
- Re-enrolling and wiping valid certificates when a simple `auth refresh` suffices (`INV-TROUBLE-AUTH-02`).
- Running `auth refresh` against a revoked certificate instead of clearing local credentials and logging in again (`INV-TROUBLE-AUTH-04`).
- Attempting to connect inbound to an outbound Operator, or configuring inbound firewall ports for an Operator (`INV-TROUBLE-OP-01`).
- Bypassing the L1–L5 pipeline when an envelope is rejected instead of diagnosing root causes from event receipts (`ztd audit event-receipts`, `INV-TROUBLE-OP-02`).
- Reusing a runtime root from an earlier release for the Gateway or an Operator, relying on a schema that is never migrated (`INV-STOR-SCHEMA-01` in [Developer Guidelines](devs.md)).

## Links out

- [Developer Guidelines](devs.md): coding invariants and repository standards.
- [Code Map](codemap.md): package and runtime ownership maps.
- [Testing Guide](tests.md): test tiers, fixtures, and CI scope.
- [Documentation Guide](docs.md): documentation audit, catalog, and formatting rules.
- [Release Process](release_process.md): versioning and release verification.
- [Getting Started](../guides/getting_started.md): platform onboarding and deployment procedures.
- [Glossary](../reference/glossary.md): canonical platform definitions and vocabulary.

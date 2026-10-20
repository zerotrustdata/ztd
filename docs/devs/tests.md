---
doc_id: tests
title: Testing Guide
audience: maintainers and coding agents
status: current
version: v1.1.0
last_updated: 2026-10-14
owners:
  - internal/cli/cmd/test/
  - internal/testutil/
  - Makefile
  - test/e2e/
  - test/fixtures/
related:
  - docs/devs/devs.md
  - docs/devs/codemap.md
  - docs/devs/docs.md
  - docs/devs/release_process.md
  - docs/devs/troubleshooting.md
when_to_read: Running tests, writing regression tests, adding fixtures, or updating test workflows.
do_not_use_for:
  - Coding standards and general invariants (docs/devs/devs.md)
  - Documentation audit and generation workflow (docs/devs/docs.md)
  - Release procedure and compliance bundles (docs/devs/release_process.md)
  - Diagnostic triage and recovery (docs/devs/troubleshooting.md)
  - Package and runtime ownership maps (docs/devs/codemap.md)
---

# Testing Guide

## Purpose

Defines the four-tier test model, execution entry points, fixture lifecycle, specialized test harnesses, and testing invariants for the ztd Go platform, Console, and protocol packages.

The platform enforces a four-tier test architecture:
- **Tier 1 (Unit)**: Fast, in-memory, hermetic unit tests that execute without a running platform, external databases, or network listeners. Parallel across packages.
- **Tier 2 (In-Process Integration)**: In-process integration tests exercising real local SQLite databases, local PKI generation, pub/sub, and the in-process Gateway with race detection on non-Windows hosts. Banned from calling `t.Parallel()`.
- **Tier 3 (Live Platform E2E)**: End-to-end tests exercising deployed service boundaries, including steady-state platform verification, multi-worker scale, and stateful enrollment topologies.
- **Tier 4 (External-Service Integration)**: Tests calling live external LLM providers or remote cloud services. These require dedicated opt-in targets and credentials and do not run in default CI. The repository has no general `ztd test` Tier 4 command. The dedicated live-provider gate is `ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>`; other Tier 4 tests use the owning package's documented target.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Test execution (`INV-TEST-RUN`)](#test-execution-inv-test-run)
  - [Test structure and isolation (`INV-TEST-ISO`)](#test-structure-and-isolation-inv-test-iso)
  - [Fixture lifecycle (`INV-TEST-FIX`)](#fixture-lifecycle-inv-test-fix)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Run Test Tiers](#run-test-tiers)
  - [Run Specialized Test Harnesses](#run-specialized-test-harnesses)
  - [Cross-Component Test Execution](#cross-component-test-execution)
  - [Onboarding Script Tests](#onboarding-script-tests)
  - [Add a Hermetic Command Test](#add-a-hermetic-command-test)
  - [Native Windows Tests](#native-windows-tests)
  - [Diagnose Slow Tests](#diagnose-slow-tests)
  - [Qualify Operator Deployment](#qualify-operator-deployment)
  - [Run the Release Smoke](#run-the-release-smoke)
  - [Profile Integration Tests](#profile-integration-tests)
  - [Scale-Test Cohorts](#scale-test-cohorts)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Test execution (`INV-TEST-RUN`)

| ID | Rule |
| :--- | :--- |
| INV-TEST-RUN-01 | Platform suites MUST run through `./ztd test ...` or root Makefile targets. MUST NOT invoke `go test` directly for platform suites (protocol package targets are the exception). |
| INV-TEST-RUN-02 | Tier 1 tests MUST stay independent of a running platform, network, or external databases. |
| INV-TEST-RUN-03 | Tier 2 and Tier 3 tests MUST exercise real local or deployed boundaries. MUST NOT mock internal services, database clients, or cross-component communication in those tiers. |
| INV-TEST-RUN-04 | Integration (Tier 2) and E2E (Tier 3) tests MUST NOT call `t.Parallel()`. |
| INV-TEST-RUN-05 | Tier 4 tests requiring external network access, live LLM endpoints, or cloud credentials MUST be isolated behind a dedicated gate and executed through an explicit target. Tiers 1 through 3 MUST NOT depend on external APIs or credentials. |

### Test structure and isolation (`INV-TEST-ISO`)

| ID | Rule |
| :--- | :--- |
| INV-TEST-ISO-01 | Tests MUST be table-driven where applicable and MUST use descriptive names stating the exact behavior verified. MUST NOT use generic test names like `TestCoverage`, `TestMisc`, or `TestEdgeCases`. |
| INV-TEST-ISO-02 | Tests MUST use `testutil.TempDir(t)` for isolated runtime roots and pass resulting base directories to `fs.NewRuntimeFileService` and `paths.InitWithBase`. MUST NOT append `.ztd` manually. |
| INV-TEST-ISO-03 | Tests MUST NOT use `os.Chdir` to align runtime state. `os.Chdir` is permitted only for behavior requiring directory discovery (e.g. Swagger source discovery), and MUST restore original working directories with `t.Cleanup`. |
| INV-TEST-ISO-04 | Tests MUST use typed constants from `internal/constants/` for statuses, paths, reason strings, and permissions instead of ad hoc string literals. |
| INV-TEST-ISO-05 | Tests discovering user configuration MUST isolate both `HOME` and `USERPROFILE` with `t.Setenv`; Go's `os.UserHomeDir` reads `USERPROFILE` on Windows. Browser approval tests MUST inject browser openers so automated suites do not launch real browsers. |
| INV-TEST-ISO-06 | `ztd test` runners MUST start test, smoke, and scale child processes without the registry `secret` and `user_endpoint` keys (`config.WithoutUserEnv`), so the developer's `.env` and shell credentials never reach a test process. Tests needing one of these keys MUST set it with `t.Setenv`. |

### Fixture lifecycle (`INV-TEST-FIX`)

| ID | Rule |
| :--- | :--- |
| INV-TEST-FIX-01 | `NewGatewayFixture` registers its own teardown via `t.Cleanup`. Callers MUST NOT close databases, stop the Gateway, or close downstream servers a second time. |
| INV-TEST-FIX-02 | Temporary credentials and fixture resources MUST register cleanup with `t.Cleanup`. Setup helpers MUST NOT use helper-local `defer` statements that run before test bodies. |
| INV-TEST-FIX-03 | Tests MUST provide explicit cancellation contexts and join background goroutines before test completion. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Test tiers and CLI commands | `internal/cli/cmd/test/test.go` | `./ztd test --help` |
| Chaos testing harness and summary | `internal/tools/chaos/`, `internal/cli/cmd/test/chaos.go` | `./ztd test chaos --help`, `./ztd test summary` |
| Public loop qualification harness | `internal/cli/cmd/test/public_loop.go` | `./ztd test public-loop --help` |
| Operator fleet scale qualification | `internal/cli/cmd/test/scale.go`, `test/e2e/operator_fleet_e2e_test.go` | `./ztd test scale --help`, `make test-scale` |
| Makefile platform test targets and coverage exclusions | `Makefile` (`TEST_PKGS`, `COVERAGE_EXCLUDE_PKGS`) | `make test`, `make test-unit`, `make test-integration`, `make test-enrollment-burst`, `make test-operator-burst`, `make test-coverage`, `make test-cross-enrollment`, `make test-scale`, `make test-airgap` |
| Platform CI scope | `.github/workflows/build-and-test.yml`, `.github/workflows/build-and-test-fips.yml` | Workflow job steps and `Makefile` CI targets |
| Cross-component test targets | `Makefile`, `console/`, `protocol/` | `make console-test`, `make ci-protocol` |
| Onboarding script tests | `scripts/test-onboarding.sh`, `scripts/onboarding/` | `make test-onboarding`, `make test-onboarding-docker` |
| Integration Gateway fixture | `test/fixtures/gateway_fixture.go` | `NewGatewayFixture` |
| Gateway-local client identity | `test/fixtures/cli_identity.go` | `BootstrapCLIIdentity` and `CreateCLIMTLSClient` |
| Production eval stack | `test/fixtures/eval_stack.go`, `test/fixtures/scripted_ollama.go` | `NewEvalStack`, `TestEvalStackUsesDistinctOutboundOperatorRoots`, `TestRunsExecute_RealStack` |
| File service test isolation | `internal/testutil/paths.go`, `internal/services/fs/file_service.go` | `testutil.TempDir` |
| Socket test tags and serial boundary tests | `internal/testutil/test_tiers_test.go` | `TestTestTiers_ListenersRequireIntegrationAndBoundaryTestsStaySerial` |
| Cross-platform port probes | `internal/netutil/port_probe_windows.go`, `internal/netutil/port_probe_windows_integration_test.go`, `internal/netutil/port_probe_other_integration_test.go` | `TestTCPPortProbe_BindsWithoutAcceptingConnectionsAndReleasesPort`, `TestTCPPortProbe_RejectsWildcardAndLoopbackConflictsAndReleasesPort` |
| Cross-process evaluation loop qualification | `internal/cli/cmd/test/eval_loop_e2e_test.go` | `./ztd test eval-loop --help` |
| Live Tier 4 evaluation loop | `internal/cli/cmd/test/eval_loop_live_e2e_test.go` | `./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>` |
| Live E2E suite | `test/e2e/` | `./ztd test e2e` |
| Completion transcript wire contract | `internal/services/gateway/platform_enrollment_validation_test.go` | `TestPlatformEnrollmentCompletionTranscriptGoldenVector` |

Gateway-local MCP/A2A tests use `BootstrapCLIIdentity` to enroll the owner through the real bootstrap endpoint and bind the CLI session to the certless embedded Operator that executes those calls. `EnrollClientIdentity` enrolls a remote Operator and its CLI identity; that remote Operator remains offline until it subscribes to its command channel. Enrollment persists its session synchronously and must not be tested by waiting for active command readiness.

Evaluation integration tests that execute assignments MUST use `fixtures.NewEvalStack`. It starts the production Gateway and separate outbound Data and Inference Operators with independent runtime roots and keyrings; only the Ollama provider is scripted. `OperatorWireWitness` records the enrolled certificates presented over mTLS. `ClientIdentity.InstallCLIRuntime` installs the bootstrap owner's certificate, sealed key, trust bundle and credentials into the test's CLI runtime. Tests MUST keep the production harness client, authentication loader, application enroller, binding client and campaign executor factories. Store-only tests use local campaign-controller records without a Gateway. Lifecycle tests consume the real CLI SSE stream; execution delays gate the scripted provider reply and keep the production executor factory. A scripted reply whose content depends on the run, such as a tool call naming the run's workspace path, uses `OllamaReply.Respond`, which receives the decoded provider-bound request; tests MUST NOT pre-compute such values or rewrite assertions to relative paths. Publication tests enable the real Gateway-owned public mirror through `EvalStackOptions.EnablePublicSpectator`.

For ratify and notary, the stack's owner answers the existing transaction-approval notifications through the mTLS pending-list and signed CLI approval HTTP routes. The app enroller still submits its own enrollment decision and waits for L3; the fixture does not replace the proof or resume handler. Notary uses the fixture's real Ed25519 consensus application. `ApprovalReceipts` exposes the returned receipts for assertions. `OwnerConsole.DenyToolTransactions(tool)` makes the owner sign a denial for one tool's transactions through the same route; enrollment and agent-turn inference are L3 transactions under ratify and stay approved. `EvalStackOptions.Provenance` adds a Provenance Operator over a real Ollama model store whose digest the scripted provider serves, so attestation windows come from the production path. These are in-process Tier 2 runtimes using the real network transport, not separate OS processes or live model evidence. Component fixtures own teardown through `t.Cleanup`; callers MUST NOT stop them again.

## Procedures

### Run Test Tiers

```bash
# Tier 1: Fast unit tests (no network or databases, parallel across packages)
./ztd test unit
make test-unit

# Narrow Tier 1 by package pattern or test name
./ztd test unit --pkg ./internal/cli/cmd/test --run TestTestUnitCmd

# Tier 2: In-process integration tests (local SQLite, PKI, pub/sub, race detector)
./ztd test integration
make test-integration

# Narrow Tier 2 by package pattern or test name
./ztd test integration --pkg ./internal/cli/cmd/gw --run TestGatewayConnect

# Show individual test names and durations while diagnosing a slow package
./ztd test integration -v --pkg ./internal/services/gateway

# Tier 3: Live platform E2E (against an already-running stack)
./ztd test e2e

# Tier 3: Specific scenario tests against a running stack
./ztd test e2e --run TestPlatformEnrollment_PendingDiscovery
./ztd test e2e --run TestCrossEnrollment
make test-cross-enrollment

# Enforce coverage threshold (75% minimum, atomic mode across integration suite)
./ztd test coverage
make test-coverage

# Narrow coverage run by package or verbosity
./ztd test coverage --pkg ./internal/services/auth --verbose

# Go lint only; Make also runs repository security and doctrine checks
./ztd test lint
# Full Makefile lint target: govulncheck, doctrine validation, temp-path check, golangci-lint
make lint
```

Unfiltered unit and integration commands delegate to `make test-unit` and `make test-integration`, so they share the Makefile package set and test settings. Supplying `--pkg` or `--run` invokes `go test` directly for the requested selection. `TEST_PKGS` is every Go package in the module, and `make test` and `make ci` run the same set; no package with tests is skipped locally. `COVERAGE_EXCLUDE_PKGS` in the root Makefile only removes packages from the coverage profile.

### Run Specialized Test Harnesses

```bash
# Run chaos testing: fire synthetic governance events directly in-process
./ztd test chaos --count 100

# View aggregated chaos test results from test vault database
./ztd test summary

# Run provider-free public feed qualification loop
./ztd test public-loop --candidate candidate.json --output evidence.json

# Run cross-process evaluation loop qualification (real Gateway, Data and Inference processes under ratify and notary)
./ztd test eval-loop
./ztd test eval-loop --posture ratify
./ztd test eval-loop --posture notary

# Tier 4 live-provider gate (explicit only; the provider is reached through the governed Inference Operator)
./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name> [--posture ratify|notary]

# Verify air-gapped vendored compilation without network access
make test-airgap

# Operator fleet scale qualification (isolated Gateway, N real Operator processes)
./ztd test scale --count 100
make test-scale SCALE_COUNT=1000 SCALE_ARGS='--soak 10m --fan-out-concurrency 64,all --root .local.dev/scale/run-1'
```

`./ztd test eval-loop` qualifies the full cross-process evaluation pipeline against live OS processes under both `ratify` and `notary` governance postures. The runner starts isolated Gateway, Data Operator, and Inference Operator processes under distinct `.ztd/` roots via `RuntimeFileService`, deploys them via `ztd operator deploy --approve`, handles L3 human authorizations through an event-driven `ztd auth approve --cli` responder driven entirely by the Gateway's SSE stream (`ztd.v1.platform.approvals.changed`), exercises real Ed25519 K-of-N consensus deliberation under `notary`, and verifies authoritative local execution receipts directly from each Operator's own SQLite store (LFAA).

`./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>` is the dedicated Tier 4 gate under `INV-TEST-RUN-05`. It builds with the `e2e,live` tags, runs `TestEvalLoopLive` under a default timeout of 40m per selected posture (each posture's run takes what remains of that deadline, less the cleanup margin), and replaces only the provider: the Inference Operator reaches that endpoint through the governed Inference Operator, and no provider is scripted. Each posture freezes the provider's real model digest with `ztd eval models freeze` and runs one Tribunal campaign. The gate proves the loop and its evidence, not model quality, so a FAIL verdict passes. A subtest that fails has its run root under `.ztd/operators/` deleted; a passing run root is kept.

`./ztd test chaos` generates synthetic governance events (70% valid intent, 20% L1 forbidden command injection, 10% corrupted transaction hash MitM) directly in-process through `TransactionVerifier` and `Actuator`, bypassing network and TLS layers. Results persist to SQLite databases under `.ztd/test-vault/<timestamp>-chaos-test/`. `./ztd test summary` aggregates outcomes across runs in the test vault directory.

`./ztd test scale` runs fleet scale tests in an isolated Gateway listening on reserved ephemeral ports, avoiding conflicts with running production Gateways. The runner:
1. Starts a doctrine Gateway in `<root>/run` with public spectator and evaluation explorer disabled.
2. Deploys `--count` Operators via `operator deploy`.
3. Runs `TestOperatorFleet_HoldsUnderFanOut`: enrollment, heartbeats, idle soak, and governed fan-out.
4. Runs `TestOperatorFleet_RecoversAfterGatewayRestart`: restart recovery, heartbeat resume, and fan-out verification.
5. Performs complete cleanup: stops workers and Gateway, retaining logs and metrics under `<root>/out`.

### Cross-Component Test Execution

The frontend targets need their npm dependencies. On a fresh clone, run `make dev-node` (protocol/node, console, ztd-adapter, website) and `npm ci --prefix evaluation-explorer` first; the full setup script installs both.

```bash
# Console (TypeScript): Vitest unit and component tests
make console-test

# Console: linting, typechecking, and embedded asset verification
make console-lint
make console-embed-check

# Evaluation explorer (TypeScript): Vitest unit and component tests, linting, typechecking
make explorer-test
make explorer-lint

# Protocol: Go parity test and Node TypeScript checks
make ci-protocol

# Full local CI pipeline across all components (stages run in sequence)
make ci
```

`make ci` refreshes generated artifacts as it runs. GitHub Actions separately checks generated-file freshness. Checks that GitHub Actions runs on Linux are Makefile targets shared with `make ci`, so the local gate cannot drift from CI: `verify-static` (standard and FIPS binaries), `fips-verify`, `build-windows`, `test-go-import`, `test-onboarding`, and `website-bundle-check`. `make ci` does not run the jobs that need a host it does not own: the Docker distro matrix (`make test-onboarding-docker`), the fresh-host contributor setup (`scripts/ci/onboarding-smoke.sh`), the Operator deployment lifecycle (`scripts/ci/operator-deploy-lifecycle.sh`, which needs ports 8080 and 8443 free), the FIPS-mode reruns of the unit and integration suites, and the native macOS and Windows jobs. `make test-onboarding` covers the host assumptions those jobs exercise offline: runtime and website goals parse with no Go on `PATH`, and developer tools install with the `go.mod` toolchain under an older system Go. The main CI workflow runs the full unit and integration suites on Linux and macOS; Windows runs the full unit suite and targeted integration tests for `internal/services/keystore/...`, `internal/cli/cmd/operator`, and `internal/cli/cmd/test`. The FIPS workflow runs the unit and integration Makefile targets with the FIPS Go module enabled. These workflows do not run the live E2E suite or fleet scale qualification as part of the standard test suite.

### Onboarding Script Tests

```bash
make test-onboarding          # offline setup-library and quickstart tests (scripts/test-onboarding.sh)
make test-onboarding-docker   # replays working tree on fresh distro hosts (scripts/onboarding/run-matrix.sh)
```

### Add a Hermetic Command Test

1. Use `cmdtest.NewCmdTestEnv(t)` from `internal/cli/cmd/cmdtest/` to obtain an isolated `RuntimeFileService` and matching configuration.
2. Inject `cmdtest.ConfigLoaderFor(cfg)`, client factories, and file-service factories into the command constructor under test.
3. For file-service error paths, add a test case in `internal/cli/cmd/<group>/factory_error_<group>_test.go` using `cmdtest.FailingFileSvcFactory(errFactory)` to verify that `constants.ErrFileServiceInit` is wrapped and downstream calls are skipped.

Hermetic command tests use isolated file fixtures. `testutil.TempDir` returns a fresh absolute directory under the repository's `.ztd/tmp/` (INV-FS-11), removed by `t.Cleanup`; `t.TempDir` and `os.TempDir` are rejected by the `forbidigo` lint. Everything under `.ztd/tmp/` is protected runtime state to `security.IsProtectedRuntimePath`, so tests that treat the directory as an Operator root agents may edit (`FILE_EDIT`, `RESTORE_FILE`, `OpenFileEditTarget`) use `testutil.OperatorDir(t)`, which lives under `.ztd/operators/`. Pass base directories directly to runtime file services.

### Native Windows Tests

Run root Makefile targets from PowerShell with Git for Windows Bash and GNU Make. Windows omits `-race` (`TEST_RACE`). File mode assertions use `testutil.FileMode`. `testutil.Symlink` skips fixtures only when Windows denies symlink privileges.

Isolate `HOME`, `USERPROFILE`, `APPDATA`, and `LOCALAPPDATA` with `t.Setenv` before constructing commands or services. Port probes use `netutil.CheckTCPPortAvailable`, which binds wildcard sockets without listening, avoiding Windows firewall prompts.

### Diagnose Slow Tests

Use `-v` with `./ztd test integration` to stream individual test names and durations from `go test` while packages execute.

The enrollment burst deadline contract (`TestPlatformEnrollmentBurst`) runs only without race or coverage instrumentation; execute with `make test-enrollment-burst`. The Operator transport burst (`TestOperatorTransportBoundary_BurstDeliversEveryCommittedIdentity`) enrolls 2000 in-process Operators; run with `make test-operator-burst`.

### Qualify Operator Deployment

`TestHandleInternalSSEStream_FirstFlushCanDeliverDeploymentAnnouncements` proves listeners exist before HTTP 200, write deadlines are cleared, announcements arrive once, and cancellation unregisters listeners.

On Linux, `bash scripts/ci/operator-deploy-lifecycle.sh --count 3` starts a fresh doctrine Gateway and invokes `TestOperatorDeploy_LocalLifecycle` through `ztd test e2e`.

### Run the Release Smoke

Build candidate binaries with `make build`, then execute `./ztd test release-smoke --root .ztd/operators/release-smoke-<unique-name>`. The run contains mutable Gateway and Operator working trees, so it must live outside protected runtime scratch paths such as `.ztd/tmp/`; the CLI rejects those roots before startup. The runner owns an isolated Gateway and data Operator, exercises governed `FILE_EDIT` mutations with exact local read-back and receipts, tests embedded governed document operations, and exports `report all` for both runtime roots. Acceptance requires zero `FAIL` and zero `SKIPPED` rows.

### Profile Integration Tests

```bash
# Capture JSON test events for duration analysis
GOFLAGS=-json ./ztd test integration --pkg ./internal/services/gateway > .ztd/tmp/ztd-tests.jsonl

# Profile CPU usage for specific package tests
GOFLAGS=-cpuprofile=$PWD/.ztd/tmp/ztd-gateway.cpu ./ztd test integration --pkg ./internal/services/gateway --run '^TestHandleBootstrap'
go tool pprof -top -cum .ztd/tmp/ztd-gateway.cpu
```

Use `testing/synctest` for self-contained timer, timeout, and channel synchronization tests. Do not wrap `testing/synctest` around real network listeners or subprocesses.

### Scale-Test Cohorts

`./ztd test scale` configures fleet scenario inputs (`ZTD_E2E_RUNTIME_ROOT`, `ZTD_E2E_GATEWAY_HTTP_PORT`, `ZTD_E2E_GATEWAY_HTTPS_PORT`, `ZTD_E2E_FLEET_*`). `TestOperatorFleet_HoldsUnderFanOut` accepts `ZTD_E2E_FLEET_SESSIONS` (comma-separated session UUIDs matching `ZTD_E2E_FLEET_SIZE`). When specified, registry health checks and CLI fan-out target exactly those remote sessions.

## Anti-patterns

- Calling `t.Parallel()` in Tier 2 or Tier 3 tests (`INV-TEST-RUN-04`).
- Invoking `go test` directly for platform suites instead of `./ztd test` or `make` (`INV-TEST-RUN-01`).
- Mocking databases or internal services in integration suites (`INV-TEST-RUN-03`).
- Calling external network APIs or requiring live credentials during Tier 1, 2, or 3 test execution (`INV-TEST-RUN-02`, `INV-TEST-RUN-05`).
- Calling `os.Chdir` without restoring the original directory via `t.Cleanup` (`INV-TEST-ISO-03`).
- Double-closing resources managed by `NewGatewayFixture` (`INV-TEST-FIX-01`).
- Leaving temporary directories or background goroutines unjoined when writing integration tests (`INV-TEST-FIX-02`, `INV-TEST-FIX-03`).
- Asserting a panic on production code paths instead of verifying typed returned errors with `assert.ErrorIs` against `internal/constants/errors.go` (`INV-CODE-06`, `INV-ERR-01`).

## Links out

- [Developer Guidelines](devs.md): coding invariants and repository standards.
- [Code Map](codemap.md): package and runtime ownership maps.
- [Documentation Guide](docs.md): documentation audit, catalog, and formatting standards.
- [Release Process](release_process.md): native evaluation acceptance and release verification.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [Console Architecture & Development](../architecture/console.md#running-tests): Vitest, typecheck, ESLint, and embed checks.

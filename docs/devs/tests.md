---
doc_id: tests
title: Testing Guide
audience: maintainers and coding agents
status: current
version: v1.5.0
last_updated: 2026-10-28
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
- **Tier 3 (Live Platform E2E)**: End-to-end tests exercising deployed service boundaries, including steady-state platform verification, multi-Operator scale, and stateful enrollment topologies.
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
  - [Run the Full Suite](#run-the-full-suite)
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
  - [Write a behavior test](#write-a-behavior-test)
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
| INV-TEST-ISO-07 | A test MUST assert an observable result of the production function under test: a returned value, a typed error (`require.ErrorIs` / `assert.ErrorIs`), a persisted row, or a published message. MUST NOT pass by ignoring that function's error, by asserting only that the call did not panic, or by comparing a value to itself. A compile-time interface check MUST be a package-level `var _ Interface = Impl{}` assignment, not a `Test` function. |

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
| Makefile platform test targets and coverage exclusions | `Makefile` (`TEST_PKGS`, `COVERAGE_EXCLUDE_PKGS`) | `make test`, `make test-unit`, `make test-integration`, `make test-eval-stack`, `make test-enrollment-burst`, `make test-operator-burst`, `make test-coverage`, `make test-cross-enrollment`, `make test-scale`, `make test-airgap` |
| Platform CI scope | `.github/workflows/build-and-test.yml`, `.github/workflows/build-and-test-fips.yml` | Workflow job steps and `Makefile` CI targets |
| Cross-component test targets | `Makefile`, `console/`, `protocol/` | `make console-test`, `make ci-protocol` |
| Onboarding script tests | `scripts/test-onboarding.sh`, `scripts/onboarding/` | `make test-onboarding`, `make test-onboarding-docker` |
| Integration Gateway fixture | `test/fixtures/gateway_fixture.go` | `NewGatewayFixture` |
| Gateway-local client identity | `test/fixtures/cli_identity.go` | `BootstrapCLIIdentity` and `CreateCLIMTLSClient` |
| Production eval stack | `test/fixtures/eval_stack.go`, `test/fixtures/eval_stack_gate*.go`, `test/fixtures/scripted_ollama.go` | `NewEvalStack`, `TestEvalStackUsesDistinctOutboundOperatorRoots`, `TestRunsExecute_RealStack` |
| File service test isolation | `internal/testutil/paths.go`, `internal/services/fs/file_service.go` | `testutil.TempDir` |
| Socket test tags and serial boundary tests | `internal/testutil/test_tiers_test.go` | `TestTestTiers_ListenersRequireIntegrationAndBoundaryTestsStaySerial` |
| Cross-platform port probes | `internal/netutil/port_probe_windows.go`, `internal/netutil/port_probe_windows_integration_test.go`, `internal/netutil/port_probe_other_integration_test.go` | `TestTCPPortProbe_BindsWithoutAcceptingConnectionsAndReleasesPort`, `TestTCPPortProbe_RejectsIPv4AndIPv6LoopbackConflicts`, `TestTCPPortProbe_RejectsWildcardAndLoopbackConflictsAndReleasesPort` |
| Cross-process evaluation loop qualification | `internal/cli/cmd/test/eval_loop_e2e_test.go` | `./ztd test eval-loop --help` |
| Cross-process conformance loop qualification | `internal/cli/cmd/test/conformance_loop.go`, `internal/cli/cmd/test/conformance_loop_e2e_test.go` | `./ztd test conformance-loop --help` |
| Isolated governed release smoke | `internal/cli/cmd/test/release_smoke.go`, `internal/cli/cmd/test/release_smoke_test.go` | `./ztd test release-smoke --help` |
| Live Tier 4 evaluation loop | `internal/cli/cmd/test/eval_loop_live_e2e_test.go` | `./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>` |
| Live E2E suite | `test/e2e/` | `./ztd test e2e` |
| Completion transcript wire contract | `internal/services/gateway/platform_enrollment_validation_test.go` | `TestPlatformEnrollmentCompletionTranscriptGoldenVector` |

Gateway-local MCP/A2A tests use `BootstrapCLIIdentity` to enroll the owner through the real bootstrap endpoint and bind the CLISession to the certless embedded Operator that executes those calls. `EnrollClientIdentity` enrolls a remote Operator and its CLI identity; that remote Operator remains offline until it subscribes to its command channel. Enrollment persists its session synchronously and must not be tested by waiting for active command readiness.

Evaluation integration tests that execute assignments MUST use `fixtures.NewEvalStack`. It starts the production Gateway and separate outbound Data and Inference Operators with independent runtime roots and keyrings; only the Ollama provider is scripted. `OperatorWireWitness` records the enrolled certificates presented over mTLS. `ClientIdentity.InstallCLIRuntime` installs the bootstrap owner's certificate, sealed key, trust bundle and credentials into the test's CLI runtime. Tests MUST keep the production harness client, authentication loader, application enroller, binding client and campaign executor factories. Store-only tests use local campaign-controller records without a Gateway. Lifecycle tests consume the real CLI SSE stream; execution delays gate the scripted provider reply and keep the production executor factory. A scripted reply whose content depends on the run, such as a tool call naming the run's workspace path, uses `OllamaReply.Respond`, which receives the decoded provider-bound request; tests MUST NOT pre-compute such values or rewrite assertions to relative paths. Publication tests enable the real Gateway-owned public mirror through `EvalStackOptions.EnablePublicSpectator`. `NewEvalStack` skips its test unless the `evalstack` build tag is set, so CI's `make test-integration` never runs real eval stacks; run them with `./ztd test integration --eval-stack` (`make test-eval-stack`, 30m per package) or narrow with `--eval-stack --pkg <pkg> --run <regexp>`. A package that adds a `NewEvalStack` caller MUST be listed in `EVAL_STACK_PKGS` in the `Makefile`.

For ratify and notary, the stack's owner answers the existing transaction-approval notifications through the mTLS pending-list and signed CLI approval HTTP routes. The app enroller still submits its own enrollment decision and waits for L3; the fixture does not replace the proof or resume handler. Approval responders, including the cross-process loop's CLI responder, reconcile concurrently so a Tribunal admission review can obtain its own governed inference approval while the parent command's approval waits for that review. Cleanup cancels and joins all responder workers. Notary uses the fixture's real Ed25519 consensus application. `ApprovalReceipts` exposes the returned receipts for assertions. `OwnerConsole.DenyToolTransactions(tool)` makes the owner sign a denial for one tool's transactions through the same route; enrollment and agent-turn inference are L3 transactions under ratify and stay approved. `EvalStackOptions.Provenance` adds a Provenance Operator over a real Ollama model store whose digest the scripted provider serves, so attestation windows come from the production path. These are in-process Tier 2 runtimes using the real network transport, not separate OS processes or live model evidence. An Operator runs governed `ztd operator model <verb>` commands by executing its own binary, never a `ztd` found on `PATH`. In-process, that binary is the Go test binary, so a package whose stack runs model commands MUST answer them from `TestMain` (see `internal/cli/cmd/eval/main_integration_test.go`). Component fixtures own teardown through `t.Cleanup`; callers MUST NOT stop them again.

## Procedures

### Run the Full Suite

`make test` runs the full suite that does not require reconfiguring your current Gateway. It formats Go sources first, then sequences the groups below, a 100-Operator fleet smoke, and the Docker onboarding matrix, including under `make -j`. Failures stop the aggregate and return nonzero. Tests that use the current Gateway or a selected live provider are explicit targets.

The full suite requires a Linux host capable of running the pinned FIPS binary, a running Docker daemon, the development tools checked by `make dev-check`, Python 3, and Playwright Chromium with its system dependencies. The existing isolated deployment lifecycle needs ports 8080 and 8443 free and refuses to stop an existing Gateway. Install workspace dependencies with `make dev-node`, and install Chromium with `cd evaluation-explorer && npx playwright install --with-deps chromium`. Missing prerequisites fail their owning gate.

| Target | Scope |
| :--- | :--- |
| `test-quality` | `fmt`, Go lint, vulnerability checks, doctrine validation, license headers, version sync, constants/catalog/notices drift, frontend/protocol typechecks and lint, adapter contract pack, frontend embeds, and website bundle validation |
| `test-platform` | All Go packages under unit and integration settings, pinned FIPS unit/integration reruns and binary self-check, real eval stacks, coverage threshold, all enrollment burst variants, uninstrumented Operator transport burst, vendored build, and external Go import |
| `test-components` | Console, explorer, adapter, Node protocol, website, demo, and Claude plugin tests; explorer browser tests against development and production bundles; Go hash parity |
| `test-scripts` | Release scripts, commit hygiene, and hermetic onboarding tests |
| `test-process` | Cross-process eval under ratify/notary, conformance under all four postures, governed release smoke, and Linux Operator deployment lifecycle |
| `test-scale` | Fleet fan-out/soak and Gateway restart recovery with 100 real Operators by default |
| `test-onboarding-docker` | Fresh-host build/rerun/quickstart/contributor/release-smoke scenarios across the Docker distro matrix in the full suite |

The fleet smoke defaults to 100 Operators. `SCALE_COUNT` overrides that cohort for both `make test` and `make test-scale`; for example, `make test-scale SCALE_COUNT=1000` selects a larger qualification run. The separate in-process Operator transport smoke also uses 100 Operators. `make test-enrollment-burst` selects the timing, session-validation, and deployment-event variants, each using 1,000 enrollment requests against an isolated test Gateway.

`make test-e2e` warns that it uses and mutates your current Gateway, then delegates to `./ztd test e2e`. Select a topology with `E2E_RUN` and additional test flags with `E2E_ARGS`. Prepare the required Operator/enrollment state before running the selection. `make test-cross-enrollment` selects the cross-enrollment tests against a prepared topology.

`make test-e2e-reset E2E_POSTURE=doctrine` warns, invokes `./ztd gw clean` with its existing destructive confirmation and evidence-backup offer, starts the Gateway with the selected posture, logs in headlessly, and runs Gateway health and owner-authentication E2E tests. Override `E2E_RUN` to choose another selection. Declining the confirmation stops the target before cleanup or startup. The reset creates a new trust domain. Tests requiring additional Operators or enrollment state use `test-e2e` after that topology is prepared. The target does not pass a force flag or add another confirmation mechanism.

`make test-eval-live` requires `LIVE_OLLAMA_ENDPOINT` and `LIVE_MODEL` and runs the Tier 4 provider qualification under ratify and notary. The existing eval-loop, conformance-loop, release-smoke, and scale runners own isolated Gateways, so their posture changes and cleanup stay within their own runtimes and remain in `make test`.

`make test-onboarding-smoke` names the fresh-checkout contributor setup and release smoke, intended for a disposable host. It refuses an existing runtime or `.env`; the Docker onboarding matrix covers the contributor and release-smoke scenarios in the full suite.

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

# Real eval stack tests (evalstack tag; skipped in CI)
./ztd test integration --eval-stack
make test-eval-stack
./ztd test integration --eval-stack --pkg ./internal/cli/cmd/eval --run '^TestPublicationRealStack_'

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

For the Tier 4 live-provider gate, the endpoint URL and model name are passed only through `--live-ollama-endpoint` and `--live-model`; the commands below read them from shell variables named `LIVE_OLLAMA_ENDPOINT` and `LIVE_MODEL` that you set yourself. `ztd` does not read those variables.

```bash
# Run chaos testing: fire synthetic governance events directly in-process
./ztd test chaos --count 100

# View results of the most recent chaos run in the test vault database
./ztd test summary

# Run provider-free public feed qualification loop
./ztd test public-loop --candidate candidate.json --output evidence.json

# Run cross-process evaluation loop qualification (real Gateway, Data and Inference processes under ratify and notary)
./ztd test eval-loop
./ztd test eval-loop --posture ratify
./ztd test eval-loop --posture notary

# Run cross-process conformance loop qualification (every posture by default)
./ztd test conformance-loop
./ztd test conformance-loop --posture ratify

# Tier 4 live-provider gate (explicit only; the provider is reached through the governed Inference Operator)
./ztd test eval-loop --live-ollama-endpoint "$LIVE_OLLAMA_ENDPOINT" --live-model "$LIVE_MODEL"
./ztd test eval-loop --live-ollama-endpoint "$LIVE_OLLAMA_ENDPOINT" --live-model "$LIVE_MODEL" --posture ratify
./ztd test eval-loop --live-ollama-endpoint "$LIVE_OLLAMA_ENDPOINT" --live-model "$LIVE_MODEL" --posture notary

# Verify air-gapped vendored compilation without network access
make test-airgap

# Operator fleet scale qualification (isolated Gateway, N real Operator processes)
./ztd test scale --count 100
make test-scale SCALE_COUNT=1000 SCALE_ARGS='--soak 10m --fan-out-concurrency 64,all --root .local.dev/scale/run-1'
```

`./ztd test eval-loop` qualifies the full cross-process evaluation pipeline against live OS processes under both `ratify` and `notary` governance postures. The runner starts isolated Gateway, Data Operator, and Inference Operator processes under distinct `.ztd/` roots via `RuntimeFileService`, deploys them via `ztd operator deploy --approve`, handles L3 human authorizations through an event-driven `ztd auth approve --cli` responder driven entirely by the Gateway's SSE stream (`ztd.v1.platform.approvals.changed`), exercises real Ed25519 K-of-N consensus deliberation under `notary`, and verifies authoritative local event receipts directly from each Operator's own SQLite store (LFEA). The runner executes the repository-root `./ztd` binary, so run `make build` first. Before the campaigns run, the scripted gate freezes the scripted provider's inventory with `ztd eval models freeze` against the isolated Gateway; the freeze replaces the Gateway's model registry that the campaigns bind. The frozen digest of the scripted model MUST equal the digest the scripted provider serves, and a mismatch fails the run.

`./ztd test conformance-loop` qualifies `ztd eval conformance run` against live OS processes under each selected posture (`doctrine`, `consensus`, `ratify`, and `notary` by default). For each posture the runner starts an isolated Gateway with A2A ingress, enrolls the CLI owner, binds the real consensus application under `consensus` and `notary`, deploys a dedicated Data Operator, and drives `ztd mcp stdio` as an agent's MCP client does. It runs the conformance suite once against the Data Operator and once against the embedded Operator. Each run MUST return one PASS verdict for every applicable case and a PASS suite verdict, and `ztd eval conformance verify` MUST reproduce the run's verdicts offline. Under `ratify` and `notary` the runs are attended: the CLI owner follows the Gateway's SSE stream and answers each suspended case through the approval flow. An unattended mutation stays suspended, an attended-approve case is approved with `ztd auth approve --cli`, and an attended-deny case is denied with `ztd auth approve --deny`. A suspension the catalogue does not predict fails the run, as does a predicted suspension that never arrives. The audit chain of both Operator sessions, read through the stdio bridge with `event_receipt_list`, MUST advance with completed receipts. Both runtimes' `report all` evidence MUST verify. The runner executes the repository-root `./ztd` binary, so run `make build` first. Evidence and private command logs remain under `.ztd/operators/`.

`./ztd test eval-loop --live-ollama-endpoint <url> --live-model <name>` is the dedicated Tier 4 gate under `INV-TEST-RUN-05`. It builds with the `e2e,live` tags, runs `TestEvalLoopLive` under a default timeout of 40m per selected posture (each posture's run takes what remains of that deadline, less the cleanup margin), and replaces only the provider: the Inference Operator reaches that endpoint through the governed Inference Operator, and no provider is scripted. Each posture freezes the provider's real model digest with `ztd eval models freeze` and runs one Tribunal campaign. The gate proves the loop and its evidence, not model quality, so a FAIL verdict passes. A subtest that fails has its run root under `.ztd/operators/` deleted; a passing run root is kept.

`./ztd test chaos` generates synthetic governance events (60% `GOOD_ACTOR` safe `FS_LIST`, 20% `PROMPT_INJECTION` forbidden `EXECUTE_BASH` rejected at L1, 10% `MITM` corrupted transaction hash, 10% `FILE_MUTATION` `FILE_EDIT` carrying an L3 proof) directly in-process through `L4Warden` and `L5Actuator`, bypassing network and TLS layers. Results persist to SQLite databases under `.ztd/test-vault/<timestamp>-chaos-test/`. `./ztd test summary` reads only the most recent `*-chaos-test` run in the test vault directory, not every run.

`./ztd test scale` runs fleet scale tests in an isolated Gateway listening on reserved ephemeral ports, avoiding conflicts with running production Gateways. The runner:
1. Starts a doctrine Gateway in `<root>/run` with the public spectator disabled (`--public-spectator=false`).
2. Deploys `--count` Operators via `operator deploy`.
3. Runs `TestOperatorFleet_HoldsUnderFanOut`: enrollment, heartbeats, idle soak, and governed fan-out.
4. Runs `TestOperatorFleet_RecoversAfterGatewayRestart`: restart recovery, heartbeat resume, and fan-out verification.
5. Performs complete cleanup: stops the run's Operators and Gateway, retaining logs and metrics under `<root>/out`.

### Cross-Component Test Execution

The frontend targets need their npm dependencies. On a fresh clone, run `make dev-node` to install dependencies for protocol/node, console, evaluation-explorer, ztd-adapter, and website.

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

`make ci` refreshes generated artifacts as it runs. GitHub Actions separately checks generated-file freshness. The local gate includes Linux standard and FIPS binary checks, Windows cross-compilation, the standard unit and integration suites with coverage, enrollment bursts, FIPS unit and integration reruns on Linux, downstream Go imports, and the frontend, protocol, website, and script checks.

`ci-onboarding`, included in `make ci`, requires a running Docker daemon. It runs `test-onboarding-docker` across Ubuntu, Debian, Fedora, and Arch for build-only setup, a fresh-terminal rerun, and quickstart. A separate Ubuntu container runs build-only setup followed by contributor setup, notice and env-file checks, governed release smoke, and the real three-Operator deployment lifecycle. GitHub Actions does not run the fresh-host onboarding jobs; they run locally only. This gives setup and process tests disposable hosts without changing the contributor's toolchain or occupying their Gateway ports. Missing Docker fails the gate. Logs and evidence remain under `.local.dev/onboarding/`.

Native macOS Keychain and Windows DPAPI/process behavior still require those hosts. The main workflow runs full unit and integration suites on Linux and macOS; Windows runs the full unit suite and selected integration packages. Live-provider E2E and fleet scale qualification remain explicit targets.

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

`TestPublicMirror_ScalingIntegration` compares ingest into mirrors retaining 500 and 5,000 batches and proof artifacts. It enforces bounded allocation, equal row-mutation counts, and bounded database growth; p95 latency ratios are diagnostic because shared-runner scheduling, filesystem sync, and race instrumentation affect elapsed time. The fixture uses one persistent SQLite connection so `total_changes()` counts every ingest transaction, and requires successful WAL checkpoints before comparing disk sizes. Run it with `./ztd test integration -v --pkg ./internal/services/gateway --run '^TestPublicMirror_ScalingIntegration$'`.

The enrollment burst deadline contract (`TestPlatformEnrollmentBurst`) runs only without race or coverage instrumentation; execute with `make test-enrollment-burst`. The Operator transport smoke (`TestOperatorTransportBoundary_BurstDeliversEveryCommittedIdentity`) enrolls 100 in-process Operators; run with `make test-operator-burst`.

### Qualify Operator Deployment

`TestHandleInternalSSEStream_FirstFlushCanDeliverDeploymentAnnouncements` proves listeners exist before HTTP 200, write deadlines are cleared, announcements arrive once, and cancellation unregisters listeners.

On Linux, `make test-operator-deploy` starts a fresh doctrine Gateway and invokes `TestOperatorDeploy_LocalLifecycle` through `ztd test e2e`. When another Gateway occupies the default ports, select two unused ports with `OPERATOR_DEPLOY_ARGS='--http-port 18080 --https-port 18443'`. The runner checks those ports before starting and passes them to enrollment and the E2E scenario; it preserves other Gateways.

### Run the Release Smoke

Build `ztd` with `make build`, then run `./ztd test release-smoke`; the runner's options, scenarios, PASS gating checks, and retained artifacts are documented in [Host-Native Headless UX Smoke Test](../guides/ux_smoke_test.md).

### Profile Integration Tests

Use `./ztd test integration -v --pkg <pkg> --run <regexp>` to stream individual test names and durations (see Diagnose Slow Tests). Use `testing/synctest` for self-contained timer, timeout, and channel synchronization tests. Do not wrap `testing/synctest` around real network listeners or subprocesses.

### Scale-Test Cohorts

`./ztd test scale` configures fleet scenario inputs (`ZTD_E2E_RUNTIME_ROOT`, `ZTD_E2E_GATEWAY_HTTP_PORT`, `ZTD_E2E_GATEWAY_HTTPS_PORT`, `ZTD_E2E_FLEET_*`). `TestOperatorFleet_HoldsUnderFanOut` accepts `ZTD_E2E_FLEET_SESSIONS` (comma-separated session UUIDs matching `ZTD_E2E_FLEET_SIZE`). When specified, registry health checks and CLI fan-out target exactly those remote sessions.

### Write a behavior test

Tier 1 tests are table-driven when one function has several inputs. Each case uses `t.Run` with a name that states the behavior. `require` stops the test when the next line would be meaningless (construction, I/O, a sentinel that must hold). `assert` checks the remaining observations so one failure does not hide the next. The repository standard is `github.com/stretchr/testify`; do not add a second assertion library. Reach for the standard `testing` helpers when the assertion is a single `t.Fatal`.

Helpers that take `*testing.T` or `testing.TB` call `t.Helper()` before any other test API. Shared constructors for real stores, vaults, and ledgers live in `internal/services/storage/storagetest` and `internal/testutil`. A new fixture extends those packages. It does not copy a constructor into the test file, and it does not replace the production type with a fake of that same type.

Tests that perform I/O take `testutil.Context(t)` (or `testutil.ContextWithTimeout` when the budget is not 30 seconds). The context is cancelled when the test returns. `t.Cleanup` owns teardown. `testutil.TempDir` and `testutil.OperatorDir` own scratch directories. `t.Parallel` is for Tier 1 only.

Log output is asserted through `testutil.NewBufferLogger`. A call that only logs is not proven by `assert.NotPanics`.

Golden files belong in a package `testdata/` directory when the assertion is a stable byte-for-byte contract. Do not add a golden file for a value the test can check with a typed field.

## Anti-patterns

- Calling `t.Parallel()` in Tier 2 or Tier 3 tests (`INV-TEST-RUN-04`).
- Invoking `go test` directly for platform suites instead of `./ztd test` or `make` (`INV-TEST-RUN-01`).
- Mocking databases or internal services in integration suites (`INV-TEST-RUN-03`).
- Calling external network APIs or requiring live credentials during Tier 1, 2, or 3 test execution (`INV-TEST-RUN-02`, `INV-TEST-RUN-05`).
- Calling `os.Chdir` without restoring the original directory via `t.Cleanup` (`INV-TEST-ISO-03`).
- Double-closing resources managed by `NewGatewayFixture` (`INV-TEST-FIX-01`).
- Leaving temporary directories or background goroutines unjoined when writing integration tests (`INV-TEST-FIX-02`, `INV-TEST-FIX-03`).
- Asserting a panic on production code paths instead of verifying typed returned errors with `assert.ErrorIs` against `internal/constants/errors.go` (`INV-CODE-06`, `INV-ERR-01`).
- Ignoring the error returned by the function under test, or treating "it did not panic" as the assertion (`INV-TEST-ISO-07`).
- Round-tripping a plain struct through `encoding/json` and calling that a test of a store or service (`INV-TEST-ISO-07`).
- Dressing a compile-time interface check up as a `Test` function (`INV-TEST-ISO-07`).

## Links out

- [Developer Guidelines](devs.md): coding invariants and repository standards.
- [Code Map](codemap.md): package and runtime ownership maps.
- [Documentation Guide](docs.md): documentation audit, catalog, and formatting standards.
- [Release Process](release_process.md): native evaluation acceptance and release verification.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [Console Architecture & Development](../architecture/console.md#running-tests): Vitest, typecheck, ESLint, and embed checks.

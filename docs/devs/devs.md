---
doc_id: devs
title: Developer Guidelines
audience: maintainers and coding agents
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - go.mod
  - Makefile
  - internal/cli/cmd/main.go
  - internal/cli/cmd/test/test.go
  - internal/cli/config/config.go
  - internal/constants/errors.go
  - internal/constants/paths.go
  - internal/services/fs/file_service.go
  - internal/services/ztdo.go
  - internal/services/pubsub/mode_deps.go
  - internal/services/pubsub/pubsub_commands.go
  - internal/services/gateway/gateway_service.go
  - internal/services/gateway/embedded/operator.go
  - internal/services/governance/l1_doctrine.go
  - internal/services/mcp/registry.go
  - internal/services/mcp/native_tool_registry.go
related:
  - docs/devs/codemap.md
  - docs/devs/tests.md
  - docs/devs/docs.md
  - docs/devs/release_process.md
  - docs/devs/troubleshooting.md
  - docs/architecture/governance.md
  - docs/architecture/agents.md
when_to_read: Changing Go platform code, CLI commands, runtime files, governance construction, native MCP tools, or doctrine loading.
do_not_use_for:
  - Package and runtime ownership maps (docs/devs/codemap.md)
  - Test selection, fixtures, CI, and lifecycle detail (docs/devs/tests.md)
  - Documentation audit, catalog, and generation ownership (docs/devs/docs.md)
  - Release, native evaluation acceptance, and signed evidence (docs/devs/release_process.md)
  - Diagnostics, failure modes, and recovery procedures (docs/devs/troubleshooting.md)
  - Deployment and enrollment procedures (docs/guides/getting_started.md)
---

# Developer Guidelines

## Purpose

Defines coding invariants, architecture boundaries, error handling contracts, persistence rules, and governance construction order for the ztd Go platform. The current working tree is the authoritative source of truth. Command names, flags, defaults, and effects resolve from `./ztd <command> --help`.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Boundaries (`INV-BOUND`)](#boundaries-inv-bound)
  - [Environment (`INV-ENV`)](#environment-inv-env)
  - [CLI layout (`INV-CLI`)](#cli-layout-inv-cli)
  - [Code (`INV-CODE`)](#code-inv-code)
  - [Errors (`INV-ERR`)](#errors-inv-err)
  - [Typed contracts (`INV-TYPE`)](#typed-contracts-inv-type)
  - [Runtime files (`INV-FS`)](#runtime-files-inv-fs)
  - [Stores (`INV-STOR`)](#stores-inv-stor)
  - [Dependency construction (`INV-DEP`)](#dependency-construction-inv-dep)
  - [Testing (`INV-TEST`)](#testing-inv-test)
  - [Generated artifacts (`INV-GEN`)](#generated-artifacts-inv-gen)
  - [Doctrine (`INV-DOCTRINE`)](#doctrine-inv-doctrine)
  - [Native MCP (`INV-MCP`)](#native-mcp-inv-mcp)
  - [Contribution (`INV-CONTRIB`)](#contribution-inv-contrib)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Build and inspect](#build-and-inspect)
  - [Platform test entry points](#platform-test-entry-points)
  - [Add a runtime-file CLI command](#add-a-runtime-file-cli-command)
  - [Gateway governance construction](#gateway-governance-construction)
  - [Add a native MCP tool](#add-a-native-mcp-tool)
  - [Change runtime doctrine](#change-runtime-doctrine)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Boundaries (`INV-BOUND`)

| ID | Rule |
| :--- | :--- |
| INV-BOUND-01 | A governed operation MUST enter as typed intent or a canonical protobuf `GovernanceEnvelope`. The active posture determines whether L2 consensus and L3 notary evidence gate execution. L1 doctrine, L4 verification, and L5 actuation stay on every governed execution path. |
| INV-BOUND-02 | The governance guarantee MUST apply only to operations that traverse a ztd ingress. There is no CLI-side MCP wrapper or reverse proxy: third-party MCP servers are governed only as Gateway downstream egress (`INV-AGT-07`), and any forwarding path that does not cross a ztd ingress MUST NOT be described as adding an envelope, L2 through L5, a signed receipt, or Gateway audit. Client-native tools and other side channels stay outside the boundary. |
| INV-BOUND-03 | A mutation added to a governed ingress MUST be classified with the canonical action and event registries, represented by a typed payload, wrapped in a `GovernanceEnvelope`, verified by L4, and dispatched by L5. MUST NOT call mutation handlers directly to skip envelope construction or the verification gauntlet. |
| INV-BOUND-04 | The Gateway in-process Operator substrate MUST stay in `internal/services/gateway/embedded/`. The outbound Operator runtime is `ZtdoService` in `internal/services/ztdo.go`. `ZtdoService` MUST NOT construct `mcp.GatewayService`. |

### Environment (`INV-ENV`)

| ID | Rule |
| :--- | :--- |
| INV-ENV-01 | The Go toolchain MUST satisfy the `go` line in `go.mod` (Go 1.27.2). Setup scripts read that line and check the development toolchain, including Node.js 22.22.2+, 24.15+, or 26+ and `npm`; a fresh `make build` uses committed frontend embeds and does not require Node.js. |
| INV-ENV-02 | Commands in this guide MUST be run from the repository root unless the owning component guide specifies otherwise. |
| INV-ENV-03 | CLI behavior MUST be taken from `./ztd <command> --help`. This file MUST NOT grow a flag inventory. |
| INV-ENV-04 | Environment variables MUST be read only through typed keys in `protocol/constants/env_vars.json`, which assigns each key one of four categories: `secret` (API keys, tokens, client secrets, encryption keys), `user_endpoint` (user-specific endpoint or identity with no universal default), `host` (OS-provided host fact, e.g. `HOME`, `USER`, `SHELL`, `LANG`, `TERM`, `TZ`, `SSH_AUTH_SOCK`), and `test` (test-harness selector read only by test tooling, e.g. `ZTD_E2E_FLEET_SESSIONS`). Platform configuration (ports, paths, passkey origins, consensus, doctrine, vault paths) is never read from environment variables; it is a typed default in code or an explicit command flag. Every env key MUST be declared with a `category` in `protocol/constants/env_vars.json`; raw-string keys outside the registry are prohibited (`INV-TYPE-06`). Every `./ztd` command loads `.env` (`constants.EnvFilename`) from the working directory, the repository root per `INV-ENV-02`, through `config.LoadEnvFile` (`internal/cli/config/env_file.go`) before running the command: a missing file loads nothing; on Linux and macOS the file MUST be a regular file owned by the user or root with mode `0600` or `0400` (`ErrEnvFileInvalid`); only `KEY=VALUE` lines naming `secret` and `user_endpoint` keys are accepted (`ErrEnvFileMalformed`, `ErrEnvFileUnknownKey`); any violation exits the command; an exported variable wins. Repo-root `.env.example` is verified by `make constants-check`. |

### CLI layout (`INV-CLI`)

| ID | Rule |
| :--- | :--- |
| INV-CLI-01 | A new Cobra group MUST be a package under `internal/cli/cmd/<group>/`. The root command stays in `internal/cli/cmd/main.go`. Shared non-group helpers stay in `internal/cli/cmd/shared/`. The group inventory lives in [Code Map](codemap.md#cli-packages). |

### Code (`INV-CODE`)

| ID | Rule |
| :--- | :--- |
| INV-CODE-01 | MUST replace a broken path. MUST NOT preserve a compatibility shim for technical debt. |
| INV-CODE-02 | MUST fix the root cause. MUST NOT add a caller guard that hides invalid construction or state. |
| INV-CODE-03 | A function MUST do one job: reads read, writes write, validation validates, and orchestration composes explicit dependencies. |
| INV-CODE-04 | A security check MUST fail closed and MUST propagate its error. |
| INV-CODE-05 | MUST pass explicit dependencies and state transitions. MUST NOT add package globals, lazy adapters, reflection, or hidden side effects for control flow. |
| INV-CODE-06 | A production path MUST return errors. MUST NOT panic for a recoverable production failure. A `panic` in non-test code is permitted ONLY for an unrecoverable runtime failure (the process cannot safely continue), and the panic site MUST carry a comment citing `INV-CODE-06`. |
| INV-CODE-07 | MUST use `context.Context` for cancellation. Every goroutine MUST have an owner, cancellation, and completion via channels or `sync.WaitGroup`. |
| INV-CODE-08 | Go MUST be formatted with `gofmt`. Imports MUST be grouped as standard library, external modules, then internal packages. |
| INV-CODE-09 | MUST pass a pointer for a mutable or large struct and a value for a small read-only struct. |
| INV-CODE-10 | MUST confirm a dependency is already required before importing it. A new dependency MUST be added through the owning package manager. MUST NOT hand-edit a lockfile or manifest entry. |
| INV-CODE-11 | MUST search for an existing implementation before adding code. MUST extend the existing service, utility, model, constant, or pattern. MUST NOT add a helper, shim, wrapper, or compatibility layer that duplicates one. |
| INV-CODE-12 | MUST NOT add an `ensure*` or `getOrCreate*` helper that combines a read and a write or hides creation as a lookup side effect. |
| INV-CODE-13 | MUST reproduce a bug with a failing regression test before changing production code, then show that the test passes with the fix. |
| INV-CODE-14 | MUST update documentation and generated artifacts in the same change as the behavior they describe. |
| INV-CODE-15 | A change SHOULD stay on one coherent behavior and SHOULD leave the affected code easier to follow. |
| INV-CODE-16 | A constructor (any `New*` function) MUST return an error for invalid input or missing required dependencies. MUST NOT panic. A factory that wraps a constructor MUST itself return `(T, error)` and propagate the construction error. |

### Errors (`INV-ERR`)

| ID | Rule |
| :--- | :--- |
| INV-ERR-01 | Sentinel errors and distinct reusable platform failure modes MUST live in `internal/constants/errors.go`. MUST NOT declare a package-level sentinel error anywhere else. |
| INV-ERR-02 | MUST NOT use `errors.New` for a distinct production failure mode outside `internal/constants/errors.go`. |
| INV-ERR-03 | Before adding an error, MUST search `internal/constants/errors.go` and existing callers for an equivalent constant. Add a centralized constant only when none fits, then replace duplicate hand-written forms in the affected scope. |
| INV-ERR-04 | MUST wrap an error with operation context and preserve the cause with `%w`, for example `fmt.Errorf("gateway: load doctrine: %w", err)`. |
| INV-ERR-05 | MUST use a centralized `constants.Err*` for a known failure mode, `fmt.Errorf` with runtime values for a dynamic error, and a one-off `fmt.Errorf` in tests only when the injected cause has no production meaning. |
| INV-ERR-06 | MUST NOT compare rendered error strings when `errors.Is`, `errors.As`, or a typed status is available. |

### Typed contracts (`INV-TYPE`)

| ID | Rule |
| :--- | :--- |
| INV-TYPE-01 | A known shape MUST use a typed model. MUST NOT replace a protobuf or domain model with an untyped map or an ad hoc JSON object. |
| INV-TYPE-02 | MUST NOT use protobuf `Any`, `map[string]interface{}`, or an equivalent untyped container for a known contract. |
| INV-TYPE-03 | Schemas under `protocol/proto/ztd/` own governance envelopes, proofs, Operator messages, pub/sub messages, receipts, and compliance messages. Governance and transport code MUST keep those payloads typed. |
| INV-TYPE-04 | An internal envelope payload MUST use protobuf binary encoding where the execution path expects it. A protobuf-owned JSON boundary MUST use `protojson`. |
| INV-TYPE-05 | A non-protobuf HTTP surface MUST use named request and response structs. |
| INV-TYPE-06 | Runtime Go constants live under `internal/constants/`. External registries and schemas live under `protocol/constants/`, `protocol/models/`, and `protocol/schemas/`. When a public registry has both a Go and a JSON representation, MUST update both through the established owner and run owning contract or conformance tests. |

### Runtime files (`INV-FS`)

| ID | Rule |
| :--- | :--- |
| INV-FS-01 | `.ztd/` state MUST go through `RuntimeFileService` in `internal/services/fs/file_service.go`. MUST NOT hardcode a `.ztd/` runtime path or call `os` file operations for that state outside `internal/services/fs`. Exception: checked-in repository files addressed from the project root may use direct `os` operations. Governed `FILE_EDIT` and `RESTORE_FILE` reach the filesystem only through `RuntimeFileService.OpenFileEditTarget`. Enforced by `TestNoRawOSAccessToRuntimePaths` in `internal/services/fs/runtime_path_guard_test.go`. |
| INV-FS-02 | Reusable system, repository, and runtime path strings MUST be defined in `internal/constants/paths.go`. Consumers MUST NOT introduce an inline `.ztd/` fragment or a `filepath.Join` of path literals. |
| INV-FS-03 | A service or function that owns runtime I/O MUST receive `fs.RuntimeFileService` as an explicit argument. |
| INV-FS-04 | MUST use `fileSvc.Resolve` only when an API requires an absolute path. MUST use `fileSvc.Rel` to turn an absolute path inside the runtime root back into a relative service path. |
| INV-FS-05 | An existence check MUST use `fileSvc.FileExists`. A missing-file error MUST be compared with `errors.Is(err, constants.ErrNotFound)`. |
| INV-FS-06 | Runtime file and directory permissions MUST use `constants.Perm*` values. |
| INV-FS-07 | Gateway startup configuration carries `DataDir`, `PKIDir`, `SecretsDir`, and `VaultDir` (`internal/cli/serve/gateway.go`). Those absolute paths MUST stay at the CLI and startup boundary. Service code MUST receive the file service. MUST NOT add duplicate directory fields to route runtime I/O. |
| INV-FS-08 | A command constructor under `internal/cli/cmd/<group>/` that touches runtime state MUST accept a file-service factory. Factory initialization failure MUST wrap `constants.ErrFileServiceInit` and preserve the underlying error. Every new factory injection point MUST have a matching case in `internal/cli/cmd/<group>/factory_error_<group>_test.go` proving downstream dependencies are not called. |
| INV-FS-09 | Tests MUST use `testutil.TempDir` and `testutil.TestPaths` for isolated roots. `testutil.TempDir` returns an absolute base directory. Pass that directory to `fs.NewRuntimeFileService` and `paths.InitWithBase`. MUST NOT append `.ztd` manually. |
| INV-FS-10 | A command that clears the runtime root MUST call `fileSvc.ArchiveRuntime`, which renames `.ztd/` to `.ztd-<MMDDHHMM>`. MUST NOT call `RemoveAll("")` on the runtime root. Destructive `clean` or `reset` commands MUST gate on `shared.ConfirmDestructive` before mutating state. |
| INV-FS-11 | Every scratch directory or file MUST live under `.ztd/tmp/` (`constants.TmpDirname`), created with `fileSvc.MkdirTemp`. Throwaway local Operators live under `.ztd/operators/<name>` (`constants.OperatorsDirname`), the `ztd operator deploy --local` default. Remote staging over SSH uses `.ztd/tmp/` under the login directory (`ssh.MkdirRemoteScratch`). MUST NOT stage anything in the OS temp directory (`/tmp`, `%TEMP%`, `os.TempDir`, `os.MkdirTemp("")`) or the user's home directory. Tests use `testutil.TempDir(t)`; `make lint` rejects `os.TempDir`, `t.TempDir`, and empty-dir `os.MkdirTemp`/`os.CreateTemp`. |

Consumers pass relative paths from `internal/constants/paths.go` into `ReadFile`, `WriteFile`, `Stat`, `FileExists`, `ReadDir`, `Rename`, `Remove`, `RemoveAll`, and other `RuntimeFileService` methods. Startup constructs the service and calls `CreateRuntimeTree` (`internal/cli/serve/gateway.go`, `internal/cli/serve/operator.go`).

### Stores (`INV-STOR`)

Store invariants are owned by [Storage Architecture](../architecture/storage.md#invariants). They govern every change to persisted tables, columns, databases, or egress payloads:

- `INV-STOR-ENC-01`: Content is encrypted with the runtime vault; a locked vault fails both reads and writes.
- `INV-STOR-ONE-01`: One database and one file ledger per runtime root, one record per fact, one implementation per store.
- `INV-STOR-EGRESS-01`: Raw content stays local; everything leaving the runtime is scrubbed at the single egress choke point.
- `INV-STOR-LOG-01`: Logs carry no execution content (commands, output, paths, arguments, prompts); log IDs, sizes, counts, or `logging.ErrorKind(err)`.
- `INV-STOR-SESSION-01`: Record paths never create a session row; the session owner creates it at session start, and an unknown session fails with `ErrAuditSessionUnknown`.
- `INV-STOR-SCHEMA-01`: The `CREATE TABLE` is the whole schema; no migrations or back-fills.

### Dependency construction (`INV-DEP`)

| ID | Rule |
| :--- | :--- |
| INV-DEP-01 | Shared governance dependencies MUST be `pubsub.GovernanceCoreDeps`. Gateway-only document, consensus, field-read, platform-enrollment, and posture dependencies MUST be added on `pubsub.GatewayModeDeps`. Outbound mode MUST use `pubsub.OutboundModeDeps`, which embeds only the shared fields. |
| INV-DEP-02 | `ZtdoService` MUST build outbound dependencies with `NewOutboundModeDeps`. That constructor validates the outbound core before `OperatorPubSubService` is constructed. |
| INV-DEP-03 | The production Gateway builder MUST construct `GatewayModeDeps` directly and pass it to `NewGatewayOperatorPubSubService`. `NewGatewayModeDeps` validates a bundle when called. Code on the production builder path MUST NOT assume that validating constructor ran. Keep the builder's explicit non-nil wiring and command-service constructor checks. |
| INV-DEP-04 | `OperatorPubSubService.BindMCPGateway` MUST run once, before either service starts. It MUST reject nil (`constants.ErrPubSubMCPGatewayNil`), duplicate (`constants.ErrPubSubMCPGatewayAlreadyBound`), and post-start (`constants.ErrPubSubMCPGatewayBindAfterStart`) binding. MUST NOT reintroduce a construction-time setter for audit, consensus, or session validation. |
| INV-DEP-05 | Posture-dependent L2 MUST stay optional only where posture does not require it. A required posture dependency MUST fail closed. |
| INV-DEP-06 | Gateway governance wiring MUST follow the order in [Gateway governance construction](#gateway-governance-construction). |
| INV-DEP-07 | `CommandService`, `FileOpsService`, `PortService`, `HistoryService`, and `OllamaMaintenanceService` MUST take their collaborators (results publisher, audit store, active scrubber, ledger, history handler) as required constructor arguments and fail closed on nil or disabled input (`ErrPubSubResultsPublisher`, `ErrAuditStoreRequired`, `ErrEgressScrubberUnavailable`, `ErrLedgerRequired`, `ErrHistoryHandlerRequired`). MUST NOT add a setter or assign their fields after construction. |

### Testing (`INV-TEST`)

Selection, timeouts, race settings, fixtures, and CI scope live in [Testing Guide](tests.md).

| ID | Rule |
| :--- | :--- |
| INV-TEST-01 | A platform suite MUST run through `./ztd test ...` or the owning Makefile target. MUST NOT invoke `go test` directly for a platform suite. |
| INV-TEST-02 | MUST NOT use `t.Parallel()` in an integration or E2E test. |
| INV-TEST-03 | Go tests SHOULD be table-driven and SHOULD use `testify/assert` and `testify/require` when an assertion library helps. |
| INV-TEST-04 | Test functions, subtests, and files MUST be named for the behavior they verify. MUST NOT use generic `coverage`, `gap`, `edge`, `misc`, `success`, or `error` names. |
| INV-TEST-05 | Tier 1 MUST stay independent of a running platform. Tier 2 and Tier 3 MUST exercise real local boundaries. MUST NOT mock internal services, database clients, or cross-component communication in those tiers. |
| INV-TEST-06 | `NewGatewayFixture` registers teardown via `t.Cleanup`. MUST NOT stop its Gateway, close its databases, or close its downstream server a second time. |
| INV-TEST-07 | Temporary credential and fixture cleanup MUST be registered with `t.Cleanup`. MUST NOT use helper-local `defer` statements that run before the test body. |
| INV-TEST-08 | A test MUST use an explicit cancellation context and MUST join goroutines before returning. |
| INV-TEST-09 | Assertions MUST use typed constants for statuses, reasons, paths, and permissions. |
| INV-TEST-10 | The canonical trust bundle path is `.ztd/pki/trust/ztdg-ca-bundle.pem`. A test MUST NOT repair a failure by mutating developer PKI state. |
| INV-TEST-11 | MUST NOT use `os.Chdir` to align runtime state. A working-directory change is allowed only for behavior discovering source-tree or configuration files, and MUST restore the original directory with `t.Cleanup`. |

### Generated artifacts (`INV-GEN`)

| ID | Rule |
| :--- | :--- |
| INV-GEN-01 | Generated protobuf reference and OpenAPI output MUST change only through the source owner and its generator. MUST NOT hand-edit those outputs. |
| INV-GEN-02 | Current-state documentation MUST move with the behavior it describes. MUST NOT broaden a security or evidence claim past the path and artifacts that support it. |

| Output | Source | Update |
| :--- | :--- | :--- |
| Root `README.md` | Handwritten product overview | Re-read changed sections, verify links, and run every command described |
| Go, TypeScript, and Markdown protobuf output | `protocol/proto/ztd/` | `make proto-generate` (runs `proto-go` and `proto-node`) plus affected conformance tests |
| Gateway OpenAPI | Swagger annotations in Go owners | `make swagger-generate` plus route and contract tests |
| Website | Root `README.md` | `make website-test` and `make website-build` |
| Doctrine references | `protocol/constants/doctrine/` | `make doctrines-validate` |

### Doctrine (`INV-DOCTRINE`)

| ID | Rule |
| :--- | :--- |
| INV-DOCTRINE-01 | `governance.NewL1DoctrineFromDir` MUST remain the loader: built-in MITRE-oriented detectors, plus enabled entries from `*.json` files in the configured directory. An empty directory argument falls back to `NewL1Doctrine()`. |
| INV-DOCTRINE-02 | `./ztd gw start --doctrine-dir <path>` sets that directory; there is no environment fallback. The Gateway loads doctrine during construction, so runtime doctrine changes MUST be followed by a Gateway restart. |
| INV-DOCTRINE-03 | Reference JSON under `protocol/constants/doctrine/` MUST pass `make doctrines-validate`. An identifier or public-shape change MUST update owning JSON and affected Go or protocol contracts together. |

### Native MCP (`INV-MCP`)

| ID | Rule |
| :--- | :--- |
| INV-MCP-01 | A native MCP tool MUST implement `mcp.NativeTool` and MUST execute through the Gateway in-process Operator boundary after governed dispatch. |
| INV-MCP-02 | The native registry is not an outbound Operator tool server (`INV-BOUND-04`). |
| INV-MCP-03 | `RegisterNativeTools` in `internal/services/mcp/native_tool_registry.go` is the inventory. Documentation MUST NOT duplicate the tool list. |
| INV-MCP-04 | Registration MUST be explicit in `RegisterNativeTools`. MUST NOT register a native tool from `init`. Registry construction MUST return errors. |

### Contribution (`INV-CONTRIB`)

| ID | Rule |
| :--- | :--- |
| INV-CONTRIB-01 | Documentation changes MUST follow [Documentation Guide](docs.md). Front matter `last_updated` and `version` MUST change only after completing the full audit workflow. `version` MUST match `VERSION`. |
| INV-CONTRIB-02 | A contribution MUST be one coherent change and MUST include tests for a fix or feature. |
| INV-CONTRIB-03 | Contribution guidelines and issue processes reside in the [Contributing Guide](../../.github/CONTRIBUTING.md). Distribution terms are governed by the [Business Source License 1.1](../../LICENSE). |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Go version and module | `go.mod` | `go` directive is `1.27.2` |
| Setup prerequisites | `scripts/lib/dev-setup-common.sh`, `scripts/linux-setup.sh`, `scripts/macos-setup.sh`, `scripts/windows-setup.ps1`, `scripts/dev-check.sh` | Setup scripts read Go version from `go.mod` and tool pins from `Makefile`; development checks include Node.js 22.22.2+, 24.15+, or 26+ and `npm`. CI targets run `make dev-check` first. Fresh clones run `make build` using committed frontend embeds. |
| Platform CLI binary | `Makefile` (`MAIN_PKG := ./cmd/ztd`), `cmd/ztd` | `make build` writes `bin/ztd-<os>-<arch>` and copies runnable binary to repo root |
| Cobra root and groups | `internal/cli/cmd/main.go`, `internal/cli/cmd/<group>/` | `./ztd --help` |
| Sentinel errors | `internal/constants/errors.go` | `constants.Err*` declarations, including `ErrFileServiceInit` |
| Runtime path constants | `internal/constants/paths.go` | Path strings for `.ztd/` runtime state |
| `.ztd/` file service | `internal/services/fs/file_service.go` | `RuntimeFileService`, `NewRuntimeFileService`, `CreateRuntimeTree` |
| Trust bundle path | `internal/cli/config/config.go` | `.ztd/pki/trust/ztdg-ca-bundle.pem` |
| Repo-root `.env` loader | `internal/cli/config/env_file.go`, `internal/cli/cmd/main.go` | `config.LoadEnvFile(constants.EnvFilename)` in `ExecuteWithVersionInfo`; accepted keys are `constants.SecretEnvVars` and `constants.UserEndpointEnvVars` |
| Mode dependency types | `internal/services/pubsub/mode_deps.go` | `GovernanceCoreDeps`, `GatewayModeDeps`, `OutboundModeDeps`, `NewOutboundModeDeps`, `NewGatewayModeDeps` |
| Outbound construction | `internal/services/ztdo.go` | Calls `pubsub.NewOutboundModeDeps` |
| Gateway builder | `internal/services/gateway/gateway_service.go` | Direct `GatewayModeDeps` composite, then `NewGatewayOperatorPubSubService`, `NewPlatformEnrollmentService`, `mcp.NewGatewayService`, `BindMCPGateway`, `initHTTPHandler` |
| MCP bind errors | `internal/services/pubsub/pubsub_commands.go` | `BindMCPGateway` |
| In-process Operator substrate | `internal/services/gateway/embedded/operator.go` | `embedded.New`, `RegisterPending`; outbound runtime is not this package |
| L1 doctrine loader | `internal/services/governance/l1_doctrine.go` | `NewL1DoctrineFromDir` |
| Doctrine flag | `internal/cli/cmd/gw/gateway.go` | `--doctrine-dir` is the only source |
| Native tool contract and registry | `internal/services/mcp/registry.go`, `internal/services/mcp/native_tool_registry.go`, `protocol/docs/mcp_tool_template.go` | `NativeTool`, `RegisterNativeTools` |

## Procedures

### Build and inspect

1. From repository root:

```bash
make build
./ztd --help
./ztd test unit
./ztd test integration
make lint
```

2. Read flags from `./ztd <command> --help` (`INV-ENV-03`). Deployment and enrollment procedures stay in [Getting Started Guide](../guides/getting_started.md).

### Platform test entry points

1. Go platform:

```bash
./ztd test unit
./ztd test integration
./ztd test e2e
./ztd test coverage
./ztd test lint
./ztd test chaos
./ztd test summary
./ztd test public-loop --candidate candidate.json --output evidence.json
```

2. `./ztd test unit` delegates to `make test-unit` by default, or accepts `--pkg` and `--run` for targeted unit testing. Other suites keep their package and timeout flags inside the CLI.
3. Makefile entry points: `make test`, `make test-unit`, `make test-integration`, `make test-coverage`, `make console-test`. Lifecycle and fixture rules reside in [Testing Guide](tests.md).

### Add a runtime-file CLI command

1. Place command in `internal/cli/cmd/<group>/` (`INV-CLI-01`).
2. Accept a file-service factory (`INV-FS-08`).
3. On factory failure, return `fmt.Errorf("%w: %w", constants.ErrFileServiceInit, err)`.
4. Add a test case in `internal/cli/cmd/<group>/factory_error_<group>_test.go` verifying downstream dependencies are not called.

### Gateway governance construction

`gatewayServiceBuilder.build` in `internal/services/gateway/gateway_service.go` wires governance in this order. Startup operations between these milestones must preserve this ordering:

1. Open canonical database and obtain typed stores.
2. After stores exist, and before `InitializePKI` or `InitializePKIWithNames`, construct in-process substrate with `embedded.New` and `RegisterPending`.
3. Load L1 doctrine with `NewL1DoctrineFromDir`, then bootstrap posture-required L2 consensus when configured.
4. Construct gateway-mode `OperatorPubSubService` with governance and platform-enrollment dependencies. The builder assigns `GatewayModeDeps` directly (`INV-DEP-03`).
5. Construct `PlatformEnrollmentService` with command service as envelope processor.
6. Construct `mcp.GatewayService` with command service as envelope processor and session validator, supplying audit and L2 dependencies at construction.
7. Call `OperatorPubSubService.BindMCPGateway` once before either service starts (`INV-DEP-04`).
8. Build HTTP handler and servers after all controller dependencies exist.

### Add a native MCP tool

1. Reference `protocol/docs/mcp_tool_template.go`.
2. Implement `Name`, `Description`, `InputSchema`, and `Execute` with typed inputs and contextual errors.
3. Register the tool in `RegisterNativeTools` in `internal/services/mcp/native_tool_registry.go`.
4. Add unit tests and integration coverage required by the execution path.
5. Do not use package `init` (`INV-MCP-04`). Do not duplicate tool lists into documentation (`INV-MCP-03`).

### Change runtime doctrine

1. Edit owning JSON under `protocol/constants/doctrine/` or Gateway doctrine directory, together with Go or protocol contracts naming the same identifier (`INV-DOCTRINE-03`).
2. Run `make doctrines-validate`.
3. Restart Gateway after runtime directory changes (`./ztd gw start --doctrine-dir <path>`, `INV-DOCTRINE-02`).

## Anti-patterns

- Preserving compatibility shims for technical debt (`INV-CODE-01`).
- Writing `ensure*` or `getOrCreate*` helpers that combine reads and writes (`INV-CODE-12`).
- Panicking in production code or constructors for recoverable failures (`INV-CODE-06`, `INV-CODE-16`).
- Declaring `errors.New` or package-level sentinels outside `internal/constants/errors.go` (`INV-ERR-01`, `INV-ERR-02`).
- Using protobuf `Any` or `map[string]interface{}` for known contracts (`INV-TYPE-02`).
- Calling raw `os` file operations on `.ztd/` state outside `internal/services/fs` (`INV-FS-01`).
- Calling `go test` directly for platform suites, or using `t.Parallel()` in integration/E2E (`INV-TEST-01`, `INV-TEST-02`).
- Closing or stopping resources managed by `NewGatewayFixture` a second time (`INV-TEST-06`).
- Hand-editing generated protobuf Markdown, Go bindings, or Gateway OpenAPI output (`INV-GEN-01`).
- Registering native MCP tools inside `init`, or duplicating tool inventories into documentation (`INV-MCP-03`, `INV-MCP-04`).
- Maintaining duplicate CLI flag tables in documentation instead of referencing `./ztd <command> --help` (`INV-ENV-03`).

## Links out

- [Code Map](codemap.md): package ownership, runtime modes, and CLI group inventory.
- [Testing Guide](tests.md): tiers, fixtures, `testutil` path rules, selection, race, coverage, and CI.
- [Documentation Guide](docs.md): audit, catalog, style, generation, and format standards.
- [Release Process](release_process.md): versioning, native evaluation acceptance, and signed evidence.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [Governance Architecture](../architecture/governance.md) and [AI Agents and the ztd Governance Boundary](../architecture/agents.md): five-layer model, posture, and ingress limits.
- [Protocol Specification](../../protocol/docs/spec.md): wire protocol requirements.
- [Automation Scripts](../architecture/scripts.md): platform setup and automation behavior.
- [Console Architecture & Development](../architecture/console.md): embedded frontend architecture.

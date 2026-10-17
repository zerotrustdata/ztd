---
doc_id: codemap
title: ztd Code Map
audience: maintainers and coding agents
status: current
version: v1.1.1
last_updated: 2026-10-15
owners:
  - cmd/ztd/main.go
  - internal/cli/cmd/main.go
  - internal/cli/cmd/test/test.go
  - internal/cli/cmd/test/public_loop.go
  - internal/cli/cmd/test/release_smoke.go
  - internal/cli/cmd/test/release_smoke_evidence.go
  - internal/cli/cmd/eval/eval.go
  - internal/cli/cmd/eval/public_restore_cmd.go
  - internal/cli/cmd/public/public.go
  - internal/services/gateway/gateway_service.go
  - internal/services/gateway/gateway_http.go
  - internal/services/gateway/gateway_http_router.go
  - internal/services/gateway/gateway_auth.go
  - internal/services/gateway/gateway_db.go
  - internal/services/gateway/embedded/operator.go
  - internal/services/ztdo.go
  - internal/services/pubsub/mode_deps.go
  - internal/services/fs/file_service.go
  - internal/services/mcp/native_tool_registry.go
  - internal/constants/paths.go
  - buf.gen.yaml
  - protocol/node/buf.gen.yaml
  - Makefile
related:
  - docs/devs/devs.md
  - docs/devs/tests.md
  - docs/devs/docs.md
  - docs/devs/release_process.md
  - docs/devs/troubleshooting.md
  - docs/architecture/overview.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/governance.md
when_to_read: Finding which package owns a runtime mode, CLI group, protocol generator, persistence store, or test entry point.
do_not_use_for:
  - Coding invariants and error contracts (docs/devs/devs.md)
  - Test selection, fixtures, race, coverage, and CI (docs/devs/tests.md)
  - Documentation audit, catalog, and generation policy (docs/devs/docs.md)
  - Release, native evaluation acceptance, and signed evidence (docs/devs/release_process.md)
  - Diagnostics, failure modes, and recovery procedures (docs/devs/troubleshooting.md)
  - Deployment and enrollment procedures (docs/guides/getting_started.md)
---

# ztd Code Map

## Purpose

Defines package and runtime ownership for the ztd repository. The current working tree is the authoritative source of truth. Command names and flags resolve from `./ztd <command> --help`. This file provides structural mapping; coding rules, test mechanics, documentation audit policies, and release procedures remain in their canonical documents.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Ownership (`INV-MAP`)](#ownership-inv-map)
  - [Mode separation (`INV-MODE`)](#mode-separation-inv-mode)
  - [Package placement (`INV-PKG`)](#package-placement-inv-pkg)
  - [Protocol generators (`INV-PROTO`)](#protocol-generators-inv-proto)
  - [Test commands (`INV-TESTMAP`)](#test-commands-inv-testmap)
- [Owned surfaces](#owned-surfaces)
  - [Repository structure](#repository-structure)
  - [Process entry](#process-entry)
  - [CLI packages](#cli-packages)
  - [Runtime modes](#runtime-modes)
  - [Governance flow](#governance-flow)
  - [Gateway HTTP boundary](#gateway-http-boundary)
  - [Persistence](#persistence)
  - [Internal services](#internal-services)
  - [Protocol packages](#protocol-packages)
  - [Native tools](#native-tools)
  - [Compliance and reporting](#compliance-and-reporting)
  - [Other product components](#other-product-components)
  - [Test map](#test-map)
  - [Build and validation](#build-and-validation)
- [Procedures](#procedures)
  - [Find an owner](#find-an-owner)
  - [Confirm the Cobra inventory](#confirm-the-cobra-inventory)
  - [Confirm a runtime mode](#confirm-a-runtime-mode)
  - [Confirm a generator before editing output](#confirm-a-generator-before-editing-output)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number in a topic. Do not renumber.

### Ownership (`INV-MAP`)

| ID | Rule |
| :--- | :--- |
| INV-MAP-01 | Package and runtime ownership MUST be maintained in this file. Coding invariants MUST stay in [Developer Guidelines](devs.md). Test selection, fixtures, race, coverage, and CI MUST stay in [Testing Guide](tests.md). Documentation audit, catalog, and generation policy MUST stay in [Documentation Guide](docs.md). Release procedure MUST stay in [Release Process](release_process.md). Diagnostics and recovery MUST stay in [Troubleshooting Guide](troubleshooting.md). This file MUST NOT duplicate those topics. |
| INV-MAP-02 | A mapped owner MUST be a repository-relative path that exists in the tree. A claim the tree no longer supports MUST be removed in the same change. CLI flags MUST be read from `./ztd <command> --help` (`INV-ENV-03`). |

### Mode separation (`INV-MODE`)

| ID | Rule |
| :--- | :--- |
| INV-MODE-01 | Gateway HTTP and control-plane ownership MUST stay on `gateway.GatewayModeService` in `internal/services/gateway/`. The in-process Operator substrate MUST stay on `embedded.Service` in `internal/services/gateway/embedded/`. The outbound Operator runtime MUST stay on `services.ZtdoService` in `internal/services/ztdo.go`, started from `internal/cli/serve/operator.go`. These three owners MUST NOT be described as one runtime. Construction constraints on `ZtdoService` are `INV-BOUND-04`. |
| INV-MODE-02 | `embedded.Service` MUST own pending registration (`RegisterPending`), claim (`Claim`), deterministic document id `constants.DocIDEmbeddedOperator` (`embedded-operator`), same-user idempotence, and `constants.ErrEmbeddedOperatorClaimed`. HTTP routing for that substrate MUST stay in `internal/services/gateway/operator_controller.go`. Web-session bind MUST stay on `RegistrationService`. |

### Package placement (`INV-PKG`)

| ID | Rule |
| :--- | :--- |
| INV-PKG-01 | A Cobra group package name MUST match its directory. When that name would collide with an existing import, the package MUST be the directory plus `cmd`: `authcmd`, `operatorcmd`, `vaultcmd`, `testcmd`, `tuicmd`, `compliancecmd`, `autochthoncmd`. Where a new group lives is governed by `INV-CLI-01`. |
| INV-PKG-02 | `ztd test public-loop` MUST stay in `internal/cli/cmd/test/public_loop.go` (`package testcmd`). It MUST NOT move into the `public` spectator group. `public restore` MUST be constructed by `eval.PublicRestoreCmdWithConfig` in `internal/cli/cmd/eval/public_restore_cmd.go` and registered on the `public` command in `internal/cli/cmd/public/public.go`. |
| INV-PKG-03 | `internal/cli/cmd/shared/`, `internal/cli/cmd/gwremote/`, and `internal/cli/cmd/cmdtest/` MUST NOT be registered from `internal/cli/cmd/main.go`. They are shared helper packages, not Cobra groups. |

### Protocol generators (`INV-PROTO`)

| ID | Rule |
| :--- | :--- |
| INV-PROTO-01 | Root `buf.gen.yaml` MUST be described as the Go and Markdown protobuf generator only (`make proto-go`, output beside `protocol/proto/ztd/` and under `protocol/docs/reference/api/`). TypeScript stubs MUST be attributed to `protocol/node/buf.gen.yaml` (`make proto-node`, output under `protocol/node/src/gen/`). `make proto-generate` runs `proto-go` and `proto-node`. MUST NOT attribute TypeScript generation to the root `buf.gen.yaml`. Hand-editing generated output is prohibited (`INV-GEN-01`). |

### Test commands (`INV-TESTMAP`)

| ID | Rule |
| :--- | :--- |
| INV-TESTMAP-01 | This file maps test commands to tiers and directories. Selection, fixtures, `testutil` path rules, race, coverage, timeouts, and CI MUST be taken from [Testing Guide](tests.md). Platform suites still enter through `./ztd test ...` or the owning Makefile target (`INV-TEST-01`). |

## Owned surfaces

### Repository structure

| Path | Owns |
| :--- | :--- |
| `cmd/ztd/` | Go binary entry point. `main` passes build metadata into `serve.VersionInfo` and calls `cmd.ExecuteWithVersionInfo`. |
| `internal/cli/` | CLI commands, API client, authentication, configuration, frontend-origin checks, process management, foreground Gateway and Operator startup, SSE, streams, platform helpers, TUI, and onboarding wizard. |
| `internal/services/` | Gateway, outbound Operator, governance, transport, persistence, execution, evaluation, compliance, inference, public disclosure, and supporting services. |
| `internal/constants/` | Go constants for paths, errors, protocol identifiers, permissions, Operator types (`OperatorType`) and capabilities (`OperatorCapabilities`), and runtime behavior. |
| `internal/config/`, `internal/models/`, `internal/paths/` | Typed configuration, internal service models, and runtime paths derived from project root. |
| `internal/netutil/` | Wildcard TCP port availability probes shared by configuration and process startup; Windows binds without listening to avoid firewall prompts. |
| `internal/governance/` | Envelope hash helpers over canonical protobuf `GovernanceEnvelope`. L1 through L5 services reside in `internal/services/governance/`. |
| `internal/ollama/` | Minimal HTTP client for Ollama model-maintenance endpoints. |
| `internal/infra/cloudflaredns/` | Cloudflare DNS client. |
| `internal/pkg/certutil/`, `internal/pkg/ssh/` | Certificate and SSH helpers. |
| `internal/buildinfo/`, `internal/exitcode/`, `internal/httpclient/`, `internal/jsonschema/`, `internal/marshaler/`, `internal/pathutil/`, `internal/response/`, `internal/security/`, `internal/testutil/`, `internal/timesvc/`, `internal/uuid/`, `internal/certs/` | Shared build metadata, exit handling, HTTP, schema validation, serialization, path utilities, responses, security helpers, test infrastructure, time, UUIDs, and certificates. |
| `protocol/` | Protobuf schemas, generated bindings, JSON registries, model schemas, conformance tests, vectors, and protocol documentation. |
| `test/` | Cross-package integration tests, `test/fixtures/`, and `test/e2e/`. |
| `eval/` | Evaluation campaign inputs and evaluation data layout. Go evaluator resides in `internal/services/evaluation/`, exposed via `internal/cli/cmd/eval/`. |
| `console/` | Gateway-embedded browser console (React + TypeScript). Docs: [Console Architecture & Development](../architecture/console.md). |
| `ztd-adapter/` | Audited browser adapter and deterministic contract pack for generated observe frontends. |
| `evaluation-explorer/` | Evaluation explorer SPA; build artifacts embedded in `internal/services/gateway/explorer/static`. |
| `examples/` | Runnable Go platform examples, external console app, and MCP client configuration templates. |
| `release-evidence/` | Git-ignored local output of `ztd compliance release-prepare` (`release-evidence/<version>/report-bundle/`). Not committed; procedure: [Release Process](release_process.md). |
| `docs/` | Architecture, guides, references, developer docs, and release notes. |
| `scripts/` | Validation, generation, release, onboarding, and build support scripts. Host lifecycle entry points are root Makefile targets (`quickstart`, `up`, `up-public`, `gw`, `gw-public`, `operator`, `status`, `down`). `scripts/configure-gateway-lan.ps1` is the Windows-to-WSL Gateway LAN helper. `scripts/ci/` holds onboarding smoke and operator deploy lifecycle workflows. Catalog: [Automation Scripts](../architecture/scripts.md). |
| `website/` | Static site generator and Cloudflare Worker packaging. Source overview is root `README.md`. |
| `vendor/` | Third-party Go modules. Not a ztd product owner. |
| `Makefile` | Repository build and host lifecycle orchestration. |

### Process entry

The executable starts in `cmd/ztd/main.go`. Command registration and process-level error handling reside in `internal/cli/cmd/main.go`. `ExecuteWithVersionInfo` loads the working-directory `.env` through `config.LoadEnvFile` before executing any command and exits on a load error (`INV-ENV-04`). `NewRootCmd` registers public command groups; confirm current inventory with `./ztd --help` rather than duplicating ordering here.

| Path | Owns |
| :--- | :--- |
| `internal/cli/serve/gateway.go` | Foreground Gateway startup: paths, `RuntimeFileService`, runtime tree, logging, configuration, `GatewayModeService`, shutdown. |
| `internal/cli/serve/launch_profile.go` | Versioned `GatewayLaunchProfile` persisted after a successful managed `gw start`. `gw restart` reads it. |
| `internal/cli/serve/operator.go` | Foreground outbound Operator: configuration, credentials, platform enrollment, mTLS transport, `ZtdoService`, shutdown. `StartOperator` in `operator_runtime.go` refuses a working directory holding a Gateway runtime (`constants.GatewayRuntimeMarker`, `constants.ErrOperatorGatewayRuntime`), then starts service and certificate renewer. |
| `internal/cli/serve/platform_enrollment_client.go` | Resumable owner-approved Operator enrollment client. |
| `internal/cli/serve/operator_cert_renewal.go` | `OperatorCertRenewer`: in-memory Operator certificate and L2 trust renewal over the `renewal:` pub/sub channel. |
| `internal/cli/serve/cert.go` | CSR generation and certificate bundle logging. |
| `internal/cli/serve/version.go` | Build version metadata passed from `cmd/ztd`. |
| `internal/services/logging/` | Daemon log files and `slog` configuration. |

### CLI packages

Live group inventory: `./ztd --help`. Group placement: `INV-CLI-01`. Name exceptions: `INV-PKG-01`.

| Group | Package | Path | Alias | Owns |
| :--- | :--- | :--- | :--- | :--- |
| `gw` | `gw` | `internal/cli/cmd/gw/` | `gateway` | Gateway lifecycle, data, security, and tunnel commands. `gateway_start_running.go` decides whether `gw start` reuses a running Gateway by comparing explicitly set startup flags with its launch profile. |
| `login`, `logout` | `authcmd` | `internal/cli/cmd/auth/` | | Top-level sign-in (`login` defaults to headless `login cli`; `login web` uses browser) and sign-out (`logout`, `logout web`, `logout cli`); shares `auth` package and enrollment runner. |
| `auth` | `authcmd` | `internal/cli/cmd/auth/` | | User and platform enrollment, sessions, and approvals. |
| `mcp` | `mcp` | `internal/cli/cmd/mcp/` | | MCP stdio serving and agent integration. |
| `operator` | `operatorcmd` | `internal/cli/cmd/operator/` | `operators` | Operator `list`, `show`, `bind`, governed `run` and `stop`, foreground `start`, `reset-identity`, binary copy (`cp`, `scp`), streams, `gateway-preflight`, and fleet `deploy`. Local and SSH deploy live in `operator_deploy.go`. Procedure: [Connect Many Operators](../guides/connect_operator_to_gateway.md#connect-many-operators). |
| `autochthon` | `autochthoncmd` | `internal/cli/cmd/autochthon/` | | Gateway-less foreground runtime: host sampling and local prior under `.ztd/autochthon/`, and read-only `status` reporting. Not an Operator mode; refuses Gateway-facing flags. |
| `vault` | `vaultcmd` | `internal/cli/cmd/vault/` | | Local vault init, unlock, rekey, status, and reset. |
| `test` | `testcmd` | `internal/cli/cmd/test/` | `tests` | Unit, integration, e2e, coverage, lint, chaos, summary, `public-loop`, `scale`, and `release-smoke`. |
| `audit` | `audit` | `internal/cli/cmd/audit/` | `audits` | Receipt, event, summary, export, report, and verify queries against a running Gateway. |
| `report` | `report` | `internal/cli/cmd/report/` | `reports` | Deterministic CSV evidence generation and offline verification. |
| `public` | `public` | `internal/cli/cmd/public/` | | Public spectator feed. `restore` is constructed in eval package (`INV-PKG-02`). |
| `swagger` | `swagger` | `internal/cli/cmd/swagger/` | | OpenAPI generation, serving, and validation. |
| `tui` | `tuicmd` | `internal/cli/cmd/tui/` | | Tactical Governance Console. |
| `version` | `version` | `internal/cli/cmd/version/` | | Build metadata. Optional FIPS module status is `./ztd version --help`. |
| `compliance` | `compliancecmd` | `internal/cli/cmd/compliance/` | `compliances` | `ksi`, `ksi-history`, `overlay`, `release-evidence`, `release-prepare`, `evidence`, `evidence-graph`, and `report`. |
| `eval` | `eval` | `internal/cli/cmd/eval/` | | `models`, `campaigns`, `runs`, `rollout`, `formations`, `gates`, `conformance`, `suites`, `backup`, and `restore`. Also constructs `public restore`. |

Files at `internal/cli/cmd/` root are the root command, its tests, and shared file-service and config-load tests. Factory-error tests reside beside the group they cover (`factory_error_<group>_test.go`). Factory rule is `INV-FS-08`.

| Path | Package | Owns |
| :--- | :--- | :--- |
| `internal/cli/cmd/shared/` | `shared` | Config load, `NewFileSvc` (`fs.NewRuntimeFileService`), source-root lookup, command context, version-info context, and destructive-action confirmation with pre-clean backups. Exists so group packages do not import root `cmd` package. |
| `internal/cli/cmd/gwremote/` | `gwremote` | Gateway HTTP publication, model provenance, provider observation, and health hook used by `eval` and `public`. Exists so those packages do not import `gw`. |
| `internal/cli/cmd/cmdtest/` | `cmdtest` | Cross-package test helpers. Not a Cobra group. |
| `internal/cli/agent/` | | Registry of external coding agents launcher supports: per-agent config writing, launch arguments, tool-lockdown level, and verify hooks. Consumed by `mcp agent list/run/show/verify`. |
| `internal/cli/api/` | | Typed CLI HTTP client. |
| `internal/cli/auth/` | | CLI enrollment, credential staging, key generation, passkey registration, trust bundles, and mTLS clients. `approval.go` owns L3 approval page URL and status verification shared by `auth approve` and TUI; `platform_enrollment.go` owns enrollment fingerprint, issued-identity, and response helpers shared by `auth enroll` and TUI; `operator_binding.go` owns bind/unbind response validation shared by CLI and TUI. |
| `internal/cli/browserorigin/` | | Frontend-origin validation and normalization. |
| `internal/cli/config/` | | CLI configuration resolution and endpoint overrides. `env_file.go` owns `LoadEnvFile` (working-directory `.env`, `secret` and `user_endpoint` keys only; owner and mode checks in `env_file_posix.go`) and `InferenceOllamaEndpoint` (`--inference-ollama-endpoint`, else `ZTD_OLLAMA_ENDPOINT`). |
| `internal/cli/frontendverify/` | | Frontend connection and running-Gateway verification. |
| `internal/cli/gateway/` | | Shared Gateway inventory helpers, including completed platform-enrollment filtering used by `gw status` and TUI status view. |
| `internal/cli/identityreset/` | `identityreset` | Removal of Operator runtime CLI credentials, CLI mTLS pair, and cached Gateway trust (`Reset`); refuses a runtime holding `constants.GatewayRuntimeMarker`. Consumed by `operator reset-identity`. |
| `internal/cli/mcptransport/` | `mcptransport` | Standard JSON-RPC 2.0 stdio framing, dispatch handler, and transport for CLI MCP serving (`mcp stdio`). |
| `internal/cli/operator/` | | Operator discovery and management helpers: governed command dispatch types, inventory helper `IsConnected`, stop helpers (`CheckStoppable`, `NewStopRequest`, `DecodeStopResponse`) shared by `operator stop` and TUI, and normalized heartbeat parser shared by `operator show` and TUI. |
| `internal/cli/output/` | | Human-readable and JSON command output. |
| `internal/cli/platform/` | | Cross-platform process, browser, and system trust operations. |
| `internal/cli/serve/` | | Foreground Gateway and Operator runtimes. See [Process entry](#process-entry). |
| `internal/cli/sse/` | | CLI SSE client: reconnect with capped backoff, connect/disconnect callbacks, and stop on CLI session rejection. |
| `internal/cli/stream/` | | Local and SSH streams for Operator management, with per-platform proxy-command and transient-network-error helpers. |
| `internal/cli/tui/` | | Tactical Governance Console implementation. Every TUI Gateway request lives in `gateway.go` and mirrors CLI command making it. |
| `internal/cli/wizard/` | | Interactive Gateway setup flow. |

### Runtime modes

Mode dependency types reside in `internal/services/pubsub/mode_deps.go`. Wiring rules are `INV-DEP-01` through `INV-DEP-07`. Construction order for Gateway is [Gateway governance construction](devs.md#gateway-governance-construction).

| Mode | Type | Path | Owns |
| :--- | :--- | :--- | :--- |
| Gateway HTTP and control plane | `gateway.GatewayModeService` | `internal/services/gateway/gateway_service.go` | Policy decision point: HTTP, PKI, persistence, MCP, pub/sub, and governance wiring. Builder assembles dependencies before start. |
| Embedded Operator substrate | `embedded.Service` | `internal/services/gateway/embedded/operator.go` | In-process operator document: `embedded.New`, `RegisterPending`, `Claim`. Not outbound runtime. Gateway builder wires enabled Gateway-local capability handlers through `pubsub.NewCapabilityHandlers`. |
| Outbound Operator | `services.ZtdoService` | `internal/services/ztdo.go` | Authenticated connection to Gateway and approved work on Operator host. Started from `internal/cli/serve/operator.go`. Calls `pubsub.NewOutboundModeDeps`. MUST NOT construct `mcp.GatewayService` (`INV-BOUND-04`). |

| Deps type | Adds beyond `GovernanceCoreDeps` |
| :--- | :--- |
| `pubsub.GovernanceCoreDeps` | Shared execution target, replay store, state-root provider, transaction audit, L3 notary, signer store, and L1 doctrine. |
| `pubsub.GatewayModeDeps` | Governed document store, consensus store, field reader, consensus deliberator, platform enrollment, and posture. |
| `pubsub.OutboundModeDeps` | Consensus policy store only. `ZtdoService` passes one in-memory `governance.L2TrustStore`, built from enrollment bundle and replaced at each certificate renewal, as both signer store and consensus policy store. |

Outbound groups owned by `ZtdoService`: enrollment-bundle L2 trust (`governance.L2TrustStore`, required at construction) and mTLS pub/sub; `ExecutionService` and `FileEditService`; `CanonicalDBService` plus vault, suspended transactions, replay, SQL audit, and required Git ledger; local L1 doctrine, outbound L3 notary, L4, and L5; enabled inference, provider-boundary observer, and model-provenance capability handlers from `pubsub.NewCapabilityHandlers`.

### Governance flow

Mutations that traverse a ztd ingress use a typed `GovernanceEnvelope` (`INV-BOUND-01`). Canonical envelope and proof messages reside in `protocol/proto/ztd/common/v1/`. Posture and the five layers are specified in [Governance](../architecture/governance.md). Ingress limits are defined in [AI Agents and the ztd Governance Boundary](../architecture/agents.md).

| Step | Owner |
| :--- | :--- |
| L1 doctrine | `internal/services/governance/l1_doctrine.go` (`NewL1DoctrineFromDir`). Loader rules: `INV-DOCTRINE-*`. |
| L2 consensus | `internal/services/governance/l2_consensus.go`, `internal/services/gateway/consensus_store_service.go`. Bound application HTTPS deliberation. Required only for postures that enforce it (`INV-DEP-05`). Architecture: [Consensus](../architecture/consensus.md). |
| L3 notary | `internal/services/governance/`. Gateway mode and outbound mode use distinct notaries. See mode table. |
| L4 verification and L5 actuation | `internal/services/governance/` with command and file dispatch in `internal/services/pubsub/`. |
| MCP and A2A translation, native tools, suspension | `internal/services/mcp/`. Native registration rules: `INV-MCP-*`. |
| Sensitive-value tokenization | `internal/services/scrubbing/`. |

### Gateway HTTP boundary

`gateway.HTTPHandler` in `internal/services/gateway/gateway_http.go` is the routing shell. `initHTTPHandler` in `gateway_service.go` builds two listeners, both bound to `Gateway.ListenHost` (`--listen-host`, default `127.0.0.1`; `0.0.0.0` must be requested explicitly):

| Field | Listener | Handler | Role |
| :--- | :--- | :--- | :--- |
| `server` | `Gateway.HTTPPort` | `buildHTTPRouter` | Plain HTTP: bootstrap health, state, bootstrap, CA bundle and fingerprint, CLI recovery request/status/complete, platform enrollment request/status/complete, `/.well-known/ztd/bin/`, deploy scripts. Other paths redirect to HTTPS. Path-traversal guard and rate limit wrap mux. |
| `publicServer` | `Gateway.HTTPSPort` | `HTTPHandler` (`buildPublicRouter`) | Full API. TLS `ClientAuth` is `VerifyClientCertIfGiven`. `AuthService.Middleware` classifies routes. `RouteAuthRegistry.AuthMode` defaults unknown paths to `RouteAuthMTLS`. CORS and path-traversal guard wrap handler. |

Controller domains registered on HTTPS router (`HTTPHandlerDependencies`):

| Domain | Controllers |
| :--- | :--- |
| Identity and enrollment | PKI, bootstrap, CLI recovery, CLI rotation, CLI refresh, CLI session, enrollment token, user, session, passkey, platform enrollment |
| Data and audit | Data, signer, audit |
| Operators and dispatch | Operator, dispatch, inference dispatch |
| Streams and ingress | SSE, MCP, pub/sub |
| Governance and admin | Governance, health, admin |
| Observation and publication | Observe, observe producer, public feed, eval campaign publication, provider observation, model provenance |

| Path | Owns |
| :--- | :--- |
| `internal/services/gateway/console/` | Embedded Console SPA. |
| `internal/services/gateway/scripts/` | Embedded deployment script templates. |
| `internal/services/gateway/docs/` | Generated OpenAPI (`swagger.json` embedded by `docs.go`, plus `swagger.yaml`). Generator: `make swagger-generate`. |
| `internal/services/gateway/explorer/` | Embedded evaluation-explorer assets. |
| `internal/services/gateway/db/schema.sql` | Canonical SQLite schema embedded by `CanonicalDBService`. |

### Persistence

`.ztd/` file I/O rules are `INV-FS-*`. This section names owners only.

| Owner | Path | Notes |
| :--- | :--- | :--- |
| `RuntimeFileService` | `internal/services/fs/file_service.go` | Canonical `.ztd/` file service. Startup calls `CreateRuntimeTree` from `internal/cli/serve/gateway.go` and `internal/cli/serve/operator.go`. |
| Path constants | `internal/constants/paths.go` | Reusable system, repository, and runtime path strings. |
| `CanonicalDBService` | `internal/services/gateway/gateway_db.go` | Primary SQLite connection, schema lifecycle, encrypted vault, secret manager, maintenance, and store lifetimes. Constructor: `OpenCanonicalDBService`. Gateway builder and `ZtdoService` both call it. |

`CanonicalDBService` accessors:

| Accessor | Store |
| :--- | :--- |
| `GetDocStore` | `DocumentStoreService` |
| `GetAppPolicyStore` | `AppPolicyStoreService` |
| `GetSignerStore` | `SignerStoreService` |
| `GetConsensusStore` | `ConsensusStoreService` |
| `GetStateRootSvc` | `StateRootService` |
| `GetReplayStore` | `storage.ReplayStore` |
| `GetSuspendedStore` | `storage.SuspendedTransactionService` |
| `GetKVStore` | `KVStoreService` |
| `GetSSEStore` | `SSEEventService` |
| `GetBlobStore` | `BlobStoreService` |
| `GetAuditStore` | `storage.SQLAuditStore` |

`internal/services/storage/` additionally owns execution event recording (`events` in `ztd.db`), suspended transactions and replay nonces (tables in `ztd.db`), Git ledger history, commitments, token store, and operational snapshots. `storage.OpenLocalExecutionEvidence` builds Git ledger and history handler for both outbound Operator (`ZtdoService`) and Gateway's embedded Operator, each against its own runtime file service. `storage.ReadLocalActionReceipt` (`local_receipt.go`) reads one runtime's signed receipt from its own audit store with that runtime's vault key, never a Gateway mirror. Storage architecture: [Storage Architecture](../architecture/storage.md).

### Internal services

| Path | Owns |
| :--- | :--- |
| `internal/services/auth/` | Operator bootstrap transport and host fingerprinting. |
| `internal/services/autochthon/` | Gateway-less `ztd autochthon` command (CLI: `internal/cli/cmd/autochthon/`): host sampling, local predictor population, `.ztd/autochthon/` hash-chained tally, and read-only status report. No governance path. |
| `internal/services/compliance/` | KSI evaluation and history, OSCAL support, catalog validation, evidence import and graph verification, and signed report bundles. |
| `internal/services/evaluation/` | Model campaign controller, campaign Operator role resolution (`campaign_operator_topology.go`), governed command lane, grading, evidence storage, conformance record storage and compliance admission (`conformance_store.go`, `conformance_compliance.go`), and campaign verification. |
| `internal/services/evaluation/conformance/` | Governance conformance suite (`governance-conformance@1.0.0`). `cases` holds the catalogue and its validation, `drive` the ingress drivers, fault mutators, sentinel, and attended wrapper, `canary` the effect snapshots, and `oracle` (with `oracle/oracletest`) the anchors, receipt verification, and ledger bracket. `run` captures evidence and writes the record, `verify` recomputes verdicts offline, and `report` builds coverage and recall. |
| `internal/services/execution/` | Command execution and governed file edits. |
| `internal/services/fs/` | Scoped `.ztd/` runtime file operations. |
| `internal/services/ztdbinaries/` | Deployment-binary catalog, reader, and publisher. CLI wrapper: `internal/tools/ztdbinaries/`. |
| `internal/services/gateway/` | Gateway HTTP and control plane, including child packages in HTTP boundary table. |
| `internal/services/gateway/embedded/` | In-process Operator substrate (`INV-MODE-02`). |
| `internal/services/governance/` | L1, L2, L3, L4, L5, governance interfaces, state-root providers, signer stores, and public-key export. |
| `internal/services/inference/` | Inference dispatch (`internal/services/inference/dispatch/`), provider backends, and attempt storage. |
| `internal/services/inference/provider_observer/` | Provider-boundary GPU and RAM sampling. |
| `internal/services/inference/model_provenance/` | Storage-side model weight attestation. |
| `internal/services/keystore/` | Encrypted key storage for Gateway secrets and PKI. |
| `internal/services/logging/` | Runtime log file and `slog` configuration. |
| `internal/services/mcp/` | MCP and A2A gateway, native tools, field-path governance, suspension, and downstream clients. |
| `internal/services/network/` | Network identity detection and endpoint construction. |
| `internal/services/operatorcapability/` | Capability resolution and selection for provider-boundary observer, provenance operator, inference, and governed data operators, plus Ollama command construction. |
| `internal/services/publicdisclosure/` | Validation of public-disclosure records. Gateway publisher and mirror orchestration stay in `internal/services/gateway/`. |
| `internal/services/pubsub/` | Gateway and Operator pub/sub clients, command dispatch, results, heartbeats, mode dependency types, and capability handlers (`NewCapabilityHandlers`) shared by Gateway-local executor and outbound Operator. |
| `internal/services/reporting/` | Deterministic CSV evidence reports and verification. Exposed by `ztd report`. |
| `internal/services/scrubbing/` | Sensitive-value detection, tokenization, and rehydration. |
| `internal/services/sqliteutil/` | Shared SQLite configuration and connection helpers. |
| `internal/services/storage/` | Audit, execution, replay, suspension, commitment, and Git ledger persistence. |
| `internal/services/system/` | Host capability and embedded Git selection. |
| `internal/services/vault/` | Encryption vault lifecycle. |

Test doubles reside in `internal/services/pubsub/pubsubtest/`, `internal/services/governance/governancetest/`, and `internal/services/keystore/keystoretest/`. `internal/services/storage/storagetest/` holds test helpers (vaults, file services, token store) and chaos test's `chaos.db` event store; audit store is always production `storage.SQLAuditStore`. `internal/tools/chaos` imports `storagetest` for `ztd test chaos`. Gateway and Operator production service code does not import these test packages.

### Protocol packages

Schemas reside in `protocol/proto/ztd/`. Wire requirements: [Protocol Specification](../../protocol/docs/spec.md). Package ownership and generation: [Protocol README](../../protocol/README.md).

| Domain | Owns |
| :--- | :--- |
| `common/v1` | Governance envelopes, layer metadata, shared enums, and common messages. |
| `compliance/v1` | Compliance evidence, assessment, and report messages. |
| `eval/v1` | Evaluation campaign, scenario catalog, assignment, run verification, witness (provider-boundary and model-provenance) evidence, public projection, governance-conformance record and verdict, and agent system-under-test messages. |
| `operator/v1` | Operator commands, execution results, telemetry, receipts, and service RPC definitions. |
| `pubsub/v1` | Pub/sub event and message envelopes. |

| Output | Generator | Make target |
| :--- | :--- | :--- |
| Go packages beside `protocol/proto/ztd/` | Root `buf.gen.yaml` (`protoc-gen-go`) | `make proto-go` |
| Markdown under `protocol/docs/reference/api/` | Root `buf.gen.yaml` (`protoc-gen-doc`) | `make proto-go` |
| TypeScript under `protocol/node/src/gen/` | `protocol/node/buf.gen.yaml` | `make proto-node` |

| Path | Owns |
| :--- | :--- |
| `protocol/constants/` | External JSON registries. Go runtime constants in `internal/constants/` remain implementation source for values platform executes. When both exist, update both through established owner (`INV-TYPE-06`). |
| `protocol/models/`, `protocol/schemas/` | JSON model shapes and validation schemas. |
| `protocol/vectors/`, `protocol/test-fixtures/` | Cross-language canonicalization vectors and fixtures. |
| `protocol/conformance/` | Cross-language constants, model, and hash parity tests. |
| `protocol/node/` | TypeScript protocol package. |
| `examples/` | Runnable Go examples, external-console app, and MCP client configuration templates. |
| `protocol/docs/` | Protocol specifications and generated API references. |
| `internal/tools/constgen/` | Regenerates event and action-type constants from `protocol/constants/events.json` and `protocol/constants/status.json`. `make constants-generate` writes. `make constants-check` verifies. `check_env.go` also validates `protocol/constants/env_vars.json` and requires every `secret` and `user_endpoint` key in repo-root `.env.example`. Outputs include `internal/constants/events_gen.go`, `internal/constants/action_types_gen.go`, and `console/src/generated/events.ts`. |
| `protocol/constants/agenttools/` | Agent tool registry JSON (`agent-tool-registry.json`) and `registry.go`. The JSON is checked-in source data; no exporter or root Makefile target regenerates it. |

### Native tools

| Path | Owns |
| :--- | :--- |
| `internal/services/mcp/native_tool_registry.go` | `RegisterNativeTools`. The inventory is this function (`INV-MCP-03`). Do not duplicate tool list into documentation. |

### Compliance and reporting

| Surface | Path | CLI |
| :--- | :--- | :--- |
| CSV evidence | `internal/services/reporting/` | `ztd report` |
| Proof-backed compliance | `internal/services/compliance/` | `ztd compliance` |
| Runtime path constants | `internal/constants/paths.go` | |
| External path reference | `protocol/constants/compliance_paths.json` | |

Evidence scope and signed artifacts are owned by [Release Process](release_process.md). Control-alignment prose is [Compliance Alignment](../reference/compliance-alignment.md).

### Other product components

| Component | Entry | Also |
| :--- | :--- | :--- |
| Console | `console/src/main.tsx` | React + TypeScript SPA; Gateway serves embedded build (`internal/services/gateway/console/`) at `/console/`. Browser calls only Gateway. Tests in `console/src/**/*.test.ts(x)` and `console/tests/`. Docs: [Console Architecture & Development](../architecture/console.md). |
| Website | `website/` | Renders root `README.md`. `make website-test`, `make website-build`. Generation policy: [Documentation Guide](docs.md#generated-outputs-inv-doc-gen). |

### Test map

In-depth test rules reside in [Testing Guide](tests.md).

| Command | Tier | Where the work lives |
| :--- | :--- | :--- |
| `./ztd test unit` | 1 | Delegates to `make test-unit` by default; supports `--pkg` and `--run` for targeted testing. Go tests sit beside packages. `make test-unit` depends on `constants-check`. |
| `./ztd test integration` | 2 | `integration` build tag. Cross-package suites in `test/`. Reusable setup in `test/fixtures/`. |
| `./ztd test e2e` | 3 | `e2e` build tag. Tests in `test/e2e/`. Expects an already running platform. |
| `./ztd test coverage` | | Coverage entry in `internal/cli/cmd/test/test.go`. Report layout: [Testing Guide](tests.md). |
| `./ztd test lint` | | CLI lint entry. Repository lint target is `make lint`. |
| `./ztd test chaos` | | Chaos command in `internal/cli/cmd/test/chaos.go`. Engine: `internal/tools/chaos/`. |
| `./ztd test summary` | | Reads chaos summary from test vault. |
| `./ztd test public-loop` | | Provider-free public-feed qualification. Stays in `testcmd` (`INV-PKG-02`). |
| `./ztd test scale` | 3 | Operator fleet scale qualification in `internal/cli/cmd/test/scale.go`: isolated Gateway, `operator deploy` batches, then `TestOperatorFleet_*` in `test/e2e/`. Owns its own Gateway and worker teardown. |
| `./ztd test release-smoke` | | Provider-free governed release qualification in `internal/cli/cmd/test/release_smoke.go`: isolated Gateway and data Operator, governed file and document mutations, local evidence exports, and owned-process cleanup. Runs on Unix-like hosts and Windows. |

Component test entry points named by root Makefile: `make test`, `make test-unit`, `make test-integration`, `make console-test`, `make website-test`.

### Build and validation

Go module and packaging rules are governed by `INV-ENV-01` and [Developer Guidelines](devs.md). `Makefile` `MAIN_PKG` is `./cmd/ztd`.

| Target | Owns |
| :--- | :--- |
| `make build` | Host binary. Writes `bin/ztd-<os>-<arch>` and repo-root copy. Compiles committed explorer and console embeds without building frontends; refresh them with `make explorer-embed` and `make console-embed`. |
| `make proto-generate` | Go and TypeScript binding refresh: `proto-go` and `proto-node` (`INV-PROTO-01`). |
| `make constants-generate`, `make constants-check` | `internal/tools/constgen` write and verify. |
| `make explorer-catalog-generate`, `make explorer-catalog-check` | `evaluation-explorer/src/content/scenario-catalog.generated.ts` generation and verification via `internal/tools/explorercatalog`. |
| `make lint` | `vulncheck`, `doctrines-validate`, then `golangci-lint run` (which also fails on compile errors). |
| `make test`, `make test-unit`, `make test-integration` | Platform tiers. `make test` is unit plus integration. |
| `make console-test`, `make website-test` | Console and website checks. |
| `make fips-build`, `make fips-verify` | Pinned Linux AMD64 FIPS variant. Reference: [FIPS 140-3](../reference/fips140-3.md). |
| `make doctrines-validate` | Doctrine JSON under `protocol/constants/doctrine/`. |
| `make swagger-generate` | Gateway OpenAPI from Swagger annotations. |

The Go tools under `internal/tools/` are catalogued in [Automation Scripts](../architecture/scripts.md#go-tools-directory).

## Procedures

### Find an owner

1. Start with the table in [Owned surfaces](#owned-surfaces). Open the repository-relative path in the corresponding row.
2. If the question is a coding rule, consult [Developer Guidelines](devs.md). If it concerns test execution or fixtures, consult [Testing Guide](tests.md). If it concerns failure triage, consult [Troubleshooting Guide](troubleshooting.md).
3. Confirm CLI flags with `./ztd <command> --help` before documenting (`INV-MAP-02`).

### Confirm the Cobra inventory

1. From repository root:

```bash
./ztd --help
```

2. Compare group list with `rootCmd.AddCommand` in `internal/cli/cmd/main.go` and with [CLI packages](#cli-packages).
3. A new group is a package under `internal/cli/cmd/<group>/` (`INV-CLI-01`). Apply `INV-PKG-01` when directory name collides with an existing import. Do not register `shared`, `gwremote`, or `cmdtest` (`INV-PKG-03`).

### Confirm a runtime mode

1. Gateway HTTP, embedded substrate, and outbound Operator are three distinct owners (`INV-MODE-01`).
2. Embedded claim and pending registration reside in `internal/services/gateway/embedded/`. The HTTP shell resides in `operator_controller.go` (`INV-MODE-02`).
3. Outbound startup resides in `internal/cli/serve/operator.go` into `ZtdoService`. Dependency fields follow the mode table.

### Confirm a generator before editing output

1. Protobuf language outputs use the generator table (`INV-PROTO-01`). Refresh with:

```bash
make proto-generate
```

2. Event and action-type constants:

```bash
make constants-check
```

Write them with `make constants-generate` when registry changes require it.

3. Gateway OpenAPI:

```bash
make swagger-generate
```

4. Hand-editing generated protobuf, constgen, or OpenAPI output is strictly prohibited (`INV-GEN-01`).

## Anti-patterns

- Pasting a coding, filesystem, dependency-construction, or native-tool essay that already has an ID in [Developer Guidelines](devs.md) (`INV-MAP-01`, `INV-MCP-03`).
- Copying the `RegisterNativeTools` list into this file or another document (`INV-MCP-03`).
- Calling `ZtdoService` the embedded substrate, or moving the embedded document owner out of `internal/services/gateway/embedded/` (`INV-MODE-01`, `INV-MODE-02`).
- Registering `shared`, `gwremote`, or `cmdtest` as a Cobra group (`INV-PKG-03`).
- Moving `ztd test public-loop` into `internal/cli/cmd/public/`, or constructing `public restore` anywhere except `eval.PublicRestoreCmdWithConfig` (`INV-PKG-02`).
- Attributing TypeScript protobuf generation to root `buf.gen.yaml` (`INV-PROTO-01`).
- Maintaining a duplicate flag dump in place of directing readers to `./ztd <command> --help` (`INV-ENV-03`).

## Links out

- [Developer Guidelines](devs.md): coding invariants. CLI group placement is `INV-CLI-01`. Gateway construction is [Gateway governance construction](devs.md#gateway-governance-construction).
- [Testing Guide](tests.md): tiers, fixtures, selection, race, coverage, and CI.
- [Documentation Guide](docs.md): audit, catalog, style, generation, and format standards.
- [Release Process](release_process.md): versioning, native evaluation acceptance, and signed evidence.
- [Troubleshooting Guide](troubleshooting.md): diagnostics and recovery procedures.
- [Architecture Overview](../architecture/overview.md), [Gateway Architecture](../architecture/gateway.md), [Operator Architecture](../architecture/operator.md), [Governance Architecture](../architecture/governance.md), [Consensus Architecture](../architecture/consensus.md), [Storage Architecture](../architecture/storage.md), [Protocol Architecture](../architecture/protocol.md).
- [AI Agents and the ztd Governance Boundary](../architecture/agents.md): ingress limits.
- [Protocol README](../../protocol/README.md) and [Protocol Specification](../../protocol/docs/spec.md).
- [Getting Started](../guides/getting_started.md): deployment and enrollment.
- [Console Architecture & Development](../architecture/console.md): embedded frontend architecture.

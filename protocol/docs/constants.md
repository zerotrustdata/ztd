# Protocol Constants System

Last Updated: 2026-10-23
Version: v1.4.0

The ztd platform maintains a centralized, cryptographically verified constant management architecture. The JSON specifications located in `protocol/constants/` represent the **Single Source of Truth (SSOT)** for the registries they cover (collections, status and action types, events, API paths, ports, channels, senders, and env vars). Some constants are hand-maintained Go with no JSON source; section 7 marks them.

Code generators synthesize Go types (`internal/constants/*_gen.go`) and TypeScript frontend definitions (`console/src/generated/events.ts`) directly from these canonical registries. Hand-editing generated constant files is strictly forbidden.

---

## 1. Architecture & Code Generation

### 1.1 The Generation Pipeline
The platform utilizes `internal/tools/constgen` to enforce schema validation and drive code generation across the polyglot monorepo:

```text
protocol/constants/events.json ─────────────────┐
protocol/constants/status.json ─────────────────┼──▶ [ constgen ] ──┬──▶ internal/constants/events_gen.go
internal/tools/constgen/operator_hierarchy.json ┘                   ├──▶ internal/constants/action_types_gen.go
                                                                    └──▶ console/src/generated/events.ts
```

Before generating, `constgen` validates `protocol/constants/env_vars.json` (categories, `_go_const`, names, descriptions) and requires every `secret` and `user_endpoint` key to appear in `.env.example` (`internal/tools/constgen/check_env.go`). The env registry produces no generated output.

Action classifications in `status.action_type` generate `ActionType.IsMutation()`, `IsBootstrapAction()`, `AuthorizedByDecision()`, and `IsStateChange()` from `_mutation`, `_bootstrap`, `_authorized_by_decision`, and `_state_change`, respectively. `ChangesState(target)` combines `_state_change` with `_read_only_targets`. Each predicate matches only explicitly flagged actions and returns false for other values. Empty registries and classifications with no matching actions produce valid Go predicates that always return false; the generator omits the matching case entirely.

### 1.2 Build Automation Targets
- **`make constants-generate`**: Executes `go run ./internal/tools/constgen -write`. Validates the registry syntax, grammar, and taxonomy, then writes the generated Go artifacts (gofmt-formatted) and the TypeScript artifact.
- **`make constants-check`**: Executes `go run ./internal/tools/constgen -check`. Validates the env registry and verifies that committed generated files match the output derived from the registry JSON files, ignoring CRLF line endings. Runs as a prerequisite of `make test-unit` and as a recipe step of `make ci-platform`.

---

## 2. Database Collections (`collections.json`)

The storage architecture defines canonical collection identifiers used across the embedded SQLite database (`ztd.db`). In Go, these are represented by the typed identifier `CollectionName` (`internal/constants/collections.go`). `collections.json` lists exactly the `CollectionName` constants declared in `collections.go`; `TestCollectionsRegistryMatchesGoConstants` fails on drift in either direction.

### 2.1 Collection Inventory

| Collection Key | Go Constant | Value | Governed | Primary Subsystem |
|:---|:---|:---|:---:|:---|
| `users` | `CollectionUsers` | `"users"` | No | Gateway Auth |
| `web_sessions` | `CollectionWebSessions` | `"web_sessions"` | No | Gateway Auth / Sessions |
| `operator_sessions` | `CollectionOperatorSessions`| `"operator_sessions"` | No | Gateway Auth / Sessions |
| `cli_sessions` | `CollectionCLISessions` | `"cli_sessions"` | No | Gateway Auth / Sessions |
| `bound_sessions` | `CollectionBoundSessions` | `"bound_sessions"` | No | Gateway Auth / Sessions |
| `login_audit` | `CollectionLoginAudit` | `"login_audit"` | No | Audit & Compliance |
| `auth_admin_audit` | `CollectionAuthAdminAudit` | `"auth_admin_audit"` | No | Audit & Compliance |
| `account_locks` | `CollectionAccountLocks` | `"account_locks"` | No | Gateway Security |
| `organizations` | `CollectionOrganizations` | `"organizations"` | No | Gateway Multi-Tenancy |
| `operators` | `CollectionOperators` | `"operators"` | No | Operator Inventory |
| `operator_usage` | `CollectionOperatorUsage` | `"operator_usage"` | No | Telemetry & Quotas |
| `cases` | `CollectionCases` | `"cases"` | **Yes** | Governed App Store |
| `investigations` | `CollectionInvestigations` | `"investigations"` | **Yes** | Governed App Store |
| `tasks` | `CollectionTasks` | `"tasks"` | **Yes** | Governed App Store |
| `memories` | `CollectionMemories` | `"memories"` | **Yes** | Governed App Store |
| `settings` | `CollectionSettings` | `"settings"` | No | Gateway Configuration |
| `passkey_challenges`| `CollectionPasskeyChallenges`| `"passkey_challenges"` | No | FIDO2 / WebAuthn Broker |
| `personas` | `CollectionPersonas` | `"personas"` | No | Agent Configuration |
| `agent_activity_metadata` | `CollectionAgentActivityMetadata` | `"agent_activity_metadata"` | **Yes** | Governed App Store |
| `reputation_state` | `CollectionReputationState`| `"reputation_state"` | **Yes** | Staking & Reputation |
| `reputation_commitments` | `CollectionReputationCommitments` | `"reputation_commitments"` | **Yes** | Staking & Reputation |
| `stake_resolutions` | `CollectionStakeResolutions` | `"stake_resolutions"` | **Yes** | Staking & Reputation |
| `revoked_certificates` | `CollectionRevokedCertificates` | `"revoked_certificates"` | No | PKI Management |
| `trusted_signers` | `CollectionTrustedSigners` | `"trusted_signers"` | No | L2 Consensus Store |
| `app_policies` | `CollectionAppPolicies` | `"app_policies"` | No | Governance Engine |
| `consensus` | `CollectionConsensus` | `"consensus"` | No | L2 Consensus Store |
| `enrollment_tokens`| `CollectionEnrollmentTokens`| `"enrollment_tokens"` | No | PKI Enrollment |
| `cli_recovery_requests` | `CollectionCLIRecoveryRequests` | `"cli_recovery_requests"` | No | Gateway Auth |
| `platform_enrollments` | `CollectionPlatformEnrollments` | `"platform_enrollments"` | No | Workload PKI Enrollment |

The `observe_*` projections and `eval_campaign_publication_state` (section 2.3) are also `CollectionName` constants in `collections.json`; none carries `_governed`.

### 2.2 Governed Collection Access Control (INV-GOV-WARD-06)
Collections marked `_governed: true` in `collections.json` are the **only** collections that external application workloads may mutate via `DOCUMENT_UPDATE` and `DOCUMENT_DELETE` actions:
- `cases`
- `tasks`
- `investigations`
- `memories`
- `agent_activity_metadata`
- `reputation_state`
- `reputation_commitments`
- `stake_resolutions`

The L4 Warden verifies `collection.IsGovernedDocument()` prior to dispatch. Any `DOCUMENT_UPDATE` or `DOCUMENT_DELETE` envelope targeting a non-governed collection (such as `users`, `trusted_signers`, or `app_policies`) is rejected fail-closed. Platform collections may only be written by their authoritative internal Gateway services.

### 2.3 Observe Projections
Read-only projections for dashboard and telemetry consumers query dedicated un-governed collections:
- `observe_agent_states`, `observe_runs`, `observe_evals`, `observe_downloads`
- `observe_supervisor_states`, `observe_cycle_states`, `observe_assignment_progress`
- `observe_verification_progress`, `observe_publication_progress`
- `observe_source_freshness`, `eval_campaign_publication_state`

---

## 3. Event Registry (`events.json`)

The event registry in `protocol/constants/events.json` records every wire event across the entire system.

### 3.1 Registry Schema
Each entry in `events.json` adheres to a strict schema validated by `constgen`:

```json
{
  "_go_const": "EventOperatorCommandRequested",
  "value": "ztd.v1.operator.command.requested",
  "kind": "request",
  "transport": ["governed"],
  "governance": {
    "action_type": "EXECUTE_BASH",
    "payload": "ztd.operator.v1.CommandRequested"
  },
  "producers": ["cli", "mcp"],
  "persistence": "ephemeral",
  "outcomes": [
    "OperatorCommandCompleted",
    "OperatorCommandFailed",
    "OperatorCommandCancelled",
    "OperatorCommandStatusUpdatedQueued",
    "OperatorCommandStatusUpdatedRunning",
    "OperatorCommandStatusUpdatedCompleted",
    "OperatorCommandStatusUpdatedFailed",
    "OperatorCommandStatusUpdatedCancelled"
  ]
}
```

- **`kind`**: Must be one of `request`, `outcome`, `fact`, `stream`.
- **`transport`**: Allowed values are `governed`, `pubsub`, and `sse`. Any `governance` request must include `governed`.
- **`producers`**: Components authorized to emit the event (`gateway`, `operator`, `cli`, `mcp`).
- **`persistence`**: Storage target (`operator.audit_log`, `gateway.audit_log`, `gateway.sse_store`, `gateway.operator_docs`, `gateway.docstore`, `ephemeral`).
- **`governance`**: Links request events directly to their `action_type` and Protobuf payload type.

### 3.2 Canonical Grammar
Wire event strings must strictly follow the canonical format:

$$\texttt{ztd.v1.} \langle\text{domain}\rangle \texttt{.} \langle\text{entity}\rangle \texttt{.} \langle\text{terminal}\rangle$$

Permitted terminals come from the closed `allowedTerminals` list in `internal/tools/constgen/main.go` and represent lifecycle states, including (`requested`, `started`, `received`, `completed`, `failed`, `cancelled`, `timeout`, `recorded`, `granted`, `rejected`, `denied`, `revoked`, `expired`, `acknowledged`, `sent`, `missed`, `bound`, `unbound`, `opened`, `closed`, `established`, `checkpointed`, `exported`, `published`, `rotated`).

`checkGrammar` accepts a terminal outside that list only for `.status.updated.` values and stream-shaped segments (`.stream.`, `.chunk.`, `.delta.`, `.keepalive.`, `.thinking.`).

### 3.3 Dynamic Action Resolution
The governance engine determines the action class of any governed request via `constants.Registry.ActionFor(event)`. Hand-coded mapping tables are strictly prohibited.

### 3.4 Hierarchical Go Accessors
The generated file `internal/constants/events_gen.go` exposes a nested `Event` struct accessor for type-safe code navigation. Its only root is `Event.Operator`, and its paths come from `internal/tools/constgen/operator_hierarchy.json`. Every other event is referenced by its flat `Event<Name>` constant. The nested groups under `Event.Operator` are `A2a`, `Audit`, `Bootstrap`, `Command` (including `Command.StatusUpdated`), `Eval` (including `Eval.WorkspaceSeed`), `FetchFileDiff`, `FetchFileHistory`, `FetchHistory`, `FetchLogs`, `FileEdit`, `FsGrep`, `FsList`, `FsRead`, `Inference`, `Intent`, `Mcp`, `ModelProvenanceObservation`, `NetworkPing`, `Notary`, `OllamaModelInventory`, `OllamaModelRelease`, `OllamaModelResidency`, `PortCheck`, `ProviderBoundaryObservation`, `Receipt`, `RestoreFile`, and `StatusUpdated`. Ungrouped operator events, such as `Event.Operator.Bound`, are `EventType` fields.

---

## 4. Action Types (`status.json` → `action_types_gen.go`)

Action types define the coarse-grained operational classes that the governance services (`internal/services/governance/`) evaluate. They are declared under `status.action_type` in `protocol/constants/status.json` and generated into `internal/constants/action_types_gen.go`. This section defines the flags. The classification of each action is the `status.action_type` object itself and is not copied here.

### 4.1 Action Classification
Each `status.action_type` entry has a `value` (the wire string), a `_go_const`, and optional flags:
- `_mutation`: the action modifies system state. Generates `IsMutation()`.
- `_bootstrap`: a platform enrollment bootstrap action. Generates `IsBootstrapAction()`.
- `_authorized_by_decision`: an enrollment action that carries out an owner decision already committed under the L3 gate. Generates `AuthorizedByDecision()`. Requires `_bootstrap`.
- `_state_change`: executing the action changes state. Generates `IsStateChange()`. Requires `_mutation`.
- `_read_only_targets`: constants identifiers of target resources against which a `_state_change` action changes no state. Generates the read-only branch of `ChangesState(target)`. Requires `_state_change`.

The loader in `internal/tools/constgen/generate_actions.go` rejects any classification that violates a "Requires" rule.

### 4.2 Mutation Gating
`IsMutation()` is consumed only by `governance.L3Required` (`internal/services/governance/l4_warden.go`). Under postures that require L3 (see `RequiresL3` in `internal/constants/posture.go`), a mutation action that is not `AuthorizedByDecision()` MUST carry an L3 notary proof. No other non-test code reads the mutation flag.

### 4.3 State-Change Gating
`ChangesState(targetResource)` is consumed by `governance.L2Enforced` (`internal/services/governance/l2_consensus.go`), the single L2 gate, together with `IsBootstrapAction()`. Under postures that require L2 (see `RequiresL2` in `internal/constants/posture.go`), only a non-bootstrap action for which `ChangesState` holds is voted on. `ChangesState` holds for a `_state_change` action against every target except its `_read_only_targets`, so an empty or unknown target is a state change.

Governed inference (`INFERENCE`) and Ollama model release (`OLLAMA_MODEL_RELEASE`) are `_mutation` without `_state_change`: L3 gates them and L2 does not.

---

## 5. API Paths Registry (`api_paths.json` / `api_paths.go`)

Route values are defined in `protocol/constants/api_paths.json`. The `APIPaths` struct in `internal/constants/api_paths.go` exposes them as Go fields for Gateway REST, mTLS, and control-plane controllers. The list below is a partial index of route groups, not the full route set:

### 5.1 Route Groups

- **Control & Ingress**:
  - `MCPEndpoint`: `/mcp`
  - `A2ACall`: `/api/v1/a2a/call`
  - `GovernanceEnvelopes`: `/api/v1/governance/envelopes`
  - `OperatorsCommands`: `/api/v1/operators/commands`
  - `OperatorsIntents`: `/api/v1/operators/intents`
- **Session & Identity**:
  - `AuthPasskeys*`: Passkey registration, verification, and authentication routes
  - `AuthCLISession`: `/api/v1/auth/cli/session`
  - `AuthPlatformEnrollment*`: Workload PKI enrollment routes
  - `Approvals`: `/api/v1/approvals`
  - `ApprovePage`: `/api/v1/approve/`
- **Observe Read Models (User-Scoped, WebSession Authenticated)**:
  - `ObservePrefix`: `/api/v1/observe/`
  - `ObserveBootstrap`: `/api/v1/observe/bootstrap`
  - `ObserveRuns`, `ObserveEvals`, `ObserveDownloads`
- **Observe Producers (mTLS Authenticated)**:
  - `ObserveProducerPrefix`: `/api/v1/observe/producer/`
  - `ObserveProducerAgentState`: `/api/v1/observe/producer/agent-state`
  - `ObserveProducerRunState`: `/api/v1/observe/producer/run-state`
  - `ObserveProducerStop`: `/api/v1/observe/producer/stop`
- **Audit & Forensics**:
  - `AuditEventReceipts`: `/api/v1/audit/event-receipts`
  - `AuditEventReceiptsExport`: `/api/v1/audit/event-receipts/export`
  - `AuditConsensusVotes`: `/api/v1/audit/consensus-votes`
  - `AuditEvents`: `/api/v1/audit/events`
  - `AuditSummary`: `/api/v1/audit/summary`
- **Streaming & PubSub**:
  - `SSEEvents`: `/api/v1/sse/events`
  - `PubSubWebSocket`: `/ws/pubsub`

---

## 6. Environment Variables SSOT (`env_vars.json` / `env_vars.go`)

The platform explicitly declares all recognized environment variables in `protocol/constants/env_vars.json`. Each entry defines a `category` of `host`, `secret`, `test`, or `user_endpoint`.

### 6.1 Invariant INV-ENV-04: Environment Access Through the Registry
Environment variables MUST be read only through typed keys in `protocol/constants/env_vars.json`, each declared with one `category`: `host`, `secret`, `test`, or `user_endpoint`. Platform configuration (ports, postures, directory locations, and timeouts) MUST NOT be read from environment variables. It is supplied by explicit CLI command flags or strongly typed code defaults.

Production Go code accesses the environment solely via typed constants defined on `constants.EnvVar`:
- **Host Facts**: `EnvVar.Shell` (`SHELL`), `EnvVar.Lang` (`LANG`), `EnvVar.Term` (`TERM`), `EnvVar.TZ` (`TZ`), `EnvVar.Home` (`HOME`), `EnvVar.User` (`USER`), `EnvVar.SSHAuthSock` (`SSH_AUTH_SOCK`), `EnvVar.XDGRuntimeDir` (`XDG_RUNTIME_DIR`), `EnvVar.DBusSessionBusAddress` (`DBUS_SESSION_BUS_ADDRESS`), `EnvVar.Display` (`DISPLAY`), `EnvVar.WaylandDisplay` (`WAYLAND_DISPLAY`), `EnvVar.Browser` (`BROWSER`).
- **Secrets**: `EnvVar.CloudflareAPIToken` (`CLOUDFLARE_API_TOKEN`).
- **User Endpoints**: `EnvVar.OllamaEndpoint` (`ZTD_OLLAMA_ENDPOINT`), the environment fallback for `--inference-ollama-endpoint`. The flag wins when set; otherwise the built-in default is `http://127.0.0.1:11434`.
- **Test Harness**: the `EnvVar.E2E*` keys (`ZTD_E2E_RUNTIME_ROOT`, `ZTD_E2E_GATEWAY_HTTP_PORT`, `ZTD_E2E_GATEWAY_HTTPS_PORT`, and the `ZTD_E2E_FLEET_*` keys), set by `ztd test scale` and read by the E2E suite under `test/e2e/`.

Raw `os.Getenv("...")` calls with hardcoded strings or locally declared string constants are prohibited, in production Go code and in the `test/` harness; `TestProductionGoEnvReads_UseRegistryKeys` enforces this. In-process `ZTD_TEST_*` re-exec markers are exempt. `config.LoadEnvFile` (`internal/cli/config/env_file.go`) loads secret and user_endpoint keys from `.env` in the working directory (`constants.EnvFilename`) into the process environment. The root command runs it after its pre-run gates pass, so a refused command does not populate the environment. A variable that is already set wins.

---

## 7. Operational & System Registries

### 7.1 Channels (`channels.json` / `channels.go`)
Pub/Sub channels enforce structured naming conventions:
- **Command Channels**: `cmd:{operator_id}:{operator_session_id}` (`ChannelPrefixCmd = "cmd"`)
- **Result Channels**: `results:{operator_id}:{operator_session_id}` (`ChannelPrefixResults = "results"`)
- **Heartbeat Channels**: `heartbeat:{operator_id}:{operator_session_id}` (`ChannelPrefixHeartbeat = "heartbeat"`)
- **System Channels**: `ChannelStorageDocument`, `ChannelStorageKv`, `ChannelStorageBlob`, `ChannelGovernance`, `ChannelOperatorIntent`, `ChannelOperatorDevice`, `ChannelSseEvent`
- **Additional Prefixes**: `receipts`, `audit`, and `renewal` (`ChannelPrefixReceipts`, `ChannelPrefixAudit`, `ChannelPrefixRenewal`); SSE channels `sse:cli` and `sse:web` (`ChannelPrefixSSECLI`, `ChannelPrefixSSEWeb`)

### 7.2 Network Ports (`ports.json` / `ports.go`)
- `OperatorHttp` (`8080`): Gateway HTTP port (plaintext): health, PKI bootstrap and discovery, CLI recovery and platform enrollment discovery, and deploy script distribution; all other paths redirect to HTTPS.
- `OperatorHttps` (`8443`): Gateway HTTPS port (mTLS, for all operator traffic).

`ports.go` also declares Gateway-owned deployment defaults that are not in `ports.json` and are not protocol ports: the public spectator ingest and read listeners (`8081`, `8082`), the evaluation explorer (`5173`), a suggested pprof port (`6060`), and Ollama's loopback API default (`11434`).

### 7.3 Message Senders (`senders.json` / `senders.go`)
Defines message attribution sources:
- `SourceUserChat` (`"ztd.v1.source.user.chat"`)
- `SourceUserTerminal` (`"ztd.v1.source.user.terminal"`)
- `SourceSystem` (`"ztd.v1.source.system"`)

Message content types: `text`, `code`, `call`, `result`, `error`, `thinking`.

`internal/constants/senders.go` is hand-maintained. `constgen` does not generate it, so keep it in step with `senders.json` by hand.

### 7.4 Protocol RPC Error Codes (`internal/constants/rpc_errors.go`)
`rpc_errors.go` is hand-maintained Go with no JSON source. It defines the protocol error codes for governance verification, including:
- `ErrCodeInvalidEnvelope` (`-32000`)
- `ErrCodeHashMismatch` (`-32001`)
- `ErrCodeExpired` (`-32002`)
- `ErrCodeReplay` (`-32003`)
- `ErrCodeStateMismatch` (`-32004`)
- `ErrCodeL1ValidationFailed` (`-32005`)
- `ErrCodeL2SignatureInvalid` (`-32006`)
- `ErrCodeL3ProofInvalid` (`-32007`)
- `ErrCodePayloadDecodeFailed` (`-32008`)
- `ErrCodeResourceNotFound` (`-32100`)
- `ErrCodeGatewayNotReady` (`-32101`)

The file's comment reserves the range -32000 to -32099; `-32100` and `-32101` sit outside it.

# Protocol Constants System

Last Updated: 2026-10-12
Version: v1.1.1

The ztd platform maintains a centralized, cryptographically verified constant management architecture. The JSON specifications located in `protocol/constants/` represent the absolute **Single Source of Truth (SSOT)** for all platform contracts, collection definitions, action types, event registries, route paths, and configuration identifiers.

Code generators synthesize Go types (`internal/constants/*_gen.go`) and TypeScript frontend definitions (`console/src/generated/events.ts`) directly from these canonical registries. Hand-editing generated constant files is strictly forbidden.

---

## 1. Architecture & Code Generation

### 1.1 The Generation Pipeline
The platform utilizes `internal/tools/constgen` to enforce schema validation and drive code generation across the polyglot monorepo:

```
protocol/constants/events.json ─────────────────┐
protocol/constants/status.json ─────────────────┼──▶ [ constgen ] ──┬──▶ internal/constants/events_gen.go
internal/tools/constgen/operator_hierarchy.json ┘                   ├──▶ internal/constants/action_types_gen.go
                                                                    └──▶ console/src/generated/events.ts
```

Before generating, `constgen` validates `protocol/constants/env_vars.json` (categories, `_go_const`, names, descriptions) and requires every `secret` and `user_endpoint` key to appear in `.env.example` (`internal/tools/constgen/check_env.go`). The env registry produces no generated output.

### 1.2 Build Automation Targets
- **`make constants-generate`**: Executes `go run ./internal/tools/constgen -write`. Validates the registry syntax, grammar, and taxonomy, then writes out the formatted Go and TypeScript artifacts.
- **`make constants-check`**: Executes `go run ./internal/tools/constgen -check`. Runs within CI to validate the env registry and ensure committed generated files are byte-for-byte identical to the output derived from the registry JSON files.

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

The L4 Warden verifies `collection.IsGovernedDocument()` prior to dispatch. Any envelope targeting non-governed collections (such as `users`, `trusted_signers`, or `app_policies`) from an external workload is rejected fail-closed. Platform collections may only be mutated by their authoritative internal Gateway services.

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
- **`transport`**: Allowed values include `governed`, `pubsub`, `sse`. Any `governance` request must include `governed`.
- **`producers`**: Components authorized to emit the event (`gateway`, `operator`, `cli`, `mcp`).
- **`persistence`**: Storage target (`operator.audit_log`, `gateway.audit_log`, `gateway.sse_store`, `gateway.operator_docs`, `gateway.docstore`, `ephemeral`).
- **`governance`**: Links request events directly to their `action_type` and Protobuf payload type.

### 3.2 Canonical Grammar
Wire event strings must strictly follow the canonical format:

$$\texttt{ztd.v1.} \langle\text{domain}\rangle \texttt{.} \langle\text{entity}\rangle \texttt{.} \langle\text{terminal}\rangle$$

Permitted terminals come from the closed `allowedTerminals` list in `internal/tools/constgen/main.go` and represent lifecycle states, including (`requested`, `started`, `received`, `completed`, `failed`, `cancelled`, `timeout`, `recorded`, `granted`, `rejected`, `denied`, `revoked`, `expired`, `acknowledged`, `sent`, `missed`, `bound`, `unbound`, `opened`, `closed`, `established`, `checkpointed`, `exported`, `published`, `rotated`).

### 3.3 Dynamic Action Resolution
The governance engine determines the action class of any governed request via `constants.Registry.ActionFor(event)`. Hand-coded mapping tables are strictly prohibited.

### 3.4 Hierarchical Go Accessors
The generated file `internal/constants/events_gen.go` exposes a nested `Event` struct accessor for type-safe code navigation. Its only root is `Event.Operator`, and its paths come from `internal/tools/constgen/operator_hierarchy.json`; every other event is referenced by its flat `Event<Name>` constant:
- `Event.Operator.Command.*`
- `Event.Operator.FileEdit.*`
- `Event.Operator.FsList.*`, `Event.Operator.FsRead.*`, `Event.Operator.FsGrep.*`
- `Event.Operator.Mcp.*`, `Event.Operator.A2a.*`
- `Event.Operator.NetworkPing.*`, `Event.Operator.PortCheck.*`
- `Event.Operator.Intent.*`

---

## 4. Action Types (`status.json` → `action_types_gen.go`)

Action types define the coarse-grained operational classes evaluated by L1 Doctrine, L4 Warden, and L5 Actuator. They are generated from `protocol/constants/status.json` into `internal/constants/action_types_gen.go`.

### 4.1 Action Classification

| Action Type Identifier | String Constant | Mutation (`IsMutation()`) | Bootstrap (`IsBootstrapAction()`) | Description |
|:---|:---|:---:|:---:|:---|
| `ActionTypeA2aCall` | `"A2A_CALL"` | **Yes** | No | Agent-to-Agent skill invocation |
| `ActionTypeCancel` | `"CANCEL"` | **Yes** | No | Command cancellation request |
| `ActionTypeDocumentDelete` | `"DOCUMENT_DELETE"` | **Yes** | No | Governed collection document removal |
| `ActionTypeDocumentUpdate` | `"DOCUMENT_UPDATE"` | **Yes** | No | Governed collection document update |
| `ActionTypeExecuteBash` | `"EXECUTE_BASH"` | **Yes** | No | Host shell command execution |
| `ActionTypeFileEdit` | `"FILE_EDIT"` | **Yes** | No | Host filesystem file modification |
| `ActionTypeMcpCall` | `"MCP_CALL"` | **Yes** | No | Model Context Protocol tool execution |
| `ActionTypeRestoreFile` | `"RESTORE_FILE"` | **Yes** | No | Git ledger file revision restoration |
| `ActionTypeShutdown` | `"SHUTDOWN"` | **Yes** | No | Operator shutdown request |
| `ActionTypeInference` | `"INFERENCE"` | **Yes** | No | Governed model inference |
| `ActionTypePlatformEnrollmentDecide` | `"PLATFORM_ENROLLMENT_DECIDE"` | **Yes** | **Yes** | Owner decision on workload enrollment |
| `ActionTypePlatformEnrollmentIssue` | `"PLATFORM_ENROLLMENT_ISSUE"` | **Yes** | **Yes** | Platform certificate issuance |
| `ActionTypePlatformEnrollmentPersistPolicy` | `"PLATFORM_ENROLLMENT_PERSIST_POLICY"` | **Yes** | **Yes** | Persisting app policy for enrolled node |
| `ActionTypePlatformEnrollmentCreateSession` | `"PLATFORM_ENROLLMENT_CREATE_SESSION"` | **Yes** | **Yes** | Initial session creation post-enrollment |
| `ActionTypePlatformEnrollmentRevoke` | `"PLATFORM_ENROLLMENT_REVOKE"` | **Yes** | **Yes** | Platform enrollment revocation |
| `ActionTypePlatformEnrollmentCreate` | `"PLATFORM_ENROLLMENT_CREATE"` | No | **Yes** | Inbound enrollment CSR submission |
| `ActionTypeEvalAnswer` | `"EVAL_ANSWER"` | No | No | Evaluation answer submission |
| `ActionTypeFetchFileDiff` | `"FETCH_FILE_DIFF"` | No | No | Ledger diff inspection |
| `ActionTypeFetchFileHistory` | `"FETCH_FILE_HISTORY"` | No | No | Ledger commit history inspection |
| `ActionTypeFetchHistory` | `"FETCH_HISTORY"` | No | No | Command execution history query |
| `ActionTypeFetchLogs` | `"FETCH_LOGS"` | No | No | Execution output log fetch |
| `ActionTypeFsGrep` | `"FS_GREP"` | No | No | Filesystem regex search |
| `ActionTypeFsList` | `"FS_LIST"` | No | No | Directory listing |
| `ActionTypeFsRead` | `"FS_READ"` | No | No | File read operation |
| `ActionTypeHeartbeat` | `"HEARTBEAT"` | No | No | Node liveness check |
| `ActionTypeMcpPromptGet` | `"MCP_PROMPT_GET"` | No | No | MCP prompt template retrieval |
| `ActionTypeMcpPromptList` | `"MCP_PROMPT_LIST"` | No | No | MCP prompt template listing |
| `ActionTypeMcpResourceList` | `"MCP_RESOURCE_LIST"` | No | No | MCP resource enumeration |
| `ActionTypeMcpResourceRead` | `"MCP_RESOURCE_READ"` | No | No | MCP resource data retrieval |
| `ActionTypeModelProvenanceObservation` | `"MODEL_PROVENANCE_OBSERVATION"` | No | No | Model provenance observation |
| `ActionTypeOllamaModelInventory` | `"OLLAMA_MODEL_INVENTORY"` | No | No | Ollama model inventory query |
| `ActionTypeOllamaModelResidency` | `"OLLAMA_MODEL_RESIDENCY"` | No | No | Ollama model residency query |
| `ActionTypePortCheck` | `"PORT_CHECK"` | No | No | Network TCP port connectivity check |
| `ActionTypeProviderBoundaryObservation` | `"PROVIDER_BOUNDARY_OBSERVATION"` | No | No | Provider boundary observation |

### 4.2 Mutation Gating
Actions where `IsMutation() == true` require full L4 Warden state Merkle root validation, JIT capability minting, git ledger tracking, and L5 signed action receipt generation. Read-only actions bypass ledger attestation and state root mutation checks.

---

## 5. API Paths Registry (`api_paths.json` / `api_paths.go`)

HTTP route paths for Gateway REST, mTLS, and control-plane controllers are exposed via the `APIPaths` struct in `internal/constants/api_paths.go`:

### 5.1 Route Groups

- **Control & Ingress**:
  - `MCPEndpoint`: `/mcp`
  - `A2ACall`: `/api/v1/a2a/call`
  - `GovernanceEnvelopes`: `/api/v1/governance/envelopes`
  - `OperatorsCommands`: `/api/v1/operators/commands`
  - `ConsensusDeliberate`: `/consensus/v1/deliberate`
- **Session & Identity**:
  - `AuthPasskeys*`: Passkey registration, verification, and authentication routes
  - `AuthCLISession`: `/api/v1/auth/cli/session`
  - `AuthPlatformEnrollment*`: Workload PKI enrollment routes
  - `Approvals`: `/api/v1/approvals`
  - `ApprovePage`: `/api/v1/approve/`
- **Observe Read Models (User-Scoped, Browser Cookie Authenticated)**:
  - `ObservePrefix`: `/api/v1/observe/`
  - `ObserveBootstrap`: `/api/v1/observe/bootstrap`
  - `ObserveRuns`, `ObserveEvals`, `ObserveDownloads`
- **Observe Producers (mTLS Authenticated)**:
  - `ObserveProducerPrefix`: `/api/v1/observe/producer/`
  - `ObserveProducerAgentState`: `/api/v1/observe/producer/agent-state`
  - `ObserveProducerRunState`: `/api/v1/observe/producer/run-state`
- **Audit & Forensics**:
  - `AuditReceipts`: `/api/v1/audit/receipts`
  - `AuditReceiptsExport`: `/api/v1/audit/receipts/export`
  - `AuditEvents`: `/api/v1/audit/events`
  - `AuditSummary`: `/api/v1/audit/summary`
- **Streaming & PubSub**:
  - `SSEEvents`: `/api/v1/sse/events`
  - `PubSubWebSocket`: `/ws/pubsub`

---

## 6. Environment Variables SSOT (`env_vars.json` / `env_vars.go`)

The platform explicitly declares all recognized environment variables in `protocol/constants/env_vars.json`. Each entry defines a `category` of `host`, `secret`, `test`, or `user_endpoint`.

### 6.1 Invariant INV-ENV-04: No Runtime Configuration via Environment
Platform operational configuration is **never** loaded from environment variables. All parameters (ports, postures, directory locations, downstream endpoints, timeouts) must be supplied via explicit CLI command flags or strongly typed code defaults.

Production Go code accesses the environment solely via typed constants defined on `constants.EnvVar`:
- **Host Facts**: `EnvVar.Shell` (`SHELL`), `EnvVar.Lang` (`LANG`), `EnvVar.Term` (`TERM`), `EnvVar.TZ` (`TZ`), `EnvVar.Home` (`HOME`), `EnvVar.User` (`USER`), `EnvVar.SSHAuthSock` (`SSH_AUTH_SOCK`), `EnvVar.XDGRuntimeDir` (`XDG_RUNTIME_DIR`), `EnvVar.DBusSessionBusAddress` (`DBUS_SESSION_BUS_ADDRESS`).
- **Secrets**: `EnvVar.CloudflareAPIToken` (`CLOUDFLARE_API_TOKEN`).
- **User Endpoints**: `EnvVar.OllamaEndpoint` (`ZTD_OLLAMA_ENDPOINT`), the default for `--inference-ollama-endpoint`.
- **Test Harness**: the `EnvVar.E2E*` keys (`ZTD_E2E_RUNTIME_ROOT`, `ZTD_E2E_GATEWAY_HTTP_PORT`, `ZTD_E2E_GATEWAY_HTTPS_PORT`, and the `ZTD_E2E_FLEET_*` keys), set by `ztd test scale` and read by the E2E suite under `test/e2e/`.

Raw `os.Getenv("...")` calls with hardcoded strings or locally declared string constants are prohibited, in production Go code and in the `test/` harness; `TestProductionGoEnvReads_UseRegistryKeys` enforces this. In-process `ZTD_TEST_*` re-exec markers are exempt. `config.LoadEnvFile` (`internal/cli/config/env_file.go`) loads secret and user_endpoint keys from the working-directory `.env` (the repository root) into the process environment before every `./ztd` command; an already exported variable wins.

---

## 7. Operational & System Registries

### 7.1 Channels (`channels.json` / `channels.go`)
Pub/Sub channels enforce structured naming conventions:
- **Command Channels**: `cmd:{operator_id}:{operator_session_id}` (`ChannelPrefixCmd = "cmd"`)
- **Result Channels**: `results:{operator_id}:{operator_session_id}` (`ChannelPrefixResults = "results"`)
- **Heartbeat Channels**: `heartbeat:{operator_id}:{operator_session_id}` (`ChannelPrefixHeartbeat = "heartbeat"`)
- **System Channels**: `ChannelStorageDocument`, `ChannelStorageKv`, `ChannelStorageBlob`, `ChannelGovernance`

### 7.2 Network Ports (`ports.json` / `ports.go`)
- `OperatorHttp` (`8080`): Bootstrap discovery, CLI recovery, deploy script distribution, and loopback MCP ingress.
- `OperatorHttps` (`8443`): Production mTLS surface, public API routes, authenticated console SPA, and external MCP/A2A ingress.

### 7.3 Message Senders (`senders.json` / `senders.go`)
Defines message attribution sources:
- `SourceUserChat` (`"ztd.v1.source.user.chat"`)
- `SourceUserTerminal` (`"ztd.v1.source.user.terminal"`)
- `SourceSystem` (`"ztd.v1.source.system"`)

Message content types: `text`, `code`, `call`, `result`, `error`, `thinking`.

### 7.4 Standard Protocol RPC Errors (`rpc_errors.go`)
Reserved JSON-RPC protocol error codes for governance verification:
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

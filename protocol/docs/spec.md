# ztd Protocol Specification

Last Updated: 2026-10-23
Version: v1.3.0

The **ztd Protocol** is a zero-trust execution and evidence platform for autonomous AI agents, human operators, and distributed target runtimes. It establishes a cryptographically enforced, typed wire contract that binds identity, intent, state, and governance proofs into an immutable transaction envelope (`GovernanceEnvelope`). Every mutation passing through the platform is subjected to an uncompromising 5-layer interlock sequence before execution.

**Core Axiom**: *A model can ask, but it cannot directly act.*

---

## 1. Architectural Foundations

### 1.1 Wire Format and Serialization
- **External Wire Ingress & Egress**: Canonical JSON (`protojson`) is mandatory for all client-facing and external interfaces, including HTTP REST endpoints, WebSocket pub/sub command/result channels (`/ws/pubsub`), and Server-Sent Events (SSE).
- **Internal Execution Substrate**: Binary Protocol Buffers (proto3) schemas defined in `protocol/proto/ztd/` serve as the single authoritative schema source. Runtimes decode canonical JSON into strongly typed protobuf messages immediately upon ingress.
- **Fail-Closed Principle**: Any structural invalidity, missing required field, unsupported field type, expired timestamp, reused nonce, stale state root, or failed cryptographic verification immediately rejects the envelope prior to dispatch.

### 1.2 Canonical Hostname and Peer Addressing
- **Canonical Internal Hostname**: `ztd.local` is the Gateway's internal hostname. Its certificate is issued to this name. Operators and CLI clients that dial a Gateway by IP address use `ztd.local` as the TLS ServerName.
- **Dial Fallback**: A client resolves `ztd.local` through the system resolver. When it does not resolve, the client dials loopback. The TLS ServerName stays `ztd.local`.

### 1.3 Execution Sovereignty
- **Policy Decision Point (PDP)**: The ztd Gateway receives typed requests from clients and tools, verifies session credentials, coordinates consensus deliberation, brokers notary approvals, and prepares the canonical transaction envelope.
- **Policy Execution Point (PEP)**: The host-side ztd Operator is the exclusive execution boundary. The Operator independently verifies all cryptographic proofs, state roots, and doctrine rules. No bundled Gateway component possesses privileged bypass channels.

---

## 2. GovernanceEnvelope Schema Specification

The `GovernanceEnvelope` is the universal container for all mutations in the ztd platform. Its normative definition is maintained in `protocol/proto/ztd/common/v1/common.proto`.

### 2.1 Core Envelope Fields

| Field Name | Protobuf Type | Protojson Key | Description |
|:---|:---|:---|:---|
| `id` | `string` | `id` | Unique transaction identifier; must match `transaction_hash` exactly. |
| `timestamp` | `google.protobuf.Timestamp` | `timestamp` | UTC envelope generation timestamp. |
| `expires_at` | `google.protobuf.Timestamp` | `expiresAt` | UTC timestamp after which the envelope is expired and invalid. |
| `source_component` | `Component` (enum) | `sourceComponent` | Originating component (`COMPONENT_AGENT`, `COMPONENT_ZTDO`, `COMPONENT_CLIENT`). |
| `operator_id` | `string` | `operatorId` | Identifier of the target Operator instance. |
| `operator_session_id` | `string` | `operatorSessionId` | Active host-side execution session ID. |
| `web_session_id` | `string` | `webSessionId` | Authenticated browser frontend session ID. |
| `cli_session_id` | `string` | `cliSessionId` | Authenticated CLI client session ID. |
| `requestor_user_id` | `string` | `requestorUserId` | Authenticated human user authorizing the execution (delegator). |
| `acting_app_id` | `string` | `actingAppId` | Authenticated application or tool acting on behalf of the user (delegate). |
| `event_type` | `string` | `eventType` | Canonical event name from `protocol/constants/events.json`. |
| `payload` | `bytes` | `payload` | Base64-encoded protobuf payload containing execution parameters. |
| `intent_data` | `google.protobuf.Struct` | `intentData` | Structured, JSON-first representation of operation intent. |
| `action_type` | `string` | `actionType` | Registry-derived governance action class (e.g., `EXECUTE_BASH`, `FILE_EDIT`, `MCP_CALL`). |
| `target_resource` | `string` | `targetResource` | Specific target resource (e.g., file path, shell command, tool URI). |
| `state_merkle_root` | `string` | `stateMerkleRoot` | Expected host ledger Merkle root at time of envelope creation. |
| `nonce` | `string` | `nonce` | Unique replay-protection token. The Gateway dispatch path generates 16 random bytes, hex-encoded. |
| `transaction_hash` | `string` | `transactionHash` | Deterministic SHA-256 digest of canonical fields. |
| `protocol_version` | `string` | `protocolVersion` | Must be `"2"` for all current ingress. L4 rejects any other value for current envelopes. |
| `posture` | `string` | `posture` | Governance posture under which envelope was issued (`doctrine`, `consensus`, `ratify`, `notary`). |
| `governance` | `GovernanceMetadata` | `governance` | Enclosed L1, L2, and L3 governance verification proofs. |
| `case_id` | `string` | `caseId` | Optional tracking case identifier. |
| `investigation_id` | `string` | `investigationId` | Optional incident or task investigation identifier. |
| `task_id` | `string` | `taskId` | Optional execution task identifier. |
| `system_fingerprint` | `string` | `systemFingerprint` | Host hardware/OS fingerprint. |
| `tenant_id` | `string` | `tenantId` | Tenancy domain identifier (defaults to `"default"`). |
| `binding_persona` | `string` | `bindingPersona` | Target role or operational persona (defaults to `"default"`). |

### 2.2 Governance Metadata Sub-schemas

The `governance` container encapsulates validation proofs across governance layers:

```protobuf
message GovernanceMetadata {
  L1Metadata l1 = 1;
  L2Metadata l2 = 2;
  L3Metadata l3 = 3;
}
```

#### L1Metadata
- `validated` (`bool`): True if static doctrine checks completed without violation.
- `violations` (`repeated string`): List of detected policy violations or forbidden pattern matches.

#### L2Metadata
- `consensus_set_id` (`string`): Unique identifier of the deliberating consensus set.
- `votes` (`repeated L2Vote`): Collection of member votes, where each vote contains:
  - `signer_key_id` (`string`): Identifier of the consensus member. It matches the trusted signer ID in the Operator SignerStore.
  - `consensus_signature` (`string`): Hex-encoded Ed25519 signature over `<transaction_hash>|<decision>`.
  - `decision` (`bool`): Member evaluation verdict (`true` = approve, `false` = reject).
- Field numbers 3 and 4 are reserved, along with the names `deliberation` and `parent_transaction_hash`, so they are never reused.

#### L3Metadata and L3Proof
- `proof` (`L3Proof`): WebAuthn or CLI/mTLS proof. Fields 1-4 carry WebAuthn data and fields 5-7 carry CLI approval data; the schema states that one proof type should be populated per instance. Its fields are:
  - `client_data_json` (`string`): WebAuthn client data JSON payload.
  - `authenticator_data` (`string`): WebAuthn authenticator data bytes.
  - `signature` (`string`): WebAuthn signature over challenge.
  - `credential_id` (`string`): Enrolled WebAuthn credential identifier.
  - `mtls_cert_fingerprint` (`string`): SHA-256 fingerprint of the caller's mTLS leaf certificate (CLI proofs).
  - `cli_signature` (`string`): Hex-encoded ECDSA P-256 signature over the SHA-256 digest of `ztd:l3:cli:v1|` followed by `transaction_hash` (CLI proofs).
  - `cli_certificate_der` (`bytes`): DER-encoded enrolled CLI leaf certificate. It is not a trust chain.
  - `passkey_context` and `cli_intermediates_der`: Gateway-sourced verification context and untrusted chain-building hints. The Operator trusts only its pinned CA pool.
- `denied` (`bool`): The human declined the suspended transaction. L4 rejects a denied envelope in every posture.
- `pending_dispatch` (`bool`): Gateway-owned suspension provenance for a waiting dispatch.

---

## 3. Deterministic Transaction Hashing

Every envelope is bound to a deterministic, tamper-evident SHA-256 digest (`transaction_hash`). The platform verifier strictly enforces:

$$\text{id} == \text{transaction\_hash} == \text{SHA256}(\text{canonical\_representation})$$

### 3.1 Hash Construction

For protocol version `"2"`, the canonical string input begins with the version prefix `ztd-tx-v2|`, appends the action and event types, and chains the canonical V1 field sequence. `event_type` MUST be non-empty for protocol version `"2"`:

$$\text{canonical\_v2} = \texttt{"ztd-tx-v2|"} \,\|\, \text{action\_type} \,\|\, \texttt{"|"} \,\|\, \text{event\_type} \,\|\, \texttt{"|"} \,\|\, \text{canonical\_v1}$$

Protocol version `"1.0"` hashes `canonical_v1` alone. The Operator retains this path only to verify historical records.

### 3.2 Canonical Field Ordering Rules

`canonical_v1` is generated by evaluating the envelope's fields in strict specification order. Each field, if present and non-empty, is written followed by a literal pipe separator (`|`). Absent or empty optional fields are skipped entirely (no pipe is emitted for absent fields):

1. `action_type`: UTF-8 string.
2. `target_resource`: UTF-8 string.
3. `payload`: Standard Base64 encoded string (`base64.StdEncoding`).
4. `state_merkle_root`: UTF-8 hex string.
5. `nonce`: UTF-8 string.
6. `expires_at`: UTC timestamp formatted with fixed microsecond precision, `YYYY-MM-DDTHH:MM:SS.ffffffZ`, where `ffffff` is six fractional-second digits.
7. `intent_data`: Recursively normalized struct string (see §3.3).
8. `requestor_user_id`: UTF-8 string.
9. `acting_app_id`: UTF-8 string.
10. `operator_id`: UTF-8 string.
11. `operator_session_id`: UTF-8 string.
12. `case_id`: UTF-8 string.
13. `investigation_id`: UTF-8 string.
14. `task_id`: UTF-8 string.
15. `web_session_id`: UTF-8 string.
16. `cli_session_id`: UTF-8 string.

### 3.3 Intent Data Normalization
When `intent_data` (`google.protobuf.Struct`) is populated, it is canonicalized deterministically:
- Map keys are sorted alphabetically in ascending ASCII byte order.
- Key-value pairs are formatted as `key=value`, delimited by commas: `key1=val1,key2=val2`.
- Primitive values:
  - Strings: rendered directly.
  - Numbers: rendered with six fractional digits (`%f`, for example `1.500000`).
  - Booleans: rendered as `true` or `false`.
  - Nulls: rendered as an empty string.
  - Lists: rendered as `[elem1,elem2]`.
  - Nested structs: recursively normalized using these same rules.

### 3.4 Governance Proof Independence
The `governance` metadata field (L1 status, L2 votes, L3 proof) and runtime posture are **strictly excluded** from the transaction hash calculation. This guarantees that:
- L2 Consensus members sign the exact invariant transaction hash before notary involvement.
- L3 Human Notaries authorize the exact transaction hash as their assertion challenge.
- Dynamic gateway postures cannot invalidate existing cryptographic signatures.

---

## 4. The Five-Layer Governance Pipeline

Every mutation must pass through five sequential, non-bypassable layers before execution. Failure at any layer results in immediate rejection.

```text
                      [ Inbound Mutation Request ]
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 1: Doctrine Gate (Gateway & Operator)                         │
│ • Heuristic & static regex analysis via reflected protobuf options │
│ • MITRE ATT&CK detection (reverse shells, privilege escalation)     │
│ • Critical filesystem path lockdown (/etc/shadow, /bin, /usr/bin)   │
└──────────────────────────────────┬──────────────────────────────────┘
                                   │ PASS
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 2: Consensus Deliberation (Consensus Set / Operator)          │
│ • K-of-N Ed25519 quorum signature verification over tx_hash         │
│ • Signer key authentication against Operator SignerStore            │
│ • Quorum policy satisfaction (Enforced: consensus, notary)          │
└──────────────────────────────────┬──────────────────────────────────┘
                                   │ PASS
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 3: Notary Authorization (Human-in-the-Loop)                   │
│ • WebAuthn / FIDO2 cryptographic assertion challenge = tx_hash      │
│ • mTLS client certificate fingerprint + ECDSA P-256 CLI signature   │
│ • Suspended transaction approval lifecycle (Enforced: ratify, notary│
└──────────────────────────────────┬──────────────────────────────────┘
                                   │ PASS
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 4: Warden Pre-Dispatch Gate (Executing Operator PEP)          │
│ • Early durable nonce reservation in ReplayStore (concurrency lock) │
│ • Expiration verification (now < expires_at)                        │
│ • Transaction hash recomputation (id == SHA256(canonical))          │
│ • State Merkle root verification against local host ledger          │
│ • Posture compliance check over L2 and L3 stage evidence            │
└──────────────────────────────────┬──────────────────────────────────┘
                                   │ PASS
                                   ▼
┌─────────────────────────────────────────────────────────────────────┐
│ Layer 5: Actuator Boundary (Sole Execution Target)                  │
│ • Deterministic stage evidence assembly & hash commitment           │
│ • Initial EventReceipt signed & persisted to LocalFirstEventsStore          │
│ • Atomic append of CommitmentAttestation to SQLite ledger           │
│ • JIT scoped capability minted (dissolved immediately post-call)    │
│ • Native handler execution (shell, file, tool dispatch)             │
│ • Output scrubbing (token redaction) at sovereign boundary          │
│ • Final EventReceipt signed (COMPLETED/FAILED) with post-state root │
│ • Signed ReceiptPersistenceAttestation attached & published         │
└─────────────────────────────────────────────────────────────────────┘
```

### 4.1 Layer Breakdown

#### L1 Doctrine: Technical Bedrock
- Enforces static constraints prior to execution logic.
- Evaluates string fields decorated with the custom protobuf option `(ztd.common.v1.forbidden_patterns)` (e.g., regex `(?i)^(sudo|su)$`).
- Analyzes commands and arguments against built-in MITRE ATT&CK-style patterns. Additional doctrine JSON is loaded only from the directory passed to the Gateway with `--doctrine-dir` (default: none). The repository's doctrine set lives in `protocol/constants/doctrine/`.
- For `FILE_EDIT` and file-restore requests, enforces an absolute path denylist of critical system resources. Examples include `/etc/passwd`, `/etc/shadow`, `/etc/sudoers`, `/etc/ssh/sshd_config`, and the directories `/bin/`, `/sbin/`, `/usr/bin/`, and `/usr/sbin/`.

#### L2 Consensus: Bound Consensus Provider Deliberation
- Under `consensus` and `notary` postures, the L2 gate applies only to state-change actions: a non-bootstrap action that the action registry classifies as changing state for its target (`governance.L2Enforced`). Inference, observations, typed provider reads, and registered read-only MCP targets get no vote round. Bootstrap actions (platform enrollment) are exempt.
- The bound consensus provider produces the votes. Provider `application` posts the envelope to an enrolled application's HTTPS protobuf endpoint; the response must return the envelope with its L2 votes populated. Provider `tribunal` runs a governed INFERENCE call for each Tribunal member, and the Gateway signs that member's decision with the member's Ed25519 key.
- Each `L2Vote` carries an Ed25519 signature over `<transaction_hash>|<decision>`. See [Consensus Architecture](../../docs/architecture/consensus.md).
- Operator L4 Warden verifies that at least $K$ distinct, valid signatures from trusted keys in the `SignerStore` are present.

#### L3 Notary: Hardware-Bound Human Presence
- Under `ratify` and `notary` postures, enforces cryptographic proof that an authorized human explicitly sanctioned the mutation.
- **Web Sessions**: FIDO2/WebAuthn assertion where `challenge == transaction_hash`.
- **CLI Sessions**: Verification of caller's mTLS certificate SHA-256 fingerprint (`mtls_cert_fingerprint`) combined with an ECDSA P-256 signature over the domain-separated `transaction_hash` digest (`cli_signature`).
- **Suspension Lifecycle**: Inbound mutations lacking L3 proof are held in `SuspendedTransactionStore` with a 2-minute TTL. The Gateway returns an approval directive with a direct URL. Human authorization completes the transaction out-of-band and triggers an `approval.completed` SSE event scoped to the submitter session.

#### L4 Warden: Pre-Dispatch Verification
- Runs within the executing Operator process as the ultimate gate before execution.
- Durably reserves `nonce` in SQLite replay storage to eliminate race conditions and double-processing.
- Confirms `now < expires_at`.
- Recomputes `transaction_hash` from the canonical wire representation and confirms exact match with `id`.
- Confirms `state_merkle_root` matches the local host's state Merkle root.
- Validates L2 and L3 proof validity according to the active posture.

#### L5 Actuator: Sovereign Execution & Evidence Boundary
- The sole subsystem authorized to perform mutations on the host runtime.
- **Evidence Binding**: Compiles `DeterministicStageEvidence` across all previous gates.
- **Initial Receipt**: Signs an `EXECUTING` receipt and records it to `LocalFirstEventsStore`.
- **Ledger Commitment**: Atomically appends a `CommitmentAttestation` to the local commitment chain.
- **JIT Capability**: Mints an ephemeral execution token restricted strictly to the verified `action_type` and `target_resource`, revoked immediately upon handler return.
- **Sovereign Scrubbing**: When a scrubber is active, redacts secrets, tokens, and PII from execution outputs before they are signed into the receipt. Without a scrubber, the signed summary is fixed text.
- **Final Receipt**: Updates receipt status (`COMPLETED` or `FAILED`), records post-execution state root, signs canonical receipt bytes, and persists a `ReceiptPersistenceAttestation`.

### 4.2 Governance Postures

Posture is configured at Gateway startup via `--posture <mode>` and is immutable at runtime:

| Posture Mode | CLI Flag | L1 Doctrine | L2 Consensus | L3 Notary | L4 Warden | L5 Actuator |
|:---|:---|:---:|:---:|:---:|:---:|:---:|
| **Doctrine** | `--posture doctrine` | **Enforced** | Audited | Audited | **Enforced** | **Enforced** |
| **Consensus** | `--posture consensus` | **Enforced** | **Enforced** | Audited | **Enforced** | **Enforced** |
| **Ratify** | `--posture ratify` | **Enforced** | Audited | **Enforced** | **Enforced** | **Enforced** |
| **Notary** | `--posture notary` | **Enforced** | **Enforced** | **Enforced** | **Enforced** | **Enforced** |

---

## 5. EventReceipt & Attestation Ledger Model

The [Event Receipt](../../docs/reference/glossary.md#event-receipt), `ztd.operator.v1.EventReceipt`, is signed evidence for a governed request event, including reads, observations, mutations, and governance rejections. Its schema is defined in `protocol/proto/ztd/operator/v1/operator.proto`.

Each receipt stage is stored as a `ztd.v1.operator.receipt.recorded` row on the `events` chain. The current stage is the row whose `event_receipt_status` is not null; recording a stage that advances the current projection clears that column on the previous one. A non-terminal stage cannot replace a terminal receipt. No separate `receipts` table is used. The event name is defined in `protocol/constants/events.json`.

### 5.1 EventReceipt Schema

| Field Name | Type | Description |
|:---|:---|:---|
| `transaction_id` | `string` | Transaction identifier from the originating envelope. |
| `transaction_hash` | `string` | Canonical transaction hash from the envelope. |
| `status` | `ExecutionStatus` (enum) | Status: `EXECUTION_STATUS_EXECUTING`, `EXECUTION_STATUS_COMPLETED`, `EXECUTION_STATUS_FAILED`, `EXECUTION_STATUS_CANCELLED`, `EXECUTION_STATUS_TIMEOUT`. |
| `result_summary` | `string` | Concise execution summary. Handler output is scrubbed before signing when a scrubber is active. |
| `state_root_before` | `string` | State Merkle root immediately prior to execution. |
| `state_root_after` | `string` | State Merkle root immediately after execution. |
| `executed_at_unix_ms` | `int64` | Millisecond Unix timestamp of completion. |
| `signer_key_id` | `string` | Public key ID of the signing Operator Actuator. |
| `signature` | `string` | Hex-encoded Ed25519 signature over canonical receipt bytes. |
| `l2_status` | `L2Status` (enum) | `L2_STATUS_NOT_REQUIRED`, `L2_STATUS_REQUIRED_VALID`, `L2_STATUS_REQUIRED_FAILED`. |
| `l3_status` | `L3Status` (enum) | `L3_STATUS_NOT_REQUIRED`, `L3_STATUS_REQUIRED_VALID`, `L3_STATUS_REQUIRED_FAILED`. |
| `deterministic_stage_evidence`| `repeated DeterministicStageEvidence` | Ordered stage proofs emitted by L1, L2, L3, and L4. |
| `final_persistence_attestation`| `ReceiptPersistenceAttestation` | Cryptographic proof binding receipt to audit storage. |
| `failure_code` | `ReceiptFailureCode` (enum) | Failure classification (UNSPECIFIED on success). |
| `rejection_code` | `GovernanceRejectionCode` (enum) | Verification check that rejected the transaction. Set exactly when `failure_code` is `RECEIPT_FAILURE_CODE_GOVERNANCE_REJECTED`. |
| `event_type` | `string` | Originating request event name. |
| `action_type` | `string` | Originating governance action class. |

### 5.2 Deterministic Receipt Signing

The receipt signature is generated over a canonical JSON serialization of the receipt fields (`CanonicalizeEventReceipt`):

```json
{
  "transaction_id": "<tx_id>",
  "transaction_hash": "<tx_hash>",
  "event_type": "<event_type>",
  "action_type": "<action_type>",
  "status": 2,
  "result_summary": "<summary>",
  "state_root_before": "<root_before>",
  "state_root_after": "<root_after>",
  "executed_at_unix_ms": 1774980000000,
  "signer_key_id": "<key_id>",
  "l2_status": 2,
  "l3_status": 2,
  "deterministic_stage_evidence_hash": "<evidence_sha256>"
}
```

The Actuator signs these canonical bytes using its private Ed25519 key, ensuring full cryptographic binding over the lifecycle evidence and state transitions. The `event_type`, `action_type`, `deterministic_stage_evidence_hash`, `failure_code`, and `rejection_code` fields are omitted when empty or zero. The `status`, `l2_status`, and `l3_status` fields are always present as enum numbers.

### 5.3 Cryptographic Commitment Ledger

The Operator maintains an append-only commitment chain:
- **`CommitmentAttestation`**: Signed by the Operator auditor key before handler execution. Binds `transaction_id`, `transaction_hash`, `prior_commitment_hash`, `state_root_at_commit`, and signature digests (`l2_signature_digest`, `warden_intent_signature_digest`, `human_signature_digest`).
- **Chain Continuity**: Every commitment references the hash of the preceding attestation, forming an immutable hash chain rooted in the local SQLite ledger.

---

## 6. Canonical Event Taxonomy

All operations in the ztd platform correspond to typed events defined in `protocol/constants/events.json`. Every event follows the canonical dot-delimited grammar with the `ztd.v1` prefix. The entity segment may span several dot-delimited parts:

$$\texttt{ztd.v1.} \langle\text{domain}\rangle \texttt{.} \langle\text{entity}\rangle \texttt{.} \langle\text{terminal}\rangle$$

The terminal segment indicates lifecycle stage. Requests end in `requested`, and completions end in `completed` or `failed`. Other stages use terminals such as `started`, `received`, `recorded`, `granted`, `denied`, `revoked`, `updated`, and `timeout`. The registry defines the complete set.

### 6.1 Major Event Domains

| Domain | Scope | Typical Request Event | Action Type Mapping |
|:---|:---|:---|:---|
| **Operator Command** | Shell command execution | `ztd.v1.operator.command.requested` | `EXECUTE_BASH` |
| **Operator Filesystem** | File read, list, grep, edit | `ztd.v1.operator.file.edit.requested` | `FILE_EDIT` |
| **Operator MCP** | Model Context Protocol calls | `ztd.v1.operator.mcp.call.requested` | `MCP_CALL` |
| **Operator A2A** | Agent-to-Agent skill calls | `ztd.v1.operator.a2a.call.requested` | `A2A_CALL` |
| **Operator Lifecycle** | Heartbeats, shutdown, status | `ztd.v1.operator.heartbeat.requested` | `HEARTBEAT` |
| **Application Docs** | Case, investigation, task updates | `ztd.v1.app.document.update.requested` | `DOCUMENT_UPDATE` |
| **Platform Enrollment**| Workload credential enrollment | `ztd.v1.platform.enrollment.create.requested` | `PLATFORM_ENROLLMENT_CREATE` |
| **AI Evaluation** | Evaluation campaigns, cycles, and assignments | `ztd.v1.ai.eval.assignment.started` | *(fact)* |
| **Observe & Feed** | Projections & public mirrors | `ztd.v1.public.feed.proof.published` | *(fact)* |

---

## 7. Session Architecture & Transport Routing

### 7.1 Cryptographic Session Isolation

The platform segregates runtime contexts into three mutually exclusive session types:

| Session Type | Primary Key | Authentication Mechanism | Authority Scope |
|:---|:---|:---|:---|
| **Operator Session** | `operator_session_id` | mTLS (Operator Certificate with URI SAN) | Host-side execution environment |
| **CLI Session** | `cli_session_id` | mTLS (CLI Client Certificate with URI SAN) | Developer / automation CLI client |
| **Web Session** | `web_session_id` | FIDO2 / WebAuthn passkey assertion cookie | Browser operator console |

### 7.2 Two-Dimensional SSE Routing Model

Server-Sent Events (SSE) and Pub/Sub notifications enforce strict contextual routing:
1. **Ownership Axis**: `user_id` is unconditionally required on all private channels. It anchors ownership to an authenticated human principal.
2. **Delivery Axis**: Exactly one of `web_session_id` or `cli_session_id` must be set.

Broadcasting to `user_id` without an explicit session target is disallowed. Approval events generated for suspended CLI commands record `submitter_cli_session_id`, ensuring the completion notification delivers solely to the submitting CLI terminal.

---

## 8. Protocol Error Codes

JSON-RPC error codes are returned on validation and governance failures. The `-32000` to `-32101` protocol identifiers are defined in `internal/constants/rpc_errors.go`. The standard JSON-RPC identifiers are defined in `internal/constants/auth.go`, except where the table shows a literal:

| Code | Constant Identifier | Failure Reason |
|:---|:---|:---|
| `-32000` | `ErrCodeInvalidEnvelope` | Invalid envelope: missing transaction ID, missing payload, or unknown action type. |
| `-32001` | `ErrCodeHashMismatch` | Transaction hash does not match computed SHA-256 digest. |
| `-32002` | `ErrCodeExpired` | `expires_at` timestamp is in the past. |
| `-32003` | `ErrCodeReplay` | Nonce already exists in durable replay store. |
| `-32004` | `ErrCodeStateMismatch` | `state_merkle_root` does not match host ledger root. |
| `-32005` | `ErrCodeL1ValidationFailed`| Payload violates L1 Doctrine forbidden patterns or MITRE threat rules. |
| `-32006` | `ErrCodeL2SignatureInvalid`| L2 Consensus signature missing, invalid, or quorum unmet. |
| `-32007` | `ErrCodeL3ProofInvalid` | L3 Notary human proof missing or invalid. |
| `-32008` | `ErrCodePayloadDecodeFailed`| Base64 payload cannot be decoded to expected protobuf message. |
| `-32100` | `ErrCodeResourceNotFound` | Reserved. Not referenced outside `internal/constants/rpc_errors.go`. |
| `-32101` | `ErrCodeGatewayNotReady` | Reserved. Not referenced outside `internal/constants/rpc_errors.go`. |
| `-32600` | literal in `internal/services/mcp/mcp_endpoint.go` and `internal/services/mcp/gateway.go` | Invalid JSON-RPC request: batch request received (batches explicitly rejected), missing or invalid JSON-RPC version or method (A2A), or request body over the read limit. |
| `-32603` | `JSONRPCErrorCodeInternal` (`internal/constants/auth.go`) | Internal execution or downstream service error. |
| `-32700` | `JSONRPCErrorCodeParseError` | Malformed JSON-RPC wire payload. |

---

## 9. Canonical Wire Examples

The values below are illustrative. `id` and `transactionHash` are not recomputed for these fields.

### 9.1 Governed Shell Command Envelope

```json
{
  "id": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "timestamp": "2026-10-12T18:00:00.000000Z",
  "expiresAt": "2026-10-12T18:05:00.000000Z",
  "sourceComponent": "COMPONENT_CLIENT",
  "operatorId": "op-prod-01",
  "operatorSessionId": "sess-op-789a",
  "cliSessionId": "sess-cli-456b",
  "requestorUserId": "user-alice",
  "actingAppId": "app-codex-cli",
  "eventType": "ztd.v1.operator.command.requested",
  "actionType": "EXECUTE_BASH",
  "targetResource": "/usr/local/bin/backup.sh",
  "payload": "Cg9iYWNrdXAuc2ggLS1hbGwSC2V4ZWMtOTkwMjE=",
  "intentData": {
    "command": "/usr/local/bin/backup.sh",
    "parameters": "--all",
    "rationale": "Scheduled system backup"
  },
  "stateMerkleRoot": "7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069",
  "nonce": "8f8682a8a5be4518a6d14be39be49f48",
  "transactionHash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "protocolVersion": "2",
  "posture": "notary",
  "governance": {
    "l1": {
      "validated": true,
      "violations": []
    },
    "l2": {
      "consensusSetId": "cset-core-01",
      "votes": [
        {
          "signerKeyId": "key-consensus-node-1",
          "consensusSignature": "a3f5c789...4412bc",
          "decision": true
        },
        {
          "signerKeyId": "key-consensus-node-2",
          "consensusSignature": "d5e8f123...9901aa",
          "decision": true
        }
      ]
    },
    "l3": {
      "proof": {
        "mtlsCertFingerprint": "9b71d224bd62f3785d96d46ad3ea3d73319bfbc2890caadae2dff72519673ca7",
        "cliSignature": "5c92e71...bb0291"
      }
    }
  }
}
```

### 9.2 Corresponding Completed EventReceipt

```json
{
  "transactionId": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "transactionHash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "status": "EXECUTION_STATUS_COMPLETED",
  "resultSummary": "exit code 0: backup completed successfully (42 files archived)",
  "stateRootBefore": "7f83b1657ff1fc53b92dc18148a1d65dfc2d4b1fa3d677284addd200126d9069",
  "stateRootAfter": "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
  "executedAtUnixMs": 1774980005120,
  "signerKeyId": "op-prod-01-actuator-key",
  "signature": "3c7e42...0a9f11",
  "l2Status": "L2_STATUS_REQUIRED_VALID",
  "l3Status": "L3_STATUS_REQUIRED_VALID",
  "eventType": "ztd.v1.operator.command.requested",
  "actionType": "EXECUTE_BASH"
}
```

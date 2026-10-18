---
doc_id: consensus
title: Consensus Architecture
audience: maintainers and coding agents
status: current
version: v1.1.3
last_updated: 2026-10-18
owners:
  - internal/services/governance/l2_consensus.go
  - internal/services/governance/l4_warden.go
  - internal/services/gateway/consensus_store_service.go
  - internal/services/gateway/platform_enrollment_service.go
  - internal/services/gateway/platform_enrollment_controller.go
  - protocol/proto/ztd/common/v1/common.proto
  - protocol/models/consensus.json
related:
  - governance.md
  - gateway.md
  - operator.md
  - auth.md
  - encryption.md
  - network.md
when_to_read: Understanding L2 machine consensus, enrolling and binding a consensus application, HTTPS protobuf deliberation, L4 Warden signature verification, and deterministic execution evidence.
do_not_use_for:
  - Five-layer governance verification pipeline overview (governance.md)
  - Platform PKI, TLS/mTLS, and SPIFFE identity issuance (network.md)
  - L3 Notary human authorization and WebAuthn passkeys (auth.md)
  - Local database layout and SQLite encryption boundaries (storage.md)
  - Remote Operator command dispatch and local execution (operator.md)
---

# Consensus Architecture

## Purpose

Defines ztd's Layer 2 (L2) machine consensus architecture: enrolling a third-party application or agent as an ordinary `PLATFORM_COMPONENT_KIND_APPLICATION`, binding it as the Gateway's consensus application via `PLATFORM_ENROLLMENT_BIND_CONSENSUS`, HTTPS protobuf deliberation mechanics, L4 Warden and outbound Operator (`OperatorL2Trust`) signature verification, and L5 Actuator receipt evidence. Consensus is not a Gateway-internal service or multi-member keystore construct; it is an external enrolled application whose Ed25519 signature over `<transaction_hash>|<decision>` gates execution under `consensus` and `notary` postures.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
  - [Application Enrollment and Binding (`INV-CON-POL`)](#application-enrollment-and-binding-inv-con-pol)
  - [Deliberation and Proof Production (`INV-CON-VOTE`)](#deliberation-and-proof-production-inv-con-vote)
  - [L4 Verification and Authorization (`INV-CON-WARD`)](#l4-verification-and-authorization-inv-con-ward)
  - [Receipt and Audit Evidence (`INV-CON-RCPT`)](#receipt-and-audit-evidence-inv-con-rcpt)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Place in the Five-Layer Interlock](#place-in-the-five-layer-interlock)
  - [Consensus Application Enrollment and Binding](#consensus-application-enrollment-and-binding)
  - [Vote Production and Deliberation Mechanics](#vote-production-and-deliberation-mechanics)
  - [Gateway Ingress Deliberation Routing](#gateway-ingress-deliberation-routing)
  - [L4 Warden and Outbound Operator Verification](#l4-warden-and-outbound-operator-verification)
  - [Receipt Evidence and Deterministic Stages](#receipt-evidence-and-deterministic-stages)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### Application Enrollment and Binding (`INV-CON-POL`)

| ID | Rule |
| :--- | :--- |
| INV-CON-POL-01 | Consensus is an ordinary enrolled application (`PLATFORM_COMPONENT_KIND_APPLICATION`), not a Gateway-internal service or multi-member keystore construct. The application MUST enroll through the standard platform enrollment workflow, receiving its mTLS client certificate and registering its Ed25519 public key in `trusted_signers`. |
| INV-CON-POL-02 | The Gateway MUST record at most one bound consensus application (`constants.DocIDConsensusBinding` in `constants.CollectionConsensus`). |
| INV-CON-POL-03 | Binding a consensus application MUST traverse the governance pipeline as the bootstrap action `PLATFORM_ENROLLMENT_BIND_CONSENSUS` (`POST /api/v1/auth/platform-enrollments/consensus`), referencing an enrolled, enabled `TrustedSigner` in the Gateway's signer store. |
| INV-CON-POL-04 | `PLATFORM_ENROLLMENT_BIND_CONSENSUS` is a mutation that is NOT exempt from L3 via `_authorized_by_decision`: it MUST require L3 human authorization under `ratify` and `notary` postures (`posture.RequiresL3() == true`). |
| INV-CON-POL-05 | Outbound Operators MUST receive the bound consensus application's identity and Ed25519 public key in their signed enrollment bundle and in-band certificate renewal bundle (`OperatorL2Trust`). |

### Deliberation and Proof Production (`INV-CON-VOTE`)

| ID | Rule |
| :--- | :--- |
| INV-CON-VOTE-01 | The consensus application MUST recompute the canonical transaction hash from envelope fields (`governance.GenerateMessageID`) and verify `envelope.Id` and `envelope.TransactionHash` before signing a decision. |
| INV-CON-VOTE-02 | Every consensus signature in `ConsensusProof` MUST be an Ed25519 signature by the bound consensus application's private key over the canonical UTF-8 payload `<transaction_hash>\|<decision>`, where `<decision>` is `true` or `false`, encoded as a lowercase hex string. |
| INV-CON-VOTE-03 | During L2 deliberation, the Gateway MUST transmit the full `GovernanceEnvelope` serialized as `application/x-protobuf` to the bound consensus application's HTTPS deliberation endpoint and receive back the envelope populated with its `ConsensusProof`. |

### L4 Verification and Authorization (`INV-CON-WARD`)

| ID | Rule |
| :--- | :--- |
| INV-CON-WARD-01 | The L4 Warden (`L4Warden.verifyL2Posture`) MUST verify the `ConsensusProof` signature against the recomputed transaction hash rather than trusting the envelope's declared identifier. |
| INV-CON-WARD-02 | Both Gateway in-process L4 verification and outbound Operator L4 verification (`OperatorL2Trust`) MUST verify the `ConsensusProof` signature against the bound consensus application's Ed25519 public key from `trusted_signers` / the Operator enrollment bundle. |
| INV-CON-WARD-03 | Evaluation MUST accept only an affirmative (`decision=true`) signature verified against the bound consensus application's enabled `TrustedSigner` public key. A negative decision (`decision=false`) authenticates dissent and does not authorize execution. |
| INV-CON-WARD-04 | Bootstrap action types (`actionType.IsBootstrapAction()`) MUST be exempt from L2 gating across all postures, permitting initial enrollment and `PLATFORM_ENROLLMENT_BIND_CONSENSUS` transactions to succeed before a consensus application is bound. |
| INV-CON-WARD-05 | Under `consensus` and `notary` postures (`posture.RequiresL2() == true`), a missing `ConsensusProof`, an unbound consensus application, or a negative decision MUST reject the transaction fail closed. Under `doctrine` and `ratify` postures, L2 proofs are evaluated for deterministic audit evidence without gating transaction admission. |
| INV-CON-WARD-06 | Under `consensus` and `notary` postures, a `ConsensusProof` whose signature fails verification or whose signer cannot be resolved against the bound consensus application MUST reject the transaction with `ErrTxL2SignatureInvalid` (`L2_SIGNATURE_INVALID`). |

An absent L2 proof (or empty L2 metadata with no set ID) yields `L2_PROOF_MISSING`. A proof naming an enabled, known set with zero votes yields `L2_QUORUM_NOT_MET`: zero affirmative votes cannot meet its quorum, including a single application's quorum of one. Both reject execution when L2 is enforced.

### Receipt and Audit Evidence (`INV-CON-RCPT`)

| ID | Rule |
| :--- | :--- |
| INV-CON-RCPT-01 | The L4 Warden decides the L2 status once and the L5 ActionReceipt `L2Status` field MUST copy it: `L2_STATUS_REQUIRED_VALID` when an L2 proof is present and verified (also where L2 is not enforced, as audit evidence), `L2_STATUS_REQUIRED_FAILED` when L2 is enforced and fails, or `L2_STATUS_NOT_REQUIRED` when L2 is not enforced for the action (the posture does not require it, or the action is bootstrap-exempt) and no valid proof is present. L5 never recomputes it from posture. |
| INV-CON-RCPT-02 | L4 verification MUST produce `DeterministicStageEvidence` for protocol L2 recording stage outcome (`VERIFIED`, `FAILED`, or `NOT_REQUIRED`) and the SHA-256 `L2SignatureDigest` over the proof signature. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Consensus binding model | `protocol/models/consensus.json`, `internal/models/auth.go` | Schema validation and `consensus_store_service_test.go` |
| Consensus binding document store | `internal/services/gateway/consensus_store_service.go` (`constants.DocIDConsensusBinding` in `constants.CollectionConsensus`) | Unit tests in `consensus_store_service_test.go` |
| Consensus application enrollment & binding | `internal/services/gateway/platform_enrollment_service.go`, `platform_enrollment_controller.go`, `internal/services/pubsub/platform_enrollment_handlers.go` | `POST /api/v1/auth/platform-enrollments/consensus` (`PLATFORM_ENROLLMENT_BIND_CONSENSUS`) |
| L4 Warden L2 verification & `OperatorL2Trust` | `internal/services/governance/l2_consensus.go`, `internal/services/governance/l4_warden.go` | Integration tests in `l4_warden_consensus_test.go` |
| L5 Actuator receipt evidence | `internal/services/governance/l5_actuator.go` | Receipt tests in `l4_warden_test.go` |
| Ingress HTTPS deliberation integration | `internal/services/mcp/gateway.go`, `internal/services/gateway/dispatch_service.go` | End-to-end tests in `test/l2_consensus_integration_test.go` |

## Procedures

### Place in the Five-Layer Interlock

L2 Consensus occupies the second layer of the platform governance interlock:
1. **L1 Doctrine**: Decodes payload and executes static regex screening and MITRE threat detection.
2. **L2 Consensus**: Verifies the bound consensus application's Ed25519 signature over `<transaction_hash>|<decision>`.
3. **L3 Notary**: Verifies human authorization proofs (WebAuthn passkeys) for mutating actions.
4. **L4 Warden**: Executes pre-dispatch verification (replay check, hash match, state root, L1-L3 proofs).
5. **L5 Actuator**: Mints single-action JIT capabilities, executes actions, and writes local signed receipts.

`constants.Posture.RequiresL2()` (`internal/constants/posture.go`) governs whether L2 verification gates execution:

| Posture | `posture.RequiresL2()` | L2 Gate Status | Receipt `L2Status` Value | Deterministic Stage Outcome |
| :--- | :--- | :--- | :--- | :--- |
| **`doctrine`** | `false` | Non-gating (audited only) | `L2_STATUS_REQUIRED_VALID` (if valid) or `L2_STATUS_NOT_REQUIRED` | `DETERMINISTIC_STAGE_OUTCOME_VERIFIED` (if valid) or `NOT_REQUIRED` |
| **`consensus`** | `true` | Strictly required for non-bootstrap | `L2_STATUS_REQUIRED_VALID` | `DETERMINISTIC_STAGE_OUTCOME_VERIFIED` |
| **`ratify`** | `false` | Non-gating (audited only) | `L2_STATUS_REQUIRED_VALID` (if valid) or `L2_STATUS_NOT_REQUIRED` | `DETERMINISTIC_STAGE_OUTCOME_VERIFIED` (if valid) or `NOT_REQUIRED` |
| **`notary`** | `true` | Strictly required for non-bootstrap | `L2_STATUS_REQUIRED_VALID` | `DETERMINISTIC_STAGE_OUTCOME_VERIFIED` |

### Consensus Application Enrollment and Binding

Ordinary application enrollment accepts a separate Ed25519 signing public key alongside its P-256 transport CSR. Owner approval and completion bind both keys as described in [Platform Workload Enrollment](auth.md#platform-workload-enrollment). `TestPlatformEnrollment_OrdinaryApplicationEnrollsSigningKeyAndBindsConsensus` exercises enrollment, binding, HTTPS protobuf deliberation, Operator trust export, and revocation without seeding a trusted signer.

For the CLI enrollment, sealed signing identity, binding/query commands, and posture-dependent approval behavior, see [Platform Workload Enrollment](auth.md#platform-workload-enrollment). Consensus enrollment and binding depend on local account authority, independently of the account's sign-in provider.

Any third-party application or agent that serves as a consensus deliberator enrolls and binds through two standard governed steps:

1. **Application Enrollment (`PLATFORM_COMPONENT_KIND_APPLICATION`)**:
   - The application generates its P-256 transport key and CSR plus a separate Ed25519 signing key, submits both public identities to `POST /api/v1/auth/platform-enrollments/request`, and awaits posture-dependent owner approval.
   - Upon completion (`POST /api/v1/auth/platform-enrollments/complete`), the application receives its mTLS client certificate (`spiffe://ztd.local/app/<app_id>`) and its Ed25519 public key is registered in `trusted_signers` (`SignerStoreService`).
2. **Consensus Binding (`PLATFORM_ENROLLMENT_BIND_CONSENSUS`)**:
   - An authenticated owner binds the enrolled application as the Gateway's consensus provider via `POST /api/v1/auth/platform-enrollments/consensus`, which executes the governed bootstrap action `PLATFORM_ENROLLMENT_BIND_CONSENSUS`.
   - Because `PLATFORM_ENROLLMENT_BIND_CONSENSUS` is a bootstrap action (`IsBootstrapAction() == true`), it is exempt from L2 gating so it can execute under `consensus` and `notary` postures before a consensus provider exists. It is not marked `_authorized_by_decision`, so it remains L3-gated under `ratify` and `notary`.
   - The handler persists the single bound consensus record at `constants.DocIDConsensusBinding` in `constants.CollectionConsensus` (`ConsensusStoreService`), recording the enrolled application ID and its HTTPS deliberation endpoint URL.
   - `models.ValidateConsensusEndpoint` requires absolute HTTPS URLs without credentials or fragments at admission, authoritative persistence, and egress. `ConsensusAppDeliberator` rejects all redirects and accepts protobuf envelope responses. Its client configuration is copied so caller-owned clients retain their redirect policy.

### Vote Production and Deliberation Mechanics

During L2 deliberation:
1. **Protobuf Request**: The Gateway serializes the `GovernanceEnvelope` as `application/x-protobuf` and sends an HTTPS `POST` request to the bound consensus application's deliberation endpoint.
2. **Hash Verification**: The consensus application recomputes the canonical transaction hash (`governance.GenerateMessageID`) and verifies `envelope.Id` and `envelope.TransactionHash`.
3. **Policy Evaluation & Signing**: The consensus application evaluates the envelope intent and signs `<transaction_hash>|<decision>` (`true` or `false`) using its Ed25519 private key.
4. **Proof Attachment**: The consensus application populates `envelope.Governance.L2` (`ConsensusProof`) with its signature and returns the protobuf `GovernanceEnvelope` in the HTTPS response body.

### Gateway Ingress Deliberation Routing

```mermaid
flowchart TD
    Client([Client Ingress]) --> IngressType{Ingress Path}
    
    IngressType -->|MCP Tool / A2A Call| MCPGateway[MCP Gateway Service]
    IngressType -->|HTTP Dispatch to Operator| DispatchSvc[Dispatch Service]
    IngressType -->|Direct Envelope POST| DirectGov[Governance Controller]
    
    MCPGateway --> CheckL2A{posture.RequiresL2?}
    DispatchSvc --> CheckL2B{posture.RequiresL2?}
    
    CheckL2A -->|Yes & Bound App| HTTPSDelibA[HTTPS POST application/x-protobuf to Bound Consensus App]
    CheckL2A -->|No| SkipDelibA[Proceed without deliberation]
    
    CheckL2B -->|Yes & Bound App| HTTPSDelibB[HTTPS POST application/x-protobuf to Bound Consensus App]
    CheckL2B -->|No| SkipDelibB[Proceed without deliberation]
    
    HTTPSDelibA --> PopulatedEnvelopeA[Envelope with ConsensusProof]
    HTTPSDelibB --> PopulatedEnvelopeB[Envelope with ConsensusProof]
    
    PopulatedEnvelopeA --> InProcessL4[In-Process L4 Warden / L5 Actuator]
    PopulatedEnvelopeB --> OutboundPubSub[WebSocket cmd channel to Operator]
    
    DirectGov --> VerifyIdentity[Verify mTLS Identity Binding]
    VerifyIdentity --> DirectL4[In-Process L4 Warden / L5 Actuator]
```

- **MCP Tools and A2A Calls**: Under `consensus` and `notary`, `prepareEnvelope` forwards the `GovernanceEnvelope` (`application/x-protobuf`) to the bound consensus application's HTTPS endpoint prior to dispatch.
- **Governed HTTP Command Dispatch**: When dispatching commands to an outbound Operator, `DispatchService` deliberates with the bound consensus application under L2 postures before publishing the envelope to `cmd:<operator_id>:<operator_session_id>`.
- **Direct Envelope Ingress**: Direct submissions to `POST /api/v1/governance/envelopes` do not trigger Gateway-initiated deliberation; callers must submit envelopes already carrying a valid `ConsensusProof`.

### L4 Warden and Outbound Operator Verification

1. **Bootstrap Exemption**: If `actionType.IsBootstrapAction()` is true, transactions bypass L2 gating across all postures.
2. **Presence Checks**: Under `consensus` and `notary` (`posture.RequiresL2()`), a missing `ConsensusProof` (`ErrTxL2SignatureMissing`), an unconfigured store (`ErrTxL2SignerStoreNotConfigured` or `ErrTxL2ConsensusNotConfigured`), or an unbound/unknown consensus application (`ErrTxL2ConsensusSetUnknown`) fails closed immediately.
3. **Signature Verification**: `L4Warden.verifyL2Posture` recomputes the transaction hash and verifies the Ed25519 signature over `<transaction_hash>|<decision>` against the bound consensus application's public key in `trusted_signers`. Under `consensus` and `notary`, an invalid signature or unresolvable signer fails closed with `ErrTxL2SignatureInvalid` (INV-CON-WARD-06).
4. **Decision Check**: Requires an affirmative (`decision=true`) vote from the bound consensus application; otherwise rejects with `ErrTxL2QuorumNotMet`.
5. **Outbound Operator Verification (`OperatorL2Trust`)**: Remote Operators receive the Gateway's bound consensus application identity and Ed25519 public key in their signed enrollment bundle (and upon in-band certificate renewal), served in memory via `governance.L2TrustStore` (`OperatorL2Trust`), and independently verify the `ConsensusProof` in their local L4 Warden.

### Receipt Evidence and Deterministic Stages

When L4 verification concludes, the Warden emits deterministic stage evidence, and the L5 Actuator embeds the final L2 outcome into the signed `ActionReceipt`:
- **Deterministic Stage Evidence**: A stage record with `DETERMINISTIC_STAGE_KIND_PROTOCOL_L2` records the outcome (`VERIFIED`, `FAILED`, or `NOT_REQUIRED`).
- **Rejection Attribution**: A rejected L2 proof produces a signed `FAILED` receipt whose signed `rejection_code` names the L2 check. The enum is defined in `protocol/proto/ztd/operator/v1/operator.proto`.
- **Signature Digest**: `L2SignatureDigest` computes a SHA-256 digest over the `ConsensusProof` signature, binding the cryptographic proof into the audit trail.
- **Receipt Status**: The signed receipt records `L2Status`: `L2_STATUS_REQUIRED_VALID`, `L2_STATUS_REQUIRED_FAILED`, or `L2_STATUS_NOT_REQUIRED`.

## Anti-patterns

- **Treating Consensus as a Gateway-Internal Service or Keystore Construct**: Expecting the Gateway to host an internal consensus deliberator (`internal/services/consensus/`), store member keys in `.ztd/secrets/`, or accept removed flags (`--consensus-id`, `--consensus-url`, `--consensus-bootstrap`) or removed routes (`POST /consensus/v1/deliberate`, `/api/v1/admin/consensus`). Consensus is an external application enrolled via `PLATFORM_COMPONENT_KIND_APPLICATION` and bound via `PLATFORM_ENROLLMENT_BIND_CONSENSUS`.
- **Equating L2 Consensus with Distributed State Machine Replication**: Treating L2 Consensus as a Raft or Paxos leader-election engine. L2 is strictly a cryptographic signature gate over an immutable transaction hash.
- **Submitting Unproved Envelopes via Direct Ingress**: Expecting the Gateway to call the consensus application on direct submissions to `POST /api/v1/governance/envelopes`. Direct submissions require pre-populated `ConsensusProof` signatures.
- **Treating Negative Votes as Authorization**: Treating a negative decision (`decision=false`) as authorizing execution. Dissent is recorded for audit purposes only (INV-CON-WARD-03).
- **Trusting Declared Transaction Identifiers**: Signing or verifying envelopes without recomputing the SHA-256 transaction hash via `governance.GenerateMessageID` (INV-CON-VOTE-01, INV-CON-WARD-01).

## Links out

- [Platform Architecture Overview](overview.md): High-level system architecture and node topologies.
- [Governance Architecture](governance.md): Five-layer governance interlock, posture definitions, and receipt validation.
- [Gateway Architecture](gateway.md): Gateway initialization, service wiring, and in-process execution.
- [Operator Architecture](operator.md): Outbound pub/sub execution, L4 Warden verification, and L5 Actuator receipts.
- [Authentication Architecture](auth.md): SPIFFE workload identities, trusted signers, and L3 human proofs.
- [Storage Architecture](storage.md): Canonical SQLite document store and encrypted persistence boundaries.
- [Network Architecture](network.md): TLS 1.3, mTLS enforcement, and WebSocket pub/sub routing.

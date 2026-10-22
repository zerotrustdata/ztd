---
doc_id: about
title: About ztd
audience: developers, operators, security architects, and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - cmd/ztd/
  - internal/services/gateway/
  - internal/services/operator/
  - protocol/
related:
  - docs/core/position_paper.md
  - docs/architecture/overview.md
  - docs/architecture/governance.md
  - docs/architecture/agents.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/reference/glossary.md
when_to_read: Understanding why ztd exists, its zero-trust execution and evidence model, the separation between Policy Decision Point (Gateway) and Policy Execution Point (Operator), and core architectural boundaries.
do_not_use_for:
  - Step-by-step developer invariants and coding rules (docs/devs/devs.md)
  - Package layout and persistence ownership maps (docs/devs/codemap.md)
  - Test tier specifications and fixture lifecycles (docs/devs/tests.md)
  - Detailed wire protocol message field definitions (protocol/docs/spec.md)
---

# About ztd

## Why ztd Exists

Modern operational environments span heterogeneous infrastructure: relational databases, key-value stores, cloud APIs, object storage, containers, hypervisors, and distributed host operating systems. Routine engineering and incident response across these environments require deep operational context, rigorous validation, and rapid execution.

As autonomous AI agents, coding assistants, and automated workflows are integrated into production environments, a structural security vulnerability emerges: probabilistic language models propose infrastructure mutations directly against high-consequence systems. A prompt injection, hallucinated parameter, poisoned context window, or unvalidated tool call can immediately trigger data loss, configuration drift, unauthorized exfiltration, or operational downtime.

The core principle of ztd is unambiguous: **A model can ask, but it cannot directly act.**

Trust in an autonomous agent or human operator does not stem from conversational fluency, internal model alignment, or subjective confidence. Trust derives strictly from the resource owner's deterministic policy controls, cryptographic boundaries, and immutable execution evidence. ztd provides the zero-trust governance and execution substrate that enforces these boundaries between untrusted proposals and real-world side effects.

## The Operating Method

ztd enforces a disciplined seven-stage operating method for high-consequence system mutations:

1. **Gather Context**: Collect telemetry, logs, configurations, and environment state from the affected systems.
2. **Formulate High-Signal Inquiries**: Ask focused questions that reduce operational uncertainty before committing changes.
3. **Converge on Justified Actions**: Evaluate candidate steps deterministically rather than executing the first plausible hypothesis.
4. **Present Explicit Intent**: Format proposed mutations, affected targets, and cryptographic justifications for accountable review.
5. **Bind Human Authorization**: Bind cryptographic human approval to the canonical transaction hash when required by policy.
6. **Execute at the Local Boundary**: Dispatch verified mutations through a constrained, host-local execution boundary at the system owning the data.
7. **Preserve Signed Evidence**: Record cryptographically signed pre-execution and post-execution receipts, commit audit ledger entries, and verify completion.

Reasoning workflows—whether powered by proprietary frontier models, local open-weights engines, or human operators—operate strictly as proposers outside the trusted computing base. The platform supplies the identity, policy enforcement, verification, execution, and receipt boundary between proposed intent and target systems.

## What ztd Is

ztd is a zero-trust execution and evidence platform designed for AI agents, human operators, and distributed target runtimes. It governs actions rather than actors. Every mutation crossing the platform boundary is modeled as a strongly typed, state-bound protobuf `GovernanceEnvelope`. Mutations execute only after satisfying all gates mandated by the active governance posture.

The platform enforces a strict separation of concerns between Policy Decision Points (PDP) and Policy Execution Points (PEP):

| Component | Architecture Role | Responsibilities |
| :--- | :--- | :--- |
| **`ztd Protocol`** | Wire Contract & Standards | Canonical Protobuf contracts (`GovernanceEnvelope`, `ActionReceipt`, `CommitmentAttestation`), canonical hashing, identity bindings, replay defenses, and proof schemas. |
| **`Governance Gateway (ztdg)`** | Policy Decision Point (PDP) | Workload authentication, PKI CA management, envelope construction, L1 Doctrine enforcement, L2 Consensus coordination, L3 Notary ceremonies, routing, and platform APIs. Hosts an in-process Operator substrate strictly for Gateway-local operations. |
| **`Governed Operator (ztdo)`** | Policy Execution Point (PEP) | Host-side execution daemon running on managed infrastructure. Connects outbound over mTLS, pulls work from session-specific channels, re-verifies envelopes at L4 Warden, executes via L5 Actuator, and retains authoritative local audit records and git file ledgers. Exposes no inbound listening ports. |
| **`Consensus Service`** | Quorum Decision Body | Collects transaction proposals and emits Ed25519 signature votes over `<tx_hash>\|<decision>`. Evaluates configured quorum and membership rules. |
| **`Clients & Interfaces`** | Proposers & Observers | AI agents, Model Context Protocol (MCP) clients, Agent-to-Agent (A2A) callers, CLI sessions, and the embedded browser console propose actions and inspect receipts without joining the trusted execution boundary. |

The reference Gateway and Operator runtimes are packaged within a single statically linked Go binary (`ztd`). The repository also embeds the browser console SPA directly into the Gateway binary. These components represent modular reference implementations of separable protocol roles.

## The Verification Boundary

Every governed mutation must clear the five-layer governance pipeline before execution:

1. **L1 Doctrine**: Decodes typed payloads and enforces technical hard gates, regex constraints, and deterministic MITRE ATT&CK threat heuristics. L1 is unconditionally enforced across all postures.
2. **L2 Consensus**: Verifies Ed25519 quorum signatures from enrolled consensus members against configured policy thresholds when multi-agent agreement is required.
3. **L3 Notary**: Verifies transaction-bound human approval. Gateway workflows enforce WebAuthn passkey ceremonies; outbound Operator workflows verify signed cryptographic approval proofs.
4. **L4 Warden**: Executes host-side pre-dispatch verification. Durably reserves nonces, checks TTL expiration, recomputes the canonical transaction hash, verifies the bound state root, re-runs L1 Doctrine, and verifies posture-required L2 and L3 proofs.
5. **L5 Actuator**: The sole authorized execution boundary on the host. Signs and persists an initial `EXECUTING` receipt, appends a signed commitment to the SQLite commitment ledger, rehydrates scrubbed tokens locally, mints a single-use transaction capability, dispatches the handler, dissolves the capability, and persists the signed final receipt and persistence attestation.

The startup-selected **Governance Posture** establishes whether L2 and L3 operate as blocking gates or non-blocking audited observations:

| Posture | L1 Doctrine | L2 Consensus | L3 Notary (Mutations) | L4 Warden | L5 Actuator |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`doctrine`** | Enforced | Audited | Audited | Enforced | Enforced |
| **`consensus`** | Enforced | Enforced | Audited | Enforced | Enforced |
| **`ratify`** | Enforced | Audited | Enforced | Enforced | Enforced |
| **`notary`** | Enforced | Enforced | Enforced | Enforced | Enforced |

Read-only inspection actions bypass L3 Notary human approval across all postures. Universal integrity checks (L1, L4, and L5) are mandatory in every posture.

## Sovereignty, Evidence, and Boundaries

ztd anchors execution authority and authoritative forensic evidence at the data owner's local boundary:

- **Outbound-Only Connectivity**: Governed Operators initiate outbound mTLS connections to the Gateway and expose no inbound network ports. Operators function securely behind NAT, firewalls, and restricted network perimeters.
- **Independent Execution Verification**: Remote Operators independently re-verify every envelope at L4 Warden before mutating the host. Gateway admission alone cannot compel host execution.
- **Sovereign Execution Boundary**: Sensitive values (tokens, credentials, PII) are redacted or converted to reversible placeholders (`{{UEI_<owner>_N}}`) before crossing network boundaries. Token rehydration occurs strictly at L5 Actuator on the runtime that minted the token, immediately prior to dispatch; another runtime's token never resolves locally.
- **Local-First Audit Architecture (LFAA)**: Each Operator maintains an authoritative local SQLite audit database and a git-backed file history ledger. Gateway audit stores maintain mirrored projections, but the target host remains the authoritative root of forensic truth.
- **Vault Encryption**: Local audit stores, execution events, and token maps are encrypted at rest using AES-256-GCM via the local vault service.
- **Explicit Claim Boundary**: Cryptographic receipts attest strictly to operations that traversed the governed ztd pipeline. ztd does not sandbox unmanaged host processes, client-native shell access, external MCP servers, or unmonitored side channels outside its boundary.

## Reference Implementation

The v1.0.0 reference implementation delivers:

- **Unified Ingress Surfaces**: Model Context Protocol (MCP) over HTTP and stdio, Agent-to-Agent (A2A) JSON-RPC endpoints (`POST /api/v1/a2a/call`), governed HTTP command dispatch (`POST /api/v1/operators/commands`), direct envelope ingestion (`POST /api/v1/governance/envelopes`), and CLI execution (`ztd operator run`).
- **Comprehensive Cryptographic Security**: Strict TLS 1.3 with post-quantum hybrid key exchange (`X25519MLKEM768`), SPIFFE workload identity, X.509 PKI hierarchy, and pure-Go FIPS 140-3 cryptographic module linkage.
- **Governed Native Tooling**: 32 compiled native tools for filesystem inspection, process telemetry, network diagnostics, database queries, container and Kubernetes management, cloud metadata extraction, shell execution, and audit receipt verification.
- **Embedded Browser Console**: React and TypeScript SPA embedded directly into the Gateway binary, providing WebAuthn authentication, real-time approval queues, and Operator inventory management.
- **Real-Time Streaming**: High-throughput Server-Sent Events (SSE) bus with sequence-tracked reconnection replay for browser and CLI sessions.

## Related Documentation

- [Position Paper](position_paper.md): The architectural and philosophical thesis on agentic execution and data custody.
- [System Architecture Overview](../architecture/overview.md): System topology, component interactions, and transaction lifecycles.
- [Governance Architecture](../architecture/governance.md): Detailed five-layer pipeline specifications, posture policies, and receipts.
- [AI Agents and Governance Boundary](../architecture/agents.md): Agent integration patterns, tool boundaries, and bypass limits.
- [Gateway Architecture](../architecture/gateway.md): Gateway control plane, routing, PKI, and embedded operator substrates.
- [Operator Architecture](../architecture/operator.md): Host execution mechanics, native tools, and local audit storage.
- [Consensus Architecture](../architecture/consensus.md): Quorum algorithms, policy rules, and multi-agent deliberation.
- [Authentication Architecture](../architecture/auth.md): mTLS, SPIFFE workload identities, WebAuthn, and CLI proofs.
- [Encryption Architecture](../architecture/encryption.md): Vault hierarchy, envelope encryption, token scrubbing, and key management.
- [Storage Architecture](../architecture/storage.md): SQLite audit persistence, state Merkle trees, and git file ledgers.
- [Protocol Specification](../../protocol/docs/spec.md): Wire format definitions, message schemas, and transaction hashing.
- [Glossary](../reference/glossary.md): Authoritative platform terminology, acronyms, and concept definitions.

---
doc_id: about
title: About ztd
audience: developers, operators, security architects, and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - cmd/ztd/
  - internal/services/gateway/
  - internal/services/governance/
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

Operational environments span databases, key-value stores, cloud APIs, object storage, containers, hypervisors, and host operating systems. Engineering and incident response in these environments need operational context, validation, and execution.

AI agents, coding assistants, and automated workflows that act in these environments can propose mutations directly against production systems. A prompt injection, hallucinated parameter, or unvalidated tool call can trigger data loss, configuration drift, exfiltration, or downtime.

The core principle of ztd is: **A model can ask, but it cannot directly act.**

ztd places a governance and execution boundary between proposals and side effects. Authority to mutate a target comes from the policy, signatures, and local execution records that ztd checks, not from the proposer's fluency or stated confidence.

## The Operating Method

The method ztd enforces for high-consequence changes (gather context, formulate inquiries, converge on a justified action, present explicit intent, bind human authorization, execute at the local boundary, and preserve signed evidence) is described in the [Position Paper](position_paper.md). This page covers only the platform boundary that enforces it.

Reasoning workflows, whether frontier models, local models, or human operators, act as proposers outside the trusted boundary. ztd supplies identity, policy checks, verification, execution, and receipts between a proposal and the target system.

## What ztd Is

ztd is a zero-trust execution and evidence platform for AI agents, human operators, and distributed target runtimes. It governs actions rather than actors. Every governed mutation is modeled as a typed protobuf `GovernanceEnvelope` and executes only after the gates required by the active governance posture pass.

The platform separates Policy Decision Points (PDP) from Policy Execution Points (PEP):

| Component | Architecture Role | Responsibilities |
| :--- | :--- | :--- |
| **`ztd Protocol`** | Wire Contract & Standards | Protobuf contracts (`GovernanceEnvelope`, `EventReceipt`, `CommitmentAttestation`), canonical hashing, identity bindings, replay defenses, and proof schemas. See [protocol/docs/spec.md](../../protocol/docs/spec.md). |
| **`Governance Gateway (ztdg)`** | Policy Decision Point (PDP) | Workload authentication, PKI CA management, envelope construction, L1 Doctrine, L2 Consensus coordination, L3 Notary ceremonies, routing, and platform APIs. Hosts an in-process Operator substrate for Gateway-local actions. |
| **`Governed Operator (ztdo)`** | Policy Execution Point (PEP) | Host-side daemon. Connects outbound over mTLS to the Gateway and receives dispatched work over that connection. Re-verifies envelopes at L4 Warden, executes via L5 Actuator, and keeps local execution events and a git file ledger. Operators expose no inbound listening ports. |
| **`Consensus`** | Quorum Decision Body | Collects votes and verifies Ed25519 signatures over `<message_id>\|<decision>`. The bound provider is an enrolled consensus application or the Gateway-hosted Tribunal panel (default quorum 3-of-5, see `internal/constants/consensus.go`). |
| **`Clients & Interfaces`** | Proposers & Observers | AI agents, MCP clients, A2A callers, CLI sessions, and the embedded browser console propose actions and inspect receipts. They are outside the execution boundary. |

The reference Gateway and Operator runtimes ship in one statically linked Go binary (`ztd`). The browser console is embedded in the Gateway binary. These are reference implementations of separable protocol roles.

## The Verification Boundary

Every governed mutation passes the five-layer pipeline before execution:

1. **L1 Doctrine**: Decodes typed payloads and applies technical hard gates, regex constraints, and deterministic MITRE ATT&CK-style heuristics. Enforced in every posture.
2. **L2 Consensus**: Verifies Ed25519 quorum votes from enrolled consensus members against the configured threshold. It applies only to actions that change state and are not bootstrap actions. Read-only targets are skipped. It is enforced only under the `consensus` and `notary` postures.
3. **L3 Notary**: Verifies human approval bound to the transaction. Gateway workflows verify WebAuthn passkey approvals. Outbound Operators verify CLI approval proofs signed by a key chaining to the pinned Gateway CA roots, or passkey approval proofs. It applies to every mutation action, including `MCP_CALL`, except actions authorized by decision (enrollment, issuance, and policy persistence), and is enforced only under the `ratify` and `notary` postures.
4. **L4 Warden**: Runs host-side pre-dispatch checks. Reserves nonces durably, checks expiry, recomputes the canonical transaction hash, verifies the bound state root, re-runs L1 Doctrine, and verifies the L2 and L3 proofs the posture requires.
5. **L5 Actuator**: The execution boundary on the host. Signs and persists an `EXECUTING` receipt, appends a signed commitment to the local commitment ledger, rehydrates scrubbed tokens locally, mints a single-action capability, dispatches the handler, dissolves the capability, and persists the signed final receipt.

The startup-selected governance posture (`--posture`, default `doctrine`, see `./ztd gw start --help`) decides whether L2 and L3 block execution or are only audited:

| Posture | L1 Doctrine | L2 Consensus | L3 Notary (Mutations) | L4 Warden | L5 Actuator |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **`doctrine`** | Enforced | Audited | Audited | Enforced | Enforced |
| **`consensus`** | Enforced | Enforced | Audited | Enforced | Enforced |
| **`ratify`** | Enforced | Audited | Enforced | Enforced | Enforced |
| **`notary`** | Enforced | Enforced | Enforced | Enforced | Enforced |

L1, L4, and L5 run in every posture. The posture-to-predicate mapping is in `internal/constants/posture.go`.

## Sovereignty, Evidence, and Boundaries

ztd keeps execution authority and forensic evidence at the data owner's local boundary:

- **Outbound-Only Connectivity**: Operators initiate outbound mTLS connections to the Gateway and need no inbound network route. Operator identity is a SPIFFE workload identity presented over that mTLS connection.
- **Independent Execution Verification**: An Operator re-runs the L4 checks on every envelope before it mutates the host. Gateway admission alone is not sufficient to execute on a host.
- **Token Scrubbing**: Values matched by the scrubbing boundary's patterns are replaced with reversible placeholders (`{{UEI_<owner>_<N>}}`) before crossing network boundaries. Rehydration happens only at L5 on the runtime that minted the token, immediately before dispatch. Another runtime's tokens do not resolve locally.
- **Local-First Events Architecture (LFEA)**: Each Operator keeps an authoritative Local-First Events Store and a git-backed file ledger for the events it executes. The Gateway's Local-First Events Store holds mirrored projections, which are best-effort. For events an Operator executed, the Operator's local records are the reference.
- **Encryption at Rest**: Audit content, token maps, and ledger entries are encrypted with AES-256-GCM through the vault service. The stores use the vault's `EncryptWithAAD` path. See [encryption.md](../architecture/encryption.md).
- **Limits of the Claim**: Receipts attest only to operations that traversed the governed pipeline. ztd does not sandbox unmanaged host processes, client-native shell access, external MCP servers, or side channels outside the pipeline. Approval strength depends on how the approving identity was enrolled. Platform-wide assurance requires that all execution paths on a host go through the Operator.

## Reference Implementation

The reference implementation provides:

- **Ingress Surfaces**: MCP over HTTP (`/mcp`) and stdio, A2A JSON-RPC (`POST /api/v1/a2a/call`), governed HTTP command dispatch (`POST /api/v1/operators/commands`), direct envelope submission (`POST /api/v1/governance/envelopes`), and `ztd operator run`. Paths are defined in `protocol/constants/api_paths.json`.
- **Transport and Identity**: TLS 1.3 minimum, with `X25519MLKEM768` preferred for key exchange (`internal/certs/embed.go`), SPIFFE workload identity, and an X.509 PKI hierarchy. FIPS 140-3 module linkage is a build-time setting. See [fips140-3.md](../reference/fips140-3.md) for the scope.
- **Native Tools**: Governed native tools are registered in `RegisterNativeTools` (`internal/services/mcp/native_tool_registry.go`). The tool list is not copied here.
- **Embedded Browser Console**: A React and TypeScript SPA embedded in the Gateway binary. It provides WebAuthn sign-in, an approvals view, and Operator inventory. See [console.md](../architecture/console.md).
- **Streaming**: Server-Sent Events (SSE) with `Last-Event-ID` replay on reconnect (`internal/services/gateway/sse_controller.go`).

## Related Documentation

- [Position Paper](position_paper.md): The architectural and philosophical thesis on agentic execution and data custody.
- [System Architecture Overview](../architecture/overview.md): System topology, component interactions, and transaction lifecycles.
- [Governance Architecture](../architecture/governance.md): Five-layer pipeline specifications, posture policies, and receipts.
- [AI Agents and Governance Boundary](../architecture/agents.md): Agent integration patterns, tool boundaries, and bypass limits.
- [Gateway Architecture](../architecture/gateway.md): Gateway control plane, routing, PKI, and embedded operator substrate.
- [Operator Architecture](../architecture/operator.md): Host execution mechanics, native tools, and local audit storage.
- [Consensus Architecture](../architecture/consensus.md): Quorum algorithms, policy rules, and multi-agent deliberation.
- [Authentication Architecture](../architecture/auth.md): mTLS, SPIFFE workload identities, WebAuthn, and CLI proofs.
- [Encryption Architecture](../architecture/encryption.md): Vault hierarchy, envelope encryption, token scrubbing, and key management.
- [Storage Architecture](../architecture/storage.md): SQLite audit persistence, state Merkle trees, and git file ledgers.
- [Protocol Specification](../../protocol/docs/spec.md): Wire format definitions, message schemas, and transaction hashing.
- [Glossary](../reference/glossary.md): Authoritative platform terminology, acronyms, and concept definitions.

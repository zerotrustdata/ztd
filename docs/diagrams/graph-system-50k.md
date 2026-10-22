---
doc_id: diagram-system-50k
title: "Diagram: System Overview 50,000-Foot View"
audience: maintainers, architects, and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - cmd/ztd/
  - internal/services/gateway/
  - internal/services/
related:
  - docs/architecture/overview.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/network.md
when_to_read: Understanding the high-level architecture, single static binary dual-role model, and zoom series.
do_not_use_for:
  - Detailed service composition (see graph-gateway-services.md)
  - Detailed pre-dispatch verification (see graph-operator-pipeline-l1-l5.md)
---

# Diagram: System Overview 50,000-Foot View

Provides the comprehensive 50,000-foot view of the platform. The single static `ztd` binary deploys into two primary architectural roles:

- **Governance Gateway (Policy Decision Point, PDP)**: Coordinates incoming agent intent, L1–L3 governance, platform PKI, WebSocket pub/sub brokering, and SSE delivery. Sits directly on top of an embedded Operator substrate in the same process when running in Gateway mode.
- **Governed Operator (Policy Execution Point, PEP)**: The execution substrate beneath all operations. Enforces L4 Warden pre-dispatch verification and L5 Actuator execution boundaries. Deploys embedded within the Gateway for local operations, or as an independent outbound daemon on sovereign remote nodes.

```mermaid
graph TD
    subgraph GatewayProcess ["Governance Gateway (PDP) — Single Static Binary, Gateway Mode"]
        direction TB

        subgraph GWStack ["Gateway Service Stack"]
            direction TB
            Surfaces["Protocol Surfaces<br/>HTTP :8080 Discovery · HTTPS :8443 mTLS"]
            Services["Core Services<br/>PKI · Auth · Registration · Passkeys"]
            Persistence["Persistence<br/>SQLite Stores · Audit · Merkle Roots"]
            Broker["Pub/Sub Broker<br/>WebSocket Fan-out · cmd:* · event_receipts:*"]
            MCP["MCP / A2A Gateway<br/>/mcp · /api/v1/a2a/call"]
            Gov["Governance Surface<br/>Envelope Admission · Consensus · Dispatch"]
            SSE["SSE Event Bridge<br/>Push · Poll · Stream"]
        end

        subgraph OpSubstrate ["Operator Substrate (PEP) — Embedded"]
            direction TB
            Loopback["Loopback Pub/Sub Bridge"]
            L4["L4 Warden<br/>Re-validates L1 · State Root · L2 · L3"]
            L5["L5 Actuator<br/>JIT Capability · Commitment · Signed Event receipt"]
            Loopback --> L4 --> L5
        end

        Gov -- "ProcessEnvelope (synchronous)" --> Loopback
        Broker -- "Loopback publish" --> Loopback
        MCP --> Loopback
    end

    subgraph RemoteHost ["Remote Host — Outbound Governed Operator"]
        direction TB
        RemoteOp["Governed Operator (PEP)<br/>Same Binary · operator start"]
        RemoteL4["L4 Warden<br/>Re-validates L1–L4 locally"]
        RemoteL5["L5 Actuator<br/>Execution + Signed event receipt"]
        RemoteLedger[("Local LFAA Ledger<br/>& Audit Vault")]
        RemoteOp --> RemoteL4 --> RemoteL5 --> RemoteLedger
    end

    subgraph Clients ["Clients & Workloads"]
        Agent["AI Agent / MCP Client"]
        BYO["CLI / BYO App<br/>(Envelope or Dispatch)"]
        Browser["Browser · Console<br/>(WebAuthn Session)"]
    end

    Agent -. "mTLS · Streamable HTTP" .-> Surfaces
    BYO -. "mTLS · HTTPS :8443" .-> Gov
    Browser -. "HTTPS :8443 · Session Cookie" .-> Surfaces

    Broker -. "Outbound-only mTLS WSS<br/>cmd:&lt;id&gt;:&lt;session&gt;" .-> RemoteOp
    RemoteOp -. "event_receipts:&lt;id&gt;:&lt;session&gt;<br/>Signed EventReceipt" .-> Broker
```

## Architecture Zoom Series

This diagram anchors the platform architecture zoom series:

1. **50k ft (This Document)**: Platform overview, dual-role single binary model, and Gateway/Operator relationship.
2. **Fleet Topology** ([graph-gateway-fleet-single-host-http-mtls.md](./graph-gateway-fleet-single-host-http-mtls.md)): Network port assignments, mTLS boundaries, and remote host deployments.
3. **Gateway Services** ([graph-gateway-services.md](./graph-gateway-services.md)): Internal Gateway service layers, dependency graphs, protocol endpoints, and in-process substrate layering.
4. **Operator Pipeline** ([graph-operator-pipeline-l1-l5.md](./graph-operator-pipeline-l1-l5.md)): Detailed five-layer governance interlock, L4 Warden verification, and L5 Actuator execution.
5. **Operator Lifecycle** ([graph-operator-lifecycle.md](./graph-operator-lifecycle.md)): State machine covering enrollment, connection, binding, liveness tracking, and termination.

## Key Architectural Invariants

- **Single Binary Architecture**: The single static binary (`ztd`) runs as Gateway (`ztd gw start --posture <posture>`) or Operator (`ztd operator start`).
- **Layered Substrate**: The Gateway is not an isolated service; it is an administrative service stack layered directly over the in-process Operator substrate. Local commands never traverse external network stacks.
- **Listener Isolation**: Remote Operators open **no inbound listening ports**, connecting strictly outbound to the Gateway via mTLS WebSockets.
- **Session-Scoped Channels**: Work is multiplexed across dedicated session channels (`cmd:<id>:<session>`, `event_receipts:<id>:<session>`), eliminating cross-tenant broadcast risks.
- **Local-First Audit Architecture (LFAA)**: Each executing Operator is the sole authority for its local audit database and state Merkle root. Gateway event receipt copies are verified mirrors.

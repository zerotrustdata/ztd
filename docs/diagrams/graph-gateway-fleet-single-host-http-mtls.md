---
doc_id: diagram-gateway-fleet-topology
title: "Diagram: Gateway Fleet Host Topology"
audience: maintainers, architects, and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - internal/services/gateway/
  - internal/services/network/
related:
  - docs/architecture/overview.md
  - docs/architecture/network.md
  - docs/architecture/operator.md
  - docs/architecture/gateway.md
when_to_read: Visualizing platform fleet network topologies, port allocations, mTLS boundaries, and outbound operator communication.
do_not_use_for:
  - Detailed internal gateway service layout (see graph-gateway-services.md)
  - Pre-dispatch verification sequence (see graph-operator-pipeline-l1-l5.md)
---

# Diagram: Gateway Fleet Host Topology

Depicts the standard fleet topology: a centralized Governance Gateway (Policy Decision Point, PDP) coordinating client ingress and one or more outbound Governed Operators (Policy Execution Points, PEP) deployed on sovereign host nodes.

```mermaid
graph TD
    subgraph Clients ["Clients — Ingress Boundaries"]
        MCP["MCP Clients<br/>(Claude Code · Codex · Goose · Gemini)"]
        CLI["CLI / BYO App<br/>(mTLS · Direct envelope / dispatch)"]
        Browser["Browser<br/>(Console SPA served by Gateway)"]
    end

    GW["Governance Gateway · ztdg (PDP)<br/>HTTP :8080 Discovery · HTTPS :8443 mTLS<br/>PKI · L1–L3 · Pub/Sub Broker · SSE Bridge"]

    subgraph Fleet ["Sovereign Hosts — Outbound-Only PEP"]
        O1["Data Operator · ztdo<br/>cmd:&lt;operator-id&gt;:&lt;session-id&gt;"]
        O2["Inference Operator · ztdo<br/>(Evaluation Profile)"]
        D1[("Local LFAA Ledger<br/>Authoritative Evidence")]
        O1 --- D1
        O2 --- D1
    end

    MCP -. "mTLS · Streamable HTTP<br/>/mcp · /api/v1/a2a/call" .-> GW
    CLI -. "mTLS · HTTPS :8443" .-> GW
    Browser -. "HTTPS :8443 · WebAuthn Cookie" .-> GW

    GW -. "Outbound-only mTLS WSS<br/>publish cmd:* · subscribe receipts:*" .-> O1
    GW -. "Outbound-only mTLS WSS" .-> O2
    O1 -. "Signed ActionReceipt<br/>(best-effort mirror)" .-> GW
    O2 -. "Signed ActionReceipt" .-> GW
```

## Ingress Paths

| Path | Primary Client | Gateway Surface | Executing Execution Point (PEP) |
| :--- | :--- | :--- | :--- |
| **Gateway MCP / A2A** | AI Agents, MCP Tools | `/mcp`, `/api/v1/a2a/call` | In-process Gateway Operator substrate or downstream target |
| **Governed HTTP Dispatch** | Enrolled Applications | `POST /api/v1/operators/commands` | Bound outbound Operator session |
| **Direct Envelope Ingress** | CLI, Operator Identity | `POST /api/v1/governance/envelopes` | In-process Gateway Operator substrate |
| **SSE Telemetry** | Browsers, CLI, TUI | `GET /api/v1/sse/stream` | Telemetry stream only; not an execution path |

## Key Architectural Boundaries

- **Unified Binary, Distinct Modes**: A single static binary (`ztd`) runs as Gateway (`ztd gw start`) or Operator (`ztd operator start`). Gateway mode embeds an in-process Operator substrate for local execution.
- **Listener Isolation**: Remote Operators establish outbound-only mTLS WebSocket connections to the Gateway pub/sub broker (`/api/v1/pubsub/stream`). Managed operator hosts expose **zero inbound listening ports**.
- **Sovereign Audit Storage (LFAA)**: Each Operator strictly owns its local SQLite audit database and Merkle state root under `.ztd/`. Receipts published back to the Gateway are best-effort projections and never override local state.

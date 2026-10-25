---
doc_id: diagram-gateway-fleet-topology
title: "Diagram: Gateway Fleet Host Topology"
audience: maintainers, architects, and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
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
        CLI["CLI / Enrolled App<br/>(Direct envelope / governed dispatch)"]
        Browser["Browser<br/>(Console SPA served by Gateway)"]
    end

    GW["Governance Gateway (PDP)<br/>HTTP :8080 plaintext discovery · HTTPS :8443 TLS 1.3<br/>PKI · L1–L3 · Pub/Sub Broker · SSE"]

    subgraph Fleet ["Sovereign Hosts — Outbound-Only PEP"]
        O1["Governed Operator · data<br/>cmd:&lt;operator-id&gt;:&lt;session-id&gt;"]
        O2["Governed Operator · inference"]
        D1[("Per-Operator Local Store<br/>SQLite events DB · Commitment Ledger")]
        D2[("Per-Operator Local Store<br/>SQLite events DB · Commitment Ledger")]
        O1 --- D1
        O2 --- D2
    end

    MCP -. "MCP / A2A · mTLS by default, JWT when JWKS configured<br/>/mcp · /api/v1/a2a/call" .-> GW
    CLI -. "HTTPS :8443 · per-route auth" .-> GW
    Browser -. "HTTPS :8443 · session cookie" .-> GW

    GW -. "Operator-initiated WebSocket<br/>/api/v1/pubsub/stream · publish cmd:*" .-> O1
    GW -. "Operator-initiated WebSocket<br/>/api/v1/pubsub/stream" .-> O2
    O1 -. "Signed receipts<br/>(best-effort mirror)" .-> GW
    O2 -. "Signed receipts<br/>(best-effort mirror)" .-> GW
```

## Ingress Paths

| Path | Primary Client | Gateway Surface | Executing Execution Point (PEP) |
| :--- | :--- | :--- | :--- |
| **Gateway MCP / A2A** | AI Agents, MCP Tools | `/mcp`, `/api/v1/a2a/call` | Embedded Operator substrate or outbound Operator, per operator type |
| **Governed HTTP Dispatch** | Enrolled Applications | `POST /api/v1/operators/commands` | Embedded Operator substrate or outbound Operator (cmd channel), per operator type |
| **Direct Envelope Ingress** | CLI, agent harness clients | `POST /api/v1/governance/envelopes` | Target operator resolved by the Gateway (embedded or outbound) |
| **SSE Telemetry** | Browsers, CLI | `GET /api/v1/sse/stream` | Telemetry stream only; not an execution path |

## Key Architectural Boundaries

- **Unified Binary, Distinct Modes**: A single static binary (`ztd`) runs as Gateway (`ztd gw start`) or Operator (`ztd operator start`). Gateway mode embeds an in-process Operator substrate for local execution.
- **Listener Isolation**: Remote Operators establish outbound WebSocket connections to the Gateway pub/sub broker (`/api/v1/pubsub/stream`). Operators expose no inbound listeners; `ztd operator start` has no listen flags.
- **Gateway Auxiliary Listeners**: Defaults from `ztd gw start --help`: `--listen-host` 127.0.0.1; public spectator private listener 127.0.0.1:8081 and public listener 127.0.0.1:8082; eval explorer 127.0.0.1:5173; pprof disabled unless `--pprof-listen` is set.
- **Sovereign Audit Storage (LFEA)**: Each Operator strictly owns its local SQLite events database and Commitment Ledger under `.ztd/`. Receipts published back to the Gateway are best-effort projections and never override local state.

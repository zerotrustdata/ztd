---
doc_id: diagram-gateway-services
title: "Diagram: Gateway Service Stack"
audience: maintainers, architects, and coding agents
status: current
version: v1.3.0
last_updated: 2026-10-23
owners:
  - internal/services/gateway/
  - internal/cli/serve/gateway.go
related:
  - docs/architecture/overview.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/governance.md
when_to_read: Understanding Gateway internal architecture, dependency graphs, protocol routers, and in-process operator substrate layering.
do_not_use_for:
  - Remote operator lifecycle state machine (see graph-operator-lifecycle.md)
  - Full platform 50k ft overview (see graph-system-50k.md)
---

# Diagram: Gateway Service Stack

Depicts the internal service architecture of the Governance Gateway (`GatewayModeService` in `internal/services/gateway/gateway_service.go`) running in Gateway mode. The Gateway acts as the Policy Decision Point (PDP), coordinating L1–L3 governance layers, platform PKI, routing, pub/sub brokering, and SSE delivery, layered on top of an embedded Operator substrate (PEP) that handles L4 verification and L5 execution.

```mermaid
graph TD
    subgraph GatewayServices ["Gateway Service Stack (PDP) — Coordinates L1–L3"]
        direction TB

        subgraph Edge ["1 — Protocol Surfaces"]
            direction LR
            HTTP["HTTP :8080<br/>Bootstrap · CA discovery · Health · Enrollment"]
            HTTPS["HTTPS :8443<br/>mTLS · Web session · Dual auth"]
        end

        subgraph Core ["2 — Core Services"]
            direction LR
            PKI["Root CA / PKI<br/>CSR enrollment · SPIFFE SANs"]
            Auth["Auth Middleware<br/>Route classification (RouteAuthRegistry)"]
            Reg["Registration Service<br/>Operator slots · Session binding"]
            Passkey["Passkey Handler<br/>WebAuthn assertions & L3"]
        end

        subgraph AppLayer ["3 — Application Layer"]
            direction LR
            subgraph MCP ["MCP / A2A Gateway"]
                MCPEndpoint["Unified MCP Endpoint<br/>/mcp · JSON-RPC dispatch"]
                A2A["A2A Endpoint<br/>/api/v1/a2a/call"]
                MCPGW["MCP Gateway<br/>Envelope normalization"]
            end
            subgraph Governance ["Governance Surface"]
                EnvelopeRx["Envelope Reception<br/>POST /api/v1/governance/envelopes"]
                Dispatch["Operator Dispatch<br/>POST /api/v1/operators/commands"]
                Consensus["Consensus Service<br/>L2 votes (Gateway Tribunal or bound app)"]
            end
            subgraph Delivery ["Delivery & Observation"]
                SSE["SSE Event Bridge<br/>Push · Poll · Stream"]
                Observe["Observe API<br/>Read projections · Producer ingest"]
            end
        end

        subgraph Broker ["4 — Pub/Sub Broker"]
            WSSHandler["WebSocket Handler<br/>cmd:* · results:* · heartbeat:* · receipts:*"]
        end

        subgraph Data ["5 — Persistence Layer"]
            direction LR
            DocStore["Document Store<br/>JSON CRUD · Collection/ID"]
            KVStore["KV Store<br/>TTL-aware ephemeral state"]
            BlobStore["Blob Store<br/>Binary attachments"]
            AuditDB["Audit Authority<br/>Append-only chained events"]
            StateRoot["State Root Service<br/>Merkle root tracking"]
        end

        HTTP --> PKI
        HTTPS --> Auth
        Reg --> DocStore

        EnvelopeRx --> Warden_Entry
        Dispatch --> Consensus
        MCPEndpoint --> MCPGW
        A2A --> MCPGW

        Dispatch -- "Remote operator (cmd publish)" --> WSSHandler
        SSE --> DocStore
        Observe --> DocStore

        DocStore -- "dirty trigger" --> StateRoot
        KVStore -- "dirty trigger" --> StateRoot
        BlobStore -- "dirty trigger" --> StateRoot
    end

    subgraph OperatorSubstrate ["Operator Substrate (PEP) — Owns L4–L5"]
        direction TB
        LoopbackBridge["InProcessPubSubClient<br/>(broker cmd delivery)"]
        Warden_Entry["OperatorPubSubService<br/>ProcessEnvelope (in-process)"]
        Warden["L4 Warden<br/>Pre-dispatch verification"]
        Actuator["L5 Actuator<br/>Execution + Signed receipt"]
        LoopbackBridge --> Warden_Entry
        Warden_Entry --> Warden --> Actuator
    end

    EnvelopeRx -- "ProcessEnvelope (direct)" --> Warden_Entry
    MCPGW -- "Gateway-local (direct)" --> Warden_Entry
    MCPGW -- "Outbound (dispatch)" --> Dispatch
    Dispatch -- "Embedded operator (direct)" --> Warden_Entry
    WSSHandler -- "Loopback publish" --> LoopbackBridge
    Actuator -- "EventReceipt" --> AuditDB
```

## Service Stack Composition

The Gateway coordinates policy decisions through five structured tiers:

1. **Protocol Surfaces**:
   - Plain HTTP on port `8080` serves health, bootstrap and CA discovery, deploy scripts, and platform enrollment routes. It redirects all other requests to HTTPS with `301`.
   - HTTPS on port `8443` serves authenticated APIs, the Console SPA, passkey challenges, WebSocket pub/sub streams, and mTLS endpoints.
2. **Core Security Services**:
   - `PKIAuthority`: Manages Root, Hub, Operator, and Peer CAs, signing CSRs with SPIFFE URI SANs under `ztd.local`.
   - `AuthService.Middleware`: Evaluates routes against `RouteAuthRegistry` (`RouteAuthNone`, `RouteAuthMTLS`, `RouteAuthWebSession`, `RouteAuthDual`).
   - `PasskeyService`: Executes passkey (WebAuthn) registration and assertion ceremonies and brokers L3 proofs.
3. **Application Ingress**:
   - MCP and A2A calls are normalized into typed `GovernanceEnvelope` messages by the MCP gateway service.
   - The Consensus Service routes L2 votes to the bound consensus provider: the Gateway-hosted Tribunal panel (`tribunal`) or an enrolled application (`application`). It votes only on state-change actions.
   - `DispatchApprovalCoordinator` and `mcp.EnvelopeSuspender` hold transactions suspended pending an L3 proof.
4. **Pub/Sub Brokering**:
   - Coordinates outbound WebSocket connections from remote Operators across the `cmd:`, `results:`, `heartbeat:`, and `receipts:` channel prefixes.
5. **Operator Substrate**:
   - The Gateway process embeds an in-process Operator substrate. Gateway-local envelopes call `OperatorPubSubService.ProcessEnvelope` directly, which runs the L4 Warden and L5 Actuator in-process. Broker `cmd:` traffic reaches the embedded Operator through `InProcessPubSubClient` rather than a network socket.

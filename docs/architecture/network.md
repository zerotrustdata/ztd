---
doc_id: network
title: Network Architecture
audience: maintainers and coding agents
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - internal/services/gateway/
  - internal/services/network/
  - internal/netutil/port_probe.go
  - internal/pkg/ssh/dialer.go
  - internal/constants/ports.go
  - internal/constants/channels.go
  - protocol/workload_identity.go
related:
  - docs/architecture/auth.md
  - docs/architecture/gateway.md
  - docs/architecture/operator.md
  - docs/architecture/sse.md
when_to_read: Understanding platform networking, PKI hierarchy, TLS/mTLS enforcement, SPIFFE identity formats, port topology, pub/sub communication, and network identity detection.
do_not_use_for:
  - Authentication and session lifecycle mechanics (docs/architecture/auth.md)
  - Gateway operational modes and policy decision points (docs/architecture/gateway.md)
  - Operator local execution and L4/L5 verification (docs/architecture/operator.md)
  - Server-Sent Events event framing and consumer routing (docs/architecture/sse.md)
---

# Network Architecture

## Purpose

Documents the network architecture of the ztd platform: the Public Key Infrastructure (PKI), SPIFFE workload identity formats, TLS 1.3 transport security, consolidated port topology, outbound-only pub/sub communication patterns, listener isolation, and automated network identity detection.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [PKI Hierarchy and Trust Anchors](#pki-hierarchy-and-trust-anchors)
- [Workload Identity Format (SPIFFE)](#workload-identity-format-spiffe)
- [Transport Security and mTLS Handshake](#transport-security-and-mtls-handshake)
- [Port Topology and Router Classification](#port-topology-and-router-classification)
- [Listener Isolation: Outbound-Only Operators](#listener-isolation-outbound-only-operators)
- [Network Identity Detection and SAN Drift](#network-identity-detection-and-san-drift)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

Key invariant groups: [PKI and Cryptography](#pki-and-cryptography-inv-net-pki), [Workload Identity](#workload-identity-inv-net-id), [Transport Security](#transport-security-inv-net-tls), [Port Topology](#port-topology-inv-net-port), [Communication Patterns](#communication-patterns-inv-net-comm).

## Invariants

Ids are stable. Append the next free number within each group; do not renumber.

### PKI and Cryptography (`INV-NET-PKI`)

| ID | Rule |
| --- | --- |
| INV-NET-PKI-01 | All platform certificates (Root CA, intermediate CAs, serving certificate, and leaf certificates) MUST use ECDSA with the NIST P-256 curve (`elliptic.P256()`). Non-P-256 CSRs MUST be rejected. |
| INV-NET-PKI-02 | The PKI hierarchy MUST separate serving issuance from workload issuance: the Hub Intermediate CA signs only the Gateway serving certificate (`operator-gateway`), while the Operator Intermediate CA signs all workload leaf certificates (`operator`, `cli`, `app`). |
| INV-NET-PKI-03 | Workload leaf certificates MUST have a maximum validity of 7 days. Serving and peer certificates MUST have a maximum validity of 90 days. Root and intermediate CAs MUST have a validity of 3650 days. |
| INV-NET-PKI-04 | Certificate revocation MUST be verified on every mTLS request and WebSocket handshake. An X.509 CRL DER binary signed by the Operator Intermediate CA MUST be exposed at `/.well-known/ztd/pki/crl`. |
| INV-NET-PKI-05 | Platform workload revocation via `POST /api/v1/auth/platform-enrollments/revoke` MUST revoke the issued certificate in persistent storage and immediately disconnect all active pub/sub connections matching the authenticated SPIFFE identity. |

### Workload Identity (`INV-NET-ID`)

| ID | Rule |
| --- | --- |
| INV-NET-ID-01 | The canonical SPIFFE trust domain MUST be `ztd.local` across all deployments. All issued workload certificates MUST embed their SPIFFE identity URI in the Subject Alternative Name (SAN). |
| INV-NET-ID-02 | SPIFFE ID URIs MUST conform to standard type paths: `operator` (`spiffe://ztd.local/operator/<org>/<op>/<session>`), `cli` (`spiffe://ztd.local/cli/<user>/<session>`), `app` (`spiffe://ztd.local/app/<id>`), `user` (`spiffe://ztd.local/user/<user>`), `hub` (`spiffe://ztd.local/hub/operator-listen`), and `gateway-peer` (`spiffe://ztd.local/gateway/<gw>`). |
| INV-NET-ID-03 | Additive SAN drift detected at Gateway startup (missing host IPs or hostnames compared to detected network identity) MUST trigger automatic regeneration of the serving certificate. Dropped host aliases MUST NOT trigger regeneration. |

### Transport Security (`INV-NET-TLS`)

| ID | Rule |
| --- | --- |
| INV-NET-TLS-01 | Inbound HTTPS and WebSocket connections MUST enforce TLS 1.3 minimum (`tls.VersionTLS13`). Plain HTTP is restricted to discovery, health checks, and unauthenticated bootstrap/recovery initiation. |
| INV-NET-TLS-02 | The HTTPS listener MUST configure `tls.VerifyClientCertIfGiven` at the transport layer to permit browser clients to access public and console routes without client certificates, while delegating mandatory mTLS enforcement to application-layer middleware. |
| INV-NET-TLS-03 | Client connections resolving the Gateway via IP address MUST provide `ztd.local` as the TLS ServerName (SNI) to satisfy hostname validation against the serving certificate DNS SAN. |

### Port Topology (`INV-NET-PORT`)

| ID | Rule |
| --- | --- |
| INV-NET-PORT-01 | The Gateway MUST expose a consolidated two-port topology: HTTP Discovery/Bootstrap (`8080` default) and HTTPS Merged API/Console (`8443` default). Startup MUST fail if multiple logical surfaces are assigned to the same port. |
| INV-NET-PORT-02 | The plain HTTP router (`buildHTTPRouter`) MUST serve only health, state root, CA bundle discovery, and token-scoped bootstrap/recovery initiation. All unhandled paths MUST redirect with HTTP 301 to HTTPS, validating the `Host` header against `isSafeHost`. |
| INV-NET-PORT-03 | Privileged CSR signing (`/api/v1/pki/csr/sign`), CLI rotation, CLI refresh, CLI session query, and enrollment approval routes MUST NOT be registered on the plain HTTP router. |
| INV-NET-PORT-04 | Gateway port selection MUST reject wildcard and loopback conflicts in either available TCP address family. Probes MUST release their sockets before returning; availability is a check, not a reservation for the eventual listener. |

### Communication Patterns (`INV-NET-COMM`)

| ID | Rule |
| --- | --- |
| INV-NET-COMM-01 | Governed remote Operators MUST establish outbound-only mTLS WebSocket connections to the Gateway pub/sub endpoint (`/api/v1/pubsub/stream`). Operators MUST NOT open inbound listening ports for command, MCP, or A2A traffic. |
| INV-NET-COMM-02 | Pub/sub topic channels (`cmd:`, `results:`, `heartbeat:`, `receipts:`, `renewal:`, `audit:`) MUST be strictly scoped by Operator ID and session ID, and authorization MUST be validated against the caller's verified SPIFFE identity URI SAN. |

## Owned surfaces

| Claim | Path | Verify |
| --- | --- | --- |
| PKI Authority and TLS config | `internal/services/gateway/gateway_certs.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestPKIAuthority` |
| Default port constants | `internal/constants/ports.go` | `protocol/constants/ports.json` and `internal/config/config.go` |
| TCP port availability | `internal/netutil/port_probe.go`, `internal/netutil/port_probe_other.go`, `internal/netutil/port_probe_windows.go` | `./ztd test integration --pkg ./internal/netutil --run TestTCPPortProbe` |
| SSH deployment transport | `internal/pkg/ssh/dialer.go` | `./ztd test integration --pkg ./internal/pkg/ssh` and `./ztd test integration --pkg ./internal/cli/stream` |
| HTTP & HTTPS route muxing | `internal/services/gateway/gateway_http_router.go` | `./ztd test unit --pkg ./internal/services/gateway --run TestGatewayHTTPRouter` |
| SPIFFE identity formatting | `protocol/workload_identity.go` | `./ztd test unit --pkg ./protocol --run TestWorkloadIdentity` |
| Network identity detection | `internal/services/network/identity.go` | `./ztd test unit --pkg ./internal/services/network` |
| Operator pub/sub transport | `internal/services/pubsub/ztdg_pubsub_client.go` | `./ztd test integration --pkg ./internal/services/gateway --run PubSub` |
| Platform enrollment revocation | `internal/services/pubsub/platform_enrollment_handlers.go` | `./ztd test integration --pkg ./internal/services/gateway --run PlatformEnrollment` |

## PKI Hierarchy and Trust Anchors

The platform PKI (`internal/services/gateway/gateway_certs.go`) isolates certificate authorities by functional responsibility:

```text
                             +-----------------------+
                             |      Root CA          |
                             |      (P-256, 10y)     |
                             +-----------------------+
                                         │
        ┌────────────────────────────────┼────────────────────────────────┐
        ▼                                ▼                                ▼
+--------------------+        +--------------------+           +--------------------+
|  Hub Interm. CA    |        | Operator Interm CA |           | Gateway Peer CA    |
|  (P-256, 10y)      |        | (P-256, 10y)       |           | (P-256, 10y)       |
+--------------------+        +--------------------+           +--------------------+
        │                                │                                │
        ▼                                ▼                                ▼
+--------------------+        +--------------------+           +--------------------+
| Serving Cert       |        | Workload Leaves    |           | Peer Certs         |
| (operator-gateway) |        | (operator, cli, app)           | (gateway-peer)     |
| (90-day validity)  |        | (7-day validity)   |           | (90-day validity)  |
+--------------------+        +--------------------+           +--------------------+
```

1. **Root CA (`ztd Root CA`)**: Trust anchor for the entire deployment (10-year validity, ECDSA P-256).
2. **Hub Intermediate CA (`ztd Hub Intermediate CA`)**: Signs exclusively the Gateway serving certificate. Isolation ensures that compromising workload intermediate signing keys does not compromise Gateway TLS identity.
3. **Operator Intermediate CA (`ztd Operator Intermediate CA`)**: Signs all client and workload leaf certificates (`operator`, `cli`, `app`). Signs CRL revocations.
4. **Gateway Peer Intermediate CA (`ztd Gateway Peer Intermediate CA`)**: Signs federated gateway peering identities.
5. **Private Key Storage**: All CA private keys and serving certificate private keys are encrypted in the platform keystore.
6. **Revocation**: Served as an X.509 CRL DER binary at `/.well-known/ztd/pki/crl` and verified on every mTLS handshake and WebSocket upgrade.

## Workload Identity Format (SPIFFE)

Platform identities use the canonical SPIFFE trust domain `ztd.local` and are embedded as URI SANs in leaf certificates (`protocol/workload_identity.go`):

```text
spiffe://ztd.local/<type>/<identity-segments>
```

- **Governed Operator**: `spiffe://ztd.local/operator/<organization_id>/<operator_id>/<operator_session_id>`
- **CLI Client**: `spiffe://ztd.local/cli/<user_id>/<cli_session_id>`
- **Application**: `spiffe://ztd.local/app/<application_id>`
- **User Principal**: `spiffe://ztd.local/user/<user_id>`
- **Gateway Hub**: `spiffe://ztd.local/hub/operator-listen`
- **Gateway Peer**: `spiffe://ztd.local/gateway/<gateway_id>`

The Gateway extracts the SPIFFE URI SAN from peer certificates and matches it against persistent session state. Unauthenticated headers cannot override this identity.

## Transport Security and mTLS Handshake

The Gateway enforces TLS 1.3 across all HTTPS and WebSocket connections:

- **Hybrid Handshake Posture**: The TLS listener is configured with `tls.VerifyClientCertIfGiven`. This permits standard browsers without client certificates to load public routes, passkey ceremonies, and the Console SPA, while capturing and validating client certificates when presented.
- **Application Policy Gate**: Application-layer auth middleware (`gateway_auth.go`) enforces the actual access policy:
  - `RouteAuthNone`: Public endpoints.
  - `RouteAuthMTLS`: Client certificate required; missing or invalid cert results in immediate failure.
  - `RouteAuthWebSession`: Valid `ztd_web_session_cookie` required.
  - `RouteAuthDual`: Prefers mTLS when present; otherwise validates web-session cookie.
- **Mesh Aliasing (`ztd.local`)**: Clients resolving the Gateway via IP address MUST supply `ztd.local` as the TLS Server Name Indication (SNI) to match the DNS SANs on the Gateway serving certificate.

## Port Topology and Router Classification

The Gateway exposes a strict two-port topology:

```text
         +---------------------------------------------------------------+
         |                     ztd Governance Gateway                     |
         +---------------------------------------------------------------+
                             │                              │
                 HTTP listener: 8080                 HTTPS listener: 8443
                             │                              │
                 +---------------------+         +------------------------+
                 | Public Bootstrap    |         | Browser + API + mTLS   |
                 | - /health           |         | - /console/            |
                 | - /state            |         | - Passkey WebAuthn     |
                 | - CA discovery      |         | - SSE streams          |
                 | - Token enrollment  |         | - WebSocket pub/sub    |
                 | - Deploy scripts    |         | - Governance envelopes |
                 | - 301 safe redirect |         | - Observe API          |
                 |   catch-all         |         | - MCP / A2A ingress    |
                 +---------------------+         +------------------------+
```

1. **HTTP Listener (`8080` default)**: Plain HTTP router (`buildHTTPRouter`) serves health checks, state discovery, CA bundles, deploy scripts, and token-scoped enrollment initiation. All other paths return an HTTP 301 redirect to HTTPS, validating the host header against `isSafeHost`.
2. **HTTPS Listener (`8443` default)**: Primary authenticated surface. Negotiates HTTP/2 (`GatewayHTTP2MaxConcurrentStreams = 8192`) via ALPN while supporting HTTP/1.1 for WebSocket connections.
3. **Loopback Auxiliary Ports**: `8081` (Evaluation spectator), `8082` (Public mirror read listener), `5173` (Vite dev). These are auxiliary loopback listeners and never exposed as core protocol surfaces.

Gateway startup resolves occupied ports through `netutil.CheckTCPPortAvailable`. On Unix, it probes IPv4 and IPv6 wildcard sockets separately with address reuse disabled, so macOS/BSD cannot report a loopback listener's port as free. An unavailable IPv6 stack leaves the IPv4 probe authoritative. Windows uses an exclusive bind without listening to avoid firewall prompts. Every probe releases the port before returning, so another process can still claim it before Gateway startup.

## Listener Isolation: Outbound-Only Operators

Governed Operators enforce strict listener isolation:

- **Zero Inbound Ports**: Remote Operators open **no listening ports** (no HTTP, SSH, gRPC, or socket listeners).
- **Outbound mTLS WebSocket**: The Operator initiates an outbound-only connection to `wss://<gateway-host>:8443/api/v1/pubsub/stream` over mTLS.
- **Channel Partitioning**:
  - `cmd:<operator_id>:<operator_session_id>`: Gateway-to-Operator command dispatch.
  - `results:<operator_id>:<operator_session_id>`: Command execution results.
  - `heartbeat:<operator_id>:<operator_session_id>`: Operator liveness telemetry.
  - `receipts:<operator_id>:<operator_session_id>`: Signed `ActionReceipt` publication.
  - `renewal:<operator_id>:<operator_session_id>`: Certificate leaf renewal.
  - `audit:<operator_id>:<operator_session_id>`: Gateway-to-Operator audit ingest.
- **Embedded Substrate**: When running in Gateway mode, Gateway-local operations use an in-process loopback pub/sub bridge without opening network sockets.

SSH deployment and streaming use `internal/pkg/ssh.DialSSH`. Its dial timeout covers connection setup and the SSH handshake. A configured `ProxyCommand` remains alive for the established SSH connection, until the caller cancels or closes it. A failed or timed-out handshake cancels and reaps the proxy process.

## Network Identity Detection and SAN Drift

At startup, the Gateway runs `network.Detector` (`internal/services/network/identity.go`) to discover local network identities:

1. Gathers non-loopback network interface IPs.
2. Reads OS hostnames, `/etc/hosts` aliases, and mDNS/Bonjour names.
3. Queries reverse DNS (PTR) records for discovered addresses.
4. Detects platform-specific identifiers (SSH known hosts, Windows NetBIOS).

**SAN Drift Policy**:
- **Additive Drift**: If newly discovered IP addresses or DNS names are absent from the current serving certificate, the Gateway triggers automatic regeneration of the serving certificate on startup.
- **Subtractive Drift**: Dropped or vanished aliases are ignored to prevent certificate churn in dynamic network environments.

## Procedures

### Inspecting Gateway CA Bundle

Fetch the public trust bundle from the plain HTTP discovery surface:

```bash
curl -s http://localhost:8080/.well-known/ztd/pki/ca-bundle
```

### Validating Gateway Health

```bash
curl -s http://localhost:8080/api/v1/health
```

### Checking Serving Certificate Fingerprint

```bash
curl -s http://localhost:8080/.well-known/ztd/pki/fingerprint
```

## Anti-patterns

- **Opening inbound listening ports on Operators**: Running HTTP or gRPC listeners on remote Operator hosts instead of utilizing outbound-only mTLS WebSockets (`INV-NET-COMM-01`).
- **Connecting by IP without SNI**: Dialing Gateway IPs directly without specifying `ServerName: ztd.local`, which fails TLS certificate verification (`INV-NET-TLS-03`).
- **Registering privileged endpoints on plain HTTP**: Exposing CSR signing, session manipulation, or approvals on port 8080 (`INV-NET-PORT-03`).
- **Relying on transport TLS alone for auth**: Failing to verify SPIFFE URI SANs and session bindings in application-layer middleware (`INV-NET-TLS-02`).
- **Using non-P-256 keys for platform PKI**: Generating RSA or Ed25519 keys for platform CAs or client CSRs (`INV-NET-PKI-01`).

## Links out

- [Authentication & Authorization Architecture](auth.md): Route classification, identity extraction, and session lifecycle.
- [Gateway Architecture](gateway.md): Gateway admission control, operational modes, and policy decision points.
- [Operator Architecture](operator.md): Remote operator execution, L4 Warden verification, and L5 Actuator receipts.
- [SSE Streaming](sse.md): Server-Sent Events architecture and consumer routing.
- [Console Architecture](console.md): Gateway-embedded browser console architecture.
- [Public Spectator Architecture](public_spectator.md): Read-only public mirror and evaluation explorer.
- [Protocol Specification](../../protocol/docs/spec.md): Wire protocols, governance envelope schemas, and message types.
- [Troubleshooting Guide](../devs/troubleshooting.md): PKI diagnostics and connection debugging.

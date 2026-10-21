---
doc_id: air_gap
title: Air-Gapped Deployment
audience: platform operators and infrastructure teams
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/air_gap.md
  - Makefile
  - go.mod
related:
  - docs/guides/unified_stack.md
  - docs/guides/connect_operator_to_gateway.md
  - docs/architecture/network.md
  - docs/architecture/encryption.md
when_to_read: Deploying ztd inside a network-isolated environment; staging binaries and protocol libraries for offline transfer.
do_not_use_for:
  - Runtime encryption and vault mechanics (docs/architecture/encryption.md)
  - Network identity, PKI, and TLS configuration (docs/architecture/network.md)
  - Operator enrollment and cross-gateway connectivity (docs/guides/connect_operator_to_gateway.md)
---

# Air-Gapped Deployment

## Purpose

Documents how to stage ztd for deployment in network-isolated environments using native binaries or source builds. Covers media transfer, offline build procedures, CLI user enrollment, and air-gap verification.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
  - [Supported deployment forms](#supported-deployment-forms)
  - [Runtime network behavior in air-gap](#runtime-network-behavior-in-air-gap)
  - [Local assets and persistence](#local-assets-and-persistence)
  - [Prepare a native binary](#prepare-a-native-binary)
  - [Verification and isolation checklist](#verification-and-isolation-checklist)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

### Isolation Properties (`INV-AIR-ISO`)

| ID | Rule |
| :--- | :--- |
| INV-AIR-ISO-01 | Air-gap isolation is an infrastructure property enforced by host, firewall, and network controls. The CLI has no `--air-gap` switch. |
| INV-AIR-ISO-02 | Every optional integration (JWKS, MCP/A2A proxies, consensus, LLM providers) MUST use an approved internal endpoint or remain unset. Public endpoints MUST NOT be configured inside the air gap. |
| INV-AIR-ISO-03 | Governance admits tools only when invoked through a governed ingress and executed by the L5 Actuator (`INV-ARCH-INGR-01` in `docs/architecture/overview.md`). Client-native tools, direct shell execution, and external MCP servers run outside the governance boundary, so network policy alone MUST control their reachability to DNS, HTTP, SSH, and cloud metadata endpoints. |

### Build and Transfer (`INV-AIR-BUILD`)

| ID | Rule |
| :--- | :--- |
| INV-AIR-BUILD-01 | Native binary builds MUST use `GOTOOLCHAIN=local GOFLAGS=-mod=vendor` to ensure the checked-in vendor tree provides all dependencies. Go 1.27.3 MUST already be installed; automatic toolchain downloading is prohibited. |
| INV-AIR-BUILD-02 | A source archive MUST include the committed Gateway frontend embeds under `internal/services/gateway/console/static/` and `internal/services/gateway/explorer/static/`. `make build` compiles those embeds and does not rebuild frontend `dist/` trees. |
| INV-AIR-BUILD-03 | `make test-airgap` verifies vendor tree presence and a vendored Go build, but does NOT verify runtime egress blocking or endpoint configuration. |

### Runtime Behavior (`INV-AIR-RUNTIME`)

| ID | Rule |
| :--- | :--- |
| INV-AIR-RUNTIME-01 | The Gateway listens on default HTTP port 8080 (bootstrap, CA discovery, token-scoped enrollment) and default HTTPS port 8443 (authenticated APIs, console, WebSocket pub/sub). When either default port is busy, the Gateway selects the next free port pair. Unhandled HTTP paths redirect to HTTPS. |
| INV-AIR-RUNTIME-02 | The Operator opens no inbound service port. It initiates an outbound-only mTLS WebSocket connection to the Gateway's pub/sub endpoint. |
| INV-AIR-RUNTIME-03 | The platform does not send telemetry or error reports to any hosted service. Operational events, SSE events, and audit records remain host-local. |
| INV-AIR-RUNTIME-04 | Gateway and Operator each maintain separate `.ztd/` runtime trees on their respective hosts. Operator state is local and authoritative to that host. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Makefile air-gap target | `Makefile` | `make test-airgap` |
| Go vendor build support | `go.mod`, `vendor/` | `go build -mod=vendor ./...` |
| Gateway port defaults | `internal/constants/ports.go` | HTTP 8080, HTTPS 8443 |

## Procedures

### Supported deployment forms

1. **Native binary**: Transfer a prebuilt static `ztd` binary and its `.sha256` checksum sidecar for the target OS and architecture. No runtime compiler or toolchain is required on the target host.
2. **Offline source build**: Transfer the complete source tree including `vendor/`, the committed evaluation-explorer and console embeds, and a build host with Go 1.27.3 pre-installed.

### Runtime network behavior in air-gap

The Gateway exposes two network-facing ports. Both bind to `127.0.0.1` by default; `--listen-host` sets the bind address for hosts that reach the Gateway over the air-gapped network.

- **HTTP port 8080**: Health checks, initial bootstrap, CA bundle discovery, token-scoped CLI recovery, enrollment, and binary downloads.
- **HTTPS port 8443**: Authenticated APIs, embedded browser console, WebAuthn ceremonies, governance envelopes, MCP/A2A ingress, WebSocket pub/sub, SSE, and audit services.

The `--public-spectator` flag (default on) also starts loopback listeners for the public mirror and evaluation explorer: `127.0.0.1:8081` (authenticated ingest), `127.0.0.1:8082` (anonymous reads), and `127.0.0.1:5173` (explorer). These bind to loopback unless `--public-spectator-allow-container-bind` is set inside a container deployment.

The Operator connects outbound to ports 8080 and 8443 and exposes no inbound ports. All communication between Gateway, Operator, and CLI stays within the air-gapped network perimeter.

### Local assets and persistence

The Gateway browser console is embedded in the binary and served locally without external CDN requests. Runtime state is stored under `.ztd/`:

- `data/`: SQLite database `ztd.db` (state, replay nonces, execution records).
- `data/ledger/`: Encrypted Git-backed ledger repositories.
- `pki/`: Root CA, intermediate CAs, issued certificates, and trust bundles.
- `secrets/`: Keystore secrets, including the vault key (`vault_key`), and the bootstrap digest.
- `vault/`: Vault header (`vault.header`) with the wrapped data-encryption key metadata.
- `logs/`, `pids/`: Operational runtime state.

### Prepare a native binary

Run on a connected build host from the repository root:

```bash
# Verify the checked-in vendor tree
GOTOOLCHAIN=local GOFLAGS=-mod=vendor make test-airgap

# Build for the connected host
GOTOOLCHAIN=local GOFLAGS=-mod=vendor make build

# Or build for all supported platforms
GOTOOLCHAIN=local GOFLAGS=-mod=vendor make build-all
```

Transfer artifacts through the approved media-transfer process:

```bash
cd bin
sha256sum -c ztd-linux-amd64.sha256
install -m 0755 ztd-linux-amd64 /usr/local/bin/ztd
```

Start the Gateway:

```bash
ztd gw start --cert-mode localhost
ztd auth enroll user
```

For headless systems without a browser:

```bash
ztd auth enroll user --headless
```

The `--headless` identity is mTLS-only and cannot authenticate to the console. Another enrolled CLI approves its recovery request with `ztd auth approve-recovery <token>`.

### Verification and isolation checklist

Run `make test-airgap` in the staged source tree before transfer to verify:
1. `vendor/` directory exists.
2. `go build -mod=vendor ./...` succeeds without network access.

On the air-gapped target host:
- Block outbound internet traffic at host firewall and network perimeter.
- Ensure external JWKS, public LLM endpoints, and external MCP/A2A URLs remain unset.
- Point model storage, consensus signers, and DNS at internal endpoints.
- Restrict permissions on `.ztd/` (directory mode 0700, sensitive files 0600).

## Anti-patterns

- Using `GOTOOLCHAIN=auto` during offline builds (INV-AIR-BUILD-01).
- Configuring public internet endpoints inside an isolated environment (INV-AIR-ISO-02).
- Relying on `make test-airgap` to prove runtime network isolation; firewall rules must be verified separately (INV-AIR-BUILD-03).
- Sharing `.ztd/` runtime directories between Gateway and Operator instances (INV-AIR-RUNTIME-04).

## Links out

- [Unified Stack Guide](unified_stack.md) — Host-native stack lifecycle and evaluation topology.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Operator enrollment and connectivity.
- [Network Architecture](../architecture/network.md) — PKI hierarchy, TLS boundaries, and port topology.
- [Encryption Architecture](../architecture/encryption.md) — Vault mechanics and key derivation.

---
doc_id: build-gateway
title: Build Gateway
audience: developers and operators
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - docs/guides/build_gateway.md
  - Makefile
  - cmd/ztd
  - internal/cli/cmd/gw
related:
  - docs/guides/unified_stack.md
  - docs/guides/connect_apps_to_gateway.md
  - docs/guides/build_operator.md
  - docs/architecture/protocol.md
when_to_read: Building ztd from source, deploying the gateway, understanding the gateway binary architecture, or implementing custom gateway-compatible services.
do_not_use_for:
  - CLI commands and runtime behavior (use ./ztd --help or ./ztd <command> --help)
  - Gateway protocol specification and contract details (docs/architecture/protocol.md)
---

# Build a ztd Gateway

## Quick Index

- [Overview](#overview)
- [Reference Implementation](#reference-implementation) — Prerequisites, build from source, build targets, cross-compilation, running the gateway
- [Protocol Library Dependencies](#protocol-library-dependencies) — Go module setup
- [Custom Gateway Implementation](#custom-gateway-implementation) — Required capabilities, invariants, governance modes, session types, two-port architecture, protocol schema
- [Testing](#testing) — Test suite tiers and test commands
- [Manage](#manage) — Gateway lifecycle commands (start, stop, restart, reset, clean, setup, security validation)
- [Monitor](#monitor) — Status, logs, data queries, and Cloudflare Tunnel
- [Next Steps](#next-steps) — Related guides and references

---

## Overview

A ztd Gateway implements the platform's Policy Decision Point (PDP). It manages PKI, canonical platform state, governance admission, messaging, and MCP/A2A protocol translation. The Gateway owns L1 Doctrine, L2 Consensus, and L3 Notary decisions; an in-process Operator substrate applies L4 Warden and L5 Actuator for operations that target the Gateway host.

The reference implementation is the static `ztd` binary running in Gateway mode. The same binary runs as a Governed Operator, the Policy Execution Point (PEP) that connects outbound to a Gateway and applies L4-L5 on a managed host. Custom implementations conform to the same protocol contracts and fail-closed invariants described in the [ztd Protocol specification](../../protocol/docs/spec.md).

---

## Reference Implementation

### Prerequisites

- **Go 1.27.2+** — Required for building the reference Gateway. The root `go.mod` is authoritative; the Makefile selects the toolchain automatically.
- **Make** — Required to run the root build targets.
- **Node.js 22.22.2+, 24.15+, or 26+ and npm** — Required only to rebuild Console or Evaluation Explorer embeds. A plain `make build` uses the committed embeds.

Run the platform setup script to install prerequisites and compile `ztd`:
- **Linux:** `bash scripts/linux-setup.sh --build-only`
- **macOS:** `bash scripts/macos-setup.sh --build-only`
- **Windows:** `pwsh scripts/windows-setup.ps1 --build-only`

### Build from Source

Clone the repository and build the `ztd` binary:

```bash
git clone https://github.com/zerotrustdata/ztd.git && cd ztd
make build
```

This produces the host binary as `./ztd` (`ztd.exe` on Windows) and a named platform binary with a neighboring SHA-256 checksum in `bin/`. The binary is statically linked with `CGO_ENABLED=0` and does not require the Go toolchain or OpenSSL at runtime.

### Build Targets

The Makefile provides several build targets:

- `make build` — Builds `ztd` for the current platform.
- `make build-all` — Builds `ztd` for all supported Linux, Windows, and macOS targets.
- `make build-linux` — Builds Linux binaries for amd64, arm64, and 386.
- `make build-windows` — Builds Windows binaries for amd64 and arm64.
- `make build-darwin` — Builds macOS binaries for amd64 and arm64.
- `make build-compressed` — Builds the current-platform binary and compresses the named `bin/` artifact with UPX.
- `make fips-build` — Builds a FIPS 140-3 approved mode ztd binary for linux/amd64.
- `make fips-verify` — Builds the FIPS variant and runs its self-check with FIPS enforcement enabled.
- `make clean` — Removes `bin/`, test and coverage artifacts, harness directories, and the Go build cache (the shared module cache is left alone; builds use `vendor/`). It preserves `.ztd/`, workload identities, and the repository-root `ztd` binary.

To intentionally reset Gateway state, use `./ztd gw clean`. This archives the runtime, replaces the CA on the next start, and requires fresh owner and workload enrollment.

The FIPS build claim is limited to linux/amd64. Approved mode is enabled by the build, while strict runtime enforcement is separate and requires `GODEBUG=fips140=only`. `make fips-build` installs the binary as `./ztd-fips`; check it with `GODEBUG=fips140=only ./ztd-fips version --fips`.

### Cross-Compilation

```bash
make build-target GOOS=linux GOARCH=amd64
make build-target GOOS=darwin GOARCH=arm64
make build-target GOOS=windows GOARCH=amd64
```

### Run the Gateway

Start the Gateway as a managed background process. Doctrine is the default posture:

```bash
./ztd gw start
```

Use `--follow` to run in the foreground:

```bash
./ztd gw start --follow
```

On first start, the Gateway creates the `.ztd/` runtime tree and PKI hierarchy. The Gateway defaults to plain HTTP on port 8080 and HTTPS on port 8443; use `--http-port` and `--https-port` flags to override defaults. Both listeners bind `127.0.0.1` unless `--listen-host` names another address; pass `--listen-host 0.0.0.0` when Operators or workstations on other hosts must reach the Gateway. Confirm endpoints, then enroll the first owner:

```bash
./ztd gw status
./ztd login web
```

`./ztd login web` signs in the owner with a browser passkey, installs the Gateway root CA into the OS trust store, and enables the Console. Use `./ztd login` for a headless host: it creates an mTLS-only CLI identity.

Choose a stricter posture only after configuring required proofs:

```bash
./ztd gw start --posture consensus
./ztd gw start --posture ratify
./ztd gw start --posture notary
```

Consensus and notary require a bound consensus application. Enroll it as an ordinary application with an L2 signing identity, then bind its HTTPS endpoint:

```bash
./ztd auth enroll app <app-name> --signing-key
./ztd auth enroll consensus bind <app-name> https://<app-host>/<deliberate-path>
./ztd auth enroll consensus show
```

Ratify and notary require L3 human authorization for mutations, including the binding itself. See [Consensus](../architecture/consensus.md).

Posture, capabilities, ports, listen host, and the other startup settings are fixed while the Gateway runs. `./ztd gw start` against a running Gateway compares the explicitly passed startup flags with its launch profile, lists any differences, and exits nonzero without changing it; stop it with `./ztd gw stop` first.

Use `./ztd gw start --help` for current startup flags and defaults. Global `-e, --endpoint` and `-p, --port` flags select remote discovery and HTTPS endpoints for client enrollment; they do not configure Gateway listening ports.

---

## Protocol Library Dependencies

Custom Gateway implementations consume the ztd Protocol Library for protobuf schema definitions, SPIFFE workload identity helpers, and JSON protocol constants.

```bash
go get github.com/zerotrustdata/ztd@v1.0.6
```

Import protobuf types and SPIFFE helpers from `github.com/zerotrustdata/ztd/protocol`.

---

## Custom Gateway Implementation

To build a custom ztd-compatible Gateway, your implementation must satisfy the following protocol contracts.

### Required Capabilities

1. **PKI and Trust Management**: Act as the platform CA, maintaining root and intermediate CAs (Hub, Operator, Gateway Peer), CSR-based enrollment with SPIFFE URI SANs, revocation checking, and trust bundle distribution.
2. **Persistence Layer**: Maintain canonical platform state, ephemeral state, binary attachments, a deterministic state root, and replay nonces.
3. **Messaging Broker**: Provide WebSocket pub/sub fan-out on identity- and session-scoped channels. Governed mutation channels accept `GovernanceEnvelope` transactions; direct messages handle heartbeats and receipts.
4. **Admission and Enrollment**: Expose bootstrap, recovery, platform enrollment, governance, PKI management, MCP, and A2A surfaces.
5. **Protocol Translation**: Translate MCP JSON-RPC tool calls and A2A skill invocations into governed `GovernanceEnvelope` transactions.
6. **Governance Pipeline Integration**: Apply L1-L3 according to active posture, dispatch to an Operator, and record signed receipts.
7. **Audit and Receipt Access**: Expose authenticated audit event and receipt query surfaces.

### Protocol Invariants

1. **Transaction integrity**: `id == transaction_hash == SHA256(canonical_fields)`.
2. **State binding**: Each mutation carries a state Merkle root checked by L4.
3. **Replay defense**: L4 reserves nonces durably before dispatch.
4. **Expiry enforcement**: L4 rejects expired envelopes.
5. **Doctrine enforcement**: L1 validates payload against forbidden patterns in all postures.
6. **Posture-aware proofs**: L2 signatures and L3 authorization are mandatory when posture requires them.
7. **Execution boundary**: Only L5 dispatches admitted mutations with signed receipts.
8. **Fail-closed behavior**: Any malformed envelope, unknown session, stale state, or failed proof halts execution with a typed rejection.

### Two-Port Architecture

| Port | Transport | Client Cert | Purpose |
| :--- | :--- | :--- | :--- |
| **HTTP 8080** | Plain HTTP | None | Bootstrap health, CA discovery, initial owner bootstrap, token-scoped CLI recovery, enrollment, binary downloads |
| **HTTPS 8443** | TLS 1.3 | Verified when presented | Console and WebAuthn, public TLS routes, authenticated API and PKI management, pub/sub, MCP/A2A, governance, audit |

Port mixing is prohibited. The Gateway fails startup if HTTP and HTTPS surfaces are assigned to the same port.

---

## Testing

```bash
./ztd test unit         # Tier 1: unit tests (fast, no external dependencies)
./ztd test integration  # Tier 2: in-process integration tests (SQLite, PKI, pub-sub)
./ztd test e2e          # Tier 3: tests against a running platform
./ztd test coverage     # Integration tests with 75% coverage enforcement
./ztd test lint         # golangci-lint static analysis
./ztd test chaos        # Governance chaos testing
```

Run all unit and integration tests with `make test`. Run the local platform CI gate with `make ci`.

---

## Manage

### Gateway Stop

```bash
./ztd gw stop
```

### Gateway Restart

```bash
./ztd gw restart
```

Restart reads the complete validated launch profile from `.ztd/pids/operator-launch-profile.json` and restarts the Gateway with its exact persisted configuration.

### Gateway Settings

```bash
./ztd gw settings
```

### Gateway Reset

```bash
./ztd gw reset
```

Moves the `.ztd/` runtime tree aside to `.ztd-<MMDDHHMM>` and starts a fresh doctrine Gateway.

### Gateway Clean

```bash
./ztd gw clean
```

Archives `.ztd/` to `.ztd-<MMDDHHMM>` and cleans local runtime state.

### Gateway Setup

```bash
./ztd gw setup
```

Runs the interactive setup wizard to preview configuration parameters.

### Security Validation

```bash
./ztd gw security validate
```

---

## Monitor

### Gateway Status

```bash
./ztd gw status
```

### Gateway Logs

```bash
./ztd gw logs -f
```

### Data Query

```bash
./ztd gw data operators
./ztd gw data users
./ztd gw data settings
./ztd gw data store --collection <name> [--document-id <id>]
./ztd gw data audit list --operator-session-id <session-id>
./ztd gw data audit summary [--operator-session-id <session-id>]
```

### Cloudflare Tunnel

```bash
./ztd gw tunnel create --name <tunnel-name> --hostname <your-domain>
./ztd gw tunnel run --name <tunnel-name>
./ztd gw tunnel status --name <tunnel-name> --hostname <your-domain>
```

---

## Next Steps

- [Unified Stack Guide](unified_stack.md) — Operate the host-native single-host stack and evaluation campaigns.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Connect applications, SDKs, and agents.
- [Build Operator](build_operator.md) — Build a custom ztd-compatible Operator.
- [Protocol Architecture](../architecture/protocol.md) — Go module API reference and constants.

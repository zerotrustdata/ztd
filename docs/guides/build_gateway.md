---
doc_id: build-gateway
title: Build Gateway
audience: developers and operators
status: current
version: v1.5.0
last_updated: 2026-10-28
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
- [Custom Gateway Implementation](#custom-gateway-implementation) — Required capabilities, protocol invariants, two-port architecture
- [Testing](#testing) — Test suite tiers and test commands
- [Manage](#manage) — Gateway lifecycle commands (stop, restart, settings, reset, clean, setup, security validation)
- [Monitor](#monitor) — Status, logs, data queries, and Cloudflare Tunnel
- [Next Steps](#next-steps) — Related guides and references

---

## Overview

A ztd Gateway implements the platform's Policy Decision Point (PDP). It manages PKI, canonical platform state, governance admission, messaging, and MCP/A2A protocol translation. The Gateway coordinates L1 Doctrine, L2 Consensus, and L3 Notary; each executing Operator independently re-verifies those proofs at L4 Warden and applies L5 Actuator. For operations that target the Gateway host, an in-process Operator substrate applies L4 and L5.

The reference implementation is the static `ztd` binary running in Gateway mode. The same binary runs as a Governed Operator, the Policy Execution Point (PEP) that connects outbound to a Gateway and applies L4-L5 on a managed host. Custom implementations conform to the same protocol contracts and fail-closed invariants described in the [ztd Protocol specification](../../protocol/docs/spec.md).

---

## Reference Implementation

### Prerequisites

- **Go 1.27.3+** — Required for building the reference Gateway. The root `go.mod` is authoritative; the Makefile sets `GOTOOLCHAIN=auto` so the toolchain is selected from it.
- **Make** — Required to run the root build targets.
- **Node.js `^22.22.2 || ^24.15.0 || >=26.0.0` and npm** — Required to build Console and Evaluation Explorer embeds (see the `engines` field in `console/package.json`), which `make build` compiles automatically.

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

This produces the host binary as `./ztd` (`ztd.exe` on Windows) and a named platform binary with a neighboring SHA-256 checksum in `bin/`. The binary is built with `CGO_ENABLED=0` and does not require the Go toolchain or OpenSSL at runtime.

### Build Targets

The Makefile provides several build targets:

- `make build` — Builds `ztd` for the current platform.
- `make build-all` — Builds `ztd` for every target in the `ztdbinaries` catalog and writes a manifest and checksums.
- `make build-linux` — Builds Linux binaries for amd64, arm64, and 386.
- `make build-windows` — Builds Windows binaries for amd64 and arm64.
- `make build-darwin` — Builds macOS binaries for amd64 and arm64.
- `make build-compressed` — Builds the current-platform binary and compresses the named `bin/` artifact with UPX.
- `make fips-build` — Builds a FIPS 140-3 approved mode ztd binary for linux/amd64.
- `make fips-verify` — Builds the FIPS variant and runs its self-check with FIPS enforcement enabled.
- `make clean` — Removes `bin/`, `.ztd-test-tmp/`, checksum and coverage files, and the Go build cache (`go clean -cache`). It does not remove the repository-root `ztd` binary.

To intentionally reset Gateway state, use `./ztd gw clean`. This archives the runtime, removes ztd root CA anchors from the OS trust store, and invalidates CLI credentials. Re-enroll the owner with `./ztd login web` after the next start.

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

On first start, the Gateway creates the `.ztd/` runtime tree and PKI hierarchy. The Gateway defaults to plain HTTP on port 8080 and HTTPS on port 8443; use `--http-port` and `--https-port` flags to override defaults. When a requested port is busy, the Gateway tries offsets from 0 to 99 and uses the first offset where both the HTTP and HTTPS ports are free, shifting both by the same amount. If no pair is free, it keeps the requested ports and the bind fails. Both listeners bind `127.0.0.1` unless `--listen-host` names another address; pass `--listen-host 0.0.0.0` when Operators or workstations on other hosts must reach the Gateway. Confirm endpoints, then enroll the first owner:

```bash
./ztd gw status
./ztd login web
```

`./ztd login web` enrolls a CLI session, installs the Gateway root CA into the OS trust store (on macOS, your login keychain as an HTTPS trust root for this account, with no sudo; skip with `--no-system-trust`), registers or confirms a passkey, and produces an identity that can use both the CLI and the Console. Use `./ztd login` (the same as `./ztd login cli`) for a headless host: it skips the passkey and OS trust steps and creates an mTLS-only CLI identity that cannot sign in to the Console.

Choose a stricter posture only after configuring required proofs. `consensus` enforces L1 and L2, `ratify` enforces L1 and L3, and `notary` enforces L1, L2, and L3; `doctrine` audits L2 and L3:

```bash
./ztd gw start --posture consensus
./ztd gw start --posture ratify
./ztd gw start --posture notary
```

Under `consensus` and `notary`, L2 consensus is required. If the Gateway starts without a bound consensus, it logs a warning and continues; L2-gated transactions are rejected until a consensus is bound.

The Gateway-hosted Tribunal panel is one provider. `./ztd consensus enroll tribunal --model <tag>` resolves the local model tag through the frozen model registry, pins it by digest, and enrolls the five `tribunal-*` member applications. `--model` is required. Each member gets a sealed signing key unless its key already exists locally, so a re-run resumes. `--quorum` accepts 3 to 5 and defaults to 3. Then run `./ztd consensus status` to confirm the binding:

```bash
./ztd consensus enroll tribunal --model <tag>
./ztd consensus status
```

To bind an application instead, enroll it as an ordinary application with an L2 signing identity, then bind its HTTPS endpoint. Set `APP_NAME` to the application name and `APP_ENDPOINT` to the application's absolute HTTPS endpoint URL, then run:

```bash
APP_NAME=etl-service
APP_ENDPOINT=https://etl.example.com/deliberate
./ztd auth enroll app "$APP_NAME" --signing-key
./ztd consensus enroll app "$APP_NAME" "$APP_ENDPOINT"
./ztd consensus status
```

A single application needs no `--member-app`; its quorum defaults to 1. For several members, repeat `--member-app` for every member, including the primary application, and pass `--quorum`.

Under `ratify` and `notary`, binding a consensus traverses governance and requires passkey approval. A pending binding prints its approval URL and transaction; check `./ztd consensus status` after approval. See [Consensus](../architecture/consensus.md).

Posture, capabilities, ports, listen host, and the other startup settings are fixed while the Gateway runs. Running `./ztd gw start` against a Gateway that is already running compares the startup flags you pass explicitly with the running Gateway's launch profile. If they differ, it reports that the startup configuration differs and returns an error without changing the running Gateway. Run `./ztd gw stop` first to apply different settings.

Use `./ztd gw start --help` for current startup flags and defaults. Global `-e, --endpoint` and `-p, --port` flags select remote discovery and HTTPS endpoints for client enrollment; they do not configure Gateway listening ports.

---

## Protocol Library Dependencies

Custom Gateway implementations consume the ztd Protocol Library for protobuf schema definitions, SPIFFE workload identity helpers, and JSON protocol constants.

```bash
go get github.com/zerotrustdata/ztd
```

Import SPIFFE workload identity helpers and protocol constants from `github.com/zerotrustdata/ztd/protocol`. Import generated protobuf types from the package path under `github.com/zerotrustdata/ztd/protocol/proto/ztd/`, for example `protocol/proto/ztd/operator/v1`.

---

## Custom Gateway Implementation

To build a custom ztd-compatible Gateway, your implementation must satisfy the following protocol contracts.

### Required Capabilities

1. **PKI and Trust Management**: Act as the platform CA, maintaining the root CA and the `hub`, `operator`, and `gateway-peer` intermediate CAs.
2. **Persistence Layer**: Maintain canonical platform state, a deterministic state root, and durable replay nonces.
3. **Messaging Broker**: Provide pub/sub fan-out to connected Operators and clients. Governed mutations travel as `GovernanceEnvelope` transactions.
4. **Admission and Enrollment**: Expose bootstrap, recovery, platform enrollment, governance, PKI management, MCP, and A2A surfaces.
5. **Protocol Translation**: Translate MCP JSON-RPC tool calls and A2A skill invocations into governed `GovernanceEnvelope` transactions.
6. **Governance Pipeline Integration**: Apply L1-L3 according to active posture, dispatch to an Operator, and record signed receipts.
7. **Audit Access**: Expose authenticated audit event query surfaces.

### Protocol Invariants

The fail-closed rules are defined in the [ztd Protocol specification](../../protocol/docs/spec.md). In summary:

1. **State binding**: Each mutation carries a state Merkle root checked by L4.
2. **Replay defense**: L4 reserves nonces durably before dispatch.
3. **Expiry enforcement**: L4 rejects expired envelopes.
4. **Doctrine enforcement**: L1 validates payload against forbidden patterns in all postures.
5. **Posture-aware proofs**: L2 signatures and L3 authorization are mandatory when posture requires them.
6. **Execution boundary**: Only L5 dispatches admitted mutations with signed receipts.
7. **Fail-closed behavior**: Any malformed envelope, unknown session, stale state, or failed proof halts execution with a typed rejection.

### Two-Port Architecture

| Port | Transport | Client Cert | Purpose |
| :--- | :--- | :--- | :--- |
| **HTTP 8080** | Plain HTTP | None | Plain-HTTP bootstrap router for CA discovery, bootstrap, and routes that do not require mTLS; other paths redirect to HTTPS |
| **HTTPS 8443** | TLS | Required only on routes that need mTLS | Console and WebAuthn, public TLS routes, authenticated API and PKI management, pub/sub, MCP/A2A, governance, audit |

The Gateway rejects a launch profile whose HTTP and HTTPS ports are equal.

---

## Testing

```bash
./ztd test unit         # Tier 1: unit tests (fast, no external dependencies)
./ztd test integration  # Tier 2: in-process integration tests (SQLite, PKI, pub-sub)
./ztd test e2e          # Tier 3: tests against a running platform
./ztd test coverage     # Run tests with coverage profiling and enforce the 75% threshold
./ztd test lint         # golangci-lint static analysis
./ztd test chaos        # Generate governance events against the local audit stack
```

Run all unit and integration tests with `make test`. Run the local CI gate with `make ci`.

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

Restart reads the complete validated launch profile from `.ztd/pids/operator-launch-profile.json` and restarts the Gateway with its persisted configuration.

### Gateway Settings

```bash
./ztd gw settings
```

### Gateway Reset

```bash
./ztd gw reset
```

Runs the `gw clean` cleanup, which moves the `.ztd/` runtime tree aside to `.ztd-<MMDDHHMM>`, and then starts a fresh doctrine Gateway.

### Gateway Clean

```bash
./ztd gw clean
```

Stops all Gateway services, archives `.ztd/` to `.ztd-<MMDDHHMM>`, removes ztd root CA anchors from the OS trust store, and invalidates CLI credentials. Run `./ztd login web` after the next start to re-enroll the owner.

### Gateway Setup

```bash
./ztd gw setup
```

Runs the interactive setup wizard for posture, consensus, passkey, CORS, and certificate options. It prints the resolved configuration and does not start the Gateway; start it with `./ztd gw start`.

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
```

These commands query the running Gateway over mTLS. Run `./ztd gw data --help` for the complete command list and flags:

```text
./ztd gw data store --collection <name> [--document-id <id>]
./ztd gw data audit list [--operator-session-id <session-id>]
./ztd gw data audit summary [--operator-session-id <session-id>]
```

`store` lists a collection, or fetches one document when `--document-id` is given. `audit` filters by Operator session ID when `--operator-session-id` is given.

### Cloudflare Tunnel

Set `TUNNEL_NAME` to the tunnel name and `TUNNEL_HOSTNAME` to the public hostname you control, then run:

```bash
TUNNEL_NAME=ztd
TUNNEL_HOSTNAME=console.example.com
./ztd gw tunnel create --name "$TUNNEL_NAME" --hostname "$TUNNEL_HOSTNAME"
./ztd gw tunnel run --name "$TUNNEL_NAME"
./ztd gw tunnel status --name "$TUNNEL_NAME" --hostname "$TUNNEL_HOSTNAME"
```

---

## Next Steps

- [Unified Stack Guide](unified_stack.md) — Operate the host-native single-host stack and evaluation campaigns.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Connect applications, SDKs, and agents.
- [Build Operator](build_operator.md) — Build a custom ztd-compatible Operator.
- [Protocol Architecture](../architecture/protocol.md) — Go module API reference and constants.

System One decision voting is an opt-in feature for evaluation. It requires `--vote-kind decision` with an explicit `--inference-operator` on the Tribunal binding. Hosted Jev is disabled by default: it requires `--allow-hosted` on the binding and `--allow-hosted-decision` on the Operator, both off by default. See [Decision Model Votes](../architecture/consensus.md#decision-model-votes-opt-in) before considering activation.

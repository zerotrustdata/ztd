---
doc_id: build_operator
title: Build and Run an Operator
audience: developers, deployers, independent implementations
status: current
version: v1.2.0
last_updated: 2026-10-20
owners:
  - docs/guides/
  - cmd/ztd
  - internal/cli/cmd/operator
  - internal/cli/cmd/gw
related:
  - docs/guides/connect_operator_to_gateway.md
  - docs/architecture/operator.md
  - docs/architecture/protocol.md
when_to_read: Building the reference Operator binary, starting a Gateway and Operator pair, configuring enrollment, understanding the public protocol surface, or implementing an independent Operator.
do_not_use_for:
  - Enrollment and day-two operations (docs/guides/connect_operator_to_gateway.md)
  - Complete service architecture and thread model (docs/architecture/operator.md)
  - Protocol library contents and package reference (docs/architecture/protocol.md)
---

# Build and Run an Operator

## Purpose

The Governed Operator is the host-side Policy Execution Point (PEP). It opens an outbound mTLS WebSocket connection to a ztd Gateway, subscribes to its identity- and session-scoped command channel, decodes canonical protojson `GovernanceEnvelope` transactions, re-runs L1 Doctrine, verifies universal transaction checks and posture-required L2 and L3 proofs in L4 Warden, and dispatches verified actions through L5 Actuator.

The reference Gateway and Operator compile into the same static `ztd` binary. `./ztd gw start` runs the Gateway Policy Decision Point (PDP). `./ztd operator start` runs the outbound Operator. The outbound Operator does not expose an MCP or A2A listener; the Gateway translates client requests into governed transactions and publishes them to the bound Operator.

## Quick index

- [Purpose](#purpose)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

| ID | Rule |
| :--- | :--- |
| INV-BUILD-OP-01 | `make build` compiles the host platform into `bin/ztd-<os>-<arch>` and copies the binary to `./ztd`. Cross-platform builds use `make build-target GOOS=<os> GOARCH=<arch>`, `make build-linux`, `make build-darwin`, `make build-windows`, or `make build-all`. Every build embeds the evaluation explorer SPA and carries provenance stamps (version, build ID, build time, platform). |
| INV-BUILD-OP-02 | The `operator start` command accepts connection parameters (`--endpoint`, `--trust-bundle`) and capability flags (`--capabilities`). Unknown flags are rejected before startup. |
| INV-BUILD-OP-03 | Multiple Operators on the same host MUST use distinct working directories to prevent fingerprint collisions. They share a Gateway endpoint and expose no inbound ports. The Gateway differentiates instances by SHA-256 composite fingerprint. |
| INV-BUILD-OP-04 | The `.ztd/` runtime state tree contains `pki/`, `data/` (with `data/ledger/`), `vault/`, `secrets/`, `logs/`, `pids/`, and `bin/` subdirectories. It never stores the Operator's enrollment identity on disk; credentials live only in process memory. Auto-initialization occurs on first `operator start`. |
| INV-BUILD-OP-05 | L1 Doctrine re-execution, L2/L3 posture verification, replay and expiry checks, transaction hash equality, and state-root binding MUST be performed in L4 Warden before L5 Actuator execution. L5 Actuator signs the execution event receipts. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Build targets and environment | `Makefile` | `make help` |
| Operator start command and flags | `internal/cli/cmd/operator/operator.go` | `./ztd operator start --help` |
| Gateway start command and flags | `internal/cli/cmd/gw/gateway.go` | `./ztd gw start --help` |
| Operator list/show/bind/run/stop commands | `internal/cli/cmd/operator/` | `./ztd operator --help` |
| Vault administration | `internal/cli/cmd/vault/` | `./ztd vault --help` |
| Public protocol Go module | `github.com/zerotrustdata/ztd@v1.2.0` | `go get github.com/zerotrustdata/ztd@v1.2.0` |

## Procedures

### Build the Reference Operator

#### Prerequisites

- **Go 1.27.3+**, declared in root `go.mod`.
- **Make** on Linux and macOS. Windows builds use the repository's PowerShell workflow and invoke Make.
- **Node.js 22.22.2+, 24.15+, or 26+ and npm**, required only if rebuilding frontend embeds. `make build` uses committed embeds.

#### Build from Source

```bash
git clone https://github.com/zerotrustdata/ztd.git && cd ztd
make build
```

The build sets `CGO_ENABLED=0`, uses `netgo` and `osusergo` build tags, strips symbols, and embeds version metadata.

#### Build Targets

| Target | Result |
| :--- | :--- |
| `make build` | Builds for current OS/architecture into `bin/ztd-<os>-<arch>` and copies to `./ztd`. |
| `make build-target GOOS=<os> GOARCH=<arch>` | Builds one target named by `GOOS` and `GOARCH`. |
| `make build-all` | Builds Linux amd64/arm64/386, Windows amd64/arm64, Darwin amd64/arm64 binaries with SHA-256 sidecars. |
| `make build-linux` | Builds Linux binaries and checksums. |
| `make build-windows` | Builds Windows binaries and checksums. |
| `make build-darwin` | Builds macOS binaries and checksums. |
| `make fips-build` | Builds `bin/ztd-fips-linux-amd64` with `GOFIPS140=v1.0.0`. |
| `make fips-verify` | Builds the FIPS variant and runs `ztd-fips version --fips` under `GODEBUG=fips140=only`. |

### Start the Reference Operator

#### Start a Gateway

The Operator requires a reachable Gateway for enrollment, pub/sub, and event receipt publication:

```bash
./ztd gw start
```

#### Start and Enroll the Operator

On the target host, set `GATEWAY_HOST` to the Gateway's host name or address, then start the Operator with the Gateway discovery endpoint:

```bash
GATEWAY_HOST=gateway.example.com
./ztd operator start --endpoint "$GATEWAY_HOST"
```

Every start runs the owner-approved platform enrollment protocol over one bootstrap websocket to the Gateway. The Operator creates keys and certificate requests in memory, holds the socket, proves possession of both keys, and receives its certificate, trust bundle, session, posture, runtime limits, and L2 signer trust. Nothing from enrollment is written to disk.

The enrolled owner lists pending requests, sets `REQUEST_ID` to the request to approve, and approves it:

```bash
./ztd auth enroll pending
./ztd auth enroll approve "$REQUEST_ID" --yes
```

#### Operator Start Options

Run `./ztd operator start --help` for the complete flag set. The options below govern connection, identity pinning, runtime scope, and capabilities:

| Option | Behavior |
| :--- | :--- |
| `-e, --endpoint <host[:port]>` | Gateway host for bootstrap websocket and pub/sub. A port names the Gateway HTTP discovery port and must match `--gateway-http-port` when both are set; the pub/sub port comes from `--gateway-https-port`. URL schemes are rejected. Default: `localhost`. |
| `--trust-bundle <path>` | Owner-supplied CA trust bundle to pin Gateway identity. |
| `--working-dir <path>` | Sets command execution directory and `.ztd/` runtime root. Defaults to current directory. Start refuses a directory that holds a Gateway runtime or places the runtime tree inside `--model-storage-root`. |
| `--capabilities <list>` | Enables comma-separated set of `data`, `inference`, `provenance`, `observer`. |

#### Running Multiple Operators on the Same System

1. **Distinct Directories**: Launch each operator from its own directory (via `--working-dir`). Each instance maintains its own isolated `.ztd/` runtime tree.
2. **Connectivity**: Operators share the same Gateway endpoint and connect outbound. They bind no inbound ports.
3. **Capabilities**: Enable capabilities via `--capabilities`:
   - Inference Operator: `--capabilities inference --inference-ollama-endpoint <url>`
   - Provenance Operator: `--capabilities provenance --model-storage-root <path>`
   - Observer Operator: `--capabilities observer --provider-boundary-observer-id <id>`
   - Data Operator: `--capabilities data`
4. **Composite Fingerprint**: The Operator automatically computes a SHA-256 composite `system_fingerprint` incorporating system properties, local directory, user account, port, and capabilities to avoid collisions on the Gateway.

### Operate Remote Operators from the CLI

#### Discover operators

List Operators, then set `OPERATOR_ID` to the ID or session ID of the Operator to inspect:

```bash
./ztd operator list
./ztd operator show "$OPERATOR_ID"
```

#### Bind the CLI session to operators

Set `OPERATOR_SESSION_ID` to the Operator session to bind. Pass additional session IDs after the first to bind several at once (`./ztd operator bind <operator-session-id> [<operator-session-id>...]`):

```bash
./ztd operator bind "$OPERATOR_SESSION_ID"
./ztd operator bind list
./ztd operator bind unbind
```

#### Run a governed shell command on operators

With `OPERATOR_SESSION_ID` set as above, run on one Operator, or on every active Operator:

```bash
./ztd operator run "$OPERATOR_SESSION_ID" --cmd 'echo hello from $(hostname)'
./ztd operator run --all-active --concurrency 64 --cmd 'uname -a' --json
```

#### Stop or revoke an Operator

Set `OPERATOR_SESSION_ID` to the Operator session to stop and `REQUEST_ID` to the enrollment request to revoke:

```bash
./ztd operator stop "$OPERATOR_SESSION_ID" --reason "maintenance"
./ztd auth enroll revoke "$REQUEST_ID" --reason "decommissioned" --yes
```

### Deployment Commands

`ztd operator cp <target>` copies the running binary to a local path. `ztd operator scp <user@host:path>` copies via SCP.

`ztd operator deploy --local --count N --capabilities data --background --approve --trust-bundle <gateway-ca.pem> --endpoint <gateway-host>` deploys isolated Operators on this system under `.ztd/operators/` (pass `--dest-dir <dir>` to place them elsewhere). Replace `--local` with `--hosts <host[,host...]>` for SSH deployment.

## Anti-patterns

- Using `./ztd gw clean` on a host where `.ztd/` runtime state must be preserved (INV-BUILD-OP-04). The command renames `.ztd` to `.ztd-<MMDDHHMM>` and removes Gateway state.
- Assuming protocol package schemas alone provide an Operator implementation without the required L1-L5 governance engine (INV-BUILD-OP-05).
- Running multiple Operators in the same working directory on one host.

## Links out

- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Enrollment, connection, health checks, and fleet operations.
- [Operator Architecture](../architecture/operator.md) — Service stack, execution boundary, audit stores, and tool handlers.
- [Protocol Architecture](../architecture/protocol.md) — Protocol schemas, models, and constants.
- [Build Applications](build_apps.md) — Connect applications to the Gateway.

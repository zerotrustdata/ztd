---
doc_id: getting_started
title: Getting Started
audience: new users and platform evaluators
status: current
version: v1.0.0
last_updated: 2026-10-12
owners:
  - docs/guides/getting_started.md
  - Makefile
related:
  - docs/guides/unified_stack.md
  - docs/guides/connect_operator_to_gateway.md
  - docs/architecture/console.md
  - docs/architecture/auth.md
when_to_read: Setting up ztd for the first time, bootstrapping a demo environment, or understanding the platform's core execution model.
do_not_use_for:
  - Platform architecture details (docs/architecture/gateway.md)
  - Unified evaluation stack and witness Operators (docs/guides/unified_stack.md)
  - Authentication protocol details (docs/architecture/auth.md)
---

# Getting Started

## Overview

ztd is a zero-trust execution platform for agentic infrastructure. Its two core execution components are:

- **ztd Gateway**, the central Policy Decision Point (PDP): PKI authority, state store, pub/sub broker, and admission APIs.
- **ztd Operator**, the Policy Execution Point (PEP) for the runtime where that Operator process runs: an outbound-only mTLS connection to the Gateway, local audit vault, and governed tool execution.

Both roles run from the static `ztd` binary, selected by the `gw` or `operator` subcommand. The Gateway serves the browser console.

## Quick Start

ztd runs natively on the machine as a host binary. Three commands take a clean machine to a running Gateway and a first governed action:

```bash
git clone https://github.com/zerotrustdata/ztd.git && cd ztd
bash scripts/linux-setup.sh --build-only   # macOS: bash scripts/macos-setup.sh --build-only
make quickstart
```

On Windows, run `pwsh scripts/windows-setup.ps1 --build-only` and follow the native host instructions, or use WSL 2 with the Linux commands above.

### What setup does

The setup script checks Git, Make, curl, and Go (the version `go.mod` requires), offers to install anything missing (`-y` accepts every prompt), builds `./ztd`, and offers to add the checkout to your PATH. `--build-only` does not need Node.js: `make build` embeds the committed Console and Evaluation Explorer assets. Without `--build-only` the script also installs the contributor toolchain that `make ci` needs, including Node.js 22.22.2+, 24.15+, or 26+. Rerunning setup reuses everything already installed.

### What `make quickstart` does

1. Builds `./ztd` if it is absent.
2. Starts a Gateway on `https://localhost:8443` in the default `doctrine` posture with an embedded data Operator, or reuses the Gateway that is already running.
3. Signs the CLI in as the Gateway owner with `./ztd login` (headless, no browser), or reuses the existing CLI identity.
4. Runs `./ztd operator run --all-active --cmd 'echo hello from ztd'`: the Gateway admits the command, the Operator re-verifies and executes it, and both record signed evidence locally.

Every step reuses existing state, so `make quickstart` is idempotent and safe to rerun.

| Goal | Command |
| :--- | :--- |
| Watch governance live in the terminal | `./ztd tui` |
| Use the browser console | `./ztd login web`, then open `https://localhost:8443/console/` |
| Inspect the evidence for what ran | `./ztd gw data audit list --operator-session-id <id>` |
| Govern an AI agent | `./ztd mcp agent run claude` |
| Stop the platform (state in `.ztd/` is kept) | `make down` |

The headless identity drives the CLI and the TUI. The browser console uses passkey authentication, which `./ztd login web` registers; it also installs the Gateway root CA in the OS trust store.

**Linux without a desktop keyring:** The Gateway seals its vault key with the OS keystore. On SSH sessions, servers, WSL, and containers without a Secret Service keyring, start an ssh-agent first (`eval "$(ssh-agent)" && ssh-add`) or pass a provisioned key file with `make quickstart QUICKSTART_ARGS='--master-key-file /abs/path/master.key'`. Quickstart passes the key file to every `./ztd` call it makes, since the same key seals the CLI identity; pass `--master-key-file` to your own `./ztd` commands too. Quickstart displays this remedy when the Gateway cannot reach a key store. See [Build Gateway](build_gateway.md) for key file provisioning.

### Step by step

`make quickstart` composes the public CLI commands:

```bash
make build
./ztd gw start --capabilities data
./ztd login
./ztd operator run --all-active --cmd 'echo hello from ztd'
```

`make up` builds and starts the Gateway alone. `make gw` starts it with every embedded Operator capability.

### Gateway and Operators track

To run local Operators alongside the Gateway, start the Gateway first, then start each Operator in the foreground with its own working directory:

```bash
make up
make operator OPERATOR_ARGS='--endpoint localhost --capabilities inference --working-dir ~/.ztd-inference'
```

### Approve workload enrollments

List pending enrollment requests and approve them:

```bash
./ztd auth enroll pending
./ztd auth enroll approve <request-id> --yes
```

Or approve all pending requests simultaneously:

```bash
./ztd auth enroll approve --all --yes
```

### Verify a governed action and local evidence

On Linux, macOS, or another Unix-like host, run the provider-free release smoke test from a built checkout:

```bash
smoke_parent="$(mktemp -d)"
./ztd test release-smoke --root "$smoke_parent/run" --binary "$PWD/ztd"
```

The command starts and owns an isolated Gateway and data Operator, submits a governed file edit through the Gateway command API, creates and merges a document through the embedded Operator path, verifies exact read-back and signed receipts, exports both runtimes' reports, and stops only its own processes. It requires no model service, provider key, or existing platform runtime. Inspect the `evidence/` and `reports/` outputs under `$smoke_parent/run`, then remove the temporary directory when finished.

### Verify the stack is healthy

```bash
make status
./ztd operator list
./ztd tui
```

### Stop the platform

```bash
make down
```

This stops the local Operators and then the Gateway. Runtime state in `.ztd/` is preserved.

---

## Prerequisites

ztd runs natively on the host: compile the `ztd` binary and run it directly.

| Requirement | Version | Notes |
| :--- | :--- | :--- |
| Go | 1.27.2 | Required to build from source |
| Make | Any recent | Required to run Makefile targets |
| Git | Any recent | Required to clone the repository |
| curl | Any recent | Used by the setup scripts to download Go |
| Node.js and npm | 22+ | Contributor toolchain and frontend development only; `make build` and `--build-only` setup use committed Console and Evaluation Explorer embeds |

---

## Build

### Build locally (native host)

```bash
make build
```

This builds the `ztd` binary for the current platform and places it at:
- Repository root: `./ztd`
- Platform-specific: `bin/ztd-<os>-<arch>`

The binary is statically linked with zero runtime dependencies.

Additional build targets:

| Target | Description |
| :--- | :--- |
| `make build` | Build for current OS/architecture |
| `make build-all` | Build for all platforms (linux, windows, darwin) |
| `make build-linux` | Linux: amd64, arm64, 386 |
| `make build-darwin` | macOS: amd64, arm64 |
| `make build-windows` | Windows: amd64, arm64 |

For cross-compilation:

```bash
GOOS=linux GOARCH=amd64 make build
GOOS=darwin GOARCH=arm64 make build
GOOS=windows GOARCH=amd64 make build
```

ztd is built and distributed natively; container execution is not maintained or supported as a distribution image.

Or download the binary from a running Gateway's bootstrap endpoint:

```bash
curl -fSLO http://localhost:8080/.well-known/ztd/bin/ztd-linux-amd64
chmod +x ztd-linux-amd64
```

The bootstrap endpoint serves the binary without a signature bundle on trusted networks. Verify a release binary from the release set for production use.

### Verify a release binary

Each published release binary ships with two companion files: `<binary>.sha256` (checksum) and `<binary>.sigstore.json` (Sigstore bundle with the signature and signing certificate). Download the binary and its `.sha256` and `.sigstore.json` files into an empty directory from the GitHub release for tag `v1.0.0`, then run both checks there.

Run the checksum check:

```bash
# Linux
sha256sum -c ztd-linux-amd64.sha256

# macOS
shasum -a 256 -c ztd-darwin-arm64.sha256
```

```powershell
# Windows (PowerShell)
$expected = (Get-Content .\ztd-windows-amd64.exe.sha256).Split(' ')[0]
$actual = (Get-FileHash -Algorithm SHA256 .\ztd-windows-amd64.exe).Hash
if ($expected -ne $actual) { throw 'checksum mismatch: do not run this binary' }
```

Then verify the signer identity with `cosign` 3.0 or later:

```bash
cosign verify-blob \
  --bundle ztd-linux-amd64.sigstore.json \
  --certificate-identity 'https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/v1.0.0' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ztd-linux-amd64
```

A successful run prints `Verified OK`. Do not run a binary when either check fails. The signing identity and release workflow are owned by [Release Process](../devs/release_process.md#release-signing).

---

## Run the Gateway

### Run natively on localhost

**Start the Gateway only:**

```bash
make up
```

**Start the Gateway with its embedded Operator carrying every capability:**

```bash
make gw
```

`make gw` starts the Gateway in a single process and passes `GW_CAPABILITIES` (default `data,inference,observer`) to `--capabilities` on its embedded Operator. The embedded Operator always includes Data. Setting `MODEL_STORAGE_ROOT` to the local model store adds the Provenance capability and passes the path as `--model-storage-root`:

```bash
make gw MODEL_STORAGE_ROOT=/srv/ollama/models GW_ARGS='--inference-ollama-endpoint http://<ollama-host>:11434'
```

Capabilities are fixed when the Gateway starts. Run `make down` before `make gw` to change them. Startup ends with the same output as `gw status`:

```text
Gateway  online (PID 29424)

Operators
  OPERATOR ID        TYPE      STATUS  HOST   HTTP PORT  CAPABILITIES                        DIRECTORY
  embedded-operator  embedded  active  local  8080       data,inference,provenance,observer  /home/you/ztd

Operator flags
  OPERATOR ID        CAPABILITY  FLAG                             VALUE
  embedded-operator  inference   --inference-ollama-endpoint      http://192.168.1.2:11434
  embedded-operator  inference   --inference-keep-alive           (default)
  embedded-operator  provenance  --model-storage-root             /mnt/d/ai/Ollama/models
  embedded-operator  provenance  --provenance-operator-id         (default)
  embedded-operator  observer    --provider-boundary-observer-id  (default)

Enrollments  users 1 | pending 0 | apps 0 | dashboards 0

Console  https://localhost:8443/console/
```

### Gateway configuration

The Gateway starts in Doctrine posture by default (L1 enforced, L2/L3 audited). To specify a different security posture:

```bash
./ztd gw start --posture doctrine    # default: L1 enforced, L2/L3 audited
./ztd gw start --posture consensus   # L1/L2 enforced, L3 audited
./ztd gw start --posture ratify      # L1/L3 enforced, L2 audited
./ztd gw start --posture notary      # L1/L2/L3 strictly enforced
```

The default plain-HTTP port is `8080` (bootstrap and PKI discovery). The default HTTPS port is `8443` (authenticated APIs, MCP, and Web Console).

### Monitor the Gateway

Check health and status:

```bash
./ztd gw status
```

View logs in real time:

```bash
./ztd gw logs -f
```

### Runtime state

All runtime state is written to `.ztd/` in the working directory:

| Path | Contents |
| :--- | :--- |
| `.ztd/pki/` | CA hierarchy and trust bundles |
| `.ztd/secrets/` | Bootstrap secrets and vault key |
| `.ztd/data/` | SQLite databases and blobs |
| `.ztd/vault/` | Encrypted audit vault |
| `.ztd/logs/` | Component logs (identifiers and metadata only, never command lines, output, or paths) |

### Stop the Gateway

```bash
make down
```

---

## Use the Protocol Library (Go Module)

To import the ztd wire protocol, constants, or protobuf definitions into custom clients or services:

```bash
go get github.com/zerotrustdata/ztd@v1.0.0
```

Import in Go code:

```go
import (
    "github.com/zerotrustdata/ztd/protocol"
    commonv1 "github.com/zerotrustdata/ztd/protocol/proto/ztd/common/v1"
)
```

---

## Authenticate the CLI

After the Gateway is running, sign in to bootstrap the PKI hierarchy and issue mTLS credentials:

```bash
./ztd login        # headless CLI identity (no browser, no OS trust changes)
./ztd login web    # browser passkey; also installs Gateway root CA and enables Console
```

The first sign-in on a fresh Gateway claims ownership and claims the embedded Operator. `./ztd logout` ends active sessions.

## Connect an Operator

### Start a remote operator

To connect an Operator on a remote host to the Gateway:

```bash
./ztd operator start -e <gateway-hostname>
```

The Operator initiates platform enrollment automatically. The Gateway holds the request in `pending_approval` until an enrolled owner approves it with `./ztd auth enroll approve <request-id> --yes`.

---

## MCP Agent Integration

ztd integrates with AI agent binaries to provide governed MCP tool access.

```bash
./ztd mcp agent list
./ztd mcp agent show claude
./ztd mcp agent run claude
./ztd mcp agent run goose
```

---

## Post-Bootstrap Actions

```bash
./ztd gw status           # Gateway health, Operators, and enrollment counts
./ztd gw data operators   # List enrolled operators
./ztd gw data users       # List users
./ztd gw data audit list --operator-session-id <id>  # Audit events for one Operator session
./ztd --help              # Full command reference
```

---

## Governance Postures

| Posture | L1 Doctrine | L2 Consensus | L3 Notary | Flag |
| :--- | :--- | :--- | :--- | :--- |
| Doctrine (default) | Enforced | Audited | Audited | `--posture doctrine` |
| Consensus | Enforced | Enforced | Audited | `--posture consensus` |
| Ratify | Enforced | Audited | Enforced | `--posture ratify` |
| Notary | Enforced | Enforced | Enforced | `--posture notary` |

Universal checks (transaction hash integrity, nonce reservation, expiry, state-root Merkle verification, and payload decoding) fail closed across all postures. Admitted actions continue through L4 Warden verification and L5 Actuator dispatch. See [Gateway Architecture](../architecture/gateway.md) for details.

---

## Next Steps

- [Unified Stack Guide](unified_stack.md) — Operate the host-native single-host stack and evaluation campaigns.
- [Build Gateway](build_gateway.md) — Gateway build reference, configuration, and custom implementations.
- [Build Operator](build_operator.md) — Build and deploy custom Operator workers.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Enrollment, mTLS configuration, and multi-operator deployment.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Integrate application-layer adapters and SDKs.
- [Gateway Architecture](../architecture/gateway.md) — Platform architecture and verification sequence.
- [MCP Specification](../../protocol/docs/mcp.md) — Model Context Protocol wire contract.
- [Protocol Architecture](../architecture/protocol.md) — Protocol schemas, constants, and envelope serialization.

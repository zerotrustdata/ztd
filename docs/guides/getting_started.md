---
doc_id: getting_started
title: Getting Started
audience: new users and platform evaluators
status: current
version: v1.5.0
last_updated: 2026-10-28
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

ztd runs natively on the machine as a host binary. On macOS, read the installer, then run it. It never calls sudo.

```bash
curl -fsSL https://zerotrustdata.io/start.sh -o /tmp/ztd-start.sh
sh /tmp/ztd-start.sh
```

The same script is the Linux path. `curl -fsSL https://zerotrustdata.io/start.sh | sh` is the one-liner after you have read it.

On Windows, in PowerShell:

```powershell
irm https://zerotrustdata.io/start.ps1 | iex
```

`start.sh` installs the release binary into `~/ztd` on Linux, and into `~/Library/Application Support/ztd` on macOS, after the SHA-256 checksum matches, then runs the same four steps as [`make quickstart`](#what-make-quickstart-does) from that directory. When `cosign` is on `PATH` it also verifies the Sigstore bundle for that release tag. `ZTD_REQUIRE_SIGSTORE=1` refuses a checksum-only install. `start.ps1` does the same with `ztd.ps1`, installing `ztd.exe` into `~\ztd`; run the follow-up commands as `.\ztd.exe`. Neither uses sudo or Administrator, every step reuses existing state, and it shows the remedy when a step fails. Run the follow-up commands below from the install directory. Both read these environment variables:

| Variable | Effect |
| :--- | :--- |
| `ZTD_DIR` | Install and runtime directory. Default `~/Library/Application Support/ztd` on macOS and `~/ztd` on Linux. When unset, `start.sh` refuses a directory that already has files and is not an existing ztd install. Finder metadata (`.DS_Store`) does not count. Runtime state lives in `$ZTD_DIR/.ztd/`. In PowerShell, set variables with `$env:ZTD_DIR='...'` before the command. |
| `ZTD_VERSION` | Install this `X.Y.Z` release instead of the latest. |
| `ZTD_OVERWRITE=1` | Replace an existing `$ZTD_DIR/ztd` (upgrade). A running Gateway keeps its version until `./ztd gw restart`. |
| `ZTD_REQUIRE_SIGSTORE=1` | Refuse the install unless `cosign` verifies the Sigstore bundle. On macOS, `brew install cosign`. |

To install only the binary into the current directory, use `curl -fsSL https://zerotrustdata.io/ztd.sh | sh` (or `irm https://zerotrustdata.io/ztd.ps1 | iex` in Windows PowerShell). Binaries, checksums, Sigstore bundles, and release notes for every version are at [zerotrustdata.io/releases](https://zerotrustdata.io/releases/).

To build from source instead, clone the repository, run the platform setup with `--build-only`, then `make quickstart`:

On macOS:

```bash
git clone https://github.com/zerotrustdata/ztd.git && cd ztd
bash scripts/macos-setup.sh --build-only
make quickstart
```

On Linux:

```bash
git clone https://github.com/zerotrustdata/ztd.git && cd ztd
bash scripts/linux-setup.sh --build-only
make quickstart
```

On Windows, the same flow runs natively from PowerShell 7:

```powershell
git clone https://github.com/zerotrustdata/ztd.git; cd ztd
pwsh scripts/windows-setup.ps1 --build-only
make quickstart
```

Windows needs PowerShell 7, Git for Windows (its bash runs every Makefile recipe), GNU Make (`winget install ezwinports.make` or `choco install make`), and Go 1.27.3. Run `make` targets from PowerShell; WSL is not required. The setup script stops if a `GOROOT` environment variable points at a different Go install than the active toolchain, because builds would then use the wrong standard library.

### What setup does

`--build-only` checks Git, Make, curl, and Go (the version `go.mod` requires), then runs `make build-go`. That compiles `./ztd` from the committed console and evaluation explorer embeds and does not install Node.js. The full setup (no `--build-only`) also checks Node.js 22.22.2+, 24.15+, or 26+, and npm, because `make build` refreshes those embeds, and it installs the contributor toolchain that `make ci` needs. Either mode offers to install anything missing (`-y` accepts every prompt), creates `.env` from `.env.example` with mode `0600` (an existing `.env` is kept), and offers to add the checkout to your PATH. Rerunning setup reuses everything already installed.

### What `make quickstart` does

1. Builds `./ztd` with `make build-go` if it is absent. That uses the committed frontend embeds and does not need Node.js.
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
| Stop the platform (state in `.ztd/` is kept) | `make down` (`./ztd gw stop` from the install directory) |

The headless identity drives the CLI and the TUI. The browser console uses passkey authentication, which `./ztd login web` registers; it also installs the Gateway root CA in the OS trust store.

**Windows and macOS:** The Gateway seals its vault key with DPAPI on Windows and the Keychain on macOS automatically; no ssh-agent is needed.

**macOS first run, what to expect:** (1) The first Gateway start (and the first run after upgrading or rebuilding `ztd`) shows a Keychain prompt for the `ztd-platform` item. That dialog wants your **Mac login password**, not a ztd account. The item is the vault master key and it stays in the login keychain. Choose **Always Allow**. It needs a GUI session, so run the first start from a GUI terminal rather than SSH. `start.sh` and `make quickstart` print this before the Gateway start, so the dialog is not a silent hang. (2) `./ztd login web` prints the Gateway CA fingerprint and asks before it installs that CA into your **login keychain** as an HTTPS trust root, for your account only. It is not trusted for code signing. Other accounts on the Mac are unchanged, and this step does not use sudo. macOS may show its own confirmation. The CA private key stays in the local vault. Decline the prompt, or pass `--no-system-trust`, to skip it. A CA left in the System keychain by an older install is listed on its own; removing that one uses sudo, affects every account, and runs only after you agree. If trust installation fails, the error includes the `security` command output. A failed install (a cancelled confirmation, or an SSH session with no GUI to show it) leaves no copy of the CA in the keychain, so rerunning `./ztd login web` from a GUI terminal retries it. (3) The installer clears Gatekeeper quarantine (`com.apple.quarantine`) only after the checksum matches. A binary you downloaded in a browser yourself is still quarantined until you verify it and run `xattr -d com.apple.quarantine ./ztd` (or right-click → Open).

**Linux without a desktop keyring:** The Gateway seals its vault key with an ssh-agent key when no Secret Service keyring is available. On a fresh SSH, server, WSL, or container installation, start an ssh-agent with a passphrase-protected Ed25519 or RSA key loaded before running `make quickstart` or `start.sh`: `eval "$(ssh-agent -s)" && ssh-add`. Generate a passphrase-protected key with `ssh-keygen -t ed25519` first only if this is a fresh installation with no key. Run follow-up `./ztd` commands from the same shell, because the same agent key seals the CLI identity. For an existing vault, `gw start` and `gw restart` identify the original sealing key and print exact copy-and-paste recovery commands; in a terminal they can ask to load a matching local key into the current agent for eight hours, with OpenSSH asking for that key's passphrase. A new key cannot open an existing seal. Keep the runtime and original key. See [Key Recovery](../devs/troubleshooting.md#vault-validation-and-key-recovery) and [Encryption Architecture](../architecture/encryption.md#master-key-and-keyring-providers).

### Configure `.env`

Every `./ztd` command, including the `make` targets, loads `.env` from the working directory (the repo root) at startup. Setup creates it from `.env.example` with every entry commented, so the default flow (`make quickstart`, `make up`, `make gw`) needs no edits. Uncomment and set an entry when you need it:

| Key | Set it when |
| :--- | :--- |
| `ZTD_OLLAMA_ENDPOINT` | The Ollama provider is not on this host (default `http://127.0.0.1:11434`). It is the default for `--inference-ollama-endpoint` on `./ztd operator start` and `./ztd operator model` commands. |
| `CLOUDFLARE_API_TOKEN` | You route a tunnel hostname with `./ztd gw tunnel route-dns` ([Cloudflare Tunnel](cloudflare_tunnel.md)). |

```bash
$EDITOR .env
make gw
```

`.env` must stay owner-only (`chmod 600 .env`). On Linux and macOS, `./ztd` refuses to run a command unless `.env` is a regular file (not a symlink) owned by you or root with mode `0600` or `0400`. On every platform it refuses a line that is not `KEY=VALUE` and any key outside the secret and user_endpoint categories of `protocol/constants/env_vars.json`. A variable exported in your shell and an explicit flag both take precedence over `.env`. Every other setting is a flag: run `./ztd <command> --help`.

### Step by step

`make quickstart` composes the public CLI commands:

```bash
make build
./ztd gw start --capabilities data
./ztd login
./ztd operator run --all-active --cmd 'echo hello from ztd'
```

`make up` builds and starts the Gateway alone. `make gw` starts it with every embedded Operator capability. Both listen on loopback (`127.0.0.1`) only, so just this host can reach them. Use `make up-public` or `make gw-public` when Operators on other hosts must enroll; see [Local or LAN-reachable Gateway](#local-or-lan-reachable-gateway).

### Gateway and Operators track

To run local Operators alongside the Gateway, start the Gateway first, then start each Operator in the foreground with its own working directory. `./ztd operator start` refuses a working directory that holds a Gateway runtime, so pass `--working-dir` when starting from the repo root:

```bash
make up
make operator OPERATOR_ARGS='--endpoint localhost --capabilities inference --working-dir ~/.ztd-inference'
```

### Approve workload enrollments

List pending enrollment requests:

```bash
./ztd auth enroll pending
```

Approve one request with `./ztd auth enroll approve <request-id> --yes`, replacing `<request-id>` with an ID from that list.

Or review and approve pending requests sequentially:

```bash
./ztd auth enroll approve
```

### Verify a governed action and local evidence

Run the provider-free release smoke test from a built checkout:

```bash
smoke_root=".ztd/operators/release-smoke-$(date -u +%Y%m%dT%H%M%SZ)-$$"
./ztd test release-smoke --root "$smoke_root" --binary "$PWD/ztd"
```

On Windows, from PowerShell:

```powershell
$smokeRoot = ".ztd/operators/release-smoke-$([guid]::NewGuid().ToString('N'))"
./ztd test release-smoke --root $smokeRoot --binary "$PWD\ztd.exe"
```

The command starts and owns an isolated Gateway and data Operator, submits a governed file edit through the Gateway command API, creates and merges a document through the embedded Operator path, verifies exact read-back and signed receipts, exports both runtimes' reports, and stops only its own processes. It requires no model service, provider key, or existing platform runtime. Inspect the `evidence/` and `reports/` outputs under `$smoke_root`; the run artifacts remain there for review.

### Verify the stack is healthy

```bash
make status
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
| Go | 1.27.3 | Required to build from source |
| Make | Any recent | Required to run Makefile targets |
| Git | Any recent | Required to clone the repository |
| curl | Any recent | Used by the setup scripts to download Go |
| Node.js and npm | 22.22.2+, 24.15+, or 26+ | Contributor toolchain and `make build`, which refreshes the embedded frontends. `make build-go` and `--build-only` use the committed embeds and do not need Node. |

---

## Build

### Build locally (native host)

```bash
make build
```

This builds the `ztd` binary for the current platform and places it at:
- Repository root: `./ztd`
- Platform-specific: `bin/ztd-<os>-<arch>`

The binary is built with `CGO_ENABLED=0` and `-trimpath`.

Additional build targets:

| Target | Description |
| :--- | :--- |
| `make build` | Build for current OS/architecture |
| `make build-all` | Build for all platforms (linux, windows, darwin) |
| `make build-linux` | Linux: amd64, arm64, 386 |
| `make build-darwin` | macOS: amd64, arm64 |
| `make build-windows` | Windows: amd64, arm64 |

For cross-compilation (writes `bin/ztd-<os>-<arch>` only; `./ztd` stays the host binary):

```bash
make build-target GOOS=linux GOARCH=amd64
make build-target GOOS=darwin GOARCH=arm64
make build-target GOOS=windows GOARCH=amd64
```

ztd is built and distributed natively; container execution is not maintained or supported as a distribution image.

Or download the binary from a running Gateway's bootstrap endpoint. The endpoint serves only binaries listed in `bin/ztd-binaries.json`, which `make build-all` writes on the Gateway host; it returns 404 after a plain `make build`:

```bash
curl -fSLO http://localhost:8080/.well-known/ztd/bin/ztd-linux-amd64
chmod +x ztd-linux-amd64
```

The bootstrap endpoint does not verify signatures. For production use, verify a release binary from the release set as described below.

### Verify a release binary

Each published release binary ships with two companion files: `<binary>.sha256` (checksum) and `<binary>.sigstore.json` (Sigstore bundle with the signature and signing certificate). Download the binary and its `.sha256` and `.sigstore.json` files into an empty directory from the GitHub release for the tag you are installing. The examples below use `v1.5.0`; substitute that tag everywhere, including the certificate identity. A checksum match against a file from the same host is not a signature check.

Run the checksum check on Linux:

```bash
sha256sum -c ztd-linux-amd64.sha256
```

On macOS, run:

```bash
shasum -a 256 -c ztd-darwin-arm64.sha256
```

On Windows, from PowerShell:

```powershell
$expected = (Get-Content .\ztd-windows-amd64.exe.sha256).Split(' ')[0]
$actual = (Get-FileHash -Algorithm SHA256 .\ztd-windows-amd64.exe).Hash
if ($expected -ne $actual) { throw 'checksum mismatch: do not run this binary' }
```

Then verify the signer identity with `cosign`. On macOS the binary name is `ztd-darwin-arm64` or `ztd-darwin-amd64`, and the bundle name matches:

```bash
cosign verify-blob \
  --bundle ztd-linux-amd64.sigstore.json \
  --certificate-identity 'https://github.com/zerotrustdata/ztd/.github/workflows/release-binary.yml@refs/tags/v1.5.0' \
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

`make gw` starts the Gateway in a single process and passes `GW_CAPABILITIES` (default `data,inference,observer`) to `--capabilities` on its embedded Operator. The embedded Operator defaults to Data only when no capabilities are specified; the `make gw` default explicitly enables Data, Inference, and Observer. Setting `MODEL_STORAGE_ROOT` to the local model store adds the Provenance capability and passes the path as `--model-storage-root`:

```bash
make gw MODEL_STORAGE_ROOT=/srv/ollama/models
```

The Inference capability uses `ZTD_OLLAMA_ENDPOINT` from `.env` (see [Configure `.env`](#configure-env)); `GW_ARGS='--inference-ollama-endpoint http://<ollama-host>:11434'` overrides it for one run.

Capabilities are fixed when the Gateway starts. Run `make down` before `make gw` to change them; `gw start` against a running Gateway with different flags lists the differences and exits nonzero. `make gw` ends with the `gw status` output:

```text
Gateway  online (PID 29424)

Operators
  OPERATOR ID        TYPE      STATUS  HOST   HTTP PORT  CAPABILITIES                        DIRECTORY
  embedded-operator  embedded  active  local  8080       data,inference,provenance,observer  /home/you/ztd

Operator flags
  OPERATOR ID        CAPABILITY  FLAG                             VALUE
  embedded-operator  inference   --inference-ollama-endpoint      http://192.168.1.2:11434
  embedded-operator  inference   --inference-keep-alive           (default)
  embedded-operator  provenance  --model-storage-root             /srv/ollama/models
  embedded-operator  provenance  --provenance-operator-id         (default)
  embedded-operator  observer    --provider-boundary-observer-id  (default)

Enrollments  users 1 | pending 0 | apps 0 | dashboards 0
Consensus  not bound (posture doctrine does not require L2)

Console  https://localhost:8443/console/
```

### Local or LAN-reachable Gateway

The Gateway HTTP (8080) and HTTPS (8443) listeners bind `127.0.0.1` by default, so only processes on the Gateway host can reach it. Operators on other hosts cannot reach the Gateway until it listens on a reachable interface.

| Target | Listens on | Use when |
| :--- | :--- | :--- |
| `make up` | `127.0.0.1` | Gateway and every Operator run on this host |
| `make gw` | `127.0.0.1` | Single process with the embedded Operator, local use only |
| `make up-public` | `0.0.0.0` | Operators on other hosts enroll against this Gateway |
| `make gw-public` | `0.0.0.0` | Embedded Operator plus Operators on other hosts |

The `-public` targets pass `--listen-host 0.0.0.0` to `./ztd gw start` and otherwise behave exactly like `make up` and `make gw`, including `GW_ARGS`, `GW_CAPABILITIES`, and `MODEL_STORAGE_ROOT`. Remote Operators also need ports 8080 and 8443 open through any host firewall. Capabilities and listen host are fixed when the Gateway starts; run `make down` before switching between local and public.

### Gateway configuration

The Gateway starts in Doctrine posture by default (L1 enforced, L2/L3 audited). Posture is fixed at startup; pass `--posture` with one of the values below to `./ztd gw start`:

| Posture | Enforced | Audited only |
| --- | --- | --- |
| `doctrine` (default) | L1 | L2, L3 |
| `consensus` | L1, L2 | L3 |
| `ratify` | L1, L3 | L2 |
| `notary` | L1, L2, L3 | — |

For example, to require human authorization:

```bash
./ztd gw start --posture ratify
```

Under `consensus` and `notary`, L2 also needs a bound consensus before the Gateway is ready. See [Governance Postures](#governance-postures) for the binding and vote rules.

The default plain-HTTP port is `8080` and the default HTTPS port is `8443`; both defaults are defined in [`internal/constants/ports.go`](../../internal/constants/ports.go).

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

To import the ztd wire protocol, constants, or protobuf definitions into custom clients or services, add the module from inside your own Go module (`go mod init` first if you have none):

```bash
go get github.com/zerotrustdata/ztd@v1.3.0
```

Import in Go code, then run `go mod tidy` to record the protobuf runtime dependencies:

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
./ztd login web    # browser passkey; also installs the Gateway root CA in the OS trust store
```

The headless identity cannot authenticate to the Console; `./ztd login web` registers a passkey for it. `./ztd logout` ends the user's sessions on every machine; see `./ztd logout --help`.

## Connect an Operator

### Start a remote operator

To connect an Operator on a remote host to the Gateway, start the Gateway with `make up-public` or `make gw-public` (a Gateway started with `make up` or `make gw` listens only on `127.0.0.1`), then run `./ztd operator start -e <gateway-hostname>` on the remote host, replacing `<gateway-hostname>` with the Gateway's hostname.

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

`ztd mcp agent run goose` launches an interactive Goose session. A one-shot task is forwarded with `-t` (or `-i` / `--recipe`); the launcher starts `goose run` for those flags because `goose session` does not accept them. Goose can use a local Ollama model, with no provider account:

```bash
./ztd mcp agent run goose -- --provider ollama --model qwen2.5:0.5b -t "Call run_shell_command: command echo, args [\"hello\"]"
```

Ollama listens on `http://127.0.0.1:11434` unless `ZTD_OLLAMA_ENDPOINT` or `--provider` says otherwise. The first launch enrolls the `goose` application. Under `doctrine` and `consensus` the signed-in owner CLI approves that enrollment in-line. Under `ratify` and `notary` the launcher prints a passkey approval URL and waits.

`make test-agent-smoke` is the same path as a non-interactive check: it builds `./ztd`, pulls `qwen2.5:0.5b` (override with `ZTD_SMOKE_MODEL`), runs one allowed `echo` and one L1-denied `rm -rf /`, and checks `ztd audit verify` plus `ztd report verify`. See [Automation Scripts](../architecture/scripts.md#smoke-and-onboarding-tests).

---

## Post-Bootstrap Actions

```bash
./ztd gw status           # Gateway health, Operators, and enrollment counts
./ztd gw data operators   # List enrolled operators
./ztd gw data users       # List users
./ztd --help              # Full command reference
```

List the audit events for one Operator session with `./ztd gw data audit list --operator-session-id <id>`, replacing `<id>` with the session ID.

---

## Governance Postures

| Posture | L1 Doctrine | L2 Consensus | L3 Notary | Flag |
| :--- | :--- | :--- | :--- | :--- |
| Doctrine (default) | Enforced | Audited | Audited | `--posture doctrine` |
| Consensus | Enforced | Enforced | Audited | `--posture consensus` |
| Ratify | Enforced | Audited | Enforced | `--posture ratify` |
| Notary | Enforced | Enforced | Enforced | `--posture notary` |

Under `consensus` and `notary`, L2 requires a bound consensus. Until one is bound, `gw status` reports `NOT BOUND` and `/health` reports `governance_ready: false`, and eval campaigns refuse to start. Bind it with `./ztd consensus enroll tribunal --model <tag>` as described in [Build a Gateway](build_gateway.md). Under an L2 posture, L2 votes only on state-changing actions; inference, observation, and read-only targets get no vote round.

Universal checks (transaction hash integrity, nonce reservation, expiry, state-root Merkle verification, and payload decoding) fail closed across all postures. Admitted actions continue through L4 Warden verification and L5 Actuator dispatch. See [Gateway Architecture](../architecture/gateway.md) for details.

---

## Next Steps

- [Unified Stack Guide](unified_stack.md) — Operate the host-native single-host stack and evaluation campaigns.
- [Build Gateway](build_gateway.md) — Gateway build reference, configuration, and custom implementations.
- [Build Operator](build_operator.md) — Build and deploy custom Operators.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Enrollment, mTLS configuration, and multi-operator deployment.
- [Connect Applications to Gateway](connect_apps_to_gateway.md) — Integrate application-layer adapters and SDKs.
- [Gateway Architecture](../architecture/gateway.md) — Platform architecture and verification sequence.
- [MCP Specification](../../protocol/docs/mcp.md) — Model Context Protocol wire contract.
- [Protocol Architecture](../architecture/protocol.md) — Protocol schemas, constants, and envelope serialization.

---
doc_id: unified_stack
title: Unified Stack Guide
audience: platform operators and evaluators
status: current
version: v1.0.6
last_updated: 2026-10-13
owners:
  - Makefile
  - docs/guides/
related:
  - docs/guides/getting_started.md
  - docs/guides/build_operator.md
  - docs/guides/connect_operator_to_gateway.md
  - docs/architecture/evals.md
  - docs/architecture/model-provenance.md
  - docs/architecture/console.md
  - docs/architecture/auth.md
when_to_read: Running the ztd platform end-to-end on one host, bootstrapping evaluation campaigns with witness Operators, or troubleshooting stack health.
do_not_use_for:
  - CLI flag inventory (use ./ztd <command> --help)
  - Evaluation execution details (docs/architecture/evals.md)
  - Authentication protocol details (docs/architecture/auth.md)
---

# Unified Stack Guide

Describes how to run the ztd platform on one host as processes of the `ztd` binary: the Gateway, a Data Operator, an Inference Operator, and witness Operators (Observer and Provenance) that enroll from the provider host. The Gateway serves the browser console. Also documents the evaluation campaign topology used for governed model scoring and the remote Ollama provider boundary.

Run all commands from the repository root unless noted otherwise.

## Purpose

This guide covers the unified evaluation stack on one host: how to start it, enroll Operator sessions, run campaigns, and observe execution. It assumes familiarity with the ztd platform's governance model. For a quick start, see [Getting Started](getting_started.md). For the flag inventory, run `./ztd gw start --help` and `./ztd operator start --help`.

## Quick index

- [Purpose](#purpose)
- [Quick index](#quick-index)
- [Invariants](#invariants)
- [Owned surfaces](#owned-surfaces)
- [Procedures](#procedures)
- [Anti-patterns](#anti-patterns)
- [Links out](#links-out)

## Invariants

| ID | Rule |
| :--- | :--- |
| INV-STACK-01 | The host stack runs as processes of the `ztd` binary on the Gateway host: `make up` or `./ztd gw start` for the Gateway, and `make operator` or `./ztd operator start` for each outbound Operator. Each Operator MUST run in its own working directory; `./ztd operator start` refuses a working directory that holds a Gateway runtime. |
| INV-STACK-02 | Operator health is determined by the Gateway registry (`./ztd operator list`), not by process existence. An Operator that has not enrolled or whose heartbeat is `stale` is not ready. |
| INV-STACK-03 | The Inference Operator MUST enroll with `--capabilities inference` before any scored campaign dispatch reaches the provider. |
| INV-STACK-04 | Observer and Provenance Operators enroll as separate sessions on the provider host and MUST NOT share the same session. |
| INV-STACK-05 | Campaign identity (campaign ID and registry digest) travels on each governed dispatch from the campaign controller. The Inference Operator has no startup campaign binding. Console chat and campaigns share it concurrently without switching. |

## Owned surfaces

| Claim | Path | Verify |
| :--- | :--- | :--- |
| Host lifecycle targets (`up`, `gw`, `operator`, `status`, `down`) | `Makefile` | `make help` |
| Gateway start flags, including embedded Operator capabilities and public spectator listeners | `internal/cli/cmd/gw` | `./ztd gw start --help` |
| Operator start flags, including inference, provenance, and observer settings | `internal/cli/cmd/operator` | `./ztd operator start --help` |
| Campaign, run, and rollout commands | `internal/cli/cmd/eval` | `./ztd eval --help` |
| Gateway runtime state | `.ztd/` | `./ztd gw status` |

## Procedures

### Prerequisites

- A built `./ztd` binary (`make build`). See [Getting Started](getting_started.md).
- An approved Ollama provider reachable from the Inference Operator host, for example: `curl -fsS http://192.168.1.2:11434/api/version`. The Inference Operator default endpoint is `http://127.0.0.1:11434`; set `ZTD_OLLAMA_ENDPOINT` in `.env` (or `--inference-ollama-endpoint`) when the approved provider is remote.
- Host ports available: **8080** (HTTP discovery) and **8443** (HTTPS/mTLS API and console). The public spectator and evaluation explorer listen on loopback **8081**, **8082**, and **5173** by default.
- A browser with WebAuthn support for the owner passkey, or headless enrollment.

`./ztd` loads `.env` from the working directory (the repo root; template: `.env.example`, mode `0600`) at startup; it accepts only the secret and user_endpoint keys in `protocol/constants/env_vars.json`. Pass every other start flag through the `GW_ARGS` and `OPERATOR_ARGS` make variables, or directly to `./ztd`.

Use `ZTD_ARGS` for global CLI flags shared by `make up`, `make gw`, `make operator`, `make status`, and `make down`. When using a provisioned master key file, pass it to every lifecycle invocation so status and governed shutdown can unseal the CLI identity with the same key:

```bash
make gw ZTD_ARGS='--master-key-file /absolute/path/master.key'
make status ZTD_ARGS='--master-key-file /absolute/path/master.key'
make down ZTD_ARGS='--master-key-file /absolute/path/master.key'
```

`make quickstart` accepts the key file through `QUICKSTART_ARGS`, as described in [Getting Started](getting_started.md).

### Binary building and version alignment

`make build` writes the host binary to `bin/ztd-<os>-<arch>` and copies it to `./ztd`. A running Operator keeps the binary it started with, so restart each Operator after a rebuild to run the new binary. A restart starts a new enrollment, which needs owner approval.

### Start the Gateway and Operators

The campaign topology uses separate Operator sessions: a Data Operator and an Inference Operator, each in its own working directory, plus the Gateway. Start the Gateway first:

```bash
make up
```

Start each Operator in its own terminal because `make operator` runs in the foreground (Ctrl+C stops it):

```bash
make operator OPERATOR_ARGS='--endpoint localhost --capabilities data --working-dir ~/.ztd-data'
make operator OPERATOR_ARGS='--endpoint localhost --capabilities inference --inference-ollama-endpoint http://192.168.1.2:11434 --working-dir ~/.ztd-inference'
```

For a single process that carries every capability, start the Gateway with its embedded Operator instead. The embedded Operator always includes Data. `make gw` enables `GW_CAPABILITIES` (default `data,inference,observer`), and setting `MODEL_STORAGE_ROOT` adds Provenance over that model store:

```bash
make gw MODEL_STORAGE_ROOT=~/.ollama/models GW_ARGS='--inference-ollama-endpoint http://192.168.1.2:11434'
```

Capabilities are fixed when the Gateway starts. Run `make down` first to change them.

The Gateway listens on loopback by default. To enroll Operators from other hosts, start it with `make up-public` or `make gw-public` and point those Operators at the Gateway host name or LAN address with `--endpoint`.

### 1. Enroll the owner

Enroll the owner identity:

- Interactive (browser WebAuthn passkey ceremony, enables the Console):
  ```bash
  ./ztd login web
  ```
- Headless (CLI-only owner, no Console sign-in):
  ```bash
  ./ztd login
  ```

### 2. Approve platform enrollments

Each Operator submits a platform enrollment request and waits for owner approval. List pending requests:

```bash
./ztd auth enroll pending
```

Approve the Data Operator request first, then the Inference Operator request, or deny any request that should not be admitted:

```bash
./ztd auth enroll approve <data-operator-request-id> --yes
./ztd auth enroll approve <inference-operator-request-id> --yes
```

The evaluation CLI dispatches inference with a separate host application identity, `ztd-eval`, and fails closed without it. Enroll it in a second terminal:

```bash
./ztd auth enroll app ztd-eval
```

Then approve its pending request from the first terminal:

```bash
./ztd auth enroll pending
./ztd auth enroll approve <request-id> --yes
```

### 3. Verify readiness

```bash
./ztd gw status
./ztd operator list
./ztd operator list --json
```

Expect two remote Operators (data and inference) plus the embedded Gateway Operator. The inference session must show an active status before any campaign starts. Session IDs change when an Operator restarts, so rediscover them with `./ztd operator list --json` after any restart.

### Stop or revoke an enrolled workload

Use the Operator session ID from `operator list` for a reversible process stop:

```bash
./ztd operator stop <operator-session-id> --reason "planned maintenance"
```

The command targets one active remote Operator owned by the authenticated user. It waits for the Operator's governed shutdown acknowledgement before the Gateway records `stopped`; the embedded Gateway Operator is never a valid target. The enrollment is not revoked, but the Operator identity is held in memory only, so the next `./ztd operator start` submits a new enrollment request that needs owner approval.

Use the completed platform enrollment request ID for permanent identity revocation:

```bash
./ztd auth enroll revoke <request-id> --reason "host retired" --yes
```

Operator revocation invalidates the Operator and companion CLI certificates, deactivates their sessions, marks the Operator `terminated`, and disconnects established pub/sub connections. The workload must submit a new enrollment request and receive owner approval before it can authenticate again.

### Provider-boundary Observer Operator (provider host; Windows example)

Deploy this on the machine that runs Ollama (e.g. `192.168.1.2`), not on the campaign host.

#### Prerequisites on the provider host

- `ztd.exe` built for Windows (`make build-windows` or copy `bin/ztd-windows-amd64.exe`).
- Outbound TCP to the campaign Gateway on **8080** and **8443**.
- `nvidia-smi` on PATH (GPU telemetry). Host RAM via Windows-equivalent collection in the observer collector.
- A hosts-file mapping so the Gateway certificate SAN matches, for example: `192.168.1.10 ztd.local`.

#### Start and enroll the Observer

In a dedicated working directory on the Windows provider host:

```powershell
.\ztd.exe operator start `
  --endpoint ztd.local `
  --capabilities observer `
  --provider-boundary-observer-id ztd-provider-boundary-observer
```

Do not add `inference` to `--capabilities`; this Operator is read-only hardware observation only.

From the campaign host owner CLI:

```bash
./ztd auth enroll pending
./ztd auth enroll approve <observer-request-id> --yes
```

After approval, confirm the observer appears in `./ztd operator list` with `observer` in its runtime config `capabilities`. The observer is a read-only witness and must not receive generic command execution.

### Storage-side Provenance Operator

Deploy this at the model storage site — the directory that holds Ollama manifests and blobs. On a typical Ollama host this is the same machine as the Observer; use a second enrolled operator session.

#### Prerequisites

- Same network and enrollment prerequisites as the Observer Operator (outbound TCP to Gateway **8080**/**8443**, `ztd.local` hosts mapping).
- Read access to the Ollama models directory (e.g. `C:\Users\<you>\.ollama\models` on Windows or `~/.ollama/models` on Linux).

#### Start and enroll the Provenance Operator

In a second dedicated working directory on the provider host:

```powershell
.\ztd.exe operator start `
  --endpoint ztd.local `
  --capabilities provenance `
  --provenance-operator-id ztd-model-provenance-operator `
  --model-storage-root "$env:USERPROFILE\.ollama\models"
```

From the campaign host owner CLI:

```bash
./ztd auth enroll pending
./ztd auth enroll approve <provenance-request-id> --yes
```

After approval, confirm the provenance operator appears in `./ztd operator list` with `provenance` in its runtime config `capabilities` and the correct `provenance_operator_model_storage_root`.

### Formation smoke (`ultra-efficient-speedster`)

Use this after the Inference Operator and both witness Operators (Observer + Provenance) are enrolled. It validates the three-model execution-topology path (Lite → Assistant → Primary) without scheduling a full campaign matrix.

#### Build formation inventory

Pull and freeze the catalog models:

```bash
./ztd eval models pull --formations
./ztd eval models freeze
```

#### Run formation smoke

```bash
./ztd eval formations smoke ultra-efficient-speedster
```

Expect `"passed": true` with three role entries (lite, assistant, primary), per-role attestation, generation throughput, and peak VRAM.

#### Run a formation campaign

```bash
./ztd eval campaigns create eval-formations-smoke --formations ultra-efficient-speedster

./ztd eval runs start eval-formations-smoke \
  --publish --daemon \
  --verify \
  --require-witness
```

### Campaign and run naming

| Purpose | Campaign ID | Run ID pattern | Model inventory |
| :--- | :--- | :--- | :--- |
| **Init campaign** (one model, single-model gate) | `eval-init-<variant_id>` | `<campaign-id>-<unix>` | `.ztd/eval/inventories/<campaign-id>.json` |
| **Mini smoke** (one-model end-to-end validation) | `eval-smoke-mini` | `eval-smoke-mini-<unix>` | `.ztd/eval/inventories/eval-smoke-mini.json` |
| **Full homogeneous run** | `eval-genesis-homogeneous` | `eval-genesis-homogeneous-<unix>` | `.ztd/eval/model-inventory.json` |

Single model init campaign:

```bash
./ztd eval models freeze
./ztd eval campaigns create eval-init-qwen3-4b qwen3:4b
./ztd eval runs start eval-init-qwen3-4b \
  --publish --daemon \
  --verify \
  --require-witness
```

Multi-model rollout queue:

```bash
./ztd eval rollout add --all
./ztd eval rollout run
```

### Mini smoke campaign workflow

#### Phase A — Public feed preparation

The Gateway owns the public feed, mirror (`8081` private ingest, `8082` public read/SSE), and evaluation explorer (`5173`) when started with `--public-spectator` (default).

#### Phase B — Create campaign and start run

```bash
./ztd eval campaigns create eval-smoke-mini qwen3:4b
./ztd eval runs start eval-smoke-mini --publish --daemon
```

#### Phase C — Monitor execution

`runs start` prints the run ID (`eval-smoke-mini-<unix>`); `./ztd eval runs list --campaign eval-smoke-mini` lists it again. Set `RUN_ID` to it:

```bash
RUN_ID=eval-smoke-mini-<unix>
./ztd eval runs show "$RUN_ID"
./ztd eval runs assignments "$RUN_ID" --failed
./ztd eval runs verify "$RUN_ID" --coverage
./ztd eval runs verify "$RUN_ID" --require-observation
```

### Stack lifecycle and cleanup

| Target | Behavior |
| :--- | :--- |
| `make up` | Starts the Gateway on this host with `ztd gw start` (flags from `GW_ARGS`). Sign in the owner with `./ztd login web` or `./ztd login`. |
| `make gw` | Starts the Gateway with its embedded Operator carrying capabilities from `GW_CAPABILITIES` (default `data,inference,observer`), then prints `ztd gw status`. Setting `MODEL_STORAGE_ROOT` adds the Provenance capability. |
| `make up-public` / `make gw-public` | Same as `make up` / `make gw` with the listeners bound to every interface (`--listen-host 0.0.0.0`) so Operators on other hosts can enroll. |
| `make operator` | Runs one outbound Operator in the foreground. Pass capability and endpoint flags in `OPERATOR_ARGS`. |
| `make status` | Prints `ztd gw status`: Gateway health, the Operator inventory, and pending enrollments. |
| `make down` | Stops Operators, then the Gateway (`ztd gw stop`). Runtime state in `.ztd/` is preserved. |

`make clean` removes build and test artifacts and the Go build cache while preserving `.ztd/` and the repository-root `ztd` binary. `./ztd gw clean` archives `.ztd/` to `.ztd-<MMDDHHMM>` and requires fresh owner and workload enrollment.

## Anti-patterns

- Leaving `--inference-ollama-endpoint` at default `http://127.0.0.1:11434` when the approved provider is remote.
- Rebinding campaign ID and digest per model instead of using dispatch-carried campaign authority.
- Running `./ztd gw clean` or `./ztd gw reset` expecting credentials and public mirror state to persist. Use `make down` to stop the stack without losing state.
- Starting Formation smoke without enrolling Observer and Provenance Operators first.
- Running `ztd eval runs publish` concurrently with `runs start --publish` or `runs resume --publish`.
- Granting command execution authority to Observer or Provenance Operators. These are read-only witness boundaries.

## Links out

- [Getting Started](getting_started.md) — First-run host build and bootstrap.
- [Evaluation Architecture](../architecture/evals.md) — Platform evaluation programs, witness Operator capabilities, evidence, and verification.
- [Model Provenance](../architecture/model-provenance.md) — Zero-trust weight attestation and chain of custody.
- [Build Operator](build_operator.md) — Build `ztd.exe` for the Windows Observer host.
- [Connect Operator to Gateway](connect_operator_to_gateway.md) — Enrollment protocol details.
- [Public Spectator Operations Guide](public_spectator.md) — Public mirror listeners, restore, and source transitions.
- [Console Architecture](../architecture/console.md) — The Gateway-served browser console.
- [Authentication and Identity](../architecture/auth.md) — mTLS, WebAuthn, PKI.
